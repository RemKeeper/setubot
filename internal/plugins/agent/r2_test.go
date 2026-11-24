package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type mockR2Store struct {
	mu       sync.Mutex
	objects  map[string][]byte
	putCount int
	delCount int
	lastAuth string
}

func newMockR2Server(t *testing.T) (*httptest.Server, *mockR2Store) {
	t.Helper()
	store := &mockR2Store{objects: make(map[string][]byte)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=") {
				http.Error(w, "missing sigv4 auth", http.StatusForbidden)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			sum := sha256.Sum256(body)
			if gotHash := r.Header.Get("X-Amz-Content-Sha256"); gotHash != hex.EncodeToString(sum[:]) {
				http.Error(w, "sha256 mismatch", http.StatusBadRequest)
				return
			}
			store.mu.Lock()
			store.putCount++
			store.lastAuth = auth
			store.objects[r.URL.Path] = body
			store.mu.Unlock()
			w.WriteHeader(http.StatusOK)

		case http.MethodDelete:
			if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=") {
				http.Error(w, "missing sigv4 auth", http.StatusForbidden)
				return
			}
			store.mu.Lock()
			store.delCount++
			delete(store.objects, r.URL.Path)
			store.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		case http.MethodGet:
			if r.URL.Query().Get("X-Amz-Signature") == "" {
				http.Error(w, "missing presign signature", http.StatusForbidden)
				return
			}
			store.mu.Lock()
			data, ok := store.objects[r.URL.Path]
			store.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(data)

		default:
			http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server, store
}

func (s *mockR2Store) counts() (puts int, dels int, live int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putCount, s.delCount, len(s.objects)
}

func TestR2ClientPutPresignAndDelete(t *testing.T) {
	r2Server, store := newMockR2Server(t)

	// 1) 配置了 PublicBaseURL 时返回干净公开域名直链
	publicClient, err := newR2Client(config.R2Config{
		Enabled:         true,
		Endpoint:        r2Server.URL,
		AccessKeyID:     "test-ak",
		SecretAccessKey: "test-sk",
		Bucket:          "search-bucket",
		PublicBaseURL:   "https://img.example.com",
		KeyPrefix:       "setubot-temp",
	}, r2Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	publicClient.nowFunc = func() time.Time {
		return time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	}

	key, err := publicClient.newObjectKey("image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "setubot-temp/20261123/") || !strings.HasSuffix(key, ".png") {
		t.Fatalf("unexpected object key: %q", key)
	}
	directURL, err := publicClient.PutObject(t.Context(), key, tinyPNG, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if directURL != "https://img.example.com/"+key {
		t.Fatalf("unexpected public direct URL: %q", directURL)
	}

	// 2) 未配置 PublicBaseURL 时回退为 S3 SigV4 预签名直链且可直接 GET 访问
	privateClient, err := newR2Client(config.R2Config{
		Enabled:           true,
		Endpoint:          r2Server.URL,
		AccessKeyID:       "test-ak",
		SecretAccessKey:   "test-sk",
		Bucket:            "search-bucket",
		PresignTTLSeconds: 600,
	}, r2Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	presignedURL, err := privateClient.PutObject(t.Context(), key, tinyPNG, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(presignedURL, "X-Amz-Algorithm=AWS4-HMAC-SHA256") || !strings.Contains(presignedURL, "X-Amz-Signature=") {
		t.Fatalf("expected presigned URL, got %q", presignedURL)
	}
	resp, err := r2Server.Client().Get(presignedURL)
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(gotBytes) != string(tinyPNG) {
		t.Fatalf("unexpected presigned GET response: status=%d bytes=%d", resp.StatusCode, len(gotBytes))
	}

	// 3) 删除对象
	if err := publicClient.DeleteObject(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	puts, dels, live := store.counts()
	if puts != 2 || dels != 1 || live != 0 {
		t.Fatalf("unexpected store counts: puts=%d dels=%d live=%d", puts, dels, live)
	}
}

func TestR2ModeNeverExposesRawQQImageURLOnNormalVisionTurn(t *testing.T) {
	r2Server, store := newMockR2Server(t)

	qqServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer qqServer.Close()

	visionAPIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		if strings.Contains(string(bodyBytes), "TOP_SECRET_QQ_RKEY") || strings.Contains(string(bodyBytes), qqServer.URL) {
			t.Errorf("vision model request leaked raw QQ URL: %s", string(bodyBytes))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{{
				Message: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "图里写着 Hello"},
			}},
		})
	}))
	defer visionAPIServer.Close()

	rawQQURL := qqServer.URL + "/download?appid=1406&rkey=TOP_SECRET_QQ_RKEY"
	p := &plugin{
		cfg: config.AgentConfig{
			Vision: config.VisionConfig{
				Enabled:        true,
				Mode:           "tool",
				BaseURL:        visionAPIServer.URL,
				APIKey:         "vision-key",
				Model:          "vision-model",
				MaxImages:      4,
				MaxImageBytes:  1024 * 1024,
				MaxResultChars: 4000,
			},
			R2: config.R2Config{
				Enabled:         true,
				Endpoint:        r2Server.URL,
				AccessKeyID:     "ak",
				SecretAccessKey: "sk",
				Bucket:          "bucket",
				PublicBaseURL:   "https://r2-public.example.com",
			},
		},
		httpClient: qqServer.Client(),
		r2Turns:    make(map[*zero.Ctx]*r2TurnSession),
	}

	ctx := &zero.Ctx{
		Event: &zero.Event{
			UserID:    10001,
			MessageID: 1,
			Message: message.Message{
				message.Text("这张图上写了什么字"),
				{Type: "image", Data: map[string]string{"url": rawQQURL}},
			},
		},
	}

	userMsg := p.userChatMessage(ctx, "这张图上写了什么字")
	if strings.Contains(userMsg.Content, "TOP_SECRET_QQ_RKEY") || strings.Contains(userMsg.Content, qqServer.URL) {
		t.Fatalf("user message leaked raw QQ URL: %q", userMsg.Content)
	}
	puts, _, _ := store.counts()
	if puts != 0 {
		t.Fatalf("non-search OCR prompt should not upload to R2 upfront, got %d puts", puts)
	}

	visionResult, err := p.callAnalyzeImages(ctx, "识别文字")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(visionResult, "TOP_SECRET_QQ_RKEY") || strings.Contains(visionResult, qqServer.URL) {
		t.Fatalf("analyze_images result leaked raw QQ URL: %q", visionResult)
	}
}

func TestRunAgentSearchUploadsToR2OnlyExposesR2LinkAndAutoDeletes(t *testing.T) {
	r2Server, store := newMockR2Server(t)

	qqServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer qqServer.Close()

	rawQQURL := qqServer.URL + "/download?appid=1406&rkey=SUPER_SECRET_RKEY"
	var sawR2LinkInModel bool
	var liveDuringAgent int

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		payload := string(bodyBytes)
		if strings.Contains(payload, "SUPER_SECRET_RKEY") || strings.Contains(payload, qqServer.URL) {
			t.Errorf("LLM payload leaked raw QQ URL: %s", payload)
		}
		if strings.Contains(payload, "https://r2.example.com/setubot-temp/") {
			sawR2LinkInModel = true
		}
		_, _, liveDuringAgent = store.counts()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{{
				Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: "找到出处：https://www.pixiv.net/artworks/123456",
				},
			}},
		})
	}))
	defer llmServer.Close()

	aiCfg := openai.DefaultConfig("test-key")
	aiCfg.BaseURL = llmServer.URL + "/v1"
	aiCfg.HTTPClient = llmServer.Client()

	skillDir := t.TempDir()
	memoryDir := t.TempDir()
	p := &plugin{
		cfg: config.AgentConfig{
			Enabled:             true,
			BaseURL:             llmServer.URL,
			APIKey:              "test-key",
			Model:               "test-model",
			SkillDir:            skillDir,
			MemoryDir:           memoryDir,
			Timeout:             15,
			MaxToolRounds:       5,
			MaxContextTurns:     10,
			MaxContextChars:     100000,
			MaxToolResultChars:  20000,
			SummaryTriggerTurns: 99,
			SummaryKeepTurns:    4,
			HistoryBootstrap:    config.HistoryBootstrapConfig{Enabled: boolPtr(false)},
			Vision: config.VisionConfig{
				Enabled:       true,
				Mode:          "tool",
				MaxImages:     4,
				MaxImageBytes: 1024 * 1024,
			},
			R2: config.R2Config{
				Enabled:         true,
				Endpoint:        r2Server.URL,
				AccessKeyID:     "ak",
				SecretAccessKey: "sk",
				Bucket:          "search-bucket",
				PublicBaseURL:   "https://r2.example.com",
				KeyPrefix:       "setubot-temp",
				Timeout:         10,
			},
		},
		aiClient:                   openai.NewClientWithConfig(aiCfg),
		httpClient:                 r2Server.Client(),
		sessions:                   make(map[string]*conversationSession),
		historyBootstrapSuppressed: make(map[string]bool),
		r2Turns:                    make(map[*zero.Ctx]*r2TurnSession),
	}

	ctx := &zero.Ctx{
		Event: &zero.Event{
			UserID:    20002,
			MessageID: 10,
			Message: message.Message{
				message.Text("帮我找一下这张图的出处"),
				{Type: "image", Data: map[string]string{"url": rawQQURL}},
			},
		},
	}

	answer, err := p.runAgent(ctx, "帮我找一下这张图的出处", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "pixiv.net/artworks/123456") {
		t.Fatalf("unexpected answer: %q", answer)
	}
	if !sawR2LinkInModel {
		t.Fatal("expected model to receive R2 direct link")
	}
	if liveDuringAgent != 1 {
		t.Fatalf("expected 1 live object in R2 while agent was running, got %d", liveDuringAgent)
	}

	puts, dels, liveAfter := store.counts()
	if puts != 1 || dels != 1 || liveAfter != 0 {
		t.Fatalf("expected R2 object to be uploaded once and deleted on exit: puts=%d dels=%d live=%d", puts, dels, liveAfter)
	}

	// 验证会话历史中已将删除后的 R2 直链替换为占位标记，且不含原始 QQ 链接
	session := p.sessions[p.sessionKey(ctx)]
	if session == nil || len(session.messages) == 0 {
		t.Fatal("expected session messages to be saved")
	}
	for _, m := range session.messages {
		if strings.Contains(m.Content, "SUPER_SECRET_RKEY") {
			t.Fatalf("session leaked raw QQ URL: %q", m.Content)
		}
		if strings.Contains(m.Content, "https://r2.example.com/setubot-temp/") {
			t.Fatalf("deleted R2 URL was not scrubbed from session history: %q", m.Content)
		}
	}
}

func TestReadReverseSearchSkillAutoUploadsToR2AndCleansUp(t *testing.T) {
	r2Server, store := newMockR2Server(t)

	qqServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer qqServer.Close()

	skillDir := t.TempDir()
	skillPath := filepath.Join(skillDir, "REVERSE_IMAGE_SEARCH_SKILL.md")
	if err := os.WriteFile(skillPath, []byte("# 以图搜图指南"), 0600); err != nil {
		t.Fatal(err)
	}

	p := &plugin{
		cfg: config.AgentConfig{
			SkillDir: skillDir,
			Vision:   config.VisionConfig{Enabled: true, Mode: "tool", MaxImages: 4, MaxImageBytes: 1024 * 1024},
			R2: config.R2Config{
				Enabled:         true,
				Endpoint:        r2Server.URL,
				AccessKeyID:     "ak",
				SecretAccessKey: "sk",
				Bucket:          "search-bucket",
				PublicBaseURL:   "https://r2.example.com",
				KeyPrefix:       "setubot-temp",
			},
		},
		httpClient: r2Server.Client(),
		r2Turns:    make(map[*zero.Ctx]*r2TurnSession),
	}

	rawQQURL := qqServer.URL + "/download?rkey=HIDDEN_TOKEN"
	ctx := &zero.Ctx{
		Event: &zero.Event{
			UserID:    30003,
			MessageID: 20,
			Message: message.Message{
				message.Text("帮我查查这个"),
				{Type: "image", Data: map[string]string{"url": rawQQURL}},
			},
		},
	}

	p.startR2Turn(ctx)
	out := p.callTool(ctx, "read_skill", `{"name":"REVERSE_IMAGE_SEARCH_SKILL.md"}`)
	if !strings.Contains(out, "https://r2.example.com/setubot-temp/") {
		t.Fatalf("expected read_skill to auto-attach R2 direct link, got: %q", out)
	}
	if strings.Contains(out, "HIDDEN_TOKEN") {
		t.Fatalf("read_skill leaked raw QQ URL: %q", out)
	}

	// 再次调用 prepare_search_images 应当幂等复用同一链接，不重复上传
	prepOut := p.callTool(ctx, "prepare_search_images", `{}`)
	if !strings.Contains(prepOut, "https://r2.example.com/setubot-temp/") {
		t.Fatalf("unexpected prepare_search_images output: %q", prepOut)
	}
	puts, dels, live := store.counts()
	if puts != 1 || dels != 0 || live != 1 {
		t.Fatalf("expected idempotent single upload before cleanup: puts=%d dels=%d live=%d", puts, dels, live)
	}

	p.cleanupR2Turn(ctx, nil)
	puts, dels, live = store.counts()
	if puts != 1 || dels != 1 || live != 0 {
		t.Fatalf("expected cleanup to delete staged object: puts=%d dels=%d live=%d", puts, dels, live)
	}
}

func TestRunAgentDeletesFromR2EvenWhenModelFails(t *testing.T) {
	r2Server, store := newMockR2Server(t)

	qqServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer qqServer.Close()

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failure", http.StatusBadGateway)
	}))
	defer llmServer.Close()

	aiCfg := openai.DefaultConfig("test-key")
	aiCfg.BaseURL = llmServer.URL + "/v1"
	aiCfg.HTTPClient = llmServer.Client()

	p := &plugin{
		cfg: config.AgentConfig{
			Enabled:          true,
			BaseURL:          llmServer.URL,
			APIKey:           "test-key",
			Model:            "test-model",
			SkillDir:         t.TempDir(),
			MemoryDir:        t.TempDir(),
			Timeout:          10,
			MaxToolRounds:    2,
			HistoryBootstrap: config.HistoryBootstrapConfig{Enabled: boolPtr(false)},
			Vision:           config.VisionConfig{Enabled: true, Mode: "tool", MaxImages: 4, MaxImageBytes: 1024 * 1024},
			R2: config.R2Config{
				Enabled:         true,
				Endpoint:        r2Server.URL,
				AccessKeyID:     "ak",
				SecretAccessKey: "sk",
				Bucket:          "search-bucket",
				PublicBaseURL:   "https://r2.example.com",
			},
		},
		aiClient:                   openai.NewClientWithConfig(aiCfg),
		httpClient:                 r2Server.Client(),
		sessions:                   make(map[string]*conversationSession),
		historyBootstrapSuppressed: make(map[string]bool),
		r2Turns:                    make(map[*zero.Ctx]*r2TurnSession),
	}

	ctx := &zero.Ctx{
		Event: &zero.Event{
			UserID:    40004,
			MessageID: 30,
			Message: message.Message{
				message.Text("搜图找一下原图"),
				{Type: "image", Data: map[string]string{"url": qqServer.URL + "/download?rkey=SECRET"}},
			},
		},
	}

	if _, err := p.runAgent(ctx, "搜图找一下原图", nil); err == nil {
		t.Fatal("expected runAgent error when LLM returns 502")
	}

	puts, dels, live := store.counts()
	if puts != 1 || dels != 1 || live != 0 {
		t.Fatalf("expected R2 cleanup even on LLM failure: puts=%d dels=%d live=%d", puts, dels, live)
	}
}
