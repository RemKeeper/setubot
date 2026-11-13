package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
)

func boolPtr(value bool) *bool { return &value }

func TestReasoningDoerInjectsIncludeReasoning(t *testing.T) {
	var received map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	doer := newReasoningHTTPDoer(config.ReasoningConfig{Enabled: true, IncludeReasoning: boolPtr(true)})
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := doer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if received["include_reasoning"] != true {
		t.Fatalf("include_reasoning not injected: %#v", received)
	}
	if received["model"] != "m" {
		t.Fatalf("original body lost: %#v", received)
	}
}

func TestReasoningDoerInjectsEffortAndExtra(t *testing.T) {
	var received map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	doer := newReasoningHTTPDoer(config.ReasoningConfig{Enabled: true, Effort: "high", Extra: map[string]interface{}{"reasoning": map[string]interface{}{"enabled": true}}})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	resp, err := doer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if received["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort not injected: %#v", received)
	}
	reasoning, ok := received["reasoning"].(map[string]interface{})
	if !ok || reasoning["enabled"] != true {
		t.Fatalf("extra field not injected: %#v", received)
	}
}

func TestReasoningDoerDoesNotOverrideExistingFields(t *testing.T) {
	var received map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	doer := newReasoningHTTPDoer(config.ReasoningConfig{Enabled: true, Effort: "high"})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","reasoning_effort":"low"}`))
	resp, err := doer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if received["reasoning_effort"] != "low" {
		t.Fatalf("existing reasoning_effort was overridden: %#v", received)
	}
}

func TestReasoningDoerCapturesReasoningFromRawBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"81","reasoning_content":"先算乘法"}}]}`))
	}))
	defer server.Close()

	doer := newReasoningHTTPDoer(config.ReasoningConfig{})
	ctx, capture := withReasoningCapture(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	resp, err := doer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("先算乘法")) {
		t.Fatalf("response body was consumed: %s", body)
	}
	if capture.reasoning != "先算乘法" {
		t.Fatalf("reasoning not captured: %q", capture.reasoning)
	}
}

func TestReasoningDoerSkipsNonChatEndpoints(t *testing.T) {
	var received map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	doer := newReasoningHTTPDoer(config.ReasoningConfig{Enabled: true, Effort: "high"})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/embeddings", strings.NewReader(`{"model":"m"}`))
	resp, err := doer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if _, ok := received["reasoning_effort"]; ok {
		t.Fatalf("non-chat endpoint should not be modified: %#v", received)
	}
}

func TestReasoningDoerFallsBackWhenGatewayRejectsFields(t *testing.T) {
	var attempts int
	var lastBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body, _ := io.ReadAll(r.Body)
		current := map[string]interface{}{}
		_ = json.Unmarshal(body, &current)
		lastBody = current
		if _, ok := current["include_reasoning"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unknown field include_reasoning"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	doer := newReasoningHTTPDoer(config.ReasoningConfig{Enabled: true, IncludeReasoning: boolPtr(true)})
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	resp, err := doer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if attempts != 2 {
		t.Fatalf("expected one retry, got %d attempts", attempts)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fallback did not succeed: %d", resp.StatusCode)
	}
	if _, ok := lastBody["include_reasoning"]; ok {
		t.Fatalf("fallback request still contains injected field: %#v", lastBody)
	}
}

func TestExtractReasoningFromBodyVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"reasoning_content", `{"choices":[{"message":{"reasoning_content":"A"}}]}`, "A"},
		{"reasoning string", `{"choices":[{"message":{"reasoning":"B"}}]}`, "B"},
		{"thinking", `{"choices":[{"message":{"thinking":"C"}}]}`, "C"},
		{"reasoning object", `{"choices":[{"message":{"reasoning":{"text":"D"}}}]}`, "D"},
		{"reasoning_details list", `{"choices":[{"message":{"reasoning_details":[{"text":"E"},{"text":"F"}]}}]}`, "E\nF"},
		{"null", `{"choices":[{"message":{"reasoning_content":null}}]}`, ""},
		{"missing", `{"choices":[{"message":{"content":"x"}}]}`, ""},
		{"invalid", `not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractReasoningFromBody([]byte(tc.body)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTraceModelResponseFallsBackToCapturedReasoning(t *testing.T) {
	p := newTestTracePlugin()
	resp := &openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "81"},
		}},
		Usage: openai.Usage{
			TotalTokens:             10,
			CompletionTokensDetails: &openai.CompletionTokensDetails{ReasoningTokens: 7},
		},
	}
	p.traceModelResponse(nil, "chat_completion", "m", resp, nil, 0, &reasoningCapture{reasoning: "网关补抓的思考"})

	events, _, _ := p.trace.snapshot(0, 0)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Reasoning != "网关补抓的思考" {
		t.Fatalf("captured reasoning not used: %q", events[0].Reasoning)
	}
}

func TestTraceModelResponseHintsWhenReasoningMissing(t *testing.T) {
	p := newTestTracePlugin()
	resp := &openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "81"},
		}},
		Usage: openai.Usage{
			TotalTokens:             10,
			CompletionTokensDetails: &openai.CompletionTokensDetails{ReasoningTokens: 7},
		},
	}
	p.traceModelResponse(nil, "chat_completion", "m", resp, nil, 0, nil)

	events, _, _ := p.trace.snapshot(0, 0)
	joined := ""
	for _, f := range events[0].Fields {
		joined += f.Key + "=" + f.Value + ";"
	}
	if !strings.Contains(joined, "思考tokens=7") || !strings.Contains(joined, "思考提示") {
		t.Fatalf("missing reasoning hint: %s", joined)
	}
}
