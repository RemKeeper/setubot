package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const maxXHSImages = 30

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
	if len(images) == 1 {
		ctx.Send(message.Image(images[0]))
		return
	}

	nodes := make(message.Message, 0, len(images)+len(results))
	botName := "setubot"
	if len(p.nickNames) > 0 {
		botName = p.nickNames[0]
	}
	for _, result := range results {
		title := strings.TrimSpace(result.Title)
		if title != "" {
			nodes = append(nodes, message.CustomNode(botName, ctx.Event.SelfID, title))
		}
		if len(result.Tags) > 0 {
			nodes = append(nodes, message.CustomNode(botName, ctx.Event.SelfID, "Tags: #"+strings.Join(result.Tags, " #")))
		}
	}
	for _, imageURL := range images {
		nodes = append(nodes, message.CustomNode(botName, ctx.Event.SelfID, message.Message{message.Image(imageURL)}))
		if len(nodes) >= maxXHSImages+len(results) {
			ctx.Send(nodes)
			return
		}
	}
	ctx.Send(nodes)
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
