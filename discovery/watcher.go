package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/fireflycore/sidecar-agent/model"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// Source 抽象一个可拉取当前健康实例集合的来源。
type Source interface {
	// Discover 返回当前环境下的健康实例快照。
	Discover(ctx context.Context) ([]model.ServiceInstance, error)
}

// MetricsRecorder 抽象最小指标能力，避免 discovery 依赖具体实现。
type MetricsRecorder interface {
	// IncDiscoveryRefresh 统计刷新次数。
	IncDiscoveryRefresh()
}

// Watcher 周期性从 Consul 拉取实例并在变化时发布快照。
type Watcher struct {
	// source 表示实例数据来源。
	source Source
	// logger 用于输出结构化日志。
	logger *slog.Logger
	// metrics 用于累计刷新指标。
	metrics MetricsRecorder
	// refreshInterval 表示轮询间隔。
	refreshInterval time.Duration
	// debounceInterval 表示最小发布时间间隔。
	debounceInterval time.Duration
	// mu 保护最近一次发布状态，避免主动刷新与后台轮询并发竞争。
	mu sync.Mutex
	// lastFingerprint 保存上次已发布快照摘要。
	lastFingerprint string
	// lastPublishAt 保存上次发布时间。
	lastPublishAt time.Time
}

// New 创建一个新的发现 watcher。
func New(source Source, refreshInterval, debounceInterval time.Duration, logger *slog.Logger, metrics MetricsRecorder) *Watcher {
	// 返回一个可直接运行的 watcher。
	return &Watcher{
		source:           source,
		logger:           logger,
		metrics:          metrics,
		refreshInterval:  refreshInterval,
		debounceInterval: debounceInterval,
	}
}

// Run 周期性刷新实例，并在变化时触发 onUpdate。
func (w *Watcher) Run(ctx context.Context, onUpdate func([]model.ServiceInstance) error) error {
	// 启动后先立即刷新一次，尽快形成首版快照。
	if err := w.refreshOnce(ctx, onUpdate); err != nil {
		return err
	}
	// 创建固定间隔的刷新 ticker。
	ticker := time.NewTicker(w.refreshInterval)
	defer ticker.Stop()
	// 循环处理后续刷新。
	for {
		select {
		case <-ctx.Done():
			// 上下文结束时退出 run loop。
			return nil
		case <-ticker.C:
			// 每次 tick 都执行一次刷新。
			if err := w.refreshOnce(ctx, onUpdate); err != nil {
				return err
			}
		}
	}
}

// RefreshNow 暴露一个主动刷新入口，供管理接口在 register/drain/deregister 后快速收敛。
func (w *Watcher) RefreshNow(ctx context.Context, onUpdate func([]model.ServiceInstance) error) error {
	// 直接复用内部刷新逻辑。
	return w.refreshOnce(ctx, onUpdate)
}

// refreshOnce 完成一轮发现、去重与快照发布。
func (w *Watcher) refreshOnce(ctx context.Context, onUpdate func([]model.ServiceInstance) error) error {
	// 为单轮发现刷新创建 span，便于观察轮询与主动刷新行为。
	ctx, span := otel.Tracer("sidecar-agent/discovery").Start(ctx, "discovery.refresh")
	defer span.End()
	// 从数据源读取最新健康实例。
	instances, err := w.source.Discover(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	// 记录刷新次数指标。
	if w.metrics != nil {
		w.metrics.IncDiscoveryRefresh()
	}
	// 计算当前实例集合的稳定摘要。
	fingerprint, err := hashInstances(instances)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	// 在比较与写回最近发布状态前加锁，避免并发 RefreshNow 造成竞态。
	w.mu.Lock()
	defer w.mu.Unlock()
	// 未变化时直接返回，避免重复发布相同快照。
	if fingerprint == w.lastFingerprint {
		span.SetStatus(codes.Ok, "unchanged")
		return nil
	}
	// 若距离上次发布时间过近，则直接跳过本轮发布。
	if !w.lastPublishAt.IsZero() && time.Since(w.lastPublishAt) < w.debounceInterval {
		span.SetStatus(codes.Ok, "debounced")
		return nil
	}
	// 回调上层完成快照构建与发布。
	if err := onUpdate(instances); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	// 更新已发布摘要与时间戳。
	w.lastFingerprint = fingerprint
	w.lastPublishAt = time.Now()
	// 输出调试日志。
	if w.logger != nil {
		w.logger.Info("service discovery snapshot published",
			slog.Int("instances", len(instances)),
		)
	}
	span.SetAttributes(attribute.Int("instances.count", len(instances)))
	span.SetStatus(codes.Ok, "published")
	// 返回成功结果。
	return nil
}

// hashInstances 生成稳定的实例集合哈希。
func hashInstances(instances []model.ServiceInstance) (string, error) {
	// 把切片编码成 JSON，借助排序后的输入获得稳定输出。
	payload, err := json.Marshal(instances)
	if err != nil {
		return "", err
	}
	// 计算 SHA-256 作为摘要。
	sum := sha256.Sum256(payload)
	// 以十六进制字符串返回。
	return hex.EncodeToString(sum[:]), nil
}
