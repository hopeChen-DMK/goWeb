// Package testutil 提供测试工具包，封装 httptest。
package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goweb-framework/goweb/core"
)

// ============================================================================
// TestContext 测试上下文
// ============================================================================

// TestContext 测试辅助器，封装 httptest。
type TestContext struct {
	Router *core.RadixRouter
	Config *core.AppConfig
}

// NewTestContext 创建测试上下文。
func NewTestContext(router *core.RadixRouter) *TestContext {
	return &TestContext{
		Router: router,
		Config: core.NewAppConfig(),
	}
}

// ============================================================================
// 请求构建
// ============================================================================

// TestRequest 测试请求构建器。
type TestRequest struct {
	method  string
	path    string
	headers map[string]string
	body    io.Reader
	cookies []*http.Cookie
}

// NewRequest 创建测试请求构建器。
func NewRequest(method, path string) *TestRequest {
	return &TestRequest{
		method:  method,
		path:    path,
		headers: make(map[string]string),
	}
}

// GET 创建 GET 请求。
func GET(path string) *TestRequest {
	return NewRequest(http.MethodGet, path)
}

// POST 创建 POST 请求。
func POST(path string) *TestRequest {
	return NewRequest(http.MethodPost, path)
}

// PUT 创建 PUT 请求。
func PUT(path string) *TestRequest {
	return NewRequest(http.MethodPut, path)
}

// DELETE 创建 DELETE 请求。
func DELETE(path string) *TestRequest {
	return NewRequest(http.MethodDelete, path)
}

// WithHeader 设置请求头。
func (r *TestRequest) WithHeader(key, value string) *TestRequest {
	r.headers[key] = value
	return r
}

// WithJSON 设置 JSON 请求体。
func (r *TestRequest) WithJSON(v interface{}) *TestRequest {
	data, _ := json.Marshal(v)
	r.body = bytes.NewReader(data)
	r.headers["Content-Type"] = "application/json"
	return r
}

// WithForm 设置表单请求体。
func (r *TestRequest) WithForm(data map[string]string) *TestRequest {
	params := make([]string, 0, len(data))
	for k, v := range data {
		params = append(params, k+"="+v)
	}
	r.body = strings.NewReader(strings.Join(params, "&"))
	r.headers["Content-Type"] = "application/x-www-form-urlencoded"
	return r
}

// WithBody 设置原始请求体。
func (r *TestRequest) WithBody(body string) *TestRequest {
	r.body = strings.NewReader(body)
	return r
}

// WithCookie 设置 Cookie。
func (r *TestRequest) WithCookie(cookie *http.Cookie) *TestRequest {
	r.cookies = append(r.cookies, cookie)
	return r
}

// ============================================================================
// 响应断言
// ============================================================================

// TestResponse 测试响应封装。
type TestResponse struct {
	*httptest.ResponseRecorder
}

// Execute 执行请求并返回响应。
func (tc *TestContext) Execute(req *TestRequest) *TestResponse {
	httpReq, _ := http.NewRequest(req.method, req.path, req.body)

	for k, v := range req.headers {
		httpReq.Header.Set(k, v)
	}

	for _, cookie := range req.cookies {
		httpReq.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	tc.Router.ServeHTTP(rec, httpReq)

	return &TestResponse{ResponseRecorder: rec}
}

// StatusCode 返回状态码。
func (r *TestResponse) StatusCode() int {
	return r.Code
}

// Body 返回响应体字符串。
func (r *TestResponse) BodyString() string {
	return r.ResponseRecorder.Body.String()
}

// JSONBody 解析 JSON 响应体。
func (r *TestResponse) JSONBody() map[string]interface{} {
	var result map[string]interface{}
	json.Unmarshal(r.ResponseRecorder.Body.Bytes(), &result)
	return result
}

// Header 获取响应头。
func (r *TestResponse) HeaderValue(key string) string {
	return r.Header().Get(key)
}

// ============================================================================
// 断言辅助
// ============================================================================

// Assert 返回断言辅助器。
func Assert(t *testing.T) *Assertions {
	return &Assertions{t: t}
}

// Assertions 测试断言。
type Assertions struct {
	t *testing.T
}

// Equal 断言相等。
func (a *Assertions) Equal(expected, actual interface{}, msg ...string) {
	a.t.Helper()
	if expected != actual {
		msgStr := ""
		if len(msg) > 0 {
			msgStr = msg[0]
		}
		a.t.Errorf("%s: expected %v, got %v", msgStr, expected, actual)
	}
}

// StatusOK 断言状态码为 200。
func (a *Assertions) StatusOK(resp *TestResponse) {
	a.t.Helper()
	if resp.Code != http.StatusOK {
		a.t.Errorf("expected status 200, got %d: %s", resp.Code, resp.BodyString())
	}
}

// Status 断言指定状态码。
func (a *Assertions) Status(expected int, resp *TestResponse) {
	a.t.Helper()
	if resp.Code != expected {
		a.t.Errorf("expected status %d, got %d: %s", expected, resp.Code, resp.BodyString())
	}
}

// NotNil 断言非 nil。
func (a *Assertions) NotNil(v interface{}, msg ...string) {
	a.t.Helper()
	if v == nil {
		msgStr := "expected not nil"
		if len(msg) > 0 {
			msgStr = msg[0]
		}
		a.t.Error(msgStr)
	}
}

// ============================================================================
// Mock 依赖注入
// ============================================================================

// MockLogger 模拟日志器。
type MockLogger struct {
	Logs []string
}

func (m *MockLogger) Debug(msg string, args ...any) {
	m.Logs = append(m.Logs, "DEBUG: "+msg)
}

func (m *MockLogger) Info(msg string, args ...any) {
	m.Logs = append(m.Logs, "INFO: "+msg)
}

func (m *MockLogger) Warn(msg string, args ...any) {
	m.Logs = append(m.Logs, "WARN: "+msg)
}

func (m *MockLogger) Error(msg string, args ...any) {
	m.Logs = append(m.Logs, "ERROR: "+msg)
}

func (m *MockLogger) With(args ...any) core.Logger { return m }
func (m *MockLogger) SetLevel(level core.LogLevel)  {}

// ============================================================================
// 测试辅助函数
// ============================================================================

// NewTestRouter 创建测试路由器。
func NewTestRouter() *core.RadixRouter {
	return core.NewRadixRouter()
}

// NewTestConfig 创建测试配置。
func NewTestConfig() *core.AppConfig {
	return core.NewAppConfig()
}
