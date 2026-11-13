package agent

// 真实网关冒烟测试：默认跳过，需要显式开启，避免 CI 依赖外网与配额。
//
//	AGENT_LIVE=1 go test -run TestReasoningLive ./internal/plugins/agent/ -v
//
// 会读取仓库根目录的 config.json 获取 baseURL / apiKey / model。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
)

func liveAgentConfig(t *testing.T) config.AgentConfig {
	t.Helper()
	if os.Getenv("AGENT_LIVE") == "" {
		t.Skip("跳过真实网关测试：设置 AGENT_LIVE=1 后运行")
	}
	path := filepath.Join("..", "..", "..", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("跳过真实网关测试：读取 %s 失败: %v", path, err)
	}
	var root struct {
		Agent config.AgentConfig `json:"agent"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("解析 config.json 失败: %v", err)
	}
	if strings.TrimSpace(root.Agent.APIKey) == "" {
		t.Skip("跳过真实网关测试：agent.apiKey 未配置")
	}
	if model := strings.TrimSpace(os.Getenv("AGENT_LIVE_MODEL")); model != "" {
		root.Agent.Model = model
	}
	return root.Agent
}

func TestReasoningLiveCapturesThinking(t *testing.T) {
	cfg := liveAgentConfig(t)
	cfg.Reasoning.Enabled = true
	include := true
	cfg.Reasoning.IncludeReasoning = &include

	aiConfig := openai.DefaultConfig(cfg.APIKey)
	aiConfig.BaseURL = openAIBaseURL(cfg.BaseURL)
	aiConfig.HTTPClient = newReasoningHTTPDoer(cfg.Reasoning)

	p := &plugin{
		cfg:      cfg,
		aiClient: openai.NewClientWithConfig(aiConfig),
		trace:    newTraceStore(config.DebugTraceConfig{MaxEvents: 50, MaxBodyChars: 20000}),
	}

	resp, err := p.chatWithTools(nil, "chat_completion", []chatMessage{
		{Role: openai.ChatMessageRoleUser, Content: "9*9=? 只回答数字"},
	}, nil)
	if err != nil {
		t.Fatalf("真实网关调用失败: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("模型未返回结果")
	}

	events, _, _ := p.trace.snapshot(0, 0)
	var response traceEvent
	for _, ev := range events {
		if ev.Kind == traceKindModelResponse {
			response = ev
		}
	}
	if response.Kind == "" {
		t.Fatalf("未记录模型响应事件: %#v", events)
	}
	t.Logf("模型=%s 思考长度=%d 内容=%q", cfg.Model, len([]rune(response.Reasoning)), response.Content)
	if strings.TrimSpace(response.Reasoning) == "" {
		t.Fatalf("未捕获到思考内容，字段=%#v", response.Fields)
	}
}
