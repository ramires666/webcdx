package main

import (
	"context"
	"sync"
)

const browserURLCheckWorkers = 4
const browserURLCheckPending = 32

type browserURLCheckJob struct {
	ctx   context.Context
	url   string
	reply func(bool)
}

type browserURLChecks struct {
	mu      sync.Mutex
	closed  bool
	check   func(context.Context, string) bool
	ctx     context.Context
	cancel  context.CancelFunc
	jobs    chan browserURLCheckJob
	workers sync.WaitGroup
	done    chan struct{}
}

func newBrowserURLChecks(check func(context.Context, string) bool) *browserURLChecks {
	ctx, cancel := context.WithCancel(context.Background())
	checks := &browserURLChecks{
		check:  check,
		ctx:    ctx,
		cancel: cancel,
		jobs:   make(chan browserURLCheckJob, browserURLCheckPending),
		done:   make(chan struct{}),
	}
	checks.workers.Add(browserURLCheckWorkers)
	for i := 0; i < browserURLCheckWorkers; i++ {
		go checks.worker()
	}
	go func() {
		checks.workers.Wait()
		close(checks.done)
	}()
	return checks
}

// submit accepts only when the bounded queue has room. The caller must fail
// closed when false is returned; it must never infer an allow decision.
func (checks *browserURLChecks) submit(ctx context.Context, rawURL string, reply func(bool)) bool {
	if ctx == nil || ctx.Err() != nil || reply == nil {
		return false
	}
	checks.mu.Lock()
	defer checks.mu.Unlock()
	if checks.closed {
		return false
	}
	select {
	case checks.jobs <- browserURLCheckJob{ctx: ctx, url: rawURL, reply: reply}:
		return true
	default:
		return false
	}
}

func (checks *browserURLChecks) worker() {
	defer checks.workers.Done()
	for {
		select {
		case <-checks.ctx.Done():
			checks.rejectQueued()
			return
		case job := <-checks.jobs:
			allowed := false
			if job.ctx.Err() == nil && checks.ctx.Err() == nil && checks.check != nil {
				checkCtx, cancel := context.WithCancel(job.ctx)
				stop := context.AfterFunc(checks.ctx, cancel)
				allowed = checks.check(checkCtx, job.url)
				stop()
				cancel()
				allowed = allowed && job.ctx.Err() == nil && checks.ctx.Err() == nil
			}
			job.reply(allowed)
		}
	}
}

func (checks *browserURLChecks) rejectQueued() {
	for {
		select {
		case job := <-checks.jobs:
			job.reply(false)
		default:
			return
		}
	}
}

func (checks *browserURLChecks) close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	checks.mu.Lock()
	if !checks.closed {
		checks.closed = true
		checks.cancel()
	}
	checks.mu.Unlock()
	select {
	case <-checks.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
