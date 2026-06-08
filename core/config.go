package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================================
// 配置管理核心实现
// ============================================================================

// AppConfig 核心配置实现：基于 map 的内存配置，支持 JSON/YAML 文件加载。
// 使用 atomic.Value 实现线程安全的热更新。
type AppConfig struct {
	data    atomic.Value // map[string]any
	mu      sync.RWMutex
	watchers []func(key string, value any)
}

// NewAppConfig 创建新的配置实例。
func NewAppConfig() *AppConfig {
	cfg := &AppConfig{}
	cfg.data.Store(make(map[string]any))
	return cfg
}

// ============================================================================
// Config 接口实现
// ============================================================================

// Get 获取配置值。
func (c *AppConfig) Get(key string) any {
	return c.getData()[key]
}

// GetString 获取字符串配置。
func (c *AppConfig) GetString(key string) string {
	v := c.Get(key)
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// GetInt 获取整数配置。
func (c *AppConfig) GetInt(key string) int {
	v := c.Get(key)
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case string:
		var i int
		fmt.Sscanf(val, "%d", &i)
		return i
	default:
		return 0
	}
}

// GetBool 获取布尔配置。
func (c *AppConfig) GetBool(key string) bool {
	v := c.Get(key)
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return strings.EqualFold(val, "true") || val == "1"
	case int:
		return val != 0
	case float64:
		return val != 0
	default:
		return false
	}
}

// GetDuration 获取时间间隔配置。
func (c *AppConfig) GetDuration(key string) time.Duration {
	v := c.Get(key)
	switch val := v.(type) {
	case time.Duration:
		return val
	case string:
		d, _ := time.ParseDuration(val)
		return d
	case int:
		return time.Duration(val) * time.Second
	case float64:
		return time.Duration(val) * time.Second
	default:
		return 0
	}
}

// GetStringSlice 获取字符串切片配置。
func (c *AppConfig) GetStringSlice(key string) []string {
	v := c.Get(key)
	switch val := v.(type) {
	case []string:
		return val
	case []interface{}:
		result := make([]string, len(val))
		for i, item := range val {
			result[i] = fmt.Sprintf("%v", item)
		}
		return result
	case string:
		return strings.Split(val, ",")
	default:
		return nil
	}
}

// Set 设置配置值。
func (c *AppConfig) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data := c.copyData()
	data[key] = value
	c.data.Store(data)

	// 通知监听器
	for _, w := range c.watchers {
		w(key, value)
	}
}

// LoadFile 从文件加载配置。
func (c *AppConfig) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("load config file: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(path))

	var raw map[string]any
	switch ext {
	case ".json":
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse json config: %w", err)
		}
	case ".yaml", ".yml":
		return fmt.Errorf("yaml support requires extension module: import goweb/config/yaml")
	case ".toml":
		return fmt.Errorf("toml support requires extension module: import goweb/config/toml")
	default:
		return fmt.Errorf("unsupported config format: %s", ext)
	}

	// 原子替换整个配置
	c.mu.Lock()
	merged := c.copyData()
	for k, v := range raw {
		merged[k] = v
	}
	c.data.Store(merged)
	c.mu.Unlock()

	return nil
}

// Watch 注册配置变更回调。
func (c *AppConfig) Watch(callback func(key string, value any)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watchers = append(c.watchers, callback)
}

// Unmarshal 将配置反序列化到结构体。
func (c *AppConfig) Unmarshal(v interface{}) error {
	data := c.getData()
	return mapToStruct(data, v)
}

// UnmarshalKey 将指定 key 的配置反序列化。
func (c *AppConfig) UnmarshalKey(key string, v interface{}) error {
	data := c.getData()
	val := data[key]
	if val == nil {
		return fmt.Errorf("config key not found: %s", key)
	}
	if m, ok := val.(map[string]any); ok {
		return mapToStruct(m, v)
	}
	return fmt.Errorf("config key %s is not a map", key)
}

// ============================================================================
// 内部辅助方法
// ============================================================================

func (c *AppConfig) getData() map[string]any {
	return c.data.Load().(map[string]any)
}

func (c *AppConfig) copyData() map[string]any {
	old := c.getData()
	newMap := make(map[string]any, len(old)+4)
	for k, v := range old {
		newMap[k] = v
	}
	return newMap
}

// ============================================================================
// 热更新：配置对象整体替换
// ============================================================================

// ReplaceConfig 原子替换整个配置对象。
func (c *AppConfig) ReplaceConfig(newData map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.Store(newData)

	for key, value := range newData {
		for _, w := range c.watchers {
			w(key, value)
		}
	}
}

// ============================================================================
// map 到 struct 的工具函数
// ============================================================================

func mapToStruct(data map[string]any, v interface{}) error {
	// 先通过 JSON 中间转换
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ============================================================================
// 反射辅助：表单绑定
// ============================================================================

func bindFormToStruct(form urlValues, v interface{}) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("bind target must be a non-nil pointer")
	}

	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("bind target must be a struct")
	}

	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" {
			tag = field.Tag.Get("form")
		}
		if tag == "" || tag == "-" {
			continue
		}

		// 去掉 tag 中的选项（如 omitempty）
		if idx := strings.Index(tag, ","); idx != -1 {
			tag = tag[:idx]
		}

		vals, ok := form[tag]
		if !ok || len(vals) == 0 {
			continue
		}

		fv := rv.Field(i)
		if !fv.CanSet() {
			continue
		}

		setFieldValue(fv, vals[0])
	}

	return nil
}

// urlValues 兼容 url.Values 和 map[string][]string
type urlValues = map[string][]string

func setFieldValue(fv reflect.Value, val string) {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(val)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var i int64
		fmt.Sscanf(val, "%d", &i)
		fv.SetInt(i)
	case reflect.Float32, reflect.Float64:
		var f float64
		fmt.Sscanf(val, "%f", &f)
		fv.SetFloat(f)
	case reflect.Bool:
		fv.SetBool(strings.EqualFold(val, "true") || val == "1")
	}
}
