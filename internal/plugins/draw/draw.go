package draw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"setubot/internal/config"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

var (
	drawCommandPattern = regexp.MustCompile(`^绘图(?:(\d+)张)?\s+(.+)$`)
	sizePattern        = regexp.MustCompile(`\s+分辨率(\d+x\d+)\s*$`)
)

const responseFormatURL = "url"

type plugin struct {
	cfg           config.DrawConfig
	client        *http.Client
	groupEnabled  map[int64]bool
	groupEnabledM sync.RWMutex
	imageCache    map[string][]string
	imageCacheM   sync.RWMutex
}

type generationRequest struct {
	Model     string                 `json:"model,omitempty"`
	Prompt    string                 `json:"prompt"`
	N         int                    `json:"n,omitempty"`
	Size      string                 `json:"size,omitempty"`
	Image     []string               `json:"image,omitempty"`
	ExtraBody map[string]interface{} `json:"extra_body,omitempty"`
}

type generationResponse struct {
	Data []imageData `json:"data"`
}

type imageData struct {
	URL     string `json:"url"`
	B64JSON string `json:"b64_json"`
}

type drawArgs struct {
	prompt string
	n      int
	size   string
	images []string
}

func Register(cfg config.DrawConfig) {
	p := &plugin{
		cfg:          cfg,
		client:       &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
		groupEnabled: make(map[int64]bool),
		imageCache:   make(map[string][]string),
	}

	zero.OnMessage().Handle(p.cacheImages)
	zero.OnFullMatch("开启绘图", zero.OnlyGroup, zero.SuperUserPermission).Handle(p.enableGroup)
	zero.OnFullMatch("关闭绘图", zero.OnlyGroup, zero.SuperUserPermission).Handle(p.disableGroup)
	zero.OnMessage(p.isDrawCommand).Handle(p.draw)
}

func (p *plugin) isDrawCommand(ctx *zero.Ctx) bool {
	return drawCommandPattern.MatchString(strings.TrimSpace(ctx.ExtractPlainText()))
}

func (p *plugin) enableGroup(ctx *zero.Ctx) {
	p.setGroupEnabled(ctx.Event.GroupID, true)
	ctx.Send("已开启本群绘图功能")
}

func (p *plugin) disableGroup(ctx *zero.Ctx) {
	p.setGroupEnabled(ctx.Event.GroupID, false)
	ctx.Send("已关闭本群绘图功能")
}

func (p *plugin) draw(ctx *zero.Ctx) {
	if !p.cfg.Enabled {
		ctx.Send("绘图功能未启用")
		return
	}
	if ctx.Event.GroupID != 0 && !p.isGroupEnabled(ctx.Event.GroupID) {
		ctx.Send("本群绘图功能已关闭")
		return
	}
	if p.cfg.APIKey == "" {
		ctx.Send("绘图接口 API Key 未配置")
		return
	}

	args, err := p.parse(ctx.ExtractPlainText())
	if err != nil {
		ctx.Send(err.Error())
		return
	}
	args.images = p.extractInputImages(ctx)

	if len(args.images) > 0 {
		ctx.Send("正在二次编辑图片，请稍候...")
	} else {
		ctx.Send("正在绘图，请稍候...")
	}
	images, err := p.generate(args)
	if err != nil {
		ctx.Send(fmt.Sprintf("绘图失败：%v", err))
		return
	}
	if len(images) == 0 {
		ctx.Send("绘图接口未返回图片")
		return
	}

	segments := make(message.Message, 0, len(images))
	for _, img := range images {
		if img.URL != "" {
			segments = append(segments, message.Image(img.URL))
			continue
		}
		if img.B64JSON != "" {
			segments = append(segments, message.Image("base64://"+img.B64JSON))
		}
	}

	if len(segments) == 0 {
		ctx.Send("绘图接口未返回可发送的图片")
		return
	}

	ctx.Send(segments)
}

func (p *plugin) parse(text string) (drawArgs, error) {
	matches := drawCommandPattern.FindStringSubmatch(strings.TrimSpace(text))
	if matches == nil {
		return drawArgs{}, fmt.Errorf("绘图命令帮助：\n1. 绘图 <提示词>\n2. 绘图N张 <提示词>\n3. 绘图 <提示词> 分辨率WxH")
	}

	n := 1
	if matches[1] != "" {
		parsed, err := strconv.Atoi(matches[1])
		if err != nil || parsed <= 0 {
			return drawArgs{}, fmt.Errorf("图片数量必须为正整数")
		}
		n = parsed
	}
	if n > p.cfg.MaxImages {
		return drawArgs{}, fmt.Errorf("单次最多生成 %d 张图片", p.cfg.MaxImages)
	}

	prompt := strings.TrimSpace(matches[2])
	size := p.cfg.DefaultSize
	if sizeMatches := sizePattern.FindStringSubmatch(prompt); sizeMatches != nil {
		size = sizeMatches[1]
		prompt = strings.TrimSpace(sizePattern.ReplaceAllString(prompt, ""))
	}
	if prompt == "" {
		return drawArgs{}, fmt.Errorf("请输入绘图提示词")
	}

	return drawArgs{prompt: prompt, n: n, size: size}, nil
}

func (p *plugin) generate(args drawArgs) ([]imageData, error) {
	body, err := json.Marshal(generationRequest{
		Model:  p.cfg.Model,
		Prompt: args.prompt,
		N:      args.n,
		Size:   args.size,
		Image:  args.images,
		ExtraBody: map[string]interface{}{
			"response_format": responseFormatURL,
		},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, p.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("接口返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result generationResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return result.Data, nil
}

func (p *plugin) endpoint() string {
	return strings.TrimRight(p.cfg.BaseURL, "/") + "/v1/images/generations/"
}

func (p *plugin) cacheImages(ctx *zero.Ctx) {
	images := extractImagesFromMessage(ctx.Event.Message)
	if len(images) == 0 {
		return
	}

	for _, key := range messageCacheKeys(ctx.Event.MessageID, ctx.Event.RawMessageID) {
		p.imageCacheM.Lock()
		p.imageCache[key] = images
		p.imageCacheM.Unlock()
	}
}

func (p *plugin) extractInputImages(ctx *zero.Ctx) []string {
	images := extractImagesFromMessage(ctx.Event.Message)
	if len(images) > 0 {
		return images
	}

	replyID := extractReplyID(ctx.Event.Message)
	if replyID == "" {
		return nil
	}
	if images := p.cachedImages(replyID); len(images) > 0 {
		return images
	}

	replied := ctx.GetMessage(replyID, true)
	return extractImagesFromMessage(replied.Elements)
}

func (p *plugin) cachedImages(messageID string) []string {
	p.imageCacheM.RLock()
	images := append([]string(nil), p.imageCache[messageID]...)
	p.imageCacheM.RUnlock()

	return images
}

func messageCacheKeys(messageID interface{}, rawMessageID json.RawMessage) []string {
	keys := make([]string, 0, 2)
	if messageID != nil {
		keys = append(keys, fmt.Sprint(messageID))
	}

	raw := strings.Trim(strings.TrimSpace(string(rawMessageID)), `"`)
	if raw != "" && !containsString(keys, raw) {
		keys = append(keys, raw)
	}

	return keys
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func extractReplyID(msg message.Message) string {
	for _, segment := range msg {
		if segment.Type == "reply" {
			return segment.Data["id"]
		}
	}

	return ""
}

func extractImagesFromMessage(msg message.Message) []string {
	images := make([]string, 0)
	for _, segment := range msg {
		if segment.Type != "image" {
			continue
		}

		image := imageSource(segment)
		if image == "" {
			continue
		}

		images = append(images, image)
	}

	return images
}

func imageSource(segment message.Segment) string {
	for _, key := range []string{"url", "file"} {
		source := html.UnescapeString(strings.TrimSpace(segment.Data[key]))
		if isSupportedInputImage(source) {
			return source
		}
	}

	return ""
}

func isSupportedInputImage(source string) bool {
	return strings.HasPrefix(source, "http://") ||
		strings.HasPrefix(source, "https://") ||
		strings.HasPrefix(source, "data:image/") ||
		strings.HasPrefix(source, "base64://")
}

func (p *plugin) isGroupEnabled(groupID int64) bool {
	p.groupEnabledM.RLock()
	enabled, ok := p.groupEnabled[groupID]
	p.groupEnabledM.RUnlock()
	if !ok {
		return true
	}

	return enabled
}

func (p *plugin) setGroupEnabled(groupID int64, enabled bool) {
	p.groupEnabledM.Lock()
	p.groupEnabled[groupID] = enabled
	p.groupEnabledM.Unlock()
}
