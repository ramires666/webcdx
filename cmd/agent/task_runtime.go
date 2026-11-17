package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	taskBatchMax       = 100
	taskPendingMax     = 500
	taskPromptMax      = 16 << 10
	taskBatchPromptMax = 256 << 10
)

var taskIDPattern = regexp.MustCompile("^[a-f0-9]{64}$")
var taskNamePattern = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$")
var taskModelPattern = regexp.MustCompile("^opencode-go/[a-zA-Z0-9._-]+$")

type taskSpec struct {
	Key     string
	Prompt  string
	Timeout int
}

type taskView struct {
	Key       string `json:"key"`
	Status    string `json:"status"`
	Output    string `json:"output,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

type taskBatch struct {
	ID          string     `json:"batch_id"`
	Owner       string     `json:"owner"`
	Fingerprint string     `json:"fingerprint"`
	Jobs        []taskView `json:"tasks"`
}

type taskWork struct {
	batch *taskBatch
	index int
	spec  taskSpec
}

type taskRun func(context.Context, string, taskSpec) taskView

type taskService struct {
	mu          sync.Mutex
	root        string
	model       string
	parallel    int
	gate        bool
	run         taskRun
	release     func()
	queue       []taskWork
	active      map[string]context.CancelFunc
	batches     map[string]*taskBatch
	workers     sync.WaitGroup
	stopping    bool
	storeFailed bool
}

func taskServiceFromEnvironment() (*taskService, error) {
	exe := strings.TrimSpace(os.Getenv("WEBCODEX_OPENCODE_PATH"))
	if exe == "" {
		return nil, nil
	}
	info, err := os.Stat(exe)
	if err != nil || !filepath.IsAbs(exe) || !info.Mode().IsRegular() {
		return nil, errors.New("WEBCODEX_OPENCODE_PATH must point to an absolute OpenCode executable")
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(exe), ".exe") {
		return nil, errors.New("WEBCODEX_OPENCODE_PATH must point to a Windows .exe")
	}
	keyPath := strings.TrimSpace(os.Getenv("WEBCODEX_TASK_KEY_FILE"))
	if !filepath.IsAbs(keyPath) {
		return nil, errors.New("WEBCODEX_TASK_KEY_FILE must be an absolute path to a file containing the OpenCode Go key")
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil || !keyInfo.Mode().IsRegular() || keyInfo.Size() > 4096 {
		return nil, errors.New("WEBCODEX_TASK_KEY_FILE must be a readable key file of at most 4096 bytes")
	}
	data, err := os.ReadFile(keyPath)
	key := strings.TrimSpace(string(data))
	if err != nil || key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
		return nil, errors.New("WEBCODEX_TASK_KEY_FILE must contain one nonempty line")
	}
	model := strings.TrimSpace(env("WEBCODEX_TASK_MODEL", "opencode-go/hy4-flash-free"))
	if !taskModelPattern.MatchString(model) {
		return nil, errors.New("WEBCODEX_TASK_MODEL must name an opencode-go model")
	}
	parallel, err := strconv.Atoi(env("WEBCODEX_TASK_PARALLEL", "4"))
	if err != nil || parallel < 1 || parallel > 32 {
		return nil, errors.New("WEBCODEX_TASK_PARALLEL must be between 1 and 32")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	runner := openCodeTaskRunner{executable: exe, model: model, key: key}
	service, err := newTaskService(filepath.Join(dir, "webcodex", "task-queue"), parallel, runner.run)
	if err != nil {
		return nil, err
	}
	service.model = model
	service.gate = os.Getenv("WEBCODEX_TASKS_GATE") == "1"
	return service, nil
}

func newTaskService(root string, parallel int, run taskRun) (*taskService, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := privateBrowserDirectory(root); err != nil {
		return nil, err
	}
	release, err := lockTaskService(filepath.Join(root, "service.lock"))
	if err != nil {
		return nil, fmt.Errorf("open task queue exclusively: %w", err)
	}
	return &taskService{
		root: root, parallel: parallel, run: run, release: release,
		active: make(map[string]context.CancelFunc), batches: make(map[string]*taskBatch),
	}, nil
}

func (s *taskService) batchPath(id string) string {
	return filepath.Join(s.root, id+".json")
}

// load runs under mu. An in-memory batch is owned by this process. After a
// restart, a queued task never launched and an active task needs reconciliation.
func (s *taskService) load(id string) (*taskBatch, error) {
	if b := s.batches[id]; b != nil {
		return b, nil
	}
	data, err := os.ReadFile(s.batchPath(id))
	if err != nil {
		return nil, err
	}
	var b taskBatch
	if json.Unmarshal(data, &b) != nil || b.ID != id {
		return nil, errors.New("task batch state is invalid")
	}
	for i, job := range b.Jobs {
		switch job.Status {
		case "queued":
			job.Status = "interrupted"
		case "running", "cancelling":
			job.Status = "outcome_unknown"
			job.Error = "Agent restarted before the task outcome was recorded"
		}
		b.Jobs[i] = job
	}
	return &b, nil
}

func (s *taskService) save(b *taskBatch, create bool) error {
	data, err := json.Marshal(b)
	if err == nil {
		if create {
			err = atomicCreate(s.batchPath(b.ID), data)
		} else {
			err = atomicWrite(s.batchPath(b.ID), data, false)
		}
	}
	if err != nil {
		s.storeFailed = true
		for _, work := range s.queue {
			work.batch.Jobs[work.index] = taskFailure("interrupted", "The task store could not save the queue")
			work.batch.Jobs[work.index].Key = work.spec.Key
		}
		s.queue = nil
	}
	return err
}

func taskBatchStatus(b *taskBatch) any {
	jobs := make([]taskView, len(b.Jobs))
	for i, job := range b.Jobs {
		job.Output = ""
		jobs[i] = job
	}
	return map[string]any{"batch_id": b.ID, "tasks": jobs}
}

func (s *taskService) start(owner, requestKey string, specs []taskSpec) (any, error) {
	id := commandHash([]string{owner, requestKey})
	fingerprint := commandHash(specs)
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.load(id)
	if err == nil {
		if b.Owner != owner || b.Fingerprint != fingerprint {
			return nil, toolErr("request_key_conflict", "request_key was already used with different tasks")
		}
		return taskBatchStatus(b), nil
	}
	if !errors.Is(err, os.ErrNotExist) || s.storeFailed || s.stopping {
		return nil, toolErr("task_store_unavailable", "task queue is unavailable")
	}
	if len(s.queue)+len(s.active)+len(specs) > taskPendingMax {
		return nil, toolErr("task_queue_full", "task queue has insufficient free capacity")
	}
	b = &taskBatch{ID: id, Owner: owner, Fingerprint: fingerprint, Jobs: make([]taskView, len(specs))}
	for i, spec := range specs {
		b.Jobs[i] = taskView{Key: spec.Key, Status: "queued"}
	}
	if err := s.save(b, true); err != nil {
		return nil, toolErr("task_store_unavailable", "could not persist task batch")
	}
	s.batches[id] = b
	for i, spec := range specs {
		s.queue = append(s.queue, taskWork{batch: b, index: i, spec: spec})
	}
	accepted := taskBatchStatus(b)
	s.dispatch()
	return accepted, nil
}

func (s *taskService) dispatch() {
	for !s.stopping && !s.storeFailed && len(s.active) < s.parallel && len(s.queue) > 0 {
		work := s.queue[0]
		s.queue[0] = taskWork{}
		s.queue = s.queue[1:]
		work.batch.Jobs[work.index].Status = "running"
		if err := s.save(work.batch, false); err != nil {
			work.batch.Jobs[work.index].Status = "interrupted"
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(work.spec.Timeout)*time.Second)
		s.active[work.id()] = cancel
		s.workers.Add(1)
		go s.execute(ctx, work)
	}
}

func (w taskWork) id() string { return w.batch.ID + "-" + w.spec.Key }

func (s *taskService) execute(ctx context.Context, work taskWork) {
	defer s.workers.Done()
	result := s.run(ctx, filepath.Join(s.root, work.id()), work.spec)
	result.Key = work.spec.Key
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[work.id()]()
	delete(s.active, work.id())
	work.batch.Jobs[work.index] = result
	if err := s.save(work.batch, false); err != nil {
		work.batch.Jobs[work.index] = taskView{Key: result.Key, Status: "outcome_unknown", Error: "Could not persist result"}
	}
	s.releaseIfFinished(work.batch)
	s.dispatch()
}

func (s *taskService) releaseIfFinished(batch *taskBatch) {
	if s.storeFailed {
		return
	}
	for _, job := range batch.Jobs {
		if job.Status == "queued" || job.Status == "running" || job.Status == "cancelling" {
			return
		}
	}
	delete(s.batches, batch.ID)
}

func (s *taskService) lookup(owner, id, name, key string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, err := s.load(id)
	if errors.Is(err, os.ErrNotExist) || err == nil && batch.Owner != owner {
		return nil, toolErr("unknown_task_batch", "task batch not found")
	}
	if err != nil {
		return nil, toolErr("task_store_unavailable", "could not read task batch")
	}
	switch name {
	case "get_tasks":
		return taskBatchStatus(batch), nil
	case "task_result":
		for _, job := range batch.Jobs {
			if job.Key == key {
				return job, nil
			}
		}
		return nil, toolErr("unknown_task", "task not found in this batch")
	case "cancel_tasks":
		return s.cancelBatch(batch)
	default:
		return nil, toolErr("unknown_tool", "unknown task tool")
	}
}

func (s *taskService) cancelBatch(batch *taskBatch) (any, error) {
	if s.batches[batch.ID] == nil {
		return taskBatchStatus(batch), nil
	}
	pending := s.queue[:0]
	for _, work := range s.queue {
		if work.batch != batch {
			pending = append(pending, work)
		} else {
			work.batch.Jobs[work.index].Status = "cancelled"
		}
	}
	clear(s.queue[len(pending):])
	s.queue = pending
	for i, job := range batch.Jobs {
		if cancel := s.active[batch.ID+"-"+job.Key]; cancel != nil {
			batch.Jobs[i].Status = "cancelling"
			cancel()
		}
	}
	if err := s.save(batch, false); err != nil {
		return nil, toolErr("task_store_unavailable", "could not persist cancellation")
	}
	s.releaseIfFinished(batch)
	return taskBatchStatus(batch), nil
}

func (s *taskService) close() {
	s.mu.Lock()
	s.stopping = true
	for _, batch := range s.batches {
		_, _ = s.cancelBatch(batch)
	}
	s.mu.Unlock()
	s.workers.Wait()
	s.release()
}
