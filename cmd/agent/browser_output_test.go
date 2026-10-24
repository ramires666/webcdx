package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Guard strict output property contracts using actual fixture responses. This
// catches fields added to implementation without corresponding schema changes.
func assertBrowserOutput(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var check func(map[string]any, any, string)
	check = func(schema map[string]any, value any, path string) {
		switch object := value.(type) {
		case map[string]any:
			fields, _ := schema["properties"].(map[string]any)
			for key, item := range object {
				field, ok := fields[key].(map[string]any)
				if !ok {
					if schema["additionalProperties"] == false {
						t.Fatalf("undeclared browser output %s.%s", path, key)
					}
					continue
				}
				check(field, item, path+"."+key)
			}
			if required, ok := schema["required"].([]string); ok {
				for _, key := range required {
					if _, ok := object[key]; !ok {
						t.Fatalf("missing output %s.%s", path, key)
					}
				}
			}
		case []any:
			if item, ok := schema["items"].(map[string]any); ok {
				for i, raw := range object {
					check(item, raw, fmt.Sprintf("%s[%d]", path, i))
				}
			}
		}
	}
	check(browserToolSchemas()[0].OutputSchema, decoded, "result")
}
