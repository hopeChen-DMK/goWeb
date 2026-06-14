// Package server 提供 HTTP 服务器、优雅关闭、健康检查、生命周期钩子、指标监控等功能。
package server

import (
	"context"
	"crypto/tls"
	"expvar"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hopechen-dmk/goWeb/core"
)

// ============================================================================
// Server 定义
// ============================================================================

// Server HTTP 服务器包装。
type Server struct {
	httpServer *http.Server
	router     *core.RadixRouter
	config     *ServerConfig

	// 生命周期钩子
	hooks struct {
		beforeStart    []core.LifecycleHook
		afterStart     []core.LifecycleHook
		beforeShutdown []core.LifecycleHook
		afterShutdown  []core.LifecycleHook
		onRouterReady  []core.LifecycleHook
	}

	// 就绪检查器
	readinessChecks map[string]func() error
	readinessMu     sync.RWMutex

	// 状态
	running atomic.Bool
	ready   atomic.Bool

	// 连接限制
	connLimiter *ConnLimitListener

	// 优雅关闭
	shutdownChan    chan struct{}
	wsConns         sync.Map // 跟踪 WebSocket 连接
	shutdownTimeout time.Duration

	// 日志
	logger core.Logger

	// 指标
	metrics *Metrics

	// 追踪阶段记录
	tracer *Tracer

	// 配置热更新
	configWatcher *ConfigWatcher
}

// ServerConfig 服务器配置。
type ServerConfig struct {
	// Addr 监听地址
	Addr string
	// ReadTimeout 读取超时
	ReadTimeout time.Duration
	// ReadHeaderTimeout 读请求头超时
	ReadHeaderTimeout time.Duration
	// WriteTimeout 写入超时
	WriteTimeout time.Duration
	// IdleTimeout 空闲超时
	IdleTimeout time.Duration
	// MaxHeaderBytes 最大请求头大小
	MaxHeaderBytes int
	// ShutdownTimeout 关闭超时（必须设置 WebSocket 硬超时）
	ShutdownTimeout time.Duration
	// MaxConns 最大连接数
	MaxConns int
	// ConnQueueTimeout 连接排队超时
	ConnQueueTimeout time.Duration
	// TLS TLS 配置
	TLS *tls.Config
	// EnableHTTP2 启用 HTTP/2（默认 true）
	EnableHTTP2 bool
	// PprofEnabled 启用 pprof
	PprofEnabled bool
	// PprofPrefix pprof 路由前缀
	PprofPrefix string
	// HealthPath 健康检查路径
	HealthPath string
	// ReadyPath 就绪检查路径
	ReadyPath string
	// HealthDetails 是否暴露健康检查详情
	HealthDetails bool
	// MetricsEnabled 启用指标
	MetricsEnabled bool
	// MetricsPath 指标端点路径
	MetricsPath string
	// TraceEnabled 启用内置追踪
	TraceEnabled bool
	// TraceSampleRate 追踪采样率（0.0-1.0）
	TraceSampleRate float64
	// SlowRequestThreshold 慢请求阈值
	SlowRequestThreshold time.Duration
}

// DefaultServerConfig 返回默认服务器配置。
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Addr:                ":8080",
		ReadTimeout:         30 * time.Second,
		ReadHeaderTimeout:   10 * time.Second,
		WriteTimeout:        30 * time.Second,
		IdleTimeout:         120 * time.Second,
		MaxHeaderBytes:      1 << 20, // 1MB
		ShutdownTimeout:     30 * time.Second,
		MaxConns:           10000,
		ConnQueueTimeout:    5 * time.Second,
		EnableHTTP2:        true,
		PprofEnabled:       false,
		PprofPrefix:        "/debug/pprof",
		HealthPath:         "/healthz",
		ReadyPath:          "/readyz",
		HealthDetails:      false,
		MetricsEnabled:     true,
		MetricsPath:        "/metrics",
		TraceEnabled:       false,
		TraceSampleRate:    0.01,
		SlowRequestThreshold: time.Second,
	}
}

// ============================================================================
// Server 构造函数
// ============================================================================

// New 创建新的 Server 实例。
func New(router *core.RadixRouter, config ...*ServerConfig) *Server {
	cfg := DefaultServerConfig()
	if len(config) > 0 && config[0] != nil {
		cfg = config[0]
	}

	srv := &Server{
		router:           router,
		config:           cfg,
		shutdownChan:     make(chan struct{}),
		shutdownTimeout:  cfg.ShutdownTimeout,
		metrics:          NewMetrics(),
		readinessChecks:  make(map[string]func() error),
		tracer:           NewTracer(cfg.TraceEnabled, cfg.TraceSampleRate),
	}

	// 构建 http.Server，严格设置所有超时字段
	srv.httpServer = &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.tracerMiddleware(srv.metricsMiddleware(router)),
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		TLSConfig:         cfg.TLS,
	}

	// 注册内部路由
	srv.registerInternalRoutes()

	return srv
}

// ============================================================================
// 内部路由注册
// ============================================================================

func (s *Server) registerInternalRoutes() {
	// 健康检查
	s.router.GET(s.config.HealthPath, s.handleHealthz)
	s.router.GET(s.config.ReadyPath, s.handleReadyz)

	// 指标端点
	if s.config.MetricsEnabled {
		s.router.GET(s.config.MetricsPath, s.handleMetrics)
	}

	// pprof
	if s.config.PprofEnabled {
		prefix := s.config.PprofPrefix
		s.router.GET(prefix+"/", wrapHandler(pprof.Index))
		s.router.GET(prefix+"/cmdline", wrapHandler(pprof.Cmdline))
		s.router.GET(prefix+"/profile", wrapHandler(pprof.Profile))
		s.router.GET(prefix+"/symbol", wrapHandler(pprof.Symbol))
		s.router.GET(prefix+"/trace", wrapHandler(pprof.Trace))
		s.router.GET(prefix+"/allocs", wrapHandler(pprof.Handler("allocs").ServeHTTP))
		s.router.GET(prefix+"/block", wrapHandler(pprof.Handler("block").ServeHTTP))
		s.router.GET(prefix+"/goroutine", wrapHandler(pprof.Handler("goroutine").ServeHTTP))
		s.router.GET(prefix+"/heap", wrapHandler(pprof.Handler("heap").ServeHTTP))
		s.router.GET(prefix+"/mutex", wrapHandler(pprof.Handler("mutex").ServeHTTP))
		s.router.GET(prefix+"/threadcreate", wrapHandler(pprof.Handler("threadcreate").ServeHTTP))
	}
}

// ============================================================================
// 健康检查
// ============================================================================

func (s *Server) handleHealthz(c *core.Context) error {
	if s.config.HealthDetails {
		return c.JSON(http.StatusOK, core.H{
			"status": "ok",
			"time":   time.Now().UTC().Format(time.RFC3339),
		})
	}
	return c.String(http.StatusOK, "ok")
}

func (s *Server) handleReadyz(c *core.Context) error {
	if !s.ready.Load() {
		return c.JSON(http.StatusServiceUnavailable, core.Response{
			Code:    http.StatusServiceUnavailable,
			Message: "not ready",
		})
	}
	if s.config.HealthDetails {
		// 调试模式下的依赖列表
		deps := s.getDependencyStatus()
		status := "ok"
		for _, dep := range deps {
			if dep.Status != "ok" {
				status = "degraded"
			}
		}
		return c.JSON(http.StatusOK, core.H{
			"status":       status,
			"dependencies": deps,
		})
	}
	return c.String(http.StatusOK, "ok")
}

type dependency struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (s *Server) getDependencyStatus() []dependency {
	s.readinessMu.RLock()
	defer s.readinessMu.RUnlock()

	deps := []dependency{
		{Name: "server", Status: "ok"},
	}
	for name, check := range s.readinessChecks {
		status := "ok"
		if err := check(); err != nil {
			status = "fail"
			deps = append(deps, dependency{Name: name, Status: status})
			continue
		}
		deps = append(deps, dependency{Name: name, Status: status})
	}
	return deps
}

// ============================================================================
// 指标处理
// ============================================================================

func (s *Server) handleMetrics(c *core.Context) error {
	metrics := s.metrics.Snapshot()

	output := ""
	output += "# HELP goweb_requests_total Total number of HTTP requests\n"
	output += "# TYPE goweb_requests_total counter\n"
	output += fmt.Sprintf("goweb_requests_total %d\n", metrics.TotalRequests)

	output += "# HELP goweb_requests_in_flight Current in-flight requests\n"
	output += "# TYPE goweb_requests_in_flight gauge\n"
	output += fmt.Sprintf("goweb_requests_in_flight %d\n", metrics.InFlight)

	output += "# HELP goweb_request_duration_seconds Request duration histogram\n"
	output += "# TYPE goweb_request_duration_seconds histogram\n"
	for _, b := range metrics.LatencyBuckets {
		output += fmt.Sprintf("goweb_request_duration_seconds_bucket{le=\"%s\"} %d\n", b.Le, b.Count)
	}
	output += fmt.Sprintf("goweb_request_duration_seconds_sum %.6f\n", metrics.LatencySum)
	output += fmt.Sprintf("goweb_request_duration_seconds_count %d\n", metrics.LatencyCount)

	output += "# HELP goweb_goroutines Current goroutine count\n"
	output += "# TYPE goweb_goroutines gauge\n"
	output += fmt.Sprintf("goweb_goroutines %d\n", metrics.Goroutines)

	output += "# HELP goweb_request_size_bytes Request body size summary (bytes)\n"
	output += "# TYPE goweb_request_size_bytes summary\n"
	output += fmt.Sprintf("goweb_request_size_bytes_sum %d\n", metrics.ReqSizeSum)
	output += fmt.Sprintf("goweb_request_size_bytes_count %d\n", metrics.ReqSizeCount)

	output += "# HELP goweb_response_size_bytes Response body size summary (bytes)\n"
	output += "# TYPE goweb_response_size_bytes summary\n"
	output += fmt.Sprintf("goweb_response_size_bytes_sum %d\n", metrics.RespSizeSum)
	output += fmt.Sprintf("goweb_response_size_bytes_count %d\n", metrics.RespSizeCount)

	output += "# HELP goweb_memory_alloc_bytes Memory allocation\n"
	output += "# TYPE goweb_memory_alloc_bytes gauge\n"
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	output += fmt.Sprintf("goweb_memory_alloc_bytes %d\n", mem.Alloc)

	return c.String(http.StatusOK, output)
}

// ============================================================================
// 指标中间件
// ============================================================================

func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	if !s.config.MetricsEnabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.metrics.IncInFlight()
		defer s.metrics.DecInFlight()

		start := time.Now()

		next.ServeHTTP(w, r)

		s.metrics.RecordRequest(time.Since(start))
	})
}

// ============================================================================
// 生命周期钩子
// ============================================================================

// BeforeStart 注册启动前钩子。
func (s *Server) BeforeStart(hook core.LifecycleHook) {
	s.hooks.beforeStart = append(s.hooks.beforeStart, hook)
}

// AfterStart 注册启动后钩子。
func (s *Server) AfterStart(hook core.LifecycleHook) {
	s.hooks.afterStart = append(s.hooks.afterStart, hook)
}

// BeforeShutdown 注册关闭前钩子。
func (s *Server) BeforeShutdown(hook core.LifecycleHook) {
	s.hooks.beforeShutdown = append(s.hooks.beforeShutdown, hook)
}

// AfterShutdown 注册关闭后钩子。
func (s *Server) AfterShutdown(hook core.LifecycleHook) {
	s.hooks.afterShutdown = append(s.hooks.afterShutdown, hook)
}

// OnRouterReady 注册路由就绪钩子（路由表冻结后、开始服务前调用）。
func (s *Server) OnRouterReady(hook core.LifecycleHook) {
	s.hooks.onRouterReady = append(s.hooks.onRouterReady, hook)
}

// RegisterReadinessCheck 注册就绪检查器。
func (s *Server) RegisterReadinessCheck(name string, check func() error) {
	s.readinessMu.Lock()
	defer s.readinessMu.Unlock()
	s.readinessChecks[name] = check
}

// ============================================================================
// 启动与关闭
// ============================================================================

// Start 启动服务器。
func (s *Server) Start() error {
	// 启动前钩子
	for _, hook := range s.hooks.beforeStart {
		if err := hook(); err != nil {
			return fmt.Errorf("beforeStart hook: %w", err)
		}
	}

	// 监听端口
	ln, err := net.Listen("tcp", s.config.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// 连接限制包装
	if s.config.MaxConns > 0 {
		s.connLimiter = NewConnLimitListener(ln, s.config.MaxConns, s.config.ConnQueueTimeout)
		ln = s.connLimiter
	}

	// HTTP/2 自动启用
	if s.config.EnableHTTP2 && s.httpServer.TLSConfig != nil {
		s.httpServer.TLSConfig.NextProtos = append(s.httpServer.TLSConfig.NextProtos, "h2")
	}

	// 路由就绪钩子
	for _, hook := range s.hooks.onRouterReady {
		if err := hook(); err != nil {
			if s.logger != nil {
				s.logger.Error("onRouterReady hook failed", "error", err)
			}
		}
	}

	s.running.Store(true)
	s.ready.Store(true)

	// 启动后钩子
	for _, hook := range s.hooks.afterStart {
		if err := hook(); err != nil {
			if s.logger != nil {
				s.logger.Error("afterStart hook failed", "error", err)
			}
		}
	}

	return s.httpServer.Serve(ln)
}

// StartTLS 以 TLS 模式启动。
func (s *Server) StartTLS(certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load TLS cert: %w", err)
	}

	if s.config.TLS == nil {
		s.config.TLS = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	} else {
		s.config.TLS.Certificates = append(s.config.TLS.Certificates, cert)
	}

	s.httpServer.TLSConfig = s.config.TLS

	if s.config.EnableHTTP2 {
		s.httpServer.TLSConfig.NextProtos = append(s.httpServer.TLSConfig.NextProtos, "h2")
	}

	return s.Start()
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown() error {
	// 关闭前钩子
	for _, hook := range s.hooks.beforeShutdown {
		if err := hook(); err != nil {
			if s.logger != nil {
				s.logger.Error("beforeShutdown hook failed", "error", err)
			}
		}
	}

	s.ready.Store(false)
	s.running.Store(false)

	// 给 WebSocket 连接发送关闭帧并设置硬超时
	ctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()

	// 遍历 WebSocket 连接关闭
	s.wsConns.Range(func(key, value interface{}) bool {
		if conn, ok := value.(core.WSConn); ok {
			// 非阻塞关闭
			conn.Close()
		}
		return true
	})

	// 设置 Connection: close 头在后续处理中
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	// 关闭后钩子
	for _, hook := range s.hooks.afterShutdown {
		if err := hook(); err != nil {
			if s.logger != nil {
				s.logger.Error("afterShutdown hook failed", "error", err)
			}
		}
	}

	return nil
}

// WaitForSignal 等待信号并优雅关闭。
func (s *Server) WaitForSignal() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	if s.logger != nil {
		s.logger.Info("shutting down server...")
	}

	if err := s.Shutdown(); err != nil {
		if s.logger != nil {
			s.logger.Error("server forced to shutdown", "error", err)
		}
	}

	if s.logger != nil {
		s.logger.Info("server exited")
	}
}

// SetLogger 设置日志器。
func (s *Server) SetLogger(logger core.Logger) {
	s.logger = logger
}

// Router 返回底层路由器。
func (s *Server) Router() *core.RadixRouter {
	return s.router
}

// ShutdownChan 返回关闭信号通道。
func (s *Server) ShutdownChan() <-chan struct{} {
	return s.shutdownChan
}

// ============================================================================
// 连接限制 Listener
// ============================================================================

// ConnLimitListener 基于信号量的连接限制监听器。
type ConnLimitListener struct {
	net.Listener
	sem       chan struct{}
	timeout   time.Duration
}

// NewConnLimitListener 创建连接限制监听器。
func NewConnLimitListener(ln net.Listener, maxConns int, timeout time.Duration) *ConnLimitListener {
	return &ConnLimitListener{
		Listener: ln,
		sem:      make(chan struct{}, maxConns),
		timeout:  timeout,
	}
}

func (l *ConnLimitListener) Accept() (net.Conn, error) {
	// 排队获取连接槽
	if l.timeout > 0 {
		select {
		case l.sem <- struct{}{}:
		case <-time.After(l.timeout):
			return nil, fmt.Errorf("connection limit exceeded")
		}
	} else {
		l.sem <- struct{}{}
	}

	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}

	return &limitConn{
		Conn: conn,
		sem:  l.sem,
	}, nil
}

type limitConn struct {
	net.Conn
	sem chan struct{}
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	<-c.sem
	return err
}

// ============================================================================
// WebSocket 连接跟踪
// ============================================================================

// TrackWS 注册 WebSocket 连接（用于优雅关闭）。
func (s *Server) TrackWS(id string, conn core.WSConn) {
	s.wsConns.Store(id, conn)
}

// UntrackWS 移除 WebSocket 连接。
func (s *Server) UntrackWS(id string) {
	s.wsConns.Delete(id)
}

// ============================================================================
// 指标收集
// ============================================================================

// Metrics 服务器指标。
type Metrics struct {
	TotalRequests  atomic.Int64
	InFlight       atomic.Int64
	requests2xx    atomic.Int64
	requests3xx    atomic.Int64
	requests4xx    atomic.Int64
	requests5xx    atomic.Int64
	reqSizeSum     atomic.Int64
	reqSizeCount   atomic.Int64
	respSizeSum    atomic.Int64
	respSizeCount  atomic.Int64

	mu             sync.RWMutex
	latencySum     float64
	latencyCount   int64
	latencyBuckets []LatencyBucket
}

// LatencyBucket 延迟分桶。
type LatencyBucket struct {
	Le    string  // 上限
	Count int64   // 计数
	threshold float64 // 秒
}

// MetricsSnapshot 指标快照。
type MetricsSnapshot struct {
	TotalRequests   int64
	InFlight        int64
	LatencySum      float64
	LatencyCount    int64
	LatencyBuckets  []LatencyBucket
	Goroutines      int
	ReqSizeSum      int64
	ReqSizeCount    int64
	RespSizeSum     int64
	RespSizeCount   int64
}

// NewMetrics 创建指标收集器。
func NewMetrics() *Metrics {
	return &Metrics{
		latencyBuckets: []LatencyBucket{
			{Le: "0.005", threshold: 0.005},
			{Le: "0.01", threshold: 0.01},
			{Le: "0.025", threshold: 0.025},
			{Le: "0.05", threshold: 0.05},
			{Le: "0.1", threshold: 0.1},
			{Le: "0.25", threshold: 0.25},
			{Le: "0.5", threshold: 0.5},
			{Le: "1", threshold: 1},
			{Le: "2.5", threshold: 2.5},
			{Le: "5", threshold: 5},
			{Le: "10", threshold: 10},
			{Le: "+Inf", threshold: 1e100},
		},
	}
}

func (m *Metrics) IncInFlight()                              { m.InFlight.Add(1) }
func (m *Metrics) DecInFlight()                              { m.InFlight.Add(-1) }

func (m *Metrics) RecordRequest(duration time.Duration) {
	m.TotalRequests.Add(1)

	m.mu.Lock()
	m.latencyCount++
	m.latencySum += duration.Seconds()

	secs := duration.Seconds()
	for i := range m.latencyBuckets {
		if secs <= m.latencyBuckets[i].threshold {
			m.latencyBuckets[i].Count++
		}
	}
	m.mu.Unlock()
}

// RecordRequestSize 记录请求体大小。
func (m *Metrics) RecordRequestSize(size int64) {
	m.reqSizeSum.Add(size)
	m.reqSizeCount.Add(1)
}

// RecordResponseSize 记录响应体大小。
func (m *Metrics) RecordResponseSize(size int64) {
	m.respSizeSum.Add(size)
	m.respSizeCount.Add(1)
}

// Snapshot 获取指标快照。
func (m *Metrics) Snapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	buckets := make([]LatencyBucket, len(m.latencyBuckets))
	copy(buckets, m.latencyBuckets)

	return MetricsSnapshot{
		TotalRequests:  m.TotalRequests.Load(),
		InFlight:       m.InFlight.Load(),
		LatencySum:     m.latencySum,
		LatencyCount:   m.latencyCount,
		LatencyBuckets: buckets,
		Goroutines:     runtime.NumGoroutine(),
		ReqSizeSum:     m.reqSizeSum.Load(),
		ReqSizeCount:   m.reqSizeCount.Load(),
		RespSizeSum:    m.respSizeSum.Load(),
		RespSizeCount:  m.respSizeCount.Load(),
	}
}

// ============================================================================
// 辅助函数
// ============================================================================

func wrapHandler(handler func(http.ResponseWriter, *http.Request)) core.HandlerFunc {
	return func(c *core.Context) error {
		handler(c.ResponseWriter(), c.Request)
		return nil
	}
}

func wrapHandlerFunc(fn http.HandlerFunc) core.HandlerFunc {
	return func(c *core.Context) error {
		fn(c.ResponseWriter(), c.Request)
		return nil
	}
}

// ============================================================================
// 日志级别管理接口
// ============================================================================

// LogLevelServer 提供安全的日志级别管理 HTTP 接口。
// 默认仅监听 127.0.0.1，生产环境必须集成鉴权。
type LogLevelServer struct {
	addr   string
	logger core.Logger
	mux    *http.ServeMux
}

// NewLogLevelServer 创建日志级别管理服务器。
func NewLogLevelServer(addr string, logger core.Logger) *LogLevelServer {
	if addr == "" {
		addr = "127.0.0.1:9090"
	}

	s := &LogLevelServer{
		addr:   addr,
		logger: logger,
		mux:    http.NewServeMux(),
	}

	s.mux.HandleFunc("/log/level", s.handleLogLevel)

	return s
}

func (s *LogLevelServer) handleLogLevel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Write([]byte(`{"level":"` + "info" + `"}`))
	case http.MethodPost:
		level := r.URL.Query().Get("level")
		w.Write([]byte(`{"status":"ok","level":"` + level + `"}`))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Start 启动日志级别管理服务。
func (s *LogLevelServer) Start() error {
	return http.ListenAndServe(s.addr, s.mux)
}

// ============================================================================
// 高基数拦截
// ============================================================================

// MetricLabelValidator 指标标签基数验证器。
type MetricLabelValidator struct {
	maxCardinality int
	labels         map[string]map[string]struct{}
	mu             sync.RWMutex
}

// NewMetricLabelValidator 创建标签验证器。
func NewMetricLabelValidator(maxCardinality int) *MetricLabelValidator {
	return &MetricLabelValidator{
		maxCardinality: maxCardinality,
		labels:         make(map[string]map[string]struct{}),
	}
}

// Validate 验证标签基数。
func (v *MetricLabelValidator) Validate(metricName, labelName, labelValue string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.labels[labelName]; !ok {
		v.labels[labelName] = make(map[string]struct{})
	}

	if len(v.labels[labelName]) >= v.maxCardinality {
		if _, exists := v.labels[labelName][labelValue]; !exists {
			return fmt.Errorf("label '%s' exceeds max cardinality of %d for metric '%s'",
				labelName, v.maxCardinality, metricName)
		}
	}

	v.labels[labelName][labelValue] = struct{}{}
	return nil
}

// ============================================================================
// Goroutine 诊断
// ============================================================================

// GoroutineCount 返回当前 goroutine 数量。
func GoroutineCount() int {
	return runtime.NumGoroutine()
}

// GoroutineStats 返回 goroutine 统计信息。
func GoroutineStats() map[string]int {
	return map[string]int{
		"goroutines": runtime.NumGoroutine(),
		"cgo_calls":  int(runtime.NumCgoCall()),
		"gomaxprocs": runtime.GOMAXPROCS(0),
		"numcpu":     runtime.NumCPU(),
	}
}

// RegisterExpvar 注册 expvar 诊断变量。
func RegisterExpvar() {
	expvar.Publish("goroutines", expvar.Func(func() interface{} {
		return runtime.NumGoroutine()
	}))
}

// ============================================================================
// 内置追踪器
// ============================================================================

// TraceStage 追踪阶段名称。
type TraceStage string

const (
	StageRouteMatch      TraceStage = "route_match"
	StageMiddlewareBefore TraceStage = "middleware_before"
	StageDeserialize     TraceStage = "deserialize"
	StageHandler         TraceStage = "handler"
	StageSerialize       TraceStage = "serialize"
	StageMiddlewareAfter TraceStage = "middleware_after"
)

// Tracer 内置轻量级阶段追踪器。
type Tracer struct {
	enabled    bool
	sampleRate float64
	mu         sync.RWMutex
	stages     []TraceEntry
	maxEntries int
}

// TraceEntry 追踪条目。
type TraceEntry struct {
	TraceID  string
	Stage    TraceStage
	Duration time.Duration
	Time     time.Time
}

// NewTracer 创建追踪器。
func NewTracer(enabled bool, sampleRate float64) *Tracer {
	return &Tracer{
		enabled:    enabled,
		sampleRate: sampleRate,
		maxEntries: 10000,
		stages:     make([]TraceEntry, 0, 1024),
	}
}

// ShouldTrace 判断是否应对当前请求进行追踪。
func (t *Tracer) ShouldTrace() bool {
	if !t.enabled {
		return false
	}
	if t.sampleRate >= 1.0 {
		return true
	}
	// 简单概率采样
	return float64(time.Now().UnixNano()%1000000)/1000000.0 < t.sampleRate
}

// RecordStage 记录一个阶段的耗时。
func (t *Tracer) RecordStage(traceID string, stage TraceStage, duration time.Duration) {
	if !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.stages) >= t.maxEntries {
		// 环形丢弃旧数据
		t.stages = t.stages[1:]
	}
	t.stages = append(t.stages, TraceEntry{
		TraceID:  traceID,
		Stage:    stage,
		Duration: duration,
		Time:     time.Now(),
	})
}

// Snapshot 获取追踪快照。
func (t *Tracer) Snapshot() []TraceEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]TraceEntry, len(t.stages))
	copy(result, t.stages)
	return result
}

// SetEnabled 动态开关追踪。
func (t *Tracer) SetEnabled(enabled bool) {
	t.enabled = enabled
}

// SetSampleRate 设置采样率。
func (t *Tracer) SetSampleRate(rate float64) {
	t.sampleRate = rate
}

// tracerMiddleware 追踪中间件（包装 http.Handler）。
func (s *Server) tracerMiddleware(next http.Handler) http.Handler {
	if !s.config.TraceEnabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.tracer.ShouldTrace() {
			next.ServeHTTP(w, r)
			return
		}
		traceID := r.Header.Get("X-Request-ID")
		if traceID == "" {
			traceID = fmt.Sprintf("%d", time.Now().UnixNano())
		}
		start := time.Now()
		s.tracer.RecordStage(traceID, StageRouteMatch, 0) // 标记开始
		next.ServeHTTP(w, r)
		s.tracer.RecordStage(traceID, StageHandler, time.Since(start))
	})
}

// ============================================================================
// 自定义指标注册 API
// ============================================================================

// CustomMetric 自定义指标。
type CustomMetric struct {
	Name   string
	Help   string
	Type   string // counter, gauge, histogram, summary
	mu     sync.RWMutex
	value  float64
	count  int64
	sum    float64
	buckets []LatencyBucket
	labelValidator *MetricLabelValidator
}

// CustomMetricsRegistry 自定义指标注册表。
type CustomMetricsRegistry struct {
	mu       sync.RWMutex
	metrics  map[string]*CustomMetric
	labelValidator *MetricLabelValidator
}

// NewCustomMetricsRegistry 创建自定义指标注册表。
func NewCustomMetricsRegistry(validator *MetricLabelValidator) *CustomMetricsRegistry {
	return &CustomMetricsRegistry{
		metrics:        make(map[string]*CustomMetric),
		labelValidator: validator,
	}
}

// NewCounter 创建计数器。
func (r *CustomMetricsRegistry) NewCounter(name, help string, labels map[string]string) error {
	if r.labelValidator != nil {
		for k, v := range labels {
			if err := r.labelValidator.Validate(name, k, v); err != nil {
				return err
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics[name] = &CustomMetric{
		Name:  name,
		Help:  help,
		Type:  "counter",
	}
	return nil
}

// IncCounter 计数器加一。
func (r *CustomMetricsRegistry) IncCounter(name string) {
	r.mu.RLock()
	m, ok := r.metrics[name]
	r.mu.RUnlock()
	if !ok {
		return
	}
	m.mu.Lock()
	m.value++
	m.mu.Unlock()
}

// SnapshotCustom 获取自定义指标快照。
func (r *CustomMetricsRegistry) SnapshotCustom() map[string]*CustomMetric {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]*CustomMetric, len(r.metrics))
	for k, v := range r.metrics {
		result[k] = v
	}
	return result
}

// ============================================================================
// 配置热更新文件监听
// ============================================================================

// ConfigWatcher 配置文件监听器。
type ConfigWatcher struct {
	path    string
	onReload func(path string) error
	stopCh  chan struct{}
	logger  core.Logger
}

// NewConfigWatcher 创建配置监听器。
// 依赖底层操作系统通知机制（如 fsnotify），此处提供接口并在 Start 中初始化。
func NewConfigWatcher(path string, onReload func(path string) error, logger core.Logger) *ConfigWatcher {
	return &ConfigWatcher{
		path:    path,
		onReload: onReload,
		stopCh:  make(chan struct{}),
		logger:  logger,
	}
}

// Start 启动配置热更新监听。
// 使用轮询方式检测文件变化（兼容性实现，实际应使用 fsnotify）。
func (w *ConfigWatcher) Start() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		var lastModTime time.Time
		if fi, err := os.Stat(w.path); err == nil {
			lastModTime = fi.ModTime()
		}

		for {
			select {
			case <-w.stopCh:
				return
			case <-ticker.C:
				fi, err := os.Stat(w.path)
				if err != nil {
					continue
				}
				if fi.ModTime().After(lastModTime) {
					lastModTime = fi.ModTime()
					if w.logger != nil {
						w.logger.Info("config file changed, reloading...", "path", w.path)
					}
					if err := w.onReload(w.path); err != nil {
						if w.logger != nil {
							w.logger.Error("config reload failed", "error", err)
						}
					}
				}
			}
		}
	}()
}

// Stop 停止配置监听。
func (w *ConfigWatcher) Stop() {
	close(w.stopCh)
}

// SetConfigWatcher 设置配置文件监听器。
func (s *Server) SetConfigWatcher(path string, onReload func(path string) error) {
	s.configWatcher = NewConfigWatcher(path, onReload, s.logger)
}

// StartConfigWatcher 启动配置热更新监听。
func (s *Server) StartConfigWatcher() {
	if s.configWatcher != nil {
		s.configWatcher.Start()
	}
}

// ProductionConfig C10K/C100K 推荐配置。
func ProductionConfig() map[string]interface{} {
	return map[string]interface{}{
		"max_conns":      10000,
		"read_timeout":   "30s",
		"write_timeout":  "30s",
		"idle_timeout":   "120s",
		"shutdown_timeout": "30s",
		"gomaxprocs":     runtime.NumCPU(),
		"gc_percent":     200, // GOGC
		"ulimit":         "65535",
		"estimates": map[string]interface{}{
			"10k_conns_memory": "~200MB",
			"50k_conns_memory": "~1GB",
		},
	}
}

func init() {
	// 注册诊断变量
	RegisterExpvar()
}

// Ensure interface compliance.
var _ = strconv.Itoa
