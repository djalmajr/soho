package wait

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const waitMCPScreen = "MCP Server Approval Required\n[a] Approve all   [c] Continue   [q] Quit\n"

// waitMCPRealScreen reads the real captured MCP dialog (cursor-agent
// 2026.10.22) shared with the cursorapproval package's fixtures.
func waitMCPRealScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "spawn", "cursorapproval", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertCursorWaitNoInput(t *testing.T, f *queuedProbeFixture) {
	t.Helper()
	for _, c := range f.calls() {
		if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys" || c.Argv[1] == "send-text") {
			t.Fatalf("input over MCP: %#v", c)
		}
	}
}

func TestWaitCursorMCPBlocksBeforeStateTransitions(t *testing.T) {
	for _, state := range []string{"idle", "blocked", "working"} {
		t.Run(state, func(t *testing.T) {
			markers := map[string]string{"queued": "1 1 /brief.md\n", "not-received": "1 1 /brief.md\n", "approvals": "2\n"}
			f := newQueuedProbeFixture(t, state, "1", waitMCPScreen, markers)
			f.cursorRoster(t, "cursor")
			f.env["SOHO_AUTO_APPROVE"] = "on"
			if got := f.probe(""); got != "blocked" {
				t.Fatalf("state=%s result=%q calls=%#v", state, got, f.calls())
			}
			assertCursorWaitNoInput(t, f)
			for name, want := range markers {
				b, e := os.ReadFile(f.marker(name))
				if e != nil || string(b) != want {
					t.Errorf("%s=%q error=%v want=%q", name, b, e, want)
				}
			}
		})
	}
}

// TestWaitCursorMCPRealScreenBlocks: the real captured screens (tall and
// short panels) block the wait probe before any state transition — no key
// or text is sent, with auto_approve=on.
func TestWaitCursorMCPRealScreenBlocks(t *testing.T) {
	for name, screen := range map[string]string{
		"tall":  waitMCPRealScreen(t, "mcp-dialog-2026.10.22-tall.txt"),
		"short": waitMCPRealScreen(t, "mcp-dialog-2026.10.22-short.txt"),
	} {
		for _, state := range []string{"idle", "blocked", "working"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				markers := map[string]string{"queued": "1 1 /brief.md\n", "not-received": "1 1 /brief.md\n", "approvals": "2\n"}
				f := newQueuedProbeFixture(t, state, "1", screen, markers)
				f.cursorRoster(t, "cursor")
				f.env["SOHO_AUTO_APPROVE"] = "on"
				if got := f.probe(""); got != "blocked" {
					t.Fatalf("state=%s result=%q calls=%#v", state, got, f.calls())
				}
				assertCursorWaitNoInput(t, f)
				for n, want := range markers {
					b, e := os.ReadFile(f.marker(n))
					if e != nil || string(b) != want {
						t.Errorf("%s=%q error=%v want=%q", n, b, e, want)
					}
				}
			})
		}
	}
}

// partialMCPAutoApproveFixture stands up the TryAutoApprove state (cursor
// roster, approvals marker) and serves the given visible screen — or a
// failing read — from a dedicated fake herdr, so the guard's fresh read is
// the scripted one.
func partialMCPAutoApproveFixture(t *testing.T, screen string, readFailed bool) *queuedProbeFixture {
	t.Helper()
	f := newQueuedProbeFixture(t, "blocked", "1", "", map[string]string{"approvals": "2\n"})
	f.cursorRoster(t, "cursor")
	bin := filepath.Join(t.TempDir(), "bin")
	readRule := fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen}
	if readFailed {
		readRule.Stdout = ""
		readRule.Code = 1
		readRule.Stderr = `{"error":{"code":"read_failed","message":"the fake read failed"}}`
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked","state_change_seq":1}}}`},
		readRule,
		{Argv: []string{"agent", "send-keys", "worker"}, ArgvPrefix: true},
	}); err != nil {
		t.Fatal(err)
	}
	f.bin = bin
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
	f.env["SOHO_AUTO_APPROVE"] = "on"
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		f.env[key] = t.TempDir()
	}
	return f
}

// TestWaitCursorMCPPartialScreenNoAutoApprove: with auto_approve=on the
// clipped or wrapped approval screens — and an unreadable screen — refuse
// the automatic approval: TryAutoApprove returns false and no key or text
// is sent into the pane.
func TestWaitCursorMCPPartialScreenNoAutoApprove(t *testing.T) {
	short := waitMCPRealScreen(t, "mcp-dialog-2026.10.22-short.txt")
	tall := waitMCPRealScreen(t, "mcp-dialog-2026.10.22-tall.txt")
	wrapped := strings.ReplaceAll(tall, "Use arrow keys to navigate, Enter to select, or press the key shown", "Use arrow keys to navigate, Enter to select,\n  │  or press the key shown")
	for name, screen := range map[string]string{
		"wrapped footer":         wrapped,
		"clipped c q and footer": strings.ReplaceAll(short, "▶ [a] Approve all servers", ""),
	} {
		t.Run(name, func(t *testing.T) {
			f := partialMCPAutoApproveFixture(t, screen, false)
			if got := TryAutoApprove(f.sd, "worker", f.ctx, f.env); got {
				t.Fatalf("partial screen auto-approved: calls=%#v", f.calls())
			}
			assertCursorWaitNoInput(t, f)
		})
	}
	t.Run("unreadable screen", func(t *testing.T) {
		f := partialMCPAutoApproveFixture(t, "", true)
		if got := TryAutoApprove(f.sd, "worker", f.ctx, f.env); got {
			t.Fatalf("unreadable screen auto-approved: calls=%#v", f.calls())
		}
		assertCursorWaitNoInput(t, f)
	})
}

func TestWaitCursorMCPTryAutoApprove(t *testing.T) {
	for _, mcp := range []bool{true, false} {
		screen := "ordinary approval\n"
		if mcp {
			screen = waitMCPScreen
		}
		f := newQueuedProbeFixture(t, "blocked", "1", screen, map[string]string{"approvals": "2\n"})
		f.cursorRoster(t, "cursor")
		f.env["SOHO_AUTO_APPROVE"] = "on"
		got := TryAutoApprove(f.sd, "worker", f.ctx, f.env)
		if got == mcp {
			t.Fatalf("mcp=%v approved=%v calls=%#v", mcp, got, f.calls())
		}
		if mcp {
			assertCursorWaitNoInput(t, f)
		} else {
			n := 0
			for _, c := range f.calls() {
				if len(c.Argv) > 1 && c.Argv[1] == "send-keys" {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("control keys=%d", n)
			}
		}
	}
}

func lateCursorMCPFixture(t *testing.T, markers map[string]string, recent string, mcp bool) *queuedProbeFixture {
	t.Helper()
	f := newQueuedProbeFixture(t, "idle", "1", "cursor ready\n", markers)
	f.cursorRoster(t, "cursor")
	visible := []string{"agent", "read", "worker", "--source", "visible"}
	latest := "cursor ready\n"
	if mcp {
		latest = waitMCPScreen
	}
	bin := filepath.Join(t.TempDir(), "bin")
	_, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`},
		{Argv: visible, Call: 1, Stdout: "cursor ready\n"},
		{Argv: visible, Stdout: latest},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: recent},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		{Argv: []string{"agent", "send-keys", "worker"}, ArgvPrefix: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.bin = bin
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
	f.env["SOHO_AUTO_APPROVE"], f.env["SOHO_PROVIDER_RETRIES"], f.env["SOHO_PROVIDER_RETRY_DELAY"] = "on", "1", "0"
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		f.env[key] = t.TempDir()
	}
	return f
}

func TestWaitCursorMCPLateNotReceivedRetry(t *testing.T) {
	f := lateCursorMCPFixture(t, map[string]string{"not-received": "1 1 /brief.md\n"}, "old output\n", true)
	if got := f.probe(""); got != "blocked" {
		t.Fatalf("probe=%q calls=%#v", got, f.calls())
	}
	assertCursorWaitNoInput(t, f)
}

func TestWaitCursorMCPLateQueuedRetry(t *testing.T) {
	f := lateCursorMCPFixture(t, map[string]string{"queued": "1 1 /brief.md\n"}, "old output\n", true)
	if got := f.probe(""); got != "blocked" {
		t.Fatalf("probe=%q calls=%#v", got, f.calls())
	}
	if b, e := os.ReadFile(f.marker("queued")); e != nil || string(b) != "1 1 /brief.md\n" {
		t.Fatalf("queued=%q error=%v", b, e)
	}
	assertCursorWaitNoInput(t, f)
}

func TestWaitCursorMCPLateCapacityResume(t *testing.T) {
	text := `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	for _, mcp := range []bool{true, false} {
		f := lateCursorMCPFixture(t, map[string]string{"provider": fmt.Sprintf("%d\ncapacity\n", CksumField(text)), "capacity-at": "1\n", "capacity-retries": "0\n"}, text, mcp)
		got := f.probe("")
		if mcp {
			if got != "blocked" {
				t.Fatalf("probe=%q calls=%#v", got, f.calls())
			}
			assertCursorWaitNoInput(t, f)
		} else {
			if got != "working" {
				t.Fatalf("control probe=%q", got)
			}
			b, e := os.ReadFile(f.marker("capacity-retries"))
			if e != nil || string(b) != "1\n" {
				t.Fatalf("capacity branch not taken: retries=%q error=%v calls=%#v", b, e, f.calls())
			}
			n := 0
			for _, c := range f.calls() {
				if len(c.Argv) > 1 && c.Argv[1] == "prompt" {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("capacity resume prompts=%d calls=%#v", n, f.calls())
			}
		}
	}
}
