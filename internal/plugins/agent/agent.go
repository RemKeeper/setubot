package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
	zero "github.com/wdvxdr1123/ZeroBot"
)

var (
	agentAliasPattern   = regexp.MustCompile(`^(?:agent|助手|ai)\s+(.+)$`)
	skillReadPattern    = regexp.MustCompile(`^(?:skill|技能)\s+(?:read|读取)\s+(.+)$`)
	memoryWritePattern  = regexp.MustCompile(`^(?:memory|记忆)\s+(?:write|写入|记住)\s+(.+?)\s*[:：]\s*(.+)$`)
	memoryReadPattern   = regexp.MustCompile(`^(?:memory|记忆)\s+(?:read|读取)\s+(.+)$`)
	memoryListPattern   = regexp.MustCompile(`^(?:memory|记忆)\s+(?:list|列表)$`)
	resetContextPattern = regexp.MustCompile(`^(?:reset|重置|清空|清除)\s*(?:context|上下文|会话|对话)?$`)
)

type plugin struct {
	cfg        config.AgentConfig
	nickNames  []string
	aiClient   *openai.Client
	httpClient *http.Client
	sessions   map[string]*conversationSession
	sessionM   sync.Mutex
}

type conversationSession struct {
	summary   string
	messages  []chatMessage
	updatedAt time.Time
}

type chatMessage = openai.ChatCompletionMessage

func Register(cfg config.AgentConfig, nickNames []string) {
	aiConfig := openai.DefaultConfig(cfg.APIKey)
	aiConfig.BaseURL = openAIBaseURL(cfg.BaseURL)

	p := &plugin{
		cfg:        cfg,
		nickNames:  normalizeNickNames(nickNames),
		aiClient:   openai.NewClientWithConfig(aiConfig),
		httpClient: &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
		sessions:   make(map[string]*conversationSession),
	}
	if err := p.ensureStorageDirs(); err != nil {
		log.Printf("[agent] 初始化目录失败: %v", err)
	}

	zero.OnMessage(p.isHelpCommand).Handle(p.help)
	zero.OnMessage(p.isResetContextCommand).Handle(p.resetContext)
	zero.OnMessage(p.isSkillCommand).Handle(p.readSkill)
	zero.OnMessage(p.isMemoryWriteCommand).Handle(p.writeMemory)
	zero.OnMessage(p.isMemoryReadCommand).Handle(p.readMemory)
	zero.OnMessage(p.isMemoryListCommand).Handle(p.listMemory)
	zero.OnMessage(p.isAgentCommand).Handle(p.agent)
}

func openAIBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}

	return baseURL + "/v1"
}

func (p *plugin) ensureStorageDirs() error {
	if err := ensureDir(p.cfg.SkillDir); err != nil {
		return fmt.Errorf("创建 skills 目录失败: %w", err)
	}
	if err := ensureDir(p.cfg.MemoryDir); err != nil {
		return fmt.Errorf("创建 memory 目录失败: %w", err)
	}

	return nil
}

func normalizeNickNames(nickNames []string) []string {
	result := make([]string, 0, len(nickNames))
	for _, nickName := range nickNames {
		nickName = strings.TrimSpace(nickName)
		if nickName == "" {
			continue
		}
		result = append(result, nickName)
	}

	return result
}

func (p *plugin) extractNickCommand(text string) (string, bool) {
	text = strings.TrimSpace(text)
	for _, nickName := range p.nickNames {
		if !strings.HasPrefix(text, nickName) {
			continue
		}

		command := strings.TrimSpace(strings.TrimPrefix(text, nickName))
		command = strings.TrimLeft(command, " \t\r\n,，:：")
		if command == "" {
			return "", false
		}

		return command, true
	}

	return "", false
}

func (p *plugin) isAgentCommand(ctx *zero.Ctx) bool {
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	if !ok {
		return false
	}

	return !p.isHelpText(command) &&
		!resetContextPattern.MatchString(command) &&
		!skillReadPattern.MatchString(command) &&
		!memoryWritePattern.MatchString(command) &&
		!memoryReadPattern.MatchString(command) &&
		!memoryListPattern.MatchString(command)
}

func (p *plugin) isSkillCommand(ctx *zero.Ctx) bool {
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	return ok && skillReadPattern.MatchString(command)
}

func (p *plugin) isMemoryWriteCommand(ctx *zero.Ctx) bool {
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	return ok && memoryWritePattern.MatchString(command)
}

func (p *plugin) isMemoryReadCommand(ctx *zero.Ctx) bool {
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	return ok && memoryReadPattern.MatchString(command)
}

func (p *plugin) isMemoryListCommand(ctx *zero.Ctx) bool {
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	return ok && memoryListPattern.MatchString(command)
}

func (p *plugin) isHelpCommand(ctx *zero.Ctx) bool {
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	return ok && p.isHelpText(command)
}

func (p *plugin) isResetContextCommand(ctx *zero.Ctx) bool {
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	return ok && resetContextPattern.MatchString(command)
}

func (p *plugin) isHelpText(command string) bool {
	switch strings.TrimSpace(command) {
	case "帮助", "help", "agent帮助", "助手帮助", "ai帮助":
		return true
	default:
		return false
	}
}

func (p *plugin) help(ctx *zero.Ctx) {
	ctx.Send("Agent 插件命令：\n1. <昵称> <问题>\n2. <昵称> 重置上下文\n3. <昵称> skill 读取 <文件名>\n4. <昵称> memory 写入 <键>: <内容>\n5. <昵称> memory 读取 <键>\n6. <昵称> memory 列表\n浏览器工具由 agent 自动调用：goto/click/type/html/screenshot/evaluate/scroll")
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

	prompt, ok := p.extractNickCommand(ctx.ExtractPlainText())
	if !ok || strings.TrimSpace(prompt) == "" {
		ctx.Send("请输入 agent 问题")
		return
	}
	if matches := agentAliasPattern.FindStringSubmatch(prompt); matches != nil {
		prompt = matches[1]
	}

	ctx.Send("Agent 正在思考...")
	answer, err := p.runAgent(ctx, strings.TrimSpace(prompt))
	if err != nil {
		ctx.Send(fmt.Sprintf("Agent 执行失败：%v", err))
		return
	}

	ctx.Send(truncate(answer, p.cfg.MaxResponseChars))
}

func (p *plugin) resetContext(ctx *zero.Ctx) {
	p.clearSession(p.sessionKey(ctx))
	ctx.Send("已重置当前上下文")
}

func (p *plugin) runAgent(ctx *zero.Ctx, prompt string) (string, error) {
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

	sessionKey := p.sessionKey(ctx)
	turnMessages := []chatMessage{{Role: openai.ChatMessageRoleUser, Content: prompt}}
	messages := p.buildMessages(system, sessionKey, turnMessages)

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
		turnMessages = append(turnMessages, msg)
		if len(msg.ToolCalls) == 0 {
			p.appendSession(sessionKey, turnMessages)
			return strings.TrimSpace(msg.Content), nil
		}

		for _, call := range msg.ToolCalls {
			result := p.callTool(call.Function.Name, call.Function.Arguments)
			toolMessage := chatMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: call.ID,
				Content:    result,
			}
			messages = append(messages, toolMessage)
			turnMessages = append(turnMessages, toolMessage)
		}
	}

	return "", fmt.Errorf("工具调用轮次超过限制")
}

func (p *plugin) sessionKey(ctx *zero.Ctx) string {
	if ctx.Event.GroupID != 0 {
		return fmt.Sprintf("group:%d:user:%d", ctx.Event.GroupID, ctx.Event.UserID)
	}

	return fmt.Sprintf("private:%d", ctx.Event.UserID)
}

func (p *plugin) buildMessages(system string, sessionKey string, turnMessages []chatMessage) []chatMessage {
	summary, history := p.sessionHistory(sessionKey)
	messages := make([]chatMessage, 0, 2+len(history)+len(turnMessages))
	messages = append(messages, chatMessage{Role: openai.ChatMessageRoleSystem, Content: system})
	if strings.TrimSpace(summary) != "" {
		messages = append(messages, chatMessage{Role: openai.ChatMessageRoleSystem, Content: "以下是较早对话的压缩摘要，请作为长期上下文参考：\n" + summary})
	}
	messages = append(messages, history...)
	messages = append(messages, turnMessages...)

	return messages
}

func (p *plugin) sessionHistory(sessionKey string) (string, []chatMessage) {
	p.sessionM.Lock()
	defer p.sessionM.Unlock()

	session, ok := p.sessions[sessionKey]
	if !ok {
		return "", nil
	}
	if p.cfg.ContextTTL > 0 && time.Since(session.updatedAt) > time.Duration(p.cfg.ContextTTL)*time.Second {
		delete(p.sessions, sessionKey)
		return "", nil
	}

	return session.summary, append([]chatMessage(nil), session.messages...)
}

func (p *plugin) appendSession(sessionKey string, messages []chatMessage) {
	p.sessionM.Lock()
	session, ok := p.sessions[sessionKey]
	if !ok {
		session = &conversationSession{}
		p.sessions[sessionKey] = session
	}
	session.messages = append(session.messages, messages...)
	session.updatedAt = time.Now()
	shouldSummarize := shouldSummarizeContext(session.messages, p.cfg.SummaryTriggerTurns)
	if !shouldSummarize {
		session.messages = trimContextMessages(session.messages, p.cfg.MaxContextTurns)
	}
	p.sessionM.Unlock()

	if shouldSummarize {
		p.summarizeSession(sessionKey)
	}
}

func shouldSummarizeContext(messages []chatMessage, triggerTurns int) bool {
	if triggerTurns <= 0 {
		return false
	}

	return len(messages) > triggerTurns*4
}

func (p *plugin) summarizeSession(sessionKey string) {
	p.sessionM.Lock()
	session, ok := p.sessions[sessionKey]
	if !ok {
		p.sessionM.Unlock()
		return
	}
	keepMessages := p.cfg.SummaryKeepTurns * 4
	if keepMessages <= 0 {
		keepMessages = 16
	}
	if len(session.messages) <= keepMessages {
		p.sessionM.Unlock()
		return
	}
	oldMessages := append([]chatMessage(nil), session.messages[:len(session.messages)-keepMessages]...)
	recentMessages := append([]chatMessage(nil), session.messages[len(session.messages)-keepMessages:]...)
	previousSummary := session.summary
	p.sessionM.Unlock()

	summary, err := p.summarizeContext(previousSummary, oldMessages)
	if err != nil {
		log.Printf("[agent] 总结上下文失败: %v", err)
		p.sessionM.Lock()
		if session, ok := p.sessions[sessionKey]; ok {
			session.messages = trimContextMessages(session.messages, p.cfg.MaxContextTurns)
		}
		p.sessionM.Unlock()
		return
	}

	p.sessionM.Lock()
	if session, ok := p.sessions[sessionKey]; ok {
		session.summary = summary
		session.messages = recentMessages
		session.updatedAt = time.Now()
	}
	p.sessionM.Unlock()
}

func (p *plugin) summarizeContext(previousSummary string, messages []chatMessage) (string, error) {
	content := "请把以下对话压缩成可延续多轮对话的中文上下文摘要。保留用户目标、关键事实、已完成操作、工具结果、未解决问题和偏好。不要添加不存在的信息。"
	if strings.TrimSpace(previousSummary) != "" {
		content += "\n\n已有摘要：\n" + previousSummary
	}
	content += "\n\n待压缩对话：\n" + renderMessagesForSummary(messages)

	resp, err := p.aiClient.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model:       p.cfg.Model,
		Messages:    []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: content}},
		Temperature: 0.2,
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("总结模型未返回结果")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

func renderMessagesForSummary(messages []chatMessage) string {
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" && len(msg.ToolCalls) > 0 {
			calls := make([]string, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				calls = append(calls, call.Function.Name+"("+call.Function.Arguments+")")
			}
			content = "调用工具：" + strings.Join(calls, "; ")
		}
		if content == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s] %s", msg.Role, content))
	}

	return strings.Join(parts, "\n")
}

func (p *plugin) clearSession(sessionKey string) {
	p.sessionM.Lock()
	delete(p.sessions, sessionKey)
	p.sessionM.Unlock()
}

func trimContextMessages(messages []chatMessage, maxTurns int) []chatMessage {
	if maxTurns <= 0 {
		return messages
	}
	maxMessages := maxTurns * 4
	if len(messages) <= maxMessages {
		return messages
	}

	trimmed := append([]chatMessage(nil), messages[len(messages)-maxMessages:]...)
	for len(trimmed) > 0 && trimmed[0].Role == openai.ChatMessageRoleTool {
		trimmed = trimmed[1:]
	}

	return trimmed
}

func (p *plugin) chat(messages []chatMessage) (*openai.ChatCompletionResponse, error) {
	resp, err := p.aiClient.CreateChatCompletion(context.Background(), openai.ChatCompletionRequest{
		Model:       p.cfg.Model,
		Messages:    messages,
		Tools:       p.toolDefinitions(),
		ToolChoice:  "auto",
		Temperature: float32(p.cfg.Temperature),
	})
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

func (p *plugin) readSkill(ctx *zero.Ctx) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
		return
	}
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	if !ok {
		ctx.Send("请输入 skill 读取命令")
		return
	}
	matches := skillReadPattern.FindStringSubmatch(command)
	if matches == nil {
		ctx.Send("skill 命令格式：<昵称> skill 读取 <文件名>")
		return
	}
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
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	if !ok {
		ctx.Send("请输入 memory 写入命令")
		return
	}
	matches := memoryWritePattern.FindStringSubmatch(command)
	if matches == nil {
		ctx.Send("memory 写入格式：<昵称> memory 写入 <键>: <内容>")
		return
	}
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
	command, ok := p.extractNickCommand(ctx.ExtractPlainText())
	if !ok {
		ctx.Send("请输入 memory 读取命令")
		return
	}
	matches := memoryReadPattern.FindStringSubmatch(command)
	if matches == nil {
		ctx.Send("memory 读取格式：<昵称> memory 读取 <键>")
		return
	}
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
