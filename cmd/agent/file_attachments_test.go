package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileAttachmentPreservesBytesAndDefaultText(t *testing.T) {
	e, root := testExecutor(t)
	for _, sample := range []struct {
		name string
		data []byte
	}{{"код.py", []byte("print('Привет 😀')\r\n")}, {"document.pdf", []byte("%PDF-1.7\n\x00\xff\xfePDF bytes")}, {"empty.txt", nil}} {
		path := filepath.Join(root, sample.name)
		if err := os.WriteFile(path, sample.data, 0600); err != nil {
			t.Fatal(err)
		}
		value, err := e.readFile(map[string]any{"path": path, "delivery": "file"})
		if err != nil {
			t.Fatal(err)
		}
		file := value.(fileAttachment)
		encoded := toolResult(json.RawMessage(`1`), file, nil)
		var response struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Type     string         `json:"type"`
					Resource map[string]any `json:"resource"`
				} `json:"content"`
				Structured map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatal(err)
		}
		if response.Result.IsError || len(response.Result.Content) != 2 || response.Result.Content[1].Type != "resource" {
			t.Fatalf("wrong attachment envelope: %s", encoded)
		}
		got, err := base64.StdEncoding.DecodeString(response.Result.Content[1].Resource["blob"].(string))
		if err != nil || !bytes.Equal(got, sample.data) {
			t.Fatal("file bytes changed")
		}
		meta := response.Result.Structured["attachment"].(map[string]any)
		if meta["sha256"] != hashBytes(sample.data) || meta["name"] != sample.name {
			t.Fatal(meta)
		}
		if _, exists := response.Result.Structured["content"]; exists {
			t.Fatal("file content duplicated as text")
		}
		if !isBinary(sample.data) {
			text, err := e.readFile(map[string]any{"path": path})
			if err != nil || text.(map[string]any)["content"] != string(sample.data) {
				t.Fatal("default text read changed", err)
			}
		}
	}
}

func TestFileAttachmentRangesAndRootPolicy(t *testing.T) {
	e, root := testExecutor(t)
	path := filepath.Join(root, "data.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := e.readFile(map[string]any{"path": path, "offset": float64(2), "limit": float64(1), "delivery": "file"})
	if err != nil {
		t.Fatal(err)
	}
	file := value.(fileAttachment)
	data, _ := base64.StdEncoding.DecodeString(file.Resource["blob"].(string))
	if string(data) != "two\n" || file.Metadata["truncated"] != true {
		t.Fatal(file.Metadata)
	}
	outside := filepath.Join(t.TempDir(), "private.pdf")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.readFile(map[string]any{"path": outside, "delivery": "file"}); err == nil {
		t.Fatal("attachment bypassed allowed roots")
	}
	if _, err := e.readFile(map[string]any{"path": path, "delivery": "unknown"}); err == nil {
		t.Fatal("invalid delivery accepted")
	}
}
