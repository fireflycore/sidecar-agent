# sidecar-agent v2.1 可优化事项记录

## 本次不纳入实现范围

- health-checker 独立模块
  - 当前仅依赖 Consul TCP check 与业务主动 drain/deregister
  - 后续可补本机端口探测、HTTP/grpc health、多次失败自动摘流

- ext_authz 接入
  - 本次先完成 agent、Consul、xDS、共享 Envoy 控制面闭环
  - 后续可按 v2.1 Phase 4 把 authz service 接到 Envoy filter 链

- Watch 机制从 polling 升级为 blocking query
  - 当前 discovery 采用固定间隔刷新，优先保证实现稳定和可验证
  - 后续可升级为 Consul blocking query 或 watch，以降低抖动与延迟

- xDS 路由细化到方法级
  - 当前共享 Envoy 先按 authority 做服务级路由
  - 后续若需要更细粒度治理，可把 route_config_ref 文档映射到 path 级 RDS

- Envoy 运行时健康探针
  - 当前 manager 负责拉起、停止、异常重启
  - 后续可补 admin ready 探针、启动超时、连续失败熔断保护

- systemd 与 deploy 样例
  - 本次先完成代码骨架与本地运行能力
  - 后续可继续补 deploy/envoy 与 deploy/systemd 产物
