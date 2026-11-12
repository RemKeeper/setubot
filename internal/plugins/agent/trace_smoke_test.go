package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"setubot/internal/config"
)

func TestSmokeTraceServerStartsAndServes(t *testing.T) {
	cfg := config.DebugTraceConfig{Addr: "127.0.0.1:18099", MaxEvents: 100, MaxBodyChars: 1000}
	p := &plugin{cfg: config.AgentConfig{DebugTrace: cfg}, trace: newTraceStore(cfg)}
	p.startTraceServer(cfg)
	time.Sleep(300 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:18099/api/events?after=0")
	if err != nil {
		t.Fatalf("trace server not reachable: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("bad payload: %v %s", err, body)
	}
	if _, ok := payload["stats"]; !ok {
		t.Fatalf("missing stats: %s", body)
	}
}
