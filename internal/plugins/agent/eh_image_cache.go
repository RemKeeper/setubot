package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ehImageCacheDirName  = "eh_image_cache"
	ehImageDownloadMax   = 100
	ehImageDownloadRetry = 3
	ehImageDownloadJobs  = 6
	ehImageMaxBytes      = 80 << 20
)

type ehImageCacheResult struct {
	OK            bool                         `json:"ok"`
	CacheRoot     string                       `json:"cacheRoot"`
	CacheDir      string                       `json:"cacheDir"`
	GalleryID     string                       `json:"galleryId,omitempty"`
	GalleryToken  string                       `json:"galleryToken,omitempty"`
	MaxCacheBytes int64                        `json:"maxCacheBytes"`
	CacheBytes    int64                        `json:"cacheBytes"`
	CacheHit      int                          `json:"cacheHit"`
	Downloaded    int                          `json:"downloaded"`
	Failed        int                          `json:"failed"`
	Cleaned       int                          `json:"cleaned"`
	CleanedBytes  int64                        `json:"cleanedBytes"`
	CookieLoaded  bool                         `json:"cookieLoaded"`
	ProxyEnabled  bool                         `json:"proxyEnabled"`
	Images        []ehImageCacheDownloadResult `json:"images"`
	Warnings      []string                     `json:"warnings,omitempty"`
}

type ehImageCacheDownloadResult struct {
	OK          bool   `json:"ok"`
	Cached      bool   `json:"cached,omitempty"`
	URL         string `json:"url"`
	FileURL     string `json:"fileUrl,omitempty"`
	Path        string `json:"path,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (p *plugin) callEHDownloadImages(args map[string]interface{}) (string, error) {
	images := cleanImageURLs(stringSliceArg(args, "images"), ehImageDownloadMax)
	if len(images) == 0 {
		return "", fmt.Errorf("图片地址不能为空")
	}

	cacheRoot, err := filepath.Abs(filepath.Join(filepath.Dir(p.cfg.MemoryDir), ehImageCacheDirName))
	if err != nil {
		return "", err
	}
	galleryID, galleryToken := ehGalleryCacheParts(firstNonEmptyString(stringArg(args, "gallery_url"), stringArg(args, "referer")))
	cacheDir := filepath.Join(cacheRoot, galleryID, galleryToken)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}

	client := ehReqClient{plugin: p}
	httpClient, proxyEnabled, err := client.httpClient()
	if err != nil {
		return "", err
	}
	cookie := client.loadCookie()
	referer := stringArg(args, "referer")

	result := ehImageCacheResult{
		OK:            true,
		CacheRoot:     cacheRoot,
		CacheDir:      cacheDir,
		GalleryID:     galleryID,
		GalleryToken:  galleryToken,
		MaxCacheBytes: p.ehImageCacheMaxBytes(),
		CookieLoaded:  cookie != "",
		ProxyEnabled:  proxyEnabled,
		Images:        make([]ehImageCacheDownloadResult, len(images)),
	}
	if len(stringSliceArg(args, "images")) > len(images) {
		result.Warnings = append(result.Warnings, fmt.Sprintf("本次最多下载 %d 张图片，多余地址已忽略", ehImageDownloadMax))
	}

	items := p.downloadEHImageBatch(context.Background(), httpClient, client, cookie, referer, cacheDir, images)
	for i, item := range items {
		if item.OK {
			result.Downloaded++
			if item.Cached {
				result.CacheHit++
			}
		} else {
			result.Failed++
		}
		result.Images[i] = item
	}
	if result.Failed > 0 {
		result.OK = result.Downloaded > 0
		if result.Downloaded == 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("全部 %d 张图片下载失败", result.Failed))
			cleanupBatchDownloadedImages(result.Images)
		} else {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%d 张图片下载失败，%d 张已成功缓存可发送；失败的图片可尝试重新下载", result.Failed, result.Downloaded))
		}
	}

	cleaned, cleanedBytes, cacheBytes, err := cleanupImageCacheBySize(cacheRoot, p.ehImageCacheMaxBytes())
	if err != nil {
		result.Warnings = append(result.Warnings, "清理缓存失败: "+err.Error())
	} else {
		result.Cleaned = cleaned
		result.CleanedBytes = cleanedBytes
		result.CacheBytes = cacheBytes
	}

	return renderJSON(result)
}

func (p *plugin) downloadEHImageBatch(ctx context.Context, httpClient *http.Client, ehClient ehReqClient, cookie string, referer string, cacheDir string, images []string) []ehImageCacheDownloadResult {
	results := make([]ehImageCacheDownloadResult, len(images))
	jobs := make(chan int)
	workerCount := ehImageDownloadJobs
	if workerCount > len(images) {
		workerCount = len(images)
	}
	if workerCount <= 0 {
		return results
	}

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				results[index] = p.downloadEHImageWithRetry(ctx, httpClient, ehClient, cookie, referer, cacheDir, images[index])
			}
		}()
	}
	for i := range images {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	return results
}

func (p *plugin) downloadEHImageWithRetry(ctx context.Context, httpClient *http.Client, ehClient ehReqClient, cookie string, referer string, cacheDir string, imageURL string) ehImageCacheDownloadResult {
	var item ehImageCacheDownloadResult
	for attempt := 1; attempt <= ehImageDownloadRetry; attempt++ {
		item = p.downloadEHImage(ctx, httpClient, ehClient, cookie, referer, cacheDir, imageURL)
		item.Attempts = attempt
		if item.OK {
			return item
		}
		if attempt < ehImageDownloadRetry {
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
		}
	}

	return item
}

func cleanupBatchDownloadedImages(items []ehImageCacheDownloadResult) {
	for _, item := range items {
		if item.Path != "" {
			_ = os.Remove(item.Path)
		}
	}
}

func (p *plugin) downloadEHImage(ctx context.Context, httpClient *http.Client, ehClient ehReqClient, cookie string, referer string, cacheDir string, imageURL string) ehImageCacheDownloadResult {
	item := ehImageCacheDownloadResult{URL: imageURL}
	parsed, err := url.Parse(imageURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		item.Error = "图片地址无效"
		return item
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		item.Error = "只支持 http/https 图片地址"
		return item
	}

	// 缓存命中检查：如果本地已存在同名文件且大小 > 0，直接返回
	cacheName := cachedEHImageName(imageURL)
	if matches, _ := filepath.Glob(filepath.Join(cacheDir, cacheName+".*")); len(matches) > 0 {
		if info, err := os.Stat(matches[0]); err == nil && info.Size() > 0 {
			item.OK = true
			item.Cached = true
			item.Path = matches[0]
			item.FileURL = (&url.URL{Scheme: "file", Path: matches[0]}).String()
			item.Size = info.Size()
			return item
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	ehClient.applyHeaders(req)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	if cookie != "" && shouldAttachEHCookie(parsed.Hostname()) {
		req.Header.Set("Cookie", cookie)
	}
	if referer = strings.TrimSpace(referer); referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		item.Error = fmt.Sprintf("图片请求返回 %d", resp.StatusCode)
		return item
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		item.Error = "响应不是图片: " + contentType
		return item
	}

	ext := imageExtension(contentType, parsed.Path)
	path := filepath.Join(cacheDir, cachedEHImageName(imageURL)+ext)
	file, err := os.Create(path)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	size, copyErr := io.Copy(file, io.LimitReader(resp.Body, ehImageMaxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		item.Error = copyErr.Error()
		return item
	}
	if closeErr != nil {
		_ = os.Remove(path)
		item.Error = closeErr.Error()
		return item
	}
	if size > ehImageMaxBytes {
		_ = os.Remove(path)
		item.Error = "图片超过 80MB 限制"
		return item
	}

	item.OK = true
	item.Path = path
	item.FileURL = (&url.URL{Scheme: "file", Path: path}).String()
	item.ContentType = contentType
	item.Size = size
	return item
}

func cachedEHImageName(imageURL string) string {
	sum := sha256.Sum256([]byte(imageURL))
	return hex.EncodeToString(sum[:])[:32]
}

func (p *plugin) ehImageCacheMaxBytes() int64 {
	if p.cfg.EHReq.ImageCacheMaxBytes > 0 {
		return p.cfg.EHReq.ImageCacheMaxBytes
	}
	return 2 << 30
}

func ehGalleryCacheParts(rawURL string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "unknown", "unknown"
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "g" && parts[i+1] != "" && parts[i+2] != "" {
			return safeEHCachePart(parts[i+1]), safeEHCachePart(parts[i+2])
		}
	}
	return "unknown", "unknown"
}

func safeEHCachePart(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func imageExtension(contentType string, requestPath string) string {
	if contentType != "" {
		if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
			return exts[0]
		}
	}
	switch strings.ToLower(filepath.Ext(requestPath)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif":
		return strings.ToLower(filepath.Ext(requestPath))
	default:
		return ".img"
	}
}

func shouldAttachEHCookie(host string) bool {
	host = strings.ToLower(host)
	return host == "e-hentai.org" || host == "exhentai.org" || strings.HasSuffix(host, ".e-hentai.org") || strings.HasSuffix(host, ".exhentai.org")
}

func cleanupImageCacheBySize(cacheDir string, maxBytes int64) (int, int64, int64, error) {
	type cacheFile struct {
		path    string
		modTime time.Time
		size    int64
	}
	files := make([]cacheFile, 0)
	var totalBytes int64
	if err := filepath.WalkDir(cacheDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		totalBytes += info.Size()
		files = append(files, cacheFile{path: path, modTime: info.ModTime(), size: info.Size()})
		return nil
	}); err != nil {
		return 0, 0, 0, err
	}
	if maxBytes <= 0 || totalBytes <= maxBytes {
		return 0, 0, totalBytes, nil
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})
	cleaned := 0
	var cleanedBytes int64
	for _, file := range files {
		if totalBytes <= maxBytes {
			break
		}
		if err := os.Remove(file.path); err == nil {
			cleaned++
			cleanedBytes += file.size
			totalBytes -= file.size
		}
	}
	removeEmptyDirs(cacheDir)

	return cleaned, cleanedBytes, totalBytes, nil
}

func removeEmptyDirs(root string) {
	dirs := make([]string, 0)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() || path == root {
			return nil
		}
		dirs = append(dirs, path)
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
}
