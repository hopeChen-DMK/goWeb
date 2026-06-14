package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/hopechen-dmk/goweb/core"
)

// ============================================================================
// CORS 中间件
// ============================================================================

// CORSConfig CORS 配置。
type CORSConfig struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           time.Duration
}

// DefaultCORSConfig 返回保守的默认 CORS 配置。
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Request-ID", "X-CSRF-Token"},
		ExposeHeaders:    []string{"Content-Length", "X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}
}

// CORS 返回 CORS 中间件。
func CORS(config ...CORSConfig) core.MiddlewareFunc {
	cfg := DefaultCORSConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	allowMethods := strings.Join(cfg.AllowMethods, ", ")
	allowHeaders := strings.Join(cfg.AllowHeaders, ", ")
	exposeHeaders := strings.Join(cfg.ExposeHeaders, ", ")
	maxAge := int(cfg.MaxAge.Seconds())

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			origin := c.GetHeader("Origin")

			// 设置允许的源
			if len(cfg.AllowOrigins) == 1 && cfg.AllowOrigins[0] == "*" {
				c.SetHeader("Access-Control-Allow-Origin", "*")
			} else if origin != "" {
				for _, allowed := range cfg.AllowOrigins {
					if allowed == origin || allowed == "*" {
						c.SetHeader("Access-Control-Allow-Origin", origin)
						break
					}
				}
			}

			if cfg.AllowCredentials {
				c.SetHeader("Access-Control-Allow-Credentials", "true")
			}

			c.SetHeader("Access-Control-Expose-Headers", exposeHeaders)

			// 预检请求处理（OPTIONS）
			if c.Method() == http.MethodOptions {
				c.SetHeader("Access-Control-Allow-Methods", allowMethods)
				c.SetHeader("Access-Control-Allow-Headers", allowHeaders)
				if cfg.MaxAge > 0 {
					c.SetHeader("Access-Control-Max-Age", itoa(maxAge))
				}
				return c.NoContent()
			}

			return next(c)
		}
	}
}

func itoa(n int) string {
	return formatInt(int64(n))
}

func formatInt(n int64) string {
	if n < 10 {
		return string('0' + byte(n))
	}
	return formatIntNonNeg(n)
}

func formatIntNonNeg(n int64) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string('0'+byte(n%10)) + s
		n /= 10
	}
	return s
}
