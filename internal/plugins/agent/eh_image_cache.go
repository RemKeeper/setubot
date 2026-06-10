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
	ehImageCacheMaxFiles = 100
	ehImageDownloadMax   = 100
	ehImageDownloadRetry = 3
	ehImageDownloadJobs  = 6
	ehImageMaxBytes      = 80 << 20
)

type ehImageCacheResult struct {
	OK           bool                         `json:"ok"`
	CacheDir     string                       `json:"cacheDir"`
	MaxCache     int                          `json:"maxCache"`
	Downloaded   int                          `json:"downloaded"`
	Failed       int                          `json:"failed"`
	Cleaned      int                          `json:"cleaned"`
	CookieLoaded bool                         `json:"cookieLoaded"`
	ProxyEnabled bool                         `json:"proxyEnabled"`
	Images       []ehImageCacheDownloadResult `json:"images"`
	Warnings     []string                     `json:"warnings,omitempty"`
}

type ehImageCacheDownloadResult struct {
	OK          bool   `json:"ok"`
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

	cacheDir, err := filepath.Abs(filepath.Join(filepath.Dir(p.cfg.MemoryDir), ehImageCacheDirName))
	if err != nil {
		return "", err
	}
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
		OK:           true,
		CacheDir:     cacheDir,
		MaxCache:     ehImageCacheMaxFiles,
		CookieLoaded: cookie != "",
		ProxyEnabled: proxyEnabled,
		Images:       make([]ehImageCacheDownloadResult, len(images)),
	}
	if len(stringSliceArg(args, "images")) > len(images) {
		result.Warnings = append(result.Warnings, fmt.Sprintf("本次最多下载 %d 张图片，多余地址已忽略", ehImageDownloadMax))
	}

	items := p.downloadEHImageBatch(context.Background(), httpClient, client, cookie, referer, cacheDir, images)
	for i, item := range items {
		if item.OK {
			result.Downloaded++
		} else {
			result.Failed++
			result.OK = false
		}
		result.Images[i] = item
	}
	if !result.OK {
		result.Warnings = append(result.Warnings, "批次下载未全部成功，已中止返回可发送 fileUrl，避免漫画章节不连贯")
		cleanupBatchDownloadedImages(result.Images)
		for i := range result.Images {
			result.Images[i].FileURL = ""
			result.Images[i].Path = ""
		}
	}

	cleaned, err := cleanupEHImageCache(cacheDir, ehImageCacheMaxFiles)
	if err != nil {
		result.Warnings = append(result.Warnings, "清理缓存失败: "+err.Error())
	} else {
		result.Cleaned = cleaned
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

func cleanupEHImageCache(cacheDir string, maxFiles int) (int, error) {
	if maxFiles <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return 0, err
	}
	type cacheFile struct {
		path    string
		modTime time.Time
	}
	files := make([]cacheFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, cacheFile{path: filepath.Join(cacheDir, entry.Name()), modTime: info.ModTime()})
	}
	if len(files) <= maxFiles {
		return 0, nil
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})
	removeCount := len(files) - maxFiles
	cleaned := 0
	for _, file := range files[:removeCount] {
		if err := os.Remove(file.path); err == nil {
			cleaned++
		}
	}

	return cleaned, nil
}
