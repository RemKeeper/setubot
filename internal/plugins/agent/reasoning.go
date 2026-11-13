package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"setubot/internal/config"
)

// reasoningCapture 保存一次模型调用中被网关额外返回的思考文本。
// 部分 OpenAI 兼容网关把思考放在 reasoning / thinking / reasoning_details 等
// 非标准字段里，go-openai 不会解析，需要从原始响应体里补抓。
type reasoningCapture struct {
	reasoning string
}

type reasoningCaptureKey struct{}

func withReasoningCapture(ctx context.Context) (context.Context, *reasoningCapture) {
	capture := &reasoningCapture{}
	return context.WithValue(ctx, reasoningCaptureKey{}, capture), capture
}

func reasoningCaptureFrom(ctx context.Context) *reasoningCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(reasoningCaptureKey{}).(*reasoningCapture)
	return capture
}

// reasoningHTTPDoer 在请求体里注入思考相关参数，并从原始响应体中补抓思考文本。
// 不设置超时，保持与 go-openai 默认客户端一致的行为，避免长生成被中断。
type reasoningHTTPDoer struct {
	base http.Client
	cfg  config.ReasoningConfig
}

func newReasoningHTTPDoer(cfg config.ReasoningConfig) *reasoningHTTPDoer {
	return &reasoningHTTPDoer{cfg: cfg}
}

func (d *reasoningHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	if req == nil {
		return d.base.Do(req)
	}
	isChat := strings.Contains(req.URL.Path, "/chat/completions")
	if !isChat {
		return d.base.Do(req)
	}

	original, err := readAndRestoreBody(req)
	if err != nil {
		return nil, err
	}
	injected, changed, err := d.injectRequestFields(original)
	if err != nil {
		return nil, err
	}
	if changed {
		setRequestBody(req, injected)
	}

	resp, err := d.base.Do(req)
	if err != nil {
		return resp, err
	}
	// 网关不认识注入字段时（400/422）自动回退到原始请求体重试一次，
	// 避免因为思考参数导致整轮对话失败。
	if changed && resp != nil && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity) {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		setRequestBody(req, original)
		resp, err = d.base.Do(req)
		if err != nil {
			return resp, err
		}
	}

	if resp == nil || resp.Body == nil {
		return resp, err
	}
	capture := reasoningCaptureFrom(req.Context())
	if capture == nil {
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if reasoning := extractReasoningFromBody(body); reasoning != "" {
		capture.reasoning = reasoning
	}
	return resp, nil
}

func readAndRestoreBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	setRequestBody(req, raw)
	return raw, nil
}

func setRequestBody(req *http.Request, raw []byte) {
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.ContentLength = int64(len(raw))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
}

// injectRequestFields 把思考参数合并进请求体，返回注入后的请求体与是否发生改动。
func (d *reasoningHTTPDoer) injectRequestFields(raw []byte) ([]byte, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, false, nil
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw, false, nil
	}
	changed := false
	if d.cfg.Enabled {
		if d.cfg.IncludeReasoning != nil && *d.cfg.IncludeReasoning {
			if _, ok := payload["include_reasoning"]; !ok {
				payload["include_reasoning"] = true
				changed = true
			}
		}
		if d.cfg.Effort != "" {
			if _, ok := payload["reasoning_effort"]; !ok {
				payload["reasoning_effort"] = d.cfg.Effort
				changed = true
			}
		}
		for key, value := range d.cfg.Extra {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if _, ok := payload[key]; ok {
				continue
			}
			payload[key] = value
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}

	updated, err := json.Marshal(payload)
	if err != nil {
		return raw, false, err
	}
	return updated, true, nil
}

// extractReasoningFromBody 从原始响应体中提取思考文本，兼容多种网关字段。
func extractReasoningFromBody(body []byte) string {
	var payload struct {
		Choices []struct {
			Message map[string]json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Choices) == 0 {
		return ""
	}
	message := payload.Choices[0].Message
	for _, key := range []string{"reasoning_content", "reasoning", "thinking", "reasoning_details"} {
		raw, ok := message[key]
		if !ok {
			continue
		}
		if text := reasoningTextFromRaw(raw); text != "" {
			return text
		}
	}
	return ""
}

func reasoningTextFromRaw(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}

	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return strings.TrimSpace(text)
	}

	var object struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(trimmed, &object); err == nil {
		if value := strings.TrimSpace(object.Text); value != "" {
			return value
		}
		if value := strings.TrimSpace(object.Content); value != "" {
			return value
		}
	}

	var list []json.RawMessage
	if err := json.Unmarshal(trimmed, &list); err == nil {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			if value := reasoningTextFromRaw(item); value != "" {
				parts = append(parts, value)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}

	return ""
}
