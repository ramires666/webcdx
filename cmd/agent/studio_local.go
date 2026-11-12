package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"webcodex/internal/buildinfo"
)

const studioLocalFirstPort = 47831
const studioLocalPortCount = 10

func startStudioLocalAccess(ctx context.Context) (func(), error) {
	roots := studioAllowedRoots()
	if len(roots) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		roots = []string{cwd}
	}
	executor, err := newNativeExecutorWithConfig(executorConfig{Source: "local", AllowedRoots: roots, LogDir: filepath.Join(studioConfigDir(), "logs"), BrowserBackend: "disabled"})
	if err != nil {
		return nil, err
	}
	closeListener, err := startStudioLocal(ctx, executor)
	if err != nil {
		executor.Close()
		return nil, err
	}
	return func() { closeListener(); executor.Close() }, nil
}

// Trust the shipped extension and unpacked copies beside this installation.
// Reading Chrome's own extension IDs avoids registry entries and manual pairing.
func studioLocalExtensionIDs(executable string) map[string]bool {
	ids := map[string]bool{studioExtensionID: true}
	trusted := map[string]bool{}
	dir := filepath.Dir(executable)
	for i := 0; i < 4; i++ {
		for _, name := range []string{"ai-studio-extension", "webcodex-ai-studio-bridge"} {
			trusted[filepath.Clean(filepath.Join(dir, name))] = true
		}
		dir = filepath.Dir(dir)
	}
	base := filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "User Data")
	if os.Getenv("LOCALAPPDATA") == "" {
		return ids
	}
	profiles, _ := os.ReadDir(base)
	for _, profile := range profiles {
		if !profile.IsDir() || (profile.Name() != "Default" && !strings.HasPrefix(profile.Name(), "Profile ")) {
			continue
		}
		for _, name := range []string{"Preferences", "Secure Preferences"} {
			data, err := os.ReadFile(filepath.Join(base, profile.Name(), name))
			if err != nil {
				continue
			}
			var prefs struct {
				Extensions struct {
					Settings map[string]struct {
						Path string `json:"path"`
					} `json:"settings"`
				} `json:"extensions"`
			}
			if json.Unmarshal(data, &prefs) != nil {
				continue
			}
			for id, extension := range prefs.Extensions.Settings {
				if studioIDPattern.MatchString(id) && filepath.IsAbs(extension.Path) && trusted[filepath.Clean(extension.Path)] {
					ids[id] = true
				}
			}
		}
	}
	return ids
}

func listenStudioLocal(firstPort, count int) (net.Listener, error) {
	for port := firstPort; port < firstPort+count; port++ {
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return listener, nil
		}
	}
	return nil, errors.New("no free local Studio port")
}

func studioLocalHandler(e *nativeExecutor, operations *studioOperations, allowedIDs map[string]bool, host, identity string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if r.Host != host || !net.ParseIP(ip).IsLoopback() {
			http.Error(w, "loopback requests only", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		id := r.Header.Get("X-WebCodex-Extension")
		if r.Method == http.MethodOptions {
			id = strings.TrimSuffix(strings.TrimPrefix(origin, "chrome-extension://"), "/")
		}
		if !allowedIDs[id] || (origin != "" && origin != "chrome-extension://"+id && origin != "chrome-extension://"+id+"/") {
			http.Error(w, "untrusted extension", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "chrome-extension://"+id)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/studio/rpc" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-WebCodex-Extension, X-WebCodex-Agent")
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
			return
		}
		var req studioRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, browserBridgeMax))
		if err := decoder.Decode(&req); err != nil || req.ID == "" || req.Method == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if supplied := r.Header.Get("X-WebCodex-Agent"); supplied != identity && (supplied != "" || req.Method != "initialize") {
			http.Error(w, "local agent identity changed", http.StatusConflict)
			return
		}
		if req.Params == nil {
			req.Params = map[string]any{}
		}
		// A lost HTTP response must not cancel an operation whose key was persisted.
		result, rpcErr := handleStudioRequest(context.WithoutCancel(r.Context()), e, operations, req)
		if req.Method == "initialize" && rpcErr == nil {
			if values, ok := result.(map[string]any); ok {
				values["localAgent"] = map[string]any{"id": identity}
			}
			client, _ := req.Params["clientInfo"].(map[string]any)
			version, _ := client["version"].(string)
			log.Printf("Studio local connected: agent=%s extension=%s", buildinfo.Version, buildinfo.DisplayPeer(version))
		}
		response := map[string]any{"id": req.ID, "result": result}
		if rpcErr != nil {
			response = map[string]any{"id": req.ID, "error": rpcErr}
		}
		data, err := json.Marshal(response)
		if err != nil || len(data) > 4<<20 {
			data, _ = json.Marshal(map[string]any{"id": req.ID, "error": studioRPCError{-32000, "response exceeds 4 MiB limit"}})
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
}

func startStudioLocal(ctx context.Context, e *nativeExecutor) (func(), error) {
	listener, err := listenStudioLocal(studioLocalFirstPort, studioLocalPortCount)
	if err != nil {
		return nil, err
	}
	operations, err := newStudioOperations()
	if err != nil {
		listener.Close()
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		listener.Close()
		return nil, err
	}
	identity := commandHash(map[string]any{"roots": e.allowedRoots, "unavailable": e.unavailableRoots, "all": e.allowAll})
	server := &http.Server{Handler: studioLocalHandler(e, operations, studioLocalExtensionIDs(executable), listener.Addr().String(), identity), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Studio local listener: %v", err)
		}
	}()
	go func() { <-ctx.Done(); _ = server.Close() }()
	log.Printf("Studio local ready: http://%s agent=%s (Chrome connects automatically)", listener.Addr(), buildinfo.Version)
	return func() { _ = server.Close() }, nil
}
