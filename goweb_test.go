package goweb

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/goweb-framework/goweb/core"
)

// ============================================================================
// 路由测试
// ============================================================================

func TestRouterBasic(t *testing.T) {
	app := New()
	app.Init()

	app.GET("/hello", func(c *Context) error {
		return c.String(200, "hello")
	})

	req := httptest.NewRequest("GET", "/hello", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("expected 'hello', got '%s'", rec.Body.String())
	}
}

func TestRouterParams(t *testing.T) {
	app := New()
	app.Init()

	app.GET("/users/:id", func(c *Context) error {
		return c.String(200, c.Param("id"))
	})

	app.GET("/files/*", func(c *Context) error {
		return c.String(200, c.Param("*"))
	})

	app.GET("/api/**", func(c *Context) error {
		return c.String(200, c.Param("**"))
	})

	// 命名参数测试
	req := httptest.NewRequest("GET", "/users/42", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)
	if rec.Body.String() != "42" {
		t.Errorf("expected '42', got '%s'", rec.Body.String())
	}

	// 单段通配符测试
	req = httptest.NewRequest("GET", "/files/avatar.png", nil)
	rec = httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)
	if rec.Body.String() != "avatar.png" {
		t.Errorf("expected 'avatar.png', got '%s'", rec.Body.String())
	}

	// 多段通配符测试
	req = httptest.NewRequest("GET", "/api/v1/users/42", nil)
	rec = httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)
	if rec.Body.String() != "v1/users/42" {
		t.Errorf("expected 'v1/users/42', got '%s'", rec.Body.String())
	}
}

func TestRouter404(t *testing.T) {
	app := New()
	app.Init()

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestRouter405(t *testing.T) {
	app := New()
	app.Init()

	app.GET("/resource", func(c *Context) error {
		return c.String(200, "ok")
	})

	req := httptest.NewRequest("POST", "/resource", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != 405 {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// ============================================================================
// JSON 响应测试
// ============================================================================

func TestJSONResponse(t *testing.T) {
	app := New()
	app.Init()

	app.GET("/json", func(c *Context) error {
		return c.JSON(200, core.Map{"message": "hello"})
	})

	req := httptest.NewRequest("GET", "/json", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["message"] != "hello" {
		t.Errorf("expected 'hello', got '%s'", resp["message"])
	}
}

// ============================================================================
// 中间件测试
// ============================================================================

func TestRecoveryMiddleware(t *testing.T) {
	app := New()
	app.UseRecovery()
	app.Init()

	app.GET("/panic", func(c *Context) error {
		panic("test panic")
	})

	req := httptest.NewRequest("GET", "/panic", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != 500 {
		t.Errorf("expected 500 after panic, got %d", rec.Code)
	}
}

func TestSecureMiddleware(t *testing.T) {
	app := New()
	app.UseSecure()
	app.Init()

	app.GET("/secure", func(c *Context) error {
		return c.String(200, "ok")
	})

	req := httptest.NewRequest("GET", "/secure", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options header")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options header")
	}
}

func TestBodyLimitMiddleware(t *testing.T) {
	app := New()
	app.UseBodyLimit(100)
	app.Init()

	app.POST("/data", func(c *Context) error {
		return c.String(200, "ok")
	})

	req := httptest.NewRequest("POST", "/data", bytes.NewReader(make([]byte, 200)))
	req.ContentLength = 200
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != 413 {
		t.Errorf("expected 413 for oversized body, got %d", rec.Code)
	}
}

// ============================================================================
// 路由组测试
// ============================================================================

func TestGroup(t *testing.T) {
	app := New()
	app.Init()

	v1 := app.Group("/api/v1")
	v1.GET("/users", func(c *Context) error {
		return c.String(200, "v1 users")
	})

	v2 := app.Group("/api/v2")
	v2.GET("/users", func(c *Context) error {
		return c.String(200, "v2 users")
	})

	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)
	if rec.Body.String() != "v1 users" {
		t.Errorf("expected 'v1 users', got '%s'", rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/v2/users", nil)
	rec = httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)
	if rec.Body.String() != "v2 users" {
		t.Errorf("expected 'v2 users', got '%s'", rec.Body.String())
	}
}

// ============================================================================
// 上下文测试
// ============================================================================

func TestContextRequestID(t *testing.T) {
	app := New()
	app.Init()

	var capturedID string
	app.GET("/id", func(c *Context) error {
		capturedID = c.RequestID()
		return c.String(200, capturedID)
	})

	req := httptest.NewRequest("GET", "/id", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if capturedID == "" {
		t.Error("request ID should not be empty")
	}
	if capturedID != rec.Body.String() {
		t.Error("request ID in context should match response")
	}
}

func TestContextQueryParams(t *testing.T) {
	app := New()
	app.Init()

	app.GET("/search", func(c *Context) error {
		q := c.QueryParam("q")
		page := c.QueryParamDefault("page", "1")
		return c.JSON(200, core.Map{"q": q, "page": page})
	})

	req := httptest.NewRequest("GET", "/search?q=golang", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["q"] != "golang" {
		t.Errorf("expected 'golang', got '%s'", resp["q"])
	}
	if resp["page"] != "1" {
		t.Errorf("expected '1', got '%s'", resp["page"])
	}
}

func TestContextBindJSON(t *testing.T) {
	app := New()
	app.Init()

	type User struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	app.POST("/user", func(c *Context) error {
		var user User
		if err := c.BindJSON(&user); err != nil {
			return c.JSON(400, core.Map{"error": err.Error()})
		}
		return c.JSON(200, core.Map{"name": user.Name, "age": user.Age})
	})

	body := `{"name":"Alice","age":30}`
	req := httptest.NewRequest("POST", "/user", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ============================================================================
// 密码测试
// ============================================================================

func TestPasswordHash(t *testing.T) {
	password := "mySecret123"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash failed: %v", err)
	}

	if !VerifyPassword(password, hash) {
		t.Error("password verification failed")
	}

	if VerifyPassword("wrongPassword", hash) {
		t.Error("wrong password should not verify")
	}
}

// ============================================================================
// 弃用路由测试
// ============================================================================

func TestDeprecatedRoute(t *testing.T) {
	app := New()
	app.Init()

	app.GET("/old-api", func(c *Context) error {
		return c.String(200, "old")
	})

	app.DeprecatedRoute("GET", "/old-api", "Use /new-api instead", "Mon, 01 Jan 2027 00:00:00 GMT")

	req := httptest.NewRequest("GET", "/old-api", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Header().Get("Deprecation") != "true" {
		t.Error("missing Deprecation header")
	}
	if rec.Header().Get("Sunset") == "" {
		t.Error("missing Sunset header")
	}
}

// ============================================================================
// 并发安全测试
// ============================================================================

func TestConcurrentRequests(t *testing.T) {
	app := New()
	app.Init()

	app.GET("/concurrent", func(c *Context) error {
		return c.String(200, "ok")
	})

	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func() {
			req := httptest.NewRequest("GET", "/concurrent", nil)
			rec := httptest.NewRecorder()
			app.Router.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("concurrent request failed with %d", rec.Code)
			}
			done <- true
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}
}

// ============================================================================
// CORS 中间件测试
// ============================================================================

func TestCORSMiddleware(t *testing.T) {
	app := New()
	app.UseCORS()
	app.Init()

	app.GET("/cors", func(c *Context) error {
		return c.String(200, "ok")
	})

	req := httptest.NewRequest("OPTIONS", "/cors", nil)
	req.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Errorf("expected 204 for OPTIONS preflight, got %d", rec.Code)
	}
}

// ============================================================================
// 上下文 Key/Value 测试
// ============================================================================

func TestContextSetGet(t *testing.T) {
	app := New()
	app.Init()

	app.GET("/ctx", func(c *Context) error {
		c.Set("user_id", "123")
		c.Set("role", "admin")

		userID := c.GetString("user_id")
		role := c.GetString("role")

		return c.JSON(200, core.Map{"user_id": userID, "role": role})
	})

	req := httptest.NewRequest("GET", "/ctx", nil)
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["user_id"] != "123" {
		t.Errorf("expected '123', got '%s'", resp["user_id"])
	}
	if resp["role"] != "admin" {
		t.Errorf("expected 'admin', got '%s'", resp["role"])
	}
}
