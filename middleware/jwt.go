package middleware

import (
	"crypto"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/goweb-framework/goweb/core"
)

// ============================================================================
// JWT 中间件
// ============================================================================

// Supported algorithms.
const (
	JWTAlgHS256 = "HS256"
	JWTAlgRS256 = "RS256"
	JWTAlgEdDSA = "EdDSA"
)

// JWTConfig JWT 配置。
type JWTConfig struct {
	// Algorithm 签名算法
	Algorithm string
	// Secret HMAC 密钥（HS256）
	Secret []byte
	// PublicKey RSA/Ed25519 公钥（PEM 格式）
	PublicKey []byte
	// PrivateKey 私钥（用于签名，服务端可选）
	PrivateKey []byte
	// TokenLookup 提取位置："header:<name>,query:<name>,cookie:<name>"
	TokenLookup string
	// AuthScheme 认证方案（默认 Bearer）
	AuthScheme string
	// ContextKey 上下文存储键
	ContextKey string
	// Claims 声明工厂
	Claims ClaimsFactory
	// MaxRefresh 最大刷新次数
	MaxRefresh int
	// RefreshRotation 刷新时是否轮换
	RefreshRotation bool
	// SigningMethod 自定义签名方法
	SigningMethod SigningMethod
	// KeyFunc 自定义密钥获取函数
	KeyFunc KeyFunc
	// Blocklist 黑名单存储（用于登出/吊销）
	Blocklist JWTBlocklist
	// Validator 额外验证器（如设备绑定）
	Validator ExtraValidator
}

// ClaimsFactory 声明工厂函数。
type ClaimsFactory func() JWTClaims

// SigningMethod 签名方法接口。
type SigningMethod interface {
	Verify(signingString, signature string, key interface{}) error
	Sign(signingString string, key interface{}) (string, error)
	Alg() string
}

// KeyFunc 密钥获取函数。
type KeyFunc func(token *JWTToken) (interface{}, error)

// ExtraValidator 额外验证器。
type ExtraValidator func(c *core.Context, claims JWTClaims) error

// JWTToken JWT 解析后的 token。
type JWTToken struct {
	Raw       string
	Header    map[string]interface{}
	Claims    JWTClaims
	Signature string
	Valid     bool
}

// JWTClaims 标准 JWT 声明（可扩展）。
type JWTClaims interface {
	Valid() error
	Get(key string) (interface{}, bool)
	Set(key string, value interface{})
	GetSubject() string
	GetExpiration() time.Time
	GetIssuedAt() time.Time
}

// StandardClaims 标准 JWT 声明。
type StandardClaims struct {
	Issuer    string                 `json:"iss,omitempty"`
	Subject   string                 `json:"sub,omitempty"`
	Audience  string                 `json:"aud,omitempty"`
	ExpiresAt int64                  `json:"exp,omitempty"`
	NotBefore int64                  `json:"nbf,omitempty"`
	IssuedAt  int64                  `json:"iat,omitempty"`
	ID        string                 `json:"jti,omitempty"`
	Roles     []string               `json:"roles,omitempty"`
	Extra     map[string]interface{} `json:"ext,omitempty"`
}

func (s *StandardClaims) Valid() error {
	now := time.Now().Unix()

	if s.ExpiresAt > 0 && now > s.ExpiresAt {
		return fmt.Errorf("token is expired")
	}
	if s.NotBefore > 0 && now < s.NotBefore {
		return fmt.Errorf("token is not valid yet")
	}
	return nil
}

func (s *StandardClaims) Get(key string) (interface{}, bool) {
	if s.Extra == nil {
		return nil, false
	}
	v, ok := s.Extra[key]
	return v, ok
}

func (s *StandardClaims) Set(key string, value interface{}) {
	if s.Extra == nil {
		s.Extra = make(map[string]interface{})
	}
	s.Extra[key] = value
}

func (s *StandardClaims) GetSubject() string         { return s.Subject }
func (s *StandardClaims) GetExpiration() time.Time   { return time.Unix(s.ExpiresAt, 0) }
func (s *StandardClaims) GetIssuedAt() time.Time     { return time.Unix(s.IssuedAt, 0) }

// JWTBlocklist JWT 黑名单接口。
type JWTBlocklist interface {
	Add(jti string, exp time.Time) error
	IsBlocked(jti string) bool
}

// ============================================================================
// JWT 中间件工厂
// ============================================================================

// DefaultJWTConfig 返回默认 JWT 配置。
func DefaultJWTConfig() JWTConfig {
	return JWTConfig{
		Algorithm:        JWTAlgHS256,
		TokenLookup:      "header:Authorization",
		AuthScheme:       "Bearer",
		ContextKey:       "user",
		MaxRefresh:       1,
		RefreshRotation:  true,
	}
}

// AuthJWT 返回 JWT 认证中间件。
func AuthJWT(config ...JWTConfig) core.MiddlewareFunc {
	cfg := DefaultJWTConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	// 解析提取位置
	extractors := parseTokenExtractors(cfg.TokenLookup)

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			// 提取 token
			var tokenString string
			for _, extract := range extractors {
				tokenString = extract(c)
				if tokenString != "" {
					break
				}
			}

			if tokenString == "" {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Missing or malformed JWT",
				})
			}

			// 去掉 AuthScheme 前缀
			tokenString = strings.TrimPrefix(tokenString, cfg.AuthScheme+" ")

			// 解析并验证
			token, err := parseJWT(tokenString, cfg)
			if err != nil || !token.Valid {
				return c.JSON(http.StatusUnauthorized, core.Response{
					Code:    http.StatusUnauthorized,
					Message: "Invalid or expired JWT: " + err.Error(),
				})
			}

			// 检查黑名单
			if cfg.Blocklist != nil {
				if jti, ok := token.Claims.Get("jti"); ok {
					if jtiStr, ok := jti.(string); ok {
						if cfg.Blocklist.IsBlocked(jtiStr) {
							return c.JSON(http.StatusUnauthorized, core.Response{
								Code:    http.StatusUnauthorized,
								Message: "Token has been revoked",
							})
						}
					}
				}
			}

			// 构架认证信息
			var roles []string
			if sc, ok := token.Claims.(*StandardClaims); ok {
				roles = sc.Roles
			}

			authInfo := &core.AuthInfo{
				ID:        token.Claims.GetSubject(),
				Roles:     roles,
				Metadata:  make(map[string]any),
				ExpiresAt: token.Claims.GetExpiration(),
			}

			// 注入元数据
			if sc, ok := token.Claims.(*StandardClaims); ok && sc.Extra != nil {
				for k, v := range sc.Extra {
					authInfo.Metadata[k] = v
				}
			}

			c.SetAuth(authInfo)
			c.Set(cfg.ContextKey, token)

			return next(c)
		}
	}
}

// ============================================================================
// JWT 解析与验证
// ============================================================================

func parseJWT(tokenString string, cfg JWTConfig) (*JWTToken, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, errors.New("token contains an invalid number of segments")
	}

	token := &JWTToken{
		Raw: tokenString,
	}

	// 解析 Header
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}
	if err := json.Unmarshal(headerBytes, &token.Header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}

	// 解析 Claims
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	var claims JWTClaims
	if cfg.Claims != nil {
		claims = cfg.Claims()
	} else {
		claims = &StandardClaims{}
	}
	if err := json.Unmarshal(claimsBytes, claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}
	token.Claims = claims

	// 验证签名
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	token.Signature = hex.EncodeToString(signature)

	signingString := parts[0] + "." + parts[1]

	// 获取密钥
	var key interface{}
	if cfg.KeyFunc != nil {
		key, err = cfg.KeyFunc(token)
		if err != nil {
			return nil, fmt.Errorf("key func: %w", err)
		}
	} else {
		key = getDefaultKey(cfg)
	}

	// 验证
	if cfg.SigningMethod != nil {
		if err := cfg.SigningMethod.Verify(signingString, token.Signature, key); err != nil {
			return nil, fmt.Errorf("signature invalid: %w", err)
		}
	} else {
		if err := verifySignature(cfg.Algorithm, signingString, parts[2], key); err != nil {
			return nil, fmt.Errorf("signature invalid: %w", err)
		}
	}

	// 验证 Claims
	if err := claims.Valid(); err != nil {
		return nil, err
	}

	token.Valid = true
	return token, nil
}

func getDefaultKey(cfg JWTConfig) interface{} {
	switch cfg.Algorithm {
	case JWTAlgHS256:
		return cfg.Secret
	case JWTAlgRS256:
		if cfg.PublicKey != nil {
			return parseRSAPublicKey(cfg.PublicKey)
		}
	case JWTAlgEdDSA:
		if cfg.PublicKey != nil {
			return parseEd25519PublicKey(cfg.PublicKey)
		}
	}
	return cfg.Secret
}

func verifySignature(alg, signingString, signature string, key interface{}) error {
	sigBytes, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return err
	}

	switch alg {
	case JWTAlgHS256:
		mac := hmac.New(sha256.New, key.([]byte))
		mac.Write([]byte(signingString))
		if !hmac.Equal(mac.Sum(nil), sigBytes) {
			return errors.New("signature invalid")
		}
	case JWTAlgRS256:
		pubKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return errors.New("invalid RSA public key")
		}
		hash := sha256.Sum256([]byte(signingString))
		if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, hash[:], sigBytes); err != nil {
			return fmt.Errorf("rsa verify: %w", err)
		}
	case JWTAlgEdDSA:
		pubKey, ok := key.(ed25519.PublicKey)
		if !ok {
			return errors.New("invalid Ed25519 public key")
		}
		if !ed25519.Verify(pubKey, []byte(signingString), sigBytes) {
			return errors.New("ed25519 verify failed")
		}
	default:
		return fmt.Errorf("unsupported algorithm: %s", alg)
	}
	return nil
}

// ============================================================================
// 密钥解析辅助函数
// ============================================================================

func parseRSAPublicKey(data []byte) *rsa.PublicKey {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil
	}
	key, _ := pub.(*rsa.PublicKey)
	return key
}

func parseEd25519PublicKey(data []byte) ed25519.PublicKey {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil
	}
	key, _ := pub.(ed25519.PublicKey)
	return key
}

// ============================================================================
// Token 提取器
// ============================================================================

type tokenExtractor func(c *core.Context) string

func parseTokenExtractors(lookup string) []tokenExtractor {
	extractors := make([]tokenExtractor, 0)

	for _, part := range strings.Split(lookup, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "header:"):
			headerName := part[7:]
			extractors = append(extractors, func(c *core.Context) string {
				return c.GetHeader(headerName)
			})
		case strings.HasPrefix(part, "query:"):
			queryName := part[6:]
			extractors = append(extractors, func(c *core.Context) string {
				return c.QueryParam(queryName)
			})
		case strings.HasPrefix(part, "cookie:"):
			cookieName := part[7:]
			extractors = append(extractors, func(c *core.Context) string {
				cookie, err := c.Cookie(cookieName)
				if err != nil {
					return ""
				}
				return cookie.Value
			})
		}
	}

	return extractors
}

// ============================================================================
// 黑名单实现（内存）
// ============================================================================

// MemoryBlocklist 内存 JWT 黑名单。
type MemoryBlocklist struct {
	mu     sync.RWMutex
	tokens map[string]time.Time
}

// NewMemoryBlocklist 创建内存黑名单。
func NewMemoryBlocklist() *MemoryBlocklist {
	bl := &MemoryBlocklist{
		tokens: make(map[string]time.Time),
	}
	go bl.cleanupLoop()
	return bl
}

func (m *MemoryBlocklist) Add(jti string, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[jti] = exp
	return nil
}

func (m *MemoryBlocklist) IsBlocked(jti string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	exp, ok := m.tokens[jti]
	if !ok {
		return false
	}
	return time.Now().Before(exp)
}

func (m *MemoryBlocklist) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		now := time.Now()
		for jti, exp := range m.tokens {
			if now.After(exp) {
				delete(m.tokens, jti)
			}
		}
		m.mu.Unlock()
	}
}

// ============================================================================
// JWT 签名辅助（用于服务端生成 Token）
// ============================================================================

// GenerateJWT 生成 JWT token。
func GenerateJWT(claims JWTClaims, cfg JWTConfig) (string, error) {
	header := map[string]interface{}{
		"alg": cfg.Algorithm,
		"typ": "JWT",
	}

	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingString := headerB64 + "." + claimsB64

	var key interface{}
	if cfg.PrivateKey != nil {
		key = cfg.PrivateKey
	} else {
		key = cfg.Secret
	}

	var signature string
	var err error

	switch cfg.Algorithm {
	case JWTAlgHS256:
		mac := hmac.New(sha256.New, key.([]byte))
		mac.Write([]byte(signingString))
		signature = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	default:
		return "", fmt.Errorf("signing not supported for algorithm %s in core - use extension", cfg.Algorithm)
	}

	if err != nil {
		return "", err
	}

	return signingString + "." + signature, nil
}

// ============================================================================
// 环境变量密钥加载
// ============================================================================

// LoadSecretFromEnv 从环境变量加载密钥。
func LoadSecretFromEnv(envName string) []byte {
	return []byte(os.Getenv(envName))
}

// LoadKeyFromFile 从文件加载 PEM 密钥。
func LoadKeyFromFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
