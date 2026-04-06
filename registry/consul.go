package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fireflycore/sidecar-agent/model"
	"github.com/hashicorp/consul/api"
)

// Settings 描述 Consul 注册、发现与路由文档写入的最小参数。
type Settings struct {
	// Address 表示 Consul API 地址。
	Address string
	// Scheme 表示 Consul API 协议。
	Scheme string
	// Datacenter 表示默认数据中心。
	Datacenter string
	// RouteKVPrefix 表示路由文档写入前缀。
	RouteKVPrefix string
	// ClusterName 表示当前实例所属集群。
	ClusterName string
	// Zone 表示宿主机所在机房或可用区。
	Zone string
	// HostIP 表示写入 Consul 的实例地址。
	HostIP string
	// Env 表示当前 agent 所属环境。
	Env string
}

// agentAPI 抽象 Consul agent 服务注册能力，便于单元测试替换。
type agentAPI interface {
	// ServiceRegister 负责把服务实例写入 Consul agent。
	ServiceRegister(service *api.AgentServiceRegistration) error
	// ServiceDeregister 负责把服务实例从 Consul agent 移除。
	ServiceDeregister(serviceID string) error
	// EnableServiceMaintenance 负责把实例切到维护模式，避免继续接收流量。
	EnableServiceMaintenance(serviceID, reason string) error
}

// healthAPI 抽象 Consul 健康查询能力。
type healthAPI interface {
	// Service 返回指定服务的健康实例列表。
	Service(service, tag string, passingOnly bool, q *api.QueryOptions) ([]*api.ServiceEntry, *api.QueryMeta, error)
}

// catalogAPI 抽象 Consul catalog 服务名查询能力。
type catalogAPI interface {
	// Services 返回当前 catalog 中的服务索引。
	Services(q *api.QueryOptions) (map[string][]string, *api.QueryMeta, error)
}

// kvAPI 抽象 Consul KV 访问能力。
type kvAPI interface {
	// Get 读取指定 key 的当前值。
	Get(key string, q *api.QueryOptions) (*api.KVPair, *api.QueryMeta, error)
	// Put 写入指定 key 的值。
	Put(p *api.KVPair, q *api.WriteOptions) (*api.WriteMeta, error)
}

// Client 负责承接本地注册、摘流、注销，以及基于 Consul 的发现能力。
type Client struct {
	// settings 保存注册中心相关基础参数。
	settings Settings
	// agent 保存 Consul agent 侧接口。
	agent agentAPI
	// health 保存健康查询接口。
	health healthAPI
	// catalog 保存 catalog 服务索引接口。
	catalog catalogAPI
	// kv 保存 KV 路由文档接口。
	kv kvAPI
	// logger 统一输出结构化日志。
	logger *slog.Logger
	// metrics 负责累计本地生命周期指标。
	metrics metricsRecorder
	// mu 保护本机服务状态。
	mu sync.RWMutex
	// localServices 保存本机已知服务实例。
	localServices map[string]model.LocalService
}

// metricsRecorder 抽象 registry 需要的最小指标能力。
type metricsRecorder interface {
	// IncRegister 统计注册次数。
	IncRegister()
	// IncDrain 统计摘流次数。
	IncDrain()
	// IncDeregister 统计注销次数。
	IncDeregister()
}

// New 创建一个真实的 Consul 客户端。
func New(settings Settings, logger *slog.Logger, metrics metricsRecorder) (*Client, error) {
	// 先构造官方 Consul 客户端配置。
	cfg := api.DefaultConfig()
	// 设置地址，避免读默认环境变量造成歧义。
	cfg.Address = strings.TrimSpace(settings.Address)
	// 设置协议，支持 http 与 https。
	cfg.Scheme = strings.TrimSpace(settings.Scheme)
	// 设置默认数据中心。
	cfg.Datacenter = strings.TrimSpace(settings.Datacenter)
	// 创建底层 Consul client。
	client, err := api.NewClient(cfg)
	if err != nil {
		// 创建失败时直接返回。
		return nil, err
	}
	// 复用 WithClient 完成装配。
	return NewWithClient(settings, logger, metrics, client.Agent(), client.Health(), client.Catalog(), client.KV()), nil
}

// NewWithClient 允许在测试中注入假的 Consul 接口。
func NewWithClient(settings Settings, logger *slog.Logger, metrics metricsRecorder, agent agentAPI, health healthAPI, catalog catalogAPI, kv kvAPI) *Client {
	// 返回一个可直接使用的 registry 客户端。
	return &Client{
		settings:      settings,
		agent:         agent,
		health:        health,
		catalog:       catalog,
		kv:            kv,
		logger:        logger,
		metrics:       metrics,
		localServices: make(map[string]model.LocalService),
	}
}

// Register 处理本地业务服务的注册请求。
func (c *Client) Register(ctx context.Context, request model.RegisterRequest) (model.LocalService, error) {
	// 先校验请求字段，避免脏数据进入控制面。
	if err := request.Validate(); err != nil {
		return model.LocalService{}, err
	}
	// 为完整方法列表提取稳定路由前缀。
	routePrefixes := model.ExtractRoutePrefixes(request.Methods)
	// 计算稳定的路由文档引用路径。
	routeConfigRef := c.routeConfigRef(request.Env, request.Namespace, request.Name)
	// 生成将要写入 KV 的完整路由文档。
	routeDoc := model.RouteDocument{
		Service:    request.Name,
		Version:    request.Version,
		UpdatedAt:  time.Now().UTC(),
		Namespace:  request.Namespace,
		Port:       request.Port,
		Protocol:   request.Protocol,
		Prefixes:   routePrefixes,
		ExactPaths: model.UniqueSortedStrings(request.Methods),
	}
	// 若当前服务已有生效路由文档，则必须确保语义一致。
	if err := c.ensureRouteDocument(ctx, routeConfigRef, routeDoc); err != nil {
		return model.LocalService{}, err
	}
	// 生成 agent 统一维护的实例 ID。
	instanceID, err := newInstanceID()
	if err != nil {
		return model.LocalService{}, err
	}
	// 先把状态置为 Registered，形成完整的状态机轨迹。
	now := time.Now().UTC()
	service := model.LocalService{
		Request:        request,
		InstanceID:     instanceID,
		Address:        strings.TrimSpace(c.settings.HostIP),
		Zone:           strings.TrimSpace(c.settings.Zone),
		RoutePrefixes:  routePrefixes,
		RouteConfigRef: routeConfigRef,
		State:          model.StateRegistered,
		RegisteredAt:   now,
		UpdatedAt:      now,
	}
	// 把实例注册到 Consul agent。
	if err := c.agent.ServiceRegister(c.registrationFor(service)); err != nil {
		return model.LocalService{}, err
	}
	// 注册完成后切换到 Serving。
	service.State = model.StateServing
	// 更新时间戳，便于调试观察状态变化。
	service.UpdatedAt = time.Now().UTC()
	// 把本机实例写入内存状态。
	c.mu.Lock()
	c.localServices[c.localKey(service.Request.Name, service.Request.Port)] = service
	c.mu.Unlock()
	// 记录结构化日志，方便联调。
	if c.logger != nil {
		c.logger.Info("service registered",
			slog.String("service", service.Request.Name),
			slog.String("namespace", service.Request.Namespace),
			slog.String("env", service.Request.Env),
			slog.String("instance_id", service.InstanceID),
			slog.Int("port", service.Request.Port),
		)
	}
	// 注册成功后累计指标。
	if c.metrics != nil {
		c.metrics.IncRegister()
	}
	// 返回最终状态。
	return service, nil
}

// Drain 将本机实例切换到维护模式，并更新本地状态为 Draining。
func (c *Client) Drain(request model.DrainRequest) (model.LocalService, error) {
	// 先校验用户输入。
	if err := request.Validate(); err != nil {
		return model.LocalService{}, err
	}
	// 解析宽限期，供调试接口展示。
	gracePeriod, err := request.GracePeriod()
	if err != nil {
		return model.LocalService{}, err
	}
	// 读取当前本机实例状态。
	c.mu.Lock()
	defer c.mu.Unlock()
	service, ok := c.localServices[c.localKey(request.Name, request.Port)]
	if !ok {
		return model.LocalService{}, fmt.Errorf("local service not found: %s:%d", request.Name, request.Port)
	}
	// 先在 Consul 中启用维护模式，让实例立即退出健康集。
	if err := c.agent.EnableServiceMaintenance(service.InstanceID, "draining by sidecar-agent"); err != nil {
		return model.LocalService{}, err
	}
	// 记录摘流截止时间。
	deadline := time.Now().UTC().Add(gracePeriod)
	// 更新本地生命周期状态。
	service.State = model.StateDraining
	service.UpdatedAt = time.Now().UTC()
	service.DrainDeadline = &deadline
	// 把新状态写回内存。
	c.localServices[c.localKey(request.Name, request.Port)] = service
	// 输出状态日志。
	if c.logger != nil {
		c.logger.Info("service draining",
			slog.String("service", service.Request.Name),
			slog.String("instance_id", service.InstanceID),
			slog.String("deadline", deadline.Format(time.RFC3339)),
		)
	}
	// 摘流成功后累计指标。
	if c.metrics != nil {
		c.metrics.IncDrain()
	}
	// 返回最新状态。
	return service, nil
}

// Deregister 强制把本机实例从 Consul 注销。
func (c *Client) Deregister(request model.DeregisterRequest) (model.LocalService, error) {
	// 先校验用户输入。
	if err := request.Validate(); err != nil {
		return model.LocalService{}, err
	}
	// 读取并锁定本机状态。
	c.mu.Lock()
	defer c.mu.Unlock()
	service, ok := c.localServices[c.localKey(request.Name, request.Port)]
	if !ok {
		return model.LocalService{}, fmt.Errorf("local service not found: %s:%d", request.Name, request.Port)
	}
	// 先从 Consul 注销实例。
	if err := c.agent.ServiceDeregister(service.InstanceID); err != nil {
		return model.LocalService{}, err
	}
	// 更新本地状态为 Deregistered。
	service.State = model.StateDeregistered
	service.UpdatedAt = time.Now().UTC()
	service.DrainDeadline = nil
	// 把状态保留在内存里，便于调试查看最近一次注销结果。
	c.localServices[c.localKey(request.Name, request.Port)] = service
	// 输出注销日志。
	if c.logger != nil {
		c.logger.Info("service deregistered",
			slog.String("service", service.Request.Name),
			slog.String("instance_id", service.InstanceID),
		)
	}
	// 注销成功后累计指标。
	if c.metrics != nil {
		c.metrics.IncDeregister()
	}
	// 返回注销后的状态。
	return service, nil
}

// LocalServices 返回当前 agent 已知的本机服务列表。
func (c *Client) LocalServices() []model.LocalService {
	// 读取阶段使用读锁，避免影响热路径。
	c.mu.RLock()
	defer c.mu.RUnlock()
	// 预先创建结果切片。
	result := make([]model.LocalService, 0, len(c.localServices))
	// 复制所有服务状态，避免把内部 map 直接暴露出去。
	for _, service := range c.localServices {
		result = append(result, service)
	}
	// 为了输出稳定，对服务名和端口做排序。
	slices.SortFunc(result, func(left, right model.LocalService) int {
		if left.Request.Name == right.Request.Name {
			switch {
			case left.Request.Port < right.Request.Port:
				return -1
			case left.Request.Port > right.Request.Port:
				return 1
			default:
				return 0
			}
		}
		switch {
		case left.Request.Name < right.Request.Name:
			return -1
		case left.Request.Name > right.Request.Name:
			return 1
		default:
			return 0
		}
	})
	// 返回排序后的副本。
	return result
}

// Discover 从 Consul 中拉取当前环境可见的健康实例。
func (c *Client) Discover(ctx context.Context) ([]model.ServiceInstance, error) {
	// 先读取当前 catalog 里的服务索引。
	services, _, err := c.catalog.Services(&api.QueryOptions{
		// 透传调用方上下文，便于上层统一控制超时。
		AllowStale: true,
	})
	if err != nil {
		return nil, err
	}
	// 预先准备结果切片。
	instances := make([]model.ServiceInstance, 0, len(services))
	// 遍历所有服务名，再查询对应健康实例。
	for serviceName := range services {
		// 对每个服务执行 passingOnly 健康查询。
		entries, _, err := c.health.Service(serviceName, "", true, &api.QueryOptions{
			AllowStale: true,
		})
		if err != nil {
			return nil, err
		}
		// 把 Consul 返回值转换成内部模型。
		for _, entry := range entries {
			instance, ok := c.serviceInstanceFromEntry(entry)
			if !ok {
				continue
			}
			// 仅保留当前 agent 所属环境的数据，避免跨环境串流量。
			if strings.TrimSpace(instance.Env) != strings.TrimSpace(c.settings.Env) {
				continue
			}
			instances = append(instances, instance)
		}
	}
	// 按服务名、命名空间、地址排序，确保快照构建结果稳定。
	slices.SortFunc(instances, func(left, right model.ServiceInstance) int {
		leftKey := fmt.Sprintf("%s|%s|%s|%d", left.Name, left.Namespace, left.Address, left.Port)
		rightKey := fmt.Sprintf("%s|%s|%s|%d", right.Name, right.Namespace, right.Address, right.Port)
		switch {
		case leftKey < rightKey:
			return -1
		case leftKey > rightKey:
			return 1
		default:
			return 0
		}
	})
	// 返回当前环境下的所有健康实例。
	return instances, nil
}

// routeConfigRef 生成稳定的完整路由文档引用。
func (c *Client) routeConfigRef(env, namespace, service string) string {
	// 把 prefix 首尾多余斜杠移除，避免产生双斜杠路径。
	prefix := strings.Trim(strings.TrimSpace(c.settings.RouteKVPrefix), "/")
	// 按文档固定输出 routes/{env}/{namespace}/{service}/current。
	return fmt.Sprintf("%s/%s/%s/%s/current", prefix, strings.TrimSpace(env), strings.TrimSpace(namespace), strings.TrimSpace(service))
}

// ensureRouteDocument 校验并写入完整路由文档。
func (c *Client) ensureRouteDocument(ctx context.Context, key string, doc model.RouteDocument) error {
	// 先读取当前已存在的路由文档。
	existingPair, _, err := c.kv.Get(key, &api.QueryOptions{})
	if err != nil {
		return err
	}
	// 如果已有值，则先校验是否冲突。
	if existingPair != nil && len(existingPair.Value) > 0 {
		var existing model.RouteDocument
		if err := json.Unmarshal(existingPair.Value, &existing); err != nil {
			return err
		}
		if !existing.Equal(doc) {
			return fmt.Errorf("route document conflict on %s", key)
		}
	}
	// 把路由文档编码成稳定 JSON。
	payload, err := doc.Marshal()
	if err != nil {
		return err
	}
	// 持久化到 Consul KV，供 gateway 和发布系统读取。
	_, err = c.kv.Put(&api.KVPair{
		Key:   key,
		Value: payload,
	}, &api.WriteOptions{})
	// 返回写入结果。
	return err
}

// registrationFor 把内部服务模型转换为 Consul 注册模型。
func (c *Client) registrationFor(service model.LocalService) *api.AgentServiceRegistration {
	// 把所有元数据转换成字符串 map，严格对齐 v2.1 文档约定。
	meta := map[string]string{
		"app_id":           service.Request.AppID,
		"app_name":         service.Request.AppName,
		"namespace":        service.Request.Namespace,
		"dns":              service.Request.DNS,
		"env":              service.Request.Env,
		"zone":             service.Zone,
		"weight":           fmt.Sprintf("%d", service.Request.Weight),
		"route_prefixes":   strings.Join(service.RoutePrefixes, ","),
		"version":          service.Request.Version,
		"protocol":         service.Request.Protocol,
		"kernel_language":  service.Request.Kernel.Language,
		"kernel_version":   service.Request.Kernel.Version,
		"route_config_ref": service.RouteConfigRef,
		"instance_id":      service.InstanceID,
		"run_date":         service.RegisteredAt.Format(time.RFC3339),
	}
	// 构造最小服务注册对象。
	return &api.AgentServiceRegistration{
		ID:      service.InstanceID,
		Name:    service.Request.Name,
		Address: service.Address,
		Port:    service.Request.Port,
		Tags: []string{
			"cluster=" + strings.TrimSpace(c.settings.ClusterName),
		},
		Meta: meta,
		Check: &api.AgentServiceCheck{
			// 当前阶段使用 TCP 检查，避免强依赖业务服务接入 gRPC health。
			TCP: fmt.Sprintf("%s:%d", service.Address, service.Request.Port),
			// 健康检查间隔对齐最小骨架需求。
			Interval: "10s",
			// 失败后 30 秒自动注销，避免僵尸实例长期残留。
			DeregisterCriticalServiceAfter: "30s",
		},
	}
}

// serviceInstanceFromEntry 把 Consul 服务实例条目转换为内部模型。
func (c *Client) serviceInstanceFromEntry(entry *api.ServiceEntry) (model.ServiceInstance, bool) {
	// entry 或 service 为空时直接忽略。
	if entry == nil || entry.Service == nil {
		return model.ServiceInstance{}, false
	}
	// 优先读取服务级地址，缺省时回退到节点地址。
	address := strings.TrimSpace(entry.Service.Address)
	if address == "" && entry.Node != nil {
		address = strings.TrimSpace(entry.Node.Address)
	}
	// 地址或端口缺失时无法形成可用 endpoint。
	if address == "" || entry.Service.Port <= 0 {
		return model.ServiceInstance{}, false
	}
	// 读取注册元数据。
	meta := entry.Service.Meta
	// 构造内部统一实例模型。
	return model.ServiceInstance{
		InstanceID:     meta["instance_id"],
		Name:           entry.Service.Service,
		Namespace:      meta["namespace"],
		DNS:            meta["dns"],
		Env:            meta["env"],
		Zone:           meta["zone"],
		Version:        meta["version"],
		Protocol:       meta["protocol"],
		Address:        address,
		Port:           entry.Service.Port,
		Weight:         parsePositiveInt(meta["weight"], 100),
		Cluster:        clusterFromTags(entry.Service.Tags),
		RouteConfigRef: meta["route_config_ref"],
		RoutePrefixes:  model.UniqueSortedStrings(strings.Split(meta["route_prefixes"], ",")),
	}, true
}

// localKey 生成本机服务索引键。
func (c *Client) localKey(name string, port int) string {
	// 服务名加端口足以唯一定位当前宿主机上的业务进程。
	return fmt.Sprintf("%s:%d", strings.TrimSpace(name), port)
}

// clusterFromTags 读取 Consul tags 中的 cluster 信息。
func clusterFromTags(tags []string) string {
	// 遍历所有 tag，寻找 cluster= 前缀。
	for _, tag := range tags {
		if strings.HasPrefix(strings.TrimSpace(tag), "cluster=") {
			return strings.TrimPrefix(strings.TrimSpace(tag), "cluster=")
		}
	}
	// 缺省时返回空字符串。
	return ""
}

// parsePositiveInt 把字符串整数安全转换成正数。
func parsePositiveInt(raw string, fallback int) int {
	// 裁剪空白后做最小解析。
	var value int
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &value); err != nil || value <= 0 {
		return fallback
	}
	// 解析成功时返回结果。
	return value
}

// newInstanceID 生成一个满足 uuid v7 时间排序特征的最小实例 ID。
func newInstanceID() (string, error) {
	// 先分配 16 字节缓冲区。
	var buffer [16]byte
	// 读取随机字节作为熵源。
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", err
	}
	// 取当前毫秒时间戳，写入高位 48 bit。
	millis := uint64(time.Now().UTC().UnixMilli())
	buffer[0] = byte(millis >> 40)
	buffer[1] = byte(millis >> 32)
	buffer[2] = byte(millis >> 24)
	buffer[3] = byte(millis >> 16)
	buffer[4] = byte(millis >> 8)
	buffer[5] = byte(millis)
	// 设置版本号为 7。
	buffer[6] = (buffer[6] & 0x0f) | 0x70
	// 设置 RFC 4122 variant 位。
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	// 编码成标准 UUID 字符串。
	encoded := hex.EncodeToString(buffer[:])
	// 按 8-4-4-4-12 规则拼装输出。
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]), nil
}

// EnsureUsable 校验 registry 当前装配是否完整。
func (c *Client) EnsureUsable() error {
	// 所有核心接口都必须存在。
	switch {
	case c.agent == nil:
		return errors.New("consul agent api is required")
	case c.health == nil:
		return errors.New("consul health api is required")
	case c.catalog == nil:
		return errors.New("consul catalog api is required")
	case c.kv == nil:
		return errors.New("consul kv api is required")
	default:
		return nil
	}
}
