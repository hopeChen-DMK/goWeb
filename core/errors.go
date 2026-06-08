package core

import (
	"fmt"
	"net/http"
	"runtime/debug"
)

// ============================================================================
// HTTP 错误
// ============================================================================

// HTTPError 表示 HTTP 层错误，可携带结构化数据。
type HTTPError struct {
	Code     int    `json:"-"`
	Message  string `json:"message"`
	Internal error  `json:"-"`
	Data     any    `json:"data,omitempty"`
}

// Error 实现 error 接口。
func (e *HTTPError) Error() string {
	if e.Internal != nil {
		return fmt.Sprintf("code=%d, message=%s, internal=%v", e.Code, e.Message, e.Internal)
	}
	return fmt.Sprintf("code=%d, message=%s", e.Code, e.Message)
}

// Unwrap 实现 errors.Unwrap。
func (e *HTTPError) Unwrap() error {
	return e.Internal
}

// WithData 设置附带数据。
func (e *HTTPError) WithData(data any) *HTTPError {
	e.Data = data
	return e
}

// WithInternal 设置内部错误。
func (e *HTTPError) WithInternal(err error) *HTTPError {
	e.Internal = err
	return e
}

// NewHTTPError 创建 HTTP 错误。
func NewHTTPError(code int, message string) *HTTPError {
	return &HTTPError{
		Code:    code,
		Message: message,
	}
}

// ============================================================================
// 便捷错误构造函数
// ============================================================================

// ErrBadRequest 创建 400 错误。
func ErrBadRequest(msg string) *HTTPError {
	return NewHTTPError(http.StatusBadRequest, msg)
}

// ErrUnauthorized 创建 401 错误。
func ErrUnauthorized(msg string) *HTTPError {
	if msg == "" {
		msg = "Unauthorized"
	}
	return NewHTTPError(http.StatusUnauthorized, msg)
}

// ErrForbidden 创建 403 错误。
func ErrForbidden(msg string) *HTTPError {
	if msg == "" {
		msg = "Forbidden"
	}
	return NewHTTPError(http.StatusForbidden, msg)
}

// ErrNotFound 创建 404 错误。
func ErrNotFound(msg string) *HTTPError {
	if msg == "" {
		msg = "Not Found"
	}
	return NewHTTPError(http.StatusNotFound, msg)
}

// ErrMethodNotAllowed 创建 405 错误。
func ErrMethodNotAllowed(msg string) *HTTPError {
	if msg == "" {
		msg = "Method Not Allowed"
	}
	return NewHTTPError(http.StatusMethodNotAllowed, msg)
}

// ErrRequestEntityTooLarge 创建 413 错误。
func ErrRequestEntityTooLarge(msg string) *HTTPError {
	if msg == "" {
		msg = "Request Entity Too Large"
	}
	return NewHTTPError(http.StatusRequestEntityTooLarge, msg)
}

// ErrUnprocessableEntity 创建 422 错误。
func ErrUnprocessableEntity(msg string) *HTTPError {
	if msg == "" {
		msg = "Unprocessable Entity"
	}
	return NewHTTPError(http.StatusUnprocessableEntity, msg)
}

// ErrTooManyRequests 创建 429 错误。
func ErrTooManyRequests(msg string) *HTTPError {
	if msg == "" {
		msg = "Too Many Requests"
	}
	return NewHTTPError(http.StatusTooManyRequests, msg)
}

// ErrInternalServer 创建 500 错误。
func ErrInternalServer(msg string) *HTTPError {
	if msg == "" {
		msg = "Internal Server Error"
	}
	return NewHTTPError(http.StatusInternalServerError, msg)
}

// ErrServiceUnavailable 创建 503 错误。
func ErrServiceUnavailable(msg string) *HTTPError {
	if msg == "" {
		msg = "Service Unavailable"
	}
	return NewHTTPError(http.StatusServiceUnavailable, msg)
}

// ErrGatewayTimeout 创建 504 错误。
func ErrGatewayTimeout(msg string) *HTTPError {
	if msg == "" {
		msg = "Gateway Timeout"
	}
	return NewHTTPError(http.StatusGatewayTimeout, msg)
}

// ============================================================================
// Panic 恢复信息
// ============================================================================

// PanicError 包含 panic 恢复信息。
type PanicError struct {
	Value interface{}
	Stack []byte
}

// Error 实现 error 接口。
func (p *PanicError) Error() string {
	return fmt.Sprintf("panic recovered: %v", p.Value)
}

// RecoverPanic 捕获 panic 并返回 PanicError。
func RecoverPanic() *PanicError {
	if r := recover(); r != nil {
		return &PanicError{
			Value: r,
			Stack: debug.Stack(),
		}
	}
	return nil
}

// ============================================================================
// Validatable 接口
// ============================================================================

// Validatable 可由结构体实现以提供自定义验证逻辑。
type Validatable interface {
	Validate() error
}
