package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskGroupWaitWakesOnPublicChange(t *testing.T) {
	done := make(chan struct{})
	service, err := newTaskService(t.TempDir(), 1, func(ctx context.Context, _ string, _ taskSpec) taskView {
		select {
		case <-done:
			return taskView{Status: "completed", Output: "hidden result"}
		case <-ctx.Done():
			return taskView{Status: "cancelled"}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()

	value, err := service.start("owner", "run", []taskSpec{{Key: "job", Prompt: "hidden prompt", Timeout: 10}})
	if err != nil {
		t.Fatal(err)
	}
	accepted := value.(map[string]any)
	id := accepted["batch_id"].(string)
	initial := accepted["revision"].(string)
	value, err = service.waitTaskGroup(t.Context(), "owner", id, initial, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	running := value.(map[string]any)
	revision := running["revision"].(string)
	if initial == revision || running["tasks"].([]taskView)[0].Status != "running" {
		t.Fatalf("stale revision did not return current status: %#v", running)
	}
	if _, err := service.waitTaskGroup(t.Context(), "another owner", id, revision, time.Second); err == nil {
		t.Fatal("status exposed to a different owner")
	}

	result := make(chan any, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() {
		status, err := service.waitTaskGroup(ctx, "owner", id, revision, time.Second)
		if err != nil {
			result <- err
		} else {
			result <- status
		}
	}()
	waitForTaskGroupWatch(t, ctx, service, id)
	close(done)
	select {
	case response := <-result:
		status, ok := response.(map[string]any)
		if !ok || status["revision"] == revision {
			t.Fatalf("wait failed to report the transition: %#v", response)
		}
		job := status["tasks"].([]taskView)[0]
		if job.Status != "completed" || job.Output != "" || job.Patch != nil {
			t.Fatalf("invalid public task status: %#v", job)
		}
	case <-ctx.Done():
		t.Fatal("wait did not wake")
	}
}

func waitForTaskGroupWatch(t *testing.T, ctx context.Context, service *taskService, id string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		service.mu.Lock()
		watching := service.watchers[id] != nil
		service.mu.Unlock()
		if watching {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("wait did not register")
		case <-ticker.C:
		}
	}
}

func TestTaskGroupWaitTimeoutAndCancellation(t *testing.T) {
	service, err := newTaskService(t.TempDir(), 1, func(ctx context.Context, _ string, _ taskSpec) taskView {
		<-ctx.Done()
		return taskView{Status: "cancelled"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()
	value, err := service.start("owner", "run", []taskSpec{{Key: "job", Timeout: 10}})
	if err != nil {
		t.Fatal(err)
	}
	id := value.(map[string]any)["batch_id"].(string)
	current, err := service.waitTaskGroup(t.Context(), "owner", id, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	revision := current.(map[string]any)["revision"].(string)
	value, err = service.waitTaskGroup(t.Context(), "owner", id, revision, 20*time.Millisecond)
	if err != nil || value.(map[string]any)["revision"] != revision {
		t.Fatalf("timeout returned incorrect status: %#v, %v", value, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.waitTaskGroup(canceled, "owner", id, revision, time.Second); err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.watchers) != 0 {
		t.Fatal("timed-out wait leaked a subscription")
	}
}

func TestTaskGroupHistoryStableOwnerPagination(t *testing.T) {
	service, err := newTaskService(t.TempDir(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.close()
	owner := "my owner"
	ids := make([]string, 0, 3)
	for i, literal := range []string{"d", "c", "b", "a"} {
		actualOwner := owner
		id := strings.Repeat(literal, 64)
		if i == 2 {
			actualOwner = "different owner"
		} else {
			ids = append([]string{id}, ids...)
		}
		if err := service.save(&taskBatch{ID: id, Owner: actualOwner, Jobs: []taskView{
			{Key: "job", Status: "completed", Output: "hidden result", Patch: &taskPatchView{ProjectRoot: "hidden root"}},
		}}, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(service.root, strings.Repeat("f", 64)+".json"),
		[]byte(`{"batch_id":"wrong","owner":"my owner","tasks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := service.listTaskGroups(owner, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	page := value.(map[string]any)
	groups := page["groups"].([]map[string]any)
	if len(groups) != 2 || groups[0]["batch_id"] != ids[0] || groups[1]["batch_id"] != ids[1] ||
		page["next_cursor"] != ids[1] || groups[0]["counts"].(map[string]int)["completed"] != 1 {
		t.Fatalf("wrong first history page: %#v", page)
	}
	data, err := json.Marshal(page)
	if err != nil || strings.Contains(string(data), "hidden") || strings.Contains(string(data), owner) {
		t.Fatalf("private data appeared in history: %s, %v", data, err)
	}
	status, err := service.waitTaskGroup(t.Context(), owner, ids[0], "", 0)
	if err != nil || groups[0]["revision"] != status.(map[string]any)["revision"] {
		t.Fatalf("history revision did not match status: %#v, %v", status, err)
	}
	value, err = service.listTaskGroups(owner, page["next_cursor"].(string), 2)
	if err != nil {
		t.Fatal(err)
	}
	page = value.(map[string]any)
	groups = page["groups"].([]map[string]any)
	if len(groups) != 1 || groups[0]["batch_id"] != ids[2] || page["next_cursor"] != nil {
		t.Fatalf("wrong last history page: %#v", page)
	}
}
