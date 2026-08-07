package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
