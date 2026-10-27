package dispatch

// Issue 60: full approvals do not guarantee that a sandboxed Windows worker
// can execute Node/Bun. The composition (the initial brief and the
// amendment) must carry a Windows-specific runtime note for sandboxed
// codex workers. The tests here are consumer tests: they check the prompt
// text the worker actually receives, through ComposePrompt/ComposeAmendment
// and the real wait, with the platform seam (currentPlatform, default
// platform.Current) injected for portable coverage. The note bytes must
// equal the JS dispatch's (skills/herdr-soho/scripts/lib/dispatch.mjs
// SANDBOX_WIN_RUNTIME_NOTE); the JS test asserts the same literal.

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/wait"
)

// The exact byte text the Go and JS dispatchers must agree on.
const windowsRuntimeNote = "- Your sandbox may not have the runtimes your brief requires (node, bun or another) or may deny them even with full approvals, so a check on the host PATH is not proof about this sandbox. If your brief carries a `## Runtime capability probe` section naming the required commands, run at most one minimal version probe per required runtime in this worker sandbox and record the exact command, exit code and result; never repeat an identical denied probe. A denied or missing runtime is [env] with its reason and exact host command; route the blocked gate to the orchestrator for an orchestrator-produced log of the same revision; check any external evidence for the same command, source and revision before letting it resolve the item, and it must never silently turn an unresolved [partial] into a pass.\n"

func TestMain(m *testing.M) {
	fakecli.RunTests(m)
}

// withPlatform injects the composition's platform seam for one test and
// restores the runtime boundary (platform.Current) after it.
func withPlatform(t *testing.T, goos string) {
	t.Helper()
	prev := currentPlatform
	currentPlatform = func() string { return goos }
	t.Cleanup(func() { currentPlatform = prev })
}

func writeRole(t *testing.T) string {
	t.Helper()
	role := filepath.Join(t.TempDir(), "implementer.md")
	if err := os.WriteFile(role, []byte("---\nname: Implementer\n---\n\nRole text.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return role
}

// TestWindowsRuntimeNoteBytes: the Go note constant is the agreed shared
// literal (the JS test pins the JS constant to the same literal), so the
// Go/JS produced note bytes are equal.
func TestWindowsRuntimeNoteBytes(t *testing.T) {
	if sandboxWinRuntimeNote != windowsRuntimeNote {
		t.Fatalf("the Go note drifted from the shared literal:\n%q\nvs\n%q", sandboxWinRuntimeNote, windowsRuntimeNote)
	}
}

// TestSandboxNotesForWindowsRuntime: the platform-aware helper — Windows
// sandboxed codex gets the runtime note after the unchanged .git/network
// notes; the network release keeps it; the bypass forms omit every note;
// other kinds and other platforms are unchanged.
func TestSandboxNotesForWindowsRuntime(t *testing.T) {
	git := sandboxGitNote
	net := sandboxNetNote
	win := sandboxWinRuntimeNote
	t.Run("win32 codex gets the runtime note after the .git and network notes", func(t *testing.T) {
		if got := SandboxNotesFor("codex", "", "", "win32"); !reflect.DeepEqual(got, []string{git, net, win}) {
			t.Fatalf("notes=%q", got)
		}
	})
	t.Run("win32 codex with network-only access keeps the runtime note", func(t *testing.T) {
		got := SandboxNotesFor("codex", "-c sandbox_workspace_write.network_access=true", "", "win32")
		if !reflect.DeepEqual(got, []string{git, win}) {
			t.Fatalf("notes=%q", got)
		}
	})
	t.Run("win32 codex with danger-full-access omits every note", func(t *testing.T) {
		if got := SandboxNotesFor("codex", "danger-full-access", "", "win32"); len(got) != 0 {
			t.Fatalf("notes=%q", got)
		}
	})
	t.Run("win32 codex with the native bypass flag omits every note", func(t *testing.T) {
		if got := SandboxNotesFor("codex", "--dangerously-bypass-approvals-and-sandbox", "", "win32"); len(got) != 0 {
			t.Fatalf("notes=%q", got)
		}
	})
	t.Run("win32 codex with the bypass flag and network still omits every note", func(t *testing.T) {
		got := SandboxNotesFor("codex", "--dangerously-bypass-approvals-and-sandbox -c sandbox_workspace_write.network_access=true", "", "win32")
		if len(got) != 0 {
			t.Fatalf("notes=%q", got)
		}
	})
	t.Run("win32 non-codex gets no note", func(t *testing.T) {
		for _, kind := range []string{"grok", "pi"} {
			if got := SandboxNotesFor(kind, "", "", "win32"); len(got) != 0 {
				t.Fatalf("%s notes=%q", kind, got)
			}
		}
	})
	t.Run("non-Windows codex is unchanged", func(t *testing.T) {
		for _, goos := range []string{"darwin", "linux"} {
			if got := SandboxNotesFor("codex", "", "", goos); !reflect.DeepEqual(got, []string{git, net}) {
				t.Fatalf("%s notes=%q", goos, got)
			}
		}
		if got := SandboxNotesFor("codex", "-c sandbox_workspace_write.network_access=true", "", "darwin"); !reflect.DeepEqual(got, []string{git}) {
			t.Fatalf("darwin network notes=%q", got)
		}
	})
}

// TestSandboxNotesBackCompat: the legacy entry keeps the .git/network notes
// byte-for-byte and the doctor's count contract (more than one note means
// the network stays off), even when the composition seam is on win32.
func TestSandboxNotesBackCompat(t *testing.T) {
	git := sandboxGitNote
	net := sandboxNetNote
	if got := SandboxNotes("codex", ""); !reflect.DeepEqual(got, []string{git, net}) {
		t.Fatalf("notes=%q", got)
	}
	if got := SandboxNotes("codex", "-c sandbox_workspace_write.network_access=true"); !reflect.DeepEqual(got, []string{git}) {
		t.Fatalf("network notes=%q", got)
	}
	if got := SandboxNotes("codex", "danger-full-access"); len(got) != 0 {
		t.Fatalf("full notes=%q", got)
	}
	if got := SandboxNotes("grok", ""); len(got) != 0 {
		t.Fatalf("grok notes=%q", got)
	}
	withPlatform(t, "win32")
	if got := SandboxNotes("codex", ""); !reflect.DeepEqual(got, []string{git, net}) {
		t.Fatalf("the legacy entry must not take the seam: notes=%q", got)
	}
	if got := SandboxNotes("codex", "-c sandbox_workspace_write.network_access=true"); len(got) != 1 {
		t.Fatalf("the doctor count contract must hold on win32: notes=%q", got)
	}
}

// TestComposePromptWindowsRuntimeNote: the worker's initial brief as
// composed — the note in place, the opt-in probe section delivered with
// the note, the bypass forms and non-codex kinds clean, non-Windows
// unchanged, and the default seam deriving the platform from the runtime
// boundary (platform.Current).
func TestComposePromptWindowsRuntimeNote(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	role := writeRole(t)
	env := composeEnv(t)
	brief := "# Goal\nDo it.\n"
	launcher := "- Run every `soho` command"
	t.Run("win32 sandboxed codex gets the note in the composed brief", func(t *testing.T) {
		withPlatform(t, "win32")
		p := ComposePrompt(role, "implementer", "worker", brief, "/report.md", ctx, env, "codex", "", "", false)
		if strings.Count(p, sandboxWinRuntimeNote) != 1 {
			t.Fatalf("the note must appear exactly once:\n%s", p)
		}
		if !strings.Contains(p, sandboxGitNote) || !strings.Contains(p, sandboxNetNote) {
			t.Fatalf("composed prompt misses the .git/network notes:\n%s", p)
		}
		if !(strings.Index(p, sandboxGitNote) < strings.Index(p, sandboxNetNote) &&
			strings.Index(p, sandboxNetNote) < strings.Index(p, sandboxWinRuntimeNote) &&
			strings.Index(p, sandboxWinRuntimeNote) < strings.Index(p, launcher)) {
			t.Fatalf("the note must sit after the .git/network notes and before the launcher line:\n%s", p)
		}
	})
	t.Run("win32 codex with network-only access keeps the note", func(t *testing.T) {
		withPlatform(t, "win32")
		p := ComposePrompt(role, "implementer", "worker", brief, "/report.md", ctx, env, "codex", "-c sandbox_workspace_write.network_access=true", "", false)
		if !strings.Contains(p, sandboxWinRuntimeNote) || strings.Contains(p, sandboxNetNote) || !strings.Contains(p, sandboxGitNote) {
			t.Fatalf("network release must keep the note and lift only the network note:\n%s", p)
		}
	})
	t.Run("win32 codex bypass forms omit every sandbox note", func(t *testing.T) {
		for _, args := range []string{"danger-full-access", "--dangerously-bypass-approvals-and-sandbox"} {
			withPlatform(t, "win32")
			p := ComposePrompt(role, "implementer", "worker", brief, "/report.md", ctx, env, "codex", args, "", false)
			if strings.Contains(p, sandboxWinRuntimeNote) || strings.Contains(p, sandboxGitNote) || strings.Contains(p, sandboxNetNote) {
				t.Fatalf("%s must omit every sandbox note:\n%s", args, p)
			}
		}
	})
	t.Run("win32 non-codex gets no note", func(t *testing.T) {
		withPlatform(t, "win32")
		p := ComposePrompt(role, "implementer", "worker", brief, "/report.md", ctx, env, "grok", "", "", false)
		if strings.Contains(p, sandboxWinRuntimeNote) || strings.Contains(p, sandboxGitNote) {
			t.Fatalf("another kind must get no sandbox note:\n%s", p)
		}
	})
	t.Run("the opt-in probe section is delivered to the worker with the note", func(t *testing.T) {
		withPlatform(t, "win32")
		probeBrief := "# Goal\nDo it.\n\n## Runtime capability probe\nnode --version\n"
		p := ComposePrompt(role, "implementer", "worker", probeBrief, "/report.md", ctx, env, "codex", "", "", false)
		if !strings.Contains(p, "# Brief\n\n"+probeBrief) {
			t.Fatalf("the probe section of the brief must reach the worker verbatim:\n%s", p)
		}
		if !strings.Contains(p, "## Runtime capability probe\nnode --version") || !strings.Contains(p, sandboxWinRuntimeNote) {
			t.Fatalf("the section and the note must travel together:\n%s", p)
		}
		// No section, no probe duty: the note is still delivered (it only
		// says what to do when the section is there).
		p2 := ComposePrompt(role, "implementer", "worker", brief, "/report.md", ctx, env, "codex", "", "", false)
		if !strings.Contains(p2, sandboxWinRuntimeNote) || strings.Contains(p2, "## Runtime capability probe\n") {
			t.Fatalf("without the section the note still ships and invents no probe section:\n%s", p2)
		}
	})
	t.Run("non-Windows codex stays byte-for-byte on the .git/network notes", func(t *testing.T) {
		withPlatform(t, "darwin")
		p := ComposePrompt(role, "implementer", "worker", brief, "/report.md", ctx, env, "codex", "", "", false)
		if !strings.Contains(p, sandboxGitNote) || !strings.Contains(p, sandboxNetNote) || strings.Contains(p, sandboxWinRuntimeNote) {
			t.Fatalf("non-Windows composition must not change:\n%s", p)
		}
	})
	t.Run("the default seam derives the platform from the runtime boundary", func(t *testing.T) {
		p := ComposePrompt(role, "implementer", "worker", brief, "/report.md", ctx, env, "codex", "", "", false)
		if strings.Contains(p, sandboxWinRuntimeNote) != (platform.Current() == "win32") {
			t.Fatalf("the note must appear exactly when platform.Current is win32 (got %s):\n%s", platform.Current(), p)
		}
	})
}

// TestComposeAmendmentWindowsRuntimeNote: the worker's amendment as
// composed — the note with its external-evidence rule in place, network
// release keeping it, the bypass forms and non-codex kinds clean, and
// non-Windows unchanged.
func TestComposeAmendmentWindowsRuntimeNote(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	standRules := "- Only you write this report"
	t.Run("win32 sandboxed codex gets the note in the amendment", func(t *testing.T) {
		withPlatform(t, "win32")
		a := ComposeAmendment("# Amend\nDo X.\n", "/report.md", ctx, composeEnv(t), "codex", "", "", false)
		if strings.Count(a, sandboxWinRuntimeNote) != 1 {
			t.Fatalf("the note must appear exactly once:\n%s", a)
		}
		if !strings.Contains(a, sandboxGitNote) || !strings.Contains(a, sandboxNetNote) {
			t.Fatalf("amendment misses the .git/network notes:\n%s", a)
		}
		if !(strings.Index(a, sandboxWinRuntimeNote) < strings.Index(a, standRules)) {
			t.Fatalf("the note must sit before the standing rules:\n%s", a)
		}
	})
	t.Run("the external-evidence rule is delivered in the amendment", func(t *testing.T) {
		withPlatform(t, "win32")
		a := ComposeAmendment("# Amend\nDo X.\n", "/report.md", ctx, composeEnv(t), "codex", "", "", false)
		for _, part := range []string{
			"check any external evidence for the same command, source and revision",
			"it must never silently turn an unresolved [partial] into a pass",
			"never repeat an identical denied probe",
			"an orchestrator-produced log of the same revision",
		} {
			if !strings.Contains(a, part) {
				t.Fatalf("amendment misses %q:\n%s", part, a)
			}
		}
	})
	t.Run("win32 codex with network-only access keeps the note", func(t *testing.T) {
		withPlatform(t, "win32")
		a := ComposeAmendment("# Amend\nDo X.\n", "/report.md", ctx, composeEnv(t), "codex", "-c sandbox_workspace_write.network_access=true", "", false)
		if !strings.Contains(a, sandboxWinRuntimeNote) || strings.Contains(a, sandboxNetNote) {
			t.Fatalf("network release must keep the note and lift only the network note:\n%s", a)
		}
	})
	t.Run("win32 codex bypass forms omit every sandbox note", func(t *testing.T) {
		for _, args := range []string{"danger-full-access", "--dangerously-bypass-approvals-and-sandbox"} {
			withPlatform(t, "win32")
			a := ComposeAmendment("# Amend\nDo X.\n", "/report.md", ctx, composeEnv(t), "codex", args, "", false)
			if strings.Contains(a, sandboxWinRuntimeNote) || strings.Contains(a, sandboxGitNote) || strings.Contains(a, sandboxNetNote) {
				t.Fatalf("%s must omit every sandbox note:\n%s", args, a)
			}
		}
	})
	t.Run("win32 non-codex gets no note", func(t *testing.T) {
		withPlatform(t, "win32")
		a := ComposeAmendment("# Amend\nDo X.\n", "/report.md", ctx, composeEnv(t), "grok", "", "", false)
		if strings.Contains(a, sandboxWinRuntimeNote) || strings.Contains(a, sandboxGitNote) {
			t.Fatalf("another kind must get no sandbox note:\n%s", a)
		}
	})
	t.Run("non-Windows codex stays byte-for-byte", func(t *testing.T) {
		withPlatform(t, "darwin")
		a := ComposeAmendment("# Amend\nDo X.\n", "/report.md", ctx, composeEnv(t), "codex", "", "", false)
		if !strings.Contains(a, sandboxGitNote) || !strings.Contains(a, sandboxNetNote) || strings.Contains(a, sandboxWinRuntimeNote) {
			t.Fatalf("non-Windows amendment must not change:\n%s", a)
		}
	})
}

// TestWindowsRuntimeNoteReportLifecycle: the report lifecycle the note
// relies on, through the real wait with the existing fixture
// infrastructure (the fake herdr from testutil/fakecli plus the wait's
// state dir, as internal/wait's own verdict tests do). An unresolved
// [partial] item keeps the effective verdict fail; a fresh amendment
// report (the dispatch's -2 pair, the task-report pointer with history)
// with resolved evidence and no partial is eligible to pass. No parser
// rule is relaxed: the same reportscan rules the wait applies decide both
// lines.
func TestWindowsRuntimeNoteReportLifecycle(t *testing.T) {
	base := t.TempDir()
	sd := filepath.Join(base, "state", "ws")
	bin := filepath.Join(base, "bin")
	for _, dir := range []string{filepath.Join(sd, "wait"), filepath.Join(sd, "reports")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\tcodex\timplementer\t\t\t\t\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	var rules []fakecli.Rule
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`})
	for _, read := range [][]string{
		{"agent", "read", "worker", "--source", "visible", "--lines", "20"},
		{"agent", "read", "worker", "--source", "visible", "--lines", "40"},
		{"agent", "read", "worker", "--source", "visible"},
		{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"},
	} {
		rules = append(rules, fakecli.Rule{Argv: read, Stdout: "done screen\n"})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}})
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	// This fixture checks report verdicts, not a worker still writing a report.
	// Keep the report authoritative without introducing state probes here.
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "HERDR_SOCKET_PATH": filepath.Join(base, "none.sock"), "SOHO_WAIT_POLL_MS": "1", "HERDR_WORKSPACE_ID": "ws", "SOHO_REPORT_WORKING_MAX": "0"}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	runWait := func(report string) (string, string, int) {
		if err := os.WriteFile(filepath.Join(sd, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		oldOut, oldErr := platform.Stdout, platform.Stderr
		platform.Stdout, platform.Stderr = &stdout, &stderr
		code := wait.WaitFor([]string{"worker"}, sd, ctx, env, 5000, false, "")
		platform.Stdout, platform.Stderr = oldOut, oldErr
		return stdout.String(), stderr.String(), code
	}
	// The first report: the runtime probe was denied in the sandbox and the
	// proof is unresolved (routed to the orchestrator for the log).
	report1 := filepath.Join(sd, "reports", "worker-20261005T160803.md")
	body1 := "# Report\n\n- Runtime capability: [partial] `node --version` was denied by the sandbox (exact command recorded, exit 127); the proof is unresolved, routed to the orchestrator for an orchestrator-produced log of the same revision.\n"
	if err := os.WriteFile(report1, []byte(body1), 0o600); err != nil {
		t.Fatal(err)
	}
	out1, err1, code1 := runWait(report1)
	if code1 != 0 {
		t.Fatalf("first wait code=%d out=%q stderr=%q", code1, out1, err1)
	}
	if !strings.Contains(out1, `"status":"done"`) || !strings.Contains(out1, `"partial":1,"verdict_effective":"fail"`) {
		t.Fatalf("the unresolved [partial] must keep the effective verdict fail: out=%q", out1)
	}
	// The amendment: the orchestrator produced the log of the same revision;
	// the fresh report resolves the item and marks no partial.
	report2 := filepath.Join(sd, "reports", "worker-20261005T160803-2.md")
	body2 := "# Report (amendment)\n\n- Runtime capability: [done] the orchestrator-produced log of the same revision shows `node --version` with the exact command, exit 0 and the version recorded.\n"
	if err := os.WriteFile(report2, []byte(body2), 0o600); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(sd, "reports", "worker-task-report.md")
	// The same pointer shape the dispatch's --amend writes
	// (internal/cli/dispatch.go: jsonjs.O with version/task_report/
	// current/history).
	pointer := jsonjs.O("version", 1, "task_report", stable, "current", report2, "history", []string{report1})
	if err := taskreport.WriteTaskReportPointer(sd, "worker", pointer); err != nil {
		t.Fatal(err)
	}
	out2, err2, code2 := runWait(report2)
	if code2 != 0 {
		t.Fatalf("amendment wait code=%d out=%q stderr=%q", code2, out2, err2)
	}
	if !strings.Contains(out2, `"status":"done"`) || !strings.Contains(out2, `"task_report"`) {
		t.Fatalf("the amendment report must settle as the current report: out=%q", out2)
	}
	if strings.Contains(out2, `"partial"`) || strings.Contains(out2, `verdict_effective":"fail"`) {
		t.Fatalf("the fresh amendment report with resolved evidence and no partial must be eligible to pass: out=%q", out2)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("report verdict lifecycle must not probe Herdr: %#v", calls)
	}
}
