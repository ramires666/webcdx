package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/jpeg"
	"os"
	"testing"
)

func TestBrowserScreenshotPayloadAndScaledMetadata(t *testing.T) {
	f := newBrowserFixture(t, `<canvas width="1200" height="900"></canvas><script>const c=document.querySelector('canvas'),ctx=c.getContext('2d'),data=ctx.createImageData(c.width,c.height);let seed=99;for(let i=0;i<data.data.length;i+=4){seed^=seed<<13;seed^=seed>>>17;seed^=seed<<5;data.data[i]=seed&255;data.data[i+1]=(seed>>>8)&255;data.data[i+2]=(seed>>>16)&255;data.data[i+3]=255}ctx.putImageData(data,0,0)</script>`)
	f.b.executor.maxResponse = 65536
	img := f.must("browser_screenshot", map[string]any{"save": true, "full_page": true}).(browserImage)
	encoded := toolResult(json.RawMessage(`1`), img, nil)
	var rpc struct {
		Result struct {
			Content    []struct{ Type, Data string }
			Structured map[string]any `json:"structuredContent"`
		}
	}
	if err := json.Unmarshal(encoded, &rpc); err != nil {
		t.Fatal(err)
	}
	if len(rpc.Result.Content) != 2 || rpc.Result.Content[1].Type != "image" {
		t.Fatal("MCP JPEG block missing")
	}
	data, err := base64.StdEncoding.DecodeString(rpc.Result.Content[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	dimensions, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Result["image_width"] != dimensions.Width || img.Result["image_height"] != dimensions.Height || img.Result["image_bytes"] != len(data) || img.Result["image_scaled"] != true || img.Result["image_payload_available"] != true || img.Result["artifact_available"] != true {
		t.Fatalf("incorrect image metadata: %v", img.Result)
	}
	if img.Result["original_image_width"].(int) <= dimensions.Width || img.Result["capture_mode"] != "full_page" {
		t.Fatal("scaling mistaken for viewport crop")
	}
	id := img.Result["operation_id"].(string)
	status := f.must("browser_control", map[string]any{"action": "status", "operation_id": id}).(browserImage)
	if status.Data != img.Data {
		t.Fatal("polling lost image payload")
	}
	// A metadata-only result must never pretend that a client received an image.
	metadata, err := f.b.operationResult(&browserOperation{Result: img.Result, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.(map[string]any)["image_payload_available"] != false {
		t.Fatal("metadata represented as image")
	}
	path := img.Result["artifact_path"].(string)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	metadata, _ = f.b.operationResult(&browserOperation{Result: img.Result, Status: "completed"})
	if metadata.(map[string]any)["artifact_available"] != false {
		t.Fatal("expired artifact still advertised")
	}
	f.b.mu.Lock()
	f.b.trimOperations(0)
	f.b.mu.Unlock()
	_, err = f.b.control(map[string]any{"action": "status", "operation_id": id})
	browserErrorCode(t, err, "unknown_operation")
}
