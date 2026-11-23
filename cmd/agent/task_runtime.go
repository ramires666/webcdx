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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	taskBatchMax             = 100
	taskPendingMax           = 500
	taskPromptMax            = 16 << 10
	taskBatchPromptMax       = 256 << 10
	taskDependencyContextMax = 16 << 10
)

var taskIDPattern = regexp.MustCompile("^[a-f0-9]{64}$")
var taskNamePattern = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$")

type taskSpec struct {
	Key               string
	Agent             string `json:",omitempty"`
	Prompt            string
	Timeout           int
	DependsOn         []string           `json:",omitempty"`
	Workspace         *taskWorkspaceSpec `json:",omitempty"`
	DependencyContext string             `json:"-"`
}

type taskView struct {
	Agent     string         `json:"agent,omitempty"`
	Key       string         `json:"key"`
	Status    string         `json:"status"`
	DependsOn []string       `json:"depends_on,omitempty"`
	Output    string         `json:"output,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Error     string         `json:"error,omitempty"`
	Patch     *taskPatchView `json:"patch,omitempty"`
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
	workspaces  *taskWorkspaces
	parallel    int
	gate        bool
	run         taskRun
	release     func()
	queue       []taskWork
	active      map[string]context.CancelFunc
	batches     map[string]*taskBatch
	watchers    map[string]*taskGroupWatch
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
	auth, err := configuredTaskProviderAuth()
	if err != nil {
		return nil, err
	}
	if model := strings.TrimSpace(os.Getenv("WEBCODEX_TASK_MODEL")); model != "" && model != taskModel {
		return nil, errors.New("WEBCODEX_TASK_MODEL must be " + taskModel)
	}
	parallel, err := strconv.Atoi(env("WEBCODEX_TASK_PARALLEL", "4"))
	if err != nil || parallel < 1 || parallel > 32 {
		return nil, errors.New("WEBCODEX_TASK_PARALLEL must be between 1 and 32")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	workspaces, err := taskWorkspacesFromEnvironment()
	if err != nil {
		return nil, err
	}
	runner := openCodeTaskRunner{executable: exe, auth: auth, workspaces: workspaces}
	service, err := newTaskService(filepath.Join(dir, "webcodex", "task-queue"), parallel, runner.run)
	if err != nil {
		return nil, err
	}
	service.workspaces = workspaces
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
	return readTaskBatch(s.batchPath(id), id)
}

func readTaskBatch(path, id string) (*taskBatch, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("task batch journal is not a regular file")
	}
	data, err := readTaskFile(path, taskBatchFileMax)
	if err != nil {
		return nil, err
	}
	var b taskBatch
	if json.Unmarshal(data, &b) != nil || b.ID != id || b.Owner == "" || len(b.Jobs) == 0 || len(b.Jobs) > taskBatchMax {
		return nil, errors.New("task batch state is invalid")
	}
	for i, job := range b.Jobs {
		switch job.Status {
		case "queued", "waiting":
			job.Status = "interrupted"
		case "running", "cancelling":
			job.Status = "outcome_unknown"
			job.Error = "Agent restarted before the task outcome was recorded"
		}
		b.Jobs[i] = job
	}
	for i, job := range b.Jobs {
		if job.Status == "interrupted" && len(job.DependsOn) > 0 {
			if _, reason := b.dependencies(job); reason != "" {
				b.Jobs[i].Status = "blocked"
				b.Jobs[i].Error = reason
			}
		}
	}
	return &b, nil
}

func (s *taskService) save(b *taskBatch, create bool) error {
	defer s.notifyTaskGroup(b.ID)
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
			job := &work.batch.Jobs[work.index]
			job.Status = "interrupted"
			job.Error = "The task store could not save the queue"
			s.notifyTaskGroup(work.batch.ID)
		}
		s.queue = nil
	}
	return err
}

func taskBatchStatus(b *taskBatch) map[string]any {
	jobs := make([]taskView, len(b.Jobs))
	for i, job := range b.Jobs {
		job.Output = ""
		job.Patch = nil
		jobs[i] = job
	}
	status := map[string]any{"batch_id": b.ID, "tasks": jobs}
	status["revision"] = commandHash(status)
	return status
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
		status := "queued"
		if len(spec.DependsOn) > 0 {
			status = "waiting"
		}
		b.Jobs[i] = taskView{Key: spec.Key, Agent: spec.agentName(), Status: status, DependsOn: slices.Clone(spec.DependsOn)}
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
	for !s.stopping && !s.storeFailed {
		blocked := false
		for i := 0; i < len(s.queue); {
			work := s.queue[i]
			job := &work.batch.Jobs[work.index]
			ready, reason := work.batch.dependencies(*job)
			switch {
			case reason != "":
				job.Status, job.Error = "blocked", reason
				blocked = true
			case !ready:
				i++
				continue
			case len(s.active) < s.parallel:
				job.Status = "running"
			default:
				if job.Status == "waiting" {
					job.Status = "queued"
					if s.save(work.batch, false) != nil {
						return
					}
				}
				i++
				continue
			}
			copy(s.queue[i:], s.queue[i+1:])
			s.queue[len(s.queue)-1] = taskWork{}
			s.queue = s.queue[:len(s.queue)-1]
			if s.save(work.batch, false) != nil {
				job.Status = "interrupted"
				job.Error = "The task store could not save the queue"
				return
			}
			if job.Status == "blocked" {
				s.releaseIfFinished(work.batch)
				continue
			}
			work.spec.DependencyContext = work.batch.dependencyContext(*job)
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(work.spec.Timeout)*time.Second)
			s.active[work.id()] = cancel
			s.workers.Add(1)
			go s.execute(ctx, work)
		}
		if !blocked {
			return
		}
	}
}

func (b *taskBatch) job(key string) taskView {
	for _, job := range b.Jobs {
		if strings.EqualFold(job.Key, key) {
			return job
		}
	}
	return taskView{Key: key, Status: "missing"}
}

func (b *taskBatch) dependencies(job taskView) (ready bool, reason string) {
	ready = true
	for _, key := range job.DependsOn {
		status := b.job(key).Status
		switch status {
		case "completed":
		case "waiting", "queued", "running", "cancelling":
			ready = false
		default:
			return false, fmt.Sprintf("Dependency %q ended with status %s", key, status)
		}
	}
	return ready, ""
}

func (b *taskBatch) dependencyContext(job taskView) string {
	if len(job.DependsOn) == 0 {
		return ""
	}
	type dependencyOutput struct {
		Key       string `json:"key"`
		Output    string `json:"output"`
		Truncated bool   `json:"truncated,omitempty"`
	}
	var context strings.Builder
	context.WriteByte('[')
	budget := (taskDependencyContextMax - len(job.DependsOn) - 1) / len(job.DependsOn)
	for i, key := range job.DependsOn {
		if i != 0 {
			context.WriteByte(',')
		}
		dependency := b.job(key)
		row := dependencyOutput{dependency.Key, dependency.Output, dependency.Truncated}
		encoded, _ := json.Marshal(row)
		if len(encoded) > budget {
			row.Truncated = true
			low, high := 0, min(len(dependency.Output), budget)
			for low < high {
				mid := (low + high + 1) / 2
				row.Output, _ = truncateUTF8(dependency.Output, mid)
				candidate, _ := json.Marshal(row)
				if len(candidate) <= budget {
					low = mid
				} else {
					high = mid - 1
				}
			}
			row.Output, _ = truncateUTF8(dependency.Output, low)
			encoded, _ = json.Marshal(row)
		}
		context.Write(encoded)
	}
	context.WriteByte(']')
	return context.String()
}

func (w taskWork) id() string { return w.batch.ID + "-" + w.spec.Key }

func (s *taskService) execute(ctx context.Context, work taskWork) {
	defer s.workers.Done()
	result := s.run(ctx, filepath.Join(s.root, work.id()), work.spec)
	result.Key = work.spec.Key
	result.Agent = work.spec.agentName()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[work.id()]()
	delete(s.active, work.id())
	result.DependsOn = work.batch.Jobs[work.index].DependsOn
	work.batch.Jobs[work.index] = result
	if err := s.save(work.batch, false); err != nil {
		work.batch.Jobs[work.index] = taskView{Key: result.Key, Status: "outcome_unknown", DependsOn: result.DependsOn, Error: "Could not persist result"}
	}
	s.releaseIfFinished(work.batch)
	s.dispatch()
}

func (s *taskService) releaseIfFinished(batch *taskBatch) {
	if s.storeFailed || taskGroupHasPendingJobs(batch) {
		return
	}
	delete(s.batches, batch.ID)
}

func (s *taskService) lookup(owner, id, name, key string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, err := s.ownedTaskBatch(owner, id)
	if err != nil {
		return nil, err
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
