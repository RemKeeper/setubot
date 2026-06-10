package agent

import (
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
