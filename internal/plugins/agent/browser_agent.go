package agent

import (
	"fmt"
	"strings"

	zero "github.com/wdvxdr1123/ZeroBot"
)

func (p *plugin) runBrowserSubagent(ctx *zero.Ctx, goal string, startURL string) (string, error) {
	if !p.cfg.Browser.Enabled {
		return "", fmt.Errorf("浏览器工具未启用")
	}
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "", fmt.Errorf("浏览器任务目标不能为空")
	}
	if startURL = strings.TrimSpace(startURL); startURL != "" {
		goal = "先访问：" + startURL + "\n任务：" + goal
	}

	p.browserM.Lock()
	defer p.browserM.Unlock()

	system := subagentBaseSystem + "\n\n## 职责\n你负责操作浏览器完成网页任务：\n- 分析页面优先用 browser_observe 获取页面状态与可交互元素，阅读正文用 browser_markdown。\n- 需要精确数据时用 browser_evaluate 提取小型结构化 JSON；不要请求或返回完整 DOM、base64、大型数组。\n- 报告的“结果”中写明最终 URL、关键证据和未完成项。"
	return p.runSubagent(subagentSpec{
		kind:            "browser_subagent",
		system:          system,
		tools:           withSay(browserToolDefinitions()),
		maxSteps:        p.cfg.Browser.MaxSubagentSteps,
		toolResultChars: p.cfg.Browser.MaxResultChars,
		execute: func(name string, rawArgs string) string {
			if name == "say" {
				return p.callTool(ctx, name, rawArgs)
			}
			return browserToolExecutor(p)(name, rawArgs)
		},
	}, goal)
}
