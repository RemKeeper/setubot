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
	"image/jpeg"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "image/gif"
	_ "image/jpeg"

	_ "golang.org/x/image/webp"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const (
	maxXHSImages              = 30
	forwardImageMaxBytes      = 80 << 20
	forwardImageCacheMaxBytes = 512 << 20
	forwardImageCacheDir      = "forward_image_cache"
	forwardImageJPEGQuality   = 90
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

func (p *plugin) callSendForwardImageBatches(ctx *zero.Ctx, args map[string]interface{}) (string, error) {
	images := cleanImageURLs(stringSliceArg(args, "images"), 0)
	if len(images) == 0 {
		return "", fmt.Errorf("图片链接不能为空")
	}
	batchSize := clamp(numberArg(args, "batch_size", maxXHSImages), 1, maxXHSImages)
	rotate, err := normalizeImageRotation(numberArg(args, "rotate", 0))
	if err != nil {
		return "", err
	}
	batches := splitImageBatches(images, batchSize)
	for i, batch := range batches {
		if err := p.sendForwardImages(ctx, batch, nil, rotate); err != nil {
			return "", fmt.Errorf("第 %d/%d 批发送失败：%w", i+1, len(batches), err)
		}
	}

	if rotate != 0 {
		return fmt.Sprintf("已分 %d 批发送 %d 张图片，每批最多 %d 张，旋转 %d°", len(batches), len(images), batchSize, rotate), nil
	}
	return fmt.Sprintf("已分 %d 批发送 %d 张图片，每批最多 %d 张", len(batches), len(images), batchSize), nil
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

	rotated := make([]string, len(images))
	workerCount := runtime.GOMAXPROCS(0)
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(images) {
		workerCount = len(images)
	}
	type rotateJob struct {
		index int
		url   string
	}
	jobs := make(chan rotateJob)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				fileURL, err := p.rotateForwardImage(job.url, degrees, cacheDir)
				if err != nil {
					select {
					case errCh <- fmt.Errorf("旋转第 %d 张图片失败：%w", job.index+1, err):
					default:
					}
					continue
				}
				rotated[job.index] = fileURL
			}
		}()
	}
	for i, imageURL := range images {
		select {
		case err := <-errCh:
			close(jobs)
			wg.Wait()
			return nil, err
		case jobs <- rotateJob{index: i, url: imageURL}:
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errCh:
		return nil, err
	default:
	}
	maxCacheBytes := p.ehImageCacheMaxBytes()
	if maxCacheBytes <= 0 {
		maxCacheBytes = forwardImageCacheMaxBytes
	}
	if _, _, _, err := cleanupImageCacheBySize(cacheDir, maxCacheBytes); err != nil {
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
	perturbPixels(rotated)

	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", imageURL, degrees, time.Now().UnixNano())))
	path := filepath.Join(cacheDir, fmt.Sprintf("%x.jpg", sum[:16]))
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	encodeErr := jpeg.Encode(file, flattenNRGBAOnWhite(rotated), &jpeg.Options{Quality: forwardImageJPEGQuality})
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
	srcNRGBA := imageToNRGBA(src)
	return rotateNRGBA(srcNRGBA, degrees)
}

func imageToNRGBA(src image.Image) *image.NRGBA {
	if img, ok := src.(*image.NRGBA); ok && img.Rect.Min.X == 0 && img.Rect.Min.Y == 0 && img.Stride == img.Rect.Dx()*4 {
		return img
	}
	bounds := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			dst.Set(x, y, src.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return dst
}

func rotateNRGBA(src *image.NRGBA, degrees int) *image.NRGBA {
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
		srcRow := src.Pix[y*src.Stride:]
		for x := 0; x < width; x++ {
			srcOffset := x * 4
			dstX := x
			dstY := y
			switch degrees {
			case 90:
				dstX = height - 1 - y
				dstY = x
			case 180:
				dstX = width - 1 - x
				dstY = height - 1 - y
			case 270:
				dstX = y
				dstY = width - 1 - x
			}
			dstOffset := dstY*dst.Stride + dstX*4
			copy(dst.Pix[dstOffset:dstOffset+4], srcRow[srcOffset:srcOffset+4])
		}
	}

	return dst
}

func flattenNRGBAOnWhite(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	for y := 0; y < src.Bounds().Dy(); y++ {
		srcRow := src.Pix[y*src.Stride:]
		dstRow := dst.Pix[y*dst.Stride:]
		for x := 0; x < src.Bounds().Dx(); x++ {
			offset := x * 4
			a := int(srcRow[offset+3])
			if a == 255 {
				copy(dstRow[offset:offset+4], srcRow[offset:offset+4])
				dstRow[offset+3] = 255
				continue
			}
			dstRow[offset] = uint8((int(srcRow[offset])*a + 255*(255-a)) / 255)
			dstRow[offset+1] = uint8((int(srcRow[offset+1])*a + 255*(255-a)) / 255)
			dstRow[offset+2] = uint8((int(srcRow[offset+2])*a + 255*(255-a)) / 255)
			dstRow[offset+3] = 255
		}
	}
	return dst
}

// perturbPixels 对图片像素进行轻微随机扰动（每个 RGB 通道 ±1~2），
// 肉眼不可见但使输出文件在二进制层面唯一，用于规避平台图片去重。
func perturbPixels(src *image.NRGBA) {
	pix := src.Pix
	n := len(pix)
	for i := 0; i < n; i += 4 {
		// 对 R、G、B 各通道施加 [-2, +2] 的随机偏移，跳过 Alpha
		for c := 0; c < 3; c++ {
			delta := rand.IntN(5) - 2 // -2, -1, 0, +1, +2
			v := int(pix[i+c]) + delta
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			pix[i+c] = uint8(v)
		}
	}
}

func splitImageBatches(images []string, batchSize int) [][]string {
	if batchSize <= 0 || len(images) == 0 {
		return nil
	}
	batches := make([][]string, 0, (len(images)+batchSize-1)/batchSize)
	for start := 0; start < len(images); start += batchSize {
		end := start + batchSize
		if end > len(images) {
			end = len(images)
		}
		batches = append(batches, images[start:end])
	}

	return batches
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
