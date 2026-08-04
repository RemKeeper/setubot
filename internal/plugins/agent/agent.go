package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
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
	"github.com/wdvxdr1123/ZeroBot/message"
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
	browserM   sync.Mutex
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
		return zero.OnlyToMe(ctx) && p.hasAgentInput(ctx)
	}

	return p.hasAgentInput(ctx)
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
	ctx.NoTimeout()
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
		return command, command != "" || (p.cfg.Vision.Enabled && hasMessageImages(ctx.Event.Message)) || replyMessageID(ctx.Event.Message) != ""
	}

	return text, text != "" || (p.cfg.Vision.Enabled && hasMessageImages(ctx.Event.Message)) || replyMessageID(ctx.Event.Message) != ""
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
	ctx.Send("Agent 插件命令：\n1. 群聊：@机器人 <问题>\n2. 私聊：直接发送 <问题>\n3. 重置上下文\n4. skill 读取 <文件名>\n5. memory 写入 <键>: <内容>\n6. memory 读取 <键>\n7. memory 列表\n说明：群聊记忆按群隔离，不同群不会互相污染；agent 会在回答后自动抽取少量长期记忆。\n小红书：来点涩图 / 来N张涩图 / 来点关键词涩图 / 不喜欢\n浏览器工具由 agent 自动调用：goto/click/type/html/screenshot/evaluate/scroll")
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
	if !p.cfg.Vision.Enabled && strings.TrimSpace(prompt) == "" {
		if replyID := replyMessageID(ctx.Event.Message); replyID != "" {
			replied := ctx.GetMessage(replyID, true)
			if strings.TrimSpace(messagePlainText(replied.Elements)) == "" && hasMessageImages(replied.Elements) {
				ctx.Send("Agent 识图功能未启用，请在 agent.vision.enabled 中开启")
				return
			}
		}
	}
	if strings.TrimSpace(prompt) == "" && !(p.cfg.Vision.Enabled && hasMessageImages(ctx.Event.Message)) && replyMessageID(ctx.Event.Message) == "" {
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
	sessionKey := p.sessionKey(ctx)
	skills, _ := p.listSkillNames()
	system := p.cfg.SystemPrompt
	if system == "" {
		system = "你是一个简洁可靠的中文 AI 助手。必要时可以调用工具读取 skill、搜索/读写记忆、控制浏览器。"
	}
	system += "\n长期记忆策略：你只能使用当前聊天作用域内的记忆。群聊记忆按群隔离，不要把不同群的偏好、约定或上下文混用。若用户明确表达稳定偏好、长期事实、后续要遵守的规则或项目约定，应调用 write_memory 保存；若问题可能依赖过往偏好或约定，应主动调用 search_memory 补充检索。不要保存敏感信息、一次性临时信息或未经确认的猜测。"
	if len(skills) > 0 {
		system += "\n可用 skills：" + strings.Join(skills, ", ")
	}
	system += p.requestIdentityContext(ctx)
	taskGuardActive := p.taskGuardActive(prompt)
	maxToolRounds := p.cfg.MaxToolRounds
	if taskGuardActive {
		system += "\n长流程任务约束：" + p.cfg.TaskGuard.CompletionPrompt
		if p.cfg.TaskGuard.MaxSteps > maxToolRounds {
			maxToolRounds = p.cfg.TaskGuard.MaxSteps
		}
	}

	turnMessages := []chatMessage{p.userChatMessage(ctx, prompt)}
	messages := p.buildMessages(system, sessionKey, turnMessages)

	for i := 0; i <= maxToolRounds; i++ {
		resp, err := p.chat(messages)
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("模型未返回结果")
		}

		msg := normalizeChatMessage(resp.Choices[0].Message)
		p.debugLogModelMessage(i, msg)
		messages = append(messages, msg)
		turnMessages = append(turnMessages, msg)
		if len(msg.ToolCalls) == 0 {
			p.appendSession(sessionKey, turnMessages)
			answer := strings.TrimSpace(msg.Content)
			relatedMemories, _ := p.SearchScopedMemory(p.memoryQuery(ctx, prompt, sessionKey), 8, p.memoryAllowedPrefixes(ctx))
			p.maybeExtractMemory(ctx, prompt, answer, relatedMemories)
			return answer, nil
		}

		for _, call := range msg.ToolCalls {
			p.debugLogf("[agent/tool] round=%d call_id=%s name=%s args=%s", i, call.ID, call.Function.Name, truncateRunes(call.Function.Arguments, 4000))
			result := p.compactToolResult(call.Function.Name, p.callTool(ctx, call.Function.Name, call.Function.Arguments), p.cfg.MaxToolResultChars)
			p.debugLogf("[agent/tool] round=%d call_id=%s name=%s result=%s", i, call.ID, call.Function.Name, truncateRunes(result, 6000))
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

func (p *plugin) hasAgentInput(ctx *zero.Ctx) bool {
	return strings.TrimSpace(ctx.ExtractPlainText()) != "" || (p.cfg.Vision.Enabled && hasMessageImages(ctx.Event.Message)) || replyMessageID(ctx.Event.Message) != ""
}

func (p *plugin) userChatMessage(ctx *zero.Ctx, prompt string) chatMessage {
	parts := make([]openai.ChatMessagePart, 0)
	if replyID := replyMessageID(ctx.Event.Message); replyID != "" {
		replied := ctx.GetMessage(replyID, true)
		repliedParts := p.messageParts(replied.Elements)
		if len(repliedParts) > 0 {
			parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: "以下是用户引用的消息：\n"})
			parts = append(parts, repliedParts...)
			parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: "\n以下是用户当前发送的消息：\n"})
		}
	}
	parts = append(parts, p.messagePartsWithPrompt(ctx.Event.Message, prompt)...)
	if len(parts) == 0 {
		return chatMessage{Role: openai.ChatMessageRoleUser, Content: prompt}
	}
	if strings.TrimSpace(multiContentText(parts)) == "" && !multiContentHasImage(parts) {
		return chatMessage{Role: openai.ChatMessageRoleUser, Content: prompt}
	}

	return chatMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts}
}

func (p *plugin) messageParts(msg message.Message) []openai.ChatMessagePart {
	return p.messagePartsWithPrompt(msg, "")
}

func (p *plugin) messagePartsWithPrompt(msg message.Message, prompt string) []openai.ChatMessagePart {
	if strings.TrimSpace(prompt) == "" || strings.TrimSpace(messagePlainText(msg)) == strings.TrimSpace(prompt) {
		return p.messagePartsRaw(msg)
	}

	parts := p.messagePartsRaw(msg)
	result := make([]openai.ChatMessagePart, 0, len(parts))
	textAdded := false
	for _, part := range parts {
		if part.Type == openai.ChatMessagePartTypeText {
			if !textAdded {
				result = append(result, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: prompt})
				textAdded = true
			}
			continue
		}
		result = append(result, part)
	}
	if !textAdded {
		result = append(result, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: prompt})
	}
	return result
}

func (p *plugin) messagePartsRaw(msg message.Message) []openai.ChatMessagePart {
	parts := make([]openai.ChatMessagePart, 0, len(msg))
	for _, segment := range msg {
		switch segment.Type {
		case "text":
			text := segment.Data["text"]
			if text != "" {
				parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: text})
			}
		case "image":
			if !p.cfg.Vision.Enabled {
				continue
			}
			source := agentImageSource(segment)
			if source != "" {
				parts = append(parts, openai.ChatMessagePart{
					Type:     openai.ChatMessagePartTypeImageURL,
					ImageURL: &openai.ChatMessageImageURL{URL: source, Detail: p.visionDetail()},
				})
			}
		}
	}
	return parts
}

func (p *plugin) visionDetail() openai.ImageURLDetail {
	switch strings.ToLower(strings.TrimSpace(p.cfg.Vision.Detail)) {
	case "low":
		return openai.ImageURLDetailLow
	case "high":
		return openai.ImageURLDetailHigh
	default:
		return openai.ImageURLDetailAuto
	}
}

func messagePlainText(msg message.Message) string {
	var b strings.Builder
	for _, segment := range msg {
		if segment.Type == "text" {
			b.WriteString(segment.Data["text"])
		}
	}
	return b.String()
}

func hasMessageImages(msg message.Message) bool {
	for _, segment := range msg {
		if segment.Type == "image" && agentImageSource(segment) != "" {
			return true
		}
	}
	return false
}

func replyMessageID(msg message.Message) string {
	for _, segment := range msg {
		if segment.Type == "reply" {
			return strings.TrimSpace(segment.Data["id"])
		}
	}
	return ""
}

func multiContentText(parts []openai.ChatMessagePart) string {
	var b strings.Builder
	for _, part := range parts {
		if part.Type == openai.ChatMessagePartTypeText {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func multiContentHasImage(parts []openai.ChatMessagePart) bool {
	for _, part := range parts {
		if part.Type == openai.ChatMessagePartTypeImageURL && part.ImageURL != nil && strings.TrimSpace(part.ImageURL.URL) != "" {
			return true
		}
	}
	return false
}

func agentImageSource(segment message.Segment) string {
	for _, key := range []string{"url", "file"} {
		source := strings.TrimSpace(segment.Data[key])
		source = strings.ReplaceAll(source, "&amp;", "&")
		source = html.UnescapeString(source)
		if strings.HasPrefix(source, "base64://") {
			return "data:image/jpeg;base64," + strings.TrimPrefix(source, "base64://")
		}
		if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") ||
			strings.HasPrefix(source, "data:image/") {
			return source
		}
	}
	return ""
}

func (p *plugin) taskGuardActive(prompt string) bool {
	if !p.cfg.TaskGuard.Enabled {
		return false
	}
	prompt = strings.ToLower(prompt)
	for _, keyword := range p.cfg.TaskGuard.LongTaskKeywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && strings.Contains(prompt, keyword) {
			return true
		}
	}

	return false
}

func (p *plugin) sessionKey(ctx *zero.Ctx) string {
	if ctx.Event.GroupID != 0 {
		return fmt.Sprintf("group:%d:user:%d", ctx.Event.GroupID, ctx.Event.UserID)
	}

	return fmt.Sprintf("private:%d", ctx.Event.UserID)
}

func (p *plugin) memoryScopePrefix(ctx *zero.Ctx) string {
	if ctx.Event.GroupID != 0 {
		return fmt.Sprintf("group_%d", ctx.Event.GroupID)
	}
	return fmt.Sprintf("private_%d", ctx.Event.UserID)
}

func (p *plugin) userMemoryScopePrefix(ctx *zero.Ctx) string {
	return fmt.Sprintf("user_%d", ctx.Event.UserID)
}

func (p *plugin) memoryAllowedPrefixes(ctx *zero.Ctx) []string {
	prefixes := []string{p.memoryScopePrefix(ctx), p.userMemoryScopePrefix(ctx)}
	return dedupeStrings(prefixes)
}

func (p *plugin) memoryQuery(ctx *zero.Ctx, prompt string, sessionKey string) string {
	summary, history := p.sessionHistory(sessionKey)
	parts := []string{
		prompt,
		fmt.Sprintf("用户ID %d", ctx.Event.UserID),
		requestUserName(ctx),
	}
	if ctx.Event.GroupID != 0 {
		parts = append(parts, fmt.Sprintf("群号 %d", ctx.Event.GroupID))
	}
	if strings.TrimSpace(summary) != "" {
		parts = append(parts, summary)
	}
	for _, msg := range recentUserMessages(history, 3) {
		parts = append(parts, msg)
	}
	return strings.Join(dedupeStrings(parts), "\n")
}

func recentUserMessages(messages []chatMessage, limit int) []string {
	if limit <= 0 {
		return nil
	}
	result := make([]string, 0, limit)
	for i := len(messages) - 1; i >= 0 && len(result) < limit; i-- {
		msg := messages[i]
		if msg.Role != openai.ChatMessageRoleUser {
			continue
		}
		content := strings.TrimSpace(chatMessageDisplayText(msg))
		if content != "" {
			result = append(result, content)
		}
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
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

	return fitMessagesToCharBudget(messages, p.cfg.MaxContextChars)
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
	messages = compactMessagesForSession(messages)
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

func (p *plugin) maybeExtractMemory(ctx *zero.Ctx, userPrompt string, assistantAnswer string, relatedMemories string) {
	if p.cfg.APIKey == "" || strings.TrimSpace(userPrompt) == "" || strings.TrimSpace(assistantAnswer) == "" {
		return
	}
	result, err := p.extractMemory(ctx, userPrompt, assistantAnswer, relatedMemories)
	if err != nil {
		log.Printf("[agent] 自动抽取记忆失败: %v", err)
		return
	}
	if !result.ShouldWrite {
		return
	}
	key := strings.TrimSpace(result.Key)
	content := strings.TrimSpace(result.Content)
	if key == "" || content == "" {
		return
	}
	if err := p.writeScopedMemoryFile(p.memoryScopePrefix(ctx), key, content); err != nil {
		log.Printf("[agent] 自动写入记忆失败: %v", err)
	}
}

func (p *plugin) extractMemory(ctx *zero.Ctx, userPrompt string, assistantAnswer string, relatedMemories string) (memoryExtractionResult, error) {
	identity := p.requestIdentityContext(ctx)
	content := "请判断以下一轮对话是否包含值得写入长期记忆的信息。只保存稳定、未来仍有用的信息，例如用户明确偏好、长期事实、后续要遵守的规则、项目约定、持续任务上下文。不要保存敏感信息、账号密钥、Cookie、一次性闲聊、临时请求、未经确认的推测。群聊记忆必须只适用于当前群，不要泛化到其他群。\n\n"
	content += "必须只输出一个 JSON 对象，不要使用 Markdown，不要附加解释。格式：{\"should_write\": boolean, \"key\": string, \"content\": string, \"reason\": string}\n"
	content += "key 使用简短中文或英文名，不要包含路径。content 用中文短句，必须包含适用范围，例如当前群号或当前用户 ID。若已有相关记忆已经覆盖本轮信息，应输出 should_write=false。\n\n"
	content += identity
	if strings.TrimSpace(relatedMemories) != "" {
		content += "\n\n已有相关记忆：\n" + truncateRunes(relatedMemories, 2000)
	}
	content += "\n\n用户消息：\n" + truncateRunes(userPrompt, 2000)
	content += "\n\n助手回答：\n" + truncateRunes(assistantAnswer, 2000)

	req := openai.ChatCompletionRequest{
		Model:       p.cfg.Model,
		Messages:    []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: content}},
		Temperature: 0,
	}
	p.debugLogChatRequest("extract_memory", req)
	resp, err := p.aiClient.CreateChatCompletion(context.Background(), req)
	if err != nil {
		return memoryExtractionResult{}, err
	}
	if len(resp.Choices) == 0 {
		return memoryExtractionResult{}, fmt.Errorf("记忆抽取模型未返回结果")
	}

	raw := strings.TrimSpace(resp.Choices[0].Message.Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var result memoryExtractionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return memoryExtractionResult{}, err
	}
	return result, nil
}

func renderMessagesForSummary(messages []chatMessage) string {
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		content := strings.TrimSpace(truncateRunes(chatMessageDisplayText(msg), 4000))
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

func compactMessagesForSession(messages []chatMessage) []chatMessage {
	compacted := append([]chatMessage(nil), messages...)
	for i := range compacted {
		if compacted[i].Role == openai.ChatMessageRoleTool {
			compacted[i].Content = truncateRunes(compacted[i].Content, 4000)
		}
		if compacted[i].ReasoningContent != "" {
			compacted[i].ReasoningContent = ""
		}
	}
	return normalizeChatMessages(compacted)
}

func fitMessagesToCharBudget(messages []chatMessage, maxChars int) []chatMessage {
	if maxChars <= 0 || chatMessagesChars(messages) <= maxChars {
		return normalizeChatMessages(messages)
	}
	if len(messages) == 0 {
		return nil
	}

	first := messages[0]
	remainingBudget := maxChars - len([]rune(first.Content))
	if remainingBudget <= 0 {
		first.Content = truncateRunes(first.Content, maxChars)
		return []chatMessage{normalizeChatMessage(first)}
	}

	kept := make([]chatMessage, 0, len(messages))
	used := 0
	for i := len(messages) - 1; i >= 1; i-- {
		msg := messages[i]
		size := chatMessageChars(msg)
		if size > remainingBudget && len(kept) == 0 {
			msg.Content = truncateRunes(msg.Content, remainingBudget)
			size = chatMessageChars(msg)
		}
		if used+size > remainingBudget {
			continue
		}
		kept = append(kept, msg)
		used += size
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	kept = sanitizeToolMessagePairs(kept)
	result := append([]chatMessage{first}, kept...)
	return normalizeChatMessages(result)
}

func chatMessagesChars(messages []chatMessage) int {
	total := 0
	for _, msg := range messages {
		total += chatMessageChars(msg)
	}
	return total
}

func chatMessageChars(msg chatMessage) int {
	total := len([]rune(msg.Content)) + len([]rune(msg.ReasoningContent)) + 32
	for _, part := range msg.MultiContent {
		switch part.Type {
		case openai.ChatMessagePartTypeText:
			total += len([]rune(part.Text))
		case openai.ChatMessagePartTypeImageURL:
			total += 256
		}
	}
	for _, call := range msg.ToolCalls {
		total += len([]rune(call.Function.Name)) + len([]rune(call.Function.Arguments)) + 32
	}
	return total
}

func chatMessageDisplayText(msg chatMessage) string {
	if strings.TrimSpace(msg.Content) != "" {
		return msg.Content
	}
	if len(msg.MultiContent) == 0 {
		return ""
	}

	var b strings.Builder
	for _, part := range msg.MultiContent {
		switch part.Type {
		case openai.ChatMessagePartTypeText:
			b.WriteString(part.Text)
		case openai.ChatMessagePartTypeImageURL:
			b.WriteString("[图片]")
		}
	}
	return b.String()
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
	return p.chatWithTools("chat_completion", fitMessagesToCharBudget(messages, p.cfg.MaxContextChars), p.toolDefinitions())
}

func (p *plugin) chatWithTools(kind string, messages []chatMessage, tools []openai.Tool) (*openai.ChatCompletionResponse, error) {
	req := openai.ChatCompletionRequest{
		Model:       p.cfg.Model,
		Messages:    messages,
		Tools:       tools,
		ToolChoice:  "auto",
		Temperature: float32(p.cfg.Temperature),
	}
	p.debugLogChatRequest(kind, req)
	resp, err := p.aiClient.CreateChatCompletion(context.Background(), req)
	if err != nil {
		p.debugLogf("[agent/model] chat_completion error=%v", err)
		return nil, err
	}

	return &resp, nil
}

func (p *plugin) debugLogModelMessage(round int, msg chatMessage) {
	if !p.cfg.Debug {
		return
	}

	p.debugLogf("[agent/model] round=%d role=%s reasoning=%s content=%s tool_calls=%s", round, msg.Role,
		truncateRunes(msg.ReasoningContent, 6000), truncateRunes(msg.Content, 6000), formatToolCalls(msg.ToolCalls))
}

func formatToolCalls(calls []openai.ToolCall) string {
	if len(calls) == 0 {
		return "[]"
	}

	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, fmt.Sprintf("%s:%s(%s)", call.ID, call.Function.Name, truncateRunes(call.Function.Arguments, 2000)))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

func (p *plugin) debugLogf(format string, args ...interface{}) {
	if !p.cfg.Debug {
		return
	}
	log.Printf(format, args...)
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
	if err := p.writeScopedMemoryFile(p.memoryScopePrefix(ctx), matches[1], matches[2]); err != nil {
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
	content, err := p.readScopedMemoryFile(p.memoryScopePrefix(ctx), matches[1])
	if err != nil {
		ctx.Send(fmt.Sprintf("读取记忆失败：%v", err))
		return
	}
	ctx.Send(truncate(content, p.cfg.MaxResponseChars))
}

func (p *plugin) listMemory(ctx *zero.Ctx) {
	names, err := p.listScopedMemoryNames(p.memoryAllowedPrefixes(ctx))
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
		err := p.writeScopedMemoryFile(p.memoryScopePrefix(ctx), stringArg(args, "key"), stringArg(args, "content"))
		return toolResult("已写入记忆", err)
	case "search_memory":
		query := stringArg(args, "query")
		limit := numberArg(args, "limit", 5)
		content, err := p.SearchScopedMemory(p.memoryQuery(ctx, query, p.sessionKey(ctx)), limit, p.memoryAllowedPrefixes(ctx))
		log.Printf("[agent] search_memory query=%q limit=%d 结果长度=%d err=%v", query, limit, len(content), err)
		return toolResult(content, err)
	case "read_memory":
		content, err := p.readScopedMemoryFile(p.memoryScopePrefix(ctx), stringArg(args, "key"))
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
	case "send_forward_images_batches":
		content, err := p.callSendForwardImageBatches(ctx, args)
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
	case "browser_task":
		content, err := p.runBrowserSubagent(stringArg(args, "goal"), stringArg(args, "start_url"))
		return toolResult(content, err)
	default:
		return "未知工具：" + name
	}
}

func (p *plugin) compactToolResult(name string, content string, maxChars int) string {
	if maxChars <= 0 || len([]rune(content)) <= maxChars {
		return content
	}
	originalChars := len([]rune(content))
	return fmt.Sprintf("%s\n[工具结果已截断：tool=%s，原始字符数=%d，上限=%d]", truncateRunes(content, maxChars), name, originalChars, maxChars)
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
