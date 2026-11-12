package agent

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

//go:embed trace_page.html
var tracePageHTML string

const (
	traceKindTurnStart     = "turn_start"
	traceKindModelRequest  = "model_request"
	traceKindModelResponse = "model_response"
	traceKindToolCall      = "tool_call"
	traceKindQQSend        = "qq_send"
	traceKindTurnEnd       = "turn_end"
	traceKindNote          = "note"

	traceSystemTurnID = "system"
)

type traceField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type traceEvent struct {
	ID         int64        `json:"id"`
	Time       string       `json:"time"`
	TurnID     string       `json:"turnId"`
	Kind       string       `json:"kind"`
	Title      string       `json:"title"`
	Fields     []traceField `json:"fields,omitempty"`
	Reasoning  string       `json:"reasoning,omitempty"`
	Content    string       `json:"content,omitempty"`
	Body       string       `json:"body,omitempty"`
	DurationMs int64        `json:"durationMs,omitempty"`
	Error      string       `json:"error,omitempty"`
}

type traceTurn struct {
	ID         string
	SessionKey string
	Model      string
	Started    time.Time
}

// traceStore 是进程内的调试事件环形缓冲，供内网页面轮询展示。
type traceStore struct {
	mu           sync.Mutex
	events       []traceEvent
	maxEvents    int
	maxBodyChars int
	nextID       int64
	turnSeq      int64
	startedAt    time.Time
	active       map[string]*traceTurn
}

func newTraceStore(cfg config.DebugTraceConfig) *traceStore {
	maxEvents := cfg.MaxEvents
	if maxEvents <= 0 {
		maxEvents = 2000
	}
	maxBodyChars := cfg.MaxBodyChars
	if maxBodyChars <= 0 {
		maxBodyChars = 20000
	}
	return &traceStore{
		maxEvents:    maxEvents,
		maxBodyChars: maxBodyChars,
		startedAt:    time.Now(),
		active:       make(map[string]*traceTurn),
	}
}

func (s *traceStore) clip(text string) string {
	if s == nil || text == "" {
		return text
	}
	if len([]rune(text)) <= s.maxBodyChars {
		return text
	}
	return truncateRunes(text, s.maxBodyChars)
}

func (s *traceStore) add(ev traceEvent) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	ev.ID = s.nextID
	if ev.Time == "" {
		ev.Time = time.Now().Format("15:04:05.000")
	}
	ev.Reasoning = s.clip(ev.Reasoning)
	ev.Content = s.clip(ev.Content)
	ev.Body = s.clip(ev.Body)
	for i := range ev.Fields {
		ev.Fields[i].Value = truncateRunes(ev.Fields[i].Value, 2000)
	}

	s.events = append(s.events, ev)
	if len(s.events) > s.maxEvents {
		drop := len(s.events) - s.maxEvents
		s.events = append([]traceEvent(nil), s.events[drop:]...)
	}
}

func (s *traceStore) snapshot(after int64, limit int) ([]traceEvent, int64, bool) {
	if s == nil {
		return nil, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 事件被清空后 ID 会重新计数，此时把 after 视为 0，并告知客户端重置本地缓存。
	reset := after > s.nextID
	if reset {
		after = 0
	}
	out := make([]traceEvent, 0, len(s.events))
	for _, ev := range s.events {
		if ev.ID > after {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	last := after
	if len(s.events) > 0 {
		last = s.events[len(s.events)-1].ID
	}
	return out, last, reset
}

func (s *traceStore) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
	s.nextID = 0
}

func (s *traceStore) stats() map[string]interface{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{
		"events":      len(s.events),
		"maxEvents":   s.maxEvents,
		"lastId":      s.nextID,
		"uptimeSec":   int64(time.Since(s.startedAt).Seconds()),
		"activeTurns": len(s.active),
	}
}

func (s *traceStore) newTurn(sessionKey string, model string) *traceTurn {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.turnSeq++
	seq := s.turnSeq
	s.mu.Unlock()

	turn := &traceTurn{
		ID:         fmt.Sprintf("T%d-%s", seq, time.Now().Format("150405")),
		SessionKey: sessionKey,
		Model:      model,
		Started:    time.Now(),
	}
	s.mu.Lock()
	s.active[sessionKey] = turn
	s.mu.Unlock()
	return turn
}

func (s *traceStore) finishTurn(turn *traceTurn) {
	if s == nil || turn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.active[turn.SessionKey]; ok && current == turn {
		delete(s.active, turn.SessionKey)
	}
}

func (s *traceStore) activeTurn(sessionKey string) *traceTurn {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[sessionKey]
}

func (p *plugin) traceEnabled() bool {
	return p != nil && p.trace != nil
}

// activeTraceTurn 返回当前会话正在处理的跟踪轮次，供子流程（如浏览器子代理）复用。
func (p *plugin) activeTraceTurn(ctx *zero.Ctx) *traceTurn {
	if !p.traceEnabled() || ctx == nil {
		return nil
	}
	return p.trace.activeTurn(p.sessionKey(ctx))
}

func traceTurnID(turn *traceTurn) string {
	if turn == nil {
		return traceSystemTurnID
	}
	return turn.ID
}

func (p *plugin) traceStartTurn(ctx *zero.Ctx, prompt string, sessionKey string) *traceTurn {
	if !p.traceEnabled() {
		return nil
	}
	turn := p.trace.newTurn(sessionKey, p.currentModel())
	fields := []traceField{
		{"会话", sessionKey},
		{"模型", turn.Model},
		{"用户", fmt.Sprintf("%s(%d)", requestUserName(ctx), ctx.Event.UserID)},
	}
	if ctx.Event.GroupID != 0 {
		fields = append(fields, traceField{"群号", fmt.Sprint(ctx.Event.GroupID)})
	}
	p.trace.add(traceEvent{
		TurnID:  turn.ID,
		Kind:    traceKindTurnStart,
		Title:   "开始处理请求",
		Fields:  fields,
		Content: prompt,
	})
	return turn
}

func (p *plugin) traceEndTurn(turn *traceTurn, answer string, err error) {
	if !p.traceEnabled() || turn == nil {
		return
	}
	ev := traceEvent{
		TurnID:     turn.ID,
		Kind:       traceKindTurnEnd,
		Title:      "处理结束",
		Content:    answer,
		DurationMs: time.Since(turn.Started).Milliseconds(),
	}
	if err != nil {
		ev.Error = err.Error()
	}
	p.trace.add(ev)
	p.trace.finishTurn(turn)
}

func (p *plugin) traceNote(turn *traceTurn, title string, body string) {
	if !p.traceEnabled() {
		return
	}
	p.trace.add(traceEvent{
		TurnID: traceTurnID(turn),
		Kind:   traceKindNote,
		Title:  title,
		Body:   body,
	})
}

func (p *plugin) traceModelRequest(turn *traceTurn, kind string, req openai.ChatCompletionRequest) {
	if !p.traceEnabled() {
		return
	}
	fields := []traceField{
		{"类型", kind},
		{"模型", req.Model},
		{"消息数", fmt.Sprint(len(req.Messages))},
		{"工具数", fmt.Sprint(len(req.Tools))},
		{"温度", fmt.Sprintf("%.2f", req.Temperature)},
	}
	p.trace.add(traceEvent{
		TurnID: traceTurnID(turn),
		Kind:   traceKindModelRequest,
		Title:  "模型请求",
		Fields: fields,
		Body:   renderTraceMessages(req.Messages),
	})
}

func (p *plugin) traceModelResponse(turn *traceTurn, kind string, model string, resp *openai.ChatCompletionResponse, err error, elapsed time.Duration) {
	if !p.traceEnabled() {
		return
	}
	ev := traceEvent{
		TurnID:     traceTurnID(turn),
		Kind:       traceKindModelResponse,
		Title:      "模型响应",
		DurationMs: elapsed.Milliseconds(),
		Fields: []traceField{
			{"类型", kind},
			{"模型", model},
		},
	}
	if err != nil {
		ev.Error = err.Error()
		p.trace.add(ev)
		return
	}
	if resp == nil || len(resp.Choices) == 0 {
		ev.Error = "模型未返回结果"
		p.trace.add(ev)
		return
	}

	choice := resp.Choices[0]
	ev.Reasoning = choice.Message.ReasoningContent
	ev.Content = choice.Message.Content
	if choice.FinishReason != "" {
		ev.Fields = append(ev.Fields, traceField{"结束原因", string(choice.FinishReason)})
	}
	if resp.Usage.TotalTokens > 0 {
		ev.Fields = append(ev.Fields, traceField{
			"tokens",
			fmt.Sprintf("prompt=%d completion=%d total=%d", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens),
		})
	}
	if len(choice.Message.ToolCalls) > 0 {
		names := make([]string, 0, len(choice.Message.ToolCalls))
		for _, call := range choice.Message.ToolCalls {
			names = append(names, call.Function.Name)
		}
		ev.Fields = append(ev.Fields, traceField{"工具调用", strings.Join(names, ", ")})
	}
	p.trace.add(ev)
}

func (p *plugin) traceToolCall(turn *traceTurn, round int, call openai.ToolCall, result string, elapsed time.Duration) {
	if !p.traceEnabled() {
		return
	}
	p.trace.add(traceEvent{
		TurnID: traceTurnID(turn),
		Kind:   traceKindToolCall,
		Title:  "工具调用 " + call.Function.Name,
		Fields: []traceField{
			{"轮次", fmt.Sprint(round)},
			{"工具", call.Function.Name},
			{"调用ID", call.ID},
		},
		Body:       call.Function.Arguments,
		Content:    result,
		DurationMs: elapsed.Milliseconds(),
	})
}

// send 统一封装 ctx.Send，并在调试跟踪开启时记录组合消息与发送结果。
func (p *plugin) send(ctx *zero.Ctx, msg interface{}, kind ...string) message.ID {
	label := "reply"
	if len(kind) > 0 && strings.TrimSpace(kind[0]) != "" {
		label = strings.TrimSpace(kind[0])
	}
	id := ctx.Send(msg)
	p.traceQQSend(ctx, label, msg, id)
	return id
}

func (p *plugin) traceQQSend(ctx *zero.Ctx, label string, msg interface{}, id message.ID) {
	if !p.traceEnabled() || ctx == nil {
		return
	}
	turn := p.trace.activeTurn(p.sessionKey(ctx))
	fields, body := describeQQMessage(msg)
	fields = append([]traceField{
		{"类型", label},
		{"目标", qqTarget(ctx)},
		{"消息ID", id.String()},
	}, fields...)
	p.trace.add(traceEvent{
		TurnID: traceTurnID(turn),
		Kind:   traceKindQQSend,
		Title:  "发送到 QQ",
		Fields: fields,
		Body:   body,
	})
}

func qqTarget(ctx *zero.Ctx) string {
	if ctx.Event.GroupID != 0 {
		return fmt.Sprintf("group:%d", ctx.Event.GroupID)
	}
	return fmt.Sprintf("private:%d", ctx.Event.UserID)
}

func describeQQMessage(msg interface{}) ([]traceField, string) {
	switch m := msg.(type) {
	case message.Message:
		return describeQQSegments(m)
	case *message.Message:
		if m == nil {
			return nil, ""
		}
		return describeQQSegments(*m)
	case message.Segment:
		return describeQQSegments(message.Message{m})
	case []message.Segment:
		return describeQQSegments(message.Message(m))
	case string:
		return []traceField{{"文本", truncateRunes(m, 500)}}, m
	default:
		data, err := json.Marshal(msg)
		if err != nil {
			return []traceField{{"内容", truncateRunes(fmt.Sprint(msg), 500)}}, ""
		}
		return []traceField{{"内容", truncateRunes(string(data), 500)}}, string(data)
	}
}

func describeQQSegments(msg message.Message) ([]traceField, string) {
	fields := []traceField{{"段数", fmt.Sprint(len(msg))}}
	counts := make(map[string]int, len(msg))
	for _, seg := range msg {
		counts[seg.Type]++
	}
	types := make([]string, 0, len(counts))
	for segType := range counts {
		types = append(types, segType)
	}
	sort.Strings(types)
	for _, segType := range types {
		fields = append(fields, traceField{"段:" + segType, fmt.Sprint(counts[segType])})
	}

	preview := make([]string, 0, len(msg))
	for i, seg := range msg {
		if i >= 20 {
			preview = append(preview, fmt.Sprintf("... 其余 %d 段省略", len(msg)-i))
			break
		}
		preview = append(preview, describeQQSegment(seg))
	}
	fields = append(fields, traceField{"预览", strings.Join(preview, " | ")})

	body, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return fields, strings.Join(preview, "\n")
	}
	return fields, string(body)
}

func describeQQSegment(seg message.Segment) string {
	switch seg.Type {
	case "text":
		return "text: " + truncateRunes(seg.Data["text"], 200)
	case "image":
		return "image: " + truncateRunes(traceFirstNonEmpty(seg.Data["url"], seg.Data["file"]), 200)
	case "node":
		return fmt.Sprintf("node: %s(%s) %s", seg.Data["name"], seg.Data["uin"], truncateRunes(seg.Data["content"], 200))
	case "at":
		return "at: " + seg.Data["qq"]
	case "reply":
		return "reply: " + seg.Data["id"]
	default:
		data, err := json.Marshal(seg.Data)
		if err != nil {
			return seg.Type
		}
		return seg.Type + ": " + truncateRunes(string(data), 200)
	}
}

func traceFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func renderTraceMessages(messages []openai.ChatCompletionMessage) string {
	var b strings.Builder
	for i, msg := range messages {
		fmt.Fprintf(&b, "[%d] %s\n", i+1, msg.Role)
		if msg.ReasoningContent != "" {
			b.WriteString("思考: " + truncateRunes(msg.ReasoningContent, 2000) + "\n")
		}
		if msg.Content != "" {
			b.WriteString("内容: " + truncateRunes(msg.Content, 2000) + "\n")
		}
		for _, part := range msg.MultiContent {
			switch part.Type {
			case openai.ChatMessagePartTypeText:
				b.WriteString("文本: " + truncateRunes(part.Text, 2000) + "\n")
			case openai.ChatMessagePartTypeImageURL:
				url := ""
				if part.ImageURL != nil {
					url = part.ImageURL.URL
				}
				b.WriteString("图片: " + sanitizeTraceImageURL(url) + "\n")
			}
		}
		for _, call := range msg.ToolCalls {
			fmt.Fprintf(&b, "工具调用: %s(%s)\n", call.Function.Name, truncateRunes(call.Function.Arguments, 2000))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sanitizeTraceImageURL 避免把 base64 图片正文写进调试事件。
func sanitizeTraceImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "data:image/") {
		return truncateRunes(raw, 300)
	}
	header, payload, ok := strings.Cut(raw, ",")
	if !ok {
		return "data:image/<omitted>"
	}
	return fmt.Sprintf("%s,<omitted %d chars>", header, len(payload))
}

func (p *plugin) startTraceServer(cfg config.DebugTraceConfig) {
	if p.trace == nil {
		return
	}
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = "0.0.0.0:18080"
	}

	server, listener, err := p.newTraceServer(addr)
	if err != nil {
		log.Printf("[agent/trace] 调试跟踪页面启动失败: %v", err)
		return
	}
	go func() {
		log.Printf("[agent/trace] 调试跟踪页面已启动: http://%s/ （仅建议在可信内网使用）", traceDisplayAddr(addr))
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent/trace] 调试跟踪页面异常退出: %v", err)
		}
	}()
}

func (p *plugin) newTraceServer(addr string) (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handleTraceIndex)
	mux.HandleFunc("/api/events", p.handleTraceEvents)
	mux.HandleFunc("/api/clear", p.handleTraceClear)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return server, listener, nil
}

func traceDisplayAddr(addr string) string {
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

func (p *plugin) handleTraceIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(tracePageHTML))
}

func (p *plugin) handleTraceEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	after, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("after")), 10, 64)
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	events, last, reset := p.trace.snapshot(after, limit)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"events": events,
		"lastId": last,
		"reset":  reset,
		"stats":  p.trace.stats(),
	})
}

func (p *plugin) handleTraceClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p.trace.clear()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"ok":true}`))
}
