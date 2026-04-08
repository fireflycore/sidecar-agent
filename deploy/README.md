# sidecar-agent 部署说明

## 部署分层

当前 `sidecar-agent` 提供三种部署层级：

- 最小版
  - 默认推荐
  - 仅部署 `sidecar-agent`
  - 依赖外部已提供的 `consul`、`envoy`、`otel`
- 标准版
  - 适合小团队或本地联调
  - 部署 `sidecar-agent + consul + envoy`
- 完整版
  - 适合完整联调与可观测验证
  - 部署 `sidecar-agent + consul + envoy + otel-collector + prometheus`

## 一、最小版

### 使用场景

- 生产环境默认方案
- 宿主环境已经有统一的 Consul、Envoy、OTel 体系
- 只需要部署当前项目自身

### 文件

- compose: [docker-compose.yml](file:///Users/lhdht/product/firefly/sidecar-agent/docker-compose.yml)
- 配置: [sidecar-agent.minimal.yaml](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/agent/sidecar-agent.minimal.yaml)

### 启动

```bash
docker compose up --build -d
```

### 说明

- sidecar-agent 通过 `192.168.1.100:18500` 访问外部 `consul`
- sidecar-agent 通过 `192.168.1.100:18503` 访问外部 `envoy admin`
- telemetry 默认指向外部 `OTLP HTTP` 地址 `192.168.1.100:4318`
- sidecar-agent 会为每个实例注册 agent ownership TTL，确保 agent 失效后实例最终退出 Consul
- 若外部基础设施地址变化，需要同步修改 `sidecar-agent.minimal.yaml`

## 二、标准版

### 使用场景

- 本地开发
- 小团队联调
- 需要快速验证 `consul + sidecar-agent + envoy` 主链路

### 文件

- compose: [docker-compose.standard.yml](file:///Users/lhdht/product/firefly/sidecar-agent/docker-compose.standard.yml)
- 配置: [sidecar-agent.standard.yaml](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/agent/sidecar-agent.standard.yaml)
- envoy bootstrap: [bootstrap.compose.yaml](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/envoy/bootstrap.compose.yaml)

### 启动

```bash
docker compose -f docker-compose.standard.yml up --build -d
```

### 说明

- 该模式会启动单节点 Consul
- 该模式会启动单个 Envoy
- 不会额外启动 OTel Collector 与 Prometheus
- 若你的环境已有外部观测容器，可继续把 sidecar-agent 的 telemetry 指向外部地址

## 三、完整版

### 使用场景

- 需要同时验证服务治理和可观测链路
- 需要本地查看指标与 trace 导出结果

### 文件

- compose: [docker-compose.full.yml](file:///Users/lhdht/product/firefly/sidecar-agent/docker-compose.full.yml)
- 配置: [sidecar-agent.full.yaml](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/agent/sidecar-agent.full.yaml)
- otel config: [otel-collector.compose.yaml](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/otel/otel-collector.compose.yaml)
- prometheus config: [prometheus.compose.yaml](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/prometheus/prometheus.compose.yaml)

### 启动

```bash
docker compose -f docker-compose.full.yml up --build -d
```

### 说明

- 该模式会额外启动 `otel-collector`
- 该模式会额外启动 `prometheus`
- `sidecar-agent` 的日志、指标、trace 会发往 `otel-collector`
- `prometheus` 抓取 `otel-collector` 转出的指标

## 端口说明

### sidecar-agent

- `15010`
  - admin API
- `GET /watch`
  - 本地长连接事件流，供业务侧核心库在 agent 恢复后自动重放注册
- `15011`
  - xDS gRPC
- `15353`
  - DNS

### Envoy

- `15001`
  - 数据入口
- `19000`
  - admin

### Consul

- `8500`
  - HTTP API / UI

### OTel / Prometheus

- `4318`
  - OTLP HTTP
- `9464`
  - Collector 导出的 Prometheus 指标端口
- `9091`
  - Prometheus UI

## 配置差异

### 最小版

- `consul.address = 192.168.1.100:18500`
- `envoy.admin_address = 192.168.1.100:18503`
- `consul.agent_lease_ttl = 10s`
- `consul.agent_lease_refresh_interval = 3s`
- `consul.deregister_critical_service_after = 30s`
- `telemetry.metric_enabled = false`
- `telemetry.trace_enabled = false`

### 标准版

- `consul.address = consul:8500`
- `envoy.admin_address = envoy:19000`
- `consul.agent_lease_ttl = 10s`
- `consul.agent_lease_refresh_interval = 3s`
- `consul.deregister_critical_service_after = 30s`
- `telemetry` 默认仍不强绑本地 OTel Collector

### 完整版

- `consul.address = consul:8500`
- `envoy.admin_address = envoy:19000`
- `consul.agent_lease_ttl = 10s`
- `consul.agent_lease_refresh_interval = 3s`
- `consul.deregister_critical_service_after = 30s`
- `telemetry.otlp_endpoint = http://otel-collector:4318`
- `trace/log/metric` 均走 OTLP

## systemd 样例

若不使用 compose，可参考以下样例：

- [sidecar-agent.service](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/systemd/sidecar-agent.service)
- [envoy.service](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/systemd/envoy.service)

## 相关辅助文件

- consul 单节点 compose 样例：[deploy/consul/docker-compose.yml](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/consul/docker-compose.yml)
- envoy bootstrap 示例：[bootstrap.example.yaml](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/envoy/bootstrap.example.yaml)
- WireGuard 示例配置：
  - [cluster-a.conf.example](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/wireguard/cluster-a.conf.example)
  - [cluster-b.conf.example](file:///Users/lhdht/product/firefly/sidecar-agent/deploy/wireguard/cluster-b.conf.example)

## 当前限制

- 最小版假定外部基础设施已存在，不负责创建外部 Consul / Envoy / OTel
- 标准版与完整版都只提供单节点 Consul、单个 Envoy 的最小联调形态
- 当前还没有补齐真正生产级高可用部署模板
- 当前还没有补齐证书、mTLS、权限模型和发布策略模板

## 建议

- 生产默认优先选最小版
- 本地开发优先选标准版
- 做可观测联调再选完整版
