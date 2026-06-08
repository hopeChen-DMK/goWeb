// Package core 定义了框架的核心类型和接口。
// 核心层严格零外部依赖，仅使用 Go 标准库。
package core

import (
	"context"
	"io"
	"net/http"
	"time"
)

// HandlerFunc 定义请求处理函数签名。
type HandlerFunc func(c *Context) error

// MiddlewareFunc 中间件函数签名，遵循洋葱模型。
// 对 HandlerFunc 进行包装，返回新的 HandlerFunc。
type MiddlewareFunc func(next HandlerFunc) HandlerFunc

// ============================================================================
// 上下文相关类型
// ============================================================================

// Param 表示路由中的命名参数。
type Param struct {
	Key   string
	Value string
}

// Params 是路由参数的有序切片。
type Params []Param

// Get 返回指定名称的路由参数值。
func (ps Params) Get(name string) string {
	for i := range ps {
		if ps[i].Key == name {
			return ps[i].Value
		}
	}
	return ""
}

// ============================================================================
// 绑定相关类型
// ============================================================================

// Binder 定义请求体绑定接口。
type Binder interface {
	Bind(r *http.Request, dst interface{}) error
}

// Bindable 定义可自定义绑定行为的接口。
type Bindable interface {
	Bind(r *http.Request) error
}

// ============================================================================
// 验证相关类型
// ============================================================================

// Validator 定义验证器接口。
type Validator interface {
	Validate(v interface{}) error
}

// ValidationError 表示单条验证错误。
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Value   interface{} `json:"value,omitempty"`
}

// ValidationErrors 是验证错误的集合。
type ValidationErrors []ValidationError

func (ve ValidationErrors) Error() string {
	if len(ve) == 0 {
		return ""
	}
	s := ve[0].Field + ": " + ve[0].Message
	for i := 1; i < len(ve); i++ {
		s += "; " + ve[i].Field + ": " + ve[i].Message
	}
	return s
}

// ============================================================================
// 日志相关类型
// ============================================================================

// LogLevel 定义日志级别。
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "DEBUG"
	case LogLevelInfo:
		return "INFO"
	case LogLevelWarn:
		return "WARN"
	case LogLevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Logger 定义日志抽象接口。
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	With(args ...any) Logger
	SetLevel(level LogLevel)
}

// ============================================================================
// 认证相关类型
// ============================================================================

// AuthInfo 包含认证成功后的用户信息。
type AuthInfo struct {
	ID        string
	Roles     []string
	Metadata  map[string]any
	ExpiresAt time.Time
}

// Authenticator 定义认证接口。
type Authenticator interface {
	Authenticate(c *Context) (*AuthInfo, error)
}

// ============================================================================
// 会话存储接口
// ============================================================================

// SessionStore 定义会话存储接口。
type SessionStore interface {
	Get(sessionID string) (map[string]any, error)
	Set(sessionID string, data map[string]any, ttl time.Duration) error
	Delete(sessionID string) error
	Exists(sessionID string) bool
	Save(sessionID string) error
	Release()
}

// ============================================================================
// 限流接口
// ============================================================================

// RateLimiterStore 定义限流存储后端接口。
type RateLimiterStore interface {
	Allow(key string, rate int, burst int, period time.Duration) (bool, time.Duration, error)
}

// ============================================================================
// 文件/魔数检测接口
// ============================================================================

// MagicDetector 定义魔数检测接口。
// 核心层仅定义接口，不提供实现。若未引入扩展签名库，调用检测方法时返回错误。
type MagicDetector interface {
	Detect(reader io.Reader) (*FileType, error)
}

// FileType 存储文件类型信息。
type FileType struct {
	MIME        string
	Extension   string
	Description string
}

// Storage 定义文件存储抽象层接口。
type Storage interface {
	Save(path string, reader io.Reader) error
	SaveAll(files map[string]io.Reader) error // 尽最大努力执行，不保证原子性
	Open(path string) (io.ReadCloser, error)
	Delete(path string) error
	Exists(path string) (bool, error)
	Size(path string) (int64, error)
	List(dir string) ([]FileInfo, error)
	SignURL(path string, ttl time.Duration) (string, error)
}

// FileInfo 文件信息。
type FileInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// ============================================================================
// 配置相关类型
// ============================================================================

// Config 定义框架配置接口。
type Config interface {
	Get(key string) any
	GetString(key string) string
	GetInt(key string) int
	GetBool(key string) bool
	GetDuration(key string) time.Duration
	GetStringSlice(key string) []string
	Set(key string, value any)
	LoadFile(path string) error
	Watch(callback func(key string, value any))
	Unmarshal(v interface{}) error
	UnmarshalKey(key string, v interface{}) error
}

// ============================================================================
// 追踪预留字段
// ============================================================================

// Trace 链路追踪预留结构。
type Trace struct {
	TraceID string
	SpanID  string
	Baggage map[string]string
}

// ============================================================================
// 响应类型
// ============================================================================

// Map 是便捷的 map 类型别名。
type Map = map[string]any

// H 是 Map 的便捷别名（gin 兼容风格）。
type H = Map

// Response 统一响应结构。
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
}

// ============================================================================
// WebSocket 相关
// ============================================================================

// WSUpgrader 接口用于 WebSocket 连接升级。
type WSUpgrader interface {
	Upgrade(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (WSConn, error)
}

// WSConn WebSocket 连接接口。
type WSConn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// ============================================================================
// 生命周期钩子
// ============================================================================

// LifecycleHook 生命周期钩子函数。
type LifecycleHook func() error

// ============================================================================
// 路由器接口
// ============================================================================

// Router 路由器接口。
type Router interface {
	http.Handler
	GET(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	POST(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	PUT(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	DELETE(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	PATCH(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	HEAD(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	OPTIONS(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	CONNECT(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	TRACE(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	ANY(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router
	Group(prefix string) *Group
	Use(mw ...MiddlewareFunc)
	Static(prefix, root string)
}

// ============================================================================
// 标准 Context 别名
// ============================================================================

type ContextAlias = context.Context
