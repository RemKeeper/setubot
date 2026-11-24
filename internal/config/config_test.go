package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentReasoningDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"reasoning":{"enabled":true}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Reasoning.IncludeReasoning == nil || !*cfg.Agent.Reasoning.IncludeReasoning {
		t.Fatal("includeReasoning should default to true when reasoning.enabled")
	}
	if cfg.Agent.Reasoning.Effort != "" {
		t.Fatalf("unexpected default effort: %q", cfg.Agent.Reasoning.Effort)
	}
}

func TestAgentReasoningNormalizesEffort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"reasoning":{"enabled":true,"effort":" HIGH "}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Reasoning.Effort != "high" {
		t.Fatalf("effort not normalized: %q", cfg.Agent.Reasoning.Effort)
	}
}

func TestAgentReasoningRejectsUnknownEffort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"reasoning":{"enabled":true,"effort":"extreme"}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Reasoning.Effort != "" {
		t.Fatalf("unknown effort should be cleared, got %q", cfg.Agent.Reasoning.Effort)
	}
}

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

func TestAgentR2Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"r2":{"enabled":true}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r2 := cfg.Agent.R2
	if !r2.Enabled {
		t.Fatal("expected r2.enabled to be true")
	}
	if r2.AccountIDEnv != "R2_ACCOUNT_ID" || r2.AccessKeyIDEnv != "R2_ACCESS_KEY_ID" || r2.SecretAccessKeyEnv != "R2_SECRET_ACCESS_KEY" || r2.BucketEnv != "R2_BUCKET" {
		t.Fatalf("unexpected r2 env defaults: %#v", r2)
	}
	if r2.Region != "auto" || r2.KeyPrefix != "setubot-temp" || r2.PresignTTLSeconds != 900 || r2.Timeout != 30 {
		t.Fatalf("unexpected r2 value defaults: %#v", r2)
	}
}
