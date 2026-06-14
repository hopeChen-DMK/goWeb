package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hopechen-dmk/goweb/core"
)

// ============================================================================
// AuthBasic 中间件 - HTTP Basic 认证
// ============================================================================

// CredentialValidator 凭证验证函数。
type CredentialValidator func(username, password string) (*core.AuthInfo, error)

// AuthBasic 返回 HTTP Basic 认证中间件。
func AuthBasic(validator CredentialValidator) core.MiddlewareFunc {
	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			authHeader := c.GetHeader("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Basic ") {
				c.SetHeader("WWW-Authenticate", `Basic realm="Restricted"`)
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Authorization required",
				})
			}

			payload, err := base64.StdEncoding.DecodeString(authHeader[6:])
			if err != nil {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Invalid authorization header",
				})
			}

			pair := strings.SplitN(string(payload), ":", 2)
			if len(pair) != 2 {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Invalid authorization format",
				})
			}

			info, err := validator(pair[0], pair[1])
			if err != nil {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Invalid credentials",
				})
			}

			c.SetAuth(info)
			return next(c)
		}
	}
}

// ============================================================================
// AuthAPIKey 中间件 - API Key 认证
// ============================================================================

// APIKeyConfig API Key 配置。
type APIKeyConfig struct {
	// HeaderName API Key 请求头名称
	HeaderName string
	// QueryParam API Key 查询参数名称
	QueryParam string
	// Prefix API Key 前缀（如 "Bearer " 或 "ApiKey "）
	Prefix string
	// Validator API Key 验证函数
	Validator func(apiKey string) (*core.AuthInfo, error)
}

// DefaultAPIKeyConfig 返回默认 API Key 配置。
func DefaultAPIKeyConfig() APIKeyConfig {
	return APIKeyConfig{
		HeaderName: "X-API-Key",
		QueryParam: "api_key",
		Prefix:     "",
	}
}

// AuthAPIKey 返回 API Key 认证中间件。
func AuthAPIKey(config APIKeyConfig) core.MiddlewareFunc {
	if config.Validator == nil {
		panic("AuthAPIKey: Validator is required")
	}
	if config.HeaderName == "" {
		config.HeaderName = "X-API-Key"
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			// 尝试多种提取位置
			apiKey := ""

			// 1. 从请求头提取
			if v := c.GetHeader(config.HeaderName); v != "" {
				apiKey = v
			}

			// 2. 从查询参数提取
			if apiKey == "" && config.QueryParam != "" {
				apiKey = c.QueryParam(config.QueryParam)
			}

			if apiKey == "" {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "API key required",
				})
			}

			// 去除前缀
			if config.Prefix != "" {
				apiKey = strings.TrimPrefix(apiKey, config.Prefix)
			}

			info, err := config.Validator(apiKey)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Invalid API key",
				})
			}

			c.SetAuth(info)
			return next(c)
		}
	}
}

// ============================================================================
// AuthChain 中间件 - 多策略认证链
// ============================================================================

// AuthChain 返回认证链中间件：任一认证策略通过即可。
func AuthChain(auths ...core.MiddlewareFunc) core.MiddlewareFunc {
	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			// 保存原始请求状态
			var lastErr error

			for _, auth := range auths {
				// 创建一个捕获中间件
				innerNext := func(cc *core.Context) error {
					// 认证通过
					return nil
				}

				err := auth(innerNext)(c)
				if err == nil {
					return next(c)
				}

				lastErr = err
				// 重置认证信息尝试下一个
				c.SetAuth(nil)
			}

			// 所有策略都失败
			return c.JSON(http.StatusUnauthorized, core.Response{
				Code:    http.StatusUnauthorized,
				Message: "Authentication failed: " + lastErr.Error(),
			})
		}
	}
}

// ============================================================================
// Session 中间件 - 会话管理
// ============================================================================

// SessionConfig 会话配置。
type SessionConfig struct {
	// CookieName 会话 Cookie 名称
	CookieName string
	// Store 会话存储引擎
	Store core.SessionStore
	// MaxAge 会话最大有效期
	MaxAge time.Duration
	// Secure 是否仅 HTTPS
	Secure bool
	// HttpOnly HttpOnly 标志
	HttpOnly bool
	// Domain Cookie Domain
	Domain string
	// Path Cookie Path
	Path string
}

// DefaultSessionConfig 返回默认会话配置。
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		CookieName: "_session",
		MaxAge:     24 * time.Hour,
		Secure:     true,
		HttpOnly:   true,
		Path:       "/",
	}
}

// Session 返回会话管理中间件。
func Session(config SessionConfig) core.MiddlewareFunc {
	if config.Store == nil {
		config.Store = NewMemorySessionStore()
	}
	if config.CookieName == "" {
		config.CookieName = "_session"
	}
	if config.Path == "" {
		config.Path = "/"
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			sessionID := ""

			// 尝试从 Cookie 获取会话 ID
			if cookie, err := c.Cookie(config.CookieName); err == nil {
				sessionID = cookie.Value
			}

			// 生成新会话 ID
			if sessionID == "" {
				sessionID = generateSessionID()
				cookie := &http.Cookie{
					Name:     config.CookieName,
					Value:    sessionID,
					Path:     config.Path,
					MaxAge:   int(config.MaxAge.Seconds()),
					HttpOnly: config.HttpOnly,
					Secure:   config.Secure,
					Domain:   config.Domain,
					SameSite: http.SameSiteLaxMode,
				}
				c.SetCookie(cookie)
			}

			// 从存储加载会话数据
			data, err := config.Store.Get(sessionID)
			if err != nil || data == nil {
				data = make(map[string]any)
			}

			// 将数据附加到上下文（仅引用，不持有存储连接）
			c.SetSessionData(data)
			c.Set("session_id", sessionID)
			c.SetSessionStore(config.Store)

			// 执行请求
			err = next(c)

			// 请求完成后持久化会话
			if saveErr := config.Store.Save(sessionID); saveErr != nil {
				if c.Logger() != nil {
					c.Logger().Error("failed to save session", "error", saveErr)
				}
			}

			return err
		}
	}
}

// ============================================================================
// 内存会话存储
// ============================================================================

// MemorySessionStore 内存会话存储实现。
type MemorySessionStore struct {
	mu   sync.RWMutex
	data map[string]sessionEntry
}

type sessionEntry struct {
	data    map[string]any
	expires time.Time
}

// NewMemorySessionStore 创建内存会话存储。
func NewMemorySessionStore() *MemorySessionStore {
	s := &MemorySessionStore{
		data: make(map[string]sessionEntry),
	}
	go s.cleanupLoop()
	return s
}

func (s *MemorySessionStore) Get(sessionID string) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.data[sessionID]
	if !ok {
		return make(map[string]any), nil
	}
	if time.Now().After(entry.expires) {
		return make(map[string]any), nil
	}

	// 深拷贝避免并发修改
	result := make(map[string]any, len(entry.data))
	for k, v := range entry.data {
		result[k] = v
	}
	return result, nil
}

func (s *MemorySessionStore) Set(sessionID string, data map[string]any, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data[sessionID] = sessionEntry{
		data:    data,
		expires: time.Now().Add(ttl),
	}
	return nil
}

func (s *MemorySessionStore) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, sessionID)
	return nil
}

func (s *MemorySessionStore) Exists(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.data[sessionID]
	if !ok {
		return false
	}
	return time.Now().Before(entry.expires)
}

// Save 持久化会话（与 Set 行为一致）。
func (s *MemorySessionStore) Save(sessionID string) error {
	// 内存存储在 Set 中已完成持久化，此处仅做兼容。
	return nil
}

// Release 释放资源（内存存储无连接池，空操作）。
func (s *MemorySessionStore) Release() {
	// 内存存储无全局连接需释放
}

func (s *MemorySessionStore) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for id, entry := range s.data {
			if now.After(entry.expires) {
				delete(s.data, id)
			}
		}
		s.mu.Unlock()
	}
}

// ============================================================================
// 工具函数
// ============================================================================

func generateSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// generateToken 生成安全随机 token。
func generateToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// SecureCompare 时序安全的字符串比较。
func SecureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ============================================================================
// TLS 工具
// ============================================================================

// TLSConfig 构建 TLS 配置。
func TLSConfig(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
