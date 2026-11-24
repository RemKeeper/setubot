package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
	zero "github.com/wdvxdr1123/ZeroBot"
)

const (
	r2EmptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	r2DeletedLinkMarker  = "[已自动从R2删除的临时搜图直链]"
)

var reverseImageSearchHints = []string{
	"出处", "搜图", "识图", "以图搜图", "找图", "查图", "反查",
	"原图", "高清", "大图", "高分辨率", "原帖", "图源", "源头", "源地址", "传送门",
	"哪来的", "哪里来的", "哪儿来的", "从哪来", "哪个网站",
	"谁画", "画师", "作者", "绘师", "推主", "coser",
	"哪部番", "什么番", "啥番", "哪集", "第几集",
	"哪部漫画", "什么漫画", "哪部动漫", "什么动漫", "哪部作品", "什么作品",
	"sauce", "saucenao", "ascii2d", "iqdb", "tineye", "trace.moe", "google lens", "yandex",
}

// r2Client 是面向 Cloudflare R2 S3 兼容接口的轻量客户端（AWS Signature V4）。
type r2Client struct {
	endpointURL     *url.URL
	accessKeyID     string
	secretAccessKey string
	bucket          string
	region          string
	publicBaseURL   string
	keyPrefix       string
	presignTTL      int
	httpClient      *http.Client
	nowFunc         func() time.Time
}

type r2CachedRawImage struct {
	Data        []byte
	ContentType string
}

type r2StagedImage struct {
	Index       int
	RawSource   string
	ObjectKey   string
	DirectURL   string
	ContentType string
	Size        int
}

// r2TurnSession 绑定在单轮 runAgent 的 *zero.Ctx 上，管理该轮下载缓存、已上传的临时 R2 对象与自动清理。
type r2TurnSession struct {
	mu          sync.Mutex
	rawCache    map[string]r2CachedRawImage
	bySource    map[string]r2StagedImage
	ordered     []r2StagedImage
	uploadedKey []string
	uploadDone  bool
	uploadErr   error
}

func newR2TurnSession() *r2TurnSession {
	return &r2TurnSession{
		rawCache: make(map[string]r2CachedRawImage),
		bySource: make(map[string]r2StagedImage),
	}
}

func (p *plugin) r2Enabled() bool {
	return p != nil && p.cfg.R2.Enabled
}

func isReverseImageSearchPrompt(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	for _, hint := range reverseImageSearchHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func (p *plugin) shouldPreUploadR2Images(prompt string) bool {
	if !p.r2Enabled() {
		return false
	}
	if p.cfg.R2.UploadAllImages || !p.visionToolEnabled() {
		return true
	}
	return isReverseImageSearchPrompt(prompt)
}

func isReverseImageSearchSkill(skillName string) bool {
	base := strings.ToLower(strings.TrimSpace(filepath.Base(skillName)))
	return base == "reverse_image_search_skill.md" || strings.Contains(base, "reverse_image_search")
}

func newR2Client(cfg config.R2Config, baseHTTP *http.Client) (*r2Client, error) {
	accountIDEnv := firstNonEmpty(cfg.AccountIDEnv, "R2_ACCOUNT_ID")
	endpointEnv := firstNonEmpty(cfg.EndpointEnv, "R2_ENDPOINT")
	accessKeyEnv := firstNonEmpty(cfg.AccessKeyIDEnv, "R2_ACCESS_KEY_ID")
	secretKeyEnv := firstNonEmpty(cfg.SecretAccessKeyEnv, "R2_SECRET_ACCESS_KEY")
	bucketEnv := firstNonEmpty(cfg.BucketEnv, "R2_BUCKET")
	publicBaseEnv := firstNonEmpty(cfg.PublicBaseURLEnv, "R2_PUBLIC_BASE_URL")

	accountID := firstNonEmpty(cfg.AccountID, os.Getenv(accountIDEnv))
	endpoint := firstNonEmpty(cfg.Endpoint, os.Getenv(endpointEnv))
	if endpoint == "" && accountID != "" {
		endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	}
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("未配置 R2 accountId 或 endpoint（可通过 agent.r2.accountId 或环境变量 %s 设置）", accountIDEnv)
	}
	endpointURL, err := url.Parse(endpoint)
	if err != nil || (endpointURL.Scheme != "http" && endpointURL.Scheme != "https") || endpointURL.Host == "" {
		return nil, fmt.Errorf("无效的 R2 endpoint 配置")
	}

	accessKeyID := firstNonEmpty(cfg.AccessKeyID, os.Getenv(accessKeyEnv))
	secretAccessKey := firstNonEmpty(cfg.SecretAccessKey, os.Getenv(secretKeyEnv))
	bucket := firstNonEmpty(cfg.Bucket, os.Getenv(bucketEnv))
	if accessKeyID == "" || secretAccessKey == "" {
		return nil, fmt.Errorf("未配置 R2 S3 访问凭证（可通过 agent.r2.accessKeyId/secretAccessKey 或环境变量 %s/%s 设置）", accessKeyEnv, secretKeyEnv)
	}
	if bucket == "" {
		return nil, fmt.Errorf("未配置 R2 bucket（可通过 agent.r2.bucket 或环境变量 %s 设置）", bucketEnv)
	}

	publicBaseURL := strings.TrimRight(firstNonEmpty(cfg.PublicBaseURL, os.Getenv(publicBaseEnv)), "/")
	if publicBaseURL != "" {
		parsedPublic, err := url.Parse(publicBaseURL)
		if err != nil || (parsedPublic.Scheme != "http" && parsedPublic.Scheme != "https") || parsedPublic.Host == "" {
			return nil, fmt.Errorf("无效的 R2 publicBaseURL 配置")
		}
	}

	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}
	keyPrefix := strings.Trim(strings.TrimSpace(cfg.KeyPrefix), "/")
	if keyPrefix == "" {
		keyPrefix = "setubot-temp"
	}
	presignTTL := cfg.PresignTTLSeconds
	if presignTTL <= 0 {
		presignTTL = 900
	}
	if presignTTL < 60 {
		presignTTL = 60
	}
	if presignTTL > 604800 {
		presignTTL = 604800
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30
	}

	httpClient := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	if baseHTTP != nil && baseHTTP.Transport != nil {
		httpClient.Transport = baseHTTP.Transport
	}

	return &r2Client{
		endpointURL:     endpointURL,
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
		bucket:          bucket,
		region:          region,
		publicBaseURL:   publicBaseURL,
		keyPrefix:       keyPrefix,
		presignTTL:      presignTTL,
		httpClient:      httpClient,
		nowFunc:         time.Now,
	}, nil
}

func (c *r2Client) now() time.Time {
	if c != nil && c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

func (c *r2Client) newObjectKey(contentType string) (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("生成随机对象名失败: %w", err)
	}
	ext := r2ImageExtension(contentType)
	dateDir := c.now().UTC().Format("20060102")
	return fmt.Sprintf("%s/%s/%s%s", c.keyPrefix, dateDir, hex.EncodeToString(randomBytes[:]), ext), nil
}

func r2ImageExtension(contentType string) string {
	switch normalizeVisionImageMIME(contentType) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/avif":
		return ".avif"
	default:
		return ".img"
	}
}

func (c *r2Client) canonicalObjectPath(key string) string {
	key = strings.TrimLeft(key, "/")
	basePath := strings.TrimRight(c.endpointURL.Path, "/")
	rawPath := basePath + "/" + c.bucket + "/" + key
	return awsURIEncode(rawPath, false)
}

func (c *r2Client) objectEndpointURL(key string) string {
	return c.endpointURL.Scheme + "://" + c.endpointURL.Host + c.canonicalObjectPath(key)
}

func (c *r2Client) directURLForKey(key string) string {
	key = strings.TrimLeft(key, "/")
	if c.publicBaseURL != "" {
		return c.publicBaseURL + "/" + awsURIEncode(key, false)
	}
	return c.PresignGetObject(key, c.now(), time.Duration(c.presignTTL)*time.Second)
}

func (c *r2Client) PutObject(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	objectURL := c.objectEndpointURL(key)
	payloadHash := sha256Hex(data)
	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt*250) * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		req.ContentLength = int64(len(data))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Cache-Control", "public, max-age=600")
		c.signRequest(req, payloadHash, c.now())

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("请求 R2 失败: %w", err)
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return c.directURLForKey(key), nil
		}
		lastErr = fmt.Errorf("R2 PutObject 返回 HTTP %d", resp.StatusCode)
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < http.StatusInternalServerError {
			break
		}
	}

	return "", lastErr
}

func (c *r2Client) DeleteObject(ctx context.Context, key string) error {
	objectURL := c.objectEndpointURL(key)
	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*250) * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, objectURL, nil)
		if err != nil {
			return err
		}
		c.signRequest(req, r2EmptyPayloadSHA256, c.now())

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("请求 R2 删除失败: %w", err)
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if (resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices) || resp.StatusCode == http.StatusNotFound {
			return nil
		}
		lastErr = fmt.Errorf("R2 DeleteObject 返回 HTTP %d", resp.StatusCode)
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < http.StatusInternalServerError {
			break
		}
	}

	return lastErr
}

// PresignGetObject 生成 S3 SigV4 预签名 GET 直链，在未配置公开域名时作为私密临时直链回退。
func (c *r2Client) PresignGetObject(key string, now time.Time, expires time.Duration) string {
	utcNow := now.UTC()
	amzDate := utcNow.Format("20060102T150405Z")
	dateStamp := utcNow.Format("20060102")
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.region)
	canonicalURI := c.canonicalObjectPath(key)
	host := strings.ToLower(strings.TrimSpace(c.endpointURL.Host))

	expSec := int(expires.Seconds())
	if expSec < 1 {
		expSec = 900
	}
	if expSec > 604800 {
		expSec = 604800
	}

	queryParams := [][2]string{
		{"X-Amz-Algorithm", "AWS4-HMAC-SHA256"},
		{"X-Amz-Credential", c.accessKeyID + "/" + scope},
		{"X-Amz-Date", amzDate},
		{"X-Amz-Expires", strconv.Itoa(expSec)},
		{"X-Amz-SignedHeaders", "host"},
	}
	canonicalQuery := buildCanonicalQuery(queryParams)
	canonicalHeaders := "host:" + host + "\n"
	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hmacHex(c.signingKey(dateStamp), stringToSign)

	return fmt.Sprintf("%s://%s%s?%s&X-Amz-Signature=%s",
		c.endpointURL.Scheme,
		c.endpointURL.Host,
		canonicalURI,
		canonicalQuery,
		signature,
	)
}

func (c *r2Client) signRequest(req *http.Request, payloadHash string, now time.Time) {
	utcNow := now.UTC()
	amzDate := utcNow.Format("20060102T150405Z")
	dateStamp := utcNow.Format("20060102")
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.region)

	req.Host = req.URL.Host
	host := strings.ToLower(strings.TrimSpace(req.URL.Host))
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalizeURLQuery(req.URL.Query())
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", host, payloadHash, amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hmacHex(c.signingKey(dateStamp), stringToSign)
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.accessKeyID, scope, signedHeaders, signature,
	))
}

func (c *r2Client) signingKey(dateStamp string) []byte {
	kDate := hmacBytes([]byte("AWS4"+c.secretAccessKey), dateStamp)
	kRegion := hmacBytes(kDate, c.region)
	kService := hmacBytes(kRegion, "s3")
	return hmacBytes(kService, "aws4_request")
}

func hmacBytes(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

func hmacHex(key []byte, data string) string {
	return hex.EncodeToString(hmacBytes(key, data))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func buildCanonicalQuery(pairs [][2]string) string {
	encoded := make([][2]string, 0, len(pairs))
	for _, kv := range pairs {
		encoded = append(encoded, [2]string{
			awsURIEncode(kv[0], true),
			awsURIEncode(kv[1], true),
		})
	}
	sort.Slice(encoded, func(i, j int) bool {
		if encoded[i][0] == encoded[j][0] {
			return encoded[i][1] < encoded[j][1]
		}
		return encoded[i][0] < encoded[j][0]
	})
	parts := make([]string, 0, len(encoded))
	for _, kv := range encoded {
		parts = append(parts, kv[0]+"="+kv[1])
	}
	return strings.Join(parts, "&")
}

func canonicalizeURLQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	pairs := make([][2]string, 0, len(values))
	for k, vs := range values {
		if len(vs) == 0 {
			pairs = append(pairs, [2]string{k, ""})
			continue
		}
		for _, v := range vs {
			pairs = append(pairs, [2]string{k, v})
		}
	}
	return buildCanonicalQuery(pairs)
}

// awsURIEncode 按 AWS SigV4 RFC 3986 规则编码字符串（空格编码为 %20，十六进制使用大写）。
func awsURIEncode(input string, encodeSlash bool) string {
	const upperHex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(input))
	for i := 0; i < len(input); i++ {
		ch := input[i]
		isUnreserved := (ch >= 'A' && ch <= 'Z') ||
			(ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' || ch == '.' || ch == '~'
		if isUnreserved || (ch == '/' && !encodeSlash) {
			b.WriteByte(ch)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHex[ch>>4])
		b.WriteByte(upperHex[ch&0x0F])
	}
	return b.String()
}

func (p *plugin) startR2Turn(ctx *zero.Ctx) *r2TurnSession {
	if ctx == nil || !p.r2Enabled() {
		return nil
	}
	p.r2TurnsM.Lock()
	defer p.r2TurnsM.Unlock()
	if p.r2Turns == nil {
		p.r2Turns = make(map[*zero.Ctx]*r2TurnSession)
	}
	if existing, ok := p.r2Turns[ctx]; ok {
		return existing
	}
	session := newR2TurnSession()
	p.r2Turns[ctx] = session
	return session
}

func (p *plugin) activeR2Turn(ctx *zero.Ctx) *r2TurnSession {
	if ctx == nil || !p.r2Enabled() {
		return nil
	}
	p.r2TurnsM.Lock()
	defer p.r2TurnsM.Unlock()
	if p.r2Turns == nil {
		return nil
	}
	return p.r2Turns[ctx]
}

func (p *plugin) cacheR2TurnRawImage(ctx *zero.Ctx, source string, data []byte, contentType string) {
	session := p.activeR2Turn(ctx)
	if session == nil || source == "" || len(data) == 0 {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.rawCache[source] = r2CachedRawImage{
		Data:        data,
		ContentType: contentType,
	}
}

func (p *plugin) lookupR2TurnRawImage(ctx *zero.Ctx, source string) ([]byte, string, bool) {
	session := p.activeR2Turn(ctx)
	if session == nil || source == "" {
		return nil, "", false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	cached, ok := session.rawCache[source]
	if !ok || len(cached.Data) == 0 {
		return nil, "", false
	}
	return cached.Data, cached.ContentType, true
}

// ensureR2TurnUploaded 将本轮请求携带的图片上传到 Cloudflare R2（幂等，单轮最多上传一次）。
func (p *plugin) ensureR2TurnUploaded(ctx *zero.Ctx, turn *traceTurn) ([]r2StagedImage, error) {
	if !p.r2Enabled() {
		return nil, fmt.Errorf("未启用 Cloudflare R2 临时图床")
	}
	if ctx == nil {
		return nil, fmt.Errorf("缺少会话上下文")
	}
	session := p.startR2Turn(ctx)

	session.mu.Lock()
	if session.uploadDone {
		staged := append([]r2StagedImage(nil), session.ordered...)
		err := session.uploadErr
		session.mu.Unlock()
		return staged, err
	}
	session.mu.Unlock()

	sources := p.requestImageSources(ctx)
	if len(sources) == 0 {
		return nil, fmt.Errorf("当前消息及引用消息中没有可用图片")
	}

	client, err := newR2Client(p.cfg.R2, p.httpClient)
	if err != nil {
		session.mu.Lock()
		session.uploadDone = true
		session.uploadErr = err
		session.mu.Unlock()
		return nil, err
	}

	timeout := p.cfg.R2.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	uploadCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout*len(sources))*time.Second)
	defer cancel()

	stagedList := make([]r2StagedImage, 0, len(sources))
	for i, source := range sources {
		data, contentType, err := p.fetchVisionImageBytes(ctx, source)
		if err != nil {
			err = fmt.Errorf("第 %d 张图片读取失败: %w", i+1, err)
			session.mu.Lock()
			session.uploadDone = true
			session.uploadErr = err
			session.mu.Unlock()
			return nil, err
		}

		key, err := client.newObjectKey(contentType)
		if err != nil {
			session.mu.Lock()
			session.uploadDone = true
			session.uploadErr = err
			session.mu.Unlock()
			return nil, err
		}

		directURL, err := client.PutObject(uploadCtx, key, data, contentType)
		if err != nil {
			err = fmt.Errorf("第 %d 张图片上传 R2 失败: %w", i+1, err)
			session.mu.Lock()
			session.uploadDone = true
			session.uploadErr = err
			session.mu.Unlock()
			return nil, err
		}

		item := r2StagedImage{
			Index:       i + 1,
			RawSource:   source,
			ObjectKey:   key,
			DirectURL:   directURL,
			ContentType: contentType,
			Size:        len(data),
		}
		// 每成功上传一张就立即登记 ObjectKey，即使后续图片失败也能由 defer 完整删除。
		session.mu.Lock()
		session.uploadedKey = append(session.uploadedKey, key)
		session.bySource[source] = item
		session.ordered = append(session.ordered, item)
		session.mu.Unlock()

		stagedList = append(stagedList, item)
		log.Printf("[agent/r2] 已上传临时搜图图片[%d] key=%s content_type=%s bytes=%d", i+1, key, contentType, len(data))
	}

	session.mu.Lock()
	session.uploadDone = true
	session.uploadErr = nil
	session.mu.Unlock()

	if turn != nil {
		summary := make([]string, 0, len(stagedList))
		for _, item := range stagedList {
			summary = append(summary, fmt.Sprintf("图片%d -> %s (%d bytes)", item.Index, item.ObjectKey, item.Size))
		}
		p.traceNote(turn, "已上传临时搜图图片至 R2", strings.Join(summary, "\n"))
	}

	return stagedList, nil
}

// cleanupR2Turn 在单轮对话结束时自动从 Cloudflare R2 删除本轮上传的全部临时图片。
func (p *plugin) cleanupR2Turn(ctx *zero.Ctx, turn *traceTurn) {
	if ctx == nil {
		return
	}
	p.r2TurnsM.Lock()
	var session *r2TurnSession
	if p.r2Turns != nil {
		session = p.r2Turns[ctx]
		delete(p.r2Turns, ctx)
	}
	p.r2TurnsM.Unlock()

	if session == nil {
		return
	}
	session.mu.Lock()
	keys := append([]string(nil), session.uploadedKey...)
	session.mu.Unlock()
	if len(keys) == 0 {
		return
	}

	client, err := newR2Client(p.cfg.R2, p.httpClient)
	if err != nil {
		log.Printf("[agent/r2] 初始化 R2 客户端清理临时图片失败: %v", err)
		return
	}

	timeout := p.cfg.R2.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	delCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout*len(keys))*time.Second)
	defer cancel()

	deleted := make([]string, 0, len(keys))
	failed := make([]string, 0)
	for _, key := range keys {
		if err := client.DeleteObject(delCtx, key); err != nil {
			log.Printf("[agent/r2] 删除临时图片失败 key=%s err=%v", key, err)
			failed = append(failed, fmt.Sprintf("%s (%v)", key, err))
			continue
		}
		log.Printf("[agent/r2] 已从 R2 删除临时图片 key=%s", key)
		deleted = append(deleted, key)
	}

	if turn != nil {
		note := "已删除：\n" + strings.Join(deleted, "\n")
		if len(failed) > 0 {
			note += "\n删除失败：\n" + strings.Join(failed, "\n")
		}
		p.traceNote(turn, "已清理 R2 临时搜图图片", strings.TrimSpace(note))
	}
}

func (p *plugin) r2StagedDirectURL(ctx *zero.Ctx, rawSource string) string {
	session := p.activeR2Turn(ctx)
	if session == nil || rawSource == "" {
		return ""
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.bySource[rawSource].DirectURL
}

func (p *plugin) r2StagedImages(ctx *zero.Ctx) []r2StagedImage {
	session := p.activeR2Turn(ctx)
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return append([]r2StagedImage(nil), session.ordered...)
}

func (p *plugin) r2TurnUploadError(ctx *zero.Ctx) error {
	session := p.activeR2Turn(ctx)
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.uploadErr
}

func formatR2StagedLinks(items []r2StagedImage) string {
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("- 图片%d：%s", item.Index, item.DirectURL))
	}
	return strings.Join(lines, "\n")
}

func (p *plugin) callPrepareSearchImages(ctx *zero.Ctx) (string, error) {
	if !p.r2Enabled() {
		return "", fmt.Errorf("Cloudflare R2 临时图床未启用")
	}
	items, err := p.ensureR2TurnUploaded(ctx, p.activeTraceTurn(ctx))
	if err != nil {
		return "", err
	}
	return "已上传至 Cloudflare R2 临时图床（仅用于本轮浏览器以图搜图，本轮对话结束后系统会自动从 R2 删除，禁止将该临时链接当作最终图片出处发给用户）：\n" + formatR2StagedLinks(items), nil
}

// sanitizeR2TurnMessages 在写入会话历史前把已删除的临时 R2 直链替换为占位标记，避免后续轮次误用失效链接。
func (p *plugin) sanitizeR2TurnMessages(ctx *zero.Ctx, messages []chatMessage) []chatMessage {
	items := p.r2StagedImages(ctx)
	if len(items) == 0 || len(messages) == 0 {
		return messages
	}
	replaceURLs := func(text string) string {
		for _, item := range items {
			if item.DirectURL != "" {
				text = strings.ReplaceAll(text, item.DirectURL, r2DeletedLinkMarker)
			}
		}
		return text
	}

	cleaned := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		copyMsg := msg
		if copyMsg.Content != "" {
			copyMsg.Content = replaceURLs(copyMsg.Content)
		}
		if len(copyMsg.MultiContent) > 0 {
			parts := make([]openai.ChatMessagePart, 0, len(copyMsg.MultiContent))
			for _, part := range copyMsg.MultiContent {
				partCopy := part
				if partCopy.Text != "" {
					partCopy.Text = replaceURLs(partCopy.Text)
				}
				if partCopy.ImageURL != nil && partCopy.ImageURL.URL != "" {
					imgCopy := *partCopy.ImageURL
					imgCopy.URL = replaceURLs(imgCopy.URL)
					partCopy.ImageURL = &imgCopy
				}
				parts = append(parts, partCopy)
			}
			copyMsg.MultiContent = parts
		}
		cleaned = append(cleaned, copyMsg)
	}
	return cleaned
}
