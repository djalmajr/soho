package herdr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// caller_session_test.go covers the session check that runs only when the
// process tree misses the pane's shell (DJA-406): a Claude Code background
// session does not descend from the shell of the pane that shows it, but
// the pane's own session (herdr agent_session) either is this process's
// session or ends in a continued-in record that leads to it. Every test
// builds its own environment: the CLAUDE_CODE_SESSION_ID of the process
// that runs the tests never reaches it, and the Claude directory is a
// temporary CLAUDE_CONFIG_DIR with synthetic transcripts.

const (
	sessPane   = "w1:p9"
	sessShell  = 4000
	sessOwn    = "66bf36b5-9b48-4b58-88a3-5da128ff288b" // S
	sessPaneA  = "d630d58c-2c56-4147-9bdb-206cba98b68e" // A
	sessMid    = "11111111-1111-4111-8111-111111111111" // B
	sessMid2   = "22222222-2222-4222-8222-222222222222"
	sessOther  = "33333333-3333-4333-8333-333333333333"
	sessTarget = "44444444-4444-4444-8444-444444444444"
)

// paneGetRule is the fake rule for `herdr pane get <pane>`.
func paneGetRule(pane, agent, kind, value string) fakecli.Rule {
	return fakecli.Rule{
		Argv:   []string{"pane", "get", pane},
		Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"agent_session":{"agent":%q,"kind":%q,"source":"herdr:claude","value":%q}}}}`, pane, agent, kind, value),
	}
}

// sessionEnv is a mismatching tree (the process never reaches the shell
// 4000), the claim on sessPane, a temporary CLAUDE_CONFIG_DIR and the
// process session S. The ancestry and the herdr fake are scripted by the
// caller's rules.
func sessionEnv(t *testing.T, own string, rules ...fakecli.Rule) platform.Env {
	t.Helper()
	ResetCallerCheck()
	rules = append([]fakecli.Rule{processInfoRule(sessPane, sessShell)}, rules...)
	env := callerCheckFixture(t, rules, map[int]int{selfPID(): 3000, 3000: 1})
	env["HERDR_PANE_ID"] = sessPane
	env["CLAUDE_CONFIG_DIR"] = t.TempDir()
	delete(env, "CLAUDE_CODE_SESSION_ID")
	if own != "" {
		env["CLAUDE_CODE_SESSION_ID"] = own
	}
	return env
}

// writeSessionTranscript writes lines (each one newline-terminated) to
// <CLAUDE_CONFIG_DIR>/projects/<project>/<id>.jsonl and returns the path.
func writeSessionTranscript(t *testing.T, env platform.Env, project, id string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(env["CLAUDE_CONFIG_DIR"], "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func continuedIn(from, to string) string {
	return fmt.Sprintf(`{"type":"continued-in","timestamp":"2026-10-17T18:41:35.132Z","sessionId":%q,"continuedInSessionId":%q}`, from, to)
}

// noise is a message-like line the check must never read as a record.
func noise(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(`{"type":"assistant","n":%d,"message":{"content":"secret text %d"}}`, i, i)
	}
	return out
}

func herdrCalls(t *testing.T, env platform.Env) [][]string {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(env["SOHO_FAKECLI_CONFIG"] + "/herdr.json")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out := make([][]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Argv)
	}
	return out
}

func countCalls(calls [][]string, prefix ...string) int {
	n := 0
	for _, argv := range calls {
		if len(argv) >= len(prefix) && strings.Join(argv[:len(prefix)], " ") == strings.Join(prefix, " ") {
			n++
		}
	}
	return n
}

func TestCheckCallerPaneSession(t *testing.T) {
	t.Run("the pane's session is this process's session", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessOwn))
		got := CheckCallerPane(env)
		if got.State != CallerVerified || got.Reason != "the pane's Claude Code session is this process's session" || got.Pane != sessPane {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("a continued-in in the last lines leads to this process's session", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		writeSessionTranscript(t, env, "-Users-x-edger", sessPaneA, append(noise(20), continuedIn(sessPaneA, sessOwn))...)
		got := CheckCallerPane(env)
		if got.State != CallerVerified || got.Reason != "the pane's Claude Code session continued in this process's session" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("two hops, A to B to S, are verified", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessMid))
		writeSessionTranscript(t, env, "-p", sessMid, continuedIn(sessMid, sessOwn))
		if got := CheckCallerPane(env); got.State != CallerVerified {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("three hops are a mismatch", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessMid))
		writeSessionTranscript(t, env, "-p", sessMid, continuedIn(sessMid, sessMid2))
		writeSessionTranscript(t, env, "-p", sessMid2, continuedIn(sessMid2, sessOwn))
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("a continued-in to S outside the last 64 KiB is a mismatch", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		lines := append([]string{continuedIn(sessPaneA, sessOwn)}, noise(1500)...) // ~100 KB after it
		path := writeSessionTranscript(t, env, "-p", sessPaneA, lines...)
		if info, err := os.Stat(path); err != nil || info.Size() < 70*1024 {
			t.Fatalf("fixture too small: %v %v", info, err)
		}
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("a record that sits inside the last 64 KiB is found in a large transcript", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		lines := append(noise(1500), continuedIn(sessPaneA, sessOwn))
		lines = append(lines, noise(10)...)
		writeSessionTranscript(t, env, "-p", sessPaneA, lines...)
		if got := CheckCallerPane(env); got.State != CallerVerified {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("an unterminated last line is not a complete line", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		path := writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessOwn))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.TrimSuffix(string(raw), "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("several continued-in records and several destinations are all considered", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		writeSessionTranscript(t, env, "-p", sessPaneA,
			continuedIn(sessPaneA, sessOwn), continuedIn(sessPaneA, sessOther))
		if got := CheckCallerPane(env); got.State != CallerVerified {
			t.Fatalf("S first: CheckCallerPane=%#v", got)
		}
		env = sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		writeSessionTranscript(t, env, "-p", sessPaneA,
			continuedIn(sessPaneA, sessOther), continuedIn(sessPaneA, sessOwn))
		if got := CheckCallerPane(env); got.State != CallerVerified {
			t.Fatalf("S last: CheckCallerPane=%#v", got)
		}
	})

	t.Run("at most four destinations per hop are followed", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		var lines []string
		// S is the oldest of five destinations; the four most recent win.
		lines = append(lines, continuedIn(sessPaneA, sessOwn))
		for i := 1; i <= 4; i++ {
			lines = append(lines, continuedIn(sessPaneA, fmt.Sprintf("5555555%d-5555-4555-8555-555555555555", i)))
		}
		writeSessionTranscript(t, env, "-p", sessPaneA, lines...)
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("a transcript without a link, or with malformed records, is a mismatch", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		writeSessionTranscript(t, env, "-p", sessPaneA,
			"not json",
			`{"type":"continued-in","continuedInSessionId":42}`,
			`{"type":"continued-in","continuedInSessionId":"`+strings.ToUpper(sessOwn)+`"}`,
			`{"type":"continued-in","continuedInSessionId":"../`+sessOwn+`"}`,
			`{"type":"assistant","continuedInSessionId":"`+sessOwn+`"}`,
			continuedIn(sessPaneA, sessOther))
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("S absent or outside the UUID format changes nothing", func(t *testing.T) {
		for _, own := range []string{"", "not-a-uuid", strings.ToUpper(sessOwn), sessOwn + "x", "../" + sessOwn, " " + sessOwn} {
			env := sessionEnv(t, "", paneGetRule(sessPane, "claude", "id", sessOwn))
			if own != "" {
				env["CLAUDE_CODE_SESSION_ID"] = own
			}
			got := CheckCallerPane(env)
			if got.State != CallerMismatch || got.ShellPID != sessShell || got.Session != "" {
				t.Fatalf("S=%q: CheckCallerPane=%#v", own, got)
			}
			if n := countCalls(herdrCalls(t, env), "pane", "get"); n != 0 {
				t.Fatalf("S=%q: %d pane get calls, want none", own, n)
			}
		}
	})

	t.Run("an agent_session that is not a claude id stays a mismatch", func(t *testing.T) {
		for name, rule := range map[string]fakecli.Rule{
			"kind path":     paneGetRule(sessPane, "claude", "path", sessOwn),
			"agent codex":   paneGetRule(sessPane, "codex", "id", sessOwn),
			"empty value":   paneGetRule(sessPane, "claude", "id", ""),
			"upper-case id": paneGetRule(sessPane, "claude", "id", strings.ToUpper(sessOwn)),
			"no session":    {Argv: []string{"pane", "get", sessPane}, Stdout: `{"result":{"pane":{"pane_id":"w1:p9"}}}`},
			"not json":      {Argv: []string{"pane", "get", sessPane}, Stdout: `nope`},
			"wrong shape":   {Argv: []string{"pane", "get", sessPane}, Stdout: `{"result":{"pane":{"agent_session":["x"]}}}`},
		} {
			env := sessionEnv(t, sessOwn, rule)
			if got := CheckCallerPane(env); got.State != CallerMismatch || got.PaneSession != "" {
				t.Fatalf("%s: CheckCallerPane=%#v", name, got)
			}
		}
	})

	t.Run("a missing transcript, or two for the same A, is a mismatch", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("missing: CheckCallerPane=%#v", got)
		}
		env = sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		writeSessionTranscript(t, env, "-one", sessPaneA, continuedIn(sessPaneA, sessOwn))
		writeSessionTranscript(t, env, "-two", sessPaneA, continuedIn(sessPaneA, sessOwn))
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("two transcripts: CheckCallerPane=%#v", got)
		}
		// A directory named like the transcript is not a transcript.
		env = sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		if err := os.MkdirAll(filepath.Join(env["CLAUDE_CONFIG_DIR"], "projects", "-p", sessPaneA+".jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := CheckCallerPane(env); got.State != CallerMismatch {
			t.Fatalf("directory: CheckCallerPane=%#v", got)
		}
	})

	t.Run("a hostile A never reaches a file outside projects", func(t *testing.T) {
		for name, hostile := range map[string]string{
			"parent dir":      "../x",
			"nested parent":   "../decoy/" + sessPaneA,
			"absolute":        "__ABS__",
			"upper-case uuid": strings.ToUpper(sessPaneA),
			"glob":            "*",
			"glob in uuid":    "d630d58c-2c56-4147-9bdb-206cba98b6*",
			"trailing":        sessPaneA + "/../" + sessPaneA,
		} {
			root := t.TempDir()
			// The decoy sits beside projects/, where "../x" and the absolute
			// path would land. It names S, so reading it would verify.
			decoyDir := filepath.Join(root, "decoy")
			if err := os.MkdirAll(decoyDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, decoy := range []string{"x.jsonl", sessPaneA + ".jsonl", strings.ToUpper(sessPaneA) + ".jsonl"} {
				if err := os.WriteFile(filepath.Join(decoyDir, decoy), []byte(continuedIn(sessPaneA, sessOwn)+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(root, "projects", "-p"), 0o755); err != nil {
				t.Fatal(err)
			}
			value := hostile
			if value == "__ABS__" {
				value = filepath.ToSlash(filepath.Join(decoyDir, sessPaneA))
			}
			env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", value))
			env["CLAUDE_CONFIG_DIR"] = root
			got := CheckCallerPane(env)
			if got.State != CallerMismatch || got.PaneSession != "" {
				t.Fatalf("%s (%q): CheckCallerPane=%#v", name, value, got)
			}
		}
	})

	t.Run("pane get that fails or times out stays a mismatch", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, fakecli.Rule{Argv: []string{"pane", "get", sessPane}, Stderr: `{"error":{"code":"pane_not_found","message":"gone"}}`, Code: 1})
		if got := CheckCallerPane(env); got.State != CallerMismatch || got.ShellPID != sessShell {
			t.Fatalf("failure: CheckCallerPane=%#v", got)
		}
		env = sessionEnv(t, sessOwn, fakecli.Rule{Argv: []string{"pane", "get", sessPane}, Delay: 11000, Stdout: `{}`})
		if got := CheckCallerPane(env); got.State != CallerMismatch || got.ShellPID != sessShell {
			t.Fatalf("timeout: CheckCallerPane=%#v", got)
		}
	})

	t.Run("a tree that reaches the shell never calls pane get", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{processInfoRule(sessPane, sessShell), paneGetRule(sessPane, "claude", "id", sessOwn)},
			map[int]int{selfPID(): sessShell, sessShell: 1})
		env["HERDR_PANE_ID"] = sessPane
		env["CLAUDE_CONFIG_DIR"] = t.TempDir()
		env["CLAUDE_CODE_SESSION_ID"] = sessOwn
		got := CheckCallerPane(env)
		if got.State != CallerVerified || got.Reason != "tree reaches the pane's shell" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		calls := herdrCalls(t, env)
		if countCalls(calls, "pane", "get") != 0 || countCalls(calls, "pane", "list") != 0 {
			t.Fatalf("herdr calls = %v, want only process-info", calls)
		}
	})

	t.Run("an unknown shell_pid is unknown and never reaches the session check", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t,
			[]fakecli.Rule{{Argv: []string{"pane", "process-info", "--pane", sessPane}, Code: 1}, paneGetRule(sessPane, "claude", "id", sessOwn)},
			map[int]int{selfPID(): 3000, 3000: 1})
		env["HERDR_PANE_ID"] = sessPane
		env["CLAUDE_CODE_SESSION_ID"] = sessOwn
		if got := CheckCallerPane(env); got.State != CallerUnknown {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		if n := countCalls(herdrCalls(t, env), "pane", "get"); n != 0 {
			t.Fatalf("%d pane get calls", n)
		}
	})

	t.Run("a delegated pane is verified before any session read", func(t *testing.T) {
		ResetCallerCheck()
		env := callerCheckFixture(t, nil, nil)
		env["HERDR_PANE_ID"] = sessPane
		env["SOHO_DELEGATED_PANE"] = sessPane
		env["CLAUDE_CODE_SESSION_ID"] = sessOwn
		if got := CheckCallerPane(env); got.State != CallerVerified || got.Reason != "delegated pane" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("the session check is memoized with the rest of the result", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessOwn))
		first := CheckCallerPane(env)
		second := CheckCallerPane(env)
		if first != second || first.State != CallerVerified {
			t.Fatalf("first=%#v second=%#v", first, second)
		}
		if n := countCalls(herdrCalls(t, env), "pane", "get"); n != 1 {
			t.Fatalf("pane get calls=%d want 1", n)
		}
	})

	t.Run("CLAUDE_CONFIG_DIR wins; without it the home .claude is used", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		home := t.TempDir()
		delete(env, "CLAUDE_CONFIG_DIR")
		env["HOME"] = home
		env["USERPROFILE"] = home
		dir := filepath.Join(home, ".claude", "projects", "-p")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sessPaneA+".jsonl"), []byte(continuedIn(sessPaneA, sessOwn)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := CheckCallerPane(env); got.State != CallerVerified {
			t.Fatalf("home: CheckCallerPane=%#v", got)
		}
		// An empty CLAUDE_CONFIG_DIR counts as absent.
		ResetCallerCheck()
		env["CLAUDE_CONFIG_DIR"] = ""
		if got := CheckCallerPane(env); got.State != CallerVerified {
			t.Fatalf("empty config dir: CheckCallerPane=%#v", got)
		}
	})
}

// paneListRule is the fake rule for `herdr pane list` (no --workspace: all
// workspaces). Each entry is a pane id and its Claude session id ("" = a
// pane with no agent session).
func paneListRule(entries ...[2]string) fakecli.Rule {
	panes := make([]string, 0, len(entries))
	for _, e := range entries {
		if e[1] == "" {
			panes = append(panes, fmt.Sprintf(`{"pane_id":%q,"agent_status":"unknown"}`, e[0]))
			continue
		}
		panes = append(panes, fmt.Sprintf(`{"pane_id":%q,"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":%q}}`, e[0], e[1]))
	}
	return fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[` + strings.Join(panes, ",") + `]}}`}
}

func TestCheckCallerPaneSessionSuggestion(t *testing.T) {
	// The claimed pane hosts another project's session A; the pane that
	// shows this process's session is w2:p3.
	claimedRule := paneGetRule(sessPane, "claude", "id", sessOther)

	t.Run("the one pane whose session is S is named", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, claimedRule,
			paneListRule([2]string{sessPane, sessOther}, [2]string{"w2:p3", sessOwn}, [2]string{"w2:p4", sessMid}, [2]string{"w2:p5", ""}))
		got := CheckCallerPane(env)
		if got.State != CallerMismatch || got.ShownIn != "w2:p3" || got.Session != sessOwn || got.PaneSession != sessOther || got.ShellPID != sessShell {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("a pane whose session continued in S is named", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule([2]string{sessPane, sessOther}, [2]string{"w24:p1", sessPaneA}, [2]string{"w2:p4", sessMid}))
		writeSessionTranscript(t, env, "-edger", sessPaneA, append(noise(5), continuedIn(sessPaneA, sessOwn))...)
		writeSessionTranscript(t, env, "-other", sessMid, continuedIn(sessMid, sessMid2))
		got := CheckCallerPane(env)
		if got.State != CallerMismatch || got.ShownIn != "w24:p1" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("a two-hop chain from another pane is named; three hops are not", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule([2]string{"w3:p1", sessPaneA}))
		writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessMid))
		writeSessionTranscript(t, env, "-p", sessMid, continuedIn(sessMid, sessOwn))
		if got := CheckCallerPane(env); got.ShownIn != "w3:p1" {
			t.Fatalf("two hops: CheckCallerPane=%#v", got)
		}
		env = sessionEnv(t, sessOwn, claimedRule, paneListRule([2]string{"w3:p1", sessPaneA}))
		writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessMid))
		writeSessionTranscript(t, env, "-p", sessMid, continuedIn(sessMid, sessMid2))
		writeSessionTranscript(t, env, "-p", sessMid2, continuedIn(sessMid2, sessOwn))
		if got := CheckCallerPane(env); got.ShownIn != "" {
			t.Fatalf("three hops: CheckCallerPane=%#v", got)
		}
	})

	t.Run("two candidate panes give no suggestion", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule([2]string{"w2:p3", sessOwn}, [2]string{"w2:p4", sessPaneA}))
		writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessOwn))
		got := CheckCallerPane(env)
		if got.State != CallerMismatch || got.ShownIn != "" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("the claimed pane is never its own suggestion", func(t *testing.T) {
		// The claimed pane's session is not S here; even if the list shows
		// it with S (a race with pane get), it is excluded.
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule([2]string{sessPane, sessOwn}))
		if got := CheckCallerPane(env); got.ShownIn != "" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("no candidate, a failing list, or a hostile pane id give no suggestion", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule([2]string{"w2:p4", sessMid}))
		if got := CheckCallerPane(env); got.ShownIn != "" {
			t.Fatalf("no candidate: CheckCallerPane=%#v", got)
		}
		env = sessionEnv(t, sessOwn, claimedRule, fakecli.Rule{Argv: []string{"pane", "list"}, Stderr: "boom", Code: 1})
		if got := CheckCallerPane(env); got.State != CallerMismatch || got.ShownIn != "" {
			t.Fatalf("failing list: CheckCallerPane=%#v", got)
		}
		env = sessionEnv(t, sessOwn, claimedRule, fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":"x"}}`})
		if got := CheckCallerPane(env); got.ShownIn != "" {
			t.Fatalf("wrong shape: CheckCallerPane=%#v", got)
		}
		env = sessionEnv(t, sessOwn, claimedRule, paneListRule([2]string{"w2:p3\n; rm -rf /", sessOwn}, [2]string{"w2 p3", sessOwn}))
		if got := CheckCallerPane(env); got.ShownIn != "" {
			t.Fatalf("hostile pane id: CheckCallerPane=%#v", got)
		}
	})

	t.Run("a failing pane get still looks for the pane that shows S", func(t *testing.T) {
		env := sessionEnv(t, sessOwn,
			fakecli.Rule{Argv: []string{"pane", "get", sessPane}, Stderr: "gone", Code: 1},
			paneListRule([2]string{"w2:p3", sessOwn}))
		got := CheckCallerPane(env)
		if got.State != CallerMismatch || got.ShownIn != "w2:p3" || got.PaneSession != "" || got.Session != sessOwn {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("without S there is no suggestion and no read", func(t *testing.T) {
		env := sessionEnv(t, "", claimedRule, paneListRule([2]string{"w2:p3", sessOwn}))
		got := CheckCallerPane(env)
		if got.State != CallerMismatch || got.ShownIn != "" || got.Session != "" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		calls := herdrCalls(t, env)
		if countCalls(calls, "pane", "get") != 0 || countCalls(calls, "pane", "list") != 0 {
			t.Fatalf("herdr calls = %v", calls)
		}
	})

	t.Run("the list is read once per command", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule([2]string{"w2:p3", sessOwn}))
		CheckCallerPane(env)
		CheckCallerPane(env)
		if n := countCalls(herdrCalls(t, env), "pane", "list"); n != 1 {
			t.Fatalf("pane list calls=%d want 1", n)
		}
	})
}

func TestSessionChainGuards(t *testing.T) {
	t.Run("a session id outside the UUID format never reaches a path", func(t *testing.T) {
		root := t.TempDir()
		decoy := filepath.Join(root, "decoy")
		if err := os.MkdirAll(filepath.Join(decoy), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "projects", "-p"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"x.jsonl", strings.ToUpper(sessPaneA) + ".jsonl"} {
			if err := os.WriteFile(filepath.Join(decoy, name), []byte(continuedIn("x", sessOwn)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		chain := &sessionChain{root: root, targets: map[string][]string{}}
		for _, hostile := range []string{"../decoy/x", "../../decoy/x", strings.ToUpper(sessPaneA), "", "*", filepath.Join(decoy, "x")} {
			if _, ok := chain.reaches(hostile, sessOwn); ok {
				t.Fatalf("reaches(%q) = true", hostile)
			}
			if got := chain.continuedFrom(hostile); len(got) != 0 {
				t.Fatalf("continuedFrom(%q) = %v", hostile, got)
			}
		}
	})

	t.Run("destinations outside the UUID format take no slot of the four", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA))
		lines := []string{continuedIn(sessPaneA, sessOwn)}
		for i := 0; i < 4; i++ {
			lines = append(lines, continuedIn(sessPaneA, fmt.Sprintf("../%d", i)))
		}
		writeSessionTranscript(t, env, "-p", sessPaneA, lines...)
		if got := CheckCallerPane(env); got.State != CallerVerified {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})
}
