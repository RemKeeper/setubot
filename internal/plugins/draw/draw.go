package draw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

type plugin struct {
	cfg           config.DrawConfig
	client        *http.Client
	groupEnabled  map[int64]bool
	groupEnabledM sync.RWMutex
}

type generationRequest struct {
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`
	N      int    `json:"n"`
	Size   string `json:"size,omitempty"`
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
}

func Register(cfg config.DrawConfig) {
	p := &plugin{
		cfg:          cfg,
		client:       &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
		groupEnabled: make(map[int64]bool),
	}

	zero.OnFullMatch("开启绘图", zero.OnlyGroup, zero.SuperUserPermission).Handle(p.enableGroup)
	zero.OnFullMatch("关闭绘图", zero.OnlyGroup, zero.SuperUserPermission).Handle(p.disableGroup)
	zero.OnRegex(`^绘图(?:\d+张)?\s+.+`).Handle(p.draw)
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

	ctx.Send("正在绘图，请稍候...")
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
