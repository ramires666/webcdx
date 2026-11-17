package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskDependenciesScheduleReadyBranchesAndBlockFailures(t *testing.T) {
	started := make(chan taskSpec, 8)
	release := map[string]chan string{}
	for _, key := range []string{"base", "peer", "fail", "cancel", "join", "blocked", "grandchild", "after-cancel"} {
		release[key] = make(chan string, 1)
	}
	s, err := newTaskService(filepath.Join(t.TempDir(), "queue"), 4, func(ctx context.Context, _ string, spec taskSpec) taskView {
		started <- spec
		select {
		case status := <-release[spec.Key]:
			return taskView{Status: status, Output: spec.Prompt}
		case <-ctx.Done():
			return taskView{Status: "cancelled"}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	specs := []taskSpec{
		{Key: "grandchild", Timeout: 10, DependsOn: []string{"blocked"}},
		{Key: "join", Timeout: 10, DependsOn: []string{"base", "peer"}},
		{Key: "base", Prompt: strings.Repeat("😀<", 4000), Timeout: 10},
		{Key: "blocked", Timeout: 10, DependsOn: []string{"fail"}},
		{Key: "peer", Prompt: "peer output", Timeout: 10},
		{Key: "fail", Timeout: 10},
		{Key: "cancel", Timeout: 10},
		{Key: "after-cancel", Timeout: 10, DependsOn: []string{"cancel"}},
	}
	value, err := s.start("owner", "dag", specs)
	if err != nil {
		t.Fatal(err)
	}
	id := value.(map[string]any)["batch_id"].(string)
	poll := func(key string) taskView {
		t.Helper()
		value, err := s.lookup("owner", id, "task_result", key)
		if err != nil {
			t.Fatal(err)
		}
		return value.(taskView)
	}
	next := func() taskSpec {
		t.Helper()
		select {
		case spec := <-started:
			return spec
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not start")
			return taskSpec{}
		}
	}
	roots := map[string]bool{"base": true, "peer": true, "fail": true, "cancel": true}
	for range len(roots) {
		spec := next()
		if !roots[spec.Key] || spec.DependencyContext != "" {
			t.Fatalf("unexpected root job: %q", spec.Key)
		}
		delete(roots, spec.Key)
	}
	if job := poll("join"); job.Status != "waiting" {
		t.Fatalf("join status = %q, want waiting", job.Status)
	}
	release["base"] <- "completed"
	release["fail"] <- "failed"
	release["cancel"] <- "cancelled"
	deadline := time.Now().Add(5 * time.Second)
	for (poll("grandchild").Status == "waiting" || poll("after-cancel").Status == "waiting") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if job := poll("blocked"); job.Status != "blocked" || !strings.Contains(job.Error, "fail") {
		t.Fatalf("unexpected status for failed branch: %#v", job)
	}
	if job := poll("grandchild"); job.Status != "blocked" || !strings.Contains(job.Error, "blocked") {
		t.Fatalf("unexpected status for transitive dependent: %#v", job)
	}
	if job := poll("after-cancel"); job.Status != "blocked" || !strings.Contains(job.Error, "cancelled") {
		t.Fatalf("unexpected status for cancelled branch: %#v", job)
	}
	if job := poll("join"); job.Status != "waiting" {
		t.Fatalf("join started before both prerequisites completed: %#v", job)
	}
	release["peer"] <- "completed"
	joined := next()
	var outputs []struct {
		Key       string
		Output    string
		Truncated bool
	}
	if joined.Key != "join" || len(joined.DependencyContext) > taskDependencyContextMax ||
		json.Unmarshal([]byte(joined.DependencyContext), &outputs) != nil || len(outputs) != 2 {
		t.Fatalf("unexpected dependent input for %q", joined.Key)
	}
	if outputs[0].Key != "base" || !outputs[0].Truncated || !strings.HasPrefix(outputs[0].Output, "😀<") ||
		outputs[1].Key != "peer" || outputs[1].Output != "peer output" || outputs[1].Truncated {
		t.Fatal("dependent did not receive bounded outputs from both predecessors")
	}
	release["join"] <- "completed"
	s.workers.Wait()
	if job := poll("join"); job.Status != "completed" || len(job.DependsOn) != 2 {
		t.Fatalf("completed dependent lost its dependencies: %#v", job)
	}
}

func TestTaskDependenciesRecoverWithoutRelaunch(t *testing.T) {
	s, err := newTaskService(filepath.Join(t.TempDir(), "queue"), 1, func(context.Context, string, taskSpec) taskView {
		t.Error("persisted request spawned another process")
		return taskView{Status: "failed"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	batch := &taskBatch{ID: commandHash([]string{"owner", "restart"}), Owner: "owner", Jobs: []taskView{
		{Key: "dependent", Status: "waiting", DependsOn: []string{"active"}},
		{Key: "active", Status: "running"},
		{Key: "ready", Status: "queued", DependsOn: []string{"done"}},
		{Key: "done", Status: "completed", Output: "preserved output"},
	}}
	if err := s.save(batch, true); err != nil {
		t.Fatal(err)
	}
	value, err := s.lookup("owner", batch.ID, "get_tasks", "")
	if err != nil {
		t.Fatal(err)
	}
	jobs := value.(map[string]any)["tasks"].([]taskView)
	if jobs[0].Status != "blocked" || !strings.Contains(jobs[0].Error, "outcome_unknown") ||
		jobs[1].Status != "outcome_unknown" || jobs[2].Status != "interrupted" || jobs[3].Status != "completed" {
		t.Fatalf("unexpected recovery: %#v", jobs)
	}
}
