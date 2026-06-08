package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"setubot/internal/config"

	zero "github.com/wdvxdr1123/ZeroBot"
)

var (
	agentCommandPattern = regexp.MustCompile(`^(?:agent|助手|ai)\s+(.+)$`)
	skillReadPattern    = regexp.MustCompile(`^(?:skill|技能)\s+(?:read|读取)\s+(.+)$`)
	memoryWritePattern  = regexp.MustCompile(`^(?:memory|记忆)\s+(?:write|写入|记住)\s+(.+?)\s*[:：]\s*(.+)$`)
	memoryReadPattern   = regexp.MustCompile(`^(?:memory|记忆)\s+(?:read|读取)\s+(.+)$`)
	memoryListPattern   = regexp.MustCompile(`^(?:memory|记忆)\s+(?:list|列表)$`)
)

type plugin struct {
	cfg    config.AgentConfig
	client *http.Client
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type chatRequest struct {
	Model       string        `json:"model,omitempty"`
	Messages    []chatMessage `json:"messages"`
	Tools       []toolDef     `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolDef struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

func Register(cfg config.AgentConfig) {
	p := &plugin{
		cfg:    cfg,
		client: &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
	}

	zero.OnFullMatchGroup([]string{"agent帮助", "助手帮助", "ai帮助"}).Handle(p.help)
	zero.OnMessage(p.isAgentCommand).Handle(p.agent)
	zero.OnMessage(p.isSkillCommand).Handle(p.readSkill)
	zero.OnMessage(p.isMemoryWriteCommand).Handle(p.writeMemory)
	zero.OnMessage(p.isMemoryReadCommand).Handle(p.readMemory)
	zero.OnMessage(p.isMemoryListCommand).Handle(p.listMemory)
}

func (p *plugin) isAgentCommand(ctx *zero.Ctx) bool {
	return agentCommandPattern.MatchString(strings.TrimSpace(ctx.ExtractPlainText()))
}

func (p *plugin) isSkillCommand(ctx *zero.Ctx) bool {
	return skillReadPattern.MatchString(strings.TrimSpace(ctx.ExtractPlainText()))
}

func (p *plugin) isMemoryWriteCommand(ctx *zero.Ctx) bool {
	return memoryWritePattern.MatchString(strings.TrimSpace(ctx.ExtractPlainText()))
}

func (p *plugin) isMemoryReadCommand(ctx *zero.Ctx) bool {
	return memoryReadPattern.MatchString(strings.TrimSpace(ctx.ExtractPlainText()))
}

func (p *plugin) isMemoryListCommand(ctx *zero.Ctx) bool {
	return memoryListPattern.MatchString(strings.TrimSpace(ctx.ExtractPlainText()))
}

func (p *plugin) help(ctx *zero.Ctx) {
	ctx.Send("Agent 插件命令：\n1. agent <问题>\n2. skill 读取 <文件名>\n3. memory 写入 <键>: <内容>\n4. memory 读取 <键>\n5. memory 列表\n浏览器工具由 agent 自动调用：goto/click/type/html/screenshot/evaluate/scroll")
}

func (p *plugin) agent(ctx *zero.Ctx) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
		return
	}
	if p.cfg.APIKey == "" {
		ctx.Send("Agent 接口 API Key 未配置")
		return
	}

	matches := agentCommandPattern.FindStringSubmatch(strings.TrimSpace(ctx.ExtractPlainText()))
	if matches == nil || strings.TrimSpace(matches[1]) == "" {
		ctx.Send("请输入 agent 问题")
		return
	}

	ctx.Send("Agent 正在思考...")
	answer, err := p.runAgent(strings.TrimSpace(matches[1]))
	if err != nil {
		ctx.Send(fmt.Sprintf("Agent 执行失败：%v", err))
		return
	}

	ctx.Send(truncate(answer, p.cfg.MaxResponseChars))
}

func (p *plugin) runAgent(prompt string) (string, error) {
	memories, _ := p.readAllMemories()
	skills, _ := p.listSkillNames()
	system := p.cfg.SystemPrompt
	if system == "" {
		system = "你是一个简洁可靠的中文 AI 助手。必要时可以调用工具读取 skill、读写记忆、控制浏览器。"
	}
	if len(skills) > 0 {
		system += "\n可用 skills：" + strings.Join(skills, ", ")
	}
	if strings.TrimSpace(memories) != "" {
		system += "\n已保存记忆：\n" + memories
	}

	messages := []chatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}

	for i := 0; i <= p.cfg.MaxToolRounds; i++ {
		resp, err := p.chat(messages)
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("模型未返回结果")
		}

		msg := resp.Choices[0].Message
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			return strings.TrimSpace(msg.Content), nil
		}

		for _, call := range msg.ToolCalls {
			result := p.callTool(call.Function.Name, call.Function.Arguments)
			messages = append(messages, chatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Name:       call.Function.Name,
				Content:    result,
			})
		}
	}

	return "", fmt.Errorf("工具调用轮次超过限制")
}

func (p *plugin) chat(messages []chatMessage) (*chatResponse, error) {
	payload := chatRequest{
		Model:       p.cfg.Model,
		Messages:    messages,
		Tools:       p.toolDefinitions(),
		ToolChoice:  "auto",
		Temperature: p.cfg.Temperature,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, strings.TrimRight(p.cfg.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
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

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.Error != nil && result.Error.Message != "" {
		return nil, fmt.Errorf("%s", result.Error.Message)
	}

	return &result, nil
}

func (p *plugin) readSkill(ctx *zero.Ctx) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
		return
	}
	matches := skillReadPattern.FindStringSubmatch(strings.TrimSpace(ctx.ExtractPlainText()))
	content, err := p.readSkillFile(strings.TrimSpace(matches[1]))
	if err != nil {
		ctx.Send(fmt.Sprintf("读取 skill 失败：%v", err))
		return
	}
	ctx.Send(truncate(content, p.cfg.MaxResponseChars))
}

func (p *plugin) writeMemory(ctx *zero.Ctx) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
		return
	}
	matches := memoryWritePattern.FindStringSubmatch(strings.TrimSpace(ctx.ExtractPlainText()))
	if err := p.writeMemoryFile(matches[1], matches[2]); err != nil {
		ctx.Send(fmt.Sprintf("写入记忆失败：%v", err))
		return
	}
	ctx.Send("已写入记忆")
}

func (p *plugin) readMemory(ctx *zero.Ctx) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
		return
	}
	matches := memoryReadPattern.FindStringSubmatch(strings.TrimSpace(ctx.ExtractPlainText()))
	content, err := p.readMemoryFile(matches[1])
	if err != nil {
		ctx.Send(fmt.Sprintf("读取记忆失败：%v", err))
		return
	}
	ctx.Send(truncate(content, p.cfg.MaxResponseChars))
}

func (p *plugin) listMemory(ctx *zero.Ctx) {
	names, err := p.listMemoryNames()
	if err != nil {
		ctx.Send(fmt.Sprintf("读取记忆列表失败：%v", err))
		return
	}
	if len(names) == 0 {
		ctx.Send("暂无记忆")
		return
	}
	ctx.Send("记忆列表：\n" + strings.Join(names, "\n"))
}

func (p *plugin) callTool(name string, rawArgs string) string {
	var args map[string]interface{}
	if rawArgs != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return "参数 JSON 解析失败：" + err.Error()
		}
	}

	switch name {
	case "read_skill":
		content, err := p.readSkillFile(stringArg(args, "name"))
		return toolResult(content, err)
	case "write_memory":
		err := p.writeMemoryFile(stringArg(args, "key"), stringArg(args, "content"))
		return toolResult("已写入记忆", err)
	case "read_memory":
		content, err := p.readMemoryFile(stringArg(args, "key"))
		return toolResult(content, err)
	case "browser_goto", "browser_click", "browser_type", "browser_html", "browser_screenshot", "browser_evaluate", "browser_scroll":
		content, err := p.callBrowser(name, args)
		return toolResult(content, err)
	default:
		return "未知工具：" + name
	}
}

func toolResult(content string, err error) string {
	if err != nil {
		return "错误：" + err.Error()
	}
	return content
}

func stringArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func numberArg(args map[string]interface{}, key string, fallback int) int {
	if args == nil || args[key] == nil {
		return fallback
	}
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func boolArg(args map[string]interface{}, key string, fallback bool) bool {
	if args == nil || args[key] == nil {
		return fallback
	}
	v, ok := args[key].(bool)
	if !ok {
		return fallback
	}
	return v
}

func truncate(text string, max int) string {
	if max <= 0 || len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "..."
}

func safeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Trim(name, ". /")
	return name
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

func readTextFileInDir(dir string, name string) (string, error) {
	name = safeName(name)
	if name == "" {
		return "", fmt.Errorf("文件名不能为空")
	}
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func listFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}
