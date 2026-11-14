package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestReplyExamplesCoverToolCatalog(t *testing.T) {
	data, err := os.ReadFile("../../ai-studio-extension/test-fixtures/reply-v2-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var examples []struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		BodyField string         `json:"bodyField"`
	}
	if err := json.Unmarshal(data, &examples); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, tool := range localTools("v5") {
		properties := tool.InputSchema["properties"].(map[string]any)
		for _, example := range examples {
			if example.Name != tool.Name {
				continue
			}
			seen[tool.Name] = true
			for key := range example.Arguments {
				if _, ok := properties[key]; !ok {
					t.Errorf("%s example has unknown argument %s", tool.Name, key)
				}
			}
			for _, key := range tool.InputSchema["required"].([]string) {
				if _, ok := example.Arguments[key]; !ok && key != "request_key" {
					t.Errorf("%s example is missing %s", tool.Name, key)
				}
			}
			if example.BodyField != "" {
				if _, ok := example.Arguments[example.BodyField].(string); !ok {
					t.Errorf("%s body must be a string", tool.Name)
				}
			}
		}
		if !seen[tool.Name] {
			t.Errorf("missing reply example for %s", tool.Name)
		}
	}
}
