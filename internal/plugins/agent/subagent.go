package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	openai "github.com/sashabaranov/go-openai"
	zero "github.com/wdvxdr1123/ZeroBot"
)

const subagentBaseSystem = `你是幕后执行子任务的工作代理。除了 say 发出的消息，你不把执行过程写给用户。
- 只完成给定任务，按需调用工具，持续推进直到完成、确认无法完成，或遇到必须由用户决定的分歧。
- 需要让用户马上知道收到、进展、转折或等待时，立刻调用 say。不要攒到结束才说。
- 只依据工具真实返回的内容行动和汇报，不要编造结果；工具报错时可以合理重试或换路径，但不要无限重试。
- 中间工具结果只用于本次任务，不会进入主对话。
- 结束时不要再调用工具，直接输出一份简洁的中文执行报告，格式如下：
状态：成功 / 部分完成 / 失败 / 需要用户决定
已完成：实际执行了哪些动作（例如已向聊天发送了几张图片、已添加了哪个下载任务）
结果：主代理回复用户所需的关键事实（标题、标签、数量、文件名、结论等）；不要附带大段原始数据、Cookie、内部路径或无关链接
待决定：仅当需要用户选择时列出候选项，否则省略本行
- 报告会交给主代理用角色口吻转述给用户，因此只写事实，不写客套话或角色台词。`

// subagentSpec 描述一个幕后子任务代理：独立 system prompt、工具子集与执行器。
type subagentSpec struct {
	kind           string
	system         string
	tools          []openai.Tool
	maxSteps       int
	maxResultChars int
	// toolResultChars 为 0 时使用 agent.maxToolResultChars。
	toolResultChars int
	execute         func(name string, rawArgs string) string
}

func (p *plugin) subagentsEnabled() bool {
	return p.cfg.Subagent.IsEnabled()
}

func (p *plugin) ehTaskEnabled() bool {
	return p.cfg.EHReq.Enabled || p.cfg.EHTag.Enabled
}

func (p *plugin) subagentModel() string {
	if model := strings.TrimSpace(p.cfg.Subagent.Model); model != "" {
		return model
	}
	return p.currentModel()
}

func (p *plugin) subagentTemperature() float32 {
	if p.cfg.Subagent.Temperature > 0 {
		return float32(p.cfg.Subagent.Temperature)
	}
	return 0.2
}

// runSubagent 在独立的消息列表里自循环调用工具，只把最终执行报告返回给调用方。
func (p *plugin) runSubagent(spec subagentSpec, goal string) (string, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "", fmt.Errorf("子任务目标不能为空")
	}
	maxSteps := spec.maxSteps
	if maxSteps <= 0 {
		maxSteps = p.cfg.Subagent.MaxSteps
	}
	if maxSteps <= 0 {
		maxSteps = 16
	}
	maxResultChars := spec.maxResultChars
	if maxResultChars <= 0 {
		maxResultChars = p.cfg.Subagent.MaxResultChars
	}
	toolResultChars := spec.toolResultChars
	if toolResultChars <= 0 {
		toolResultChars = p.cfg.MaxToolResultChars
	}
	allowed := make(map[string]struct{}, len(spec.tools))
	for _, tool := range spec.tools {
		if tool.Function != nil {
			allowed[tool.Function.Name] = struct{}{}
		}
	}

	system := spec.system
	if _, ok := allowed["say"]; ok {
		system += p.sayGuidance()
	}
	// 目标同时写入 system，避免上下文裁剪掉首条 user 消息后子代理丢失任务。
	messages := []chatMessage{
		{Role: openai.ChatMessageRoleSystem, Content: system + "\n\n## 本次任务\n" + goal},
		{Role: openai.ChatMessageRoleUser, Content: goal},
	}
	log.Printf("[agent/subagent] kind=%s 开始执行，最大步数=%d", spec.kind, maxSteps)

	for step := 0; step < maxSteps; step++ {
		messages = fitMessagesToCharBudget(messages, p.cfg.MaxContextChars)
		resp, err := p.chatWithToolsOptions(spec.kind, p.subagentModel(), p.subagentTemperature(), messages, spec.tools, "auto")
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("子任务 %s 未返回结果", spec.kind)
		}

		msg := normalizeChatMessage(resp.Choices[0].Message)
		p.debugLogf("[agent/subagent] kind=%s step=%d content=%s tool_calls=%s", spec.kind, step, truncateRunes(msg.Content, 4000), formatToolCalls(msg.ToolCalls))
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			log.Printf("[agent/subagent] kind=%s 完成，步数=%d", spec.kind, step+1)
			return p.subagentReport(spec.kind, msg.Content, maxResultChars), nil
		}

		for _, call := range msg.ToolCalls {
			var result string
			if _, ok := allowed[call.Function.Name]; !ok {
				result = "未知工具：" + call.Function.Name
			} else {
				result = spec.execute(call.Function.Name, call.Function.Arguments)
			}
			result = p.compactToolResult(call.Function.Name, result, toolResultChars)
			p.debugLogf("[agent/subagent] kind=%s step=%d name=%s result=%s", spec.kind, step, call.Function.Name, truncateRunes(result, 4000))
			messages = append(messages, chatMessage{Role: openai.ChatMessageRoleTool, ToolCallID: call.ID, Content: result})
		}
	}

	log.Printf("[agent/subagent] kind=%s 达到最大步数 %d，请求阶段性报告", spec.kind, maxSteps)
	messages = append(messages, chatMessage{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("已达到最大步数 %d，请停止调用工具，按报告格式汇报当前进度：已完成的部分、未完成的部分及原因。", maxSteps)})
	messages = fitMessagesToCharBudget(messages, p.cfg.MaxContextChars)
	resp, err := p.chatWithToolsOptions(spec.kind, p.subagentModel(), p.subagentTemperature(), messages, spec.tools, "none")
	if err != nil {
		return "", fmt.Errorf("子任务 %s 超过最大步数 %d", spec.kind, maxSteps)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("子任务 %s 超过最大步数 %d", spec.kind, maxSteps)
	}
	return p.subagentReport(spec.kind, resp.Choices[0].Message.Content, maxResultChars), nil
}

func (p *plugin) subagentReport(kind string, content string, maxChars int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		content = "状态：成功\n已完成：子任务已结束，但未提供详细报告"
	}
	return p.compactToolResult(kind, content, maxChars)
}

func buildSubagentGoal(goal string, extra string) string {
	goal = strings.TrimSpace(goal)
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return goal
	}
	return goal + "\n\n补充上下文（来自主对话，可能包含用户偏好与约束）：\n" + extra
}

// toolExecutor 让子代理复用主 agent 的工具实现，ctx 保留当前聊天以便直接发送图片。
func (p *plugin) toolExecutor(ctx *zero.Ctx) func(name string, rawArgs string) string {
	return func(name string, rawArgs string) string {
		return p.callTool(ctx, name, rawArgs)
	}
}

func (p *plugin) taskToolDefinitions() []openai.Tool {
	tools := make([]openai.Tool, 0, 4)
	if p.ehTaskEnabled() {
		tools = append(tools, functionTool("eh_task", "委托幕后子任务完成 E-Hentai/ExHentai 相关流程：中文标签映射、搜索漫画、获取详情与标签、解析正文图片、下载并合并转发到当前聊天。子任务会直接把图片发到聊天，只返回简短的执行报告。", taskToolProperties(
			"完整描述要做的事和成功条件，例如：搜索“中文 全彩”的漫画，挑评分最高的一本，发送前 5 页正文图片",
		), []string{"goal"}))
	}
	if p.cfg.QBittorrent.Enabled {
		tools = append(tools, functionTool("torrent_task", "委托幕后子任务操作 qBittorrent 下载器：添加磁力/种子、筛除广告文件、查看进度、暂停/恢复/删除、移动目录、调整分享率与 tracker。只返回简短的执行报告；权限由工具强制校验。", taskToolProperties(
			"完整描述要做的事，需原样包含用户给出的磁力链接或种子 URL、任务名或 hash",
		), []string{"goal"}))
	}
	if p.cfg.Exa.Enabled {
		tools = append(tools, functionTool("research_task", "委托幕后子任务联网搜索并整理资料，适合实时信息、新闻、需要来源的问题。只返回结论要点和少量关键来源。", taskToolProperties(
			"要调查的问题、关注点和期望的结论形式",
		), []string{"goal"}))
	}
	if p.cfg.Browser.Enabled {
		tools = append(tools, browserTaskToolDefinition())
	}
	return tools
}

func taskToolProperties(goalDescription string) map[string]interface{} {
	return map[string]interface{}{
		"goal":    stringSchema(goalDescription),
		"context": stringSchema("可选；子任务需要知道的用户偏好、约束、上一轮结果等事实信息。子任务看不到主对话，相关信息必须写在这里"),
	}
}

func browserTaskToolDefinition() openai.Tool {
	properties := taskToolProperties("要完成的浏览器任务、需要提取的信息及成功条件")
	properties["start_url"] = stringSchema("可选起始 URL；未填写时从浏览器当前页面开始")
	return functionTool("browser_task", "使用隔离的浏览器子代理完成多步网页操作。子代理独立维护临时上下文，HTML、evaluate 等中间结果不会进入主对话历史；只返回简洁的执行报告。", properties, []string{"goal"})
}

func (p *plugin) runEHTask(ctx *zero.Ctx, goal string) (string, error) {
	if !p.ehTaskEnabled() {
		return "", fmt.Errorf("EH 相关工具未启用")
	}
	system := subagentBaseSystem + "\n\n## 职责\n你负责 E-Hentai/ExHentai 漫画查询与正文图片发送。硬性约束：\n- EH/EX 正文图片必须先用 eh_download_images 下载为本地 file:/// 地址，再传给 send_forward_images 或 send_forward_images_batches；严禁把远端图片直链交给发送工具。\n- eh_download_images 返回 ok=false 时停止发送，并在报告中说明失败原因。\n- 未经任务明确要求，不要解析或发送整本漫画的全部图片。\n- 报告中写清已发送的漫画标题、页数范围和关键中文标签，不要粘贴图片链接。"
	skillFile := strings.TrimSpace(p.cfg.Subagent.EHSkillFile)
	if skillFile != "" {
		if manual, err := p.readSkillFile(skillFile); err != nil {
			log.Printf("[agent/subagent] 读取 EH skill %s 失败，将只使用内置约束: %v", skillFile, err)
		} else {
			system += "\n\n## 操作手册\n" + manual
		}
	}

	tools := withSay(append(ehToolDefinitions(), forwardImageToolDefinitions()...))
	return p.runSubagent(subagentSpec{
		kind:    "eh_task",
		system:  system,
		tools:   tools,
		execute: p.toolExecutor(ctx),
	}, goal)
}

func (p *plugin) runTorrentTask(ctx *zero.Ctx, goal string) (string, error) {
	if !p.cfg.QBittorrent.Enabled {
		return "", fmt.Errorf("qBittorrent 未启用")
	}
	system := subagentBaseSystem + "\n\n## 职责\n你负责通过 qbittorrent 工具管理下载任务：\n- 添加非 PT 公开种子时，先用 paused=true 添加并查看文件列表，用 file_prio 剔除广告/推广文件（如 *.url、广告*.txt、*推广*.exe、www.* 等），只保留需要的内容后再 resume。\n- PT 资源由工具自动识别并设置不限上传，不要手动改动其分享率。\n- 保存位置按内容选择合适的 subdirectory；不确定时使用默认目录。\n- 删除任务时，除非任务明确要求，不要删除本地文件。\n- 权限由工具强制校验；工具返回无权限时如实报告，不要臆测原因。\n- 报告中写清任务名、保留/剔除了哪些文件、保存位置和当前状态，不要粘贴完整 hash 列表或 tracker 列表。"
	return p.runSubagent(subagentSpec{
		kind:    "torrent_task",
		system:  system,
		tools:   withSay([]openai.Tool{qbittorrentToolDefinition()}),
		execute: p.toolExecutor(ctx),
	}, goal)
}

func (p *plugin) runResearchTask(ctx *zero.Ctx, goal string) (string, error) {
	if !p.cfg.Exa.Enabled {
		return "", fmt.Errorf("联网搜索未启用")
	}
	system := subagentBaseSystem + "\n\n## 职责\n你负责联网调查问题：\n- 使用 exa_search 搜索，结果不足时换关键词、分类或开启 live 再查，最多搜索 4 次。\n- 交叉核对多个来源，区分确定事实与传闻，注明信息时间。\n- 报告的“结果”写结论要点，末尾附 2-5 个最相关来源（标题 + URL）。"
	return p.runSubagent(subagentSpec{
		kind:    "research_task",
		system:  system,
		tools:   withSay([]openai.Tool{exaToolDefinition()}),
		execute: p.toolExecutor(ctx),
	}, goal)
}

func (p *plugin) callTask(ctx *zero.Ctx, name string, args map[string]interface{}) (string, error) {
	goal := buildSubagentGoal(stringArg(args, "goal"), stringArg(args, "context"))
	switch name {
	case "eh_task":
		return p.runEHTask(ctx, goal)
	case "torrent_task":
		return p.runTorrentTask(ctx, goal)
	case "research_task":
		return p.runResearchTask(ctx, goal)
	case "browser_task":
		return p.runBrowserSubagent(ctx, goal, stringArg(args, "start_url"))
	default:
		return "", fmt.Errorf("未知子任务：%s", name)
	}
}

func browserToolExecutor(p *plugin) func(name string, rawArgs string) string {
	return func(name string, rawArgs string) string {
		var args map[string]interface{}
		if rawArgs != "" {
			if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
				return "参数 JSON 解析失败：" + err.Error()
			}
		}
		content, err := p.callBrowser(name, args)
		return toolResult(content, err)
	}
}
