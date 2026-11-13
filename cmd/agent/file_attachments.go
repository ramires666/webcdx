package main

import (
	"encoding/base64"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// MCP embedded resources carry file bytes once, separately from the readable
// metadata. Text remains the default for existing local and Gate clients.
type fileAttachment struct {
	Metadata map[string]any
	Resource map[string]any
}

func attachReadFile(metadata map[string]any, data []byte, name string) fileAttachment {
	mimeType := mime.TypeByExtension(filepath.Ext(name))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	mimeType = strings.Split(mimeType, ";")[0]
	sha := hashBytes(data)
	uri := "urn:helper:sha256:" + sha
	delete(metadata, "content")
	metadata["attachment"] = map[string]any{"uri": uri, "name": name, "mimeType": mimeType, "size": len(data), "sha256": sha}
	return fileAttachment{Metadata: metadata, Resource: map[string]any{"uri": uri, "mimeType": mimeType, "blob": base64.StdEncoding.EncodeToString(data)}}
}
