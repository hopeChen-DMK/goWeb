# GoWeb - 高性能 Go Web 后端框架

GoWeb 是一个构建于 Go 标准库 `net/http` 之上的企业级 Web 框架。核心层 **零外部依赖**，提供高性能 Radix Tree 路由、上下文池化、完整的生产级中间件体系和开箱即用的安全能力。

<br />

声明：本项目完全是由 AI 设计开发的。目前还没有经过实战测试。有兴趣的同学可以用来跑两个 demo 尝试。强烈建议不要用到生产项目中去。

项目功能及源码正在不断完善中。

项目功能及源码正在不断完善中。

<br />

如果使用中遇到无法解决的问题可以在 github 上提 bug。

<br />

扩展：更多功能正在开发中。

## 快速开始

```go
package main

import "github.com/hopechen-dmk/goweb"

func main() {
	app := goweb.New()

	// 按推荐顺序注册中间件
	app.UseRecovery()      // 1. panic 恢复
	app.UseLogger()        // 2. 请求日志
	app.UseSecure()        // 3. 安全头
	app.UseCORS()          // 4. 跨域
	app.UseRateLimiter()   // 5. 限流
	app.UseBodyLimit(10 << 20) // 6. 请求体限制 10MB

	// 注册路由
	app.GET("/hello", func(c *goweb.Context) error {
		return c.JSON(200, goweb.H{"message": "Hello, World!"})
	})

	// 启动并等待信号
	go app.Start(":8080")
	app.WaitForSignal()
}
```

## 安装

```bash
go get github.com/hopechen-dmk/goweb
```

要求 Go 1.21+。

---

## 路由

### 基本路由

支持 9 种标准 HTTP 方法，外加 `ANY` 匹配所有方法：

```go
app.GET("/users", listUsers)
app.POST("/users", createUser)
app.PUT("/users/:id", updateUser)
app.DELETE("/users/:id", deleteUser)
app.PATCH("/users/:id", patchUser)
```

### 路径参数

```go
// 命名参数
app.GET("/users/:id/posts/:postId", func(c *goweb.Context) error {
	id := c.Param("id")
	postId := c.Param("postId")
	return c.JSON(200, goweb.H{"id": id, "postId": postId})
})

// 单段通配符
app.GET("/files/*", func(c *goweb.Context) error {
	filename := c.Param("*")
	return c.String(200, filename)
})

// 多段通配符
app.GET("/static/**", func(c *goweb.Context) error {
	path := c.Param("**")
	return c.String(200, path)
})
```

### 路由分组

```go
api := app.Group("/api")
api.Use(authMiddleware)

api.GET("/users", listUsers)
api.POST("/users", createUser)

// 嵌套分组
v2 := api.Group("/v2")
v2.GET("/users", listUsersV2)
```

### ANY 方法

```go
// 匹配所有 HTTP 方法
app.ANY("/webhook", handleWebhook)
```

### 静态文件

```go
app.Static("/public", "./static")
```

### 路由弃用标记

```go
app.DeprecatedRoute("GET", "/api/v1/old", "请使用 /api/v2/new", "2025-12-31")
```

---

## 上下文 (Context)

上下文对象通过 `sync.Pool` 复用，零 GC 压力。请求处理完成后自动归还池中。

### 请求信息

```go
func handler(c *goweb.Context) error {
	requestID := c.RequestID()   // 自动生成或从 X-Request-ID 头读取
	clientIP := c.ClientIP()     // 支持 X-Forwarded-For / X-Real-IP
	userAgent := c.UserAgent()
	method := c.Method()
	path := c.Path()
	query := c.QueryParam("page")
	formValue := c.FormValue("name")
	return nil
}
```

### 响应方法

```go
c.JSON(200, data)                // JSON 响应
c.XML(200, data)                 // XML 响应
c.String(200, "hello")           // 纯文本
c.HTML(200, "<h1>Hi</h1>")      // HTML
c.NoContent()                    // 204
c.Redirect(301, "/new-url")      // 重定向
c.Blob(200, "image/png", bytes)  // 二进制
c.Stream(200, "text/csv", reader) // 流式响应
```

### 请求体绑定与验证

```go
type CreateUserReq struct {
	Name  string `json:"name" validate:"required,min=2,max=50"`
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age" validate:"min=1,max=120"`
}

func createUser(c *goweb.Context) error {
	var req CreateUserReq
	if err := c.BindAndValidate(&req); err != nil {
		return err // 自动返回 422 + 结构化错误
	}
	// 业务逻辑...
	return c.JSON(201, req)
}
```

支持的验证标签：`required`, `min`, `max`, `len`, `email`, `url`, `oneof`, `eqfield`

- `required`：去除空格后判断非空
- `url`：必须为 http 或 https
- `email`：正则校验格式

### 键值存储

```go
c.Set("user", userObj)
c.Set("requestStart", time.Now())

if val, ok := c.Get("user"); ok { /* ... */ }
```

### 中间件控制

```go
func myMiddleware(next goweb.HandlerFunc) goweb.HandlerFunc {
	return func(c *goweb.Context) error {
		// 前置处理
		if someCondition {
			c.Abort()      // 中止后续处理器
			return c.JSON(403, goweb.H{"error": "forbidden"})
		}
		err := next(c)     // 调用下一个处理器
		// 后置处理
		return err
	}
}
```

### 认证信息

```go
func handler(c *goweb.Context) error {
	if !c.IsAuthenticated() {
		return c.JSON(401, "unauthorized")
	}
	auth := c.Auth()
	userID := auth.ID
	isAdmin := c.HasRole("admin")
	return nil
}
```

### HTTP/2 Server Push

```go
c.Push("/app.css", &http.PushOptions{})
```

### 链路追踪

```go
// 从上游请求提取追踪信息 (W3C Trace Context / X-B3)
c.ExtractTraceInfo()

// 注入追踪信息到下游请求
downstreamReq, _ := http.NewRequest("GET", "http://api.example.com", nil)
c.InjectTraceInfo(downstreamReq)
```

---

## 中间件

### 推荐顺序

```
Recovery → Logger → Secure → CORS → RateLimiter → BodyLimit → Timeout
  → CSRF → Auth → RBAC → 业务处理器
```

### Recovery - Panic 恢复

```go
app.UseRecovery()

// 自定义配置
app.UseRecovery(middleware.RecoveryConfig{
	LogStack: true,
})
```

### Logger - 请求日志

```go
app.UseLogger()

// 自定义配置
app.UseLogger(middleware.LoggerConfig{
	Format:         "json",
	LogBody:        false,
	LogBodyMaxSize: 1024,
	SkipPaths:      []string{"/healthz", "/readyz"},
})
```

### Secure - 安全响应头

```go
app.UseSecure() // 使用默认安全头

// 自定义
app.UseSecure(middleware.SecureConfig{
	CSP:                "default-src 'self'",
	HSTS:               "max-age=63072000; includeSubDomains",
	FrameOptions:       "DENY",
	ContentTypeNosniff: "nosniff",
	ReferrerPolicy:     "strict-origin-when-cross-origin",
})
```

### CORS - 跨域

```go
app.UseCORS()

app.UseCORS(middleware.CORSConfig{
	AllowOrigins:     []string{"https://example.com"},
	AllowMethods:     []string{"GET", "POST", "PUT"},
	AllowHeaders:     []string{"Authorization", "Content-Type"},
	AllowCredentials: true,
	MaxAge:           12 * time.Hour,
})
```

### RateLimiter - 令牌桶限流

```go
app.UseRateLimiter()

app.UseRateLimiter(middleware.RateLimiterConfig{
	Rate:  100,    // 每秒令牌数
	Burst: 200,    // 桶容量
	Period: time.Second,
})
```

### BodyLimit - 请求体限制

```go
app.UseBodyLimit(10 << 20) // 10MB
```

### Timeout - 请求超时

```go
app.UseTimeout(30 * time.Second)
```

### CSRF - 跨站请求伪造防护

支持 Cookie 模式和 SPA 双重提交模式，内置 HMAC-SHA256 签名和客户端指纹补偿（User-Agent + IP/24）：

```go
app.UseCSRF()

// SPA 模式
app.UseCSRF(middleware.CSRFConfig{
	Mode:       middleware.CSRFModeSPA,
	CookieName: "_csrf_token",
	MaxAge:     24 * time.Hour,
})
```

### 认证

**JWT**（支持 HS256 / RS256 / EdDSA）：

```go
app.UseAuthJWT(middleware.JWTConfig{
	Algorithm:       middleware.JWTAlgHS256,
	Secret:          []byte("your-secret-key"),
	RefreshRotation:  true,
	MaxRefresh:      1,
})

// 生成 JWT
claims := &middleware.StandardClaims{
	Subject:   "user123",
	ExpiresAt: time.Now().Add(15 * time.Minute).Unix(),
	Roles:     []string{"user"},
}
token, _ := middleware.GenerateJWT(claims, cfg)
```

**HTTP Basic**：

```go
app.UseAuthBasic(func(username, password string) (*goweb.AuthInfo, error) {
	if username == "admin" && password == "pass" {
		return &goweb.AuthInfo{ID: "1", Roles: []string{"admin"}}, nil
	}
	return nil, fmt.Errorf("invalid credentials")
})
```

**API Key**：

```go
app.UseAuthAPIKey(middleware.APIKeyConfig{
	HeaderName: "X-API-Key",
	Validator: func(key string) (*goweb.AuthInfo, error) {
		if key == "secret-api-key" {
			return &goweb.AuthInfo{ID: "app1"}, nil
		}
		return nil, fmt.Errorf("invalid key")
	},
})
```

**认证链**（任一策略通过即可）：

```go
app.Use(middleware.AuthChain(
	middleware.AuthJWT(jwtCfg)(func(c *goweb.Context) error { return nil }),
	middleware.AuthAPIKey(apiKeyCfg)(func(c *goweb.Context) error { return nil }),
))
```

### RBAC - 角色访问控制

```go
// admin 角色自动拥有所有权限（通配）
app.GET("/admin", handler, middleware.RBAC([]string{"admin"}))

// 需要 user:read 或 user:write 任一
app.GET("/users", handler, middleware.RBAC([]string{"user:read", "user:write"}))

// 需要同时拥有多个角色
app.GET("/admin/users", handler, middleware.RBAC([]string{"admin", "superadmin"}, true))
```

### 请求签名 (HMAC-SHA256)

```go
app.UseRequestSignature(middleware.SignatureConfig{
	Secret: []byte("shared-secret"),
	MaxAge: 5 * time.Minute,
})
```

客户端生成签名头：

```go
header := middleware.BuildSignatureHeader(secret, "POST", "/api/data", body)
// 设置 Authorization: Signature timestamp=...,signature=...
```

### Session - 会话管理

```go
store := middleware.NewMemorySessionStore()
app.UseSession(middleware.SessionConfig{
	CookieName: "_session",
	Store:      store,
	MaxAge:     24 * time.Hour,
	HttpOnly:   true,
	Secure:     true,
})

// 在处理器中使用
func handler(c *goweb.Context) error {
	session := c.Session()
	session["user_id"] = "123"
	return nil
}
```

---

## 服务器配置

### 基本启动

```go
app.Start(":8080")           // 快速启动
app.StartWithConfig(cfg)     // 自定义配置
app.StartTLS("cert.pem", "key.pem") // HTTPS
```

### 完整配置

```go
cfg := &server.ServerConfig{
	Addr:              ":8080",
	ReadTimeout:       30 * time.Second,
	ReadHeaderTimeout: 10 * time.Second,
	WriteTimeout:      30 * time.Second,
	IdleTimeout:       120 * time.Second,
	MaxHeaderBytes:    1 << 20,   // 1MB
	MaxConns:          10000,
	ConnQueueTimeout:  5 * time.Second,
	ShutdownTimeout:   30 * time.Second,
	EnableHTTP2:       true,
	MetricsEnabled:    true,
	MetricsPath:       "/metrics",
	TraceEnabled:      false,
	TraceSampleRate:   0.01,
	PprofEnabled:      false,
	HealthDetails:     false,
}
app.StartWithConfig(cfg)
```

### 健康检查

```go
// 存活检查 - GET /healthz → 200
// 就绪检查 - GET /readyz  → 200/503

// 注册自定义就绪检查器
app.RegisterReadinessCheck("database", func() error {
	return db.Ping()
})
app.RegisterReadinessCheck("redis", func() error {
	return redisClient.Ping().Err()
})

// 开启详情暴露（调试模式）
cfg.HealthDetails = true
```

### 生命周期钩子

```go
app.Server.BeforeStart(func() error {
	return db.Connect() // 返回 error 将阻止启动
})
app.Server.AfterStart(func() error {
	log.Println("server started")
	return nil
})
app.Server.OnRouterReady(func() error {
	log.Println("routes registered")
	return nil
})
app.Server.BeforeShutdown(func() error {
	log.Println("shutting down...")
	return nil
})
app.Server.AfterShutdown(func() error {
	return db.Close()
})
```

### 优雅关闭

`SIGINT` / `SIGTERM` 触发优雅关闭流程：

1. 执行 `BeforeShutdown` 钩子
2. `http.Server.Shutdown()` 停止接收新连接，等待活跃请求完成
3. 遍历 WebSocket 连接发送关闭帧（硬超时可配置）
4. 执行 `AfterShutdown` 钩子，释放全局资源

### 配置热更新

```go
// 设置配置文件监听
app.SetConfigWatcher("config.json", func(path string) error {
	return app.LoadConfig(path)
})

// 启动监听（10 秒轮询，依赖 fsnotify 可替换）
app.StartConfigWatcher()
```

---

## 文件上传与下载

### 单文件上传

```go
func uploadHandler(c *goweb.Context) error {
	file, err := c.FormFile("file")
	if err != nil { return err }

	// 保存到磁盘
	return c.SaveUploadedFile(file, "./uploads/"+file.Filename, true)
}
```

### 多文件上传

```go
func multiUpload(c *goweb.Context) error {
	files, err := c.FormFiles("files")
	if err != nil { return err }

	for _, f := range files {
		c.SaveUploadedFile(f, "./uploads/"+f.Filename, false)
	}
	return c.JSON(200, "ok")
}
```

### 分块上传

```go
mgr := files.NewChunkUploadManager("./temp", 100, 1<<30, 30*time.Minute)

// 初始化
session, _ := mgr.InitUpload("video.mp4", 10)

// 上传块
mgr.UploadChunk(session.ID, 0, chunkData)

// 合并
finalPath, _ := mgr.MergeChunks(session.ID)
```

### 文件下载

```go
func downloadHandler(c *goweb.Context) error {
	cfg := files.DownloadConfig{
		Attachment: true,
		Filename:   "report.pdf",
	}
	return files.SendFile(c, "./reports/report.pdf", cfg)
}
```

支持 Range 请求（断点续传）、ETag、Cache-Control 和 RFC 5987 文件名编码。

---

## 安全工具

### 密码

```go
hash, _ := goweb.HashPassword("myPassword")          // bcrypt, cost=12
valid := goweb.VerifyPassword("myPassword", hash)    // true/false
```

### 加密

```go
key, _ := security.GenerateAESKey()                  // AES-256
encrypted, _ := security.EncryptAESGCM(data, key)    // AES-GCM
decrypted, _ := security.DecryptAESGCM(encrypted, key)
```

### 密钥管理

```go
km := security.NewKeyManager(
	&security.EnvKeySource{},
	&security.FileKeySource{BasePath: "/etc/secrets"},
)

// 密钥轮换回调
km.OnRotate(func(name string, oldKey, newKey []byte) {
	log.Printf("key %s rotated", name)
})

// 从多个来源级联查找
key, _ := km.GetKey("jwt-secret")
```

---

## 指标与追踪

### Prometheus 指标

默认启用，访问 `/metrics` 端点：

```
goweb_requests_total
goweb_requests_in_flight
goweb_request_duration_seconds (histogram, 12 个分桶)
goweb_request_size_bytes (summary)
goweb_response_size_bytes (summary)
goweb_goroutines
goweb_memory_alloc_bytes
```

### 内置追踪器

```go
cfg.TraceEnabled = true
cfg.TraceSampleRate = 0.01 // 1% 采样

// 6 个追踪阶段:
// route_match → middleware_before → deserialize → handler → serialize → middleware_after
```

### 自定义指标

```go
validator := server.NewMetricLabelValidator(10000)
registry := server.NewCustomMetricsRegistry(validator)
registry.NewCounter("business_orders_total", "订单总数", map[string]string{"app": "shop"})
registry.IncCounter("business_orders_total")
```

---

## 国际化 (i18n)

```go
app.SetTranslations("zh-CN", map[string]string{
	"welcome": "欢迎",
})
app.SetTranslations("en", map[string]string{
	"welcome": "Welcome",
})
app.Use(app.I18NMiddleware())

// 在处理器中使用
func handler(c *goweb.Context) error {
	t := c.Get("T").(func(string) string)
	return c.String(200, t("welcome"))
}
```

---

## 模板引擎

```go
app.LoadTemplates("templates/*.html")
app.SetTemplateFunc("formatDate", func(t time.Time) string {
	return t.Format("2006-01-02")
})

func page(c *goweb.Context) error {
	return app.Render(c, "index.html", goweb.H{"title": "Home"})
}
```

---

## 测试

```go
import "github.com/hopechen-dmk/goweb/testutil"

func TestMyHandler(t *testing.T) {
	router := core.NewRadixRouter()
	router.GET("/hello", handler)

	tc := testutil.NewTestContext(router)
	resp := tc.Execute(testutil.GET("/hello"))

	assert := testutil.Assert(t)
	assert.StatusOK(resp)
	assert.Equal(`{"message":"hello"}`, resp.BodyString())
}
```

---

## CLI 工具

```bash
# 创建新项目
goweb new -name myapp -module github.com/my/app

# 生成 handler
goweb generate handler -name User

# 生成 OpenAPI 文档
goweb doc -output docs/api.yaml

# 开发服务器（热重载）
goweb run -port 3000 -watch

# 迁移指引
goweb migrate -from gin
```

---

## 性能基准

测试环境：4 vCPU、8G 内存 Linux、Go 最新版、500 并发 keep-alive、1KB JSON 请求体。

中间件链路：`Recovery → Logger → BodyLimit → AuthJWT → BindAndValidate → JSON`

- **QPS**：> 80,000
- **P99 延迟**：< 10ms

---

## 生产环境建议

```go
cfg := server.ProductionConfig()
// max_conns: 10000
// gomaxprocs: 物理核心数
// gc_percent: 200
// ulimit: 65535
```

高并发调优：

```bash
ulimit -n 65535
export GOMAXPROCS=$(nproc)
export GOGC=100
```

---

## 架构

```
goweb/
├── core/          # 核心层（零外部依赖）
│   ├── router.go  # 压缩 Radix Tree 路由
│   ├── context.go # 上下文对象（sync.Pool 复用）
│   ├── config.go  # 配置管理
│   ├── errors.go  # 错误类型
│   ├── logger.go  # slog 日志封装
│   └── types.go   # 核心接口定义
├── middleware/     # 内置中间件
│   ├── middleware.go  # Recovery/Logger/Secure/BodyLimit/Timeout/RBAC
│   ├── auth.go       # Basic/APIKey/AuthChain/Session
│   ├── csrf.go       # CSRF（HMAC+指纹）
│   ├── jwt.go        # JWT（HS256/RS256/EdDSA）
│   ├── cors.go       # CORS
│   ├── ratelimit.go  # 令牌桶限流
│   └── reqsign.go    # 请求签名（HMAC-SHA256）
├── server/        # HTTP 服务器
│   └── server.go  # 优雅关闭/指标/追踪/健康检查/配置热更新
├── security/      # 安全工具
│   └── security.go # 验证器/密码/加密/密钥管理
├── files/          # 文件处理
│   └── files.go   # 上传/下载/分块/存储抽象
├── testutil/       # 测试工具
│   └── testutil.go
├── cmd/goweb/      # CLI 脚手架
└── goweb.go        # 框架主入口（API 聚合）
```

## License

MIT
