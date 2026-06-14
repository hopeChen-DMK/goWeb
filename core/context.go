package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// 上下文对象
// ============================================================================

// Context 封装请求上下文，通过 sync.Pool 复用。
// 重置时严格区分：清空请求级数据但绝不关闭全局连接池。
type Context struct {
	responseWriter http.ResponseWriter
	Request        *http.Request

	// 路由参数
	params Params

	// 存储（请求级键值对）
	keys map[string]any

	// 状态码
	statusCode int

	// 是否已响应
	written bool

	// 请求 ID（自动注入）
	requestID string

	// 追踪信息
	Trace Trace

	// 错误
	err error

	// 日志器引用（不拥有，仅引用）
	logger Logger

	// 配置引用（不拥有，仅引用）
	config Config

	// 会话引用（不拥有，仅清除引用）
	sessionData  map[string]any
	sessionStore SessionStore

	// 认证信息
	authInfo *AuthInfo

	// 文件引用列表（用于清理）
	fileRefs []*os.File

	// 魔数检测器（可注入）
	magicDetector MagicDetector

	// body 缓存（用于多次读取）
	bodyCache []byte
	bodyRead  bool

	// 请求开始时间
	startTime time.Time

	// 中间件链：处理链的剩余处理器切片
	handlers []HandlerFunc

	// 当前执行的处理器下标
	index int

	// 是否已中止
	aborted bool
}

// ============================================================================
// sync.Pool 管理
// ============================================================================

var contextPool = sync.Pool{
	New: func() any {
		return &Context{
			keys:    make(map[string]any),
			fileRefs: make([]*os.File, 0),
		}
	},
}

// acquireContext 从池中获取上下文。
func acquireContext(w http.ResponseWriter, r *http.Request) *Context {
	c := contextPool.Get().(*Context)
	c.reset(w, r)
	return c
}

// releaseContext 将上下文归还到池中。
func releaseContext(c *Context) {
	c.release()
	contextPool.Put(c)
}

// ============================================================================
// 重置与释放
// ============================================================================

func (c *Context) reset(w http.ResponseWriter, r *http.Request) {
	c.responseWriter = w
	c.Request = r
	c.params = c.params[:0]
	c.statusCode = http.StatusOK
	c.written = false
	c.err = nil
	c.authInfo = nil
	c.sessionData = nil // 不关闭 Session 存储，仅移除引用
	c.sessionStore = nil
	c.bodyCache = nil
	c.bodyRead = false
	c.startTime = time.Now()
	c.Trace = Trace{}
	c.handlers = nil
	c.index = 0
	c.aborted = false

	// 生成请求 ID：优先从请求头获取
	c.requestID = r.Header.Get("X-Request-ID")
	if c.requestID == "" || len(c.requestID) > 128 {
		c.requestID = generateRequestID()
	}
	// 将请求 ID 注入响应头
	w.Header().Set("X-Request-ID", c.requestID)

	// 清空 keys（不创建新 map，复用底层存储）
	for k := range c.keys {
		delete(c.keys, k)
	}

	// 关闭文件引用并清空
	for _, f := range c.fileRefs {
		f.Close()
	}
	c.fileRefs = c.fileRefs[:0]
}

func (c *Context) release() {
	// 归还会话存储连接
	if c.sessionStore != nil {
		c.sessionStore.Release()
		c.sessionStore = nil
	}
	// 确保所有文件引用被关闭
	for _, f := range c.fileRefs {
		f.Close()
	}
	c.fileRefs = c.fileRefs[:0]
	c.responseWriter = nil
	c.Request = nil
}

// ============================================================================
// 请求 ID
// ============================================================================

func generateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// RequestID 返回请求 ID。
func (c *Context) RequestID() string {
	return c.requestID
}

// ============================================================================
// 参数获取
// ============================================================================

// SetParams 设置路由参数。
func (c *Context) SetParams(p Params) {
	c.params = p
}

// Param 获取单个路由参数值。
func (c *Context) Param(name string) string {
	return c.params.Get(name)
}

// ParamDefault 获取路由参数，不存在时返回默认值。
func (c *Context) ParamDefault(name, defaultVal string) string {
	v := c.params.Get(name)
	if v == "" {
		return defaultVal
	}
	return v
}

// QueryParam 获取查询参数。
func (c *Context) QueryParam(name string) string {
	return c.Request.URL.Query().Get(name)
}

// QueryParamDefault 获取查询参数。
func (c *Context) QueryParamDefault(name, defaultVal string) string {
	v := c.QueryParam(name)
	if v == "" {
		return defaultVal
	}
	return v
}

// QueryParams 获取所有查询参数。
func (c *Context) QueryParams() url.Values {
	return c.Request.URL.Query()
}

// FormValue 获取表单值。
func (c *Context) FormValue(name string) string {
	return c.Request.FormValue(name)
}

// ============================================================================
// 键值存储
// ============================================================================

// Set 设置请求级键值对。
func (c *Context) Set(key string, value any) {
	c.keys[key] = value
}

// Get 获取键值。
func (c *Context) Get(key string) (any, bool) {
	v, ok := c.keys[key]
	return v, ok
}

// GetString 获取字符串类型键值。
func (c *Context) GetString(key string) string {
	v, ok := c.keys[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// MustGet 获取键值，不存在时 panic。
func (c *Context) MustGet(key string) any {
	v, ok := c.keys[key]
	if !ok {
		panic("key not found: " + key)
	}
	return v
}

// ============================================================================
// 响应方法
// ============================================================================

// Status 设置 HTTP 状态码。
func (c *Context) Status(code int) {
	c.statusCode = code
}

// StatusCode 返回当前状态码。
func (c *Context) StatusCode() int {
	return c.statusCode
}

// Written 返回是否已写入响应。
func (c *Context) Written() bool {
	return c.written
}

// JSON 发送 JSON 响应。
func (c *Context) JSON(code int, data any) error {
	c.Status(code)
	c.responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.responseWriter.WriteHeader(code)
	c.written = true

	enc := json.NewEncoder(c.responseWriter)
	enc.SetEscapeHTML(false)
	return enc.Encode(data)
}

// XML 发送 XML 响应。
func (c *Context) XML(code int, data any) error {
	c.Status(code)
	c.responseWriter.Header().Set("Content-Type", "application/xml; charset=utf-8")
	c.responseWriter.WriteHeader(code)
	c.written = true
	return xml.NewEncoder(c.responseWriter).Encode(data)
}

// String 发送纯文本响应。
func (c *Context) String(code int, s string) error {
	c.Status(code)
	c.responseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.responseWriter.WriteHeader(code)
	c.written = true
	_, err := c.responseWriter.Write([]byte(s))
	return err
}

// HTML 发送 HTML 响应。
func (c *Context) HTML(code int, html string) error {
	c.Status(code)
	c.responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.responseWriter.WriteHeader(code)
	c.written = true
	_, err := c.responseWriter.Write([]byte(html))
	return err
}

// NoContent 发送 204 无内容响应。
func (c *Context) NoContent() error {
	return c.Empty(http.StatusNoContent)
}

// Empty 发送指定状态码的无内容响应。
func (c *Context) Empty(code int) error {
	c.Status(code)
	c.responseWriter.WriteHeader(code)
	c.written = true
	return nil
}

// Redirect 重定向。
func (c *Context) Redirect(code int, url string) error {
	c.Status(code)
	c.responseWriter.WriteHeader(code)
	c.written = true
	http.Redirect(c.responseWriter, c.Request, url, code)
	return nil
}

// Blob 发送二进制数据。
func (c *Context) Blob(code int, contentType string, data []byte) error {
	c.Status(code)
	c.responseWriter.Header().Set("Content-Type", contentType)
	c.responseWriter.WriteHeader(code)
	c.written = true
	_, err := c.responseWriter.Write(data)
	return err
}

// Stream 流式响应。
func (c *Context) Stream(code int, contentType string, reader io.Reader) error {
	c.Status(code)
	c.responseWriter.Header().Set("Content-Type", contentType)
	c.responseWriter.WriteHeader(code)
	c.written = true
	_, err := io.Copy(c.responseWriter, reader)
	return err
}

// ============================================================================
// 响应头操作
// ============================================================================

// SetHeader 设置响应头。
func (c *Context) SetHeader(key, value string) {
	c.responseWriter.Header().Set(key, value)
}

// AddHeader 添加响应头（可重复）。
func (c *Context) AddHeader(key, value string) {
	c.responseWriter.Header().Add(key, value)
}

// GetHeader 获取请求头。
func (c *Context) GetHeader(key string) string {
	return c.Request.Header.Get(key)
}

// Header 返回响应头对象。
func (c *Context) Header() http.Header {
	return c.responseWriter.Header()
}

// ============================================================================
// Cookie 操作
// ============================================================================

// SetCookie 设置 Cookie。
func (c *Context) SetCookie(cookie *http.Cookie) {
	http.SetCookie(c.responseWriter, cookie)
}

// Cookie 获取指定名称的 Cookie。
func (c *Context) Cookie(name string) (*http.Cookie, error) {
	return c.Request.Cookie(name)
}

// Cookies 获取所有 Cookie。
func (c *Context) Cookies() []*http.Cookie {
	return c.Request.Cookies()
}

// ============================================================================
// 请求体绑定
// ============================================================================

// Bind 绑定请求体到目标结构体。
func (c *Context) Bind(v interface{}) error {
	contentType := c.Request.Header.Get("Content-Type")

	switch {
	case strings.Contains(contentType, "application/json"):
		return c.BindJSON(v)
	case strings.Contains(contentType, "application/xml"):
		return c.BindXML(v)
	case strings.Contains(contentType, "application/x-www-form-urlencoded"):
		return c.BindForm(v)
	default:
		return c.BindJSON(v) // 默认 JSON
	}
}

// BindJSON 绑定 JSON 请求体。
func (c *Context) BindJSON(v interface{}) error {
	body, err := c.GetRawBody()
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	return json.Unmarshal(body, v)
}

// BindXML 绑定 XML 请求体。
func (c *Context) BindXML(v interface{}) error {
	body, err := c.GetRawBody()
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	return xml.Unmarshal(body, v)
}

// BindForm 绑定表单数据。
func (c *Context) BindForm(v interface{}) error {
	// 对于简单表单绑定，使用手动解析
	if err := c.Request.ParseForm(); err != nil {
		return err
	}
	// 使用 json tag 作为表单键的简单映射
	return bindFormToStruct(c.Request.Form, v)
}

// BindQuery 绑定查询参数。
func (c *Context) BindQuery(v interface{}) error {
	return bindFormToStruct(c.Request.URL.Query(), v)
}

// BindAndValidate 绑定并验证请求体。失败返回结构化 422 错误。
func (c *Context) BindAndValidate(v interface{}) error {
	if err := c.Bind(v); err != nil {
		return NewHTTPError(http.StatusBadRequest, "invalid request body: "+err.Error())
	}

	// 检查是否实现了 Validatable 接口
	if validatable, ok := v.(Validatable); ok {
		if err := validatable.Validate(); err != nil {
			var verr ValidationErrors
			if errors.As(err, &verr) {
				return NewHTTPError(http.StatusUnprocessableEntity, err.Error()).
					WithData(map[string]any{"errors": verr})
			}
			return NewHTTPError(http.StatusUnprocessableEntity, err.Error())
		}
	}

	// 使用全局 validate tag 验证器
	if defaultValidator != nil {
		if err := defaultValidator.Validate(v); err != nil {
			return NewHTTPError(http.StatusUnprocessableEntity, err.Error())
		}
	}

	return nil
}

// GetRawBody 获取原始请求体（带缓存）。
func (c *Context) GetRawBody() ([]byte, error) {
	if c.bodyRead {
		return c.bodyCache, nil
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}

	c.bodyCache = body
	c.bodyRead = true
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	return body, nil
}

// ============================================================================
// 文件处理
// ============================================================================

// FormFile 获取单个上传文件。
func (c *Context) FormFile(name string) (*multipart.FileHeader, error) {
	_, fh, err := c.Request.FormFile(name)
	return fh, err
}

// MultipartForm 获取 multipart form。
func (c *Context) MultipartForm() (*multipart.Form, error) {
	if c.Request.MultipartForm == nil {
		return nil, errors.New("multipart form not parsed")
	}
	return c.Request.MultipartForm, nil
}

// SaveUploadedFile 保存上传文件到目标路径。
func (c *Context) SaveUploadedFile(file *multipart.FileHeader, dst string, magicCheck bool) error {
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	// 魔数检测
	if magicCheck {
		if c.magicDetector != nil {
			if _, err := c.magicDetector.Detect(src); err != nil {
				return fmt.Errorf("magic detection failed: %w", err)
			}
			// 重置读取位置
			if seeker, ok := src.(io.Seeker); ok {
				seeker.Seek(0, io.SeekStart)
			}
		} else {
			return errors.New("magic detection not supported: no detector registered, " +
				"import an extension or disable magic check")
		}
	}

	// 防路径穿越
	dst = filepath.Clean(dst)

	// 创建目标文件
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	c.fileRefs = append(c.fileRefs, out)

	_, err = io.Copy(out, src)
	return err
}

// SaveUploadedFileStream 流式保存上传文件（不内存缓冲）。
func (c *Context) SaveUploadedFileStream(file *multipart.FileHeader, dst string, magicCheck bool) error {
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	// 魔数检测
	if magicCheck {
		if c.magicDetector != nil {
			if _, err := c.magicDetector.Detect(src); err != nil {
				return fmt.Errorf("magic detection failed: %w", err)
			}
			if seeker, ok := src.(io.Seeker); ok {
				seeker.Seek(0, io.SeekStart)
			}
		} else {
			return errors.New("magic detection not supported: no detector registered, " +
				"import an extension or disable magic check")
		}
	}

	dst = filepath.Clean(dst)
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	c.fileRefs = append(c.fileRefs, out)

	_, err = io.Copy(out, src)
	return err
}

// ============================================================================
// 会话管理
// ============================================================================

// Session 获取会话数据（不持有会话存储引用）。
func (c *Context) Session() map[string]any {
	return c.sessionData
}

// SetSessionData 设置会话数据引用。
func (c *Context) SetSessionData(data map[string]any) {
	c.sessionData = data
}

// SetSessionStore 设置会话存储引用（用于 Release 时归还连接）。
func (c *Context) SetSessionStore(store SessionStore) {
	c.sessionStore = store
}

// ============================================================================
// 认证信息
// ============================================================================

// Auth 返回认证信息。
func (c *Context) Auth() *AuthInfo {
	return c.authInfo
}

// SetAuth 设置认证信息。
func (c *Context) SetAuth(info *AuthInfo) {
	c.authInfo = info
}

// IsAuthenticated 判断是否已认证。
func (c *Context) IsAuthenticated() bool {
	return c.authInfo != nil
}

// HasRole 检查是否拥有指定角色。
func (c *Context) HasRole(role string) bool {
	if c.authInfo == nil {
		return false
	}
	for _, r := range c.authInfo.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// ============================================================================
// 配置访问
// ============================================================================

// SetConfig 设置配置引用。
func (c *Context) SetConfig(cfg Config) {
	c.config = cfg
}

// Config 返回配置引用。
func (c *Context) Config() Config {
	return c.config
}

// SetLogger 设置日志器引用。
func (c *Context) SetLogger(l Logger) {
	c.logger = l
}

// Logger 返回日志器引用。
func (c *Context) Logger() Logger {
	return c.logger
}

// ============================================================================
// IP / 客户端信息
// ============================================================================

// ClientIP 返回客户端真实 IP。
func (c *Context) ClientIP() string {
	if ip := c.Request.Header.Get("X-Forwarded-For"); ip != "" {
		parts := strings.Split(ip, ",")
		return strings.TrimSpace(parts[0])
	}
	if ip := c.Request.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}

// UserAgent 返回 User-Agent。
func (c *Context) UserAgent() string {
	return c.Request.Header.Get("User-Agent")
}

// ============================================================================
// 上下文操作
// ============================================================================

// Deadline 实现 context.Context。
func (c *Context) Deadline() (time.Time, bool) {
	return c.Request.Context().Deadline()
}

// Done 实现 context.Context。
func (c *Context) Done() <-chan struct{} {
	return c.Request.Context().Done()
}

// Err 实现 context.Context。
func (c *Context) Err() error {
	return c.Request.Context().Err()
}

// Value 实现 context.Context。
func (c *Context) Value(key any) any {
	if k, ok := key.(string); ok {
		return c.GetString(k)
	}
	return nil
}

// Context 返回底层标准 context.Context。
func (c *Context) Context() context.Context {
	return c.Request.Context()
}

// ============================================================================
// 错误处理
// ============================================================================

// SetError 设置错误。
func (c *Context) SetError(err error) {
	c.err = err
}

// Error 返回当前错误。
func (c *Context) Error() error {
	return c.err
}

// ============================================================================
// 魔数检测器
// ============================================================================

// SetMagicDetector 设置魔数检测器。
func (c *Context) SetMagicDetector(d MagicDetector) {
	c.magicDetector = d
}

// ============================================================================
// 写入器接口扩展
// ============================================================================

// ResponseWriter 返回底层 ResponseWriter。
func (c *Context) ResponseWriter() http.ResponseWriter {
	return c.responseWriter
}

// Hijack 支持 WebSocket 升级等需要 hijack 的场景。
func (c *Context) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := c.responseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

// Flush 刷新响应。
func (c *Context) Flush() {
	flusher, ok := c.responseWriter.(http.Flusher)
	if ok {
		flusher.Flush()
	}
}

// ============================================================================
// 路径与方法
// ============================================================================

// Path 返回请求路径。
func (c *Context) Path() string {
	return c.Request.URL.Path
}

// Method 返回请求方法。
func (c *Context) Method() string {
	return c.Request.Method
}

// FullPath 返回完整请求 URL。
func (c *Context) FullPath() string {
	return c.Request.URL.String()
}

// ============================================================================
// 中间件链控制
// ============================================================================

// SetHandlers 设置处理链。
func (c *Context) SetHandlers(handlers []HandlerFunc) {
	c.handlers = handlers
}

// Next 调用处理链中的下一个处理器。
func (c *Context) Next() error {
	if c.aborted {
		return nil
	}
	if c.index >= len(c.handlers) {
		return nil
	}
	handler := c.handlers[c.index]
	c.index++
	return handler(c)
}

// Abort 中止后续处理器执行，但当前处理器仍会运行完。
func (c *Context) Abort() {
	c.aborted = true
}

// IsAborted 返回是否已中止。
func (c *Context) IsAborted() bool {
	return c.aborted
}

// NewError 创建带有状态码和消息的错误，可被全局错误处理器捕获。
func (c *Context) NewError(status int, msg string) error {
	return NewHTTPError(status, msg)
}

// ============================================================================
// 文件处理（多文件）
// ============================================================================

// FormFiles 获取多个同名上传文件。
func (c *Context) FormFiles(name string) ([]*multipart.FileHeader, error) {
	if c.Request.MultipartForm == nil {
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
			return nil, err
		}
	}
	if c.Request.MultipartForm == nil {
		return nil, errors.New("no multipart form")
	}
	return c.Request.MultipartForm.File[name], nil
}

// ============================================================================
// HTTP/2 Server Push
// ============================================================================

// Push 发起 HTTP/2 服务端推送。若不支持则静默忽略。
func (c *Context) Push(target string, opts *http.PushOptions) error {
	pusher, ok := c.responseWriter.(http.Pusher)
	if !ok {
		return nil // 不支持 HTTP/2 Push，静默返回
	}
	return pusher.Push(target, opts)
}

// ============================================================================
// 链路追踪信息提取与注入
// ============================================================================

// ExtractTraceInfo 从 W3C Trace Context 请求头提取追踪信息。
func (c *Context) ExtractTraceInfo() {
	if c.Trace.TraceID == "" {
		if v := c.Request.Header.Get("Traceparent"); v != "" {
			// W3C Trace Context: version-traceid-spanid-flags
			parts := strings.Split(v, "-")
			if len(parts) >= 3 {
				c.Trace.TraceID = parts[1]
				c.Trace.SpanID = parts[2]
			}
		}
	}
	if c.Trace.TraceID == "" {
		if v := c.Request.Header.Get("X-Trace-ID"); v != "" {
			c.Trace.TraceID = v
		}
	}
	if v := c.Request.Header.Get("tracestate"); v != "" {
		if c.Trace.Baggage == nil {
			c.Trace.Baggage = make(map[string]string)
		}
		c.Trace.Baggage["tracestate"] = v
	}
	if v := c.Request.Header.Get("X-B3-TraceId"); v != "" && c.Trace.TraceID == "" {
		c.Trace.TraceID = v
	}
	if v := c.Request.Header.Get("X-B3-SpanId"); v != "" && c.Trace.SpanID == "" {
		c.Trace.SpanID = v
	}
}

// InjectTraceInfo 将追踪信息注入到下游 HTTP 请求头中。
func (c *Context) InjectTraceInfo(req *http.Request) {
	if c.Trace.TraceID != "" {
		req.Header.Set("X-Trace-ID", c.Trace.TraceID)
		if c.Trace.SpanID != "" {
			// W3C Trace Context 格式
			req.Header.Set("Traceparent", fmt.Sprintf("00-%s-%s-01", c.Trace.TraceID, c.Trace.SpanID))
		}
	}
	if c.Trace.Baggage != nil {
		if ts, ok := c.Trace.Baggage["tracestate"]; ok {
			req.Header.Set("tracestate", ts)
		}
	}
}
