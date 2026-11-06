package herdr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// sharedCodexFixture keeps all ancestry and executable reads synthetic. The
// Herdr executable is the existing Go fakecli fixture, with a private PATH/home.
func sharedCodexFixture(t *testing.T, parents map[int]int, shell int, paths map[int]string, rules ...fakecli.Rule) (platform.Env, *[]int) {
	t.Helper()
	ResetCallerCheck()
	t.Cleanup(ResetCallerCheck)
	env := callerCheckFixture(t, rules, parents)
	env["HERDR_PANE_ID"] = sessPane
	env["HOME"] = t.TempDir()
	env["USERPROFILE"] = env["HOME"]
	env["XDG_CONFIG_HOME"] = env["HOME"]
	env["CLAUDE_CONFIG_DIR"] = env["HOME"]
	delete(env, "CLAUDE_CODE_SESSION_ID")
	delete(env, "CODEX_THREAD_ID")
	t.Cleanup(OverrideCallerProcessInfoRunner(func(exe string, args []string, opts platform.RunOptions) platform.RunResult {
		if exe != "herdr" || !reflect.DeepEqual(args, []string{"pane", "process-info", "--pane", sessPane}) || opts.TimeoutMs != 10000 {
			t.Fatalf("unexpected process-info call: %q %q timeout=%d", exe, args, opts.TimeoutMs)
		}
		rule := processInfoRule(sessPane, shell)
		zero := 0
		return platform.RunResult{Status: &zero, Stdout: rule.Stdout}
	}))
	reads := []int{}
	t.Cleanup(platform.OverrideProcExecutableRunner(func(pid int) (string, error) {
		reads = append(reads, pid)
		if path, ok := paths[pid]; ok {
			return path, nil
		}
		return "", errors.New("synthetic executable unavailable")
	}))
	return env, &reads
}

func TestCallerSharedCodexChain(t *testing.T) {
	self, shell, above := selfPID(), fakePID(7001), fakePID(7002)
	native := func(name string) string { return filepath.Join(t.TempDir(), name) }
	for _, tc := range []struct {
		name         string
		paths        map[int]string
		missingShell bool
		want         CallerState
	}{
		{"none", nil, false, CallerVerified},
		{"one", map[int]string{self: native("codex")}, false, CallerVerified},
		{"one-above-shell", map[int]string{above: native("codex")}, false, CallerVerified},
		{"two-above-shell", map[int]string{self: native("codex"), above: native("codex")}, false, CallerMismatch},
		{"two-shell-absent", map[int]string{self: native("codex"), above: native("codex")}, true, CallerMismatch},
		{"case-and-exe", map[int]string{self: native("CoDeX"), above: native("CODEX.ExE")}, false, CallerMismatch},
		{"codex2", map[int]string{self: native("codex"), above: native("codex2")}, false, CallerVerified},
		{"my-codex", map[int]string{self: native("codex"), above: native("my-codex")}, false, CallerVerified},
		{"backup", map[int]string{self: native("codex"), above: native("codex.exe.bak")}, false, CallerVerified},
		{"directory-name", map[int]string{self: native("codex"), above: filepath.Join(native("codex"), "worker")}, false, CallerVerified},
		{"read-error-skipped", map[int]string{self: native("codex")}, true, CallerMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claimedShell := shell
			if tc.missingShell {
				claimedShell = fakePID(7999)
			}
			env, reads := sharedCodexFixture(t, map[int]int{self: shell, shell: above, above: 1}, claimedShell, tc.paths)
			got := CheckCallerPane(env)
			if got.State != tc.want {
				t.Fatalf("state=%#v want %s", got, tc.want)
			}
			shared := tc.name == "two-above-shell" || tc.name == "two-shell-absent" || tc.name == "case-and-exe"
			if (got.Reason == "shared codex executor") != shared {
				t.Fatalf("reason=%q shared=%v", got.Reason, shared)
			}
			if !reflect.DeepEqual(*reads, []int{self, shell, above, 1}) {
				t.Fatalf("complete chain executable reads=%v", *reads)
			}
		})
	}
	if runtime.GOOS != "windows" {
		t.Run("unix-backslash-is-a-filename", func(t *testing.T) {
			env, _ := sharedCodexFixture(t, map[int]int{self: shell, shell: 1}, shell, map[int]string{self: native("codex"), shell: `/tmp/something\codex`})
			if got := CheckCallerPane(env); got.State != CallerVerified {
				t.Fatalf("Unix backslash matched: %#v", got)
			}
		})
	}
}

func TestCallerSharedCodexEarlyReturns(t *testing.T) {
	self, shell := selfPID(), fakePID(7001)
	overLimit := map[int]int{}
	previous := self
	for i := 0; i < 66; i++ {
		next := fakePID(8000 + i)
		overLimit[previous] = next
		previous = next
	}
	for _, tc := range []struct {
		name               string
		parents            map[int]int
		noClaim, delegated bool
		shell              int
		want               CallerState
	}{
		{"absent-claim", nil, true, false, shell, CallerUnknown},
		{"delegated", nil, false, true, shell, CallerVerified},
		{"shell-unavailable", nil, false, false, 0, CallerUnknown},
		{"failed-ancestry", map[int]int{self: shell}, false, false, shell, CallerUnknown},
		{"cycle", map[int]int{self: shell, shell: self}, false, false, shell, CallerUnknown},
		{"over-limit", overLimit, false, false, shell, CallerUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, reads := sharedCodexFixture(t, tc.parents, tc.shell, nil)
			if tc.noClaim {
				delete(env, "HERDR_PANE_ID")
			}
			if tc.delegated {
				env["SOHO_DELEGATED_PANE"] = sessPane
			}
			if tc.noClaim || tc.delegated {
				t.Cleanup(OverrideCallerProcessInfoRunner(func(string, []string, platform.RunOptions) platform.RunResult {
					t.Fatal("early return read process-info/ancestry")
					return platform.RunResult{}
				}))
			}
			if got := CheckCallerPane(env); got.State != tc.want {
				t.Fatalf("early result=%#v", got)
			}
			if len(*reads) != 0 {
				t.Fatalf("early return executable reads=%v", *reads)
			}
			if calls := herdrCalls(t, env); len(calls) != 0 {
				t.Fatalf("early return Herdr reads=%v", calls)
			}
		})
	}
}

func codexPaneRecord(pane, agent, kind, id string) string {
	return fmt.Sprintf(`{"pane_id":%q,"agent_session":{"agent":%q,"kind":%q,"value":%q,"source":"herdr:codex"}}`, pane, agent, kind, id)
}

func codexListRule(records ...string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[` + strings.Join(records, ",") + `]}}`}
}

func TestCallerSharedCodexSession(t *testing.T) {
	self, shell := selfPID(), fakePID(7001)
	other := "w2:p8"
	good := codexPaneRecord(other, "codex", "id", sessOwn)
	for _, tc := range []struct {
		name, thread string
		rule         fakecli.Rule
		shown        string
		lookup       bool
	}{
		{"missing-thread", "", codexListRule(good), "", false},
		{"invalid-thread", "../bad", codexListRule(good), "", false},
		{"uppercase-thread", strings.ToUpper(sessOwn), codexListRule(good), "", false},
		{"unique-other", sessOwn, codexListRule(good), other, true},
		{"source-is-metadata", sessOwn, codexListRule(strings.Replace(good, "herdr:codex", "untrusted-source", 1)), other, true},
		{"no-match", sessOwn, codexListRule(codexPaneRecord(other, "codex", "id", sessOther)), "", true},
		{"multiple-other", sessOwn, codexListRule(good, codexPaneRecord("w3:p7", "codex", "id", sessOwn)), "", true},
		{"duplicate-candidate", sessOwn, codexListRule(good, good), "", true},
		{"claimed-excluded", sessOwn, codexListRule(codexPaneRecord(sessPane, "codex", "id", sessOwn)), "", true},
		{"claimed-and-other", sessOwn, codexListRule(codexPaneRecord(sessPane, "codex", "id", sessOwn), good), other, true},
		{"excluded-shapes", sessOwn, codexListRule(
			codexPaneRecord("w3:p7", "claude", "id", sessOwn),
			codexPaneRecord("w3:p8", "codex", "title", sessOwn),
			codexPaneRecord("w3:p9", "codex", "id", "bad"),
			codexPaneRecord("w3:p10", "codex", "id", strings.ToUpper(sessOwn)),
			codexPaneRecord("bad pane", "codex", "id", sessOwn), good), other, true},
		{"failed-list", sessOwn, fakecli.Rule{Argv: []string{"pane", "list"}, Code: 1}, "", true},
		{"malformed-json", sessOwn, fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":`}, "", true},
		{"malformed-list", sessOwn, fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":{}}}`}, "", true},
		{"error-envelope", sessOwn, fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"error":{"message":"failed"},"result":{"panes":[` + good + `]}}`}, "", true},
		{"malformed-record", sessOwn, codexListRule(good, `{"pane_id":"w3:p8","agent_session":{"agent":42}}`), "", true},
		{"null-record", sessOwn, codexListRule(good, `null`), "", true},
		{"trailing-json", sessOwn, fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: codexListRule(good).Stdout + ` {}`}, "", true},
		{"timeout", sessOwn, fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: codexListRule(good).Stdout, Delay: callerSessionTimeoutMs + 100}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := sharedCodexFixture(t, map[int]int{self: shell, shell: 1}, fakePID(7999), map[int]string{self: filepath.Join("bin", "codex"), shell: filepath.Join("bin", "codex.exe")}, tc.rule)
			env["CODEX_THREAD_ID"] = tc.thread
			env["CLAUDE_CODE_SESSION_ID"] = sessOwn
			oldList, oldRead := listProjects, readTranscriptTail
			listProjects = func(string) ([]os.DirEntry, error) {
				t.Fatal("Codex diagnostic listed Claude projects")
				return nil, nil
			}
			readTranscriptTail = func(string, int64) ([]byte, bool) { t.Fatal("Codex diagnostic read transcript"); return nil, false }
			t.Cleanup(func() { listProjects, readTranscriptTail = oldList, oldRead })
			got := CheckCallerPane(env)
			if got.State != CallerMismatch || got.Reason != CallerReasonSharedCodexExecutor || got.ShownIn != tc.shown || got.Pane != sessPane {
				t.Fatalf("diagnostic result=%#v want shown=%q", got, tc.shown)
			}
			wantSession := ""
			if validSessionID(tc.thread) {
				wantSession = tc.thread
			}
			if got.CodexSession != wantSession || got.Session != "" || got.PaneSession != "" {
				t.Fatalf("session fields mixed: %#v", got)
			}
			calls := herdrCalls(t, env)
			wantCalls := 0
			if tc.lookup {
				wantCalls = 1
			}
			if len(calls) != wantCalls || (wantCalls == 1 && !reflect.DeepEqual(calls[0], []string{"pane", "list"})) {
				t.Fatalf("session calls=%v want count=%d", calls, wantCalls)
			}
		})
	}
}

func TestCallerSharedCodexClaudePrecedence(t *testing.T) {
	self, shell := selfPID(), fakePID(7001)
	for _, two := range []bool{false, true} {
		t.Run(fmt.Sprintf("two=%v", two), func(t *testing.T) {
			paths := map[int]string{self: filepath.Join("bin", "codex")}
			if two {
				paths[shell] = filepath.Join("bin", "codex")
			}
			env, _ := sharedCodexFixture(t, map[int]int{self: shell, shell: 1}, fakePID(7999), paths, paneGetRule(sessPane, "claude", "id", sessOwn))
			env["CLAUDE_CODE_SESSION_ID"] = sessOwn
			got := CheckCallerPane(env)
			if two {
				if got.State != CallerMismatch || got.Reason != CallerReasonSharedCodexExecutor {
					t.Fatalf("Claude overrode shared executor: %#v", got)
				}
				if calls := herdrCalls(t, env); len(calls) != 0 {
					t.Fatalf("shared executor queried Claude: %v", calls)
				}
			} else if got.State != CallerVerified || got.Reason != "the pane's Claude Code session is this process's session" {
				t.Fatalf("single executor changed Claude verification: %#v", got)
			}
		})
	}
}
