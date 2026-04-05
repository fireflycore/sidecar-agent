package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// ServiceState 表示本机服务实例在 agent 内部的生命周期状态。
type ServiceState string

const (
	// StateInit 表示服务尚未进入注册流程。
	StateInit ServiceState = "Init"
	// StateRegistered 表示 agent 已受理注册并准备写入注册中心。
	StateRegistered ServiceState = "Registered"
	// StateServing 表示实例已经可以接收新流量。
	StateServing ServiceState = "Serving"
	// StateDraining 表示实例停止接收新流量，只等待存量请求结束。
	StateDraining ServiceState = "Draining"
	// StateDeregistered 表示实例已经从注册中心移除。
	StateDeregistered ServiceState = "Deregistered"
)

// KernelInfo 保存业务服务随注册请求上报的核心库信息。
type KernelInfo struct {
	// Language 表示核心库或运行时语言。
	Language string `json:"language"`
	// Version 表示核心库版本。
	Version string `json:"version"`
}

// RegisterRequest 对齐 v2.1 文档中的 /register 请求结构。
type RegisterRequest struct {
	// AppID 表示应用唯一标识。
	AppID string `json:"app_id"`
	// AppName 表示应用名称。
	AppName string `json:"app_name"`
	// Name 表示逻辑服务名。
	Name string `json:"name"`
	// Namespace 表示命名空间。
	Namespace string `json:"namespace"`
	// Port 表示业务服务监听端口。
	Port int `json:"port"`
	// DNS 表示统一服务域名。
	DNS string `json:"dns"`
	// Env 表示开发、测试、生产等环境。
	Env string `json:"env"`
	// Weight 表示实例流量权重。
	Weight int `json:"weight"`
	// Protocol 表示服务协议。
	Protocol string `json:"protocol"`
	// Kernel 表示核心库信息。
	Kernel KernelInfo `json:"kernel"`
	// Methods 表示服务完整方法列表。
	Methods []string `json:"methods"`
	// Version 表示业务版本号。
	Version string `json:"version"`
}

// Validate 校验 register 请求中的必填字段。
func (r RegisterRequest) Validate() error {
	// 先逐项校验字符串字段，避免不完整注册污染控制面状态。
	required := map[string]string{
		"app_id":          r.AppID,
		"app_name":        r.AppName,
		"name":            r.Name,
		"namespace":       r.Namespace,
		"dns":             r.DNS,
		"env":             r.Env,
		"protocol":        r.Protocol,
		"version":         r.Version,
		"kernel.language": r.Kernel.Language,
		"kernel.version":  r.Kernel.Version,
	}
	// 逐个校验字段值是否为空白字符串。
	for field, value := range required {
		// 对字符串进行去空白处理，保证前后空格不会绕过校验。
		if strings.TrimSpace(value) == "" {
			// 返回明确字段名，便于业务服务快速修正上报数据。
			return fmt.Errorf("%s is required", field)
		}
	}
	// 端口必须是合法的 TCP 端口。
	if r.Port <= 0 || r.Port > 65535 {
		// 端口非法时直接拒绝注册。
		return fmt.Errorf("port is invalid: %d", r.Port)
	}
	// 权重必须是正整数，避免生成不可预测的负载策略。
	if r.Weight <= 0 {
		// 权重为零或负数都会导致调度语义失真。
		return errors.New("weight must be greater than zero")
	}
	// methods 至少需要一项，才能产出 route_prefixes 与 route_config_ref 对应的完整路由文档。
	if len(r.Methods) == 0 {
		// 空方法列表会让 gateway 和发布系统失去路由索引基础。
		return errors.New("methods must not be empty")
	}
	// 遍历校验每个方法路径是否合法。
	for _, method := range r.Methods {
		// 标准 gRPC 路径必须以 / 开头。
		if !strings.HasPrefix(strings.TrimSpace(method), "/") {
			// 返回具体方法，便于定位问题。
			return fmt.Errorf("method is invalid: %s", method)
		}
	}
	// 所有校验通过后返回 nil。
	return nil
}

// DrainRequest 对齐 v2.1 文档中的 /drain 请求结构。
type DrainRequest struct {
	// Name 表示待摘流的逻辑服务名。
	Name string `json:"name"`
	// Port 表示待摘流实例端口。
	Port int `json:"port"`
	// GracePeriodRaw 保留原始 JSON 字段，便于解析时兼容字符串格式。
	GracePeriodRaw string `json:"grace_period"`
}

// GracePeriod 将用户输入的宽限期转换为 time.Duration。
func (r DrainRequest) GracePeriod() (time.Duration, error) {
	// 未传值时回退到文档推荐的 20 秒宽限期。
	if strings.TrimSpace(r.GracePeriodRaw) == "" {
		// 返回默认宽限期，避免业务侧漏填时出现零秒摘流。
		return 20 * time.Second, nil
	}
	// 使用标准库解析类似 20s 的时长格式。
	value, err := time.ParseDuration(strings.TrimSpace(r.GracePeriodRaw))
	if err != nil {
		// 解析失败时把原始错误继续向上返回。
		return 0, err
	}
	// 宽限期必须大于零。
	if value <= 0 {
		// 非正数宽限期不符合优雅摘流语义。
		return 0, errors.New("grace_period must be greater than zero")
	}
	// 返回解析后的时长。
	return value, nil
}

// Validate 校验 drain 请求关键字段。
func (r DrainRequest) Validate() error {
	// 服务名不能为空。
	if strings.TrimSpace(r.Name) == "" {
		// 缺少服务名时无法定位本机实例。
		return errors.New("name is required")
	}
	// 端口必须有效。
	if r.Port <= 0 || r.Port > 65535 {
		// 非法端口直接拒绝。
		return fmt.Errorf("port is invalid: %d", r.Port)
	}
	// 触发一次宽限期解析，确保输入可用。
	_, err := r.GracePeriod()
	// 直接返回解析结果。
	return err
}

// DeregisterRequest 对齐 /deregister 请求结构。
type DeregisterRequest struct {
	// Name 表示逻辑服务名。
	Name string `json:"name"`
	// Port 表示服务端口。
	Port int `json:"port"`
}

// Validate 校验强制注销请求。
func (r DeregisterRequest) Validate() error {
	// 服务名不能为空。
	if strings.TrimSpace(r.Name) == "" {
		// 缺少服务名时无法锁定目标实例。
		return errors.New("name is required")
	}
	// 端口必须合法。
	if r.Port <= 0 || r.Port > 65535 {
		// 端口不合法时直接拒绝处理。
		return fmt.Errorf("port is invalid: %d", r.Port)
	}
	// 输入合法时返回 nil。
	return nil
}

// RouteDocument 表示独立存放在 Consul KV 的完整路由文档。
type RouteDocument struct {
	// Service 表示服务名。
	Service string `json:"service"`
	// Version 表示当前生效的业务版本。
	Version string `json:"version"`
	// UpdatedAt 表示路由文档最后更新时间。
	UpdatedAt time.Time `json:"updated_at"`
	// Namespace 表示命名空间。
	Namespace string `json:"namespace"`
	// Port 表示目标服务端口。
	Port int `json:"port"`
	// Protocol 表示服务协议。
	Protocol string `json:"protocol"`
	// Prefixes 表示服务级稳定前缀集合。
	Prefixes []string `json:"prefixes"`
	// ExactPaths 表示需要精确匹配的方法集合。
	ExactPaths []string `json:"exact_paths"`
}

// Canonicalize 对路由文档做排序与去重，保证比较结果稳定。
func (d RouteDocument) Canonicalize() RouteDocument {
	// 复制一份 prefixes，避免原始切片被原地改写。
	d.Prefixes = UniqueSortedStrings(d.Prefixes)
	// 同样复制并排序 exact paths。
	d.ExactPaths = UniqueSortedStrings(d.ExactPaths)
	// 返回归一化后的副本。
	return d
}

// Equal 判断两份路由文档的核心路由含义是否一致。
func (d RouteDocument) Equal(other RouteDocument) bool {
	// 先把两边都归一化，避免比较受到顺序影响。
	left := d.Canonicalize()
	// 对右侧也执行同样处理。
	right := other.Canonicalize()
	// 使用切片深比较保证精确一致。
	return left.Service == right.Service &&
		left.Version == right.Version &&
		left.Namespace == right.Namespace &&
		left.Port == right.Port &&
		left.Protocol == right.Protocol &&
		slices.Equal(left.Prefixes, right.Prefixes) &&
		slices.Equal(left.ExactPaths, right.ExactPaths)
}

// Marshal 将路由文档编码为稳定 JSON。
func (d RouteDocument) Marshal() ([]byte, error) {
	// 统一使用归一化后的结构进行编码，方便比对和审计。
	return json.Marshal(d.Canonicalize())
}

// LocalService 表示 agent 当前管理的本机服务实例状态。
type LocalService struct {
	// Request 保存业务服务原始注册请求。
	Request RegisterRequest `json:"request"`
	// InstanceID 表示 agent 统一生成的实例标识。
	InstanceID string `json:"instance_id"`
	// Address 表示写入 Consul 的实例地址。
	Address string `json:"address"`
	// Zone 表示 agent 自动注入的机房或可用区。
	Zone string `json:"zone"`
	// RoutePrefixes 表示稳定前缀索引。
	RoutePrefixes []string `json:"route_prefixes"`
	// RouteConfigRef 表示完整路由文档引用。
	RouteConfigRef string `json:"route_config_ref"`
	// State 表示当前生命周期状态。
	State ServiceState `json:"state"`
	// RegisteredAt 表示首次注册时间。
	RegisteredAt time.Time `json:"registered_at"`
	// UpdatedAt 表示最近一次状态更新时间。
	UpdatedAt time.Time `json:"updated_at"`
	// DrainDeadline 表示当前摘流宽限截止时间。
	DrainDeadline *time.Time `json:"drain_deadline,omitempty"`
}

// ServiceInstance 表示从 Consul 发现到的健康实例。
type ServiceInstance struct {
	// InstanceID 表示唯一实例标识。
	InstanceID string `json:"instance_id"`
	// Name 表示逻辑服务名。
	Name string `json:"name"`
	// Namespace 表示命名空间。
	Namespace string `json:"namespace"`
	// DNS 表示统一服务域名。
	DNS string `json:"dns"`
	// Env 表示环境。
	Env string `json:"env"`
	// Zone 表示机房或可用区。
	Zone string `json:"zone"`
	// Version 表示业务版本。
	Version string `json:"version"`
	// Protocol 表示服务协议。
	Protocol string `json:"protocol"`
	// Address 表示实例地址。
	Address string `json:"address"`
	// Port 表示实例端口。
	Port int `json:"port"`
	// Weight 表示实例权重。
	Weight int `json:"weight"`
	// Cluster 表示实例所属集群。
	Cluster string `json:"cluster"`
	// RouteConfigRef 表示完整路由文档引用。
	RouteConfigRef string `json:"route_config_ref"`
	// RoutePrefixes 表示稳定路由前缀集合。
	RoutePrefixes []string `json:"route_prefixes"`
}

// UniqueSortedStrings 返回去重且升序排序后的字符串切片。
func UniqueSortedStrings(values []string) []string {
	// 使用 map 做一次去重。
	unique := make(map[string]struct{}, len(values))
	// 逐项归一化字符串内容。
	for _, value := range values {
		// 去掉多余空白，避免同义值重复。
		trimmed := strings.TrimSpace(value)
		// 空字符串没有意义，直接忽略。
		if trimmed == "" {
			continue
		}
		// 写入去重集合。
		unique[trimmed] = struct{}{}
	}
	// 准备稳定输出切片。
	result := make([]string, 0, len(unique))
	// 把集合中的值搬运到切片。
	for value := range unique {
		// 追加当前值。
		result = append(result, value)
	}
	// 排序后再返回，保证日志、KV 与测试结果稳定。
	sort.Strings(result)
	// 返回去重后的切片副本。
	return result
}

// ExtractRoutePrefixes 从完整方法列表中提取稳定的服务级前缀。
func ExtractRoutePrefixes(methods []string) []string {
	// 预先准备结果切片。
	prefixes := make([]string, 0, len(methods))
	// 逐项分析方法路径。
	for _, method := range methods {
		// 先做去空白处理。
		trimmed := strings.TrimSpace(method)
		// 非法路径直接跳过，真正的合法性由注册校验兜底。
		if !strings.HasPrefix(trimmed, "/") {
			continue
		}
		// 找到最后一个 /，保留 service 级前缀。
		index := strings.LastIndex(trimmed, "/")
		// 缺少方法段时也直接跳过。
		if index <= 0 {
			continue
		}
		// 把 service 前缀连同尾部 / 一起保留。
		prefixes = append(prefixes, trimmed[:index+1])
	}
	// 返回去重排序后的结果。
	return UniqueSortedStrings(prefixes)
}
