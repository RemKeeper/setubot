package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

func (p *plugin) runBrowserSubagent(goal string, startURL string) (string, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "", fmt.Errorf("浏览器任务目标不能为空")
	}

	p.browserM.Lock()
	defer p.browserM.Unlock()

	system := "你是短生命周期的浏览器操作子代理。只完成给定网页任务，可使用浏览器工具。优先通过 evaluate 提取标题、文本、链接等小型结构化 JSON；不要请求或返回完整 DOM、base64、大型数组。完成后用简洁中文说明结果、最终 URL、关键证据和未完成项。中间工具结果只用于本次任务，不会进入主对话。"
	if startURL != "" {
		goal = "先访问：" + startURL + "\n任务：" + goal
	}
	messages := []chatMessage{
		{Role: openai.ChatMessageRoleSystem, Content: system},
		{Role: openai.ChatMessageRoleUser, Content: goal},
	}
	maxSteps := p.cfg.Browser.MaxSubagentSteps
	if maxSteps <= 0 {
		maxSteps = 12
	}

	for step := 0; step < maxSteps; step++ {
		messages = fitMessagesToCharBudget(messages, p.cfg.MaxContextChars)
		resp, err := p.chatWithTools("browser_subagent", messages, browserToolDefinitions())
		if err != nil {
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("浏览器子代理未返回结果")
		}

		msg := normalizeChatMessage(resp.Choices[0].Message)
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			result := strings.TrimSpace(msg.Content)
			if result == "" {
				result = "浏览器任务已完成，但子代理未提供结果摘要"
			}
			return p.compactToolResult("browser_task", result, p.cfg.Browser.MaxResultChars), nil
		}

		for _, call := range msg.ToolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				messages = append(messages, chatMessage{Role: openai.ChatMessageRoleTool, ToolCallID: call.ID, Content: "参数 JSON 解析失败：" + err.Error()})
				continue
			}
			content, err := p.callBrowser(call.Function.Name, args)
			result := p.compactToolResult(call.Function.Name, toolResult(content, err), p.cfg.Browser.MaxResultChars)
			messages = append(messages, chatMessage{Role: openai.ChatMessageRoleTool, ToolCallID: call.ID, Content: result})
		}
	}

	return "", fmt.Errorf("浏览器子代理超过最大步骤数 %d", maxSteps)
}
