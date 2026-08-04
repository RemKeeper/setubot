package agent

import (
	"context"
	"fmt"
	"log"
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

	parts := make([]openai.ChatMessagePart, 0, len(images)+1)
	parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: prompt})
	for _, imageURL := range images {
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
	p.debugLogChatRequest("vision_tool", req)

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
