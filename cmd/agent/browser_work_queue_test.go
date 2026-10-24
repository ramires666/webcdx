package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func waitForBrowserQueue(t *testing.T, queue *browserWorkQueue, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		queue.mu.Lock()
		ready := len(queue.waiting) == count
		queue.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not reach %d waiters", count)
}

func TestBrowserWorkQueueFIFO(t *testing.T) {
	var queue browserWorkQueue
	release, err := queue.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan int, 3)
	var workers sync.WaitGroup
	for i := 0; i < 3; i++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			done, err := queue.acquire(context.Background())
			if err != nil {
				t.Errorf("acquire %d: %v", id, err)
				return
			}
			started <- id
			done()
		}(i)
		waitForBrowserQueue(t, &queue, i+1)
	}
	release()
	for want := 0; want < 3; want++ {
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("start order = %d; want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("queued work was not granted")
		}
	}
	workers.Wait()
}

func TestBrowserWorkQueueOverflow(t *testing.T) {
	var queue browserWorkQueue
	release, err := queue.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cancels := make([]context.CancelFunc, 0, browserQueueWaitingMax)
	results := make([]chan error, 0, browserQueueWaitingMax)
	for i := 0; i < browserQueueWaitingMax; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		result := make(chan error, 1)
		results = append(results, result)
		go func() {
			_, err := queue.acquire(ctx)
			result <- err
		}()
		waitForBrowserQueue(t, &queue, i+1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = queue.acquire(ctx)
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "browser_busy" || coded.message != browserQueueBusyMessage {
		t.Fatalf("overflow error = %v", err)
	}
	queue.cancelPending()
	for _, result := range results {
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("overflow waiter error = %v", err)
		}
	}
	for _, cancel := range cancels {
		cancel()
	}
}

func TestBrowserWorkQueueCancellation(t *testing.T) {
	t.Run("before acquire", func(t *testing.T) {
		var queue browserWorkQueue
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := queue.acquire(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire error = %v", err)
		}
		queue.mu.Lock()
		active := queue.active
		queue.mu.Unlock()
		if active {
			t.Fatal("canceled acquire took the permit")
		}
	})

	t.Run("while waiting", func(t *testing.T) {
		var queue browserWorkQueue
		release, err := queue.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { _, err := queue.acquire(ctx); result <- err }()
		waitForBrowserQueue(t, &queue, 1)
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting acquire error = %v", err)
		}
		release()
		queue.mu.Lock()
		active := queue.active
		queue.mu.Unlock()
		if active {
			t.Fatal("canceled waiter retained the permit")
		}
	})

	t.Run("racing release", func(t *testing.T) {
		type acquireResult struct {
			release func()
			err     error
		}
		for i := 0; i < 50; i++ {
			var queue browserWorkQueue
			release, err := queue.acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan acquireResult, 1)
			go func() {
				done, err := queue.acquire(ctx)
				result <- acquireResult{release: done, err: err}
			}()
			waitForBrowserQueue(t, &queue, 1)
			start := make(chan struct{})
			go func() { <-start; cancel() }()
			go func() { <-start; release() }()
			close(start)
			select {
			case acquired := <-result:
				if acquired.release != nil {
					acquired.release()
				}
				if acquired.err != nil && !errors.Is(acquired.err, context.Canceled) {
					t.Fatalf("racing acquire error: %v", acquired.err)
				}
			case <-time.After(time.Second):
				t.Fatal("racing acquire did not finish")
			}
			cancel()
			// Whether grant or cancellation won, the permit remains usable.
			next, err := queue.acquire(context.Background())
			if err != nil {
				t.Fatalf("permit lost after race: %v", err)
			}
			next()
		}
	})
}

func TestBrowserWorkQueueCancelPending(t *testing.T) {
	var queue browserWorkQueue
	release, err := queue.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	results := make([]chan error, 3)
	for i := range results {
		results[i] = make(chan error, 1)
		go func(result chan error) {
			_, err := queue.acquire(context.Background())
			result <- err
		}(results[i])
		waitForBrowserQueue(t, &queue, i+1)
	}
	queue.cancelPending()
	for _, result := range results {
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("pending acquire error = %v", err)
		}
	}
	release()
	next, err := queue.acquire(context.Background())
	if err != nil {
		t.Fatalf("future acquire failed: %v", err)
	}
	next()
}
