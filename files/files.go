// Package files 提供文件上传、下载、分块续传和存储抽象。
package files

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/goweb-framework/goweb/core"
)

// ============================================================================
// 文件下载
// ============================================================================

// DownloadConfig 文件下载配置。
type DownloadConfig struct {
	// Attachment 是否为附件模式（false 为内联）
	Attachment bool
	// Filename 下载文件名
	Filename string
	// ContentType MIME 类型
	ContentType string
	// CacheControl Cache-Control 头
	CacheControl string
	// ETag ETag 头
	ETag string
	// SupportRange 是否支持 Range 请求
	SupportRange bool
}

// DefaultDownloadConfig 返回默认下载配置。
func DefaultDownloadConfig() DownloadConfig {
	return DownloadConfig{
		Attachment:   true,
		CacheControl: "private, max-age=3600",
		SupportRange: true,
	}
}

// SendFile 发送文件（支持 Range、ETag、Cache-Control）。
func SendFile(c *core.Context, filePath string, config ...DownloadConfig) error {
	cfg := DefaultDownloadConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	// 打开文件
	file, err := os.Open(filePath)
	if err != nil {
		return core.ErrNotFound("file not found").WithInternal(err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return core.ErrInternalServer("read file stat").WithInternal(err)
	}

	fileSize := stat.Size()

	// 确定 MIME 类型
	contentType := cfg.ContentType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(filePath))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}

	// ETag（基于修改时间和大小）
	etag := cfg.ETag
	if etag == "" {
		etag = fmt.Sprintf(`"%x-%x"`, stat.ModTime().UnixNano(), fileSize)
	}

	// 检查 If-None-Match
	if match := c.GetHeader("If-None-Match"); match != "" && match == etag {
		return c.NoContent()
	}

	// 设置文件名
	filename := cfg.Filename
	if filename == "" {
		filename = filepath.Base(filePath)
	}

	// RFC 5987 + ASCII fallback 双文件名
	if cfg.Attachment {
		c.SetHeader("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
				toASCII(filename), urlEncode(filename)))
	} else {
		c.SetHeader("Content-Disposition",
			fmt.Sprintf(`inline; filename="%s"`, toASCII(filename)))
	}

	// Cache-Control 和 ETag
	if cfg.CacheControl != "" {
		c.SetHeader("Cache-Control", cfg.CacheControl)
	}
	c.SetHeader("ETag", etag)
	c.SetHeader("Accept-Ranges", "bytes")
	c.SetHeader("Content-Type", contentType)

	// Range 请求处理
	rangeHeader := c.GetHeader("Range")
	if cfg.SupportRange && rangeHeader != "" {
		return serveRange(c, file, rangeHeader, fileSize, contentType)
	}

	// 完整响应
	c.SetHeader("Content-Length", fmt.Sprintf("%d", fileSize))
	c.Status(200)

	_, err = io.Copy(c.ResponseWriter(), file)
	return err
}

// serveRange 处理 HTTP Range 请求。
func serveRange(c *core.Context, file *os.File, rangeHeader string, fileSize int64, contentType string) error {
	ranges, err := parseRange(rangeHeader, fileSize)
	if err != nil || len(ranges) == 0 {
		c.SetHeader("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		return c.JSON(416, core.Response{
			Code:    416,
			Message: "Range Not Satisfiable",
		})
	}

	// 单段 Range
	r := ranges[0]
	c.Status(206)
	c.SetHeader("Content-Range", fmt.Sprintf("bytes %d-%d/%d", r.start, r.end, fileSize))
	c.SetHeader("Content-Length", fmt.Sprintf("%d", r.end-r.start+1))
	c.SetHeader("Content-Type", contentType)

	file.Seek(r.start, io.SeekStart)
	limitReader := io.LimitReader(file, r.end-r.start+1)
	_, err = io.Copy(c.ResponseWriter(), limitReader)
	return err
}

type byteRange struct {
	start, end int64
}

func parseRange(s string, size int64) ([]byteRange, error) {
	if !strings.HasPrefix(s, "bytes=") {
		return nil, fmt.Errorf("invalid range")
	}

	s = s[6:]
	ranges := strings.Split(s, ",")
	result := make([]byteRange, 0)

	for _, r := range ranges {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}

		i := strings.Index(r, "-")
		if i < 0 {
			return nil, fmt.Errorf("invalid range")
		}

		startStr := strings.TrimSpace(r[:i])
		endStr := strings.TrimSpace(r[i+1:])

		var start, end int64

		if startStr == "" {
			// -N: 最后 N 字节
			var suffix int64
			fmt.Sscanf(endStr, "%d", &suffix)
			start = size - suffix
			end = size - 1
		} else {
			fmt.Sscanf(startStr, "%d", &start)
			if endStr == "" {
				end = size - 1
			} else {
				fmt.Sscanf(endStr, "%d", &end)
			}
		}

		if start < 0 || end < 0 || start > end || start >= size {
			continue
		}
		if end >= size {
			end = size - 1
		}

		result = append(result, byteRange{start, end})
	}

	return result, nil
}

// ============================================================================
// 流式文件上传（直接写磁盘，不内存缓冲）
// ============================================================================

// UploadConfig 上传配置。
type UploadConfig struct {
	// DestDir 目标目录
	DestDir string
	// MaxSize 最大文件大小
	MaxSize int64
	// AllowedTypes 允许的 MIME 类型
	AllowedTypes []string
	// RandomFilename 是否随机文件名
	RandomFilename bool
	// MagicCheck 是否启用魔数检测
	MagicCheck bool
	// PreventPathTraversal 防止路径穿越
	PreventPathTraversal bool
}

// DefaultUploadConfig 返回默认上传配置。
func DefaultUploadConfig() UploadConfig {
	return UploadConfig{
		DestDir:              os.TempDir(),
		MaxSize:              10 << 20, // 10MB
		RandomFilename:       true,
		MagicCheck:           true,
		PreventPathTraversal: true,
	}
}

// UploadSingleFile 上传单个文件（流式写入磁盘）。
func UploadSingleFile(c *core.Context, fieldName string, config ...UploadConfig) (string, error) {
	cfg := DefaultUploadConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	fileHeader, err := c.FormFile(fieldName)
	if err != nil {
		return "", core.ErrBadRequest("file field not found: " + err.Error())
	}

	// 大小检查
	if fileHeader.Size > cfg.MaxSize {
		return "", core.ErrRequestEntityTooLarge("file too large")
	}

	// MIME 检查
	if len(cfg.AllowedTypes) > 0 {
		contentType := fileHeader.Header.Get("Content-Type")
		allowed := false
		for _, t := range cfg.AllowedTypes {
			if strings.EqualFold(t, contentType) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", core.ErrBadRequest("file type not allowed: " + contentType)
		}
	}

	// 文件名处理
	filename := fileHeader.Filename
	if cfg.RandomFilename {
		ext := filepath.Ext(filename)
		filename = generateRandomName() + ext
	}

	// 防路径穿越
	if cfg.PreventPathTraversal {
		filename = filepath.Base(filename) // 去除任何路径前缀
	}

	// 完整路径
	destPath := filepath.Join(cfg.DestDir, filename)
	destPath = filepath.Clean(destPath)

	// 确认目标在允许的目录下
	if cfg.PreventPathTraversal {
		absDir, _ := filepath.Abs(cfg.DestDir)
		absDest, _ := filepath.Abs(destPath)
		if !strings.HasPrefix(absDest, absDir) {
			return "", core.ErrForbidden("path traversal detected")
		}
	}

	// 确保目标目录存在
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", core.ErrInternalServer("create upload dir").WithInternal(err)
	}

	// 流式写入磁盘
	if err := c.SaveUploadedFile(fileHeader, destPath, cfg.MagicCheck); err != nil {
		return "", core.ErrInternalServer("save file").WithInternal(err)
	}

	return destPath, nil
}

// UploadMultipleFiles 上传多个文件。
func UploadMultipleFiles(c *core.Context, fieldName string, config ...UploadConfig) ([]string, error) {
	cfg := DefaultUploadConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	form, err := c.MultipartForm()
	if err != nil {
		return nil, core.ErrBadRequest("multipart form error: " + err.Error())
	}

	files := form.File[fieldName]
	if len(files) == 0 {
		return nil, core.ErrBadRequest("no files found in field: " + fieldName)
	}

	paths := make([]string, 0, len(files))
	for _, fh := range files {
		// 模拟单文件上传流程
		path, err := saveMultipartFile(c, fh, cfg)
		if err != nil {
			return paths, err
		}
		paths = append(paths, path)
	}

	return paths, nil
}

func saveMultipartFile(c *core.Context, fh *multipart.FileHeader, cfg UploadConfig) (string, error) {
	if fh.Size > cfg.MaxSize {
		return "", core.ErrRequestEntityTooLarge(fmt.Sprintf("file %s too large", fh.Filename))
	}

	filename := fh.Filename
	if cfg.RandomFilename {
		ext := filepath.Ext(filename)
		filename = generateRandomName() + ext
	}

	if cfg.PreventPathTraversal {
		filename = filepath.Base(filename)
	}

	destPath := filepath.Join(cfg.DestDir, filename)
	destPath = filepath.Clean(destPath)

	if cfg.PreventPathTraversal {
		absDir, _ := filepath.Abs(cfg.DestDir)
		absDest, _ := filepath.Abs(destPath)
		if !strings.HasPrefix(absDest, absDir) {
			return "", core.ErrForbidden("path traversal detected")
		}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", core.ErrInternalServer("create dir").WithInternal(err)
	}

	if err := c.SaveUploadedFile(fh, destPath, cfg.MagicCheck); err != nil {
		return "", core.ErrInternalServer("save file").WithInternal(err)
	}

	return destPath, nil
}

// ============================================================================
// 分块上传
// ============================================================================

// ChunkUploadSession 分块上传会话。
type ChunkUploadSession struct {
	ID          string
	Filename    string
	DestPath    string
	TotalChunks int
	Chunks      map[int]string // chunkIndex -> tempFilePath
	CreatedAt   time.Time
	ExpiresAt   time.Time
	mu          sync.Mutex
}

// ChunkUploadManager 分块上传管理器。
type ChunkUploadManager struct {
	sessions    map[string]*ChunkUploadSession
	mu          sync.RWMutex
	maxSessions int
	maxTotalSize int64
	tempDir     string
	sessionTTL  time.Duration
}

// NewChunkUploadManager 创建分块上传管理器。
func NewChunkUploadManager(tempDir string, maxSessions int, maxTotalSize int64, ttl time.Duration) *ChunkUploadManager {
	if tempDir == "" {
		tempDir = os.TempDir() + "/chunk_uploads"
	}
	os.MkdirAll(tempDir, 0755)

	mgr := &ChunkUploadManager{
		sessions:     make(map[string]*ChunkUploadSession),
		maxSessions:  maxSessions,
		maxTotalSize: maxTotalSize,
		tempDir:      tempDir,
		sessionTTL:   ttl,
	}

	go mgr.cleanupLoop()
	return mgr
}

// InitUpload 初始化分块上传会话。
func (m *ChunkUploadManager) InitUpload(filename string, totalChunks int) (*ChunkUploadSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 限制同时上传会话数
	if len(m.sessions) >= m.maxSessions {
		return nil, fmt.Errorf("too many upload sessions: max %d", m.maxSessions)
	}

	session := &ChunkUploadSession{
		ID:          generateRandomName(),
		Filename:    filename,
		DestPath:    filepath.Join(m.tempDir, generateRandomName()),
		TotalChunks: totalChunks,
		Chunks:      make(map[int]chunkPath),
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(m.sessionTTL),
	}

	m.sessions[session.ID] = session
	return session, nil
}

// UploadChunk 上传分块。
func (m *ChunkUploadManager) UploadChunk(sessionID string, chunkIndex int, data []byte) error {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("upload session not found: %s", sessionID)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	// 限制临时总大小
	m.mu.RLock()
	var totalSize int64
	for _, s := range m.sessions {
		for _, path := range s.Chunks {
			if info, err := os.Stat(path); err == nil {
				totalSize += info.Size()
			}
		}
	}
	m.mu.RUnlock()

	if totalSize+int64(len(data)) > m.maxTotalSize {
		return fmt.Errorf("total chunk size exceeds limit of %d", m.maxTotalSize)
	}

	chunkPath := filepath.Join(m.tempDir, fmt.Sprintf("%s_chunk_%d", sessionID, chunkIndex))
	if err := os.WriteFile(chunkPath, data, 0644); err != nil {
		return fmt.Errorf("write chunk: %w", err)
	}

	session.Chunks[chunkIndex] = chunkPath
	return nil
}

// MergeChunks 合并所有分块为完整文件。
func (m *ChunkUploadManager) MergeChunks(sessionID string) (string, error) {
	m.mu.RLock()
	session, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("upload session not found: %s", sessionID)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if len(session.Chunks) != session.TotalChunks {
		return "", fmt.Errorf("incomplete upload: %d/%d chunks", len(session.Chunks), session.TotalChunks)
	}

	// 合并分块
	dest, err := os.Create(session.DestPath)
	if err != nil {
		return "", fmt.Errorf("create dest file: %w", err)
	}
	defer dest.Close()

	for i := 0; i < session.TotalChunks; i++ {
		chunkPath, ok := session.Chunks[i]
		if !ok {
			return "", fmt.Errorf("missing chunk %d", i)
		}

		chunkFile, err := os.Open(chunkPath)
		if err != nil {
			return "", fmt.Errorf("open chunk %d: %w", i, err)
		}

		if _, err := io.Copy(dest, chunkFile); err != nil {
			chunkFile.Close()
			return "", fmt.Errorf("copy chunk %d: %w", i, err)
		}
		chunkFile.Close()
	}

	// 清理临时分块文件
	for _, path := range session.Chunks {
		os.Remove(path)
	}

	// 清理会话
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	return session.DestPath, nil
}

// AbortUpload 取消分块上传。
func (m *ChunkUploadManager) AbortUpload(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return
	}

	for _, path := range session.Chunks {
		os.Remove(path)
	}
	os.Remove(session.DestPath)
	delete(m.sessions, sessionID)
}

// cleanupLoop 定期清理过期会话。
func (m *ChunkUploadManager) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		now := time.Now()
		for id, session := range m.sessions {
			if now.After(session.ExpiresAt) {
				for _, path := range session.Chunks {
					os.Remove(path)
				}
				os.Remove(session.DestPath)
				delete(m.sessions, id)
			}
		}
		m.mu.Unlock()
	}
}

// ============================================================================
// 本地存储实现
// ============================================================================

// LocalStorage 本地文件存储实现。
type LocalStorage struct {
	BasePath string
}

// NewLocalStorage 创建本地存储。
func NewLocalStorage(basePath string) *LocalStorage {
	os.MkdirAll(basePath, 0755)
	return &LocalStorage{BasePath: basePath}
}

func (s *LocalStorage) fullPath(path string) string {
	return filepath.Join(s.BasePath, filepath.Clean("/"+path))
}

func (s *LocalStorage) Save(path string, reader io.Reader) error {
	fullPath := s.fullPath(path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}

	file, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, reader)
	return err
}

// SaveAll 批量保存文件（尽最大努力，不保证原子性）。
func (s *LocalStorage) SaveAll(files map[string]io.Reader) error {
	var lastErr error
	for path, reader := range files {
		if err := s.Save(path, reader); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (s *LocalStorage) Open(path string) (io.ReadCloser, error) {
	return os.Open(s.fullPath(path))
}

func (s *LocalStorage) Delete(path string) error {
	return os.Remove(s.fullPath(path))
}

func (s *LocalStorage) Exists(path string) (bool, error) {
	_, err := os.Stat(s.fullPath(path))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (s *LocalStorage) Size(path string) (int64, error) {
	info, err := os.Stat(s.fullPath(path))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *LocalStorage) List(dir string) ([]FileInfo, error) {
	entries, err := os.ReadDir(s.fullPath(dir))
	if err != nil {
		return nil, err
	}

	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, _ := entry.Info()
		f := FileInfo{
			Name:    entry.Name(),
			IsDir:   entry.IsDir(),
		}
		if info != nil {
			f.Size = info.Size()
			f.ModTime = info.ModTime()
		}
		files = append(files, f)
	}
	return files, nil
}

// SignURL 签名 URL（本地存储不支持，返回提示）。
func (s *LocalStorage) SignURL(path string, ttl time.Duration) (string, error) {
	return "", fmt.Errorf("sign URL not supported for local storage - use cloud storage extension")
}

// FileInfo 文件信息。
type FileInfo = core.FileInfo

// ============================================================================
// 工具函数
// ============================================================================

var (
	nonASCIIRegex = regexp.MustCompile(`[^\x00-\x7F]`)
)

func generateRandomName() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", md5.Sum(b))[:16]
}

func toASCII(s string) string {
	return nonASCIIRegex.ReplaceAllString(s, "_")
}

func urlEncode(s string) string {
	return strings.ReplaceAll(s, " ", "%20")
}

func randRead(b []byte) {
	// 简单随机数生成
	for i := range b {
		b[i] = byte(time.Now().UnixNano() & 0xFF)
	}
	time.Sleep(time.Nanosecond)
}

// Ensure rand is declared
type chunkPath = string

// Ensure rand variable - we use a simple package-level approach
var _ = randRead
