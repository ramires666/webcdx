package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	taskBatchFileMax = 16 << 20
	taskGroupMaxWait = 5 * time.Second

	taskHistoryMaxEntries = 10000
	taskGroupListMax      = 25
)

type taskGroupWatch struct {
	changed chan struct{}
	users   int
}

func (s *taskService) ownedTaskBatch(owner, id string) (*taskBatch, error) {
	if owner == "" || !taskIDPattern.MatchString(id) {
		return nil, toolErr("unknown_task_batch", "task batch not found")
	}
	batch, err := s.load(id)
	if errors.Is(err, os.ErrNotExist) || err == nil && batch.Owner != owner {
		return nil, toolErr("unknown_task_batch", "task batch not found")
	}
	if err != nil {
		return nil, toolErr("task_store_unavailable", "could not read task batch")
	}
	return batch, nil
}

// subscribeTaskGroup and notifyTaskGroup run under s.mu. Registering in the
// same critical section as the status read prevents missed transitions.
func (s *taskService) subscribeTaskGroup(id string) *taskGroupWatch {
	if s.watchers == nil {
		s.watchers = make(map[string]*taskGroupWatch)
	}
	watch := s.watchers[id]
	if watch == nil {
		watch = &taskGroupWatch{changed: make(chan struct{})}
		s.watchers[id] = watch
	}
	watch.users++
	return watch
}

func (s *taskService) notifyTaskGroup(id string) {
	if watch := s.watchers[id]; watch != nil {
		close(watch.changed)
		delete(s.watchers, id)
	}
}

func (s *taskService) unsubscribeTaskGroup(id string, watch *taskGroupWatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchers[id] == watch {
		watch.users--
		if watch.users == 0 {
			delete(s.watchers, id)
		}
	}
}

func taskGroupHasPendingJobs(batch *taskBatch) bool {
	for _, task := range batch.Jobs {
		switch task.Status {
		case "waiting", "queued", "running", "cancelling":
			return true
		}
	}
	return false
}

func (s *taskService) waitTaskGroup(ctx context.Context, owner, batchID, previous string, maxWait time.Duration) (any, error) {
	if previous != "" && !taskIDPattern.MatchString(previous) {
		return nil, toolErr("invalid_arguments", "invalid task status revision")
	}
	var deadline <-chan time.Time
	if previous != "" && maxWait > 0 {
		timer := time.NewTimer(min(maxWait, taskGroupMaxWait))
		defer timer.Stop()
		deadline = timer.C
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		batch, err := s.ownedTaskBatch(owner, batchID)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		status := taskBatchStatus(batch)
		if deadline == nil || status["revision"] != previous || !taskGroupHasPendingJobs(batch) {
			s.mu.Unlock()
			return status, nil
		}
		watch := s.subscribeTaskGroup(batchID)
		s.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-deadline:
			deadline = nil
		case <-watch.changed:
		}
		s.unsubscribeTaskGroup(batchID, watch)
	}
}

func (s *taskService) listTaskGroups(owner, cursor string, limit int) (any, error) {
	if owner == "" || cursor != "" && !taskIDPattern.MatchString(cursor) || limit < 1 || limit > taskGroupListMax {
		return nil, toolErr("invalid_arguments", "invalid task history cursor or page size")
	}
	ids, err := s.taskHistoryIDs(cursor)
	if err != nil {
		return nil, err
	}
	groups := make([]map[string]any, 0, limit)
	response := map[string]any{}
	for _, id := range ids {
		if !taskBatchHasOwner(s.batchPath(id), id, owner) {
			continue
		}
		status, counts := s.taskGroupHistorySummary(owner, id)
		if status == nil {
			continue
		}
		if len(groups) == limit {
			response["next_cursor"] = groups[len(groups)-1]["batch_id"]
			break
		}
		groups = append(groups, map[string]any{"batch_id": id, "revision": status["revision"], "counts": counts})
	}
	response["groups"] = groups
	return response, nil
}

func (s *taskService) taskHistoryIDs(cursor string) ([]string, error) {
	dir, err := os.Open(s.root)
	if err != nil {
		return nil, toolErr("task_store_unavailable", "could not open task history")
	}
	defer dir.Close()
	var ids []string
	seen := 0
	for {
		entries, err := dir.ReadDir(128)
		seen += len(entries)
		if seen > taskHistoryMaxEntries {
			return nil, toolErr("task_history_limit", "task history exceeds its scan limit")
		}
		for _, entry := range entries {
			id, ok := strings.CutSuffix(entry.Name(), ".json")
			if ok && id > cursor && taskIDPattern.MatchString(id) && entry.Type().IsRegular() {
				ids = append(ids, id)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, toolErr("task_store_unavailable", "could not read task history")
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// Batch journals write the ID and owner before job results. Check that small
// header first so another owner's history never causes result decoding.
func taskBatchHasOwner(path, id, owner string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > taskBatchFileMax {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	reader := json.NewDecoder(io.LimitReader(file, 4<<10))
	readToken := func(want any) bool {
		token, err := reader.Token()
		return err == nil && token == want
	}
	return readToken(json.Delim('{')) && readToken("batch_id") && readToken(id) && readToken("owner") && readToken(owner)
}

func (s *taskService) taskGroupHistorySummary(owner, id string) (map[string]any, map[string]int) {
	s.mu.Lock()
	if live := s.batches[id]; live != nil {
		defer s.mu.Unlock()
		if live.Owner != owner {
			return nil, nil
		}
		return taskBatchStatus(live), taskGroupCounts(live)
	}
	s.mu.Unlock()

	batch, err := readTaskBatch(s.batchPath(id), id)
	if err != nil || batch.Owner != owner {
		return nil, nil
	}
	return taskBatchStatus(batch), taskGroupCounts(batch)
}

func taskGroupCounts(batch *taskBatch) map[string]int {
	counts := make(map[string]int)
	for _, job := range batch.Jobs {
		counts[job.Status]++
	}
	return counts
}
