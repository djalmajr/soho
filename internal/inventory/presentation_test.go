package inventory

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestInventoryPresentationFold(t *testing.T) {
	for _, tc := range []struct {
		effective, terminal, state, reason string
		include                            bool
	}{
		{"working", "idle", "working", "", true},
		{"compacting", "", "working", "", true},
		{"collaborating", "", "working", "", true},
		{"compact-interrupted", "", "needs_you", "compact", true},
		{"question", "blocked", "needs_you", "question", true},
		{"blocked", "blocked", "needs_you", "approval", true},
		{"quota", "idle", "needs_you", "quota", true},
		{"capacity", "idle", "needs_you", "provider", true},
		{"provider-error", "idle", "needs_you", "provider", true},
		{"provider-refusal", "blocked", "needs_you", "provider", true},
		{"not-received", "idle", "needs_you", "not_received", true},
		{"no-report-yet", "done", "needs_you", "no_report", true},
		{"no-report-yet", "idle", "idle", "", true},
		{"no-report-yet", "working", "unknown", "", true},
		{"done", "unavailable", "done", "", true},
		{"idle", "idle", "idle", "", true},
		{"gone", "", "unknown", "", false},
		{"done", "gone", "unknown", "", false},
		{"working", "gone", "unknown", "", false},
		{"unavailable", "", "unknown", "", true},
		{"compact-unknown", "", "unknown", "", true},
		{"collaboration-unavailable", "", "unknown", "", true},
		{"collaboration-disabled", "", "unknown", "", true},
		{"not-in-roster", "", "unknown", "", true},
		{"unknown-agent", "", "unknown", "", true},
		{"unknown", "", "unknown", "", true},
		{"", "working", "unknown", "", true},
		{"new-state", "done", "unknown", "", true},
		{"owner", "done", "unknown", "", true},
		{"needs_you", "done", "unknown", "", true},
	} {
		t.Run(tc.effective+"/"+tc.terminal, func(t *testing.T) {
			state, reason, include := FoldStatus(tc.effective, tc.terminal)
			if string(state) != tc.state || include != tc.include {
				t.Fatalf("state=%q include=%v, want %q/%v", state, include, tc.state, tc.include)
			}
			if tc.reason == "" {
				if reason != nil {
					t.Fatalf("reason=%q, want nil", *reason)
				}
			} else if reason == nil || string(*reason) != tc.reason {
				t.Fatalf("reason=%v, want %q", reason, tc.reason)
			}
		})
	}
}

func TestInventoryPresentationTask(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		worker bool
		want   string
	}{
		{"implementer: Repair ✓\n", true, "Repair"},
		{"scouter: Inspect a changed dispatch role ✓\n", true, "Inspect a changed dispatch role"},
		{"review: Keep: the inner colon", true, "Keep: the inner colon"},
		{"design: literal objective ✓", false, "design: literal objective ✓"},
		{"worker: a\x00b\nc\u0085", true, "abc"},
		{" \t\n", true, ""}, {"worker:  ✓", true, ""},
		{"worker: \xff", true, ""}, {"\xff", false, ""},
		{"worker: " + strings.Repeat("🦙", 121), true, strings.Repeat("🦙", 120)},
	} {
		got := TaskTitle(tc.raw, tc.worker)
		if tc.want == "" {
			if got != nil {
				t.Fatalf("empty task got %q", *got)
			}
		} else if got == nil || *got != tc.want || !utf8.ValidString(*got) {
			t.Fatalf("task=%v, want %q", got, tc.want)
		}
	}
}

func TestInventoryPresentationOptionalFields(t *testing.T) {
	for _, raw := range []string{"", " ", "m\n", "m\x00", "m\u0085", "\xff"} {
		if got := OptionalModel(raw); got != nil {
			t.Fatalf("invalid model %q became %q", raw, *got)
		}
	}
	for _, raw := range []string{"provider/model", " default-id "} {
		if got := OptionalModel(raw); got == nil || *got != raw {
			t.Fatalf("model %q changed: %v", raw, got)
		}
	}
	for _, raw := range []string{"low", "medium", "high", "xhigh", "max"} {
		if got := OptionalEffort(raw); got == nil || *got != raw {
			t.Fatalf("effort %q changed: %v", raw, got)
		}
	}
	for _, raw := range []string{"", " high", "HIGH", "ultra", "high\n", "\xff"} {
		if got := OptionalEffort(raw); got != nil {
			t.Fatalf("invalid effort %q became %q", raw, *got)
		}
	}
}

func TestInventoryPresentationStarted(t *testing.T) {
	utc, ok := ParseStartedMS("20261031T120000", time.UTC)
	if !ok || utc != 1793448000000 {
		t.Fatalf("UTC start=%d/%v", utc, ok)
	}
	local, ok := ParseStartedMS("20261031T120000", time.FixedZone("fixture", -3*60*60))
	if !ok || local != 1793458800000 {
		t.Fatalf("local start=%d/%v", local, ok)
	}
	for _, raw := range []string{"", "2026-10-31T12:00:00Z", "20261031T12000", "20261031T120000\n", "20260230T120000", "19691231T235959"} {
		if _, ok := ParseStartedMS(raw, time.UTC); ok {
			t.Fatalf("invalid start %q accepted", raw)
		}
	}
	if _, ok := ParseStartedMS("20261031T120000", nil); ok {
		t.Fatal("nil location accepted")
	}
}
