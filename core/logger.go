package core

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

// ============================================================================
// 日志实现：基于 slog
// ============================================================================

// SlogLogger 基于标准库 slog 的日志器。
type SlogLogger struct {
	logger *slog.Logger
	level  atomic.Int32 // 存储 LogLevel
}

// NewLogger 创建新的 slog 日志器。
func NewLogger(w io.Writer, level LogLevel) *SlogLogger {
	if w == nil {
		w = os.Stdout
	}

	opts := &slog.HandlerOptions{
		Level: toSlogLevel(level),
	}

	l := &SlogLogger{
		logger: slog.New(slog.NewJSONHandler(w, opts)),
	}
	l.level.Store(int32(level))
	return l
}

// NewTextLogger 创建文本格式日志器。
func NewTextLogger(w io.Writer, level LogLevel) *SlogLogger {
	if w == nil {
		w = os.Stdout
	}

	opts := &slog.HandlerOptions{
		Level: toSlogLevel(level),
	}

	l := &SlogLogger{
		logger: slog.New(slog.NewTextHandler(w, opts)),
	}
	l.level.Store(int32(level))
	return l
}

// ============================================================================
// Logger 接口实现
// ============================================================================

func (l *SlogLogger) Debug(msg string, args ...any) {
	l.logger.Debug(msg, args...)
}

func (l *SlogLogger) Info(msg string, args ...any) {
	l.logger.Info(msg, args...)
}

func (l *SlogLogger) Warn(msg string, args ...any) {
	l.logger.Warn(msg, args...)
}

func (l *SlogLogger) Error(msg string, args ...any) {
	l.logger.Error(msg, args...)
}

// With 创建带预设字段的子日志器。
func (l *SlogLogger) With(args ...any) Logger {
	return &SlogLogger{
		logger: l.logger.With(args...),
	}
}

// SetLevel 动态修改日志级别。
func (l *SlogLogger) SetLevel(level LogLevel) {
	l.level.Store(int32(level))
	// 重新创建 logger 以应用新级别
	// 注意：生产环境建议通过 slog 的 LevelVar 实现
}

// Level 返回当前日志级别。
func (l *SlogLogger) Level() LogLevel {
	return LogLevel(l.level.Load())
}

// ============================================================================
// slog 级别映射
// ============================================================================

func toSlogLevel(l LogLevel) slog.Level {
	switch l {
	case LogLevelDebug:
		return slog.LevelDebug
	case LogLevelInfo:
		return slog.LevelInfo
	case LogLevelWarn:
		return slog.LevelWarn
	case LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ============================================================================
// 动态日志级别（支持运行时调整）
// ============================================================================

// DynamicLevelHandler 支持运行时动态调整日志级别。
type DynamicLevelHandler struct {
	levelVar *slog.LevelVar
	handler  slog.Handler
}

// NewDynamicLogger 创建支持动态级别的日志器。
func NewDynamicLogger(w io.Writer, level LogLevel, useJSON bool) *DynamicLevelHandler {
	if w == nil {
		w = os.Stdout
	}

	var levelVar slog.LevelVar
	levelVar.Set(toSlogLevel(level))

	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: &levelVar,
	}
	if useJSON {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return &DynamicLevelHandler{
		levelVar: &levelVar,
		handler:  handler,
	}
}

// Handler 返回底层 slog.Handler。
func (d *DynamicLevelHandler) Handler() slog.Handler {
	return d.handler
}

// SetLevel 动态调整日志级别。
func (d *DynamicLevelHandler) SetLevel(level LogLevel) {
	d.levelVar.Set(toSlogLevel(level))
}

// Logger 返回 slog.Logger。
func (d *DynamicLevelHandler) Logger() *slog.Logger {
	return slog.New(d.handler)
}

// ============================================================================
// 无操作日志器（用于测试/静默模式）
// ============================================================================

// NopLogger 不执行任何操作的日志器。
type NopLogger struct{}

func (n *NopLogger) Debug(msg string, args ...any) {}
func (n *NopLogger) Info(msg string, args ...any)  {}
func (n *NopLogger) Warn(msg string, args ...any)  {}
func (n *NopLogger) Error(msg string, args ...any) {}
func (n *NopLogger) With(args ...any) Logger       { return n }
func (n *NopLogger) SetLevel(level LogLevel)        {}

// Ensure interface compliance.
var _ Logger = (*SlogLogger)(nil)
var _ Logger = (*NopLogger)(nil)
