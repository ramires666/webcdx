package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskServiceQueuesDeduplicatesAndCancels(t *testing.T) {
	var runs atomic.Int32
	started := make(chan string, 4)
	release := make(chan struct{})
	root := filepath.Join(t.TempDir(), "queue")
	s, err := newTaskService(root, 2, func(ctx context.Context, dir string, spec taskSpec) taskView {
		runs.Add(1)
		started <- dir
		select {
		case <-release:
			return taskView{Status: "completed", Output: spec.Prompt}
		case <-ctx.Done():
			return taskView{Status: "cancelled"}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if another, err := newTaskService(root, 2, s.run); err == nil {
		another.close()
		t.Fatal("second scheduler opened the same directory")
	}
	specs := []taskSpec{{Key: "first", Prompt: "first output", Timeout: 10}, {Key: "second", Prompt: "second output", Timeout: 10}, {Key: "third", Prompt: "third output", Timeout: 10}}
	v, err := s.start("owner", "request", specs)
	if err != nil {
		t.Fatal(err)
	}
	id := v.(map[string]any)["batch_id"].(string)
	next := func() string {
		t.Helper()
		select {
		case dir := <-started:
			return dir
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not start")
			return ""
		}
	}
	if a, b := next(), next(); a == b || filepath.Dir(a) != root || filepath.Dir(b) != root {
		t.Fatalf("workspaces were not independent: %q %q", a, b)
	}
	if duplicate, err := s.start("owner", "request", specs); err != nil || duplicate.(map[string]any)["batch_id"] != id {
		t.Fatalf("lost-response retry: %#v, %v", duplicate, err)
	}
	if _, err := s.start("owner", "request", specs[:2]); err == nil {
		t.Fatal("different request reused the same key")
	}
	if _, err := s.lookup("other", id, "get_tasks", ""); err == nil {
		t.Fatal("another owner can get task status")
	}
	if _, err := s.lookup("other", id, "cancel_tasks", ""); err == nil {
		t.Fatal("another owner can cancel tasks")
	}
	if _, err := s.lookup("owner", id, "cancel_tasks", ""); err != nil {
		t.Fatal(err)
	}
	s.workers.Wait()
	if runs.Load() != 2 {
		t.Fatalf("queued task ran after cancellation, runs=%d", runs.Load())
	}
	for _, spec := range specs {
		v, err := s.lookup("owner", id, "task_result", spec.Key)
		if err != nil || v.(taskView).Status != "cancelled" {
			t.Fatalf("unexpected task result: %#v, %v", v, err)
		}
	}
}

func TestTaskServiceRestartDoesNotRelaunch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "queue")
	neverRun := func(context.Context, string, taskSpec) taskView {
		t.Error("persisted request spawned another process")
		return taskView{Status: "failed"}
	}
	s, err := newTaskService(root, 1, neverRun)
	if err != nil {
		t.Fatal(err)
	}
	specs := []taskSpec{{Key: "a", Prompt: "first", Timeout: 10}, {Key: "b", Prompt: "second", Timeout: 10}, {Key: "c", Prompt: "third", Timeout: 10}}
	b := &taskBatch{ID: commandHash([]string{"caller", "same"}), Owner: "caller", Fingerprint: commandHash(specs),
		Jobs: []taskView{{Key: "a", Status: "running"}, {Key: "b", Status: "queued"}, {Key: "c", Status: "completed", Output: "persisted"}}}
	err = s.save(b, true)
	s.close()
	if err != nil {
		t.Fatal(err)
	}
	s, err = newTaskService(root, 1, neverRun)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	v, err := s.start("caller", "same", specs)
	if err != nil {
		t.Fatal(err)
	}
	jobs := v.(map[string]any)["tasks"].([]taskView)
	if jobs[0].Status != "outcome_unknown" || jobs[1].Status != "interrupted" || jobs[2].Output != "" {
		t.Fatalf("invalid recovered batch: %#v", jobs)
	}
	result, err := s.lookup("caller", b.ID, "task_result", "c")
	if err != nil || result.(taskView).Output != "persisted" {
		t.Fatalf("persisted result was not returned: %#v, %v", result, err)
	}
}

func TestTaskToolsRequireTrustedOwner(t *testing.T) {
	e := &nativeExecutor{source: "gate", tasks: &taskService{gate: true}, gateOperationDir: "test-store"}
	raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_webcodex_contract":"v5","name":"task_capabilities","arguments":{}}}`)
	for _, tc := range []struct {
		ctx     context.Context
		allowed bool
	}{
		{context.Background(), false},
		{withTaskOwner(context.Background(), e.gateTaskOwner("client", "agent")), true},
	} {
		result, err := e.call(tc.ctx, raw)
		if err != nil || strings.Contains(string(result), `"isError":false`) != tc.allowed {
			t.Fatalf("unexpected permission result: %s, %v", result, err)
		}
	}
	forged := json.RawMessage(strings.ReplaceAll(string(raw), `"arguments":{}`, `"arguments":{"owner":"client"}`))
	result, err := e.call(withTaskOwner(context.Background(), "other-client"), forged)
	if err != nil || !strings.Contains(string(result), `"isError":true`) {
		t.Fatalf("tool accepted a caller-supplied owner: %s, %v", result, err)
	}
}

func TestTaskEventsDetectErrorEvenAfterText(t *testing.T) {
	w := &taskEventWriter{}
	_, _ = w.Write([]byte(`{"type":"te`))
	_, _ = w.Write([]byte(`xt","part":{"text":"Привет"}}` + "\n"))
	if got := w.text.String(); got != "Привет\n" || w.failed {
		t.Fatalf("invalid decoded output: %q; failed=%t", got, w.failed)
	}
	_, _ = w.Write([]byte(`{"type":"error","error":{"message":"provider error"}}` + "\n"))
	if !w.failed {
		t.Fatal("provider error was ignored")
	}
}
