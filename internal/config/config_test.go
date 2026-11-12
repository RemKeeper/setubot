package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentDebugTraceDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := []byte(`{"agent":{"debug":true}}`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.DebugTrace.Enabled == nil || !*cfg.Agent.DebugTrace.Enabled {
		t.Fatal("debugTrace.enabled should follow agent.debug when unset")
	}
	if cfg.Agent.DebugTrace.Addr != "0.0.0.0:18080" {
		t.Fatalf("unexpected default addr: %q", cfg.Agent.DebugTrace.Addr)
	}
	if cfg.Agent.DebugTrace.MaxEvents != 2000 || cfg.Agent.DebugTrace.MaxBodyChars != 20000 {
		t.Fatalf("unexpected defaults: %#v", cfg.Agent.DebugTrace)
	}
}

func TestAgentDebugTraceExplicitDisable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := []byte(`{"agent":{"debug":true,"debugTrace":{"enabled":false,"addr":"127.0.0.1:9999","maxEvents":10,"maxBodyChars":100}}}`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.DebugTrace.Enabled == nil || *cfg.Agent.DebugTrace.Enabled {
		t.Fatal("explicit debugTrace.enabled=false must be preserved")
	}
	if cfg.Agent.DebugTrace.Addr != "127.0.0.1:9999" || cfg.Agent.DebugTrace.MaxEvents != 10 || cfg.Agent.DebugTrace.MaxBodyChars != 100 {
		t.Fatalf("explicit values not preserved: %#v", cfg.Agent.DebugTrace)
	}
}

func TestUpdateAgentModelPreservesOtherSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := []byte(`{"nickName":["bot"],"agent":{"apiKey":"secret","model":"old","temperature":0.5},"draw":{"enabled":false}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}

	if err := UpdateAgentModel(path, "new-model"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	var agent map[string]json.RawMessage
	if err := json.Unmarshal(result["agent"], &agent); err != nil {
		t.Fatal(err)
	}
	var model, key string
	if err := json.Unmarshal(agent["model"], &model); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(agent["apiKey"], &key); err != nil {
		t.Fatal(err)
	}
	if model != "new-model" || key != "secret" {
		t.Fatalf("unexpected updated config: model=%q key=%q", model, key)
	}
}
