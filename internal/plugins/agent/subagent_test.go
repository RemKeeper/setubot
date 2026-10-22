package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
	zero "github.com/wdvxdr1123/ZeroBot"
)

func delegationTestConfig() config.AgentConfig {
	return config.AgentConfig{
		EHReq:       config.EHReqConfig{Enabled: true},
		QBittorrent: config.QBittorrentConfig{Enabled: true},
		Exa:         config.ExaConfig{Enabled: true},
		Browser:     config.BrowserConfig{Enabled: true},
	}
}

func TestToolDefinitionsDelegatesHeavyFlowsToTasks(t *testing.T) {
	p := &plugin{cfg: delegationTestConfig()}
	tools := p.toolDefinitions()
	for _, name := range []string{"eh_task", "torrent_task", "research_task", "browser_task", "write_memory", "xhs_setu", "send_forward_images"} {
		if !hasToolDefinition(tools, name) {
			t.Fatalf("main agent should expose %s", name)
		}
	}
	for _, name := range []string{"eh_req_search", "eh_download_images", "eh_tag_resolve_keyword", "qbittorrent", "exa_search"} {
		if hasToolDefinition(tools, name) {
			t.Fatalf("main agent should not expose raw tool %s when subagents are enabled", name)
		}
	}
}

func TestToolDefinitionsWithoutSubagentsKeepsRawTools(t *testing.T) {
	cfg := delegationTestConfig()
	disabled := false
	cfg.Subagent.Enabled = &disabled
	p := &plugin{cfg: cfg}
	tools := p.toolDefinitions()
	for _, name := range []string{"eh_req_search", "qbittorrent", "exa_search", "browser_task"} {
		if !hasToolDefinition(tools, name) {
			t.Fatalf("raw tool %s should be exposed when subagents are disabled", name)
		}
	}
	for _, name := range []string{"eh_task", "torrent_task", "research_task"} {
		if hasToolDefinition(tools, name) {
			t.Fatalf("task tool %s should not be exposed when subagents are disabled", name)
		}
	}
}

func TestRunSubagentLoopsUntilReport(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []openai.ChatCompletionRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, req)
		round := len(requests)
		mu.Unlock()

		msg := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant}
		switch round {
		case 1:
			msg.ToolCalls = []openai.ToolCall{
				{ID: "call_1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "fake_tool", Arguments: `{"q":"x"}`}},
				{ID: "call_2", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "not_allowed", Arguments: `{}`}},
			}
		default:
			msg.Content = "状态：成功\n已完成：调用了 fake_tool"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{Choices: []openai.ChatCompletionChoice{{Message: msg}}})
	}))
	defer server.Close()

	aiConfig := openai.DefaultConfig("test")
	aiConfig.BaseURL = server.URL + "/v1"
	p := &plugin{
		cfg:      config.AgentConfig{Model: "main-model", Subagent: config.SubagentConfig{Model: "sub-model"}},
		aiClient: openai.NewClientWithConfig(aiConfig),
	}

	var executed []string
	report, err := p.runSubagent(subagentSpec{
		kind:   "test_task",
		system: "system",
		tools:  []openai.Tool{functionTool("fake_tool", "fake", map[string]interface{}{"q": stringSchema("q")}, []string{"q"})},
		execute: func(name string, rawArgs string) string {
			executed = append(executed, name+rawArgs)
			return strings.Repeat("大段原始数据", 100)
		},
	}, "做一件事")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "状态：成功") {
		t.Fatalf("unexpected report: %q", report)
	}
	if len(executed) != 1 || executed[0] != `fake_tool{"q":"x"}` {
		t.Fatalf("only allowed tools should be executed, got %#v", executed)
	}
	if len(requests) != 2 {
		t.Fatalf("expected 2 model rounds, got %d", len(requests))
	}
	if requests[0].Model != "sub-model" {
		t.Fatalf("subagent should use configured model, got %q", requests[0].Model)
	}
	if !strings.Contains(requests[0].Messages[0].Content, "做一件事") {
		t.Fatal("goal should be embedded in subagent system prompt")
	}
	var rejected bool
	for _, msg := range requests[1].Messages {
		if msg.Role == openai.ChatMessageRoleTool && msg.ToolCallID == "call_2" && strings.Contains(msg.Content, "未知工具") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("tool outside the spec should be rejected")
	}
}

func TestBuildSubagentGoalAppendsContext(t *testing.T) {
	if got := buildSubagentGoal("搜索", ""); got != "搜索" {
		t.Fatalf("unexpected goal: %q", got)
	}
	if got := buildSubagentGoal("搜索", "用户不喜欢黑白"); !strings.Contains(got, "用户不喜欢黑白") || !strings.HasPrefix(got, "搜索") {
		t.Fatalf("context not appended: %q", got)
	}
}

func roleplayTestCtx() *zero.Ctx {
	return &zero.Ctx{Event: &zero.Event{UserID: 10001, Sender: &zero.User{ID: 10001, NickName: "旅行者"}}}
}

func TestMainSystemPromptPutsPersonaFirstAndHidesInjectedSkill(t *testing.T) {
	skillDir := t.TempDir()
	for _, name := range []string{"EH_SKILL.md", "SKILL.md"} {
		if err := writeTestFile(skillDir, name, "x"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := delegationTestConfig()
	cfg.SkillDir = skillDir
	cfg.SystemPrompt = "你是猫娘小雪。"
	cfg.Subagent.EHSkillFile = "EH_SKILL.md"
	cfg.Roleplay.Enabled = true
	p := &plugin{cfg: cfg}

	system := p.mainSystemPrompt(roleplayTestCtx(), "你好")
	if !strings.HasPrefix(system, "你是猫娘小雪。") {
		t.Fatalf("persona must come first: %q", system[:40])
	}
	if !strings.Contains(system, "【角色扮演要求】") {
		t.Fatal("roleplay guidelines missing")
	}
	if strings.Index(system, "【角色扮演要求】") > strings.Index(system, "【幕后规则") {
		t.Fatal("roleplay guidelines should precede backstage rules")
	}
	if strings.Contains(system, "EH_SKILL.md") || !strings.Contains(system, "SKILL.md") {
		t.Fatalf("EH skill should be hidden while other skills stay visible: %q", system)
	}
}

func TestRoleplayRepliesAndHistoryCompaction(t *testing.T) {
	p := &plugin{cfg: config.AgentConfig{}}
	if p.thinkingText() != "Agent 正在思考..." {
		t.Fatal("non-roleplay mode should keep default thinking text")
	}
	if p.sessionToolResultChars() != defaultSessionToolResultChars {
		t.Fatal("non-roleplay mode should keep default tool history size")
	}

	p.cfg.Roleplay = config.RoleplayConfig{Enabled: true, ErrorText: "呜，失败了：{error}", HistoryToolResultChars: 10}
	if p.thinkingText() != "" {
		t.Fatal("roleplay mode without thinkingText should stay silent")
	}
	if got := p.failureReply(errTest("超时")); got != "呜，失败了：超时" {
		t.Fatalf("unexpected failure reply: %q", got)
	}
	compacted := compactMessagesForSessionWithLimit([]chatMessage{
		{Role: openai.ChatMessageRoleAssistant, Content: " ", ToolCalls: []openai.ToolCall{{ID: "call_1"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "call_1", Content: strings.Repeat("x", 100)},
	}, p.sessionToolResultChars())
	if len([]rune(compacted[1].Content)) > 13 {
		t.Fatalf("tool result should be compacted for roleplay history: %d", len([]rune(compacted[1].Content)))
	}
}

func writeTestFile(dir string, name string, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
}

type errTest string

func (e errTest) Error() string { return string(e) }
