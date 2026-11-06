package agent

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	zero "github.com/wdvxdr1123/ZeroBot"
)

const (
	// ehGalleryDefaultBatchSize 是每份合并转发聊天记录的最大图片数。
	ehGalleryDefaultBatchSize = 30
	ehGalleryMaxFetchImages   = 500
	ehGalleryMaxThumbPages    = 80
	// ehGalleryDefaultIntervalMs 是相邻 EH 页面请求的默认最小间隔毫秒。
	ehGalleryDefaultIntervalMs = 1100
	ehGalleryDefaultRetries    = 3
	ehGalleryMaxImageAttempts  = 3
	// ehGalleryBatchSendAttempts 是单个批次合并转发的最多尝试次数。
	ehGalleryBatchSendAttempts = 2
	// ehGalleryMaxReportedFails 限制回传的失败明细条数，避免工具结果过长。
	ehGalleryMaxReportedFails = 60
	// ehImageQuotaError 是正文图片页命中 EH 配额时的统一错误文案，用于区分「配额跳过」与「真实失败」。
	ehImageQuotaError = "触发 EH 图片配额限制"
	// ehImageQuotaPendingError 表示该页因前序配额限制而没有被拉取。
	ehImageQuotaPendingError = "EH 图片配额限制，该页未拉取"
)

var (
	ehGalleryTitlePattern      = regexp.MustCompile(`(?is)<h1[^>]*id="gn"[^>]*>(.*?)</h1>`)
	ehGalleryJapanesePattern   = regexp.MustCompile(`(?is)<h1[^>]*id="gj"[^>]*>(.*?)</h1>`)
	ehGalleryLengthPattern     = regexp.MustCompile(`(?is)Length:</td>\s*<td[^>]*>\s*([0-9,]+)\s+pages`)
	ehGalleryThumbPattern      = regexp.MustCompile(`(?is)href="((?:https?://[^"]*)?/s/([0-9a-fA-F]+)/(\d+)-(\d+))"`)
	ehGalleryThumbPagePattern  = regexp.MustCompile(`(?i)[?&]p=(\d+)`)
	ehImagePageOriginalPattern = regexp.MustCompile(`(?is)<a[^>]*href="([^"]+)"[^>]*>[^<]*Download original`)
	ehImagePageReloadPattern   = regexp.MustCompile(`(?i)[?&]nl=([a-zA-Z0-9]+)`)
	ehImagePageImgTagPattern   = regexp.MustCompile(`(?is)<img[^>]*id="img"[^>]*>`)
	ehImagePageSrcAttrPattern  = regexp.MustCompile(`(?is)src="([^"]+)"`)
)

// ehGalleryRef 描述一部作品在 EH/EX 上的定位信息。
type ehGalleryRef struct {
	Site  string
	GID   string
	Token string
	URL   string
}

type ehGalleryThumb struct {
	PageNo int
	Hash   string
	URL    string
}

type ehGallerySnapshot struct {
	GalleryURL    string
	Site          string
	GID           string
	Token         string
	Title         string
	JapaneseTitle string
	PageCount     int
	QuotaExceeded bool
	Thumbs        []ehGalleryThumb
	Warnings      []string
}

type ehGalleryImage struct {
	PageNo      int    `json:"page"`
	PageURL     string `json:"pageUrl"`
	ImageURL    string `json:"imageUrl,omitempty"`
	OriginalURL string `json:"originalImageUrl,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	Error       string `json:"error,omitempty"`
}

type ehGalleryImageFailure struct {
	Page  int    `json:"page"`
	Error string `json:"error"`
}

// ehGalleryBatchResult 记录一份合并转发聊天记录的发送结果。
type ehGalleryBatchResult struct {
	Index      int    `json:"index"`
	OK         bool   `json:"ok"`
	Count      int    `json:"count"`
	FirstPage  int    `json:"firstPage"`
	LastPage   int    `json:"lastPage"`
	MessageID  string `json:"messageId,omitempty"`
	HiddenMode string `json:"mode"`
	Attempts   int    `json:"attempts"`
	ElapsedMs  int64  `json:"elapsedMs"`
	Error      string `json:"error,omitempty"`
}

type ehGallerySendResult struct {
	OK bool `json:"ok"`
	// Complete 表示所有请求页都成功解析、下载并发送，没有跳过任何页。
	Complete        bool                    `json:"complete"`
	GalleryURL      string                  `json:"galleryUrl"`
	Site            string                  `json:"site"`
	GID             string                  `json:"gid"`
	Token           string                  `json:"token"`
	Title           string                  `json:"title,omitempty"`
	JapaneseTitle   string                  `json:"japaneseTitle,omitempty"`
	PageCount       int                     `json:"pageCount"`
	ThumbnailCount  int                     `json:"thumbnailCount"`
	RequestedPages  int                     `json:"requestedPages"`
	ResolvedImages  int                     `json:"resolvedImages"`
	Downloaded      int                     `json:"downloaded"`
	CacheHit        int                     `json:"cacheHit"`
	DownloadFailed  int                     `json:"downloadFailed"`
	SkippedPages    int                     `json:"skippedPages"`
	BatchSize       int                     `json:"batchSize"`
	BatchTotal      int                     `json:"batchTotal"`
	BatchSent       int                     `json:"batchSent"`
	SentImages      int                     `json:"sentImages"`
	WebPHidden      bool                    `json:"webpHidden"`
	RequestCount    int                     `json:"requestCount"`
	CooldownMs      int                     `json:"cooldownMs"`
	Retries         int                     `json:"retries"`
	ElapsedSeconds  float64                 `json:"elapsedSeconds"`
	QuotaExceeded   bool                    `json:"quotaExceeded"`
	CacheDir        string                  `json:"cacheDir,omitempty"`
	CookieLoaded    bool                    `json:"cookieLoaded"`
	ProxyEnabled    bool                    `json:"proxyEnabled"`
	Batches         []ehGalleryBatchResult  `json:"batches"`
	Failures        []ehGalleryImageFailure `json:"failures"`
	FailuresOmitted int                     `json:"failuresOmitted,omitempty"`
	Warnings        []string                `json:"warnings,omitempty"`
}

// ehFetchResponse 记录一次 EH 页面请求结果与尝试次数。
type ehFetchResponse struct {
	Result   ehReqResult
	Attempts int
}

// ehGalleryFetcher 在 EH/EX 页面上串行节流请求，并带有失败重试。
type ehGalleryFetcher struct {
	client   ehReqClient
	interval time.Duration
	retries  int
	mu       sync.Mutex
	next     time.Time
	requests int
}

func newEHGalleryFetcher(p *plugin, interval time.Duration, retries int) *ehGalleryFetcher {
	if retries <= 0 {
		retries = ehGalleryDefaultRetries
	}

	return &ehGalleryFetcher{
		client:   ehReqClient{plugin: p},
		interval: interval,
		retries:  retries,
	}
}

func (f *ehGalleryFetcher) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.requests
}

// wait 预留一个请求时间槽，保证相邻请求之间至少间隔 interval。
func (f *ehGalleryFetcher) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	f.mu.Lock()
	now := time.Now()
	slot := now
	if f.next.After(now) {
		slot = f.next
	}
	f.next = slot.Add(f.interval)
	delay := slot.Sub(now)
	f.requests++
	f.mu.Unlock()

	return sleepWithContext(ctx, delay)
}

// fetch 带冷却与重试地请求一个 EH 页面；4xx 与配额页不重试，5xx 与网络错误按 retries 重试。
func (f *ehGalleryFetcher) fetch(ctx context.Context, site string, path string, query url.Values) (ehFetchResponse, error) {
	var lastErr error
	attempts := 0

	for attempt := 1; attempt <= f.retries; attempt++ {
		if err := f.wait(ctx); err != nil {
			return ehFetchResponse{Attempts: attempts}, err
		}
		attempts = attempt

		res, err := f.client.do(ctx, site, http.MethodGet, path, query, nil, "")
		switch {
		case err != nil:
			lastErr = err
		case res.OK || !ehRetryableStatus(res.StatusCode):
			return ehFetchResponse{Result: res, Attempts: attempt}, nil
		default:
			lastErr = fmt.Errorf("页面返回 %d", res.StatusCode)
		}

		if attempt < f.retries {
			if err := sleepWithContext(ctx, time.Duration(attempt)*700*time.Millisecond); err != nil {
				return ehFetchResponse{Attempts: attempts}, err
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("请求失败")
	}

	return ehFetchResponse{Attempts: attempts}, lastErr
}

func ehRetryableStatus(status int) bool {
	if status == http.StatusTooManyRequests || status == 509 {
		return false
	}

	return status >= http.StatusInternalServerError
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// callEHSendGallery 传入作品链接后原子化完成整条流程：
// 拉取作品正文缩略图入口 → 解析并下载正文图片 → 按 batchSize 切片做 WebP 隐藏图合并转发。
func (p *plugin) callEHSendGallery(ctx *zero.Ctx, args map[string]interface{}) (string, error) {
	if !p.cfg.EHReq.Enabled {
		return "", fmt.Errorf("EH 请求工具未启用")
	}
	if ctx == nil {
		return "", fmt.Errorf("缺少消息上下文，无法发送图片")
	}

	ref, err := parseEHGalleryRef(stringArg(args, "gallery_url"), stringArg(args, "site"), stringArg(args, "gid"), stringArg(args, "token"))
	if err != nil {
		return "", err
	}

	batchSize := clamp(numberArg(args, "batch_size", ehGalleryDefaultBatchSize), 1, ehGalleryDefaultBatchSize)
	webpHidden := boolArg(args, "webpHidden", true)
	if webpHidden && strings.TrimSpace(p.cfg.ForwardImage.WebPSurfacePath) == "" {
		return "", fmt.Errorf("webpHidden=true 需要先配置 agent.forwardImage.webpSurfacePath；如只需发送原图请传 webpHidden=false")
	}

	intervalMs := numberArg(args, "cooldown_ms", p.ehGalleryCooldownMs())
	if intervalMs < 0 {
		intervalMs = 0
	}
	retries := numberArg(args, "retries", p.ehGalleryRetries())
	if retries <= 0 {
		retries = ehGalleryDefaultRetries
	}
	retries = clamp(retries, 1, 10)
	preferOriginal := boolArg(args, "prefer_original", false)
	startPage := numberArg(args, "start_page", 1)
	endPage := numberArg(args, "end_page", 0)
	maxImages := numberArg(args, "max_images", 0)
	concurrency := clamp(numberArg(args, "concurrency", 2), 1, 6)

	fetcher := newEHGalleryFetcher(p, time.Duration(intervalMs)*time.Millisecond, retries)
	startedAt := time.Now()
	ctxBackground := context.Background()

	result := ehGallerySendResult{
		GalleryURL: ref.URL,
		Site:       ref.Site,
		GID:        ref.GID,
		Token:      ref.Token,
		WebPHidden: webpHidden,
		BatchSize:  batchSize,
		CooldownMs: intervalMs,
		Retries:    retries,
		Batches:    make([]ehGalleryBatchResult, 0, 8),
		Failures:   make([]ehGalleryImageFailure, 0, 4),
	}

	httpClient, proxyEnabled, err := fetcher.client.httpClient()
	if err != nil {
		return "", err
	}
	cookie := fetcher.client.loadCookie()
	result.CookieLoaded = cookie != ""
	result.ProxyEnabled = proxyEnabled

	snapshot, err := fetcher.collectThumbs(ctxBackground, ref)
	if err != nil {
		return "", err
	}
	result.Title = snapshot.Title
	result.JapaneseTitle = snapshot.JapaneseTitle
	result.PageCount = snapshot.PageCount
	result.ThumbnailCount = len(snapshot.Thumbs)
	result.QuotaExceeded = snapshot.QuotaExceeded
	result.Warnings = append(result.Warnings, snapshot.Warnings...)

	thumbs := selectEHGalleryThumbs(snapshot.Thumbs, startPage, endPage, maxImages)
	result.RequestedPages = len(thumbs)
	if len(thumbs) == 0 {
		if len(snapshot.Thumbs) == 0 {
			result.Warnings = append(result.Warnings, "详情页未解析到任何缩略图入口：请确认 Cookie 有效、链接指向作品页且站点未被反爬拦截")
		}
		p.finalizeEHGalleryResult(&result, startedAt, fetcher.requestCount())

		return renderJSON(result)
	}

	images, quota := p.resolveEHGalleryImages(ctxBackground, fetcher, ref, thumbs, concurrency)
	if quota {
		result.QuotaExceeded = true
		result.Warnings = append(result.Warnings, "正文图片页触发 EH 图片配额限制，已停止拉取后续页面，可稍后用相同参数重试剩余页")
	}
	for _, image := range images {
		if strings.TrimSpace(image.ImageURL) == "" {
			pageErr := image.Error
			switch {
			case image.Attempts == 0:
				// 配额中断后剩余页面根本没有发起请求，要与真实解析失败区分开。
				if quota {
					pageErr = ehImageQuotaPendingError
				} else {
					pageErr = "未发起请求，页面已被跳过"
				}
			case pageErr == "":
				pageErr = "未解析到图片地址"
			}
			result.Failures = append(result.Failures, ehGalleryImageFailure{Page: image.PageNo, Error: pageErr})
			continue
		}
		result.ResolvedImages++
	}

	cacheRoot, err := p.ehImageCacheRoot()
	if err != nil {
		return "", err
	}
	galleryCacheID, galleryCacheToken := ehGalleryCacheParts(ref.URL)
	cacheDir := filepath.Join(cacheRoot, galleryCacheID, galleryCacheToken)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	result.CacheDir = cacheDir
	if _, _, _, err := cleanupImageCacheBySize(cacheRoot, p.ehImageCacheMaxBytes()); err != nil {
		result.Warnings = append(result.Warnings, "发送前清理图片缓存失败: "+err.Error())
	}

	tasks := make([]ehImageDownloadTask, 0, len(images))
	imageByTask := make([]ehGalleryImage, 0, len(images))
	for _, image := range images {
		if strings.TrimSpace(image.ImageURL) == "" {
			continue
		}
		downloadURL := image.ImageURL
		if preferOriginal && strings.TrimSpace(image.OriginalURL) != "" {
			downloadURL = image.OriginalURL
		}
		tasks = append(tasks, ehImageDownloadTask{URL: downloadURL, Referer: image.PageURL})
		imageByTask = append(imageByTask, image)
	}

	if len(tasks) > 0 {
		items := p.downloadEHImageTasks(ctxBackground, httpClient, fetcher.client, cookie, cacheDir, tasks)
		downloaded := make([]ehLocalGalleryImage, 0, len(items))
		for i, item := range items {
			if !item.OK {
				result.DownloadFailed++
				errText := item.Error
				if errText == "" {
					errText = "下载失败"
				}
				result.Failures = append(result.Failures, ehGalleryImageFailure{Page: imageByTask[i].PageNo, Error: errText})
				continue
			}
			result.Downloaded++
			if item.Cached {
				result.CacheHit++
			}
			downloaded = append(downloaded, ehLocalGalleryImage{
				PageNo:  imageByTask[i].PageNo,
				FileURL: item.FileURL,
			})
		}
		sort.SliceStable(downloaded, func(i, j int) bool { return downloaded[i].PageNo < downloaded[j].PageNo })

		sent, sentImages := p.sendEHGalleryBatches(ctx, &result, downloaded, batchSize, webpHidden)
		result.BatchSent = sent
		result.SentImages = sentImages
	}

	if _, _, _, err := cleanupImageCacheBySize(cacheRoot, p.ehImageCacheMaxBytes()); err != nil {
		result.Warnings = append(result.Warnings, "发送后清理图片缓存失败: "+err.Error())
	}

	p.finalizeEHGalleryResult(&result, startedAt, fetcher.requestCount())

	return renderJSON(result)
}

// finalizeEHGalleryResult 汇总统计数据、裁剪失败明细并生成告警。
func (p *plugin) finalizeEHGalleryResult(result *ehGallerySendResult, startedAt time.Time, requestCount int) {
	if result.Failures == nil {
		result.Failures = make([]ehGalleryImageFailure, 0)
	}
	sort.SliceStable(result.Failures, func(i, j int) bool { return result.Failures[i].Page < result.Failures[j].Page })

	result.SkippedPages = 0
	if result.RequestedPages > result.ResolvedImages {
		result.SkippedPages = result.RequestedPages - result.ResolvedImages
	}
	result.RequestCount = requestCount
	result.ElapsedSeconds = time.Since(startedAt).Seconds()
	result.OK = result.BatchTotal > 0 && result.BatchSent == result.BatchTotal && result.SentImages > 0
	result.Complete = result.OK && result.SkippedPages == 0 && result.DownloadFailed == 0

	if len(result.Failures) > ehGalleryMaxReportedFails {
		result.FailuresOmitted = len(result.Failures) - ehGalleryMaxReportedFails
		result.Failures = result.Failures[:ehGalleryMaxReportedFails]
		result.Warnings = append(result.Warnings, fmt.Sprintf("失败明细超过 %d 条，其余 %d 条已省略", ehGalleryMaxReportedFails, result.FailuresOmitted))
	}
	if result.RequestedPages == 0 {
		result.Warnings = append(result.Warnings, "没有可发送的图片页，请检查 start_page/end_page 范围或作品页数")
	}
	if result.BatchTotal > 0 && result.BatchSent < result.BatchTotal {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d 个批次发送失败，详见 batches", result.BatchTotal-result.BatchSent))
	}
	if result.SkippedPages > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d 页未能解析出图片地址，已跳过并在 failures 中列出页码", result.SkippedPages))
	}
	if result.DownloadFailed > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d 张图片下载失败，已跳过并在 failures 中列出页码", result.DownloadFailed))
	}
}

type ehLocalGalleryImage struct {
	PageNo  int    `json:"page"`
	FileURL string `json:"fileUrl"`
}

// sendEHGalleryBatches 把本地图片按 batchSize 切片后逐批合并转发，并记录每一批的发送结果。
func (p *plugin) sendEHGalleryBatches(ctx *zero.Ctx, result *ehGallerySendResult, images []ehLocalGalleryImage, batchSize int, webpHidden bool) (int, int) {
	mode := "plain"
	if webpHidden {
		mode = "webpHidden"
	}
	batches := make([][]ehLocalGalleryImage, 0, (len(images)+batchSize-1)/batchSize)
	for start := 0; start < len(images); start += batchSize {
		end := start + batchSize
		if end > len(images) {
			end = len(images)
		}
		batches = append(batches, images[start:end])
	}
	result.BatchTotal = len(batches)

	sentBatches := 0
	sentImages := 0
	for i, batch := range batches {
		fileURLs := make([]string, 0, len(batch))
		for _, image := range batch {
			fileURLs = append(fileURLs, image.FileURL)
		}
		batchResult := ehGalleryBatchResult{
			Index:      i + 1,
			Count:      len(fileURLs),
			FirstPage:  batch[0].PageNo,
			LastPage:   batch[len(batch)-1].PageNo,
			HiddenMode: mode,
		}

		batchStartedAt := time.Now()
		for attempt := 1; attempt <= ehGalleryBatchSendAttempts; attempt++ {
			batchResult.Attempts = attempt
			messageID, err := p.sendForwardImagesWithID(ctx, fileURLs, nil, webpHidden)
			if err == nil {
				batchResult.OK = true
				batchResult.MessageID = messageID.String()
				batchResult.Error = ""
				break
			}
			batchResult.Error = err.Error()
			if attempt < ehGalleryBatchSendAttempts {
				time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			}
		}
		batchResult.ElapsedMs = time.Since(batchStartedAt).Milliseconds()

		result.Batches = append(result.Batches, batchResult)
		if batchResult.OK {
			sentBatches++
			sentImages += len(fileURLs)
		}
	}

	return sentBatches, sentImages
}

// collectThumbs 翻页拉取作品详情页缩略图入口，并解析标题与总页数。
func (f *ehGalleryFetcher) collectThumbs(ctx context.Context, ref ehGalleryRef) (ehGallerySnapshot, error) {
	snapshot := ehGallerySnapshot{GalleryURL: ref.URL, Site: ref.Site, GID: ref.GID, Token: ref.Token}
	path := fmt.Sprintf("/g/%s/%s/", ref.GID, url.PathEscape(ref.Token))
	seen := make(map[int]struct{})
	maxThumbPage := 0

	for thumbPage := 0; thumbPage <= ehGalleryMaxThumbPages; thumbPage++ {
		if thumbPage > 0 && maxThumbPage > 0 && thumbPage > maxThumbPage {
			break
		}
		query := url.Values{}
		if thumbPage > 0 {
			query.Set("p", strconv.Itoa(thumbPage))
		}
		response, err := f.fetch(ctx, ref.Site, path, query)
		if err != nil {
			if thumbPage == 0 {
				return snapshot, fmt.Errorf("请求作品详情页失败：%w", err)
			}
			snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("缩略图第 %d 页请求失败，已停止翻页：%v", thumbPage+1, err))
			break
		}

		res := response.Result
		if ehIsQuotaResult(res) {
			snapshot.QuotaExceeded = true
			snapshot.Warnings = append(snapshot.Warnings, "作品详情页触发 EH 配额限制，已停止翻页")
			break
		}
		if res.StatusCode == http.StatusNotFound {
			return snapshot, fmt.Errorf("作品链接不存在或已删除（HTTP 404）")
		}
		if !res.OK {
			if thumbPage == 0 {
				return snapshot, fmt.Errorf("请求作品详情页失败（HTTP %d），请检查 Cookie 与站点配置", res.StatusCode)
			}
			snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("缩略图第 %d 页返回 HTTP %d，已停止翻页", thumbPage+1, res.StatusCode))
			break
		}

		body := res.Body
		if thumbPage == 0 {
			snapshot.Title = stripHTMLText(firstSubmatch(ehGalleryTitlePattern, body))
			snapshot.JapaneseTitle = stripHTMLText(firstSubmatch(ehGalleryJapanesePattern, body))
			snapshot.PageCount = parseEHCount(firstSubmatch(ehGalleryLengthPattern, body))
		}
		if page := maxEHThumbPage(body); page > maxThumbPage {
			maxThumbPage = page
		}

		added := 0
		for _, thumb := range parseEHGalleryThumbs(body, ref.Site) {
			if _, ok := seen[thumb.PageNo]; ok {
				continue
			}
			seen[thumb.PageNo] = struct{}{}
			snapshot.Thumbs = append(snapshot.Thumbs, thumb)
			added++
		}
		if thumbPage > 0 && added == 0 {
			break
		}
		if snapshot.PageCount > 0 && len(snapshot.Thumbs) >= snapshot.PageCount {
			break
		}
		if maxThumbPage == 0 && added == 0 && thumbPage == 0 {
			break
		}
	}

	sort.SliceStable(snapshot.Thumbs, func(i, j int) bool { return snapshot.Thumbs[i].PageNo < snapshot.Thumbs[j].PageNo })

	return snapshot, nil
}

// resolveEHGalleryImages 并发解析正文图片页，带冷却与重试，配额耗尽时提前中止。
func (p *plugin) resolveEHGalleryImages(ctx context.Context, fetcher *ehGalleryFetcher, ref ehGalleryRef, thumbs []ehGalleryThumb, concurrency int) ([]ehGalleryImage, bool) {
	results := make([]ehGalleryImage, len(thumbs))
	var (
		mu      sync.Mutex
		next    int
		quota   bool
		wg      sync.WaitGroup
		workers = concurrency
	)
	if workers > len(thumbs) {
		workers = len(thumbs)
	}
	if workers <= 0 {
		return results, false
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if quota || next >= len(thumbs) {
					mu.Unlock()
					return
				}
				index := next
				next++
				mu.Unlock()

				image, hitQuota := fetcher.fetchImage(ctx, ref, thumbs[index])
				if hitQuota {
					mu.Lock()
					quota = true
					mu.Unlock()
				}
				results[index] = image
			}
		}()
	}
	wg.Wait()

	return results, quota
}

// fetchImage 解析单个正文图片页，解析失败时用 reload key 重试。
func (f *ehGalleryFetcher) fetchImage(ctx context.Context, ref ehGalleryRef, thumb ehGalleryThumb) (ehGalleryImage, bool) {
	image := ehGalleryImage{PageNo: thumb.PageNo, PageURL: thumb.URL}
	path := fmt.Sprintf("/s/%s/%s-%d", thumb.Hash, ref.GID, thumb.PageNo)
	query := url.Values{}

	for attempt := 1; attempt <= ehGalleryMaxImageAttempts; attempt++ {
		if attempt > 1 && f.interval > 0 {
			if err := sleepWithContext(ctx, f.interval); err != nil {
				image.Error = err.Error()

				return image, false
			}
		}

		response, err := f.fetch(ctx, ref.Site, path, query)
		image.Attempts = attempt
		if err != nil {
			image.Error = err.Error()
			continue
		}

		res := response.Result
		if ehIsQuotaResult(res) {
			image.Error = ehImageQuotaError

			return image, true
		}
		if !res.OK {
			image.Error = fmt.Sprintf("图片页返回 HTTP %d", res.StatusCode)
			continue
		}

		parsed := parseEHImagePage(res.Body)
		if parsed.ImageURL == "" {
			image.Error = "未解析到正文图片地址"
			// 图片页提供 nl reload key 时带上它重试，可绕过临时失效的图片地址。
			if reloadKey := firstSubmatch(ehImagePageReloadPattern, res.Body); reloadKey != "" && !query.Has("nl") {
				query.Set("nl", reloadKey)
			}
			continue
		}

		image.ImageURL = parsed.ImageURL
		image.OriginalURL = parsed.OriginalURL
		image.Error = ""

		return image, false
	}

	return image, false
}

type ehImagePageInfo struct {
	ImageURL    string
	OriginalURL string
}

// parseEHImagePage 优先从 id="img" 的主图标签取 src，避免命中页面中的其它小图。
func parseEHImagePage(body string) ehImagePageInfo {
	info := ehImagePageInfo{}

	src := ""
	if tag := ehImagePageImgTagPattern.FindString(body); tag != "" {
		src = firstSubmatch(ehImagePageSrcAttrPattern, tag)
	}
	if strings.TrimSpace(src) == "" {
		src = firstSubmatch(ehImagePageSrcAttrPattern, body)
	}
	if src = strings.TrimSpace(src); src != "" {
		info.ImageURL = html.UnescapeString(src)
	}
	if original := strings.TrimSpace(firstSubmatch(ehImagePageOriginalPattern, body)); original != "" {
		info.OriginalURL = html.UnescapeString(original)
	}

	return info
}

func parseEHGalleryThumbs(body string, site string) []ehGalleryThumb {
	baseURL, _, err := ehSiteBaseURL(site)
	if err != nil {
		baseURL = "https://exhentai.org"
	}

	matches := ehGalleryThumbPattern.FindAllStringSubmatch(body, -1)
	thumbs := make([]ehGalleryThumb, 0, len(matches))
	for _, match := range matches {
		pageNo, err := strconv.Atoi(match[4])
		if err != nil || pageNo <= 0 {
			continue
		}
		href := html.UnescapeString(match[1])
		if strings.HasPrefix(href, "/") {
			href = baseURL + href
		}
		thumbs = append(thumbs, ehGalleryThumb{PageNo: pageNo, Hash: match[2], URL: href})
	}

	return thumbs
}

func maxEHThumbPage(body string) int {
	maxPage := 0
	for _, match := range ehGalleryThumbPagePattern.FindAllStringSubmatch(body, -1) {
		page, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if page > maxPage {
			maxPage = page
		}
	}

	return maxPage
}

func selectEHGalleryThumbs(thumbs []ehGalleryThumb, startPage int, endPage int, maxImages int) []ehGalleryThumb {
	if startPage < 1 {
		startPage = 1
	}

	selected := make([]ehGalleryThumb, 0, len(thumbs))
	for _, thumb := range thumbs {
		if thumb.PageNo < startPage {
			continue
		}
		if endPage > 0 && thumb.PageNo > endPage {
			continue
		}
		selected = append(selected, thumb)
		if maxImages > 0 && len(selected) >= maxImages {
			break
		}
		if len(selected) >= ehGalleryMaxFetchImages {
			break
		}
	}

	return selected
}

// parseEHGalleryRef 从作品链接或 gid/token 参数解析出统一的定位信息。
func parseEHGalleryRef(rawURL string, siteArg string, gidArg string, tokenArg string) (ehGalleryRef, error) {
	rawURL = strings.TrimSpace(rawURL)
	gid := strings.TrimSpace(gidArg)
	token := strings.TrimSpace(tokenArg)
	site := ""

	if rawURL != "" {
		candidate := rawURL
		if !strings.Contains(candidate, "://") {
			candidate = "https://" + candidate
		}
		parsed, err := url.Parse(candidate)
		if err != nil {
			return ehGalleryRef{}, fmt.Errorf("作品链接无法解析：%w", err)
		}
		host := strings.ToLower(parsed.Hostname())
		switch {
		case strings.Contains(host, "exhentai.org"):
			site = "EX"
		case strings.Contains(host, "e-hentai.org"):
			site = "EH"
		case host != "":
			return ehGalleryRef{}, fmt.Errorf("作品链接不是 EH/EX 域名：%s", host)
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		for i := 0; i+2 < len(parts); i++ {
			if parts[i] == "g" {
				gid = parts[i+1]
				token = parts[i+2]
				break
			}
		}
	}

	if gid == "" || token == "" {
		return ehGalleryRef{}, fmt.Errorf("无法从作品链接中解析出 gid/token，请传入形如 https://exhentai.org/g/{gid}/{token}/ 的链接")
	}
	if _, err := strconv.Atoi(gid); err != nil {
		return ehGalleryRef{}, fmt.Errorf("作品 gid 无效：%s", gid)
	}
	for _, r := range token {
		if r < '0' || r > '9' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return ehGalleryRef{}, fmt.Errorf("作品 token 无效：%s", token)
		}
	}

	baseURL, normalizedSite, err := ehSiteBaseURL(firstNonEmptyString(site, siteArg))
	if err != nil {
		return ehGalleryRef{}, err
	}

	return ehGalleryRef{
		Site:  normalizedSite,
		GID:   gid,
		Token: token,
		URL:   fmt.Sprintf("%s/g/%s/%s/", baseURL, gid, token),
	}, nil
}

func (p *plugin) ehGalleryCooldownMs() int {
	if p.cfg.EHReq.GalleryCooldownMs > 0 {
		return p.cfg.EHReq.GalleryCooldownMs
	}

	return ehGalleryDefaultIntervalMs
}

func (p *plugin) ehGalleryRetries() int {
	if p.cfg.EHReq.GalleryRetries > 0 {
		return p.cfg.EHReq.GalleryRetries
	}

	return ehGalleryDefaultRetries
}

func ehIsQuotaResult(res ehReqResult) bool {
	if res.StatusCode == 509 {
		return true
	}

	body := strings.ToLower(res.Body)
	for _, marker := range []string{
		"exceeded the image viewing limit",
		"temporarily banned",
		"image viewing quota",
		"509 bandwidth limit",
	} {
		if strings.Contains(body, marker) {
			return true
		}
	}

	return false
}

func firstSubmatch(pattern *regexp.Regexp, body string) string {
	match := pattern.FindStringSubmatch(body)
	if len(match) < 2 {
		return ""
	}

	return strings.TrimSpace(match[1])
}

func parseEHCount(raw string) int {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), ",", "")
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}

	return value
}
