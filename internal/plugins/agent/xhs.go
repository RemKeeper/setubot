package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"

	_ "golang.org/x/image/webp"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const (
	maxXHSImages         = 30
	forwardImageMaxBytes = 80 << 20
	forwardImageCacheMax = 100
	forwardImageCacheDir = "forward_image_cache"
)

type xhsSetuOutput struct {
	Count   int             `json:"count"`
	Results []xhsSetuResult `json:"results"`
	Error   string          `json:"error"`
}

type xhsSetuResult struct {
	Title      string                 `json:"title"`
	URL        string                 `json:"url"`
	NoteID     string                 `json:"note_id"`
	Tags       []string               `json:"tags"`
	TagMatches map[string]interface{} `json:"tag_matches"`
	Images     []string               `json:"images"`
	Liked      bool                   `json:"liked"`
	Collected  bool                   `json:"collected"`
}

type xhsLastState map[string]xhsLastItem

type xhsLastItem struct {
	Title      string                 `json:"title"`
	URL        string                 `json:"url"`
	NoteID     string                 `json:"note_id"`
	Tags       []string               `json:"tags"`
	TagMatches map[string]interface{} `json:"tag_matches"`
	Images     []string               `json:"images"`
	Time       time.Time              `json:"time"`
}

func (p *plugin) runXHSSetu(ctx *zero.Ctx, args map[string]interface{}) (string, error) {
	count := clamp(numberArg(args, "count", 1), 1, 5)
	keyword := stringArg(args, "keyword")
	scriptPath, err := filepath.Abs(filepath.Join(p.cfg.SkillDir, "xhs_setu.py"))
	if err != nil {
		return "", err
	}

	timeout := time.Duration(p.cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmdArgs := []string{scriptPath, "--count", fmt.Sprint(count)}
	if args != nil && args["scroll"] != nil {
		scroll := clamp(numberArg(args, "scroll", 0), 1, 10)
		cmdArgs = append(cmdArgs, "--scroll", fmt.Sprint(scroll))
	}
	if boolArg(args, "skip_engage", false) {
		cmdArgs = append(cmdArgs, "--skip-engage")
	}
	if keyword != "" {
		cmdArgs = append(cmdArgs, "--keyword", keyword)
		seenPath := p.xhsSeenPath(ctx, keyword)
		if err := os.MkdirAll(filepath.Dir(seenPath), 0755); err != nil {
			return "", err
		}
		cmdArgs = append(cmdArgs, "--seen", seenPath)
	}
	cmd := exec.CommandContext(runCtx, "python3", cmdArgs...)
	cmd.Dir = filepath.Dir(scriptPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("执行 xhs_setu.py 失败：%w\n%s", err, strings.TrimSpace(stderr.String()))
	}

	var output xhsSetuOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return "", fmt.Errorf("解析 xhs_setu.py 输出失败：%w\n%s", err, strings.TrimSpace(stdout.String()))
	}
	if output.Error != "" {
		return "", errors.New(output.Error)
	}

	images := collectXHSImages(output.Results, maxXHSImages)
	if len(images) == 0 {
		return "脚本执行完成，但没有提取到可发送图片", nil
	}

	if err := p.saveXHSLast(ctx, output.Results); err != nil {
		return "", err
	}
	p.sendXHSImages(ctx, output.Results, images)
	return fmt.Sprintf("已处理 %d 个帖子，发送 %d 张图片", len(output.Results), len(images)), nil
}

func (p *plugin) runXHSDislike(ctx *zero.Ctx, args map[string]interface{}) (string, error) {
	last, ok, err := p.loadXHSLast(ctx)
	if err != nil {
		return "", err
	}
	if ok && last.URL != "" {
		if _, err := p.browserPost("/api/goto", map[string]interface{}{"url": last.URL}); err != nil {
			return "", fmt.Errorf("导航到最近帖子失败：%w", err)
		}
		time.Sleep(3 * time.Second)
	}

	scriptPath, err := filepath.Abs(filepath.Join(p.cfg.SkillDir, "xhs_dislike.py"))
	if err != nil {
		return "", err
	}
	timeout := time.Duration(p.cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmdArgs := []string{scriptPath}
	if keyword := stringArg(args, "keyword"); keyword != "" {
		cmdArgs = append(cmdArgs, "--keyword", keyword)
	}
	cmd := exec.CommandContext(runCtx, "python3", cmdArgs...)
	cmd.Dir = filepath.Dir(scriptPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("执行 xhs_dislike.py 失败：%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func collectXHSImages(results []xhsSetuResult, limit int) []string {
	seen := make(map[string]struct{})
	images := make([]string, 0)
	for _, result := range results {
		for _, imageURL := range result.Images {
			imageURL = strings.TrimSpace(imageURL)
			if imageURL == "" {
				continue
			}
			if _, ok := seen[imageURL]; ok {
				continue
			}
			seen[imageURL] = struct{}{}
			images = append(images, imageURL)
			if len(images) >= limit {
				return images
			}
		}
	}
	return images
}

func (p *plugin) sendXHSImages(ctx *zero.Ctx, results []xhsSetuResult, images []string) {
	nodes := make(message.Message, 0, len(results))
	for _, result := range results {
		title := strings.TrimSpace(result.Title)
		if title != "" {
			nodes = append(nodes, p.forwardNode(ctx, title))
		}
		if len(result.Tags) > 0 {
			nodes = append(nodes, p.forwardNode(ctx, "Tags: #"+strings.Join(result.Tags, " #")))
		}
	}
	if err := p.sendForwardImages(ctx, images, nodes, 0); err != nil {
		log.Printf("[agent/xhs] 合并发送图片失败: %v", err)
	}
}

func (p *plugin) callSendForwardImages(ctx *zero.Ctx, args map[string]interface{}) (string, error) {
	images := stringSliceArg(args, "images")
	rotate, err := normalizeImageRotation(numberArg(args, "rotate", 0))
	if err != nil {
		return "", err
	}
	if err := p.sendForwardImages(ctx, images, nil, rotate); err != nil {
		return "", err
	}
	if rotate != 0 {
		return fmt.Sprintf("已合并发送 %d 张图片，旋转 %d°", len(cleanImageURLs(images, maxXHSImages)), rotate), nil
	}

	return fmt.Sprintf("已合并发送 %d 张图片", len(cleanImageURLs(images, maxXHSImages))), nil
}

func (p *plugin) sendForwardImages(ctx *zero.Ctx, images []string, prefixNodes message.Message, rotate int) error {
	images = cleanImageURLs(images, maxXHSImages)
	if len(images) == 0 {
		return fmt.Errorf("图片链接不能为空")
	}
	if rotate != 0 {
		rotated, err := p.rotateForwardImages(images, rotate)
		if err != nil {
			return err
		}
		images = rotated
	}

	if len(images) == 1 && len(prefixNodes) == 0 {
		msg := message.Image(images[0])
		log.Printf("[agent/forward_images] 单图发送: sender=%d 类型=%T 内容=%+v", ctx.Event.UserID, msg, msg)
		ctx.Send(msg)
		return nil
	}

	nodes := make(message.Message, 0, len(prefixNodes)+len(images))
	nodes = append(nodes, prefixNodes...)
	for _, imageURL := range images {
		nodes = append(nodes, p.forwardNode(ctx, message.Message{message.Image(imageURL)}))
	}

	log.Printf("[agent/forward_images] 合并转发发送: sender=%d 节点数=%d 类型=%T", ctx.Event.UserID, len(nodes), nodes)
	ctx.Send(nodes)
	return nil
}

func normalizeImageRotation(degrees int) (int, error) {
	switch degrees {
	case 0, 90, 180, 270:
		return degrees, nil
	default:
		return 0, fmt.Errorf("rotate 只支持 0、90、180、270")
	}
}

func (p *plugin) rotateForwardImages(images []string, degrees int) ([]string, error) {
	cacheDir, err := filepath.Abs(filepath.Join(filepath.Dir(p.cfg.MemoryDir), forwardImageCacheDir))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, err
	}

	rotated := make([]string, 0, len(images))
	for i, imageURL := range images {
		fileURL, err := p.rotateForwardImage(imageURL, degrees, cacheDir)
		if err != nil {
			return nil, fmt.Errorf("旋转第 %d 张图片失败：%w", i+1, err)
		}
		rotated = append(rotated, fileURL)
	}
	if _, err := cleanupEHImageCache(cacheDir, forwardImageCacheMax); err != nil {
		log.Printf("[agent/forward_images] 清理旋转图片缓存失败: %v", err)
	}

	return rotated, nil
}

func (p *plugin) rotateForwardImage(imageURL string, degrees int, cacheDir string) (string, error) {
	data, err := p.readForwardImageData(imageURL)
	if err != nil {
		return "", err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("解码图片失败: %w", err)
	}
	rotated := rotateImage(img, degrees)

	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", imageURL, degrees, time.Now().UnixNano())))
	path := filepath.Join(cacheDir, fmt.Sprintf("%x.png", sum[:16]))
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	encodeErr := png.Encode(file, rotated)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(path)
		return "", encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}

	return (&url.URL{Scheme: "file", Path: path}).String(), nil
}

func (p *plugin) readForwardImageData(imageURL string) ([]byte, error) {
	switch {
	case strings.HasPrefix(imageURL, "base64://"):
		return base64.StdEncoding.DecodeString(strings.TrimPrefix(imageURL, "base64://"))
	case strings.HasPrefix(imageURL, "file://"):
		parsed, err := url.Parse(imageURL)
		if err != nil {
			return nil, err
		}
		return readForwardImageFile(parsed.Path)
	case strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://"):
		return p.downloadForwardImageData(imageURL)
	default:
		return readForwardImageFile(imageURL)
	}
}

func readForwardImageFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > forwardImageMaxBytes {
		return nil, fmt.Errorf("图片超过 80MB 限制")
	}
	return os.ReadFile(path)
}

func (p *plugin) downloadForwardImageData(imageURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("图片请求返回 %d", resp.StatusCode)
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return nil, fmt.Errorf("响应不是图片: %s", contentType)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, forwardImageMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > forwardImageMaxBytes {
		return nil, fmt.Errorf("图片超过 80MB 限制")
	}
	return data, nil
}

func rotateImage(src image.Image, degrees int) *image.NRGBA {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	var dst *image.NRGBA
	if degrees == 90 || degrees == 270 {
		dst = image.NewNRGBA(image.Rect(0, 0, height, width))
	} else {
		dst = image.NewNRGBA(image.Rect(0, 0, width, height))
	}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			color := src.At(bounds.Min.X+x, bounds.Min.Y+y)
			switch degrees {
			case 90:
				dst.Set(height-1-y, x, color)
			case 180:
				dst.Set(width-1-x, height-1-y, color)
			case 270:
				dst.Set(y, width-1-x, color)
			default:
				dst.Set(x, y, color)
			}
		}
	}

	return dst
}

func (p *plugin) forwardNode(ctx *zero.Ctx, content interface{}) message.Segment {
	return message.CustomNode(forwardSenderName(ctx), ctx.Event.UserID, content)
}

func forwardSenderName(ctx *zero.Ctx) string {
	if ctx.Event.Sender != nil {
		return ctx.Event.Sender.Name()
	}
	return fmt.Sprint(ctx.Event.UserID)
}

func cleanImageURLs(images []string, limit int) []string {
	seen := make(map[string]struct{}, len(images))
	cleaned := make([]string, 0, len(images))
	for _, imageURL := range images {
		imageURL = strings.TrimSpace(imageURL)
		if imageURL == "" {
			continue
		}
		if _, ok := seen[imageURL]; ok {
			continue
		}
		seen[imageURL] = struct{}{}
		cleaned = append(cleaned, imageURL)
		if limit > 0 && len(cleaned) >= limit {
			break
		}
	}

	return cleaned
}

func (p *plugin) saveXHSLast(ctx *zero.Ctx, results []xhsSetuResult) error {
	if len(results) == 0 {
		return nil
	}
	last := results[len(results)-1]
	state, err := p.readXHSLastState()
	if err != nil {
		return err
	}
	state[p.sessionKey(ctx)] = xhsLastItem{Title: last.Title, URL: last.URL, NoteID: last.NoteID, Tags: last.Tags, TagMatches: last.TagMatches, Images: last.Images, Time: time.Now()}
	return p.writeXHSLastState(state)
}

func (p *plugin) loadXHSLast(ctx *zero.Ctx) (xhsLastItem, bool, error) {
	state, err := p.readXHSLastState()
	if err != nil {
		return xhsLastItem{}, false, err
	}
	item, ok := state[p.sessionKey(ctx)]
	return item, ok, nil
}

func (p *plugin) readXHSLastState() (xhsLastState, error) {
	path := p.xhsLastPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return xhsLastState{}, nil
	}
	if err != nil {
		return nil, err
	}
	var state xhsLastState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state == nil {
		state = xhsLastState{}
	}
	return state, nil
}

func (p *plugin) writeXHSLastState(state xhsLastState) error {
	path := p.xhsLastPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (p *plugin) xhsLastPath() string {
	return filepath.Join(filepath.Dir(p.cfg.MemoryDir), "xhs_last.json")
}

func (p *plugin) xhsSeenPath(ctx *zero.Ctx, keyword string) string {
	return filepath.Join(filepath.Dir(p.cfg.MemoryDir), "xhs_seen", safeName(p.sessionKey(ctx)+"_"+keyword)+".txt")
}

func clamp(value int, minValue int, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
