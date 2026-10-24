package main

import (
	"context"
	"errors"
	"sync"
)

const browserJournalWriterSlots = 8

var (
	errBrowserJournalWriterClosed = errors.New("browser journal writer closed")
	errBrowserJournalSlotReleased = errors.New("browser journal slot released")
	errBrowserJournalSlotUsed     = errors.New("browser journal slot already submitted")
	errBrowserJournalRecordLarge  = errors.New("browser journal record exceeds limit")
	errBrowserJournalWriteFailed  = errors.New("browser journal write failed")
)

type browserJournalWriter struct {
	writeFn   func(string, []byte) error
	jobs      chan *browserJournalJob
	mu        sync.Mutex
	slots     map[*browserJournalSlot]struct{}
	closed    bool
	changed   chan struct{}
	workerEnd chan struct{}
}

type browserJournalSlot struct {
	writer    *browserJournalWriter
	submitted bool
	released  bool
}

type browserJournalJob struct {
	slot *browserJournalSlot
	path string
	data []byte
	done chan struct{}
	err  error
}

func newBrowserJournalWriter(write func(string, []byte) error) *browserJournalWriter {
	w := &browserJournalWriter{
		writeFn: write, jobs: make(chan *browserJournalJob, browserJournalWriterSlots),
		slots:   make(map[*browserJournalSlot]struct{}, browserJournalWriterSlots),
		changed: make(chan struct{}), workerEnd: make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *browserJournalWriter) signalLocked() {
	close(w.changed)
	w.changed = make(chan struct{})
}

func (w *browserJournalWriter) tryReserve() (*browserJournalSlot, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tryReserveLocked()
}

func (w *browserJournalWriter) tryReserveLocked() (*browserJournalSlot, bool) {
	if w.closed || len(w.slots) >= browserJournalWriterSlots {
		return nil, false
	}
	slot := &browserJournalSlot{writer: w}
	w.slots[slot] = struct{}{}
	return slot, true
}

func (slot *browserJournalSlot) release() {
	if slot == nil || slot.writer == nil {
		return
	}
	w := slot.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	if slot.submitted || slot.released {
		return
	}
	slot.released = true
	delete(w.slots, slot)
	w.signalLocked()
}

func (slot *browserJournalSlot) write(ctx context.Context, path string, data []byte) error {
	if slot == nil || slot.writer == nil {
		return errBrowserJournalSlotReleased
	}
	if len(data) > browserJournalRecordMax {
		slot.release()
		return errBrowserJournalRecordLarge
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		slot.release()
		return err
	}
	copyData := append([]byte(nil), data...)
	w := slot.writer
	w.mu.Lock()
	if w.closed {
		if !slot.submitted && !slot.released {
			slot.released = true
			delete(w.slots, slot)
			w.signalLocked()
		}
		w.mu.Unlock()
		return errBrowserJournalWriterClosed
	}
	if slot.released {
		w.mu.Unlock()
		return errBrowserJournalSlotReleased
	}
	if slot.submitted {
		w.mu.Unlock()
		return errBrowserJournalSlotUsed
	}
	if err := ctx.Err(); err != nil {
		slot.released = true
		delete(w.slots, slot)
		w.signalLocked()
		w.mu.Unlock()
		return err
	}
	job := &browserJournalJob{slot: slot, path: path, data: copyData, done: make(chan struct{})}
	slot.submitted = true
	w.jobs <- job // The slot bound guarantees room in this channel.
	w.mu.Unlock()

	select {
	case <-job.done:
		return job.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *browserJournalWriter) write(ctx context.Context, path string, data []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(data) > browserJournalRecordMax {
		return errBrowserJournalRecordLarge
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return errBrowserJournalWriterClosed
		}
		if slot, ok := w.tryReserveLocked(); ok {
			w.mu.Unlock()
			return slot.write(ctx, path, data)
		}
		changed := w.changed
		w.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (w *browserJournalWriter) close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	w.closed = true
	for slot := range w.slots {
		if !slot.submitted {
			slot.released = true
			delete(w.slots, slot)
		}
	}
	w.signalLocked()
	w.mu.Unlock()
	select {
	case <-w.workerEnd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *browserJournalWriter) run() {
	defer close(w.workerEnd)
	for {
		select {
		case job := <-w.jobs:
			w.runJob(job)
		default:
		}
		w.mu.Lock()
		if w.closed && len(w.slots) == 0 {
			w.mu.Unlock()
			return
		}
		changed := w.changed
		w.mu.Unlock()
		select {
		case job := <-w.jobs:
			w.runJob(job)
		case <-changed:
		}
	}
}

func (w *browserJournalWriter) runJob(job *browserJournalJob) {
	if err := w.writeFn(job.path, job.data); err != nil {
		job.err = errBrowserJournalWriteFailed
	}
	w.mu.Lock()
	delete(w.slots, job.slot)
	job.slot.released = true
	w.signalLocked()
	w.mu.Unlock()
	close(job.done)
}
