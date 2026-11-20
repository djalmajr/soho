package cli

// SOH-460/SOH-458: every command that resolves a worker name keeps its
// refusal and exit code, and appends why spawn never opened that name.

import (
	"strings"
	"testing"
)

func rosterHintInvalidNames() []struct{ name, reason string } {
	long := strings.Repeat("a", 33)
	return []struct{ name, reason string }{
		{long, "; '" + long + "' is not a valid agent name (longer than 32 characters), so spawn never opened it"},
		{"Review", "; 'Review' is not a valid agent name (must start with a lowercase letter), so spawn never opened it"},
		{"review.1", "; 'review.1' is not a valid agent name (only a-z, 0-9, _ and - are allowed), so spawn never opened it"},
	}
}

func TestReleaseReopenInvalidNameReason(t *testing.T) {
	for _, tc := range rosterHintInvalidNames() {
		t.Run(tc.name, func(t *testing.T) {
			f := newReleaseFixture(t, "", "")
			r := f.run(t, tc.name)
			if r.code != 3 || !strings.HasSuffix(r.stderr, tc.reason+"\n") {
				t.Fatalf("release: code=%d stderr=%q want 3 ending with %q", r.code, r.stderr, tc.reason)
			}

			o := newReopenFixture(t, "")
			reopened := o.reopen(t, tc.name)
			if reopened.code != 3 || !strings.HasSuffix(reopened.stderr, tc.reason+"\n") {
				t.Fatalf("reopen: code=%d stderr=%q want 3 ending with %q", reopened.code, reopened.stderr, tc.reason)
			}
		})
	}
}

func TestDispatchInvalidNameReason(t *testing.T) {
	for _, tc := range rosterHintInvalidNames() {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			code, _, stderr := f.run(t, tc.name, f.brief, "--no-wait")
			want := "soho: agent '" + tc.name + "' is not in this skill's roster (spawn it first, or pass a name you spawned)" + tc.reason + "\n"
			if code != 3 || stderr != want {
				t.Fatalf("dispatch: code=%d stderr=%q want 3 %q", code, stderr, want)
			}
		})
	}
}

func TestModelSetInvalidNameReason(t *testing.T) {
	for _, tc := range rosterHintInvalidNames() {
		t.Run(tc.name, func(t *testing.T) {
			f := newModelSetHcFixture(t, "")
			code, _, stderr := f.run(t, tc.name, "openai/gpt-5")
			want := "soho: agent '" + tc.name + "' is not in this skill's roster (spawn it first, or pass a name you spawned)" + tc.reason + "\n"
			if code != 3 || stderr != want {
				t.Fatalf("model-set: code=%d stderr=%q want 3 %q", code, stderr, want)
			}
		})
	}
}

func TestRosterHintKeepsValidMissingNameUnchanged(t *testing.T) {
	f := newReleaseFixture(t, "", "")
	r := f.run(t, "absent")
	want := "soho: agent 'absent' is not in the roster (state dir: " + f.state + ")\n"
	if r.code != 3 || r.stderr != want {
		t.Fatalf("release: code=%d stderr=%q want 3 %q", r.code, r.stderr, want)
	}
}
