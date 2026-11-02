package protocol

import (
	"strings"
	"testing"
)

func TestV5InstructionsDescribeAdvertisedBrowserProviders(t *testing.T) {
	for _, want := range []string{
		"tools actually advertised by tools/list",
		"browser_task_start",
		"exact allowed_origins",
		"fresh idempotency_key",
		"sync_cookies=false",
		"browser_task_status",
		"browser_task_events",
		"browser_task_result",
		"do not fall back to personal Chrome",
		"only when native browser_open and browser_control tools are actually advertised",
		"browser_control with operation_id",
		"Use fresh snapshot_id and element_id",
		"request_key",
	} {
		if !strings.Contains(V5Instructions, want) {
			t.Errorf("V5Instructions missing %q", want)
		}
	}
}

func TestV5InstructionsGuardNativeBrowserRules(t *testing.T) {
	guard := "The following native-browser instructions apply only when native browser_open and browser_control tools are actually advertised."
	_, nativeRules, found := strings.Cut(V5Instructions, guard)
	if !found {
		t.Fatal("native browser instruction guard missing")
	}
	if !strings.Contains(nativeRules, BrowserReliabilityInstructions) {
		t.Fatal("native browser reliability instructions must remain after the guard")
	}
	if !strings.Contains(nativeRules, BrowserExistingTargetInstructions) {
		t.Fatal("existing-target instructions must remain after the guard")
	}
	for _, want := range []string{
		"Native browser mutations require request_key",
		"poll browser_control with operation_id",
		"browser status is separate from process status and has no exit code",
		"Use fresh snapshot_id and element_id in the same frame",
		"Local extension pause and selected-tab scope take precedence",
	} {
		if !strings.Contains(nativeRules, want) {
			t.Errorf("native instructions missing %q", want)
		}
	}
	prefix, _, _ := strings.Cut(V5Instructions, guard)
	if strings.Contains(prefix, "poll browser_control") {
		t.Fatal("native browser polling instruction leaked outside its provider guard")
	}
}
