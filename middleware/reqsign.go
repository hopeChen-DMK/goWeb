package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hopeChen-DMK/goWeb/core"
)

// ============================================================================
// 请求签名中间件（HMAC-SHA256）
// ============================================================================

// SignatureConfig 请求签名配置。
type SignatureConfig struct {
	// Secret 签名密钥
	Secret []byte
	// KeyFunc 根据请求上下文获取密钥的函数（优先于 Secret）
	KeyFunc func(c *core.Context) ([]byte, error)
	// MaxAge 允许的最大时间戳偏差
	MaxAge time.Duration
	// SkipMethods 跳过签名验证的方法
	SkipMethods []string
}

// DefaultSignatureConfig 返回默认签名配置。
func DefaultSignatureConfig() SignatureConfig {
	return SignatureConfig{
		MaxAge:      5 * time.Minute,
		SkipMethods: []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace},
	}
}

// RequestSignature 返回请求签名验证中间件。
// 客户端使用 HMAC-SHA256 对规范字符串签名，放入 Authorization: Signature ... 头。
// 规范字符串格式: "METHOD\nPATH\nTIMESTAMP\nBODY_HASH"
func RequestSignature(config SignatureConfig) core.MiddlewareFunc {
	if config.MaxAge <= 0 {
		config.MaxAge = 5 * time.Minute
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			// 跳过指定方法
			for _, m := range config.SkipMethods {
				if strings.EqualFold(c.Method(), m) {
					return next(c)
				}
			}

			auth := c.GetHeader("Authorization")
			if auth == "" || !strings.HasPrefix(auth, "Signature ") {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Missing signature",
				})
			}

			// 解析 Authorization: Signature keyId=...,signature=...,timestamp=...
			params := parseSignatureParams(auth[10:])
			timestamp := params["timestamp"]
			clientSig := params["signature"]

			if timestamp == "" || clientSig == "" {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Invalid signature format",
				})
			}

			// 验证时间戳窗口
			ts, err := strconv.ParseInt(timestamp, 10, 64)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Invalid timestamp",
				})
			}
			reqTime := time.Unix(ts, 0)
			if time.Since(reqTime) > config.MaxAge || time.Since(reqTime) < -config.MaxAge {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Request expired",
				})
			}

			// 获取密钥
			secret := config.Secret
			if config.KeyFunc != nil {
				s, err := config.KeyFunc(c)
				if err != nil {
					return c.JSON(http.StatusUnauthorized, core.Response{
						Code:    http.StatusUnauthorized,
						Message: "Signature key error",
					})
				}
				secret = s
			}
			if len(secret) == 0 {
				return c.JSON(http.StatusInternalServerError, core.Response{
					Code:    http.StatusInternalServerError,
					Message: "Signature secret not configured",
				})
			}

			// 计算请求体哈希
			bodyHash := hashRequestBody(c)

			// 构建规范字符串并验证签名
			canonical := fmt.Sprintf("%s\n%s\n%s\n%s", c.Method(), c.Path(), timestamp, bodyHash)
			expectedSig := computeHMACSignature(secret, canonical)

			if !hmac.Equal([]byte(clientSig), []byte(expectedSig)) {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Invalid signature",
				})
			}

			return next(c)
		}
	}
}

// parseSignatureParams 解析签名字符串中的键值对。
func parseSignatureParams(s string) map[string]string {
	result := make(map[string]string)
	parts := strings.Split(s, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			result[kv[0]] = strings.Trim(kv[1], `"`)
		}
	}
	return result
}

// hashRequestBody 计算请求体 SHA-256 哈希。
func hashRequestBody(c *core.Context) string {
	body, err := c.GetRawBody()
	if err != nil || len(body) == 0 {
		return hex.EncodeToString(make([]byte, 32)) // 空体的 SHA-256
	}
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// computeHMACSignature 计算 HMAC-SHA256 签名。
func computeHMACSignature(secret []byte, message string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// ============================================================================
// 工具函数
// ============================================================================

// GenerateSignatureSecret 生成签名密钥。
func GenerateSignatureSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// BuildSignatureHeader 构建签名请求头（客户端使用）。
func BuildSignatureHeader(secret []byte, method, path string, body []byte) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	bodyHash := ""
	if len(body) > 0 {
		h := sha256.Sum256(body)
		bodyHash = hex.EncodeToString(h[:])
	} else {
		h := sha256.Sum256([]byte{})
		bodyHash = hex.EncodeToString(h[:])
	}
	canonical := fmt.Sprintf("%s\n%s\n%s\n%s", method, path, ts, bodyHash)
	sig := computeHMACSignature(secret, canonical)
	return fmt.Sprintf("Signature timestamp=%s,signature=%s", ts, sig)
}
