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
	for _, role := range []string{"scouter", "reviewer", "security-reviewer", "implementer", "designer", "documenter", "tasker", "tester", "validator"} {
		mode, body := "edit", "Edit role instructions."
		if role == "scouter" || role == "reviewer" || role == "security-reviewer" || role == "validator" {
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
		{name: "review-scope", body: "Type: review\n## Scope\n- app.go\n## Escopo\n- other.go\n", explicit: "reviewer"},
		{name: "security-scope", body: "Type: security\n## Scope\n- app.go\n## Escopo\n- other.go\n", explicit: "security-reviewer"},
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
				if tc.name == "review-scope" || tc.name == "security-scope" {
					calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
					if err != nil || countArgvPrefix(calls, []string{"agent", "prompt"}) != 1 {
						t.Fatalf("review scope was not sent exactly once: calls=%v err=%v", calls, err)
					}
				}
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

func TestDispatchSecurityBriefOnBuildLaneWarns(t *testing.T) {
	// DJA-686: a brief that declares Type: security routed to a build-lane
	// worker warns on stderr and still sends; other declared Type values and
	// the critical lane stay silent, and panes=2 (no critical lane) stays
	// silent. The warning reads the declared Type field, never free text.
	warnedLine := func(brief, agent string) string {
		return fmt.Sprintf("soho: warning: brief %s declares Type: security but '%s' is in lane build: security work goes to the critical lane (spawn critical-implementer)", brief, agent)
	}
	for _, tc := range []struct {
		name, body, lane, conf string
		role                   string
		warned                 bool
	}{
		{name: "security-build-warns-and-sends", body: "Type: security\nCheck the patch.\n", lane: "build", warned: true},
		{name: "backend-build-silent", body: "Type: backend\nFix the slice.\n", lane: "build", role: "implementer"},
		{name: "security-critical-silent", body: "Type: security\nCheck the patch.\n", lane: "critical"},
		{name: "security-panes2-silent", body: "Type: security\nCheck the patch.\n", lane: "build", conf: "panes=2\n"},
		{name: "security-flex-panes2-silent", body: "Type: security\nCheck the patch.\n", lane: "build", conf: "panes=2\npane_mode=flex\n"},
		{name: "security-custom-team-without-critical-silent", body: "Type: security\nCheck the patch.\n", lane: "build", conf: "lane.build.roles=implementer,scouter\nlane.review.roles=reviewer\n"},
		{name: "security-orphan-capacity-silent", body: "Type: security\nCheck the patch.\n", lane: "build", conf: "panes=2\nlane.critical.panes=1\n"},
		{name: "security-custom-team-with-critical-warns", body: "Type: security\nCheck the patch.\n", lane: "build", conf: "lane.build.roles=implementer\nlane.critical.roles=critical-implementer\nlane.review.roles=reviewer\n", warned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := roleDispatchFixture(t)
			writeRoleFixture(t, f.brief, tc.body)
			roster := filepath.Join(f.state, "ws", "agents.tsv")
			writeRoleFixture(t, roster, "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tw0test:p0a\tcodex\tscouter\topenai\t0\t\tnow\tgpt-5\ttask\tscouter\t"+tc.lane+"\t\t\thigh\n")
			if tc.conf != "" {
				if err := os.MkdirAll(filepath.Join(f.root, ".agents"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeRoleFixture(t, filepath.Join(f.root, ".agents", "soho.conf"), tc.conf)
			}
			args := []string{"worker", f.brief, "--no-wait"}
			if tc.role != "" {
				args = append(args, "--role", tc.role)
			}
			code, out, msg := f.run(t, args...)
			if code != 0 {
				t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
			}
			if tc.warned {
				if !strings.Contains(msg, warnedLine(f.brief, "worker")) {
					t.Fatalf("missing the lane warning: %q", msg)
				}
				return
			}
			if strings.Contains(msg, "declares Type: security") {
				t.Fatalf("unexpected lane warning: %q", msg)
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
			if err != nil || countArgvPrefix(calls, []string{"agent", "prompt"}) != 1 {
				t.Fatalf("prompt was not sent exactly once: calls=%v err=%v", calls, err)
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
