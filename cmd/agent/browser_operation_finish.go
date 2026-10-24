package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

func (b *browserManager) finishBrowserOperation(ctx context.Context, op *browserOperation, args map[string]any, result any, executionErr error) {
	b.mu.Lock()
	final := *op
	b.mu.Unlock()
	final.Status, final.Phase = "completed", "completed"
	if executionErr != nil {
		final.Status, final.Phase = "failed", "not_dispatched"
		if op.execution.dispatched.Load() {
			final.Phase = "dispatch_started"
		}
		final.fail(executionErr, args)
		if final.KeyHash != "" && op.execution.dispatched.Load() && browserUncertainTransport(final.ErrorCode) {
			final.setFailureCode("outcome_unknown")
		}
		if ctx.Err() != nil || errors.Is(executionErr, context.Canceled) || errors.Is(executionErr, context.DeadlineExceeded) {
			final.Status = "cancelled"
			final.setFailureCode("cancelled")
			if final.KeyHash != "" {
				final.setFailureCode("cancelled_before_dispatch")
				if op.execution.dispatched.Load() {
					final.setFailureCode("outcome_unknown")
				}
			}
		}
	} else if img, ok := result.(browserImage); ok {
		final.Result, final.image = img.Result, img.Data
	} else {
		final.Result, _ = result.(map[string]any)
	}
	if final.Result != nil {
		final.Result["operation_id"] = final.ID
		final.Result["phase"] = final.Phase
	}
	if final.KeyHash != "" {
		data, err := json.Marshal(&final)
		if err == nil {
			writeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			err = b.journalWriter.write(writeCtx, filepath.Join(b.config.Dir, "operations", final.ID+".json"), data)
			stop()
		}
		if err != nil {
			final.Status, final.Phase = "failed", "unknown"
			final.setFailureCode("outcome_unknown")
			b.mu.Lock()
			b.healthReason = "storage_unavailable"
			b.mu.Unlock()
		}
	}
	b.mu.Lock()
	// Identity, done/cancel and execution are immutable after admission. Replacing
	// the entire struct would race with callers selecting on op.done.
	op.Status, op.Phase = final.Status, final.Phase
	op.Result, op.image = final.Result, final.image
	op.ErrorCode, op.ErrorMessage = final.ErrorCode, final.ErrorMessage
	op.ErrorDetails, op.errorTransient = final.ErrorDetails, final.errorTransient
	b.cacheOperation(op)
	b.trimOperations(browserOperationCacheMax)
	b.mu.Unlock()
}
