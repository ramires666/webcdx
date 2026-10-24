package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserURLChecksSlowJobDoesNotBlockOtherWorkers(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	checks := newBrowserURLChecks(func(ctx context.Context, rawURL string) bool {
		if rawURL == "https://slow.example.test/path?q=private" {
			entered <- struct{}{}
			select {
			case <-release:
				return true
			case <-ctx.Done():
				return false
			}
		}
		return rawURL == "https://allowed.example.test/"
	})
	defer checks.close(context.Background())

	slowReply := make(chan bool, 1)
	if !checks.submit(context.Background(), "https://slow.example.test/path?q=private", func(allowed bool) { slowReply <- allowed }) {
		t.Fatal("slow job was rejected")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("slow check did not start")
	}
	fastReply := make(chan bool, 1)
	if !checks.submit(context.Background(), "https://allowed.example.test/", func(allowed bool) { fastReply <- allowed }) {
		t.Fatal("fast job was rejected")
	}
	select {
	case allowed := <-fastReply:
		if !allowed {
			t.Fatal("allowed URL was denied")
		}
	case <-time.After(time.Second):
		t.Fatal("slow job blocked another worker")
	}
	close(release)
	if allowed := <-slowReply; !allowed {
		t.Fatal("released slow check was denied")
	}
}

func TestBrowserURLChecksBoundedCapacityFailsClosed(t *testing.T) {
	started := make(chan struct{}, browserURLCheckWorkers)
	release := make(chan struct{})
	checks := newBrowserURLChecks(func(ctx context.Context, _ string) bool {
		started <- struct{}{}
		select {
		case <-release:
			return true
		case <-ctx.Done():
			return false
		}
	})
	defer func() {
		close(release)
		_ = checks.close(context.Background())
	}()
	for i := 0; i < browserURLCheckWorkers; i++ {
		if !checks.submit(context.Background(), "https://example.test/", func(bool) {}) {
			t.Fatalf("worker slot %d rejected", i)
		}
	}
	for i := 0; i < browserURLCheckWorkers; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	for i := 0; i < browserURLCheckPending; i++ {
		if !checks.submit(context.Background(), "https://example.test/queued", func(bool) {}) {
			t.Fatalf("pending slot %d rejected", i)
		}
	}
	if checks.submit(context.Background(), "https://example.test/overflow", func(bool) {}) {
		t.Fatal("overflow job accepted")
	}
}

func TestBrowserURLChecksCancellationAndClose(t *testing.T) {
	checks := newBrowserURLChecks(func(ctx context.Context, _ string) bool {
		<-ctx.Done()
		return true // The pool must still fail closed after cancellation.
	})
	ctx, cancel := context.WithCancel(context.Background())
	reply := make(chan bool, 1)
	if !checks.submit(ctx, "https://example.test/cancel", func(allowed bool) { reply <- allowed }) {
		t.Fatal("cancellable job was rejected")
	}
	cancel()
	select {
	case allowed := <-reply:
		if allowed {
			t.Fatal("canceled check was allowed")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled check did not reply")
	}
	if err := checks.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checks.submit(context.Background(), "https://example.test/after-close", func(bool) {}) {
		t.Fatal("job accepted after close")
	}
}

func TestBrowserURLChecksCloseRespectsWaitDeadline(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	checks := newBrowserURLChecks(func(_ context.Context, _ string) bool {
		entered <- struct{}{}
		<-release // Simulates a checker that cannot be interrupted.
		return false
	})
	if !checks.submit(context.Background(), "https://example.test/slow", func(bool) {}) {
		t.Fatal("slow job was rejected")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("slow check did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := checks.close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded close error = %v", err)
	}
	close(release)
	if err := checks.close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserURLChecksPreservesDenyAndNeverAutoAllows(t *testing.T) {
	var calls atomic.Int32
	checks := newBrowserURLChecks(func(_ context.Context, rawURL string) bool {
		calls.Add(1)
		return rawURL == "https://permitted.example.test/"
	})
	defer checks.close(context.Background())
	allowed := make(chan bool, 1)
	denied := make(chan bool, 1)
	checks.submit(context.Background(), "https://permitted.example.test/", func(value bool) { allowed <- value })
	checks.submit(context.Background(), "http://127.0.0.1/private", func(value bool) { denied <- value })
	if !<-allowed || <-denied || calls.Load() != 2 {
		t.Fatal("URL policy outcomes were changed")
	}
	if checks.submit(context.Background(), "https://overflow.example.test/", nil) {
		t.Fatal("invalid submission accepted")
	}
}
