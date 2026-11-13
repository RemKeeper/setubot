package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

func newTestTracePlugin() *plugin {
	cfg := config.AgentConfig{DebugTrace: config.DebugTraceConfig{MaxEvents: 5, MaxBodyChars: 100}}
	return &plugin{cfg: cfg, trace: newTraceStore(cfg.DebugTrace)}
}

func TestTraceStoreKeepsOnlyRecentEvents(t *testing.T) {
	store := newTraceStore(config.DebugTraceConfig{MaxEvents: 3, MaxBodyChars: 100})
	for i := 0; i < 5; i++ {
		store.add(traceEvent{Kind: traceKindNote, Title: "n"})
	}

	events, last, _ := store.snapshot(0, 0)
	if len(events) != 3 {
		t.Fatalf("expected 3 retained events, got %d", len(events))
	}
	if last != 5 {
		t.Fatalf("expected last id 5, got %d", last)
	}
	if events[0].ID != 3 || events[2].ID != 5 {
		t.Fatalf("unexpected retained ids: %d..%d", events[0].ID, events[2].ID)
	}
}

func TestTraceStoreClipsLongBodies(t *testing.T) {
	store := newTraceStore(config.DebugTraceConfig{MaxEvents: 10, MaxBodyChars: 10})
	store.add(traceEvent{Kind: traceKindNote, Body: strings.Repeat("a", 50)})

	events, _, _ := store.snapshot(0, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if len([]rune(events[0].Body)) > 13 {
		t.Fatalf("body not clipped: %q", events[0].Body)
	}
}

func TestTraceStoreSnapshotAfter(t *testing.T) {
	store := newTraceStore(config.DebugTraceConfig{MaxEvents: 10})
	store.add(traceEvent{Kind: traceKindNote, Title: "a"})
	store.add(traceEvent{Kind: traceKindNote, Title: "b"})

	events, last, _ := store.snapshot(1, 0)
	if len(events) != 1 || events[0].Title != "b" || last != 2 {
		t.Fatalf("unexpected snapshot: %#v last=%d", events, last)
	}
}

func TestTraceStoreSnapshotResetsAfterClear(t *testing.T) {
	store := newTraceStore(config.DebugTraceConfig{MaxEvents: 10})
	store.add(traceEvent{Kind: traceKindNote, Title: "a"})
	store.add(traceEvent{Kind: traceKindNote, Title: "b"})
	store.clear()
	store.add(traceEvent{Kind: traceKindNote, Title: "c"})

	events, last, reset := store.snapshot(2, 0)
	if !reset {
		t.Fatal("expected reset flag after clear")
	}
	if len(events) != 1 || events[0].Title != "c" || last != 1 {
		t.Fatalf("unexpected snapshot after clear: %#v last=%d", events, last)
	}
}

func TestTraceStoreTurnLifecycle(t *testing.T) {
	store := newTraceStore(config.DebugTraceConfig{MaxEvents: 10})
	turn := store.newTurn("group:1:user:2", "test-model")
	if turn == nil || turn.ID == "" {
		t.Fatal("expected a turn with id")
	}
	if got := store.activeTurn("group:1:user:2"); got != turn {
		t.Fatal("expected active turn to be registered")
	}
	store.finishTurn(turn)
	if got := store.activeTurn("group:1:user:2"); got != nil {
		t.Fatal("expected active turn to be removed")
	}
}

func TestTraceModelResponseCapturesReasoningAndToolCalls(t *testing.T) {
	p := newTestTracePlugin()
	turn := p.trace.newTurn("private:1", "m")
	resp := &openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			FinishReason: openai.FinishReasonToolCalls,
			Message: openai.ChatCompletionMessage{
				Role:             openai.ChatMessageRoleAssistant,
				ReasoningContent: "先查资料",
				ToolCalls: []openai.ToolCall{{
					ID:       "call_1",
					Type:     openai.ToolTypeFunction,
					Function: openai.FunctionCall{Name: "exa_search", Arguments: `{"query":"x"}`},
				}},
			},
		}},
		Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	p.traceModelResponse(turn, "chat_completion", "m", resp, nil, 120*time.Millisecond, nil)

	events, _, _ := p.trace.snapshot(0, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Kind != traceKindModelResponse || ev.Reasoning != "先查资料" {
		t.Fatalf("unexpected event: %#v", ev)
	}
	if ev.DurationMs != 120 {
		t.Fatalf("unexpected duration: %d", ev.DurationMs)
	}
	joined := ""
	for _, f := range ev.Fields {
		joined += f.Key + "=" + f.Value + ";"
	}
	if !strings.Contains(joined, "exa_search") || !strings.Contains(joined, "total=15") {
		t.Fatalf("missing tool/token fields: %s", joined)
	}
}

func TestTraceToolCallRecordsArgsAndResult(t *testing.T) {
	p := newTestTracePlugin()
	call := openai.ToolCall{ID: "c1", Function: openai.FunctionCall{Name: "read_skill", Arguments: `{"name":"A.md"}`}}
	p.traceToolCall(nil, 2, call, "skill body", 30*time.Millisecond)

	events, _, _ := p.trace.snapshot(0, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Kind != traceKindToolCall || ev.Body != `{"name":"A.md"}` || ev.Content != "skill body" {
		t.Fatalf("unexpected tool event: %#v", ev)
	}
	if ev.TurnID != traceSystemTurnID {
		t.Fatalf("expected system turn id, got %q", ev.TurnID)
	}
}

func TestDescribeQQMessageSummarizesSegments(t *testing.T) {
	msg := message.Message{
		message.Text("hello"),
		message.Image("https://example.com/a.jpg"),
		message.CustomNode("nick", 42, message.Message{message.Text("inner")}),
	}
	fields, body := describeQQMessage(msg)
	if body == "" {
		t.Fatal("expected JSON body")
	}
	var decoded message.Message
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body is not valid message JSON: %v", err)
	}
	joined := ""
	for _, f := range fields {
		joined += f.Key + "=" + f.Value + ";"
	}
	if !strings.Contains(joined, "段数=3") || !strings.Contains(joined, "段:node=1") {
		t.Fatalf("unexpected fields: %s", joined)
	}
	if !strings.Contains(joined, "https://example.com/a.jpg") {
		t.Fatalf("expected image url in preview: %s", joined)
	}
}

func TestDescribeQQMessageHandlesPlainString(t *testing.T) {
	fields, body := describeQQMessage("plain text")
	if body != "plain text" || len(fields) != 1 || fields[0].Key != "文本" {
		t.Fatalf("unexpected describe result: %#v %q", fields, body)
	}
}

func TestSanitizeTraceImageURLDropsBase64(t *testing.T) {
	raw := "data:image/png;base64," + strings.Repeat("A", 500)
	got := sanitizeTraceImageURL(raw)
	if strings.Contains(got, strings.Repeat("A", 50)) {
		t.Fatalf("base64 payload leaked: %q", got)
	}
	if !strings.Contains(got, "omitted") {
		t.Fatalf("expected omission marker: %q", got)
	}
}

func TestTracePageAndEventsHandlers(t *testing.T) {
	p := newTestTracePlugin()
	p.trace.add(traceEvent{Kind: traceKindNote, Title: "hello"})

	index := httptest.NewRecorder()
	p.handleTraceIndex(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("unexpected index status: %d", index.Code)
	}
	if !strings.Contains(index.Body.String(), "Agent 调试跟踪") {
		t.Fatal("index page missing title")
	}

	events := httptest.NewRecorder()
	p.handleTraceEvents(events, httptest.NewRequest(http.MethodGet, "/api/events?after=0", nil))
	if events.Code != http.StatusOK {
		t.Fatalf("unexpected events status: %d", events.Code)
	}
	var payload struct {
		Events []traceEvent `json:"events"`
		LastID int64        `json:"lastId"`
	}
	if err := json.Unmarshal(events.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid events payload: %v", err)
	}
	if len(payload.Events) != 1 || payload.LastID != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}

	clear := httptest.NewRecorder()
	p.handleTraceClear(clear, httptest.NewRequest(http.MethodPost, "/api/clear", nil))
	if clear.Code != http.StatusOK {
		t.Fatalf("unexpected clear status: %d", clear.Code)
	}
	if remaining, _, _ := p.trace.snapshot(0, 0); len(remaining) != 0 {
		t.Fatalf("expected cleared events, got %d", len(remaining))
	}
}

func TestTraceServerServesPageAndEvents(t *testing.T) {
	p := newTestTracePlugin()
	server, listener, err := p.newTraceServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start trace server: %v", err)
	}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()

	base := "http://" + listener.Addr().String()
	p.trace.add(traceEvent{Kind: traceKindQQSend, Title: "发送到 QQ"})

	page, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("index request failed: %v", err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("unexpected index status: %d", page.StatusCode)
	}

	resp, err := http.Get(base + "/api/events?after=0")
	if err != nil {
		t.Fatalf("events request failed: %v", err)
	}
	defer resp.Body.Close()
	var payload struct {
		Events []traceEvent           `json:"events"`
		Stats  map[string]interface{} `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("invalid events payload: %v", err)
	}
	if len(payload.Events) != 1 || payload.Events[0].Kind != traceKindQQSend {
		t.Fatalf("unexpected events: %#v", payload.Events)
	}
	if payload.Stats == nil {
		t.Fatal("expected stats in payload")
	}
}

func TestTraceQQSendRecordsTargetAndMessageID(t *testing.T) {
	p := newTestTracePlugin()
	ctx := &zero.Ctx{Event: &zero.Event{GroupID: 123, UserID: 456}}
	p.traceQQSend(ctx, "final", message.Message{message.Text("hi")}, message.NewMessageIDFromInteger(789))

	events, _, _ := p.trace.snapshot(0, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Kind != traceKindQQSend || ev.Title != "发送到 QQ" {
		t.Fatalf("unexpected event: %#v", ev)
	}
	joined := ""
	for _, f := range ev.Fields {
		joined += f.Key + "=" + f.Value + ";"
	}
	if !strings.Contains(joined, "group:123") || !strings.Contains(joined, "789") || !strings.Contains(joined, "final") {
		t.Fatalf("missing target/id/type fields: %s", joined)
	}
}

func TestChatWithToolsTracesRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"test-model",
			"choices":[{"index":0,"finish_reason":"tool_calls","message":{
				"role":"assistant","reasoning_content":"先搜索","content":"",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"exa_search","arguments":"{\"query\":\"go\"}"}}]
			}}],
			"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}
		}`))
	}))
	defer server.Close()

	aiConfig := openai.DefaultConfig("test-key")
	aiConfig.BaseURL = server.URL + "/v1"
	p := newTestTracePlugin()
	p.aiClient = openai.NewClientWithConfig(aiConfig)
	p.cfg.Model = "test-model"
	p.cfg.MaxContextChars = 10000

	turn := p.trace.newTurn("private:1", "test-model")
	resp, err := p.chatWithTools(turn, "chat_completion", []chatMessage{{Role: openai.ChatMessageRoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chatWithTools failed: %v", err)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("unexpected response: %#v", resp)
	}

	events, _, _ := p.trace.snapshot(0, 0)
	if len(events) != 2 {
		t.Fatalf("expected request+response events, got %d: %#v", len(events), events)
	}
	if events[0].Kind != traceKindModelRequest || !strings.Contains(events[0].Body, "hi") {
		t.Fatalf("unexpected request event: %#v", events[0])
	}
	if events[1].Kind != traceKindModelResponse || events[1].Reasoning != "先搜索" {
		t.Fatalf("unexpected response event: %#v", events[1])
	}
	if events[1].TurnID != turn.ID {
		t.Fatalf("expected turn id %q, got %q", turn.ID, events[1].TurnID)
	}
}

func TestTraceDisabledByDefaultWithoutStore(t *testing.T) {
	p := &plugin{}
	if p.traceEnabled() {
		t.Fatal("trace should be disabled without a store")
	}
	// 未启用时所有跟踪入口都必须是安全的空操作。
	p.traceNote(nil, "x", "y")
	p.traceToolCall(nil, 0, openai.ToolCall{}, "", 0)
	p.traceEndTurn(nil, "", nil)
	if p.activeTraceTurn(nil) != nil {
		t.Fatal("expected nil active turn")
	}
}
