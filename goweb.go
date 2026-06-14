// Package goweb is the main entry point for the GoWeb framework.
// It provides a unified API for building enterprise-grade web applications.
package goweb

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/hopechen-dmk/goWeb/core"
	"github.com/hopechen-dmk/goWeb/files"
	"github.com/hopechen-dmk/goWeb/middleware"
	"github.com/hopechen-dmk/goWeb/security"
	"github.com/hopechen-dmk/goWeb/server"
)

// ============================================================================
// 框架主入口
// ============================================================================

// App 框架应用主结构。
type App struct {
	Router *core.RadixRouter
	Server *server.Server
	Config *core.AppConfig
	Logger core.Logger

	// 中间件注册
	middlewares []core.MiddlewareFunc

	// 模板
	templates *template.Template
	tplFuncs  template.FuncMap

	// 文件存储
	storage core.Storage

	// 国际化翻译
	translations sync.Map // map[string]map[string]string

	// 状态
	initialized bool
}

// New 创建新的 App 实例。
func New() *App {
	router := core.NewRadixRouter()
	cfg := core.NewAppConfig()

	app := &App{
		Router:      router,
		Config:      cfg,
		middlewares: make([]core.MiddlewareFunc, 0),
	}

	// 设置默认日志
	logger := core.NewLogger(os.Stdout, core.LogLevelInfo)
	app.Logger = logger

	return app
}

// ============================================================================
// 配置方法
// ============================================================================

// SetConfig 设置配置实例。
func (app *App) SetConfig(cfg core.Config) {
	if ac, ok := cfg.(*core.AppConfig); ok {
		app.Config = ac
	}
}

// LoadConfig 从文件加载配置。
func (app *App) LoadConfig(path string) error {
	return app.Config.LoadFile(path)
}

// SetLogger 设置日志器。
func (app *App) SetLogger(logger core.Logger) {
	app.Logger = logger
}

// SetLogLevel 设置日志级别。
func (app *App) SetLogLevel(level core.LogLevel) {
	app.Logger.SetLevel(level)
}

// ============================================================================
// 路由注册
// ============================================================================

// GET 注册 GET 路由。
func (app *App) GET(path string, handler core.HandlerFunc, mw ...core.MiddlewareFunc) {
	app.Router.GET(path, handler, mw...)
}

// POST 注册 POST 路由。
func (app *App) POST(path string, handler core.HandlerFunc, mw ...core.MiddlewareFunc) {
	app.Router.POST(path, handler, mw...)
}

// PUT 注册 PUT 路由。
func (app *App) PUT(path string, handler core.HandlerFunc, mw ...core.MiddlewareFunc) {
	app.Router.PUT(path, handler, mw...)
}

// DELETE 注册 DELETE 路由。
func (app *App) DELETE(path string, handler core.HandlerFunc, mw ...core.MiddlewareFunc) {
	app.Router.DELETE(path, handler, mw...)
}

// PATCH 注册 PATCH 路由。
func (app *App) PATCH(path string, handler core.HandlerFunc, mw ...core.MiddlewareFunc) {
	app.Router.PATCH(path, handler, mw...)
}

// ANY 注册到所有 HTTP 方法的路由。
func (app *App) ANY(path string, handler core.HandlerFunc, mw ...core.MiddlewareFunc) {
	app.Router.ANY(path, handler, mw...)
}

// Group 创建路由组。
func (app *App) Group(prefix string) *core.Group {
	return app.Router.Group(prefix)
}

// ============================================================================
// 中间件注册（推荐顺序）
// ============================================================================

// Use 注册全局中间件。
func (app *App) Use(mw ...core.MiddlewareFunc) {
	app.middlewares = append(app.middlewares, mw...)
}

// UseRecovery 注册 Recovery 中间件（第一层）。
func (app *App) UseRecovery(config ...middleware.RecoveryConfig) {
	app.Use(middleware.Recovery(config...))
}

// UseLogger 注册 Logger 中间件（第二层）。
func (app *App) UseLogger(config ...middleware.LoggerConfig) {
	app.Use(middleware.Logger(config...))
}

// UseSecure 注册 Secure 中间件（第三层）。
func (app *App) UseSecure(config ...middleware.SecureConfig) {
	app.Use(middleware.Secure(config...))
}

// UseCORS 注册 CORS 中间件（第四层）。
func (app *App) UseCORS(config ...middleware.CORSConfig) {
	app.Use(middleware.CORS(config...))
}

// UseRateLimiter 注册限流中间件（第五层）。
func (app *App) UseRateLimiter(config ...middleware.RateLimiterConfig) {
	app.Use(middleware.RateLimiter(config...))
}

// UseBodyLimit 注册请求体大小限制中间件（第六层）。
func (app *App) UseBodyLimit(maxSize int64) {
	app.Use(middleware.BodyLimit(maxSize))
}

// UseTimeout 注册超时中间件（第七层）。
func (app *App) UseTimeout(d time.Duration) {
	app.Use(middleware.Timeout(d))
}

// UseCSRF 注册 CSRF 中间件（第八层）。
func (app *App) UseCSRF(config ...middleware.CSRFConfig) {
	app.Use(middleware.CSRF(config...))
}

// UseAuthJWT 注册 JWT 认证中间件（第九层）。
func (app *App) UseAuthJWT(config ...middleware.JWTConfig) {
	app.Use(middleware.AuthJWT(config...))
}

// UseAuthBasic 注册 Basic 认证中间件。
func (app *App) UseAuthBasic(validator middleware.CredentialValidator) {
	app.Use(middleware.AuthBasic(validator))
}

// UseAuthAPIKey 注册 API Key 认证中间件。
func (app *App) UseAuthAPIKey(config middleware.APIKeyConfig) {
	app.Use(middleware.AuthAPIKey(config))
}

// UseRBAC 注册 RBAC 中间件（第十层）。
func (app *App) UseRBAC(roles []string, requireAll ...bool) {
	app.Use(middleware.RBAC(roles, requireAll...))
}

// UseSession 注册 Session 中间件。
func (app *App) UseSession(config middleware.SessionConfig) {
	app.Use(middleware.Session(config))
}

// UseRequestSignature 注册请求签名中间件。
func (app *App) UseRequestSignature(config middleware.SignatureConfig) {
	app.Use(middleware.RequestSignature(config))
}

// ============================================================================
// 初始化
// ============================================================================

// Init 初始化应用（注册全局中间件到路由器）。
func (app *App) Init() {
	// 按推荐顺序注册中间件
	app.Router.Use(app.middlewares...)
	app.initialized = true
}

// ============================================================================
// 服务器启动
// ============================================================================

// Start 启动 HTTP 服务器。
func (app *App) Start(addr string) error {
	app.Init()

	cfg := server.DefaultServerConfig()
	cfg.Addr = addr

	app.Server = server.New(app.Router, cfg)
	app.Server.SetLogger(app.Logger)

	return app.Server.Start()
}

// StartWithConfig 使用自定义配置启动服务器。
func (app *App) StartWithConfig(cfg *server.ServerConfig) error {
	app.Init()

	app.Server = server.New(app.Router, cfg)
	app.Server.SetLogger(app.Logger)

	return app.Server.Start()
}

// WaitForSignal 等待信号并优雅关闭。
func (app *App) WaitForSignal() {
	if app.Server != nil {
		app.Server.WaitForSignal()
	}
}

// RegisterReadinessCheck 注册就绪检查器。
func (app *App) RegisterReadinessCheck(name string, check func() error) {
	if app.Server != nil {
		app.Server.RegisterReadinessCheck(name, check)
	}
}

// OnRouterReady 注册路由就绪钩子。
func (app *App) OnRouterReady(hook core.LifecycleHook) {
	if app.Server != nil {
		app.Server.OnRouterReady(hook)
	}
}

// SetConfigWatcher 设置配置文件热更新监听。
func (app *App) SetConfigWatcher(path string, onReload func(path string) error) {
	if app.Server != nil {
		app.Server.SetConfigWatcher(path, onReload)
	}
}

// StartConfigWatcher 启动配置热更新监听。
func (app *App) StartConfigWatcher() {
	if app.Server != nil {
		app.Server.StartConfigWatcher()
	}
}

// Shutdown 优雅关闭服务器。
func (app *App) Shutdown() error {
	if app.Server != nil {
		return app.Server.Shutdown()
	}
	return nil
}

// ============================================================================
// 模板（6 个基础指令：变量、if/else、range、with、template、block）
// ============================================================================

// SetTemplateFunc 注册自定义模板函数。
func (app *App) SetTemplateFunc(name string, fn interface{}) {
	if app.tplFuncs == nil {
		app.tplFuncs = make(template.FuncMap)
	}
	app.tplFuncs[name] = fn
}

// LoadTemplates 加载模板文件。
func (app *App) LoadTemplates(pattern string) error {
	app.tplFuncs = make(template.FuncMap)
	// 内置函数
	app.tplFuncs["upper"] = strings.ToUpper
	app.tplFuncs["lower"] = strings.ToLower
	app.tplFuncs["title"] = strings.Title
	app.tplFuncs["now"] = func() string { return time.Now().Format(time.RFC3339) }

	app.templates = template.Must(template.New("").Funcs(app.tplFuncs).ParseGlob(pattern))
	return nil
}

// Render 渲染模板。
func (app *App) Render(c *core.Context, name string, data interface{}) error {
	if app.templates == nil {
		return core.ErrInternalServer("templates not loaded")
	}

	var buf strings.Builder
	if err := app.templates.ExecuteTemplate(&buf, name, data); err != nil {
		return core.ErrInternalServer("template render error").WithInternal(err)
	}

	return c.HTML(http.StatusOK, buf.String())
}

// ============================================================================
// 文件处理
// ============================================================================

// SetStorage 设置文件存储后端。
func (app *App) SetStorage(storage core.Storage) {
	app.storage = storage
}

// UploadFile 上传单个文件。
func (app *App) UploadFile(c *core.Context, fieldName string) (string, error) {
	return files.UploadSingleFile(c, fieldName)
}

// SendFile 发送文件下载。
func (app *App) SendFile(c *core.Context, filePath string, attachment bool) error {
	return files.SendFile(c, filePath, files.DownloadConfig{Attachment: attachment})
}

// ============================================================================
// 国际化
// ============================================================================

// SetTranslations 设置翻译数据。
func (app *App) SetTranslations(lang string, translations map[string]string) {
	app.translations.Store(lang, translations)
}

// T 翻译函数。
func (app *App) T(lang, key string) string {
	if data, ok := app.translations.Load(lang); ok {
		if m, ok := data.(map[string]string); ok {
			if val, ok := m[key]; ok {
				return val
			}
		}
	}
	return key
}

// I18NMiddleware 国际化中间件。
func (app *App) I18NMiddleware() core.MiddlewareFunc {
	return func(next core.HandlerFunc) core.HandlerFunc {
		return func(c *core.Context) error {
			lang := c.GetHeader("Accept-Language")
			if lang == "" {
				lang = "zh-CN"
			}
			// 提取主语言
			if idx := strings.Index(lang, ","); idx > 0 {
				lang = lang[:idx]
			}
			if idx := strings.Index(lang, ";"); idx > 0 {
				lang = lang[:idx]
			}

			c.Set("lang", lang)
			c.Set("T", func(key string) string {
				return app.T(lang, key)
			})

			return next(c)
		}
	}
}

// ============================================================================
// 验证器
// ============================================================================

// Validate 验证结构体。
func (app *App) Validate(v interface{}) error {
	return security.ValidateStruct(v)
}

// BindAndValidate 绑定并验证。
func (app *App) BindAndValidate(c *core.Context, v interface{}) error {
	return c.BindAndValidate(v)
}

// ============================================================================
// 密码工具
// ============================================================================

// HashPassword 哈希密码。
func HashPassword(password string) (string, error) {
	return security.HashPassword(password)
}

// VerifyPassword 验证密码。
func VerifyPassword(password, hash string) bool {
	return security.VerifyPassword(password, hash)
}

// ============================================================================
// WebSocket 支持
// ============================================================================

// WebSocketUpgrade 升级到 WebSocket。
func (app *App) WebSocketUpgrade(c *core.Context) (core.WSConn, error) {
	// 需要 gorilla/websocket 扩展
	return nil, fmt.Errorf("websocket requires extension: goweb/ws")
}

// ============================================================================
// 静态文件
// ============================================================================

// Static 注册静态文件路由。
func (app *App) Static(prefix, root string) {
	app.Router.Static(prefix, root)
}

// ============================================================================
// 弃用路由
// ============================================================================

// DeprecatedRoute 标记路由为弃用。
func (app *App) DeprecatedRoute(method, path, message string, sunset ...string) {
	app.Router.MarkDeprecated(method, path, core.Deprecated(message, sunset...))
}

// ============================================================================
// 调试与诊断
// ============================================================================

// GoroutineCount 返回 goroutine 数量。
func GoroutineCount() int {
	return server.GoroutineCount()
}

// ============================================================================
// 工具
// ============================================================================

// GenerateID 生成随机 ID。
func GenerateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ============================================================================
// 类型别名导出（便捷使用）
// ============================================================================

type (
	Context          = core.Context
	HandlerFunc      = core.HandlerFunc
	MiddlewareFunc   = core.MiddlewareFunc
	Map              = core.Map
	H                = core.H
	Response         = core.Response
	HTTPError        = core.HTTPError
	AuthInfo         = core.AuthInfo
	LogLevel         = core.LogLevel
	ValidationErrors = core.ValidationErrors
	Params           = core.Params
)

// 常量导出
const (
	LogLevelDebug = core.LogLevelDebug
	LogLevelInfo  = core.LogLevelInfo
	LogLevelWarn  = core.LogLevelWarn
	LogLevelError = core.LogLevelError
)

// Ensure unused imports are ok
var _ = filepath.Base
var _ = reflect.ValueOf
