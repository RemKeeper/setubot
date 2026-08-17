package agent

import (
	"strings"
	"testing"

	"setubot/internal/config"

	"github.com/tidwall/gjson"
)

func TestParseHistoryRecordsNormalizesAndFilters(t *testing.T) {
	raw := `{"messages":[
		{"message_id":3,"time":30,"user_id":100,"sender":{"nickname":"用户"},"raw_message":"当前问题"},
		{"message_id":1,"time":10,"user_id":100,"sender":{"nickname":"用户"},"message":[{"type":"text","data":{"text":"你好"}},{"type":"image","data":{"file":"a.jpg"}}]},
		{"message_id":2,"time":20,"user_id":200,"sender":{"nickname":"机器人"},"raw_message":"Agent 正在思考..."},
		{"message_id":4,"time":25,"user_id":200,"sender":{"nickname":"机器人"},"raw_message":"上一次回答"}
	]}`
	records := parseHistoryRecords(gjson.Parse(raw), 200, "3")
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %#v", records)
	}
	if records[0].MessageID != "1" || records[0].Content != "你好[图片]" {
		t.Fatalf("unexpected first record: %#v", records[0])
	}
	if records[1].MessageID != "4" || !records[1].IsSelf {
		t.Fatalf("unexpected self record: %#v", records[1])
	}
}

func TestRenderHistoryBootstrapKeepsRecentRecordsWithinBudget(t *testing.T) {
	records := []historyRecord{
		{UserID: 1, UserName: "甲", Content: strings.Repeat("旧", 20)},
		{UserID: 2, UserName: "乙", Content: "较新"},
		{UserID: 3, UserName: "丙", Content: "最新"},
	}
	got := renderHistoryBootstrap(records, 30)
	if strings.Contains(got, "旧") {
		t.Fatalf("expected old oversized history to be dropped: %q", got)
	}
	if !strings.Contains(got, "较新") || !strings.Contains(got, "最新") {
		t.Fatalf("expected recent history to be kept: %q", got)
	}
}

func TestManualResetSuppressesHistoryBootstrapUntilNewSession(t *testing.T) {
	enabled := true
	p := &plugin{
		cfg:                        config.AgentConfig{HistoryBootstrap: config.HistoryBootstrapConfig{Enabled: &enabled}},
		sessions:                   make(map[string]*conversationSession),
		historyBootstrapSuppressed: make(map[string]bool),
	}
	key := "private:1"
	if !p.canBootstrapHistory(key) {
		t.Fatal("expected an empty session to allow history bootstrap")
	}
	p.clearSessionAndSuppressHistory(key)
	if p.canBootstrapHistory(key) {
		t.Fatal("manual reset must suppress history bootstrap")
	}
	p.appendSession(key, []chatMessage{{Role: "user", Content: "重置后的新问题"}})
	if p.historyBootstrapSuppressed[key] {
		t.Fatal("suppression marker should be removed after the new session is stored")
	}
}
