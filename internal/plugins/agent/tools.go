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
				Description: "写入或更新一条当前聊天作用域内的持久化记忆。群聊会自动按群隔离，适合保存明确的长期偏好、规则、项目约定或持续任务上下文；不要保存敏感信息、临时请求或猜测。",
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
				Description: "按键名精确读取当前聊天作用域内的一条持久化记忆。群聊只能读取当前群的记忆。需要先搜索未知键名时优先用 search_memory。",
				Parameters: objectSchema(map[string]interface{}{
					"key": stringSchema("记忆键名"),
				}, []string{"key"}),
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "search_memory",
				Description: "使用本地 MemoryStore 全文索引搜索当前聊天作用域内的相关持久化记忆。群聊会自动按群隔离，避免不同群的记忆互相污染。",
				Parameters: objectSchema(map[string]interface{}{
					"query": stringSchema("用于检索记忆的关键词或自然语言问题"),
					"limit": numberSchema("最多返回条数，默认 5，最大 20"),
				}, []string{"query"}),
			},
		},
		functionTool("xhs_setu", "执行小红书涩图脚本，自动按标题/tag筛选、默认点赞收藏并把图片发送到当前聊天；关键词搜索会按会话去重，多图会用合并转发避免刷屏。传 skip_engage=true 时只提取和发送图片，不自动点赞或收藏。", map[string]interface{}{
			"count":       numberSchema("要处理的帖子数量，默认 1，最大 5"),
			"scroll":      numberSchema("推荐页滚动次数，默认 2，最大 10"),
			"keyword":     stringSchema("可选搜索关键词；为空时使用推荐页"),
			"skip_engage": boolSchema("可选；true 表示不自动点击点赞和收藏，只提取并发送图片"),
		}, []string{}),
		functionTool("xhs_dislike", "撤销最近一次小红书帖子的点赞和收藏；可选把关键词加入负面列表。", map[string]interface{}{
			"keyword": stringSchema("可选负面关键词，只有用户明确要求以后别推某类内容时填写"),
		}, []string{}),
		functionTool("send_forward_images", "把多个图片链接作为合并转发消息发送到当前聊天。合并转发节点的发送者会伪造成本次请求的发起人。可选 rotate 参数会先把图片顺时针旋转后再发送。", map[string]interface{}{
			"images": arrayStringSchema("图片链接列表，至少 1 个，最多 30 个"),
			"rotate": enumNumberSchema("可选；顺时针旋转角度，仅支持 90、180、270；不传或传 0 表示不旋转", []int{0, 90, 180, 270}),
		}, []string{"images"}),
		functionTool("eh_download_images", "下载多个 EH/EX 正文图片直链到本地缓存，并返回可传给 send_forward_images 的 file:/// 本地图片地址。下载复用 EH 请求代理配置；本地缓存最多保留 100 张旧图并自动清理。", map[string]interface{}{
			"images":  arrayStringSchema("图片直链列表，至少 1 个；工具会返回对应 fileUrl"),
			"referer": stringSchema("可选 Referer，建议传对应详情页或图片页 URL"),
		}, []string{"images"}),
		functionTool("eh_tag_load", "加载或刷新 EhTagTranslation 标签数据库索引。启动时会自动加载；仅在需要查看状态或强制刷新时调用。", map[string]interface{}{
			"force_refresh": boolSchema("是否忽略本地缓存并强制从远程 sourceURL 重新下载"),
		}, []string{}),
		functionTool("eh_tag_search", "搜索 E-Hentai/EhTagTranslation 标签候选。支持中文名、英文 key、命名空间、简介检索。用于 EH_SKILL 的标签中文映射。", map[string]interface{}{
			"query":         stringSchema("搜索文本，例如 中文、语言:中文、female:sole female、画师名"),
			"namespace":     stringSchema("可选命名空间，可用英文或中文，例如 language、female、语言、女性"),
			"limit":         numberSchema("最多返回数量，默认 10，最大 100"),
			"include_intro": boolSchema("是否搜索简介内容，默认 false；解析中文标签时可设 true"),
		}, []string{"query"}),
		functionTool("eh_tag_resolve_keyword", "把中文标签或自然语言关键词解析为 E-Hentai 搜索表达式，例如 中文 -> language:chinese。候选有歧义时会返回 candidates。", map[string]interface{}{
			"keyword":     stringSchema("要解析的中文标签、英文标签或 EH 搜索表达式"),
			"auto_select": boolSchema("是否在最高候选明显领先时自动选择，默认 true"),
			"limit":       numberSchema("候选数量，默认 10，最大 100"),
		}, []string{"keyword"}),
		functionTool("eh_tag_translate", "把 E-Hentai 标签翻译为中文展示信息。输入 namespace/key 数组，输出中文命名空间、中文标签名和简介。", map[string]interface{}{
			"tags": arrayObjectSchema("标签数组，每项包含 namespace 和 key", map[string]interface{}{
				"namespace": stringSchema("标签命名空间，例如 language、female、artist"),
				"key":       stringSchema("标签 key，例如 chinese"),
			}, []string{"namespace", "key"}),
		}, []string{"tags"}),
		functionTool("eh_req_search", "请求 E-Hentai/ExHentai 搜索页。Cookie 只从 config/env/本地文件注入，不允许通过参数传入，也不会在结果中回显。", map[string]interface{}{
			"site":     enumSchema("站点，默认 EX", []string{"EX", "EH"}),
			"keyword":  stringSchema("f_search 搜索表达式，例如 language:chinese artist:name"),
			"page":     numberSchema("搜索页码，从 0 开始"),
			"advanced": boolSchema("是否添加 s_act=advanced"),
			"query":    objectFreeSchema("可选额外 query 参数；cookie/authorization 等敏感键会被忽略"),
		}, []string{}),
		functionTool("eh_req_gallery", "请求 E-Hentai/ExHentai 漫画详情页 /g/{gid}/{token}/。Cookie 自动注入且不回显。", map[string]interface{}{
			"site":          enumSchema("站点，默认 EX", []string{"EX", "EH"}),
			"gid":           numberSchema("漫画 gid"),
			"token":         stringSchema("漫画 token"),
			"page":          numberSchema("缩略图分页 p，默认 0"),
			"show_comments": boolSchema("是否设置 hc=1 显示完整评论，默认 true"),
		}, []string{"gid", "token"}),
		functionTool("eh_req_api", "请求 E-Hentai/ExHentai 官方 API，例如 gdata、tagsuggest。Cookie 自动注入且不回显。", map[string]interface{}{
			"site":      enumSchema("站点，默认 EX", []string{"EX", "EH"}),
			"payload":   objectFreeSchema("完整 JSON payload；优先使用该字段"),
			"method":    stringSchema("API method，例如 gdata 或 tagsuggest；未传 payload 时使用"),
			"text":      stringSchema("tagsuggest 文本；未传 payload 时使用"),
			"gidlist":   arrayArraySchema("gdata 的 gidlist，例如 [[gid, token]]"),
			"namespace": numberSchema("gdata namespace 参数，通常为 1"),
		}, []string{}),
		functionTool("eh_req_image_page", "请求 E-Hentai/ExHentai 正文图片页 /s/{imageHash}/{gid}-{pageNo}，用于解析正文图片 URL。Cookie 自动注入且不回显。", map[string]interface{}{
			"site":       enumSchema("站点，默认 EX", []string{"EX", "EH"}),
			"image_hash": stringSchema("图片页 hash"),
			"gid":        numberSchema("漫画 gid"),
			"page_no":    numberSchema("图片页序号，从 1 开始"),
			"reload_key": stringSchema("可选 nl reload key"),
		}, []string{"image_hash", "gid", "page_no"}),
	}
	if p.cfg.Exa.Enabled {
		tools = append(tools, functionTool("exa_search", "使用 Exa.ai 搜索互联网，返回标题、URL、发布时间、作者和 highlights 摘要。适合查询实时信息、网页资料和需要来源的问题。", map[string]interface{}{
			"query":           stringSchema("搜索查询，使用自然语言描述要找的信息"),
			"type":            enumSchema("搜索类型，默认使用配置值", []string{"auto", "fast", "instant", "deep-lite", "deep", "deep-reasoning"}),
			"num_results":     numberSchema("返回结果数量，默认使用配置值，范围 1-10"),
			"category":        stringSchema("可选分类，如 news、research paper、company、people、personal site、financial report"),
			"include_domains": arrayStringSchema("可选，仅包含这些域名"),
			"exclude_domains": arrayStringSchema("可选，排除这些域名；company/people 分类不要使用"),
			"live":            boolSchema("是否强制实时抓取。true 会设置 contents.maxAgeHours=0，可能更慢"),
		}, []string{"query"}))
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

func enumNumberSchema(description string, values []int) map[string]interface{} {
	enum := make([]interface{}, 0, len(values))
	for _, value := range values {
		enum = append(enum, value)
	}
	return map[string]interface{}{"type": "number", "description": description, "enum": enum}
}

func boolSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": description}
}

func arrayStringSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "array", "description": description, "items": map[string]interface{}{"type": "string"}}
}

func arrayObjectSchema(description string, properties map[string]interface{}, required []string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "array",
		"description": description,
		"items":       objectSchema(properties, required),
	}
}

func arrayArraySchema(description string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "array",
		"description": description,
		"items":       map[string]interface{}{"type": "array", "items": map[string]interface{}{}},
	}
}

func objectFreeSchema(description string) map[string]interface{} {
	return map[string]interface{}{"type": "object", "description": description, "additionalProperties": true}
}

func enumSchema(description string, values []string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": description, "enum": values}
}
