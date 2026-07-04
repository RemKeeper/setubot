package agent

import (
	"image"
	"image/color"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

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

func TestNormalizeImageRotation(t *testing.T) {
	for _, degrees := range []int{0, 90, 180, 270} {
		got, err := normalizeImageRotation(degrees)
		if err != nil {
			t.Fatalf("normalizeImageRotation(%d) unexpected error: %v", degrees, err)
		}
		if got != degrees {
			t.Fatalf("normalizeImageRotation(%d) = %d", degrees, got)
		}
	}
	if _, err := normalizeImageRotation(45); err == nil {
		t.Fatal("expected unsupported rotation to fail")
	}
}

func TestRotateImage90(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	red := color.NRGBA{R: 255, A: 255}
	green := color.NRGBA{G: 255, A: 255}
	blue := color.NRGBA{B: 255, A: 255}
	src.Set(0, 0, red)
	src.Set(1, 0, green)
	src.Set(0, 2, blue)

	dst := rotateImage(src, 90)
	if dst.Bounds().Dx() != 3 || dst.Bounds().Dy() != 2 {
		t.Fatalf("unexpected bounds: %v", dst.Bounds())
	}
	if got := dst.NRGBAAt(2, 0); got != red {
		t.Fatalf("red pixel moved to %v, want %v", got, red)
	}
	if got := dst.NRGBAAt(2, 1); got != green {
		t.Fatalf("green pixel moved to %v, want %v", got, green)
	}
	if got := dst.NRGBAAt(0, 0); got != blue {
		t.Fatalf("blue pixel moved to %v, want %v", got, blue)
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
