package main

import (
	"context"
	"sync"
)

const browserQueueWaitingMax = 8

const browserQueueBusyMessage = "browser operation queue is full; retry later"

type browserWorkQueue struct {
	mu      sync.Mutex
	active  bool
	waiting []*browserWorkWaiter
}

type browserWorkWaiter struct {
	ready chan struct{}
	state browserWorkWaiterState
	err   error
}

type browserWorkWaiterState uint8

const (
	browserWorkQueued browserWorkWaiterState = iota
	browserWorkGranted
	browserWorkCanceled
)

func (q *browserWorkQueue) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	q.mu.Lock()
	if err := ctx.Err(); err != nil {
		q.mu.Unlock()
		return nil, err
	}
	if !q.active {
		q.active = true
		q.mu.Unlock()
		return q.releaseFunc(), nil
	}
	if len(q.waiting) >= browserQueueWaitingMax {
		q.mu.Unlock()
		return nil, toolErr("browser_busy", browserQueueBusyMessage)
	}
	waiter := &browserWorkWaiter{ready: make(chan struct{}), state: browserWorkQueued}
	q.waiting = append(q.waiting, waiter)
	q.mu.Unlock()

	select {
	case <-ctx.Done():
		q.mu.Lock()
		if waiter.state == browserWorkQueued {
			q.removeWaiter(waiter)
			waiter.state = browserWorkCanceled
			waiter.err = ctx.Err()
		} else if waiter.state == browserWorkGranted {
			waiter.state = browserWorkCanceled
			waiter.err = ctx.Err()
			q.releasePermit()
		}
		err := waiter.err
		q.mu.Unlock()
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	case <-waiter.ready:
		q.mu.Lock()
		if waiter.state == browserWorkCanceled {
			err := waiter.err
			q.mu.Unlock()
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			if waiter.state == browserWorkGranted {
				waiter.state = browserWorkCanceled
				waiter.err = err
				q.releasePermit()
			}
			q.mu.Unlock()
			return nil, err
		}
		q.mu.Unlock()
		return q.releaseFunc(), nil
	}
}

func (q *browserWorkQueue) cancelPending() {
	q.mu.Lock()
	for _, waiter := range q.waiting {
		waiter.state = browserWorkCanceled
		waiter.err = context.Canceled
		close(waiter.ready)
	}
	q.waiting = nil
	q.mu.Unlock()
}

func (q *browserWorkQueue) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			q.releasePermit()
			q.mu.Unlock()
		})
	}
}

func (q *browserWorkQueue) releasePermit() {
	for len(q.waiting) > 0 {
		waiter := q.waiting[0]
		q.waiting[0] = nil
		q.waiting = q.waiting[1:]
		if waiter.state != browserWorkQueued {
			continue
		}
		waiter.state = browserWorkGranted
		close(waiter.ready)
		return
	}
	q.active = false
}

func (q *browserWorkQueue) removeWaiter(target *browserWorkWaiter) {
	for index, waiter := range q.waiting {
		if waiter == target {
			copy(q.waiting[index:], q.waiting[index+1:])
			q.waiting[len(q.waiting)-1] = nil
			q.waiting = q.waiting[:len(q.waiting)-1]
			return
		}
	}
}
