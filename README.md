# sidecar-agent

## 项目定位

`sidecar-agent` 是一个主机级控制面代理，目标是按 `backend/design/sidecar-agent/v2.1` 路线，为同机业务进程提供以下能力：

- 本地 DNS 拦截
- 本地服务注册、摘流、注销
- 基于 Consul 的服务发现
- 基于 xDS 的共享 Envoy 控制面
- 基于 OTel 的日志、指标、链路追踪

当前代码形态不是通用 service mesh 全量实现，而是已经进入 v2.2 路线、具备 agent ownership 与业务侧自动重放注册骨架的可运行实现。

## 当前架构

当前仓库中的主要模块如下：

- `cmd/sidecar-agent`
  - 进程入口
- `app`
  - 统一装配配置、DNS、Registry、Discovery、xDS、Admin API、Telemetry
- `config`
  - 配置模型、默认值、配置校验
- `adminapi`
  - 本地管理接口
- `registry`
  - Consul 注册、摘流、注销、发现、路由文档写入
- `discovery`
  - 基于轮询的实例刷新与防抖发布
- `xds`
  - go-control-plane 快照构建与 gRPC xDS 服务
- `dns`
  - 本地 DNS 拦截
- `envoy`
  - bootstrap 生成与 Envoy 生命周期管理
- `telemetry`
  - OTel logger、meter、tracer provider 装配
- `model`
  - 注册请求、生命周期状态、路由文档与服务实例模型

## 已实现内容

### 1. 本地服务生命周期管理

已实现以下本地管理接口：

- `POST /register`
- `POST /drain`
- `POST /deregister`
- `GET /healthz`
- `GET /watch`
- `GET /debug/services`
- `GET /debug/xds`

其中：

- `register` 会校验请求参数、生成实例 ID、写入 Consul、写入完整路由文档
- `drain` 会把实例切换到维护模式，并更新本地状态为 `Draining`
- `deregister` 会从 Consul 注销实例，并更新本地状态为 `Deregistered`
- `watch` 会提供本地长连接事件流，供业务侧核心库在连接恢复后自动重放注册

### 2. Consul 注册与发现

已实现：

- 写 Consul Service Registration
- 写 agent ownership TTL 检查
- 写服务 metadata
- 写 `agent_id / agent_run_id / agent_started_at` 等归属信息
- 写完整路由文档到 Consul KV
- agent 启动时清理同一宿主机旧轮次残留实例
- agent 正常退出时主动摘除本机已接管实例
- 基于 Catalog + Health 拉取当前环境下的健康实例
- 对发现结果做稳定排序，供 xDS 快照生成使用

### 3. 本地 DNS

已实现：

- `*.svc.cluster.local` 固定返回 `127.0.0.1`
- 非 mesh 域名转发给上游 DNS

### 4. xDS 控制面

已实现：

- go-control-plane SnapshotCache
- Listener / Route / Cluster / Endpoint 快照发布
- 共享 Envoy 通过 ADS 拉取配置
- 按 authority 做服务级路由
- 基于 cluster 优先级做同集群优先转发
- 熔断与异常剔除基础参数下发

### 5. Envoy 托管

已实现：

- 生成最小可用 bootstrap
- 可按配置决定是否由 sidecar-agent 托管 Envoy 进程
- 异常退出后重启
- 优雅停止

### 6. OTel 可观测

已实现：

- OTel Log Provider
- OTel Meter Provider
- OTel Tracer Provider
- `stdout` / `otlp` 日志导出
- `prometheus` / `otlp` 指标导出
- `stdout` / `otlp` trace 导出
- 管理接口、注册中心、发现刷新、xDS 发布链路基础 span

### 7. 部署产物

已实现：

- Dockerfile
- 三份 docker-compose 配置
- systemd 样例
- envoy bootstrap 样例
- consul 单节点 compose 样例

### 8. go-micro 联动骨架

已实现：

- `go-consul/agent` 作为正式裸机 sidecar-agent 接入库
- 统一 `RegisterRequest / DrainRequest / DeregisterRequest`
- `ServiceRegistration / ServiceOptions / ServiceNode` 等正式模型
- 业务侧 `Controller`
- 本地连接事件 `Runner`
- 本地 HTTP JSON client
- `/watch` 长连接事件源
- 连接恢复后自动重放 `register` 的最小运行时骨架

说明：

- `go-micro/registry` 根目录下的旧 `Register / Discovery / ServiceNode` 代码已移除
- 裸机业务服务后续统一接入 `go-consul/agent`
- 外部仓库批量迁移可参考 `go-micro/registry/MIGRATION.md`

## 未实现内容

以下内容按当前路线尚未进入可交付范围：

- 独立 health-checker 模块
- 与 go-micro 框架启动钩子的完整自动集成
- 本地长连接 lease / stream 的更强协议约束
- ext_authz 过滤链接入
- 方法级 path 路由治理
- 更细粒度发布规则
- Consul blocking query / watch 机制
- mTLS、证书轮转与身份体系
- iptables 劫持
- 真正完整的生产部署编排

## 需要优化的地方

当前已知后续优化方向如下：

- Discovery 从固定轮询升级为 blocking query
- xDS 从 authority 级路由升级到 path 级治理
- Envoy manager 增加 ready 探针、启动超时与连续失败保护
- Registry 与 Admin API 增加更完整的错误码和状态响应结构
- Telemetry 增加统一属性命名规范与更细的 span 语义
- deploy 目录继续收敛与标准化

更细的优化项记录见 [optimization-notes.md](file:///Users/lhdht/product/firefly/sidecar-agent/optimization-notes.md)。

## 运行方式

### 1. 本地直接运行

```bash
go run ./cmd/sidecar-agent --config ./deploy/agent/sidecar-agent.yaml
```

### 2. Docker Compose

当前提供三种 compose 形态：

- `docker-compose.yml`
  - 最小版，只启动 sidecar-agent
- `docker-compose.standard.yml`
  - 标准版，启动 sidecar-agent + consul + envoy
- `docker-compose.full.yml`
  - 完整版，启动 sidecar-agent + consul + envoy + otel-collector + prometheus

详细说明见 [deploy/README.md](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/README.md)。

## 默认端口

- `15010`
  - sidecar-agent admin API
- `GET /watch`
  - 本地 agent 连接恢复事件流
- `15011`
  - xDS gRPC
- `15353`
  - 本地 DNS
- `15001`
  - Envoy listener
- `19000`
  - Envoy admin
- `8500`
  - Consul HTTP API
- `4318`
  - OTel Collector HTTP OTLP
- `9091`
  - Prometheus UI

## 当前建议

- 生产默认优先使用最小版 compose，外部接入已有 Consul、Envoy、OTel 体系
- 本地或小团队联调优先使用标准版
- 只有在需要完整观测闭环时再使用完整版
