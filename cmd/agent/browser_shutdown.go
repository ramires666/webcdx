package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// browserCleanup bounds a caller's wait for browser teardown. A timed-out
// cleanup remains observable through done until work actually returns.
func browserCleanup(work func(), force func(), budget time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		work()
	}()
	if budget <= 0 {
		go force()
		return done
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		select {
		case <-done:
			return nil
		default:
		}
		go force()
		return done
	}
}

// Caller holds b.mu. Pending teardown blocks new mutations so repeated failed
// launches cannot accumulate cleanup goroutines and owned Chrome processes.
func (b *browserManager) cleanupsPendingLocked() bool {
	active := b.pendingCleanups[:0]
	for _, done := range b.pendingCleanups {
		select {
		case <-done:
		default:
			active = append(active, done)
		}
	}
	b.pendingCleanups = active
	return len(active) != 0
}

func removeBrowserCleanProfile(base, profile string) error {
	if _, err := os.Lstat(profile); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		return err
	}
	profile, err = filepath.EvalSymlinks(profile)
	if err != nil {
		return err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return err
	}
	profile, err = filepath.Abs(profile)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(base, profile)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("browser cleanup path is outside the profile directory")
	}
	return os.RemoveAll(profile)
}
