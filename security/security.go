// Package security 提供安全基础能力：验证器、密码管理、密钥管理。
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// ============================================================================
// 核心验证器（8 种基础规则）
// 支持: required, min, max, len, email, url, oneof, eqfield
// ============================================================================

// ValidateStruct 基于 struct tag 进行验证。
// 标签格式: `validate:"required,min=2,max=100,email"`
func ValidateStruct(v interface{}) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("validate: expected struct, got %s", rv.Kind())
	}

	rt := rv.Type()
	var errs []string

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		tag := field.Tag.Get("validate")
		if tag == "" {
			continue
		}

		fv := rv.Field(i)
		fieldName := field.Tag.Get("json")
		if fieldName == "" {
			fieldName = field.Name
		}

		rules := strings.Split(tag, ",")
		for _, rule := range rules {
			rule = strings.TrimSpace(rule)
			if err := applyRule(rule, fv, fieldName, rv, field); err != nil {
				errs = append(errs, err.Error())
				break // 该字段遇到第一个错误就停止
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func applyRule(rule string, fv reflect.Value, fieldName string, parent reflect.Value, field reflect.StructField) error {
	switch {
	case rule == "required":
		return validateRequired(fv, fieldName)
	case strings.HasPrefix(rule, "min="):
		min := rule[4:]
		return validateMin(fv, fieldName, min)
	case strings.HasPrefix(rule, "max="):
		max := rule[4:]
		return validateMax(fv, fieldName, max)
	case strings.HasPrefix(rule, "len="):
		l := rule[4:]
		return validateLen(fv, fieldName, l)
	case rule == "email":
		return validateEmail(fv, fieldName)
	case rule == "url":
		return validateURL(fv, fieldName)
	case strings.HasPrefix(rule, "oneof="):
		values := strings.Split(rule[6:], " ")
		return validateOneOf(fv, fieldName, values)
	case strings.HasPrefix(rule, "eqfield="):
		eqFieldName := rule[8:]
		return validateEqField(fv, fieldName, eqFieldName, parent)
	}
	return nil
}

// validateRequired 验证非空（字符串去除空格后判断）。
func validateRequired(fv reflect.Value, name string) error {
	if fv.Kind() == reflect.String {
		if strings.TrimSpace(fv.String()) == "" {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if isZero(fv) {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

// validateMin 验证最小值/长度。
func validateMin(fv reflect.Value, name, minStr string) error {
	switch fv.Kind() {
	case reflect.String:
		var min int
		fmt.Sscanf(minStr, "%d", &min)
		if len(fv.String()) < min {
			return fmt.Errorf("%s must be at least %s characters", name, minStr)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var min int64
		fmt.Sscanf(minStr, "%d", &min)
		if fv.Int() < min {
			return fmt.Errorf("%s must be at least %s", name, minStr)
		}
	case reflect.Float32, reflect.Float64:
		var min float64
		fmt.Sscanf(minStr, "%f", &min)
		if fv.Float() < min {
			return fmt.Errorf("%s must be at least %s", name, minStr)
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		var min int
		fmt.Sscanf(minStr, "%d", &min)
		if fv.Len() < min {
			return fmt.Errorf("%s must have at least %s items", name, minStr)
		}
	}
	return nil
}

// validateMax 验证最大值/长度。
func validateMax(fv reflect.Value, name, maxStr string) error {
	switch fv.Kind() {
	case reflect.String:
		var max int
		fmt.Sscanf(maxStr, "%d", &max)
		if len(fv.String()) > max {
			return fmt.Errorf("%s must be at most %s characters", name, maxStr)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var max int64
		fmt.Sscanf(maxStr, "%d", &max)
		if fv.Int() > max {
			return fmt.Errorf("%s must be at most %s", name, maxStr)
		}
	}
	return nil
}

// validateLen 验证精确长度。
func validateLen(fv reflect.Value, name, lenStr string) error {
	var length int
	fmt.Sscanf(lenStr, "%d", &length)

	switch fv.Kind() {
	case reflect.String:
		if len(fv.String()) != length {
			return fmt.Errorf("%s must be exactly %s characters", name, lenStr)
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		if fv.Len() != length {
			return fmt.Errorf("%s must have exactly %s items", name, lenStr)
		}
	}
	return nil
}

// validateEmail 验证邮箱格式。
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func validateEmail(fv reflect.Value, name string) error {
	if fv.Kind() != reflect.String {
		return nil
	}
	if !emailRegex.MatchString(fv.String()) {
		return fmt.Errorf("%s must be a valid email", name)
	}
	return nil
}

// validateURL 验证 URL 格式（要求 scheme 为 http 或 https）。
func validateURL(fv reflect.Value, name string) error {
	if fv.Kind() != reflect.String {
		return nil
	}
	if fv.String() == "" {
		return nil
	}
	u, err := url.ParseRequestURI(fv.String())
	if err != nil {
		return fmt.Errorf("%s must be a valid URL", name)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s must be an http or https URL", name)
	}
	return nil
}

// validateOneOf 验证值在指定集合中。
func validateOneOf(fv reflect.Value, name string, values []string) error {
	if fv.Kind() != reflect.String {
		return nil
	}
	for _, v := range values {
		if fv.String() == v {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of [%s]", name, strings.Join(values, ", "))
}

// validateEqField 验证与另一个字段相等。
func validateEqField(fv reflect.Value, name, eqFieldName string, parent reflect.Value) error {
	eqFv := parent.FieldByName(eqFieldName)
	if !eqFv.IsValid() {
		return fmt.Errorf("eqfield '%s' not found for %s", eqFieldName, name)
	}
	if fv.String() != eqFv.String() {
		return fmt.Errorf("%s must equal %s", name, eqFieldName)
	}
	return nil
}

// isZero 判断值是否为零值。
func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.String() == ""
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Slice, reflect.Map, reflect.Array:
		return v.IsNil() || v.Len() == 0
	default:
		return false
	}
}

// ============================================================================
// 密码管理
// ============================================================================

const (
	// DefaultBcryptCost 默认 bcrypt 开销（10-12 推荐）
	DefaultBcryptCost = 12
)

// HashPassword 使用 bcrypt 哈希密码。
func HashPassword(password string, cost ...int) (string, error) {
	c := DefaultBcryptCost
	if len(cost) > 0 {
		c = cost[0]
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), c)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword 验证密码与 bcrypt 哈希。
func VerifyPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// ============================================================================
// AES-GCM 加密
// ============================================================================

const (
	// AESKeySize AES-256 密钥大小
	AESKeySize = 32
)

// GenerateAESKey 生成 AES-256 密钥。
func GenerateAESKey() ([]byte, error) {
	key := make([]byte, AESKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate AES key: %w", err)
	}
	return key, nil
}

// EncryptAESGCM 使用 AES-256-GCM 加密。
func EncryptAESGCM(plaintext, key []byte) (string, error) {
	if len(key) != AESKeySize {
		return "", errors.New("key must be 32 bytes for AES-256")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptAESGCM 使用 AES-256-GCM 解密。
func DecryptAESGCM(encoded string, key []byte) ([]byte, error) {
	if len(key) != AESKeySize {
		return nil, errors.New("key must be 32 bytes for AES-256")
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}

// ============================================================================
// 密钥管理接口
// ============================================================================

// KeySource 定义密钥来源。
type KeySource interface {
	GetKey(name string) ([]byte, error)
}

// KeyManager 密钥管理器。
// 支持环境变量、文件、KMS，禁止硬编码。
type KeyManager struct {
	sources  []KeySource
	onRotate []func(name string, oldKey, newKey []byte)
}

// NewKeyManager 创建密钥管理器。
func NewKeyManager(sources ...KeySource) *KeyManager {
	return &KeyManager{sources: sources}
}

// GetKey 按顺序从多个来源获取密钥。
func (m *KeyManager) GetKey(name string) ([]byte, error) {
	for _, src := range m.sources {
		key, err := src.GetKey(name)
		if err == nil && key != nil {
			return key, nil
		}
	}
	return nil, fmt.Errorf("key '%s' not found in any source", name)
}

// OnRotate 注册密钥轮换回调。当密钥被轮换时调用。
func (m *KeyManager) OnRotate(fn func(name string, oldKey, newKey []byte)) {
	m.onRotate = append(m.onRotate, fn)
}

// RotateKey 轮换指定名称的密钥，触发所有 OnRotate 回调。
func (m *KeyManager) RotateKey(name string, newKey []byte) error {
	oldKey, _ := m.GetKey(name)
	for _, fn := range m.onRotate {
		fn(name, oldKey, newKey)
	}
	return nil
}

// EnvKeySource 从环境变量获取密钥。
type EnvKeySource struct{}

func (s *EnvKeySource) GetKey(name string) ([]byte, error) {
	v := os.Getenv(name)
	if v == "" {
		return nil, fmt.Errorf("env var %s not set", name)
	}
	return []byte(v), nil
}

// FileKeySource 从文件获取密钥。
type FileKeySource struct {
	BasePath string
}

func (s *FileKeySource) GetKey(name string) ([]byte, error) {
	path := name
	if s.BasePath != "" {
		path = s.BasePath + "/" + name
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file %s: %w", path, err)
	}
	return data, nil
}
