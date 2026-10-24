package main

import "testing"

func TestBrowserFailureStateOverrideKeepsContext(t *testing.T) {
	for _, tc := range []struct {
		name, initial, recovery string
	}{
		{name: "stale snapshot", initial: "stale_snapshot", recovery: "refresh_snapshot"},
		{name: "locator", initial: "ambiguous_locator", recovery: "inspect_locator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := &browserOperation{
				ID:        "bop_0123456789abcdef01234567",
				ErrorCode: tc.initial,
				ErrorDetails: map[string]any{
					"operation_id": "bop_0123456789abcdef01234567",
					"tab_id":       "tab_0123456789abcdef01234567",
					"snapshot_id":  "snapshot_0123456789abcdef01234567",
					"recovery":     tc.recovery,
				},
			}

			op.setFailureCode("outcome_unknown")

			if op.ErrorCode != "outcome_unknown" || op.ErrorMessage != browserFailureMessage("outcome_unknown") {
				t.Fatalf("failure code/message = %q / %q", op.ErrorCode, op.ErrorMessage)
			}
			if op.ErrorDetails["operation_id"] != op.ID || op.ErrorDetails["tab_id"] != "tab_0123456789abcdef01234567" || op.ErrorDetails["snapshot_id"] != "snapshot_0123456789abcdef01234567" {
				t.Fatalf("context was lost: %#v", op.ErrorDetails)
			}
			if op.ErrorDetails["recovery"] != "verify_outcome" {
				t.Fatalf("recovery = %v; want verify_outcome", op.ErrorDetails["recovery"])
			}
		})
	}
}

func TestBrowserFailureStateCancelledAndNilDetails(t *testing.T) {
	op := &browserOperation{ID: "bop_0123456789abcdef01234567"}
	op.setFailureCode("cancelled")

	if op.ErrorCode != "cancelled" || op.ErrorMessage != browserFailureMessage("cancelled") {
		t.Fatalf("failure code/message = %q / %q", op.ErrorCode, op.ErrorMessage)
	}
	if op.ErrorDetails == nil || op.ErrorDetails["operation_id"] != op.ID || op.ErrorDetails["recovery"] != browserErrorRecovery("cancelled") {
		t.Fatalf("inconsistent cancelled details: %#v", op.ErrorDetails)
	}
}
