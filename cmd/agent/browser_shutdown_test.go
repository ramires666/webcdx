package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserPendingCleanupRejectsMutationBeforeReservation(t *testing.T) {
	e, root := testExecutor(t)
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "pending-browser")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	done := make(chan struct{})
	b.pendingCleanups = append(b.pendingCleanups, done)
	_, err = b.call(context.Background(), "browser_open", map[string]any{"mode": "clean", "request_key": "blocked-cleanup"})
	browserErrorCode(t, err, "browser_busy")
	if len(b.operations) != 0 || len(b.journal) != 0 {
		t.Fatal("pending cleanup admitted a new mutation reservation")
	}
	close(done)
	b.mu.Lock()
	pending := b.cleanupsPendingLocked()
	b.mu.Unlock()
	if pending {
		t.Fatal("completed cleanup still blocks new work")
	}
}

func TestBrowserCleanProfileRemovalStaysInsideProfiles(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "profiles")
	profile := filepath.Join(base, "clean-owned")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{profile, outside} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, denied := range []string{base, outside} {
		if err := removeBrowserCleanProfile(base, denied); err == nil {
			t.Fatalf("cleanup accepted protected path %q", denied)
		}
		if _, err := os.Stat(denied); err != nil {
			t.Fatalf("protected path was changed: %v", err)
		}
	}
	if err := removeBrowserCleanProfile(base, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("owned temporary profile remains: %v", err)
	}
}

func TestBrowserCleanupFastPathReturnsNil(t *testing.T) {
	var forced atomic.Bool
	if done := browserCleanup(func() {}, func() { forced.Store(true) }, time.Second); done != nil {
		t.Fatalf("completed cleanup should return nil, got channel %v", done)
	}
	if forced.Load() {
		t.Fatal("force called for cleanup that completed within budget")
	}
}

func TestBrowserCleanupReturnsByBudgetAndForcesWithoutWaiting(t *testing.T) {
	workRelease := make(chan struct{})
	forceRelease := make(chan struct{})
	forceStarted := make(chan struct{})
	started := time.Now()
	done := browserCleanup(
		func() { <-workRelease },
		func() { close(forceStarted); <-forceRelease },
		30*time.Millisecond,
	)
	if done == nil {
		t.Fatal("blocked work should leave an observable completion channel")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("caller exceeded cleanup budget by too much: %s", elapsed)
	}
	select {
	case <-forceStarted:
	case <-time.After(time.Second):
		t.Fatal("force callback was not started")
	}
	select {
	case <-done:
		t.Fatal("done closed before blocked work returned")
	default:
	}
	close(workRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("done did not close after work returned")
	}
	// Unblock the force goroutine so the test leaves no blocked helper behind.
	close(forceRelease)
}

func TestBrowserCleanupDoesNotWaitForBlockedForce(t *testing.T) {
	forceStarted := make(chan struct{})
	forceRelease := make(chan struct{})
	workRelease := make(chan struct{})
	started := time.Now()
	done := browserCleanup(func() { <-workRelease }, func() { close(forceStarted); <-forceRelease }, 20*time.Millisecond)
	if done == nil {
		t.Fatal("blocked cleanup should return a completion channel")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("caller waited on blocked force callback: %s", elapsed)
	}
	select {
	case <-forceStarted:
	case <-time.After(time.Second):
		t.Fatal("force callback was not started")
	}
	select {
	case <-done:
		t.Fatal("cleanup completion channel closed while work is still blocked")
	default:
	}
	close(forceRelease)
	close(workRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after work was unblocked")
	}
}
