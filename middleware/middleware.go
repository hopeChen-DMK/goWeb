// Package middleware 包含框架所有内置中间件。
// 推荐中间件执行顺序：
//
//	Recovery → Logger → Secure → CORS → RateLimiter → BodyLimit → Timeout →
//	CSRF → Auth → RBAC → 业务处理器
package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/hopechen-dmk/goweb/core"
)

// ============================================================================
// Recovery 中间件 - 捕获 panic
// ============================================================================

// RecoveryConfig Recovery 配置。
type RecoveryConfig struct {
	// LogStack 是否记录堆栈信息
	LogStack bool
	// Handler 自定义 panic 处理器，nil 则使用默认
	Handler func(c *core.Context, panicErr *core.PanicError)
}

// Recovery 返回 panic 恢复中间件。
func Recovery(config ...RecoveryConfig) core.MiddlewareFunc {
	cfg := RecoveryConfig{LogStack: true}
	if len(config) > 0 {
		cfg = config[0]
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					panicErr := &core.PanicError{
						Value: r,
						Stack: debug.Stack(),
					}

					if cfg.LogStack && c.Logger() != nil {
						c.Logger().Error("panic recovered",
							"error", panicErr.Value,
							"stack", string(panicErr.Stack),
							"request_id", c.RequestID(),
							"method", c.Method(),
							"path", c.Path(),
						)
					}

					if cfg.Handler != nil {
						cfg.Handler(c, panicErr)
					} else {
						err = c.JSON(http.StatusInternalServerError, core.Response{
							Code:    http.StatusInternalServerError,
							Message: "Internal Server Error",
						})
					}
				}
			}()
			return next(c)
		}
	}
}

// ============================================================================
// Logger 中间件 - 请求日志
// ============================================================================

// LoggerConfig 日志配置。
type LoggerConfig struct {
	// Format 格式："json" 或 "text"
	Format string
	// LogBody 是否记录请求/响应体（生产环境仅 debug 时启用）
	LogBody bool
	// LogBodyMaxSize 记录体内容的最大长度
	LogBodyMaxSize int
	// SkipPaths 跳过日志的路径
	SkipPaths []string
}

// DefaultLoggerConfig 返回默认日志配置。
func DefaultLoggerConfig() LoggerConfig {
	return LoggerConfig{
		Format:         "json",
		LogBody:        false,
		LogBodyMaxSize: 1024,
		SkipPaths:      []string{"/healthz", "/readyz"},
	}
}

// Logger 返回请求日志中间件（基于 slog）。
func Logger(config ...LoggerConfig) core.MiddlewareFunc {
	cfg := DefaultLoggerConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	skipMap := make(map[string]bool, len(cfg.SkipPaths))
	for _, p := range cfg.SkipPaths {
		skipMap[p] = true
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			if skipMap[c.Path()] {
				return next(c)
			}

			start := time.Now()

			err := next(c)

			latency := time.Since(start)
			statusCode := c.StatusCode()
			method := c.Method()
			path := c.Path()
			clientIP := c.ClientIP()
			requestID := c.RequestID()

			// 输出性能警告：超过 1 秒的请求
			if latency > time.Second {
				if c.Logger() != nil {
					c.Logger().Warn("slow request",
						slog.String("method", method),
						slog.String("path", path),
						slog.Duration("latency", latency),
						slog.Int("status", statusCode),
						slog.String("request_id", requestID),
					)
				}
			}

			// 记录体内容时输出性能警告
			if cfg.LogBody && cfg.LogBodyMaxSize > 0 {
				if c.Logger() != nil {
					c.Logger().Warn("body logging enabled - may impact performance",
						slog.Int("max_size", cfg.LogBodyMaxSize))
				}
			}

			if c.Logger() != nil {
				args := []any{
					slog.String("method", method),
					slog.String("path", path),
					slog.Int("status", statusCode),
					slog.Duration("latency", latency),
					slog.String("ip", clientIP),
					slog.String("request_id", requestID),
					slog.String("user_agent", c.UserAgent()),
				}

				if err != nil {
					args = append(args, slog.String("error", err.Error()))
				}

				level := slog.LevelInfo
				if statusCode >= 500 {
					level = slog.LevelError
				} else if statusCode >= 400 {
					level = slog.LevelWarn
				}

				switch level {
				case slog.LevelError:
					c.Logger().Error("request completed", args...)
				case slog.LevelWarn:
					c.Logger().Warn("request completed", args...)
				default:
					c.Logger().Info("request completed", args...)
				}
			}

			return err
		}
	}
}

// ============================================================================
// Secure 中间件 - 安全头
// ============================================================================

// SecureConfig 安全头配置。
type SecureConfig struct {
	// CSP Content-Security-Policy
	CSP string
	// HSTS Strict-Transport-Security
	HSTS string
	// FrameOptions X-Frame-Options
	FrameOptions string
	// ContentTypeNosniff X-Content-Type-Options
	ContentTypeNosniff string
	// XSSProtection X-XSS-Protection
	XSSProtection string
	// ReferrerPolicy Referrer-Policy
	ReferrerPolicy string
	// PermissionsPolicy Permissions-Policy
	PermissionsPolicy string
}

// DefaultSecureConfig 返回安全的默认配置。
func DefaultSecureConfig() SecureConfig {
	return SecureConfig{
		CSP:                "default-src 'self'",
		HSTS:               "max-age=63072000; includeSubDomains; preload",
		FrameOptions:       "DENY",
		ContentTypeNosniff: "nosniff",
		XSSProtection:      "1; mode=block",
		ReferrerPolicy:     "strict-origin-when-cross-origin",
		PermissionsPolicy:  "geolocation=(), camera=(), microphone=()",
	}
}

// Secure 返回安全头中间件。
func Secure(config ...SecureConfig) core.MiddlewareFunc {
	cfg := DefaultSecureConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			if cfg.CSP != "" {
				c.SetHeader("Content-Security-Policy", cfg.CSP)
			}
			if cfg.HSTS != "" {
				c.SetHeader("Strict-Transport-Security", cfg.HSTS)
			}
			if cfg.FrameOptions != "" {
				c.SetHeader("X-Frame-Options", cfg.FrameOptions)
			}
			if cfg.ContentTypeNosniff != "" {
				c.SetHeader("X-Content-Type-Options", cfg.ContentTypeNosniff)
			}
			if cfg.XSSProtection != "" {
				c.SetHeader("X-XSS-Protection", cfg.XSSProtection)
			}
			if cfg.ReferrerPolicy != "" {
				c.SetHeader("Referrer-Policy", cfg.ReferrerPolicy)
			}
			if cfg.PermissionsPolicy != "" {
				c.SetHeader("Permissions-Policy", cfg.PermissionsPolicy)
			}

			return next(c)
		}
	}
}

// ============================================================================
// BodyLimit 中间件 - 请求体大小限制
// ============================================================================

// BodyLimitConfig 请求体限制配置。
type BodyLimitConfig struct {
	// MaxSize 最大请求体大小（字节）
	MaxSize int64
}

// BodyLimit 返回请求体大小限制中间件。
func BodyLimit(maxSize int64) core.MiddlewareFunc {
	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			if c.Request.ContentLength > maxSize {
				return c.JSON(http.StatusRequestEntityTooLarge, core.Response{
					Code:    http.StatusRequestEntityTooLarge,
					Message: "Request entity too large",
				})
			}

			// 限制 Reader
			c.Request.Body = http.MaxBytesReader(c.ResponseWriter(), c.Request.Body, maxSize)
			return next(c)
		}
	}
}

// ============================================================================
// Timeout 中间件 - 请求超时
// ============================================================================

// TimeoutConfig 超时配置。
type TimeoutConfig struct {
	// Timeout 超时时间
	Timeout time.Duration
}

// Timeout 返回请求超时中间件。
func Timeout(d time.Duration) core.MiddlewareFunc {
	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			done := make(chan error, 1)

			go func() {
				defer func() {
					if p := core.RecoverPanic(); p != nil {
						done <- fmt.Errorf("handler panic: %v", p.Value)
					}
				}()
				done <- next(c)
			}()

			select {
			case err := <-done:
				return err
			case <-time.After(d):
				return c.JSON(http.StatusServiceUnavailable, core.Response{
					Code:    http.StatusServiceUnavailable,
					Message: "Request timeout",
				})
			}
		}
	}
}

// ============================================================================
// RBAC 中间件 - 角色访问控制
// ============================================================================

// RBACConfig RBAC 配置。
type RBACConfig struct {
	// RequiredRoles 需要的角色列表
	RequiredRoles []string
	// RequireAll 是否需要所有角色（false 表示任一即可）
	RequireAll bool
}

// RBAC 返回角色访问控制中间件。
func RBAC(roles []string, requireAll ...bool) core.MiddlewareFunc {
	all := false
	if len(requireAll) > 0 {
		all = requireAll[0]
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			authInfo := c.Auth()
			if authInfo == nil {
				return c.JSON(http.StatusForbidden, core.Response{
					Code:    http.StatusForbidden,
					Message: "Forbidden: authentication required",
				})
			}

			if len(roles) == 0 {
				return next(c)
			}

			matched := 0
			for _, requiredRole := range roles {
				for _, userRole := range authInfo.Roles {
					if userRole == requiredRole || userRole == "admin" { // admin 通配
						matched++
						break
					}
				}
			}

			authorized := false
			if all {
				authorized = matched == len(roles)
			} else {
				authorized = matched > 0
			}

			if !authorized {
				return c.JSON(http.StatusForbidden, core.Response{
					Code:    http.StatusForbidden,
					Message: "Forbidden: insufficient permissions",
				})
			}

			return next(c)
		}
	}
}

// ============================================================================
// 推荐中间件顺序
// ============================================================================

// RecommendedOrder 返回推荐的中间件执行顺序。
// Recovery → Logger → Secure → CORS → RateLimiter → BodyLimit → Timeout →
// CSRF → Auth → RBAC → 业务处理器
func RecommendedOrder() []string {
	return []string{
		"Recovery",
		"Logger",
		"Secure",
		"CORS",
		"RateLimiter",
		"BodyLimit",
		"Timeout",
		"CSRF",
		"Auth",
		"RBAC",
	}
}

// ============================================================================
// Stack trace 格式化
// ============================================================================

func stackTrace() string {
	return string(debug.Stack())
}
