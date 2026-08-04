package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

func (p *plugin) visionToolEnabled() bool {
	return p.cfg.Vision.Enabled && strings.EqualFold(strings.TrimSpace(p.cfg.Vision.Mode), "tool")
}

func (p *plugin) hasRequestImages(ctx *zero.Ctx) bool {
	if hasMessageImages(ctx.Event.Message) {
		return true
	}
	replyID := replyMessageID(ctx.Event.Message)
	if replyID == "" {
		return false
	}
	replied := ctx.GetMessage(replyID, true)
	return hasMessageImages(replied.Elements)
}

func (p *plugin) callAnalyzeImages(ctx *zero.Ctx, prompt string) (string, error) {
	if !p.visionToolEnabled() {
		return "", fmt.Errorf("独立识图工具未启用")
	}
	if strings.TrimSpace(p.cfg.Vision.APIKey) == "" {
		return "", fmt.Errorf("识图工具 API Key 未配置")
	}
	if strings.TrimSpace(p.cfg.Vision.Model) == "" {
		return "", fmt.Errorf("识图工具模型未配置")
	}

	images := p.requestImageSources(ctx)
	if len(images) == 0 {
		return "", fmt.Errorf("当前消息及引用消息中没有可用图片")
	}
	log.Printf("[agent/vision] 已从 OneBot 上下文提取 %d 张图片，来源=%s，视觉模型=%s", len(images), strings.Join(visionImageSourceLabels(images), ","), p.cfg.Vision.Model)
	p.debugLogf("[agent/vision] 当前消息ID=%v 原始消息ID=%s replyID=%s", ctx.Event.MessageID, strings.TrimSpace(string(ctx.Event.RawMessageID)), replyMessageID(ctx.Event.Message))
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(ctx.ExtractPlainText())
	}
	if prompt == "" {
		prompt = "请详细描述图片内容，并识别其中清晰可见的文字。"
	}
	imageDataURLs, err := p.prepareVisionImages(images)
	if err != nil {
		return "", err
	}

	parts := make([]openai.ChatMessagePart, 0, len(imageDataURLs)+1)
	parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: prompt})
	for _, imageURL := range imageDataURLs {
		parts = append(parts, openai.ChatMessagePart{
			Type:     openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{URL: imageURL, Detail: p.visionDetail()},
		})
	}

	req := openai.ChatCompletionRequest{
		Model: p.cfg.Vision.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: p.cfg.Vision.SystemPrompt},
			{Role: openai.ChatMessageRoleUser, MultiContent: parts},
		},
		Temperature: float32(p.cfg.Vision.Temperature),
	}
	p.debugLogf("[agent/vision] 即将请求视觉模型 model=%s images=%d prompt_chars=%d detail=%s", p.cfg.Vision.Model, len(imageDataURLs), len([]rune(prompt)), p.visionDetail())

	clientConfig := openai.DefaultConfig(p.cfg.Vision.APIKey)
	clientConfig.BaseURL = openAIBaseURL(p.cfg.Vision.BaseURL)
	client := openai.NewClientWithConfig(clientConfig)
	timeout := p.cfg.Timeout
	if timeout <= 0 {
		timeout = 120
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	resp, err := client.CreateChatCompletion(requestCtx, req)
	if err != nil {
		return "", fmt.Errorf("视觉模型请求失败: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("视觉模型未返回结果")
	}

	result := strings.TrimSpace(resp.Choices[0].Message.Content)
	if result == "" {
		return "", fmt.Errorf("视觉模型返回了空结果")
	}
	return truncateRunes(result, p.cfg.Vision.MaxResultChars), nil
}

func (p *plugin) prepareVisionImages(images []string) ([]string, error) {
	prepared := make([]string, 0, len(images))
	for i, source := range images {
		dataURL, size, contentType, err := p.visionImageDataURL(source)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张图片处理失败: %w", i+1, err)
		}
		prepared = append(prepared, dataURL)
		p.debugLogf("[agent/vision] 图片[%d] 已转换为 base64，content_type=%s bytes=%d data_url_chars=%d", i+1, contentType, size, len(dataURL))
	}
	return prepared, nil
}

func (p *plugin) visionImageDataURL(source string) (string, int, string, error) {
	if strings.HasPrefix(source, "data:image/") {
		contentType, data, err := decodeImageDataURL(source)
		if err != nil {
			return "", 0, "", err
		}
		if err := p.validateVisionImageSize(len(data)); err != nil {
			return "", 0, "", err
		}
		return imageBytesDataURL(contentType, data), len(data), contentType, nil
	}
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return "", 0, "", fmt.Errorf("不支持的图片来源")
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, source, nil)
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/jpeg,image/png,image/gif,image/*,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/125.0 Safari/537.36")
	req.Header.Set("Referer", visionImageReferer(source))
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", 0, "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", 0, "", fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}

	maxBytes := p.visionMaxImageBytes()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", 0, "", fmt.Errorf("读取下载内容失败: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return "", 0, "", fmt.Errorf("图片超过 %d 字节限制", maxBytes)
	}
	contentType := normalizeVisionImageContentType(resp.Header.Get("Content-Type"), data)
	if contentType == "" {
		return "", 0, "", fmt.Errorf("下载内容不是支持的图片，Content-Type=%q", resp.Header.Get("Content-Type"))
	}
	return imageBytesDataURL(contentType, data), len(data), contentType, nil
}

func (p *plugin) visionMaxImageBytes() int64 {
	if p.cfg.Vision.MaxImageBytes > 0 {
		return p.cfg.Vision.MaxImageBytes
	}
	return 20 << 20
}

func (p *plugin) validateVisionImageSize(size int) error {
	if int64(size) > p.visionMaxImageBytes() {
		return fmt.Errorf("图片超过 %d 字节限制", p.visionMaxImageBytes())
	}
	return nil
}

func decodeImageDataURL(source string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(source, ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", nil, fmt.Errorf("图片 data URL 必须使用 base64 编码")
	}
	contentType := strings.TrimPrefix(strings.Split(header, ";")[0], "data:")
	contentType = normalizeVisionImageMIME(contentType)
	if contentType == "" {
		return "", nil, fmt.Errorf("不支持的图片类型")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("图片 base64 解码失败: %w", err)
	}
	return contentType, data, nil
}

func imageBytesDataURL(contentType string, data []byte) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func normalizeVisionImageContentType(header string, data []byte) string {
	detected := normalizeVisionImageMIME(http.DetectContentType(data))
	if detected != "" {
		return detected
	}
	return normalizeVisionImageMIME(strings.TrimSpace(strings.Split(header, ";")[0]))
}

func normalizeVisionImageMIME(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/gif":
		return "image/gif"
	case "image/webp":
		return "image/webp"
	case "image/avif":
		return "image/avif"
	default:
		return ""
	}
}

func visionImageReferer(source string) string {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}

func visionImageSourceLabels(images []string) []string {
	labels := make([]string, 0, len(images))
	for _, image := range images {
		switch {
		case strings.HasPrefix(image, "data:image/"):
			labels = append(labels, "data:image")
		case strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://"):
			parsed, err := url.Parse(image)
			if err != nil || parsed.Host == "" {
				labels = append(labels, "http(s)")
			} else {
				labels = append(labels, parsed.Host)
			}
		default:
			labels = append(labels, "unknown")
		}
	}
	return labels
}

func (p *plugin) requestImageSources(ctx *zero.Ctx) []string {
	limit := p.cfg.Vision.MaxImages
	if limit <= 0 {
		limit = 8
	}
	images := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	appendImages := func(msg message.Message, origin string) {
		for _, segment := range msg {
			if len(images) >= limit || segment.Type != "image" {
				continue
			}
			source := agentImageSource(segment)
			if source == "" {
				continue
			}
			if _, ok := seen[source]; ok {
				continue
			}
			seen[source] = struct{}{}
			images = append(images, source)
			p.debugLogf("[agent/vision] 图片[%d] origin=%s source=%s", len(images), origin, source)
		}
	}

	if replyID := replyMessageID(ctx.Event.Message); replyID != "" {
		replied := ctx.GetMessage(replyID, true)
		appendImages(replied.Elements, "reply:"+replyID)
	}
	appendImages(ctx.Event.Message, "current:"+fmt.Sprint(ctx.Event.MessageID))
	return images
}
