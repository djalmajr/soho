package wait

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const claudeStreamPrior = "● working on the task"
const claudeStreamError = "● API Error: The response stopped arriving. The response above may be incomplete."
const claudeStreamCause = "API Error: The response stopped arriving. The response above may be incomplete."
const claudeStreamDone = "\n✻ Cogitated for 23m 53s · done 7:12 PM\n❯\n"
const claudeStreamFrame = claudeStreamPrior + "\n\n" + claudeStreamError + claudeStreamDone

const claudeStreamSession = "5f902449-376e-47a0-b0c0-c3204ab0f324"
const claudeStreamReplacement = "f9f56a57-335a-4bab-a216-0660c5b55730"

func claudeStreamGet(seq, session string) string {
	record := fmt.Sprintf(`{"result":{"agent":{"name":"worker","pane_id":"p0a","agent":"claude","agent_status":"working","agent_session":{"kind":"id","value":%q}`, session)
	if seq != "" {
		record += `,"state_change_seq":` + seq
	}
	return record + "}}}"
}

func claudeStreamFixture(t *testing.T, gets, frames []string) *queuedProbeFixture {
	t.Helper()
	root := t.TempDir()
	f := &queuedProbeFixture{
		sd: filepath.Join(root, "state", "ws"), bin: filepath.Join(root, "bin"), ctx: &core.Config{},
	}
	if err := os.MkdirAll(filepath.Join(f.sd, "wait"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("worker\tp0a\tclaude\timplementer\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var rules []fakecli.Rule
	for i, body := range gets {
		rule := fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i + 1, Stdout: body}
		if body == "gone" {
			rule.Stdout, rule.Stderr, rule.Code = "", `{"error":{"code":"agent_not_found"}}`, 137
		}
		rules = append(rules, rule)
	}
	for i, frame := range frames {
		rule := fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: i + 1, Stdout: frame}
		if frame == "unavailable" {
			rule.Stdout, rule.Stderr, rule.Code = "", "read unavailable", 1
		}
		rules = append(rules, rule)
	}
	if _, err := fakecli.Install(t, f.bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	f.env = platform.Env{
		"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root,
		"PATH": f.bin, "GORACE": "atexit_sleep_ms=0", "SOHO_FAKECLI_CONFIG": f.bin,
		"HERDR_SOCKET_PATH": filepath.Join(root, "none.sock"), "SOHO_WAIT_POLL_MS": "1",
	}
	return f
}

func TestWaitClaudeStreamStopProbes(t *testing.T) {
	record := claudeStreamGet("17", claudeStreamSession)
	for _, tc := range []struct {
		name   string
		frames []string
		edit   func([]string)
		report bool
		stop   bool
		calls  []string
	}{
		{name: "new stable stop", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame}, stop: true, calls: []string{"get", "read", "get", "read", "get", "get", "read", "get"}},
		{name: "healthy with real identity", frames: []string{claudeStreamPrior, "another working frame", "more healthy output"}, calls: []string{"get", "read", "get", "read", "get", "read"}},
		{name: "historic from first read", frames: []string{claudeStreamFrame, claudeStreamFrame, claudeStreamFrame}},
		{name: "active output after error", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame + "● more tokens\n"}},
		{name: "quoted error", frames: []string{claudeStreamPrior, claudeStreamPrior + "\n> " + claudeStreamError + claudeStreamDone, claudeStreamPrior + "\n> " + claudeStreamError + claudeStreamDone}},
		{name: "changed history", frames: []string{claudeStreamPrior, "old output\n" + claudeStreamError + claudeStreamDone, "old output\n" + claudeStreamError + claudeStreamDone}},
		{name: "vanished after visible read", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame}, edit: func(gets []string) { gets[4] = "gone" }},
		{name: "replaced after visible read", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame}, edit: func(gets []string) { gets[4] = claudeStreamGet("17", claudeStreamReplacement) }},
		{name: "sequence advanced after visible read", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame}, edit: func(gets []string) { gets[4] = claudeStreamGet("18", claudeStreamSession) }},
		{name: "missing sequence on confirmation", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame}, edit: func(gets []string) { gets[2] = claudeStreamGet("", claudeStreamSession) }},
		{name: "replaced before the candidate poll", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame}, edit: func(gets []string) {
			for i := 1; i < len(gets); i++ {
				gets[i] = claudeStreamGet("17", claudeStreamReplacement)
			}
		}},
		{name: "missing identity on confirmation", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame}, edit: func(gets []string) { gets[4] = claudeStreamGet("17", "") }},
		{name: "unavailable frame breaks corroboration", frames: []string{claudeStreamPrior, claudeStreamFrame, "unavailable"}},
		{name: "report takes precedence", frames: []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame}, report: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gets := make([]string, 5)
			for i := range gets {
				gets[i] = record
			}
			if tc.edit != nil {
				tc.edit(gets)
			}
			f := claudeStreamFixture(t, gets, tc.frames)
			svc := backend.New(f.env)
			var sample probeScreen
			report := filepath.Join(t.TempDir(), "report.md")
			for i := range tc.frames {
				if tc.report && i == 2 {
					if err := os.WriteFile(report, []byte("completed report\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				want := "working"
				if tc.stop && i == 2 {
					want = "provider-error"
				}
				if tc.report && i == 2 {
					want = "pending"
				}
				if got := probeAgent(f.sd, "worker", report, f.ctx, svc, f.env, &sample); got != want {
					t.Fatalf("probe %d = %q, want %q", i+1, got, want)
				}
			}
			cause, err := os.ReadFile(f.marker("provider-cause"))
			if tc.stop {
				if err != nil || string(cause) != claudeStreamCause+"\n" {
					t.Fatalf("provider cause = %q (%v)", cause, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("unexpected provider cause = %q (%v)", cause, err)
			}
			if tc.calls != nil {
				calls := f.calls()
				if len(calls) != len(tc.calls) {
					t.Fatalf("recorded %d transport calls, want %v", len(calls), tc.calls)
				}
				for i, call := range calls {
					want := []string{"agent", tc.calls[i], "worker"}
					if tc.calls[i] == "read" {
						want = append(want, "--source", "visible")
					}
					if !slices.Equal(call.Argv, want) {
						t.Fatalf("call %d = %v, want %v", i+1, call.Argv, want)
					}
				}
			}
			assertNoResend(t, f, tc.name)
		})
	}
}

func TestWaitClaudeStreamStopExit14(t *testing.T) {
	record := claudeStreamGet("17", claudeStreamSession)
	gets := make([]string, 5)
	for i := range gets {
		gets[i] = record
	}
	f := claudeStreamFixture(t, gets, []string{claudeStreamPrior, claudeStreamFrame, claudeStreamFrame})
	var out bytes.Buffer
	old := platform.Stdout
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = old })
	if code := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, 30000, false, t.TempDir()); code != 14 {
		t.Fatalf("wait exit = %d, stdout = %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"status":"provider-error"`) || !strings.Contains(out.String(), claudeStreamCause) {
		t.Fatalf("missing provider error or cause in %s", out.String())
	}
	assertNoResend(t, f, "Claude stream stop")
}
