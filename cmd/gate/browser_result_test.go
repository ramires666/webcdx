package main

import (
	"encoding/json"
	"testing"
)

func TestBrowserFailureDetailsPassThroughGate(t *testing.T) {
	response := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"structuredContent":{"error":{"code":"invalid_option_value","message":"inspect options","details":{"operation_id":"bop_123","frame_id":"frame","recovery":"inspect_options"}}},"content":[{"type":"text","text":"inspect options"}]}}`)
	if got := ensureToolCallResult(response, json.RawMessage(`1`)); string(got) != string(response) {
		t.Fatalf("gate flattened a structured browser failure: %s", got)
	}
}
