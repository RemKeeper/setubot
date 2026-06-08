package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

func (p *plugin) callBrowser(name string, args map[string]interface{}) (string, error) {
	if !p.cfg.Browser.Enabled {
		return "", fmt.Errorf("浏览器工具未启用")
	}

	switch name {
	case "browser_goto":
		return p.browserPost("/api/goto", map[string]interface{}{"url": stringArg(args, "url")})
	case "browser_click":
		return p.browserPost("/api/click", map[string]interface{}{"selector": stringArg(args, "selector"), "force": boolArg(args, "force", false)})
	case "browser_type":
		return p.browserPost("/api/type", map[string]interface{}{"selector": stringArg(args, "selector"), "text": stringArg(args, "text"), "delay": numberArg(args, "delay", 100)})
	case "browser_html":
		return p.browserGet("/api/html")
	case "browser_screenshot":
		return p.browserGet("/api/screenshot")
	case "browser_evaluate":
		return p.browserPost("/api/evaluate", map[string]interface{}{"expression": stringArg(args, "expression")})
	case "browser_scroll":
		return p.browserPost("/api/scroll", map[string]interface{}{"direction": stringArg(args, "direction"), "distance": numberArg(args, "distance", 500)})
	default:
		return "", fmt.Errorf("未知浏览器工具：%s", name)
	}
}

func (p *plugin) browserPost(path string, payload map[string]interface{}) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return p.browserRequest(http.MethodPost, path, bytes.NewReader(body))
}

func (p *plugin) browserGet(path string) (string, error) {
	return p.browserRequest(http.MethodGet, path, nil)
}

func (p *plugin) browserRequest(method string, path string, body io.Reader) (string, error) {
	url := strings.TrimRight(p.cfg.Browser.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("浏览器接口返回 %d：%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return strings.TrimSpace(string(respBody)), nil
}

func (p *plugin) toolDefinitions() []openai.Tool {
	tools := []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "read_skill",
				Description: "读取一个本地 skill 文件。",
				Parameters: objectSchema(map[string]interface{}{
					"name": stringSchema("skill 文件名"),
				}, []string{"name"}),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "write_memory",
				Description: "写入或更新一条持久化记忆。",
				Parameters: objectSchema(map[string]interface{}{
					"key":     stringSchema("记忆键名"),
					"content": stringSchema("记忆内容"),
				}, []string{"key", "content"}),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "read_memory",
				Description: "读取一条持久化记忆。",
				Parameters: objectSchema(map[string]interface{}{
					"key": stringSchema("记忆键名"),
				}, []string{"key"}),
			},
		},
	}

	if !p.cfg.Browser.Enabled {
		return tools
	}

	browserTools := []openai.Tool{
		functionTool("browser_goto", "让浏览器访问指定 URL。", map[string]interface{}{"url": stringSchema("要访问的完整 URL")}, []string{"url"}),
		functionTool("browser_click", "点击页面上的 CSS 选择器。", map[string]interface{}{"selector": stringSchema("CSS 选择器"), "force": boolSchema("是否强制点击")}, []string{"selector"}),
		functionTool("browser_type", "在页面元素中输入文本。", map[string]interface{}{"selector": stringSchema("CSS 选择器"), "text": stringSchema("输入文本"), "delay": numberSchema("输入延迟毫秒")}, []string{"selector", "text"}),
		functionTool("browser_html", "获取当前页面 HTML。", map[string]interface{}{}, []string{}),
		functionTool("browser_screenshot", "获取当前页面截图 base64。", map[string]interface{}{}, []string{}),
		functionTool("browser_evaluate", "在当前页面执行 JavaScript 表达式。", map[string]interface{}{"expression": stringSchema("JavaScript 表达式")}, []string{"expression"}),
		functionTool("browser_scroll", "滚动当前页面。", map[string]interface{}{"direction": enumSchema("滚动方向", []string{"down", "up"}), "distance": numberSchema("滚动像素")}, []string{}),
	}

	return append(tools, browserTools...)
}

func functionTool(name string, description string, properties map[string]interface{}, required []string) openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        name,
			Description: description,
			Parameters:  objectSchema(properties, required),
		},
	}
}

func objectSchema(properties map[string]interface{}, required []string) map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func stringSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": description}
}

func numberSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "number", "description": description}
}

func boolSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": description}
}

func enumSchema(description string, values []string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": description, "enum": values}
}
