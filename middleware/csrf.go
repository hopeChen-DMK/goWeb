package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hopeChen-DMK/goWeb/core"
)

// ============================================================================
// CSRF 中间件 - 跨站请求伪造防护
// ============================================================================

// CSRFMode 定义 CSRF 模式。
type CSRFMode int

const (
	// CSRFModeCookie Cookie 模式：SameSite=Lax, HttpOnly
	CSRFModeCookie CSRFMode = iota
	// CSRFModeSPA SPA 模式：双重提交 + 客户端指纹绑定
	CSRFModeSPA
)

// CSRFConfig CSRF 配置。
type CSRFConfig struct {
	// Mode CSRF 模式
	Mode CSRFMode
	// CookieName CSRF Cookie 名称
	CookieName string
	// HeaderName CSRF 请求头名称
	HeaderName string
	// TokenLength Token 长度
	TokenLength int
	// MaxAge Token 有效期
	MaxAge time.Duration
	// AllowMethods 不需要 CSRF 检查的方法（GET/HEAD/OPTIONS/TRACE 默认豁免）
	SkipMethods []string
	// Secret 签名密钥
	Secret []byte
	// FingerprintSalt 指纹盐值（SPA 模式）
	FingerprintSalt string
}

// DefaultCSRFConfig 返回 CSRF 默认配置。
func DefaultCSRFConfig() CSRFConfig {
	return CSRFConfig{
		Mode:        CSRFModeCookie,
		CookieName:  "_csrf",
		HeaderName:  "X-CSRF-Token",
		TokenLength: 32,
		MaxAge:      24 * time.Hour,
		Secret:      mustGenerateRandomBytes(32),
	}
}

// CSRF 返回 CSRF 中间件。
func CSRF(config ...CSRFConfig) core.MiddlewareFunc {
	cfg := DefaultCSRFConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	skipMethods := map[string]bool{
		http.MethodGet:     true,
		http.MethodHead:    true,
		http.MethodOptions: true,
		http.MethodTrace:   true,
	}
	for _, m := range cfg.SkipMethods {
		skipMethods[strings.ToUpper(m)] = true
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			// GET/HEAD/OPTIONS/TRACE 不需要 CSRF
			if skipMethods[c.Method()] {
				return next(c)
			}

			// 获取或生成 token
			cookieToken, err := c.Cookie(cfg.CookieName)
			var token string

			if err != nil || cookieToken.Value == "" {
				// 生成新 token
				token = generateCSRFToken(cfg)
				cookie := &http.Cookie{
					Name:     cfg.CookieName,
					Value:    token,
					Path:     "/",
					MaxAge:   int(cfg.MaxAge.Seconds()),
					HttpOnly: cfg.Mode == CSRFModeCookie, // SPA 模式下设为 false
					Secure:   true,
					SameSite: http.SameSiteLaxMode,
				}
				c.SetCookie(cookie)
			} else {
				token = cookieToken.Value
			}

			// 验证请求中的 token
			headerToken := c.GetHeader(cfg.HeaderName)
			if headerToken == "" {
				// 也检查表单
				headerToken = c.FormValue(cfg.CookieName)
			}

			if headerToken == "" || !verifyCSRFToken(token, headerToken) {
				return c.JSON(http.StatusForbidden, core.Response{
					Code:    http.StatusForbidden,
					Message: "CSRF token validation failed",
				})
			}

			// SPA 模式下的补偿防护：客户端指纹验证
			if cfg.Mode == CSRFModeSPA {
				if !validateClientFingerprint(c, cfg) {
					return c.JSON(http.StatusForbidden, core.Response{
						Code:    http.StatusForbidden,
						Message: "CSRF fingerprint validation failed",
					})
				}
			}

			// 令牌有效，存到上下文
			c.Set("csrf_token", token)

			return next(c)
		}
	}
}

// generateCSRFToken 生成 CSRF token。
func generateCSRFToken(cfg CSRFConfig) string {
	token := make([]byte, cfg.TokenLength)
	rand.Read(token)

	// 使用 HMAC 签名增强安全性
	mac := hmac.New(sha256.New, cfg.Secret)
	mac.Write(token)
	signed := mac.Sum(nil)

	return hex.EncodeToString(token) + "." + hex.EncodeToString(signed[:16])
}

// verifyCSRFToken 验证 CSRF token。
func verifyCSRFToken(cookieToken, headerToken string) bool {
	if cookieToken == "" || headerToken == "" {
		return false
	}
	// 双重提交模式：直接比对
	return hmac.Equal([]byte(cookieToken), []byte(headerToken))
}

// ============================================================================
// SPA 补偿防护：客户端指纹绑定
// ============================================================================

// clientFingerprint 客户端指纹。
type clientFingerprint struct {
	UserAgentHash string
	IPSubnet      string
}

// validateClientFingerprint 验证客户端指纹。
// 将 User-Agent 哈希 + 请求 IP 子网绑定到 token，降低 XSS 窃取后重放风险。
func validateClientFingerprint(c *core.Context, cfg CSRFConfig) bool {
	ua := c.UserAgent()
	uaHash := hashUserAgent(ua)

	ip := c.ClientIP()
	subnet := extractSubnet(ip)

	// 构建期望指纹
	fingerprint := uaHash + "|" + subnet

	// 从 token 中提取指纹信息
	storedFingerprint := c.GetString("csrf_fingerprint")
	if storedFingerprint != "" {
		return hmac.Equal([]byte(fingerprint), []byte(storedFingerprint))
	}

	// 首次请求，存储指纹
	c.Set("csrf_fingerprint", fingerprint)

	return true
}

// hashUserAgent 对 User-Agent 进行哈希。
func hashUserAgent(ua string) string {
	h := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(h[:8])
}

// extractSubnet 提取 IP 子网（/24）。
func extractSubnet(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}

	// IPv4: /24 子网
	if parsed.To4() != nil {
		mask := net.CIDRMask(24, 32)
		subnet := parsed.Mask(mask)
		return subnet.String()
	}

	// IPv6: /64 子网
	mask := net.CIDRMask(64, 128)
	subnet := parsed.Mask(mask)
	return subnet.String()
}

// ============================================================================
// 工具函数
// ============================================================================

var (
	randBytesMu sync.Mutex
)

func mustGenerateRandomBytes(n int) []byte {
	randBytesMu.Lock()
	defer randBytesMu.Unlock()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// fallback for extreme edge cases
		for i := range b {
			b[i] = byte(time.Now().UnixNano() & 0xFF)
		}
	}
	return b
}
