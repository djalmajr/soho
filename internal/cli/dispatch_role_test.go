package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func roleDispatchFixture(t *testing.T) *dispatchArrivalFixture {
	t.Helper()
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	for _, role := range []string{"scouter", "reviewer", "implementer", "designer", "documenter", "tasker"} {
		mode, body := "edit", "Edit role instructions."
		if role == "scouter" || role == "reviewer" {
			mode, body = "read-only", "Read-only. Never write, edit, or modify files."
		}
		writeRoleFixture(t, filepath.Join(f.root, "roles", role+".md"), fmt.Sprintf("---\nname: %s\nmode: %s\n---\n%s\n", role, mode, body))
	}
	roster := filepath.Join(f.state, "ws", "agents.tsv")
	writeRoleFixture(t, roster, strings.ReplaceAll(mustRead(t, roster), "\timplementer\t", "\tscouter\t"))
	return f
}
func writeRoleFixture(t *testing.T, p, s string) {
	t.Helper()
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func assertRoleNoSend(t *testing.T, f *dispatchArrivalFixture) {
	t.Helper()
	calls, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if e != nil && !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if countArgvPrefix(calls, []string{"agent", "prompt"}) != 0 {
		t.Fatal("prompt sent")
	}
	for _, pattern := range []string{"task-report-*.json", "briefs/*.dispatch.json", "reports/*.md", "wait/*.compact-pending.json"} {
		paths, e := filepath.Glob(filepath.Join(f.state, "ws", pattern))
		if e != nil || len(paths) != 0 {
			t.Fatalf("state written: %v %v", paths, e)
		}
	}
}

func TestDispatchRoleEditSignals(t *testing.T) {
	for _, tc := range []struct {
		name, body, explicit, signal, suggest, word string
		refuse                                      bool
	}{
		{name: "roster-backend-owned", body: "Type: backend\n# Owned files\n- app.go\n", signal: "Type: backend, Owned files", suggest: "implementer", word: "runs", refuse: true},
		{name: "explicit-edit", body: "Type: backend\n# Owned files\n- app.go\n", explicit: "implementer"},
		{name: "explicit-reviewer", body: "# Owned files\n- app.go\n", explicit: "reviewer", signal: "Owned files", suggest: "implementer", word: "was given", refuse: true},
		{name: "ui", body: "Type: ui\n", signal: "Type: ui", suggest: "designer", word: "runs", refuse: true},
		{name: "docs", body: "Type: docs\n", signal: "Type: docs", suggest: "documenter", word: "runs", refuse: true},
		{name: "mechanical", body: "Type: mechanical\n", signal: "Type: mechanical", suggest: "tasker", word: "runs", refuse: true},
		{name: "review", body: "Type: review\n", explicit: "reviewer"},
		{name: "security", body: "Type: security\n", explicit: "reviewer"},
		{name: "empty-owned", body: "# Owned files\n\n# Report\nDone\n"},
		{name: "fenced-owned", body: "```md\nType: backend\n# Owned files\n- app.go\n```\nType: review\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := roleDispatchFixture(t)
			writeRoleFixture(t, f.brief, tc.body)
			before := mustRead(t, filepath.Join(f.state, "ws", "agents.tsv"))
			args := []string{"worker", f.brief, "--no-wait", "--compact"}
			if !tc.refuse {
				args = args[:len(args)-1]
			}
			if tc.explicit != "" {
				args = append(args, "--role", tc.explicit)
			}
			code, out, msg := f.run(t, args...)
			if tc.refuse {
				role := tc.explicit
				if role == "" {
					role = "scouter"
				}
				want := fmt.Sprintf("dispatch: brief %s asks for edits (%s) but 'worker' %s the read-only role '%s'; nothing was sent. Pass --role %s to give it an edit role, or drop the edit sections from the brief", f.brief, tc.signal, tc.word, role, tc.suggest)
				if code != 2 || !strings.Contains(msg, want) {
					t.Fatalf("code=%d out=%s msg=%q want=%q", code, out, msg, want)
				}
				assertRoleNoSend(t, f)
			} else {
				if code != 0 {
					t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
				}
				p, _ := dispatchOutputField(t, out, "composed_prompt")
				if tc.explicit == "implementer" && strings.Contains(mustRead(t, p), "Read-only") {
					t.Fatal("readonly header in edit prompt")
				}
			}
			if mustRead(t, filepath.Join(f.state, "ws", "agents.tsv")) != before {
				t.Fatal("roster changed")
			}
		})
	}
}

func TestDispatchAmendRoleSelection(t *testing.T) {
	for _, tc := range []struct{ name, first, next string }{{"inherit", "implementer", ""}, {"switch", "scouter", "implementer"}, {"same", "implementer", "implementer"}, {"legacy", "scouter", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			f := roleDispatchFixture(t)
			writeRoleFixture(t, f.brief, "Type: review\nReview it.\n")
			code, out, msg := f.run(t, "worker", f.brief, "--role", tc.first, "--no-wait")
			if code != 0 {
				t.Fatalf("first %d %s %s", code, out, msg)
			}
			if tc.name == "legacy" {
				pointer := filepath.Join(f.state, "ws", "task-report-worker.json")
				p := taskreport.ReadTaskReportPointer(filepath.Join(f.state, "ws"), "worker")
				p.Delete("role")
				if e := taskreport.WriteTaskReportPointer(filepath.Join(f.state, "ws"), "worker", p); e != nil {
					t.Fatal(e)
				}
				_ = pointer
				composed, _ := dispatchOutputField(t, out, "composed_prompt")
				side := strings.TrimSuffix(composed, ".md") + ".dispatch.json"
				b, e := os.ReadFile(side)
				if e != nil {
					t.Fatal(e)
				}
				v, e := jsonjs.Parse(b)
				if e != nil {
					t.Fatal(e)
				}
				v.(*jsonjs.Object).Delete("role")
				writeRoleFixture(t, side, jsonjs.Stringify(v))
			}
			amend := filepath.Join(f.root, "amend.md")
			body := "Type: backend\n# Owned files\n- app.go\n"
			if tc.name == "legacy" {
				body = "Type: review\nReview more.\n"
			}
			writeRoleFixture(t, amend, body)
			args := []string{"worker", amend, "--amend", "--no-wait"}
			if tc.next != "" {
				args = append(args, "--role", tc.next)
			}
			code, out, msg = f.run(t, args...)
			if code != 0 {
				t.Fatalf("amend %d %s %s", code, out, msg)
			}
			role := tc.next
			if role == "" {
				role = tc.first
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			text := mustRead(t, composed)
			if !strings.Contains(text, "You are running as the `"+role+"` role") || (role == "implementer" && strings.Contains(text, "Read-only")) {
				t.Fatalf("header=%s", text)
			}
			p := taskreport.ReadTaskReportPointer(filepath.Join(f.state, "ws"), "worker")
			if got, _ := p.Get("role"); got != role {
				t.Fatalf("pointer role=%v", got)
			}
			fields := readSidecarFields(t, f, composed)
			if fields["role"] != role {
				t.Fatalf("sidecar=%v", fields)
			}
			if !strings.Contains(core.RosterLine(filepath.Join(f.state, "ws"), "worker"), "\tscouter\t") {
				t.Fatal("roster role changed")
			}
		})
	}
}

func TestDispatchAmendRoleKeepsCollaborationRefusal(t *testing.T) {
	f, brief := collaborationFixture(t, false)
	startCollaboration(t, f, brief)
	// The earlier-dispatch marker is required before the active-contract check.
	ctx := core.LoadConfig(f.env, f.repo)
	state := core.StateDir(&ctx, f.env, f.repo)
	writeRoleFixture(t, core.LastReportPath(state, "author"), filepath.Join(state, "reports", "old.md"))
	before, e := fakecli.ReadCallsForConfig(filepath.Join(f.env.Get("SOHO_FAKECLI_CONFIG"), "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	code, out, msg := f.run(t, f.env, "dispatch", "author", brief, "--amend", "--role", "implementer", "--no-wait")
	if code != 10 || !strings.Contains(msg, "active collaboration") {
		t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
	}
	after, e := fakecli.ReadCallsForConfig(filepath.Join(f.env.Get("SOHO_FAKECLI_CONFIG"), "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	if countArgvPrefix(after[len(before):], []string{"agent", "prompt"}) != 0 {
		t.Fatal("collaboration refusal sent prompt")
	}
	for _, pattern := range []string{"task-report-author.json", "briefs/*.dispatch.json"} {
		paths, e := filepath.Glob(filepath.Join(state, pattern))
		if e != nil || len(paths) != 0 {
			t.Fatalf("refusal state=%v err=%v", paths, e)
		}
	}
}

func TestDispatchAmendReadOnlyEditRefusal(t *testing.T) {
	f := roleDispatchFixture(t)
	writeRoleFixture(t, f.brief, "Type: review\nRead it.\n")
	firstDispatch(t, f)
	state := filepath.Join(f.state, "ws")
	pointer := filepath.Join(state, "task-report-worker.json")
	beforePointer := mustRead(t, pointer)
	beforeSide, _ := filepath.Glob(filepath.Join(state, "briefs", "*.dispatch.json"))
	before, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	amend := filepath.Join(f.root, "amend.md")
	writeRoleFixture(t, amend, "Type: backend\n# Owned files\n- app.go\n")
	code, out, msg := f.run(t, "worker", amend, "--amend", "--role", "reviewer", "--no-wait")
	if code != 2 || !strings.Contains(msg, "was given the read-only role 'reviewer'") {
		t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
	}
	after, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	afterSide, _ := filepath.Glob(filepath.Join(state, "briefs", "*.dispatch.json"))
	if countArgvPrefix(after[len(before):], []string{"agent", "prompt"}) != 0 || mustRead(t, pointer) != beforePointer || len(afterSide) != len(beforeSide) {
		t.Fatal("amendment refusal changed dispatch state")
	}
}

// A role without a declared mode (a custom project role) is not read-only for
// the refusal: an edit brief goes out unchanged.
func TestDispatchRoleWithoutModeAcceptsEditBrief(t *testing.T) {
	f := roleDispatchFixture(t)
	writeRoleFixture(t, filepath.Join(f.root, "roles", "custom.md"), "---\nname: custom\n---\nCustom role instructions.\n")
	writeRoleFixture(t, f.brief, "Type: backend\n# Owned files\n- app.go\n")
	code, out, msg := f.run(t, "worker", f.brief, "--no-wait", "--role", "custom")
	if code != 0 {
		t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
	}
	if strings.Contains(msg, "asks for edits") {
		t.Fatalf("refused a role without a declared mode: %q", msg)
	}
}
