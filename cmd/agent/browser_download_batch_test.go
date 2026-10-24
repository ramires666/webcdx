package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBrowserBatchDownloadsSaveVerifiedBytesWithoutOverwrite(t *testing.T) {
	if os.Getenv("WEBCODEX_BROWSER_TEST") != "1" {
		t.Skip("set WEBCODEX_BROWSER_TEST=1 for local browser fixtures")
	}
	var source bytes.Buffer
	image := makeTestDownloadPNG()
	if err := png.Encode(&source, image); err != nil {
		t.Fatal(err)
	}
	pngBytes := append([]byte(nil), source.Bytes()...)
	expectedHash := sha256.Sum256(pngBytes)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><title>Fixture</title><div>fixture ready</div><main>`)
			for i := 0; i < 10; i++ {
				_, _ = fmt.Fprintf(w, `<a id="download-%d" href="/image/%d.png" download="image-%d.png">Download image %d</a>`, i, i, i, i)
			}
			_, _ = fmt.Fprint(w, `</main>`)
			return
		}
		if len(r.URL.Path) >= len("/image/") && r.URL.Path[:len("/image/")] == "/image/" {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Disposition", `attachment; filename="source.png"`)
			_, _ = w.Write(pngBytes)
			return
		}
		http.NotFound(w, r)
	})
	f := newBrowserFixtureHandler(t, handler)
	f.must("browser_files", map[string]any{"action": "enable_downloads", "request_key": "batch-downloads-enable"})
	for i := 0; i < 10; i++ {
		f.must("browser_interact", map[string]any{
			"action": "click", "selector": fmt.Sprintf("#download-%d", i), "request_key": fmt.Sprintf("batch-download-click-%d", i),
		})
	}

	var downloads []browserDownload
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		listing := f.must("browser_files", map[string]any{"action": "downloads"}).(map[string]any)
		downloads, _ = listing["downloads"].([]browserDownload)
		completed := 0
		for _, download := range downloads {
			if download.Status == "completed" {
				completed++
			}
		}
		if completed >= 10 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(downloads) != 10 {
		t.Fatalf("tracked %d downloads, want 10", len(downloads))
	}
	root := f.b.executor.allowedRoots[0]
	outputDir := filepath.Join(root, "batch-download-output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seenIDs := map[string]bool{}
	for _, download := range downloads {
		if download.Status != "completed" {
			t.Fatalf("download %s status = %q", download.ID, download.Status)
		}
		if seenIDs[download.ID] {
			t.Fatalf("duplicate download ID %q", download.ID)
		}
		seenIDs[download.ID] = true
		destination := filepath.Join(outputDir, download.ID+".png")
		saved := f.must("browser_files", map[string]any{
			"action": "save_download", "download_id": download.ID, "path": destination, "request_key": "batch-save-" + download.ID,
		}).(map[string]any)
		got, err := os.ReadFile(destination)
		if err != nil {
			t.Fatalf("read saved download %s: %v", download.ID, err)
		}
		gotHash := sha256.Sum256(got)
		if !bytes.Equal(got, pngBytes) || saved["sha256"] != hex.EncodeToString(expectedHash[:]) || saved["sha256"] != hex.EncodeToString(gotHash[:]) {
			t.Fatalf("download %s bytes/hash do not match the local source", download.ID)
		}
		if saved["mime_type"] != "image/png" || saved["width"] != image.Bounds().Dx() || saved["height"] != image.Bounds().Dy() || saved["source_original_verified"] != false {
			t.Fatalf("download %s metadata = %#v", download.ID, saved)
		}
		before, err := os.Stat(destination)
		if err != nil {
			t.Fatal(err)
		}
		retry := f.must("browser_files", map[string]any{
			"action": "save_download", "download_id": download.ID, "path": destination, "request_key": "batch-save-retry-" + download.ID,
		}).(map[string]any)
		after, err := os.Stat(destination)
		if err != nil || retry["duplicate"] != true || !before.ModTime().Equal(after.ModTime()) {
			t.Fatalf("retry rewrote download %s: result=%#v err=%v", download.ID, retry, err)
		}
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 {
		t.Fatalf("output directory has %d files, want 10 distinct complete files", len(entries))
	}
}

func makeTestDownloadPNG() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 13, 7))
	for y := 0; y < 7; y++ {
		for x := 0; x < 13; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 13), G: uint8(y * 21), B: 0x7b, A: 0xff})
		}
	}
	return img
}
