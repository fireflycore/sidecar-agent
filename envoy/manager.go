package envoy

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// MetricsRecorder 抽象 Envoy 管理器需要的最小指标能力。
type MetricsRecorder interface {
	// IncEnvoyRestart 统计异常重启次数。
	IncEnvoyRestart()
}

// Manager 负责拉起、停止与在异常退出后重启共享 Envoy。
type Manager struct {
	// binaryPath 表示 Envoy 可执行文件路径。
	binaryPath string
	// bootstrapPath 表示 Envoy bootstrap 文件路径。
	bootstrapPath string
	// adminAddress 表示 Envoy admin 地址。
	adminAddress string
	// logLevel 表示 Envoy 日志级别。
	logLevel string
	// restartBackoff 表示异常重启等待时间。
	restartBackoff time.Duration
	// logger 负责输出结构化日志。
	logger *slog.Logger
	// metrics 负责统计异常重启次数。
	metrics MetricsRecorder
	// mu 保护进程状态。
	mu sync.Mutex
	// cmd 保存当前 Envoy 进程。
	cmd *exec.Cmd
	// ctx 保存外部生命周期上下文。
	ctx context.Context
	// cancel 用于终止后台守护循环。
	cancel context.CancelFunc
}

// NewManager 创建一个新的 Envoy 管理器。
func NewManager(binaryPath, bootstrapPath, adminAddress, logLevel string, restartBackoff time.Duration, logger *slog.Logger, metrics MetricsRecorder) *Manager {
	// 返回已装配完成的管理器实例。
	return &Manager{
		binaryPath:     binaryPath,
		bootstrapPath:  bootstrapPath,
		adminAddress:   adminAddress,
		logLevel:       logLevel,
		restartBackoff: restartBackoff,
		logger:         logger,
		metrics:        metrics,
	}
}

// Start 启动 Envoy 进程并进入守护循环。
func (m *Manager) Start(parent context.Context) error {
	// 避免重复启动多个守护循环。
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return nil
	}
	// 基于父上下文创建一个可取消上下文。
	m.ctx, m.cancel = context.WithCancel(parent)
	// 先尝试启动首个 Envoy 进程。
	if err := m.startProcessLocked(); err != nil {
		m.cancel()
		m.cancel = nil
		return err
	}
	// 在后台等待并处理异常退出。
	go m.watchLoop()
	// 启动成功后返回 nil。
	return nil
}

// Stop 停止当前 Envoy 进程并退出守护循环。
func (m *Manager) Stop(ctx context.Context) error {
	// 先锁定状态，防止和 watchLoop 并发修改。
	m.mu.Lock()
	defer m.mu.Unlock()
	// 若尚未启动，则直接返回。
	if m.cancel == nil {
		return nil
	}
	// 先通知后台循环停止。
	m.cancel()
	m.cancel = nil
	// 若没有活跃进程，则无需继续处理。
	if m.cmd == nil || m.cmd.Process == nil {
		return nil
	}
	// 先发送 SIGTERM 请求 Envoy 优雅退出。
	if err := m.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	// 启动等待协程，避免阻塞持锁流程。
	done := make(chan error, 1)
	go func(cmd *exec.Cmd) {
		done <- cmd.Wait()
	}(m.cmd)
	// 在上下文超时前等待退出完成。
	select {
	case err := <-done:
		if err != nil && !isExited(err) {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	// 清理进程引用。
	m.cmd = nil
	// 返回成功结果。
	return nil
}

// AdminAddress 返回当前 Envoy admin 地址。
func (m *Manager) AdminAddress() string {
	// 直接返回管理器维护的地址。
	return m.adminAddress
}

// watchLoop 持续观察进程退出并在异常时重启。
func (m *Manager) watchLoop() {
	// 守护循环持续运行直到上下文取消。
	for {
		// 读取当前上下文与进程引用。
		m.mu.Lock()
		ctx := m.ctx
		cmd := m.cmd
		m.mu.Unlock()
		// 上下文为空表示管理器已经停止。
		if ctx == nil {
			return
		}
		// 若没有进程则直接结束。
		if cmd == nil {
			return
		}
		// 等待当前进程退出。
		err := cmd.Wait()
		// 若是主动停止，则直接退出守护循环。
		if ctx.Err() != nil {
			return
		}
		// 输出异常退出日志。
		if m.logger != nil {
			m.logger.Warn("envoy process exited unexpectedly",
				slog.String("error", errString(err)),
			)
		}
		// 统计一次重启。
		if m.metrics != nil {
			m.metrics.IncEnvoyRestart()
		}
		// 等待退避窗口，避免崩溃后疯狂重启。
		select {
		case <-time.After(m.restartBackoff):
		case <-ctx.Done():
			return
		}
		// 再次尝试拉起进程。
		m.mu.Lock()
		if m.ctx == nil || m.ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if restartErr := m.startProcessLocked(); restartErr != nil && m.logger != nil {
			m.logger.Error("envoy restart failed",
				slog.String("error", restartErr.Error()),
			)
		}
		m.mu.Unlock()
	}
}

// startProcessLocked 在持锁状态下启动一个 Envoy 进程。
func (m *Manager) startProcessLocked() error {
	// 使用 CommandContext 让上下文取消时自动终止子进程。
	cmd := exec.CommandContext(m.ctx, m.binaryPath,
		"-c", m.bootstrapPath,
		"--log-level", m.logLevel,
	)
	// 直接继承标准输出和标准错误，便于 systemd 或容器采集日志。
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// 启动 Envoy 进程。
	if err := cmd.Start(); err != nil {
		return err
	}
	// 保存最新进程引用。
	m.cmd = cmd
	// 输出启动日志。
	if m.logger != nil {
		m.logger.Info("envoy process started",
			slog.String("binary", m.binaryPath),
			slog.String("bootstrap", m.bootstrapPath),
			slog.String("admin_address", m.adminAddress),
		)
	}
	// 返回成功结果。
	return nil
}

// errString 把错误对象转换成可打印文本。
func errString(err error) string {
	// 空错误时返回空字符串，避免日志里出现 <nil>。
	if err == nil {
		return ""
	}
	// 返回底层错误文本。
	return err.Error()
}

// isExited 判断错误是否只是普通退出结果。
func isExited(err error) bool {
	// Wait 成功返回 nil，也属于已退出。
	if err == nil {
		return true
	}
	// exec.ExitError 表示子进程已经结束。
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}
