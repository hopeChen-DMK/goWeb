package middleware

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/goweb-framework/goweb/core"
)

// ============================================================================
// 限流中间件 - 令牌桶（内存实现）
// ============================================================================

// RateLimiterConfig 限流配置。
type RateLimiterConfig struct {
	// Rate 令牌填充速率（每 Period 生成多少令牌）
	Rate int
	// Burst 桶容量
	Burst int
	// Period 填充周期
	Period time.Duration
	// KeyFunc 限流键生成函数（默认使用 IP）
	KeyFunc func(c *core.Context) string
	// Store 可插拔存储后端（nil 则使用内存）
	Store core.RateLimiterStore
	// Skip 跳过限流的条件
	Skip func(c *core.Context) bool
}

// DefaultRateLimiterConfig 返回默认限流配置。
func DefaultRateLimiterConfig() RateLimiterConfig {
	return RateLimiterConfig{
		Rate:   100,
		Burst:  200,
		Period: time.Second,
		KeyFunc: func(c *core.Context) string {
			// 默认按 IP + 路径限流
			return c.ClientIP() + ":" + c.Path()
		},
	}
}

// RateLimiter 返回 IP/自定义头/路径令牌桶限流中间件。
func RateLimiter(config ...RateLimiterConfig) core.MiddlewareFunc {
	cfg := DefaultRateLimiterConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	var store core.RateLimiterStore
	if cfg.Store != nil {
		store = cfg.Store
	} else {
		store = newMemoryLimiterStore()
	}

	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			// 跳过条件
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}

			key := cfg.KeyFunc(c)
			allowed, retryAfter, err := store.Allow(key, cfg.Rate, cfg.Burst, cfg.Period)
			if err != nil {
				if c.Logger() != nil {
					c.Logger().Error("rate limiter error", "error", err)
				}
				return next(c) // 降级放行
			}

			if !allowed {
				c.SetHeader("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
				return c.JSON(http.StatusTooManyRequests, core.Response{
					Code:    http.StatusTooManyRequests,
					Message: "Rate limit exceeded. Retry after " + retryAfter.String(),
				})
			}

			return next(c)
		}
	}
}

// ============================================================================
// 内存令牌桶实现
// ============================================================================

type tokenBucket struct {
	tokens    float64
	lastTime  time.Time
	rate      float64 // tokens per second
	burst     float64
}

type memoryLimiterStore struct {
	mu     sync.Mutex
	buckets map[string]*tokenBucket
}

func newMemoryLimiterStore() *memoryLimiterStore {
	s := &memoryLimiterStore{
		buckets: make(map[string]*tokenBucket),
	}
	go s.cleanupLoop()
	return s
}

func (s *memoryLimiterStore) Allow(key string, rate int, burst int, period time.Duration) (bool, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket, exists := s.buckets[key]
	now := time.Now()

	if !exists {
		bucket = &tokenBucket{
			tokens:   float64(burst),
			lastTime: now,
			rate:     float64(rate) / period.Seconds(),
			burst:    float64(burst),
		}
		s.buckets[key] = bucket
	}

	// 补充令牌
	elapsed := now.Sub(bucket.lastTime).Seconds()
	bucket.tokens = math.Min(bucket.burst, bucket.tokens+elapsed*bucket.rate)
	bucket.lastTime = now

	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0, nil
	}

	// 计算需要等待的时间
	waitTime := time.Duration((1-bucket.tokens)/bucket.rate*1000) * time.Millisecond
	return false, waitTime, nil
}

func (s *memoryLimiterStore) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		cutoff := time.Now().Add(-30 * time.Minute)
		for key, bucket := range s.buckets {
			if bucket.lastTime.Before(cutoff) {
				delete(s.buckets, key)
			}
		}
		s.mu.Unlock()
	}
}

// ============================================================================
// 连接数限制
// ============================================================================

// ConnLimiter 基于信号量的连接数限制器。
type ConnLimiter struct {
	sem     chan struct{}
	timeout time.Duration
}

// NewConnLimiter 创建连接数限制器。
func NewConnLimiter(maxConns int, timeout time.Duration) *ConnLimiter {
	return &ConnLimiter{
		sem:     make(chan struct{}, maxConns),
		timeout: timeout,
	}
}

// Acquire 获取连接许可。
func (l *ConnLimiter) Acquire() bool {
	if l.timeout > 0 {
		select {
		case l.sem <- struct{}{}:
			return true
		case <-time.After(l.timeout):
			return false
		}
	}
	select {
	case l.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release 释放连接许可。
func (l *ConnLimiter) Release() {
	<-l.sem
}

// Available 返回当前可用槽位。
func (l *ConnLimiter) Available() int {
	return cap(l.sem) - len(l.sem)
}

// ConnLimitMiddleware 连接数限制中间件（全局）。
func ConnLimitMiddleware(limiter *ConnLimiter) core.MiddlewareFunc {
	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			if !limiter.Acquire() {
				c.SetHeader("Retry-After", "5")
				return c.JSON(http.StatusServiceUnavailable, core.Response{
					Code:    http.StatusServiceUnavailable,
					Message: "Server is at capacity. Please try again later.",
				})
			}
			defer limiter.Release()
			return next(c)
		}
	}
}
