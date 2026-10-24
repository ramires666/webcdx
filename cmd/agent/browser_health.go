package main

import (
	"errors"
	"io"
	"log"
	"os"
)

var errBrowserJournalCorrupt = errors.New("invalid browser operation journal")

const browserJournalRecordMax = 1 << 20

func browserEmptyManager(e *nativeExecutor, config browserConfig) *browserManager {
	return &browserManager{config: config, executor: e, sessions: map[string]*browserSession{}, operations: map[string]*browserOperation{}, journal: map[string]browserJournalEntry{}}
}

// Keep the corrupt/unreadable journal in place. Ignoring or deleting an unknown
// record would allow replaying a mutation whose request key cannot be recovered.
func browserDegradedManager(e *nativeExecutor, config browserConfig, cause error) *browserManager {
	b := browserEmptyManager(e, config)
	b.healthReason = "storage_unavailable"
	if errors.Is(cause, errBrowserJournalCorrupt) {
		b.healthReason = "journal_corrupt"
	}
	log.Printf("⚠ Браузер ограничен (%s): изменения заблокированы; файлы и команды доступны. Восстановите журнал и перезапустите агент", b.healthReason)
	return b
}

// Caller holds b.mu. These are fixed diagnostic categories, never raw paths or
// persisted page contents. A pending managed teardown has a bounded count.
func (b *browserManager) healthLocked() (string, string) {
	if !b.config.Enabled {
		return "disabled", ""
	}
	if b.healthReason != "" {
		return "degraded", b.healthReason
	}
	if b.cleanupsPendingLocked() {
		return "degraded", "cleanup_pending"
	}
	if b.closed {
		return "unavailable", "shutdown"
	}
	return "healthy", ""
}

func readBrowserJournalRecord(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > browserJournalRecordMax {
		return nil, errBrowserJournalCorrupt
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, browserJournalRecordMax+1))
	if err == nil && len(data) > browserJournalRecordMax {
		return nil, errBrowserJournalCorrupt
	}
	return data, err
}
