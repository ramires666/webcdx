package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/chromedp/chromedp"
)

type browserDownload struct {
	ID     string `json:"download_id"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes"`
}

var downloadGUID = regexp.MustCompile(`^[a-fA-F0-9-]{36}$`)

func (b *browserManager) trackDownloads(s *browserSession) {
	for _, method := range []string{"Browser.downloadWillBegin", "Browser.downloadProgress"} {
		events, stop := chromedp.FromContext(s.ctx).Browser.Subscribe(method)
		go func(method string) {
			defer stop()
			for {
				select {
				case <-s.ctx.Done():
					return
				case raw, ok := <-events:
					if !ok {
						return
					}
					var event struct {
						GUID  string  `json:"guid"`
						State string  `json:"state"`
						Bytes float64 `json:"receivedBytes"`
					}
					if json.Unmarshal(raw, &event) != nil || !downloadGUID.MatchString(event.GUID) {
						continue
					}
					tooLarge := recordManagedDownload(s, method, event.GUID, event.State, int64(event.Bytes))
					b.artifactMu.Lock()
					size, err := b.cleanArtifacts()
					b.artifactMu.Unlock()
					tooLarge = tooLarge || err != nil || size > browserArtifactMax
					if tooLarge {
						ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
						_ = chromedp.FromContext(s.ctx).Browser.Call(ctx, "Browser.cancelDownload", map[string]any{"guid": event.GUID}, nil)
						cancel()
					}
				}
			}
		}(method)
	}
}

// Different CDP event subscriptions run in separate goroutines: progress may
// reach us before willBegin. A late willBegin must not cancel or reset a file.
func recordManagedDownload(s *browserSession, method, id, state string, bytes int64) bool {
	s.downloadMu.Lock()
	defer s.downloadMu.Unlock()
	download := s.downloads[id]
	if download == nil && len(s.downloads) < 16 {
		download = &browserDownload{ID: id, Status: "running"}
		s.downloads[id] = download
	}
	if download == nil {
		return true
	}
	if method == "Browser.downloadProgress" {
		if bytes > download.Bytes {
			download.Bytes = bytes
		}
		if state != "" {
			download.Status = state
		}
	}
	return bytes > browserDownloadMaxBytes
}

func (b *browserManager) files(ctx context.Context, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	if s.personal != nil {
		return b.personalFiles(ctx, s, t, args)
	}
	action, _ := requiredString(args, "action")
	dir := filepath.Join(b.config.Dir, "artifacts", s.id)
	switch action {
	case "enable_downloads":
		b.artifactMu.Lock()
		size, err := b.cleanArtifacts()
		b.artifactMu.Unlock()
		if err != nil {
			return nil, toolErr("io_error", "inspect artifacts", err)
		}
		if size >= browserArtifactMax {
			return nil, toolErr("limit_reached", "browser artifact budget reached")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, toolErr("io_error", "create download directory", err)
		}
		if err := chromedp.FromContext(s.ctx).Browser.Call(ctx, "Browser.setDownloadBehavior", map[string]any{"behavior": "allowAndName", "downloadPath": dir, "eventsEnabled": true}, nil); err != nil {
			return nil, toolErr("browser_failed", "enable downloads")
		}
		return map[string]any{}, nil
	case "downloads":
		s.downloadMu.Lock()
		defer s.downloadMu.Unlock()
		list := []browserDownload{}
		for _, download := range s.downloads {
			list = append(list, *download)
		}
		return map[string]any{"downloads": list}, nil
	case "save_download":
		id, err := requiredString(args, "download_id")
		if err != nil {
			return nil, err
		}
		s.downloadMu.Lock()
		download := s.downloads[id]
		ready := download != nil && download.Status == "completed"
		s.downloadMu.Unlock()
		if !ready {
			return nil, toolErr("download_not_ready", "download is not complete in this session")
		}
		raw, err := requiredString(args, "path")
		if err != nil {
			return nil, err
		}
		path, err := b.executor.resolvePath(raw)
		if err != nil {
			return nil, err
		}
		return saveBrowserDownloadedFile(filepath.Join(dir, id), path, id)
	case "upload":
		return b.upload(ctx, t, args)
	}
	return nil, toolErr("invalid_arguments", "unknown files action")
}

func (b *browserManager) upload(ctx context.Context, t *browserTab, args map[string]any) (any, error) {
	raw, err := requiredString(args, "path")
	if err != nil {
		return nil, err
	}
	path, err := b.executor.resolvePath(raw)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return nil, toolErr("invalid_path", "upload requires a regular permitted file up to 64 MiB")
	}
	locator, err := b.locator(t, args)
	if err != nil {
		return nil, err
	}
	frameID, _ := optionalString(args, "frame_id")
	if err := b.eval(ctx, t, frameID, `(()=>{`+locator+`if(el.tagName!=='INPUT'||el.type!=='file')throw Error('not file input');return true})()`, nil); err != nil {
		return nil, err
	}
	frame, err := b.pageFrame(ctx, t, frameID)
	if err != nil {
		return nil, err
	}
	id, _ := frame["id"].(string)
	t.eventMu.Lock()
	world := t.worlds[id]
	t.eventMu.Unlock()
	var res struct {
		Result struct {
			ID string `json:"objectId"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := t.commandFrame(ctx, id, "Runtime.evaluate", map[string]any{"expression": `(()=>{` + locator + `return el})()`, "contextId": world, "silent": true}, &res); err != nil {
		return nil, err
	}
	if len(res.Exception) > 0 || res.Result.ID == "" {
		return nil, toolErr("stale_element", "file input unavailable")
	}
	return map[string]any{"path": path}, t.commandFrame(ctx, id, "DOM.setFileInputFiles", map[string]any{"objectId": res.Result.ID, "files": []string{path}}, nil)
}
