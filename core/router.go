package core

import (
	"net/http"
	"strings"
	"sync"
)

// ============================================================================
// 压缩 Radix Tree 节点
// ============================================================================

// nodeKind 表示节点类型。
type nodeKind uint8

const (
	kindStatic    nodeKind = iota // 静态路径段 /users
	kindParam                     // 命名参数 :id
	kindWildcard                  // 单段通配符 *
	kindWildcardMulti             // 多段通配符 **
)

// radixNode 压缩 Radix Tree 节点。
type radixNode struct {
	kind     nodeKind
	prefix   string        // 压缩前缀
	paramKey string        // 参数名（kindParam 时使用）
	handler  HandlerFunc   // 路由处理器
	children []*radixNode  // 子节点（按前缀排序）
	indices  []byte        // 子节点首字符索引
	parent   *radixNode

	// 中间件
	middlewares []MiddlewareFunc
}

// 节点类型排序优先级（数字越小越优先匹配）：
// kindStatic < kindParam < kindWildcard < kindWildcardMulti
func (n *radixNode) priority() int {
	switch n.kind {
	case kindStatic:
		return 0
	case kindParam:
		return 1
	case kindWildcard:
		return 2
	case kindWildcardMulti:
		return 3
	default:
		return 4
	}
}

// findByKind 在同一层级中查找指定类型的子节点。
func (n *radixNode) findByKind(kind nodeKind) *radixNode {
	for _, child := range n.children {
		if child.kind == kind {
			return child
		}
	}
	return nil
}

// ============================================================================
// 方法树
// ============================================================================

// methodTree 每个 HTTP 方法对应一棵树。
type methodTree struct {
	tree *radixNode
}

// ============================================================================
// RadixRouter 路由器实现
// ============================================================================

// RadixRouter 基于压缩 Radix Tree 的路由器。
// 每个 HTTP 方法一棵树，运行时只读无锁。
type RadixRouter struct {
	trees map[string]*methodTree // method -> tree
	mu    sync.RWMutex           // 仅在注册阶段（启动时）使用

	// 全局中间件
	globalMiddleware []MiddlewareFunc

	// 静态文件
	staticRoutes map[string]string

	// 路由的弃用信息
	deprecatedRoutes map[string]*DeprecationInfo

	// 404 处理器
	notFoundHandler HandlerFunc

	// 405 处理器
	methodNotAllowedHandler HandlerFunc

	// 路由就绪标志
	routesReady bool

	// 路由元数据（用于文档生成）
	routeMeta []RouteMeta
}

// DeprecationInfo 路由弃用信息。
type DeprecationInfo struct {
	Message   string
	Sunset    string // RFC 1123 格式日期
	Deprecated bool
}

// RouteMeta 路由元数据。
type RouteMeta struct {
	Method      string
	Path        string
	HandlerName string
	Deprecated  bool
	Deprecation *DeprecationInfo
	Group       string
}

// NewRadixRouter 创建新的路由器实例。
func NewRadixRouter() *RadixRouter {
	r := &RadixRouter{
		trees:            make(map[string]*methodTree),
		staticRoutes:     make(map[string]string),
		deprecatedRoutes: make(map[string]*DeprecationInfo),
		routeMeta:        make([]RouteMeta, 0),
	}
	r.notFoundHandler = defaultNotFound
	r.methodNotAllowedHandler = defaultMethodNotAllowed

	// 初始化常用方法树
	for _, m := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodPatch, http.MethodHead, http.MethodOptions, http.MethodConnect, http.MethodTrace,
	} {
		r.trees[m] = &methodTree{tree: &radixNode{}}
	}

	return r
}

// ============================================================================
// 路由注册方法
// ============================================================================

// GET 注册 GET 路由。
func (r *RadixRouter) GET(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodGet, path, handler, mw...)
	return r
}

// POST 注册 POST 路由。
func (r *RadixRouter) POST(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodPost, path, handler, mw...)
	return r
}

// PUT 注册 PUT 路由。
func (r *RadixRouter) PUT(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodPut, path, handler, mw...)
	return r
}

// DELETE 注册 DELETE 路由。
func (r *RadixRouter) DELETE(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodDelete, path, handler, mw...)
	return r
}

// PATCH 注册 PATCH 路由。
func (r *RadixRouter) PATCH(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodPatch, path, handler, mw...)
	return r
}

// HEAD 注册 HEAD 路由。
func (r *RadixRouter) HEAD(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodHead, path, handler, mw...)
	return r
}

// OPTIONS 注册 OPTIONS 路由。
func (r *RadixRouter) OPTIONS(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodOptions, path, handler, mw...)
	return r
}

// CONNECT 注册 CONNECT 路由。
func (r *RadixRouter) CONNECT(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodConnect, path, handler, mw...)
	return r
}

// TRACE 注册 TRACE 路由。
func (r *RadixRouter) TRACE(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.registerRoute(http.MethodTrace, path, handler, mw...)
	return r
}

// ANY 注册到所有 HTTP 方法的路由（用于 Handle 风格的全局处理器）。
func (r *RadixRouter) ANY(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	r.mu.Lock()
	defer r.mu.Unlock()

	if path == "" {
		path = "/"
	}
	if path[0] != '/' {
		path = "/" + path
	}
	if path != "/" && len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}

	for method := range r.trees {
		r.insertNode(r.trees[method].tree, path, handler, mw)
	}
	return r
}

// Use 注册全局中间件。
func (r *RadixRouter) Use(mw ...MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalMiddleware = append(r.globalMiddleware, mw...)
}

// Group 创建路由组。
func (r *RadixRouter) Group(prefix string) *Group {
	return &Group{
		prefix:      prefix,
		router:      r,
		middlewares: make([]MiddlewareFunc, 0),
	}
}

// ============================================================================
// 核心路由注册
// ============================================================================

func (r *RadixRouter) registerRoute(method, path string, handler HandlerFunc, mw ...MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if path == "" {
		path = "/"
	}
	if path[0] != '/' {
		path = "/" + path
	}
	// 去除末尾尾部斜杠（根路径除外）
	if path != "/" && len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}

	mt, ok := r.trees[method]
	if !ok {
		mt = &methodTree{tree: &radixNode{}}
		r.trees[method] = mt
	}

	r.insertNode(mt.tree, path, handler, mw)
}

// insertNode 将路径插入到 Radix Tree 中。
func (r *RadixRouter) insertNode(root *radixNode, path string, handler HandlerFunc, mw []MiddlewareFunc) {
	// 移除前导 /
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	current := root

	for {
		if path == "" {
			current.handler = handler
			current.middlewares = mw
			return
		}

		// 解析下一个路径段
		segment, remaining, isLast := r.nextSegment(path)
		var kind nodeKind
		var paramKey string

		switch {
		case segment == "**":
			kind = kindWildcardMulti
			paramKey = ""
		case segment == "*":
			kind = kindWildcard
			paramKey = ""
		case len(segment) > 0 && segment[0] == ':':
			kind = kindParam
			paramKey = segment[1:]
		default:
			kind = kindStatic
		}

		// 在当前节点的子节点中查找匹配
		found := false
		for i, child := range current.children {
			if child.kind == kind {
				if kind == kindStatic {
					// 静态节点：检查是否共享前缀
					commonPrefix := commonPrefixLen(child.prefix, segment)
					if commonPrefix == 0 {
						continue
					}

					if commonPrefix < len(child.prefix) || commonPrefix < len(segment) {
						// 需要分裂节点
						r.splitNode(current, i, commonPrefix, segment, remaining, isLast, handler, mw, kind, paramKey)
					} else {
						// 完全匹配，继续向下
						current = child
					}
				} else if kind == kindParam && child.paramKey == paramKey {
					// 命名参数完全匹配
					if isLast {
						child.handler = handler
						child.middlewares = mw
						return
					}
					current = child
				} else if kind == kindWildcard || kind == kindWildcardMulti {
					if isLast {
						child.handler = handler
						child.middlewares = mw
						return
					}
					// 通配符后不能再有子路径 → 冲突
					panic("route conflict: wildcard segment must be the last segment in path")
				}
				found = true
				break
			}
		}

		if !found {
			// 不存在匹配的子节点，创建新的
			newNode := &radixNode{
				kind:       kind,
				prefix:     segment,
				paramKey:   paramKey,
				parent:     current,
			}

			if isLast {
				newNode.handler = handler
				newNode.middlewares = mw
			}

			r.addChildSorted(current, newNode)

			if isLast {
				return
			}

			current = newNode
		}

		path = remaining
		if isLast {
			return
		}
	}
}

// splitNode 分裂静态节点以共享前缀。
func (r *RadixRouter) splitNode(parent *radixNode, childIdx, commonLen int,
	segment, remaining string, isLast bool, handler HandlerFunc, mw []MiddlewareFunc, kind nodeKind, paramKey string) {

	child := parent.children[childIdx]

	// 子节点的后缀部分
	suffix := child.prefix[commonLen:]
	suffixNode := &radixNode{
		kind:        child.kind,
		prefix:      suffix,
		paramKey:    child.paramKey,
		handler:     child.handler,
		children:    child.children,
		middlewares: child.middlewares,
		parent:      child,
	}

	// 更新子节点为共享前缀
	child.prefix = child.prefix[:commonLen]
	child.indices = []byte{suffix[0]}
	child.children = []*radixNode{suffixNode}
	child.handler = nil
	child.middlewares = nil
	child.paramKey = ""
	child.kind = kindStatic

	for _, gchild := range suffixNode.children {
		gchild.parent = suffixNode
	}

	// 构建剩余路径并递归插入
	remainingSegment := segment[commonLen:]
	insertPath := remainingSegment
	if !isLast {
		insertPath += "/" + remaining
	}
	insertPath = "/" + insertPath
	r.insertNode(child, insertPath, handler, mw)
}

// nextSegment 解析路径的下一个段。
func (r *RadixRouter) nextSegment(path string) (segment, remaining string, isLast bool) {
	if path == "" {
		return "", "", true
	}

	// 处理多段通配符 **
	if strings.HasPrefix(path, "**") {
		if len(path) == 2 {
			return "**", "", true
		}
		return "**", path[2:], false
	}

	idx := strings.IndexByte(path, '/')
	if idx == -1 {
		return path, "", true
	}
	return path[:idx], path[idx+1:], false
}

// commonPrefixLen 计算两个字符串的最长公共前缀长度。
func commonPrefixLen(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// addChildSorted 按优先级插入子节点：static < param < wildcard < wildcardMulti
func (r *RadixRouter) addChildSorted(parent *radixNode, child *radixNode) {
	priority := child.priority()

	insertIdx := len(parent.children)
	for i, c := range parent.children {
		if c.priority() > priority {
			insertIdx = i
			break
		}
	}

	parent.children = append(parent.children, nil)
	copy(parent.children[insertIdx+1:], parent.children[insertIdx:])
	parent.children[insertIdx] = child

	// 更新索引
	if child.prefix != "" {
		parent.indices = append(parent.indices, child.prefix[0])
	} else {
		parent.indices = append(parent.indices, 0)
	}
}

// ============================================================================
// 路由查找
// ============================================================================

// routeResult 路由查找结果。
type routeResult struct {
	handler     HandlerFunc
	params      Params
	middlewares []MiddlewareFunc
}

// find 在方法树中查找路由匹配。
func (r *RadixRouter) find(method, path string) *routeResult {
	mt, ok := r.trees[method]
	if !ok {
		return nil
	}

	if path == "" {
		path = "/"
	}
	if path[0] != '/' {
		path = "/" + path
	}
	// 标准化：去除尾部斜杠
	if len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}

	path = path[1:] // 去掉前导 /

	result := &routeResult{
		params: make(Params, 0),
	}

	node := r.searchTree(mt.tree, path, result)
	if node != nil && node.handler != nil {
		result.handler = node.handler
		result.middlewares = node.middlewares
		return result
	}

	return nil
}

func (r *RadixRouter) searchTree(node *radixNode, path string, result *routeResult) *radixNode {
	if path == "" {
		return node
	}

	for _, child := range node.children {
		switch child.kind {
		case kindStatic:
			if strings.HasPrefix(path, child.prefix) {
				remaining := path[len(child.prefix):]
				if len(remaining) > 0 && remaining[0] == '/' {
					remaining = remaining[1:]
				}
				if found := r.searchTree(child, remaining, result); found != nil {
					return found
				}
			}

		case kindParam:
			idx := strings.IndexByte(path, '/')
			var val string
			var remaining string
			if idx == -1 {
				val = path
				remaining = ""
			} else {
				val = path[:idx]
				remaining = path[idx+1:]
			}
			result.params = append(result.params, Param{Key: child.paramKey, Value: val})

			if found := r.searchTree(child, remaining, result); found != nil {
				return found
			}
			// 回溯
			result.params = result.params[:len(result.params)-1]

		case kindWildcard:
			idx := strings.IndexByte(path, '/')
			var val string
			var remaining string
			if idx == -1 {
				val = path
				remaining = ""
			} else {
				val = path[:idx]
				remaining = ""
			}
			result.params = append(result.params, Param{Key: "*", Value: val})

			if child.handler != nil && remaining == "" {
				return child
			}
			return nil

		case kindWildcardMulti:
			result.params = append(result.params, Param{Key: "**", Value: path})
			if child.handler != nil {
				return child
			}
			return nil
		}
	}
	return nil
}

// ============================================================================
// http.Handler 接口实现
// ============================================================================

// ServeHTTP 实现 http.Handler 接口。
func (r *RadixRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 路由锁定后只读访问，无锁
	if !r.routesReady {
		r.mu.RLock()
		r.routesReady = true
		r.mu.RUnlock()
	}

	result := r.find(req.Method, req.URL.Path)

	if result == nil {
		// 检查是否是 405（路径存在但方法不匹配）
		for method := range r.trees {
			if method == req.Method {
				continue
			}
			if altResult := r.find(method, req.URL.Path); altResult != nil {
				if r.methodNotAllowedHandler != nil {
					c := acquireContext(w, req)
					defer releaseContext(c)
					c.SetParams(Params{})
					r.executeWithMiddleware(c, r.methodNotAllowedHandler, r.globalMiddleware)
				} else {
					w.Header().Set("Allow", r.getAllowedMethods(req.URL.Path))
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
				return
			}
		}

		// 404
		c := acquireContext(w, req)
		defer releaseContext(c)
		c.SetParams(Params{})

		// 检查弃用信息
		if depInfo, ok := r.deprecatedRoutes[req.URL.Path]; ok && depInfo.Deprecated {
			c.responseWriter.Header().Set("Deprecation", "true")
			if depInfo.Sunset != "" {
				c.responseWriter.Header().Set("Sunset", depInfo.Sunset)
			}
		}

		r.executeWithMiddleware(c, r.notFoundHandler, r.globalMiddleware)
		return
	}

	// 正常处理
	c := acquireContext(w, req)
	defer releaseContext(c)
	c.SetParams(result.params)

	// 检查弃用
	routeKey := req.Method + " " + req.URL.Path
	if depInfo, ok := r.deprecatedRoutes[routeKey]; ok && depInfo.Deprecated {
		c.responseWriter.Header().Set("Deprecation", "true")
		if depInfo.Sunset != "" {
			c.responseWriter.Header().Set("Sunset", depInfo.Sunset)
		}
	}

	// 合并全局中间件和路由级中间件
	allMW := make([]MiddlewareFunc, 0, len(r.globalMiddleware)+len(result.middlewares))
	allMW = append(allMW, r.globalMiddleware...)
	allMW = append(allMW, result.middlewares...)

	r.executeWithMiddleware(c, result.handler, allMW)
}

func (r *RadixRouter) executeWithMiddleware(c *Context, handler HandlerFunc, middlewares []MiddlewareFunc) {
	// 洋葱模型：从前往后，最后一层是实际 handler
	final := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		final = middlewares[i](final)
	}
	// 执行
	if err := final(c); err != nil {
		c.SetError(err)
	}
}

func (r *RadixRouter) getAllowedMethods(path string) string {
	methods := make([]string, 0)
	for method := range r.trees {
		if result := r.find(method, path); result != nil {
			methods = append(methods, method)
		}
	}
	return strings.Join(methods, ", ")
}

// ============================================================================
// 默认处理器
// ============================================================================

func defaultNotFound(c *Context) error {
	return c.JSON(http.StatusNotFound, Response{
		Code:    http.StatusNotFound,
		Message: "Not Found",
	})
}

func defaultMethodNotAllowed(c *Context) error {
	return c.JSON(http.StatusMethodNotAllowed, Response{
		Code:    http.StatusMethodNotAllowed,
		Message: "Method Not Allowed",
	})
}

// ============================================================================
// 路由筛选器 / 工具方法
// ============================================================================

// MarkDeprecated 将路由标记为弃用。
func (r *RadixRouter) MarkDeprecated(method, path string, info DeprecationInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := method + " " + path
	info.Deprecated = true
	r.deprecatedRoutes[key] = &info
}

// Deprecated 创建弃用路由信息。
func Deprecated(message string, sunset ...string) DeprecationInfo {
	d := DeprecationInfo{
		Message:   message,
		Deprecated: true,
	}
	if len(sunset) > 0 {
		d.Sunset = sunset[0]
	}
	return d
}

// Match 检查一个请求路径是否匹配（用于测试/调试）。
func (r *RadixRouter) Match(method, path string) bool {
	return r.find(method, path) != nil
}

// AllRoutes 返回所有已注册路由。
func (r *RadixRouter) AllRoutes() []RouteMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.routeMeta
}

// ============================================================================
// 路由组
// ============================================================================

// Group 路由组。
type Group struct {
	prefix      string
	router      *RadixRouter
	middlewares []MiddlewareFunc
}

// Use 为路由组添加中间件。
func (g *Group) Use(mw ...MiddlewareFunc) {
	g.middlewares = append(g.middlewares, mw...)
}

// Group 创建嵌套路由组。
func (g *Group) Group(relativePath string) *Group {
	return &Group{
		prefix:      joinPaths(g.prefix, relativePath),
		router:      g.router,
		middlewares: append([]MiddlewareFunc{}, g.middlewares...),
	}
}

func (g *Group) handle(method, relativePath string, handler HandlerFunc, mw ...MiddlewareFunc) {
	fullPath := joinPaths(g.prefix, relativePath)
	allMW := append(append([]MiddlewareFunc{}, g.middlewares...), mw...)
	g.router.registerRoute(method, fullPath, handler, allMW...)
}

// GET 注册 GET 路由。
func (g *Group) GET(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodGet, path, handler, mw...)
	return g.router
}

// POST 注册 POST 路由。
func (g *Group) POST(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodPost, path, handler, mw...)
	return g.router
}

// PUT 注册 PUT 路由。
func (g *Group) PUT(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodPut, path, handler, mw...)
	return g.router
}

// DELETE 注册 DELETE 路由。
func (g *Group) DELETE(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodDelete, path, handler, mw...)
	return g.router
}

// PATCH 注册 PATCH 路由。
func (g *Group) PATCH(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodPatch, path, handler, mw...)
	return g.router
}

// HEAD 注册 HEAD 路由。
func (g *Group) HEAD(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodHead, path, handler, mw...)
	return g.router
}

// OPTIONS 注册 OPTIONS 路由。
func (g *Group) OPTIONS(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodOptions, path, handler, mw...)
	return g.router
}

// CONNECT 注册 CONNECT 路由。
func (g *Group) CONNECT(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodConnect, path, handler, mw...)
	return g.router
}

// TRACE 注册 TRACE 路由。
func (g *Group) TRACE(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	g.handle(http.MethodTrace, path, handler, mw...)
	return g.router
}

// ANY 注册到所有 HTTP 方法的路由。
func (g *Group) ANY(path string, handler HandlerFunc, mw ...MiddlewareFunc) Router {
	fullPath := joinPaths(g.prefix, path)
	allMW := append(append([]MiddlewareFunc{}, g.middlewares...), mw...)
	g.router.ANY(fullPath, handler, allMW...)
	return g.router
}

// Static 注册静态文件服务。
func (g *Group) Static(relativePath, root string) {
	fullPath := joinPaths(g.prefix, relativePath)
	g.router.Static(fullPath, root)
}

// Static 注册静态文件服务。
func (r *RadixRouter) Static(prefix, root string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prefix == "" {
		prefix = "/"
	}
	r.staticRoutes[prefix] = root
}

// joinPaths 合并路径。
func joinPaths(absolute, relative string) string {
	if relative == "" {
		return absolute
	}

	finalPath := absolute
	if !strings.HasSuffix(absolute, "/") && !strings.HasPrefix(relative, "/") {
		finalPath += "/"
	}
	finalPath += relative

	return finalPath
}

// ============================================================================
// 中间件构建器（洋葱模型）
// ============================================================================

// BuildChain 构建洋葱模型中间件链。
func BuildChain(handler HandlerFunc, middlewares ...MiddlewareFunc) HandlerFunc {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}
