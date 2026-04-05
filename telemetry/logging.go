package telemetry

import (
	"log/slog"
	"os"
)

// NewLogger 创建一个统一的结构化日志器。
func NewLogger() *slog.Logger {
	// 使用文本处理器便于本机排障时直接阅读。
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		// 当前阶段默认输出 info 级别以上日志。
		Level: slog.LevelInfo,
	})
	// 返回统一日志器实例。
	return slog.New(handler)
}
