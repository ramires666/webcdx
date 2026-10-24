package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func awaitJournalQueueDepth(t *testing.T, writer *browserJournalWriter, depth int) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if len(writer.jobs) >= depth {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("writer queue did not reach depth %d", depth)
		}
	}
}

func TestBrowserJournalWriterSerializesAndCopiesAcceptedPayloads(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	var order, contents []string
	writer := newBrowserJournalWriter(func(path string, data []byte) error {
		mu.Lock()
		order = append(order, path)
		contents = append(contents, string(data))
		mu.Unlock()
		if path == "first" {
			close(firstStarted)
			<-releaseFirst
		}
		if path == "private/path" {
			return fmt.Errorf("private payload detail")
		}
		return nil
	})
	defer writer.close(context.Background())

	first, ok := writer.tryReserve()
	if !ok {
		t.Fatal("first reservation failed")
	}
	payload := []byte("immutable payload")
	firstResult := make(chan error, 1)
	go func() { firstResult <- first.write(context.Background(), "first", payload) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first write did not begin")
	}
	payload[0] = 'X'
	if err := first.write(context.Background(), "duplicate", []byte("second use")); !errors.Is(err, errBrowserJournalSlotUsed) {
		t.Fatalf("second use of a submitted slot should fail safely, got %v", err)
	}

	second, ok := writer.tryReserve()
	if !ok {
		t.Fatal("second reservation failed")
	}
	secondResult := make(chan error, 1)
	go func() { secondResult <- second.write(context.Background(), "second", []byte("2")) }()
	awaitJournalQueueDepth(t, writer, 1)
	third, ok := writer.tryReserve()
	if !ok {
		t.Fatal("third reservation failed")
	}
	thirdResult := make(chan error, 1)
	go func() { thirdResult <- third.write(context.Background(), "third", []byte("3")) }()
	awaitJournalQueueDepth(t, writer, 2)
	close(releaseFirst)
	for _, result := range []<-chan error{firstResult, secondResult, thirdResult} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("write failed: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("ordered write did not complete")
		}
	}
	mu.Lock()
	gotOrder, gotContents := append([]string(nil), order...), append([]string(nil), contents...)
	mu.Unlock()
	if fmt.Sprint(gotOrder) != "[first second third]" {
		t.Fatalf("writes were reordered: %v", gotOrder)
	}
	if gotContents[0] != "immutable payload" {
		t.Fatalf("accepted payload was not copied: %q", gotContents[0])
	}
	if err := writer.write(context.Background(), "oversized", make([]byte, browserJournalRecordMax+1)); !errors.Is(err, errBrowserJournalRecordLarge) {
		t.Fatalf("oversized payload should be rejected: %v", err)
	}
	if err := writer.write(context.Background(), "private/path", []byte("private payload")); !errors.Is(err, errBrowserJournalWriteFailed) || strings.Contains(err.Error(), "private") {
		t.Fatalf("callback error should be replaced with a fixed message: %v", err)
	}
}

func TestBrowserJournalWriterCapacityRetainsCancelledActiveSlot(t *testing.T) {
	started := make(chan struct{})
	releaseIO := make(chan struct{})
	var writes atomic.Int32
	writer := newBrowserJournalWriter(func(string, []byte) error {
		writes.Add(1)
		close(started)
		<-releaseIO
		return nil
	})
	defer writer.close(context.Background())

	active, ok := writer.tryReserve()
	if !ok {
		t.Fatal("active reservation failed")
	}
	var held []*browserJournalSlot
	for i := 0; i < browserJournalWriterSlots-1; i++ {
		slot, ok := writer.tryReserve()
		if !ok {
			t.Fatalf("reservation %d failed before capacity", i)
		}
		held = append(held, slot)
	}
	if _, ok := writer.tryReserve(); ok {
		t.Fatal("writer admitted a ninth active or queued slot")
	}

	ctx, cancel := context.WithCancel(context.Background())
	activeResult := make(chan error, 1)
	go func() { activeResult <- active.write(ctx, "active", []byte("record")) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active write did not reach IO")
	}

	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitResult := make(chan error, 1)
	go func() { waitResult <- writer.write(waitCtx, "never", []byte("no write")) }()
	cancelWait()
	select {
	case err := <-waitResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("full-queue waiter should return cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled full-queue waiter did not return")
	}
	if writes.Load() != 1 {
		t.Fatalf("cancelled waiter reached IO, writes=%d", writes.Load())
	}

	cancel()
	select {
	case err := <-activeResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active caller should observe cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled active caller did not return")
	}
	if _, ok := writer.tryReserve(); ok {
		t.Fatal("cancelled caller released an active IO slot early")
	}
	close(releaseIO)
	for i := 0; i < 100; i++ {
		if slot, ok := writer.tryReserve(); ok {
			slot.release()
			break
		}
		if i == 99 {
			t.Fatal("active slot was not released after IO returned")
		}
		time.Sleep(time.Millisecond)
	}
	for _, slot := range held {
		slot.release()
	}
}

func TestBrowserJournalWriterCloseIsBoundedAndDrainsAcceptedIO(t *testing.T) {
	started := make(chan struct{})
	releaseIO := make(chan struct{})
	var writes atomic.Int32
	writer := newBrowserJournalWriter(func(_ string, _ []byte) error {
		writes.Add(1)
		close(started)
		<-releaseIO
		return nil
	})
	active, ok := writer.tryReserve()
	if !ok {
		t.Fatal("active reservation failed")
	}
	issued, ok := writer.tryReserve()
	if !ok {
		t.Fatal("pending reservation failed")
	}
	activeResult := make(chan error, 1)
	go func() { activeResult <- active.write(context.Background(), "accepted", []byte("immutable")) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("accepted IO did not start")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := writer.close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close should respect its deadline while IO is blocked, got %v", err)
	}
	if _, ok := writer.tryReserve(); ok {
		t.Fatal("closed writer admitted a new reservation")
	}
	if err := issued.write(context.Background(), "late", []byte("not accepted")); !errors.Is(err, errBrowserJournalWriterClosed) {
		t.Fatalf("issued but unsubmitted slot should be rejected after close: %v", err)
	}
	issued.release() // Idempotent after close invalidated this slot.
	if writes.Load() != 1 {
		t.Fatalf("late submission reached IO: writes=%d", writes.Load())
	}

	close(releaseIO)
	select {
	case err := <-activeResult:
		if err != nil {
			t.Fatalf("accepted write failed while draining: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted write did not finish after IO unblocked")
	}
	if err := writer.close(context.Background()); err != nil {
		t.Fatalf("close did not finish draining accepted IO: %v", err)
	}
}
