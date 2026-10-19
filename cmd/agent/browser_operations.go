package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

const (
	browserOperationCacheMax   = 4096
	browserOperationRunningMax = 128
	browserOperationReadMax    = 128
	browserOperationTTL        = 24 * time.Hour
	browserOperationCleanup    = time.Minute
)

var browserOperationID = regexp.MustCompile(`^bop_[0-9a-f]{24}$`)

// The small retry index outlives cached results. Eviction must never turn a
// retry of a completed mutation into another browser action.
type browserJournalEntry struct {
	ID string
	At time.Time
}

// Caller holds b.mu (or is still constructing the manager).
func (b *browserManager) loadOperation(id string) (*browserOperation, error) {
	if op := b.operations[id]; op != nil {
		return op, nil
	}
	if !browserOperationID.MatchString(id) {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(b.config.Dir, "operations", id+".json"))
	if err != nil {
		return nil, err
	}
	var op browserOperation
	if err := json.Unmarshal(data, &op); err != nil {
		return nil, err
	}
	if op.ID != id || op.KeyHash == "" {
		return nil, errors.New("invalid browser operation journal")
	}
	if time.Since(op.At) > browserOperationTTL {
		return nil, os.ErrNotExist
	}
	if op.Status == "running" {
		op.Status, op.ErrorCode = "failed", "outcome_unknown"
	}
	op.done = make(chan struct{})
	close(op.done)
	return &op, nil
}

// Only completed results leave the cache. Mutations remain on disk until TTL;
// read-only results (including screenshots) have a smaller memory budget.
func (b *browserManager) trimOperations(limit int) {
	reads := 0
	for _, op := range b.operations {
		if op.Status != "running" && op.KeyHash == "" {
			reads++
		}
	}
	if len(b.operations) <= limit && reads <= browserOperationReadMax {
		return
	}
	completed := make([]*browserOperation, 0, len(b.operations))
	for _, op := range b.operations {
		if op.Status != "running" {
			completed = append(completed, op)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		a, c := completed[i], completed[j]
		if (a.KeyHash == "") != (c.KeyHash == "") {
			return a.KeyHash == ""
		}
		if a.At.Equal(c.At) {
			return a.ID < c.ID
		}
		return a.At.Before(c.At)
	})
	// Free a batch so sustained traffic does not sort the cache on every call.
	target := limit
	if len(b.operations) > limit {
		target -= limit / 8
	}
	for _, op := range completed {
		if len(b.operations) <= target && (op.KeyHash != "" || reads <= browserOperationReadMax) {
			continue
		}
		delete(b.operations, op.ID)
		if op.KeyHash == "" {
			reads--
		}
	}
}

// Caller holds b.mu. Missing files are already clean; other I/O errors keep
// their index entries so the next cleanup can retry without losing protection.
func (b *browserManager) expireOperations(now time.Time) error {
	for key, entry := range b.journal {
		if now.Sub(entry.At) <= browserOperationTTL {
			continue
		}
		if op := b.operations[entry.ID]; op != nil && op.Status == "running" {
			continue
		}
		if err := os.Remove(filepath.Join(b.config.Dir, "operations", entry.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(b.journal, key)
		delete(b.operations, entry.ID)
	}
	for id, op := range b.operations {
		if op.Status != "running" && now.Sub(op.At) > browserOperationTTL {
			delete(b.operations, id)
		}
	}
	return nil
}

func (b *browserManager) startOperationCleanup() {
	ctx, cancel := context.WithCancel(context.Background())
	b.cleanupStop = cancel
	b.cleanupDone = make(chan struct{})
	go func() {
		defer close(b.cleanupDone)
		b.runOperationCleanup(ctx, browserOperationCleanup)
	}()
}

func (b *browserManager) runOperationCleanup(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			b.mu.Lock()
			err := b.expireOperations(now)
			b.trimOperations(browserOperationCacheMax)
			b.mu.Unlock()
			if err != nil {
				log.Printf("browser journal cleanup failed: %v", err)
			}
		}
	}
}
