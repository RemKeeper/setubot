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
	superUsers []int64
	aiClient   *openai.Client
	httpClient *http.Client
	memory     *MemoryStore
	ehTags     *ehTagStore
	sessions   map[string]*conversationSession
	sessionM   sync.Mutex
}

type conversationSession struct {
	summary   string
	messages  []chatMessage
	updatedAt time.Time
}

type chatMessage = openai.ChatCompletionMessage

func Register(cfg config.AgentConfig, nickNames []string, superUsers []int64) {
	aiConfig := openai.DefaultConfig(cfg.APIKey)
	aiConfig.BaseURL = openAIBaseURL(cfg.BaseURL)

	p := &plugin{
		cfg:        cfg,
		nickNames:  normalizeNickNames(nickNames),
		superUsers: append([]int64(nil), superUsers...),
		aiClient:   openai.NewClientWithConfig(aiConfig),
		httpClient: &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
		sessions:   make(map[string]*conversationSession),
	}
	p.ehTags = newEHTagStore(ehTagRuntimeConfig{
		Enabled:   cfg.EHTag.Enabled,
		SourceURL: cfg.EHTag.SourceURL,
		CachePath: cfg.EHTag.CachePath,
	}, p.httpClient)
	if err := p.ensureStorageDirs(); err != nil {
		log.Printf("[agent] 初始化目录失败: %v", err)
	}
	if store, err := NewMemoryStore(cfg.MemoryDir); err != nil {
		log.Printf("[agent] 初始化记忆索引失败，将退回文件读取: %v", err)
	} else {
		p.memory = store
		if err := p.memory.Rebuild(); err != nil {
			log.Printf("[agent] 重建记忆索引失败: %v", err)
		}
	}
	if p.ehTags != nil && cfg.EHTag.Enabled {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Timeout)*time.Second)
			defer cancel()
			if err := p.ehTags.Load(ctx, false); err != nil {
				log.Printf("[agent/eh_tag] 初始化标签数据库失败: %v", err)
			}
		}()
	}

	zero.OnMessage(p.isTriggerMessage).Handle(p.dispatch)
}

func (p *plugin) isTriggerMessage(ctx *zero.Ctx) bool {
	if ctx.Event.GroupID != 0 {
		return zero.OnlyToMe(ctx)
	}

	return strings.TrimSpace(ctx.ExtractPlainText()) != ""
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

func (p *plugin) dispatch(ctx *zero.Ctx) {
	command, ok := p.extractCommand(ctx)
	if !ok {
		return
	}

	switch {
	case p.isHelpText(command):
		p.help(ctx)
	case resetContextPattern.MatchString(command):
		p.resetContext(ctx)
	case skillReadPattern.MatchString(command):
		p.readSkillCommand(ctx, command)
	case memoryWritePattern.MatchString(command):
		p.writeMemoryCommand(ctx, command)
	case memoryReadPattern.MatchString(command):
		p.readMemoryCommand(ctx, command)
	case memoryListPattern.MatchString(command):
		p.listMemory(ctx)
	default:
		p.agentCommand(ctx, command)
	}
}

func (p *plugin) extractCommand(ctx *zero.Ctx) (string, bool) {
	text := strings.TrimSpace(ctx.ExtractPlainText())
	if ctx.Event.GroupID != 0 {
		command := strings.TrimLeft(text, " \t\r\n,，:：")
		return command, command != ""
	}

	return text, text != ""
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
	ctx.Send("Agent 插件命令：\n1. 群聊：@机器人 <问题>\n2. 私聊：直接发送 <问题>\n3. 重置上下文\n4. skill 读取 <文件名>\n5. memory 写入 <键>: <内容>\n6. memory 读取 <键>\n7. memory 列表\n小红书：来点涩图 / 来N张涩图 / 来点关键词涩图 / 不喜欢\n浏览器工具由 agent 自动调用：goto/click/type/html/screenshot/evaluate/scroll")
}

func (p *plugin) agentCommand(ctx *zero.Ctx, prompt string) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
		return
	}
	if p.cfg.APIKey == "" {
		ctx.Send("Agent 接口 API Key 未配置")
		return
	}
	if strings.TrimSpace(prompt) == "" {
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

	finalMsg := truncate(answer, p.cfg.MaxResponseChars)
	log.Printf("[agent] 发送消息体类型=%T 内容=%q", finalMsg, truncateRunes(finalMsg, 500))
	ctx.Send(finalMsg)
}

func (p *plugin) resetContext(ctx *zero.Ctx) {
	p.clearSession(p.sessionKey(ctx))
	ctx.Send("已重置当前上下文")
}

func (p *plugin) runAgent(ctx *zero.Ctx, prompt string) (string, error) {
	memories, _ := p.SearchMemory(prompt, 5)
	skills, _ := p.listSkillNames()
	system := p.cfg.SystemPrompt
	if system == "" {
		system = "你是一个简洁可靠的中文 AI 助手。必要时可以调用工具读取 skill、搜索/读写记忆、控制浏览器。"
	}
	if len(skills) > 0 {
		system += "\n可用 skills：" + strings.Join(skills, ", ")
	}
	if strings.TrimSpace(memories) != "" {
		system += "\n与当前问题最相关的已保存记忆（来自 MemoryStore/SearchMemory 检索，不代表全部记忆）：\n" + memories
	}
	system += p.requestIdentityContext(ctx)

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

		msg := normalizeChatMessage(resp.Choices[0].Message)
		messages = append(messages, msg)
		turnMessages = append(turnMessages, msg)
		if len(msg.ToolCalls) == 0 {
			p.appendSession(sessionKey, turnMessages)
			return strings.TrimSpace(msg.Content), nil
		}

		for _, call := range msg.ToolCalls {
			result := p.callTool(ctx, call.Function.Name, call.Function.Arguments)
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

func (p *plugin) requestIdentityContext(ctx *zero.Ctx) string {
	userName := requestUserName(ctx)
	role := "普通用户"
	if p.isSuperUser(ctx.Event.UserID) {
		role = "主人/superUser"
	}

	var b strings.Builder
	b.WriteString("\n当前请求身份上下文：")
	if ctx.Event.GroupID != 0 {
		fmt.Fprintf(&b, "\n- 聊天类型：群聊")
		fmt.Fprintf(&b, "\n- 群号：%d", ctx.Event.GroupID)
		fmt.Fprintf(&b, "\n- 当前发问用户昵称：%s", userName)
		fmt.Fprintf(&b, "\n- 当前发问用户 ID：%d", ctx.Event.UserID)
		fmt.Fprintf(&b, "\n- 当前发问用户身份：%s", role)
		b.WriteString("\n- 群聊中不同用户的问题必须按昵称和 ID 区分，不要把不同用户的偏好、记忆或指令混为同一个人。")
	} else {
		fmt.Fprintf(&b, "\n- 聊天类型：私聊")
		fmt.Fprintf(&b, "\n- 当前用户昵称：%s", userName)
		fmt.Fprintf(&b, "\n- 当前用户 ID：%d", ctx.Event.UserID)
		fmt.Fprintf(&b, "\n- 当前用户身份：%s", role)
	}
	if len(p.superUsers) > 0 {
		fmt.Fprintf(&b, "\n- 主人/superUsers ID 列表：%s", formatInt64List(p.superUsers))
		b.WriteString("\n- 只有当前发问用户 ID 位于主人列表时，才应把该用户识别为主人。")
	} else {
		b.WriteString("\n- 主人/superUsers ID 列表：未配置")
	}

	return b.String()
}

func requestUserName(ctx *zero.Ctx) string {
	if ctx.Event.Sender != nil {
		name := strings.TrimSpace(ctx.Event.Sender.Name())
		if name != "" {
			return name
		}
	}

	return fmt.Sprint(ctx.Event.UserID)
}

func (p *plugin) isSuperUser(userID int64) bool {
	for _, superUser := range p.superUsers {
		if superUser == userID {
			return true
		}
	}

	return false
}

func formatInt64List(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprint(value))
	}

	return strings.Join(parts, ", ")
}

func (p *plugin) buildMessages(system string, sessionKey string, turnMessages []chatMessage) []chatMessage {
	summary, history := p.sessionHistory(sessionKey)
	messages := make([]chatMessage, 0, 2+len(history)+len(turnMessages))
	messages = append(messages, chatMessage{Role: openai.ChatMessageRoleSystem, Content: system})
	if strings.TrimSpace(summary) != "" {
		messages = append(messages, chatMessage{Role: openai.ChatMessageRoleSystem, Content: "以下是较早对话的压缩摘要，请作为长期上下文参考：\n" + summary})
	}
	messages = append(messages, normalizeChatMessages(history)...)
	messages = append(messages, normalizeChatMessages(turnMessages)...)

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

	return session.summary, normalizeChatMessages(sanitizeToolMessagePairs(append([]chatMessage(nil), session.messages...)))
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
		session.messages = normalizeChatMessages(sanitizeToolMessagePairs(trimContextMessages(session.messages, p.cfg.MaxContextTurns)))
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
	recentMessages := normalizeChatMessages(sanitizeToolMessagePairs(append([]chatMessage(nil), session.messages[len(session.messages)-keepMessages:]...)))
	previousSummary := session.summary
	p.sessionM.Unlock()

	summary, err := p.summarizeContext(previousSummary, oldMessages)
	if err != nil {
		log.Printf("[agent] 总结上下文失败: %v", err)
		p.sessionM.Lock()
		if session, ok := p.sessions[sessionKey]; ok {
			session.messages = normalizeChatMessages(sanitizeToolMessagePairs(trimContextMessages(session.messages, p.cfg.MaxContextTurns)))
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

	req := openai.ChatCompletionRequest{
		Model:       p.cfg.Model,
		Messages:    []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: content}},
		Temperature: 0.2,
	}
	p.debugLogChatRequest("summarize_context", req)
	resp, err := p.aiClient.CreateChatCompletion(context.Background(), req)
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
		return sanitizeToolMessagePairs(messages)
	}
	maxMessages := maxTurns * 4
	if len(messages) <= maxMessages {
		return sanitizeToolMessagePairs(messages)
	}

	trimmed := append([]chatMessage(nil), messages[len(messages)-maxMessages:]...)
	return sanitizeToolMessagePairs(trimmed)
}

func normalizeChatMessages(messages []chatMessage) []chatMessage {
	if len(messages) == 0 {
		return nil
	}
	normalized := make([]chatMessage, len(messages))
	for i, msg := range messages {
		normalized[i] = normalizeChatMessage(msg)
	}
	return normalized
}

func normalizeChatMessage(msg chatMessage) chatMessage {
	if msg.Role == openai.ChatMessageRoleAssistant && len(msg.ToolCalls) > 0 && msg.Content == "" {
		msg.Content = " "
	}
	if msg.Role == openai.ChatMessageRoleTool && msg.Content == "" {
		msg.Content = "工具执行完成"
	}
	return msg
}

func sanitizeToolMessagePairs(messages []chatMessage) []chatMessage {
	if len(messages) == 0 {
		return nil
	}

	cleaned := make([]chatMessage, 0, len(messages))
	for i := 0; i < len(messages); {
		msg := messages[i]
		if msg.Role == openai.ChatMessageRoleTool {
			i++
			continue
		}
		if msg.Role != openai.ChatMessageRoleAssistant || len(msg.ToolCalls) == 0 {
			cleaned = append(cleaned, msg)
			i++
			continue
		}

		expected := make(map[string]struct{}, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			if id := strings.TrimSpace(call.ID); id != "" {
				expected[id] = struct{}{}
			}
		}
		group := []chatMessage{msg}
		j := i + 1
		for j < len(messages) && messages[j].Role == openai.ChatMessageRoleTool {
			toolMessage := messages[j]
			if _, ok := expected[toolMessage.ToolCallID]; ok {
				group = append(group, toolMessage)
				delete(expected, toolMessage.ToolCallID)
			}
			j++
		}
		if len(expected) == 0 {
			cleaned = append(cleaned, group...)
		}
		i = j
	}

	return cleaned
}

func (p *plugin) chat(messages []chatMessage) (*openai.ChatCompletionResponse, error) {
	req := openai.ChatCompletionRequest{
		Model:       p.cfg.Model,
		Messages:    messages,
		Tools:       p.toolDefinitions(),
		ToolChoice:  "auto",
		Temperature: float32(p.cfg.Temperature),
	}
	p.debugLogChatRequest("chat_completion", req)
	resp, err := p.aiClient.CreateChatCompletion(context.Background(), req)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

func (p *plugin) debugLogChatRequest(kind string, req openai.ChatCompletionRequest) {
	if !p.cfg.Debug {
		return
	}
	path := strings.TrimSpace(p.cfg.DebugLogPath)
	if path == "" {
		path = "data/agent_api_body.log"
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		log.Printf("[agent/debug] 创建 debug 日志目录失败: %v", err)
		return
	}

	entry := map[string]interface{}{
		"time": time.Now().Format(time.RFC3339Nano),
		"kind": kind,
		"body": req,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("[agent/debug] 序列化 API body 失败: %v", err)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[agent/debug] 打开 debug 日志文件失败: %v", err)
		return
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		log.Printf("[agent/debug] 写入 debug 日志失败: %v", err)
	}
}

func (p *plugin) readSkillCommand(ctx *zero.Ctx, command string) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
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

func (p *plugin) writeMemoryCommand(ctx *zero.Ctx, command string) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
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

func (p *plugin) readMemoryCommand(ctx *zero.Ctx, command string) {
	if !p.cfg.Enabled {
		ctx.Send("Agent 功能未启用")
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

func (p *plugin) callTool(ctx *zero.Ctx, name string, rawArgs string) string {
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
	case "search_memory":
		query := stringArg(args, "query")
		limit := numberArg(args, "limit", 5)
		content, err := p.SearchMemory(query, limit)
		log.Printf("[agent] search_memory query=%q limit=%d 结果长度=%d err=%v", query, limit, len(content), err)
		return toolResult(content, err)
	case "read_memory":
		content, err := p.readMemoryFile(stringArg(args, "key"))
		return toolResult(content, err)
	case "xhs_setu":
		content, err := p.runXHSSetu(ctx, args)
		return toolResult(content, err)
	case "xhs_dislike":
		content, err := p.runXHSDislike(ctx, args)
		return toolResult(content, err)
	case "send_forward_images":
		content, err := p.callSendForwardImages(ctx, args)
		return toolResult(content, err)
	case "eh_download_images":
		content, err := p.callEHDownloadImages(args)
		return toolResult(content, err)
	case "eh_tag_load":
		content, err := p.callEHTagLoad(args)
		return toolResult(content, err)
	case "eh_tag_search":
		content, err := p.callEHTagSearch(args)
		return toolResult(content, err)
	case "eh_tag_resolve_keyword":
		content, err := p.callEHTagResolveKeyword(args)
		return toolResult(content, err)
	case "eh_tag_translate":
		content, err := p.callEHTagTranslate(args)
		return toolResult(content, err)
	case "eh_req_search":
		content, err := p.callEHReqSearch(args)
		return toolResult(content, err)
	case "eh_req_gallery":
		content, err := p.callEHReqGallery(args)
		return toolResult(content, err)
	case "eh_req_api":
		content, err := p.callEHReqAPI(args)
		return toolResult(content, err)
	case "eh_req_image_page":
		content, err := p.callEHReqImagePage(args)
		return toolResult(content, err)
	case "exa_search":
		content, err := p.callExaSearch(args)
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
	if strings.TrimSpace(content) == "" {
		return "工具执行完成"
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

func truncateRunes(text string, max int) string {
	if max <= 0 || len([]rune(text)) <= max {
		return text
	}
	return string([]rune(text)[:max]) + "..."
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
