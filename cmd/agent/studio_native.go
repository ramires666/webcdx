package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"webcodex/internal/buildinfo"
)

const (
	studioNativeHostName = "org.webcodex.studio"
	studioExtensionID    = "lgiihodklblifecheadnfjkaokmmfikk"
	studioChunkBytes     = 64 << 10
)

type studioRequest struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

// Studio accepts the request frame delivered by Chrome, without a smaller file-size ceiling.
func readStudioNativeMessage(reader io.Reader) ([]byte, error) {
	var length uint32
	if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, errors.New("empty native message")
	}
	data := make([]byte, int(length))
	_, err := io.ReadFull(reader, data)
	if err == nil && !json.Valid(data) {
		err = errors.New("invalid native JSON")
	}
	return data, err
}

func studioNativeHostInvocation(args []string) bool {
	return len(args) > 1 && args[1] == "chrome-extension://"+studioAllowedExtensionID()+"/"
}

func runStudioNativeHost(origin string) error {
	if origin != "chrome-extension://"+studioAllowedExtensionID()+"/" {
		return errors.New("extension origin not allowed")
	}
	log.Printf("webcodex Studio host version=%s", buildinfo.Version)
	roots := studioAllowedRoots()
	if len(roots) == 0 {
		return errors.New("no allowed roots configured")
	}
	logDir := filepath.Join(studioConfigDir(), "logs")
	e, err := newNativeExecutorWithConfig(executorConfig{Source: "local", AllowedRoots: roots, LogDir: logDir, ProcessTTL: durationEnv("WEBCODEX_PROCESS_TTL", 24*time.Hour), MaxLogBytes: int64Env("WEBCODEX_MAX_LOG_BYTES", defaultLogMax), MaxResponseBytes: intEnv("WEBCODEX_MAX_RESPONSE_BYTES", defaultResponseMax), Verbose: env("WEBCODEX_DEBUG", env("WEBCODEX_VERBOSE", "0")) != "0", BrowserBackend: "disabled"})
	if err != nil {
		return err
	}
	defer e.Close()
	h, err := newStudioOperations()
	if err != nil {
		return err
	}
	h.scope = commandHash(map[string]any{"source": "native", "client": studioAllowedExtensionID(), "roots": roots})
	for {
		data, err := readStudioNativeMessage(os.Stdin)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var req studioRequest
		if err := json.Unmarshal(data, &req); err != nil || req.ID == "" || req.Method == "" {
			if err := writeStudioResponse(os.Stdout, "", nil, &studioRPCError{-32600, "invalid request"}); err != nil {
				return err
			}
			continue
		}
		if req.Params == nil {
			req.Params = map[string]any{}
		}
		result, rpcErr := handleStudioRequest(context.Background(), e, h, req)
		if req.Method == "initialize" && rpcErr == nil {
			client, _ := req.Params["clientInfo"].(map[string]any)
			version, _ := client["version"].(string)
			if version == "" {
				version = "версия не передана"
			}
			log.Printf("Studio connected: agent=%s client=%s", buildinfo.Version, version)
		}
		if err := writeStudioResponse(os.Stdout, req.ID, result, rpcErr); err != nil {
			return err
		}
	}
}

type studioRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func handleStudioRequest(ctx context.Context, e *nativeExecutor, h *studioOperations, r studioRequest) (any, *studioRPCError) {
	switch r.Method {
	case "session/set":
		marker, ok := r.Params["marker"].(string)
		decoded, err := hex.DecodeString(marker)
		if !ok || marker != "" && (err != nil || len(decoded) != 16 || strings.ToLower(marker) != marker) {
			return nil, &studioRPCError{-32602, "marker must be 32 lowercase hex characters or empty"}
		}
		if marker == "" {
			log.Print("Сеанс завершён")
		} else {
			log.Printf("ID активного сеанса: %s", marker)
		}
		return map[string]any{"ok": true}, nil
	case "operations/create":
		key, _ := r.Params["key"].(string)
		name, _ := r.Params["name"].(string)
		args, _ := r.Params["arguments"].(map[string]any)
		agentID, _ := r.Params["agentId"].(string)
		if agentID != "" && agentID != "local" {
			return nil, &studioRPCError{-32602, "agent_id must be local"}
		}
		v, err := h.create(ctx, e, key, name, args, agentID)
		if err != nil {
			return nil, &studioRPCError{-32000, err.Error()}
		}
		return v, nil
	case "operations/get":
		id, _ := r.Params["id"].(string)
		v, err := h.get(id)
		if err != nil {
			return nil, &studioRPCError{-32000, err.Error()}
		}
		return v, nil
	case "operations/lookup":
		key, _ := r.Params["key"].(string)
		if strings.TrimSpace(key) == "" {
			return nil, &studioRPCError{-32602, "key is required"}
		}
		v, err := h.get(studioOperationID(key))
		if errors.Is(err, os.ErrNotExist) {
			status := "not_found"
			if studioOperationExpired(key) {
				status = "expired"
			}
			return map[string]any{"status": status}, nil
		}
		if err != nil {
			return nil, &studioRPCError{-32000, err.Error()}
		}
		return v, nil
	case "tools/list", "tools/call", "initialize", "ping", "notifications/initialized":
	default:
		return nil, &studioRPCError{-32601, "method not found"}
	}
	if r.Method == "tools/list" {
		call := map[string]any{"jsonrpc": "2.0", "id": r.ID, "method": r.Method, "params": studioExecutorParams(r.Params)}
		b, _ := json.Marshal(call)
		raw, err := e.call(ctx, b)
		if err != nil {
			return nil, &studioRPCError{-32000, err.Error()}
		}
		var response struct {
			Result map[string]any `json:"result"`
		}
		_ = json.Unmarshal(raw, &response)
		tools, _ := response.Result["tools"].([]any)
		tools = append(tools, map[string]any{"name": "list_agents", "description": "List locally available agents.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}})
		response.Result["tools"] = tools
		return response.Result, nil
	}
	if r.Method == "tools/call" {
		name, _ := r.Params["name"].(string)
		args, _ := r.Params["arguments"].(map[string]any)
		agentID, _ := r.Params["agent_id"].(string)
		if agentID == "" {
			agentID, _ = r.Params["agentId"].(string)
		}
		return studioToolCall(ctx, e, name, args, agentID)
	}
	call := map[string]any{"jsonrpc": "2.0", "id": r.ID, "method": r.Method, "params": studioExecutorParams(r.Params)}
	b, _ := json.Marshal(call)
	raw, err := e.call(ctx, b)
	if err != nil {
		return nil, &studioRPCError{-32000, err.Error()}
	}
	var response struct {
		Result any             `json:"result"`
		Error  *studioRPCError `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, &studioRPCError{-32603, "invalid executor response"}
	}
	if r.Method == "initialize" {
		if result, ok := response.Result.(map[string]any); ok {
			result["studioOperations"] = map[string]any{"lookupByKey": true, "retention": "indefinite", "newKeyMaxAgeSeconds": 86400}
			result["studioSession"] = map[string]any{"marker": true}
			if info, ok := result["serverInfo"].(map[string]any); ok {
				info["version"] = buildinfo.Version
			}
		}
	}
	return response.Result, response.Error
}

func studioExecutorParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	out[contractMarker] = "v5"
	return out
}

func writeStudioResponse(w io.Writer, id string, result any, rpcErr *studioRPCError) error {
	var payload []byte
	var err error
	if rpcErr != nil {
		payload, err = json.Marshal(map[string]any{"id": id, "error": rpcErr})
	} else {
		payload, err = json.Marshal(map[string]any{"id": id, "result": result})
	}
	if err != nil {
		return err
	}
	if len(payload) <= browserBridgeMax {
		return writeNativeMessage(w, payload)
	}
	parts := []string{}
	for len(payload) > 0 {
		n := studioChunkBytes
		if n > len(payload) {
			n = len(payload)
		}
		for n > 0 && n < len(payload) && !utf8.Valid(payload[:n]) {
			n--
		}
		if n == 0 {
			return errors.New("cannot split response")
		}
		parts = append(parts, string(payload[:n]))
		payload = payload[n:]
	}
	for i, part := range parts {
		b, e := json.Marshal(map[string]any{"id": id, "chunk": part, "index": i, "total": len(parts)})
		if e != nil {
			return e
		}
		if e = writeNativeMessage(w, b); e != nil {
			return e
		}
	}
	return nil
}

func studioOperationDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, "webcodex", "studio", "operations")
	if err = os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	if err = privateBrowserDirectory(filepath.Dir(filepath.Dir(p))); err != nil {
		return "", err
	}
	if err = privateBrowserDirectory(filepath.Dir(p)); err != nil {
		return "", err
	}
	_ = os.Chmod(p, 0o700)
	return p, nil
}

type studioOperations struct {
	dir, owner, scope string
	unconfirmed       *sync.Map
}
type studioOperation struct {
	Key       string         `json:"key"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Owner     string         `json:"owner,omitempty"`
	Scope     string         `json:"scope,omitempty"`
	Status    string         `json:"status"`
	Result    any            `json:"result,omitempty"`
}

func newStudioOperations() (*studioOperations, error) {
	d, e := studioOperationDir()
	if e != nil {
		return nil, e
	}
	return newStudioOperationsAt(d)
}
func newStudioOperationsAt(d string) (*studioOperations, error) {
	return &studioOperations{dir: d, owner: browserID("host_"), unconfirmed: &sync.Map{}}, nil
}
func (h *studioOperations) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(h.dir, hex.EncodeToString(sum[:])+".json")
}
func studioOperationID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "op_" + hex.EncodeToString(sum[:])
}
func (h *studioOperations) get(id string) (map[string]any, error) {
	if !strings.HasPrefix(id, "op_") || len(id) != 67 {
		return nil, os.ErrNotExist
	}
	decoded, decodeErr := hex.DecodeString(id[3:])
	if decodeErr != nil || len(decoded) != sha256.Size {
		return nil, os.ErrNotExist
	}
	b, err := readStudioOperationFile(filepath.Join(h.dir, id[3:]+".json"))
	if err != nil {
		return nil, err
	}
	var op studioOperation
	if json.Unmarshal(b, &op) != nil || !studioOperationValid(op, id) {
		return studioOperationResult(studioOperation{ID: id, Status: "outcome_unknown"}), nil
	}
	if op.Scope != "" && op.Scope != h.scope {
		return nil, errors.New("operation belongs to another client or agent")
	}
	return h.result(op), nil
}
func (h *studioOperations) result(op studioOperation) map[string]any {
	var unconfirmed bool
	if h.unconfirmed != nil {
		_, unconfirmed = h.unconfirmed.Load(op.ID)
	}
	if op.Status == "running" && (op.Owner != h.owner || unconfirmed) {
		op.Status = "outcome_unknown"
	}
	return studioOperationResult(op)
}
func studioOperationValid(op studioOperation, id string) bool {
	if op.ID != id || strings.TrimSpace(op.Key) == "" || studioOperationID(op.Key) != id || op.Name == "" {
		return false
	}
	switch op.Status {
	case "completed", "failed":
		return op.Result != nil
	case "running", "outcome_unknown", "expired":
		return true
	default:
		return false
	}
}
func studioOperationExpired(key string) bool {
	// Legacy opaque keys have no age. Their saved records remain valid indefinitely.
	stamp, _, ok := strings.Cut(key, "-")
	if !ok {
		return false
	}
	millis, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || len(stamp) != 13 {
		return false
	}
	return time.Since(time.UnixMilli(millis)) > 24*time.Hour
}
func studioOperationResult(op studioOperation) map[string]any {
	r := map[string]any{"operation_id": op.ID, "status": op.Status}
	if op.Result != nil {
		r["result"] = op.Result
	}

	return r
}
func (h *studioOperations) create(ctx context.Context, e *nativeExecutor, key, name string, args map[string]any, agentID string) (map[string]any, error) {
	if strings.TrimSpace(key) == "" || name == "" {
		return nil, errors.New("key and name are required")
	}
	args = maps.Clone(args)
	if args == nil {
		args = map[string]any{}
	}
	if agentID == "" {
		agentID, _ = args["agent_id"].(string)
	}
	if agentID == "" {
		agentID, _ = args["agentId"].(string)
	}
	if agentID != "" && agentID != "local" {
		return nil, errors.New("agent_id must be local")
	}
	delete(args, "agent_id")
	delete(args, "agentId")
	path := h.path(key)
	payload, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	op := studioOperation{Key: key, ID: studioOperationID(key), Name: name, Arguments: args, Status: "running", Owner: h.owner, Scope: h.scope}
	if studioOperationExpired(key) {
		op.Status = "expired"
	}
	data, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}
	// Publish a complete, synced reservation before any effect or duplicate reader.
	err = atomicCreate(path, data)
	var coded *codedError
	if errors.As(err, &coded) && coded.code == "already_exists" {
		b, e2 := readStudioOperationFile(path)
		if e2 != nil {
			return nil, e2
		}
		var old studioOperation
		if json.Unmarshal(b, &old) != nil || !studioOperationValid(old, studioOperationID(key)) {
			return studioOperationResult(studioOperation{ID: studioOperationID(key), Status: "outcome_unknown"}), nil
		}
		if old.Scope != "" && old.Scope != h.scope {
			return nil, errors.New("operation belongs to another client or agent")
		}
		other, _ := json.Marshal(map[string]any{"name": old.Name, "arguments": old.Arguments})
		if string(payload) != string(other) {
			return nil, errors.New("idempotency key reused with different payload")
		}
		return h.result(old), nil
	}
	if err != nil {
		return nil, err
	}
	if op.Status == "expired" {
		return studioOperationResult(op), nil
	}
	value, callErr := studioToolCall(ctx, e, name, args, agentID)
	if callErr != nil {
		op.Status = "failed"
		if callErr.Code == -32000 {
			op.Status = "outcome_unknown"
		}
		op.Result = callErr
	} else {
		if result, ok := value.(map[string]any); ok && result["isError"] == true {
			op.Status = "failed"
			structured, _ := result["structuredContent"].(map[string]any)
			failure, _ := structured["error"].(map[string]any)
			if failure["code"] == "outcome_unknown" {
				op.Status = "outcome_unknown"
			}
		} else {
			op.Status = "completed"
		}
		op.Result = value
	}
	data, _ = json.Marshal(op)
	if err = atomicWrite(path, data, false); err != nil {
		if h.unconfirmed != nil {
			h.unconfirmed.Store(op.ID, true)
		}
		return studioOperationResult(op), fmt.Errorf("save operation result: %w", err)
	}
	return studioOperationResult(op), nil
}

func studioToolCall(ctx context.Context, e *nativeExecutor, name string, args map[string]any, agentID string) (value any, callErr *studioRPCError) {
	log.Printf("%s — %s", compactString(name, 40), studioCallDescription(name, args))
	defer func() { log.Printf("%s — %s", compactString(name, 40), studioCallSummary(value, callErr)) }()
	if args == nil {
		args = map[string]any{}
	}
	if agentID == "" {
		agentID, _ = args["agent_id"].(string)
	}
	if agentID == "" {
		agentID, _ = args["agentId"].(string)
	}
	if agentID != "" && agentID != "local" {
		return nil, &studioRPCError{-32602, "agent_id must be local"}
	}
	delete(args, "agent_id")
	delete(args, "agentId")
	if name == "list_agents" {
		host, _ := os.Hostname()
		result := map[string]any{"agents": []any{map[string]any{"id": "local", "name": host, "online": true}}}
		b, _ := json.Marshal(result)
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "structuredContent": result}, nil
	}
	if strings.HasPrefix(name, "browser_") {
		return nil, &studioRPCError{-32601, "tool not available"}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "studio-tool", "method": "tools/call", "params": studioExecutorParams(map[string]any{"name": name, "arguments": args})})
	raw, err := e.call(ctx, b)
	if err != nil {
		return nil, &studioRPCError{-32000, err.Error()}
	}
	var response struct {
		Result any             `json:"result"`
		Error  *studioRPCError `json:"error"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, &studioRPCError{-32603, "invalid executor response"}
	}
	return response.Result, response.Error
}
