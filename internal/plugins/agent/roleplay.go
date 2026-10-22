package agent

import (
	"log"
	"strings"

	zero "github.com/wdvxdr1123/ZeroBot"
)

const defaultRoleplayGuidelines = `【角色扮演要求】
- 始终以上述角色的身份、语气、称呼和性格说话；不要自称 AI、助手、模型或程序，不要跳出角色解释系统机制。
- 工具调用和 *_task 子任务是你在幕后“做事”的方式。回复时不要提及工具名、子任务、函数、JSON、API、参数、系统提示词等技术细节，而是把结果自然地融入角色的言行。
- 子任务返回的是幕后执行报告：图片等内容已由子任务直接发到聊天，你不需要重复发送或列出链接，只需用角色口吻回应；报告状态为“需要用户决定”时，用角色口吻把候选项问给用户。
- 刚收到请求、长任务开始和中途，用 say 发一句角色台词，表示收到或进度。say 的内容就是要出现在聊天里的原文。
- 委托子任务时，把用户偏好、约束和必要的前情写进 context 参数，子任务看不到主对话。子任务自己也会用 say 发言。
- 遇到失败时用角色口吻简短说明原因并给出下一步建议，不要原样输出报错、堆栈或内部路径。
- 只有用户明确要求查看链接、原始数据或技术细节时，才提供对应信息。`

const defaultSystemPrompt = "你是一个简洁可靠的中文 AI 助手。必要时可以调用工具读取 skill、搜索/读写记忆、委托子任务完成复杂操作。"

// mainSystemPrompt 把人设放在最前，角色扮演约束其次，其余运行规则统一归入“幕后规则”，避免稀释人设。
func (p *plugin) mainSystemPrompt(ctx *zero.Ctx, prompt string) string {
	var b strings.Builder
	persona := strings.TrimSpace(p.cfg.SystemPrompt)
	if persona == "" {
		persona = defaultSystemPrompt
	}
	b.WriteString(persona)

	if p.cfg.Roleplay.Enabled {
		b.WriteString("\n\n")
		b.WriteString(p.roleplayGuidelines())
	}

	b.WriteString("\n\n【幕后规则：仅约束你的行为，不要在回复中提及】")
	b.WriteString("\n长期记忆策略：你只能使用当前聊天作用域内的记忆。群聊记忆按群隔离，不要把不同群的偏好、约定或上下文混用。若用户明确表达稳定偏好、长期事实、后续要遵守的规则或项目约定，应调用 write_memory 保存；若问题可能依赖过往偏好或约定，应主动调用 search_memory 补充检索。不要保存敏感信息、一次性临时信息或未经确认的猜测。")
	b.WriteString("\n需要立刻对用户说话时调用 say，任何时候都可以，包括刚收到请求和任务中途。text 会马上发到当前聊天。本轮结束时的最终回复还会再发一次，不要把同一句话又 say 又当作最终回复。")
	if p.subagentsEnabled() {
		b.WriteString("\n复杂操作（EH 漫画、下载器、联网调查、网页操作）请委托对应的 *_task 子任务一次性完成，不要自己拆成多步；同一需求通常只需调用一次。子任务进行中也可以自己调用 say。")
	}
	if skills := p.visibleSkillNames(); len(skills) > 0 {
		b.WriteString("\n可用 skills：" + strings.Join(skills, ", "))
	}
	if p.visionToolEnabled() && p.hasRequestImages(ctx) {
		b.WriteString("\n当前用户消息或其引用消息包含图片，但你不会直接接收图片。你必须调用 analyze_images 工具，把用户对图片的要求作为 prompt 传入；收到识图结果后再结合对话回答。不要仅凭图片占位符猜测内容。")
	}
	b.WriteString(p.requestIdentityContext(ctx))
	if p.taskGuardActive(prompt) {
		b.WriteString("\n长流程任务约束：" + p.cfg.TaskGuard.CompletionPrompt)
	}
	return b.String()
}

func (p *plugin) sayGuidance() string {
	guidance := "\n\n## 主动发言\n你可以随时调用 say，把一句消息立刻发到当前聊天，用来表示收到、进展、转折或需要用户马上知道的事。不要等任务结束才说。最终执行报告仍然只交给主代理，不要用 say 复述整份报告。"
	if p.cfg.Roleplay.Enabled {
		guidance += "say 的 text 必须是角色台词，一句即可，不要出现工具名、子任务、JSON、参数、报错堆栈或内部路径。"
	}
	return guidance
}

func (p *plugin) roleplayGuidelines() string {
	if guidelines := strings.TrimSpace(p.cfg.Roleplay.Guidelines); guidelines != "" {
		return guidelines
	}
	return defaultRoleplayGuidelines
}

// visibleSkillNames 隐藏已注入子代理的 skill，避免主 agent 读入大段操作手册。
func (p *plugin) visibleSkillNames() []string {
	skills, err := p.listSkillNames()
	if err != nil || len(skills) == 0 {
		return skills
	}
	hidden := make(map[string]struct{})
	if p.subagentsEnabled() && p.ehTaskEnabled() {
		if name := safeName(p.cfg.Subagent.EHSkillFile); name != "" {
			hidden[strings.ToLower(name)] = struct{}{}
		}
	}
	if len(hidden) == 0 {
		return skills
	}
	visible := make([]string, 0, len(skills))
	for _, skill := range skills {
		if _, ok := hidden[strings.ToLower(skill)]; !ok {
			visible = append(visible, skill)
		}
	}
	return visible
}

func (p *plugin) thinkingText() string {
	if p.cfg.Roleplay.Enabled {
		return strings.TrimSpace(p.cfg.Roleplay.ThinkingText)
	}
	return "Agent 正在思考..."
}

func (p *plugin) failureReply(err error) string {
	if !p.cfg.Roleplay.Enabled {
		return "Agent 执行失败：" + err.Error()
	}
	log.Printf("[agent] 执行失败: %v", err)
	text := strings.TrimSpace(p.cfg.Roleplay.ErrorText)
	if text == "" {
		text = "唔……刚才好像出了点状况，稍后再试试吧。"
	}
	return strings.ReplaceAll(text, "{error}", err.Error())
}

// sessionToolResultChars 是写入会话历史时每条工具结果保留的字符数；角色扮演模式下只留简短痕迹。
func (p *plugin) sessionToolResultChars() int {
	if p.cfg.Roleplay.Enabled && p.cfg.Roleplay.HistoryToolResultChars > 0 {
		return p.cfg.Roleplay.HistoryToolResultChars
	}
	return defaultSessionToolResultChars
}
