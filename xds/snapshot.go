package xds

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	clusterpb "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointpb "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerpb "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routepb "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	routerpb "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcmpb "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	cachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/fireflycore/sidecar-agent/model"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"
)

// BuilderSettings 描述快照构建所需的静态参数。
type BuilderSettings struct {
	// NodeID 表示 Envoy 拉取快照时使用的节点标识。
	NodeID string
	// ListenerAddress 表示共享 Envoy 的监听地址。
	ListenerAddress string
	// RouteName 表示 RDS 资源名称。
	RouteName string
	// ClusterNamePrefix 表示 cluster 资源名前缀。
	ClusterNamePrefix string
	// LocalCluster 表示当前 agent 所属集群名。
	LocalCluster string
	// DefaultTimeout 表示默认请求超时。
	DefaultTimeout time.Duration
	// RetryAttempts 表示默认重试次数。
	RetryAttempts uint32
	// PerTryTimeout 表示单次重试超时。
	PerTryTimeout time.Duration
	// MaxConnections 表示连接数熔断阈值。
	MaxConnections uint32
	// MaxPendingRequests 表示排队请求熔断阈值。
	MaxPendingRequests uint32
	// MaxRequests 表示请求数熔断阈值。
	MaxRequests uint32
	// Consecutive5XX 表示异常剔除阈值。
	Consecutive5XX uint32
	// BaseEjectionTime 表示异常剔除时长。
	BaseEjectionTime time.Duration
	// MaxEjectionPercent 表示最大剔除比例。
	MaxEjectionPercent uint32
}

// Summary 保存当前已发布 xDS 快照的调试摘要。
type Summary struct {
	// Version 表示当前快照版本号。
	Version uint64 `json:"version"`
	// ListenerName 表示当前 listener 名称。
	ListenerName string `json:"listener_name"`
	// RouteName 表示当前 route 配置名称。
	RouteName string `json:"route_name"`
	// Services 表示当前快照覆盖的逻辑服务列表。
	Services []string `json:"services"`
	// ClusterCount 表示 cluster 数量。
	ClusterCount int `json:"cluster_count"`
	// EndpointCount 表示 endpoint 总数。
	EndpointCount int `json:"endpoint_count"`
	// PublishedAt 表示最近一次发布时间。
	PublishedAt time.Time `json:"published_at"`
}

// Builder 负责把 Consul 实例集合转换成 Envoy xDS 快照。
type Builder struct {
	// settings 保存静态构建参数。
	settings BuilderSettings
	// version 用于生成单调递增的快照版本。
	version atomic.Uint64
	// mu 保护 summary 读取。
	mu sync.RWMutex
	// summary 保存当前已发布快照的摘要。
	summary Summary
}

// NewBuilder 创建一个新的快照构建器。
func NewBuilder(settings BuilderSettings) *Builder {
	// 若未显式指定前缀，则使用默认 cluster 前缀。
	if strings.TrimSpace(settings.ClusterNamePrefix) == "" {
		settings.ClusterNamePrefix = "cluster"
	}
	// 返回构建器实例。
	return &Builder{
		settings: settings,
		summary: Summary{
			ListenerName: "local-listener",
			RouteName:    settings.RouteName,
		},
	}
}

// Build 基于当前健康实例生成一版完整快照。
func (b *Builder) Build(instances []model.ServiceInstance) (*cachev3.Snapshot, Summary, error) {
	// 先按服务分组，形成 cluster 与 virtual host 的最小单元。
	grouped := groupInstances(instances)
	// 预先准备资源切片。
	clusters := make([]cachetypes.Resource, 0, len(grouped))
	endpoints := make([]cachetypes.Resource, 0, len(grouped))
	virtualHosts := make([]*routepb.VirtualHost, 0, len(grouped))
	serviceNames := make([]string, 0, len(grouped))
	endpointCount := 0
	// 遍历每个服务分组构造 CDS、EDS 与 RDS 资源。
	for _, serviceGroup := range grouped {
		// 生成当前服务对应的 cluster 名称。
		clusterName := b.clusterName(serviceGroup.Name, serviceGroup.Namespace, serviceGroup.Port)
		// 生成 cluster 资源。
		cluster := b.buildCluster(clusterName)
		clusters = append(clusters, cluster)
		// 生成 endpoint 资源。
		loadAssignment, count := b.buildLoadAssignment(clusterName, serviceGroup.Instances)
		endpoints = append(endpoints, loadAssignment)
		endpointCount += count
		// 生成按 authority 匹配的 virtual host。
		virtualHosts = append(virtualHosts, b.buildVirtualHost(clusterName, serviceGroup.DNS, serviceGroup.Port))
		// 收集服务名，供调试摘要展示。
		serviceNames = append(serviceNames, fmt.Sprintf("%s.%s", serviceGroup.Name, serviceGroup.Namespace))
	}
	// listener 始终只有一个，负责接住本机所有流量。
	listener, err := b.buildListener()
	if err != nil {
		return nil, Summary{}, err
	}
	// route config 统一承载所有服务的 authority 路由。
	routeConfig := &routepb.RouteConfiguration{
		Name:         b.settings.RouteName,
		VirtualHosts: virtualHosts,
	}
	// 生成下一版快照版本。
	version := b.version.Add(1)
	// 使用 go-control-plane 官方快照结构承载资源。
	snapshot, err := cachev3.NewSnapshot(fmt.Sprintf("%d", version), map[resourcev3.Type][]cachetypes.Resource{
		resourcev3.EndpointType: endpoints,
		resourcev3.ClusterType:  clusters,
		resourcev3.RouteType:    []cachetypes.Resource{routeConfig},
		resourcev3.ListenerType: []cachetypes.Resource{listener},
	})
	if err != nil {
		return nil, Summary{}, err
	}
	// 使用官方一致性校验保证 cluster、route、listener 引用闭合。
	if err := snapshot.Consistent(); err != nil {
		return nil, Summary{}, err
	}
	// 排序服务列表，保证调试输出稳定。
	slices.Sort(serviceNames)
	// 更新摘要。
	summary := Summary{
		Version:       version,
		ListenerName:  "local-listener",
		RouteName:     b.settings.RouteName,
		Services:      serviceNames,
		ClusterCount:  len(clusters),
		EndpointCount: endpointCount,
		PublishedAt:   time.Now().UTC(),
	}
	// 保存最新摘要。
	b.mu.Lock()
	b.summary = summary
	b.mu.Unlock()
	// 返回快照与摘要。
	return snapshot, summary, nil
}

// Summary 返回当前最新快照摘要。
func (b *Builder) Summary() Summary {
	// 使用读锁保证并发安全。
	b.mu.RLock()
	defer b.mu.RUnlock()
	// 返回摘要副本。
	return b.summary
}

// buildListener 构造共享 Envoy 的统一入口 listener。
func (b *Builder) buildListener() (*listenerpb.Listener, error) {
	// 把监听地址拆解成 host 与 port。
	host, port, err := net.SplitHostPort(b.settings.ListenerAddress)
	if err != nil {
		return nil, err
	}
	// 创建基于 RDS 的 HCM 配置。
	manager := &hcmpb.HttpConnectionManager{
		CodecType:  hcmpb.HttpConnectionManager_AUTO,
		StatPrefix: "ingress_http",
		RouteSpecifier: &hcmpb.HttpConnectionManager_Rds{
			Rds: &hcmpb.Rds{
				ConfigSource: &corepb.ConfigSource{
					ResourceApiVersion: corepb.ApiVersion_V3,
					ConfigSourceSpecifier: &corepb.ConfigSource_Ads{
						Ads: &corepb.AggregatedConfigSource{},
					},
				},
				RouteConfigName: b.settings.RouteName,
			},
		},
		HttpFilters: []*hcmpb.HttpFilter{
			{
				Name: "envoy.filters.http.router",
				ConfigType: &hcmpb.HttpFilter_TypedConfig{
					TypedConfig: mustAny(&routerpb.Router{}),
				},
			},
		},
	}
	// 返回 listener 定义。
	return &listenerpb.Listener{
		Name: "local-listener",
		Address: &corepb.Address{
			Address: &corepb.Address_SocketAddress{
				SocketAddress: &corepb.SocketAddress{
					Address: host,
					PortSpecifier: &corepb.SocketAddress_PortValue{
						PortValue: uint32(parsePort(port)),
					},
				},
			},
		},
		FilterChains: []*listenerpb.FilterChain{
			{
				Filters: []*listenerpb.Filter{
					{
						Name: "envoy.filters.network.http_connection_manager",
						ConfigType: &listenerpb.Filter_TypedConfig{
							TypedConfig: mustAny(manager),
						},
					},
				},
			},
		},
	}, nil
}

// buildVirtualHost 为单个逻辑服务生成 authority 路由。
func (b *Builder) buildVirtualHost(clusterName, dns string, port int) *routepb.VirtualHost {
	// 构造 gRPC dial 常见的 authority 形式。
	fullAuthority := fmt.Sprintf("%s:%d", dns, port)
	// 生成一个默认匹配 / 的路由规则。
	return &routepb.VirtualHost{
		Name:    clusterName,
		Domains: []string{dns, fullAuthority},
		Routes: []*routepb.Route{
			{
				Match: &routepb.RouteMatch{
					PathSpecifier: &routepb.RouteMatch_Prefix{
						Prefix: "/",
					},
				},
				Action: &routepb.Route_Route{
					Route: &routepb.RouteAction{
						ClusterSpecifier: &routepb.RouteAction_Cluster{
							Cluster: clusterName,
						},
						Timeout: durationpb.New(b.settings.DefaultTimeout),
						RetryPolicy: &routepb.RetryPolicy{
							RetryOn:       "connect-failure,5xx,reset",
							NumRetries:    wrapperspb.UInt32(b.settings.RetryAttempts),
							PerTryTimeout: durationpb.New(b.settings.PerTryTimeout),
						},
					},
				},
			},
		},
	}
}

// buildCluster 构造共享 Envoy 需要的 cluster 资源。
func (b *Builder) buildCluster(clusterName string) *clusterpb.Cluster {
	// 当前阶段统一使用 EDS 动态发现 endpoint。
	return &clusterpb.Cluster{
		Name:                 clusterName,
		ClusterDiscoveryType: &clusterpb.Cluster_Type{Type: clusterpb.Cluster_EDS},
		ConnectTimeout:       durationpb.New(b.settings.DefaultTimeout),
		EdsClusterConfig: &clusterpb.Cluster_EdsClusterConfig{
			EdsConfig: &corepb.ConfigSource{
				ResourceApiVersion: corepb.ApiVersion_V3,
				ConfigSourceSpecifier: &corepb.ConfigSource_Ads{
					Ads: &corepb.AggregatedConfigSource{},
				},
			},
		},
		CircuitBreakers: &clusterpb.CircuitBreakers{
			Thresholds: []*clusterpb.CircuitBreakers_Thresholds{
				{
					MaxConnections:     wrapperspb.UInt32(b.settings.MaxConnections),
					MaxPendingRequests: wrapperspb.UInt32(b.settings.MaxPendingRequests),
					MaxRequests:        wrapperspb.UInt32(b.settings.MaxRequests),
				},
			},
		},
		OutlierDetection: &clusterpb.OutlierDetection{
			Consecutive_5Xx:    wrapperspb.UInt32(b.settings.Consecutive5XX),
			BaseEjectionTime:   durationpb.New(b.settings.BaseEjectionTime),
			MaxEjectionPercent: wrapperspb.UInt32(b.settings.MaxEjectionPercent),
		},
		LbPolicy: clusterpb.Cluster_ROUND_ROBIN,
	}
}

// buildLoadAssignment 构造一个 cluster 对应的 endpoint 集合。
func (b *Builder) buildLoadAssignment(clusterName string, instances []model.ServiceInstance) (*endpointpb.ClusterLoadAssignment, int) {
	// 先按 locality priority 分桶，实现同集群优先。
	localityBuckets := make(map[string]*endpointpb.LocalityLbEndpoints)
	// 统计总 endpoint 数量，供摘要输出。
	count := 0
	// 遍历实例列表，逐个转换为 Envoy endpoint。
	for _, instance := range instances {
		priority := uint32(1)
		if strings.TrimSpace(instance.Cluster) == strings.TrimSpace(b.settings.LocalCluster) {
			priority = 0
		}
		key := fmt.Sprintf("%s|%d", instance.Zone, priority)
		bucket, ok := localityBuckets[key]
		if !ok {
			bucket = &endpointpb.LocalityLbEndpoints{
				Locality: &corepb.Locality{
					Zone: instance.Zone,
				},
				Priority: priority,
			}
			localityBuckets[key] = bucket
		}
		bucket.LbEndpoints = append(bucket.LbEndpoints, &endpointpb.LbEndpoint{
			HostIdentifier: &endpointpb.LbEndpoint_Endpoint{
				Endpoint: &endpointpb.Endpoint{
					Address: &corepb.Address{
						Address: &corepb.Address_SocketAddress{
							SocketAddress: &corepb.SocketAddress{
								Address: instance.Address,
								PortSpecifier: &corepb.SocketAddress_PortValue{
									PortValue: uint32(instance.Port),
								},
							},
						},
					},
				},
			},
			LoadBalancingWeight: wrapperspb.UInt32(uint32(instance.Weight)),
		})
		count++
	}
	// 把 map 转成稳定切片。
	localities := make([]*endpointpb.LocalityLbEndpoints, 0, len(localityBuckets))
	for _, bucket := range localityBuckets {
		localities = append(localities, bucket)
	}
	slices.SortFunc(localities, func(left, right *endpointpb.LocalityLbEndpoints) int {
		leftKey := fmt.Sprintf("%d|%s", left.Priority, left.GetLocality().GetZone())
		rightKey := fmt.Sprintf("%d|%s", right.Priority, right.GetLocality().GetZone())
		switch {
		case leftKey < rightKey:
			return -1
		case leftKey > rightKey:
			return 1
		default:
			return 0
		}
	})
	// 返回最终的 EDS 资源。
	return &endpointpb.ClusterLoadAssignment{
		ClusterName: clusterName,
		Endpoints:   localities,
	}, count
}

// clusterName 生成稳定的 cluster 资源名。
func (b *Builder) clusterName(service, namespace string, port int) string {
	// 命名严格采用 cluster.{service}.{namespace}.{port} 这一可读模式。
	return fmt.Sprintf("%s.%s.%s.%d", b.settings.ClusterNamePrefix, strings.TrimSpace(service), strings.TrimSpace(namespace), port)
}

// serviceGroup 表示单个逻辑服务的实例集合。
type serviceGroup struct {
	// Name 表示服务名。
	Name string
	// Namespace 表示命名空间。
	Namespace string
	// DNS 表示服务统一域名。
	DNS string
	// Port 表示服务端口。
	Port int
	// Instances 表示该服务的健康实例集合。
	Instances []model.ServiceInstance
}

// groupInstances 把实例列表按 service+namespace+port 聚合。
func groupInstances(instances []model.ServiceInstance) []serviceGroup {
	// 先用 map 收集同一逻辑服务的实例。
	grouped := make(map[string]*serviceGroup)
	for _, instance := range instances {
		key := fmt.Sprintf("%s|%s|%d", instance.Name, instance.Namespace, instance.Port)
		if _, ok := grouped[key]; !ok {
			grouped[key] = &serviceGroup{
				Name:      instance.Name,
				Namespace: instance.Namespace,
				DNS:       instance.DNS,
				Port:      instance.Port,
			}
		}
		grouped[key].Instances = append(grouped[key].Instances, instance)
	}
	// 再把 map 转成稳定切片。
	result := make([]serviceGroup, 0, len(grouped))
	for _, group := range grouped {
		result = append(result, *group)
	}
	slices.SortFunc(result, func(left, right serviceGroup) int {
		leftKey := fmt.Sprintf("%s|%s|%d", left.Name, left.Namespace, left.Port)
		rightKey := fmt.Sprintf("%s|%s|%d", right.Name, right.Namespace, right.Port)
		switch {
		case leftKey < rightKey:
			return -1
		case leftKey > rightKey:
			return 1
		default:
			return 0
		}
	})
	// 返回分组结果。
	return result
}

// parsePort 把端口字符串转换成整数。
func parsePort(raw string) int {
	// 使用默认扫描即可满足 host:port 结果解析。
	var port int
	fmt.Sscanf(raw, "%d", &port)
	return port
}

// mustAny 把 proto message 包装成 Any，失败时直接 panic。
func mustAny(message proto.Message) *anypb.Any {
	// Any 打包失败属于编程错误，这里直接中断。
	value, err := anypb.New(message)
	if err != nil {
		panic(err)
	}
	// 返回打包结果。
	return value
}
