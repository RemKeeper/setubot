package agent

import (
	"strings"
	"testing"

	"setubot/internal/config"

	openai "github.com/sashabaranov/go-openai"
	"github.com/wdvxdr1123/ZeroBot/message"
)

func TestMessagePartsPreservesTextImageOrder(t *testing.T) {
	p := &plugin{cfg: config.AgentConfig{Vision: config.VisionConfig{Enabled: true, Detail: "high"}}}
	msg := message.Message{
		message.Text("before"),
		{Type: "image", Data: map[string]string{"url": "https://example.com/a.jpg"}},
		message.Text("after"),
	}

	parts := p.messageParts(msg)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	if parts[0].Text != "before" || parts[1].Type != openai.ChatMessagePartTypeImageURL || parts[2].Text != "after" {
		t.Fatalf("unexpected multimodal parts: %#v", parts)
	}
	if parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/a.jpg" {
		t.Fatalf("unexpected image part: %#v", parts[1])
	}
	if parts[1].ImageURL.Detail != openai.ImageURLDetailHigh {
		t.Fatalf("unexpected image detail: %q", parts[1].ImageURL.Detail)
	}
}

func TestMessagePartsDropsImagesWhenVisionDisabled(t *testing.T) {
	p := &plugin{}
	msg := message.Message{
		message.Text("before"),
		{Type: "image", Data: map[string]string{"url": "https://example.com/a.jpg"}},
		message.Text("after"),
	}

	parts := p.messageParts(msg)
	if len(parts) != 2 || parts[0].Text != "before" || parts[1].Text != "after" {
		t.Fatalf("unexpected parts with vision disabled: %#v", parts)
	}
}

func TestAgentImageSourceNormalizesOneBotSources(t *testing.T) {
	httpImage := message.Segment{Type: "image", Data: map[string]string{"url": "https://example.com/a.jpg?a=1&amp;b=2"}}
	if got := agentImageSource(httpImage); got != "https://example.com/a.jpg?a=1&b=2" {
		t.Fatalf("unexpected HTTP image source: %q", got)
	}

	base64Image := message.Segment{Type: "image", Data: map[string]string{"file": "base64://YWJj"}}
	if got := agentImageSource(base64Image); got != "data:image/jpeg;base64,YWJj" {
		t.Fatalf("unexpected base64 image source: %q", got)
	}

	localImage := message.Segment{Type: "image", Data: map[string]string{"file": `C:\temp\a.jpg`}}
	if got := agentImageSource(localImage); got != "" {
		t.Fatalf("expected local image path to be rejected, got %q", got)
	}
}

func TestChatMessageDisplayTextMarksImages(t *testing.T) {
	msg := chatMessage{Role: openai.ChatMessageRoleUser, MultiContent: []openai.ChatMessagePart{
		{Type: openai.ChatMessagePartTypeText, Text: "看看"},
		{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: "https://example.com/a.jpg"}},
	}}
	if got := chatMessageDisplayText(msg); got != "看看[图片]" {
		t.Fatalf("unexpected display text: %q", got)
	}
}

func TestSanitizeToolMessagePairsDropsOrphanTool(t *testing.T) {
	messages := []chatMessage{
		{Role: openai.ChatMessageRoleUser, Content: "first"},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "call_1", Content: "orphan"},
		{Role: openai.ChatMessageRoleUser, Content: "second"},
	}

	cleaned := sanitizeToolMessagePairs(messages)
	if len(cleaned) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(cleaned))
	}
	if cleaned[0].Role != openai.ChatMessageRoleUser || cleaned[1].Content != "second" {
		t.Fatalf("unexpected cleaned messages: %#v", cleaned)
	}
}

func TestCompactMessagesForSessionTruncatesToolResult(t *testing.T) {
	messages := []chatMessage{{Role: openai.ChatMessageRoleTool, ToolCallID: "call_1", Content: strings.Repeat("x", 10000)}}
	compacted := compactMessagesForSession(messages)
	if len([]rune(compacted[0].Content)) > 4003 {
		t.Fatalf("tool result was not compacted: %d chars", len([]rune(compacted[0].Content)))
	}
}

func TestFitMessagesToCharBudgetDropsOldLargeToolGroup(t *testing.T) {
	messages := []chatMessage{
		{Role: openai.ChatMessageRoleSystem, Content: "system"},
		{Role: openai.ChatMessageRoleAssistant, Content: " ", ToolCalls: []openai.ToolCall{{ID: "old"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "old", Content: strings.Repeat("x", 10000)},
		{Role: openai.ChatMessageRoleUser, Content: "latest question"},
	}
	fitted := fitMessagesToCharBudget(messages, 1000)
	if chatMessagesChars(fitted) > 1000 {
		t.Fatalf("messages exceed budget: %d", chatMessagesChars(fitted))
	}
	if fitted[len(fitted)-1].Content != "latest question" {
		t.Fatalf("latest message was not preserved: %#v", fitted)
	}
	for _, msg := range fitted {
		if msg.ToolCallID == "old" {
			t.Fatal("old oversized tool result should have been dropped")
		}
	}
}

func TestCompactToolResultAddsTruncationNotice(t *testing.T) {
	p := &plugin{}
	result := p.compactToolResult("browser_html", strings.Repeat("a", 100), 20)
	if !strings.Contains(result, "工具结果已截断") || !strings.Contains(result, "browser_html") {
		t.Fatalf("missing truncation metadata: %q", result)
	}
}

func TestSanitizeToolMessagePairsKeepsCompleteGroup(t *testing.T) {
	messages := []chatMessage{
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "call_1"}, {ID: "call_2"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "call_1", Content: "one"},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "call_2", Content: "two"},
		{Role: openai.ChatMessageRoleAssistant, Content: "done"},
	}

	cleaned := sanitizeToolMessagePairs(messages)
	if len(cleaned) != 4 {
		t.Fatalf("expected complete group to be kept, got %d messages", len(cleaned))
	}
	if cleaned[0].Role != openai.ChatMessageRoleAssistant || cleaned[3].Content != "done" {
		t.Fatalf("unexpected cleaned messages: %#v", cleaned)
	}
}

func TestSanitizeToolMessagePairsDropsIncompleteGroup(t *testing.T) {
	messages := []chatMessage{
		{Role: openai.ChatMessageRoleUser, Content: "before"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "call_1"}, {ID: "call_2"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "call_1", Content: "one"},
		{Role: openai.ChatMessageRoleUser, Content: "after"},
	}

	cleaned := sanitizeToolMessagePairs(messages)
	if len(cleaned) != 2 {
		t.Fatalf("expected incomplete group to be dropped, got %d messages", len(cleaned))
	}
	if cleaned[0].Content != "before" || cleaned[1].Content != "after" {
		t.Fatalf("unexpected cleaned messages: %#v", cleaned)
	}
}

func TestNormalizeChatMessageAddsContentForToolCalls(t *testing.T) {
	message := normalizeChatMessage(chatMessage{
		Role:      openai.ChatMessageRoleAssistant,
		ToolCalls: []openai.ToolCall{{ID: "call_1"}},
	})

	if message.Content == "" {
		t.Fatal("expected assistant tool-call message content to be non-empty")
	}
}

func TestNormalizeChatMessageAddsContentForToolResult(t *testing.T) {
	message := normalizeChatMessage(chatMessage{
		Role:       openai.ChatMessageRoleTool,
		ToolCallID: "call_1",
	})

	if message.Content == "" {
		t.Fatal("expected tool message content to be non-empty")
	}
}

func TestToolResultReturnsNonEmptySuccess(t *testing.T) {
	if got := toolResult("", nil); got == "" {
		t.Fatal("expected empty successful tool result to be replaced")
	}
}

func TestTaskGuardActive(t *testing.T) {
	p := &plugin{}
	if p.taskGuardActive("请分批发送全部图片") {
		t.Fatal("expected disabled task guard to stay inactive")
	}
	p.cfg.TaskGuard.Enabled = true
	p.cfg.TaskGuard.LongTaskKeywords = []string{"分批", "直到完成"}
	if !p.taskGuardActive("请分批发送全部图片") {
		t.Fatal("expected keyword to activate task guard")
	}
	if p.taskGuardActive("普通聊天") {
		t.Fatal("expected unrelated prompt to stay inactive")
	}
}

func TestEHGalleryCacheParts(t *testing.T) {
	gid, token := ehGalleryCacheParts("https://exhentai.org/g/4022117/22c0b08fdc/")
	if gid != "4022117" || token != "22c0b08fdc" {
		t.Fatalf("unexpected gallery cache parts: %q %q", gid, token)
	}
	gid, token = ehGalleryCacheParts("not a gallery URL")
	if gid != "unknown" || token != "unknown" {
		t.Fatalf("expected unknown parts, got %q %q", gid, token)
	}
}

func TestSafeEHCachePart(t *testing.T) {
	if got := safeEHCachePart("abc/../DEF_123-456!"); got != "abcDEF_123-456" {
		t.Fatalf("unexpected safe cache part: %q", got)
	}
	if got := safeEHCachePart("../"); got != "unknown" {
		t.Fatalf("expected unknown for empty safe part, got %q", got)
	}
}

func TestSplitImageBatches(t *testing.T) {
	batches := splitImageBatches([]string{"a", "b", "c", "d", "e"}, 2)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	wants := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	for i := range wants {
		if len(batches[i]) != len(wants[i]) {
			t.Fatalf("batch %d length = %d, want %d", i, len(batches[i]), len(wants[i]))
		}
		for j := range wants[i] {
			if batches[i][j] != wants[i][j] {
				t.Fatalf("batch %d item %d = %q, want %q", i, j, batches[i][j], wants[i][j])
			}
		}
	}
	if got := splitImageBatches([]string{"a"}, 0); got != nil {
		t.Fatalf("expected nil for invalid batch size, got %#v", got)
	}
}
