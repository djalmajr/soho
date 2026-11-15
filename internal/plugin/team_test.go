package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const teamRosterFixture = "NAME  KIND  STATUS  TASK\n" +
	"worker-1  codex  working  implement\n" +
	"worker-2  grok  idle  scout\n" +
	"\n" +
	"# other live agents\n" +
	"other-1  claude  idle  elsewhere\n"

const teamExplainFixture = "A equipe está implementando o módulo de relatórios.\n" +
	"worker-1 implementa e worker-2 faz a revisão.\n"

// teamPanelFixture installs the fake herdr (pane get) and the fake soho
// CLI (explain, roster, doctor, friction, gc, collect, release) with env
// capture, plus the panel's environment (the focused context and shell
// HERDR_* decoys that the target ids must win over).
func teamPanelFixture(t *testing.T) (platform.Env, string, string) {
	t.Helper()
	dir := t.TempDir()
	cwd := t.TempDir()
	paneJSON, _ := json.Marshal(map[string]any{"result": map[string]any{"pane": map[string]any{"workspace_id": "ws-a", "cwd": cwd}}})
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "get", "pane-a"}, Stdout: string(paneJSON)},
	})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := fakecli.InstallWithOptions(t, dir, "cli", []fakecli.Rule{
		{Argv: []string{"explain"}, Stdout: teamExplainFixture},
		{Argv: []string{"roster"}, Stdout: teamRosterFixture},
		{Argv: []string{"status"}, Stdout: "worker-1\tworking\t-\nworker-2\tidle\t-\n"},
		{Argv: []string{"doctor"}, Stdout: "doctor-ok\n"},
		{Argv: []string{"friction", "--summary"}, Stdout: "friction-ok\n"},
		{Argv: []string{"gc"}, Stdout: "gc dry-run ok\npressure 12%\n"},
		{Argv: []string{"gc", "--yes"}, Stdout: "gc done\n"},
		{Argv: []string{"collect", "worker-1", "--lines", "60"}, Stdout: "report one line\n"},
		{Argv: []string{"collect", "worker-2", "--lines", "60"}, Stdout: "report two line\n"},
		{Argv: []string{"release", "worker-1", "--close"}, Stdout: "released one\n"},
		{Argv: []string{"release", "worker-2", "--close"}, Stdout: "released two\n"},
	}, fakecli.InstallOptions{CaptureEnv: []string{"HERDR_WORKSPACE_ID", "HERDR_PANE_ID", "HERDR_TAB_ID", "SOHO_NOWRITE", "PATH"}})
	if err != nil {
		t.Fatal(err)
	}
	env := teamEnv(t, dir, map[string]string{
		"HERDR_BIN_PATH":            herdr,
		"HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_id":"ws-a","tab_id":"tab-a","focused_pane_id":"pane-a"}`,
		"HERDR_WORKSPACE_ID":        "shell-ws",
		"HERDR_TAB_ID":              "shell-tab",
		"HERDR_PANE_ID":             "shell-pane",
	})
	return env, cli, cwd
}

func teamEnv(t *testing.T, dir string, values map[string]string) platform.Env {
	t.Helper()
	entries := fakecli.Env(testutil.CleanEnv(t), dir, fakecli.EnvOptions{IncludeBasePath: true})
	env := platform.Env{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	for key, value := range values {
		env[key] = value
	}
	return env
}

func teamStartWithPipes(t *testing.T, env platform.Env, cliPath string, args []string) (*os.File, *os.File, <-chan int) {
	t.Helper()
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "team-panel-")
	if err != nil {
		_ = input.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = input, output
	loopDone := make(chan struct{})
	t.Cleanup(func() {
		_ = writer.Close()
		select {
		case <-loopDone:
		case <-time.After(20 * time.Second):
			t.Error("team panel did not stop during cleanup")
			return // keep its descriptors and global stream references alive
		}
		os.Stdin, os.Stdout = oldIn, oldOut
		_ = input.Close()
		_ = output.Close()
	})
	finished := make(chan int, 1)
	go func() {
		defer close(loopDone)
		finished <- RunTeam(env, platform.Current(), cliPath, args)
	}()
	return writer, output, finished
}

func teamReadOutput(output *os.File) string {
	// A separate descriptor must read the capture: seeking the writer's
	// descriptor can overwrite a redraw in progress and erase its footer.
	data, _ := os.ReadFile(output.Name())
	return string(data)
}

func teamWaitForOutput(t *testing.T, output *os.File, want string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data := teamReadOutput(output)
		if strings.Contains(data, want) {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %q in the panel output:\n%s", want, data)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func teamWaitExit(t *testing.T, finished <-chan int, want int) {
	t.Helper()
	select {
	case code := <-finished:
		if code != want {
			t.Fatalf("panel exit=%d want %d", code, want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout waiting for the panel to exit")
	}
}

// teamLastFrame is the screen as of the last redraw (\x1b[H … \x1b[J).
func teamLastFrame(output string) string {
	idx := strings.LastIndex(output, "\x1b[H")
	if idx < 0 {
		return output
	}
	rest := output[idx+len("\x1b[H"):]
	if end := strings.Index(rest, "\x1b[J"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func teamLastFrameFirstLine(output string) string {
	frame := teamLastFrame(output)
	line, _, _ := strings.Cut(frame, "\x1b[K")
	return line
}

func teamCalls(t *testing.T, cliPath string) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(cliPath), "cli.json"))
	if err != nil {
		t.Fatalf("CLI call log: %v", err)
	}
	return calls
}

func teamCallIndex(calls []fakecli.Call, want []string) int {
	for i, call := range calls {
		if reflect.DeepEqual(call.Argv, want) {
			return i
		}
	}
	return -1
}

// teamWaitForLastFrame polls until the last frame satisfies cond (or 20 s).
func teamWaitForLastFrame(t *testing.T, output *os.File, cond func(frame string) bool, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data := teamReadOutput(output)
		start := strings.LastIndex(data, "\x1b[H")
		// A partial redraw may omit the confirmation footer simply because
		// it has not been written yet. Only a finished frame proves cancel.
		complete := start >= 0 && strings.Contains(data[start:], "\x1b[J")
		if complete && cond(teamLastFrame(data)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s:\n%s", why, teamLastFrame(teamReadOutput(output)))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// teamWaitForOccurrences waits until the marker appears at least n times in
// the cumulative output (20 s deadline). The output is never cleared, so a
// marker that was already drawn once is a real round-trip barrier only at
// its second (or later) occurrence.
func teamWaitForOccurrences(t *testing.T, output *os.File, marker string, n int, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if strings.Count(teamReadOutput(output), marker) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s (marker %q x%d)", why, marker, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestTeamPanelShowsFirstLineAndTeamView: a valid context renders the
// first line, the explain text and the team's workers - the `# other live
// agents` section stays out of the view.
func TestTeamPanelShowsFirstLineAndTeamView(t *testing.T) {
	env, cli, cwd := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	teamWaitForOutput(t, output, "módulo de relatórios")
	firstLine := teamLastFrameFirstLine(teamReadOutput(output))
	// Non-TTY panels render at 80 columns; the long test cwd makes the
	// first line the displaySlice of the full line.
	if want := displaySlice("soho · ws-a · "+cwd, 80); firstLine != want {
		t.Fatalf("first line=%q want %q", firstLine, want)
	}
	data := teamReadOutput(output)
	if strings.Contains(data, "other-1") {
		t.Fatalf("the # other live agents section leaked into the team view:\n%s", data)
	}
	_, _ = writer.Write([]byte("\x1b"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
}

// TestTeamPanelRejectsWorkspaceDivergence: the focused pane belongs to
// another workspace; the panel shows the cause on one line, closes with
// Esc, and never invokes the CLI.
func TestTeamPanelRejectsWorkspaceDivergence(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	paneJSON, _ := json.Marshal(map[string]any{"result": map[string]any{"pane": map[string]any{"workspace_id": "ws-b", "cwd": cwd}}})
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{{Argv: []string{"pane", "get", "pane-a"}, Stdout: string(paneJSON)}})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := fakecli.Install(t, dir, "cli", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := teamEnv(t, dir, map[string]string{
		"HERDR_BIN_PATH":            herdr,
		"HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_id":"ws-a","tab_id":"tab-a","focused_pane_id":"pane-a"}`,
	})
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	// The cause is one line, hard-cut to the panel width: assert the prefix
	// and that the last frame is a single content line.
	teamWaitForOutput(t, output, "workspace divergence: the context points to 'ws-a'")
	frame := teamLastFrame(teamReadOutput(output))
	if !strings.Contains(frame, "workspace divergence") {
		t.Fatalf("the cause is not on the screen:\n%s", frame)
	}
	if lines := strings.Split(strings.TrimSuffix(frame, "\x1b[J"), "\n"); len(lines) > 2 {
		t.Fatalf("the cause must be a single line:\n%s", frame)
	}
	_, _ = writer.Write([]byte("\x1b"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
	if _, statErr := os.Stat(filepath.Join(dir, "cli.calls.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("CLI invoked despite the rejected target: %v", statErr)
	}
}

// TestTeamPanelRejectsMissingCwd: the pane's cwd does not exist; the panel
// shows the cause and never invokes the CLI.
func TestTeamPanelRejectsMissingCwd(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing-dir")
	paneJSON, _ := json.Marshal(map[string]any{"result": map[string]any{"pane": map[string]any{"workspace_id": "ws-a", "cwd": missing}}})
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{{Argv: []string{"pane", "get", "pane-a"}, Stdout: string(paneJSON)}})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := fakecli.Install(t, dir, "cli", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := teamEnv(t, dir, map[string]string{
		"HERDR_BIN_PATH":            herdr,
		"HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_id":"ws-a","tab_id":"tab-a","focused_pane_id":"pane-a"}`,
	})
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "pane cwd does not exist: "+missing[:40])
	_, _ = writer.Write([]byte("\x1b"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
	if _, statErr := os.Stat(filepath.Join(dir, "cli.calls.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("CLI invoked despite the missing cwd: %v", statErr)
	}
}

// TestTeamPanelReadsCarryNowriteCwdAndTargetIDs: every read call carries
// SOHO_NOWRITE=1, runs in the target's cwd, and uses the target's
// HERDR_* ids (not the invoking shell's).
func TestTeamPanelReadsCarryNowriteCwdAndTargetIDs(t *testing.T) {
	env, cli, cwd := teamPanelFixture(t)
	cwdMarker := filepath.Join(filepath.Dir(cli), "child-cwd.txt")
	env["HERDR_SOHO_PLUGIN_CWD_MARKER"] = cwdMarker
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	_, _ = writer.Write([]byte("2"))
	teamWaitForOutput(t, output, "doctor-ok")
	_, _ = writer.Write([]byte("3"))
	teamWaitForOutput(t, output, "pressure 12%")
	_, _ = writer.Write([]byte("4"))
	teamWaitForOutput(t, output, "friction-ok")
	_, _ = writer.Write([]byte("\x1b"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
	calls := teamCalls(t, cli)
	wantArgv := [][]string{{"explain"}, {"roster"}, {"status"}, {"doctor"}, {"gc"}, {"friction", "--summary"}}
	if len(calls) != len(wantArgv) {
		t.Fatalf("CLI calls=%#v want %d reads", calls, len(wantArgv))
	}
	for i, want := range wantArgv {
		if !reflect.DeepEqual(calls[i].Argv, want) {
			t.Fatalf("call %d argv=%#v want %#v", i, calls[i].Argv, want)
		}
		e := calls[i].Env
		if e["SOHO_NOWRITE"] != "1" || e["HERDR_WORKSPACE_ID"] != "ws-a" || e["HERDR_PANE_ID"] != "pane-a" || e["HERDR_TAB_ID"] != "tab-a" {
			t.Fatalf("read %d (%v) env=%#v (NOWRITE, target ids)", i, want, e)
		}
	}
	childCwd, err := os.ReadFile(cwdMarker)
	resolved, expectedErr := filepath.EvalSymlinks(cwd)
	actual, actualErr := filepath.EvalSymlinks(string(childCwd))
	if err != nil || expectedErr != nil || actualErr != nil || actual != resolved {
		t.Fatalf("child cwd=%q want %q read=%v expected=%v actual=%v", childCwd, resolved, err, expectedErr, actualErr)
	}
}

// TestTeamWorkersParseOnlyTheFirstTable: only the first roster table is
// used - the `# other live agents` section (after the blank line) stays out.
func TestTeamWorkersParseOnlyTheFirstTable(t *testing.T) {
	lines := strings.Split(strings.TrimSuffix(teamRosterFixture, "\n"), "\n")
	workers := teamWorkers(lines)
	want := []string{"worker-1  codex  working  implement", "worker-2  grok  idle  scout"}
	if !reflect.DeepEqual(workers, want) {
		t.Fatalf("workers=%#v want %#v", workers, want)
	}
	if got := teamAgent(workers[1]); got != "worker-2" {
		t.Fatalf("agent=%q want worker-2", got)
	}
	if got := teamWorkers(nil); got != nil {
		t.Fatalf("workers from empty roster=%#v want nil", got)
	}
}

// TestTeamPanelEnterCollectsSelectedWorker: Enter opens the collect
// subview for the selected worker (`collect <agent> --lines 60`, read).
func TestTeamPanelEnterCollectsSelectedWorker(t *testing.T) {
	env, cli, _ := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	_, _ = writer.Write([]byte("\x1b[B")) // down: select worker-2
	teamWaitForOutput(t, output, "> worker-2")
	_, _ = writer.Write([]byte("\r")) // Enter
	teamWaitForOutput(t, output, "report two line")
	calls := teamCalls(t, cli)
	i := teamCallIndex(calls, []string{"collect", "worker-2", "--lines", "60"})
	if i < 0 {
		t.Fatalf("no collect call for the selected worker: %#v", calls)
	}
	if got := calls[i].Env["SOHO_NOWRITE"]; got != "1" {
		t.Fatalf("collect env NOWRITE=%q want 1 (a read)", calls[i].Env)
	}
	_, _ = writer.Write([]byte("\x1b")) // Esc: back to the team view
	_, _ = writer.Write([]byte("\x1b"))
	_, _ = writer.Write([]byte("\x1b"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
}

// TestTeamPanelXCancelNeverCallsRelease: x opens the confirmation; n or
// Esc cancel it, and no release call is made.
func TestTeamPanelXCancelNeverCallsRelease(t *testing.T) {
	env, cli, cwd := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	_, _ = writer.Write([]byte("x"))
	teamWaitForOutput(t, output, "release --close worker-1 in ws-a ("+cwd[:40])
	_, _ = writer.Write([]byte("n")) // cancel with n
	teamWaitForLastFrame(t, output, func(frame string) bool { return !strings.Contains(frame, "y executa") }, "the confirmation to disappear after n")
	_, _ = writer.Write([]byte("x"))
	teamWaitForOutput(t, output, "release --close worker-1 in ws-a ("+cwd[:40])
	_, _ = writer.Write([]byte("\x1b")) // Esc pending
	_, _ = writer.Write([]byte("q"))    // resolved by the next key: Esc cancels
	teamWaitForLastFrame(t, output, func(frame string) bool { return !strings.Contains(frame, "y executa") }, "the confirmation to disappear after Esc")
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
	for _, call := range teamCalls(t, cli) {
		if call.Argv[0] == "release" {
			t.Fatalf("release invoked without confirmation: %#v", call)
		}
	}
}

// TestTeamPanelXConfirmsReleaseWithoutNowriteAndReloads: y runs
// `release <agent> --close` WITHOUT the NOWRITE gate — the SOHO_NOWRITE
// inherited from the environment is stripped from the write call — the view
// shows the output and reloads (explain and roster run again after the
// release).
func TestTeamPanelXConfirmsReleaseWithoutNowriteAndReloads(t *testing.T) {
	env, cli, cwd := teamPanelFixture(t)
	env["SOHO_NOWRITE"] = "1" // the gate must not survive into a write
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	_, _ = writer.Write([]byte("\x1b[B")) // down: select worker-2
	teamWaitForOutput(t, output, "> worker-2")
	_, _ = writer.Write([]byte("x"))
	teamWaitForOutput(t, output, "release --close worker-2 in ws-a ("+cwd[:40])
	time.Sleep(teamConfirmQuiet + 100*time.Millisecond) // the confirmation's quiet window
	_, _ = writer.Write([]byte("y"))
	teamWaitForOutput(t, output, "released two") // the release output block
	calls := teamCalls(t, cli)
	release := teamCallIndex(calls, []string{"release", "worker-2", "--close"})
	if release < 0 {
		t.Fatalf("no release call: %#v", calls)
	}
	if got := calls[release].Env["SOHO_NOWRITE"]; got != "" {
		t.Fatalf("release env NOWRITE=%q want absent (a write)", calls[release].Env)
	}
	for _, want := range [][]string{{"explain"}, {"roster"}} {
		if i := teamCallIndexAfter(calls, release, want); i < 0 {
			t.Fatalf("no %v after the release (the view must reload): %#v", want, calls)
		}
	}
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
}

func teamCallIndexAfter(calls []fakecli.Call, after int, want []string) int {
	for i := after + 1; i < len(calls); i++ {
		if reflect.DeepEqual(calls[i].Argv, want) {
			return i
		}
	}
	return -1
}

// TestTeamPanelGConfirmsGCYes: g opens the confirmation; n cancels; y runs
// `gc --yes` WITHOUT SOHO_NOWRITE, the view shows the output and
// reloads the dry-run.
func TestTeamPanelGConfirmsGCYes(t *testing.T) {
	env, cli, cwd := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	_, _ = writer.Write([]byte("3"))
	teamWaitForOutput(t, output, "pressure 12%")
	_, _ = writer.Write([]byte("g"))
	teamWaitForOutput(t, output, "gc --yes in "+cwd[:40])
	_, _ = writer.Write([]byte("n"))
	teamWaitForLastFrame(t, output, func(frame string) bool { return !strings.Contains(frame, "y executa") }, "the confirmation to disappear after n")
	_, _ = writer.Write([]byte("g"))
	// The prompt marker is cumulative: its second occurrence proves the new
	// confirmation is on screen before the separate y block is written.
	teamWaitForOccurrences(t, output, "gc --yes in "+cwd[:40], 2, "the second gc confirmation prompt")
	time.Sleep(teamConfirmQuiet + 100*time.Millisecond) // the confirmation's quiet window
	_, _ = writer.Write([]byte("y"))
	teamWaitForOutput(t, output, "gc done")
	calls := teamCalls(t, cli)
	gcYes := teamCallIndex(calls, []string{"gc", "--yes"})
	if gcYes < 0 {
		t.Fatalf("no gc --yes call: %#v", calls)
	}
	if got := calls[gcYes].Env["SOHO_NOWRITE"]; got != "" {
		t.Fatalf("gc --yes env NOWRITE=%q want absent (a write)", calls[gcYes].Env)
	}
	if i := teamCallIndexAfter(calls, gcYes, []string{"gc"}); i < 0 {
		t.Fatalf("no dry-run gc after gc --yes (the view must reload): %#v", calls)
	}
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
}

// TestTeamPanelCLIFailureShowsExitAndStderr: a failed call shows its exit
// code and stderr in the view, and the panel keeps working.
func TestTeamPanelCLIFailureShowsExitAndStderr(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	paneJSON, _ := json.Marshal(map[string]any{"result": map[string]any{"pane": map[string]any{"workspace_id": "ws-a", "cwd": cwd}}})
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{{Argv: []string{"pane", "get", "pane-a"}, Stdout: string(paneJSON)}})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := fakecli.InstallWithOptions(t, dir, "cli", []fakecli.Rule{
		{Argv: []string{"explain"}, Stdout: teamExplainFixture},
		{Argv: []string{"roster"}, Stdout: teamRosterFixture},
		{Argv: []string{"doctor"}, Stderr: "boom\n", Code: 3},
	}, fakecli.InstallOptions{CaptureEnv: []string{"SOHO_NOWRITE"}})
	if err != nil {
		t.Fatal(err)
	}
	env := teamEnv(t, dir, map[string]string{
		"HERDR_BIN_PATH":            herdr,
		"HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_id":"ws-a","tab_id":"tab-a","focused_pane_id":"pane-a"}`,
	})
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1") // the panel survived the load
	_, _ = writer.Write([]byte("2"))
	teamWaitForOutput(t, output, "doctor failed (exit 3)")
	teamWaitForOutput(t, output, "boom")
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
}

// TestTeamPanelInitialViewDoctor: --view doctor starts on the doctor view
// (the first CLI call is doctor) and marks it in the view bar.
func TestTeamPanelInitialViewDoctor(t *testing.T) {
	env, cli, _ := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, []string{"--view", "doctor"})
	teamWaitForOutput(t, output, "doctor-ok")
	teamWaitForOutput(t, output, ">2 doctor")
	calls := teamCalls(t, cli)
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, []string{"doctor"}) {
		t.Fatalf("initial view calls=%#v want only the doctor read", calls)
	}
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
}

// TestTeamInitialViewParsesFlags: no flag means the equipe view, --view
// accepts only "doctor", and anything else is rejected.
func TestTeamInitialViewParsesFlags(t *testing.T) {
	if view, err := teamInitialView(nil); view != teamViewTeam || err != nil {
		t.Fatalf("default view=%d err=%v", view, err)
	}
	if view, err := teamInitialView([]string{"--view", "doctor"}); view != teamViewDoctor || err != nil {
		t.Fatalf("doctor view=%d err=%v", view, err)
	}
	for _, args := range [][]string{{"--view", "team"}, {"--bogus"}, {"--view"}} {
		if _, err := teamInitialView(args); err == nil {
			t.Fatalf("args %v accepted", args)
		}
	}
}

// TestTeamRenderNeverPassesWidth: with wide and mixed content, every
// rendered line (any view, subview or confirmation) fits the width in
// display columns.
func TestTeamRenderNeverPassesWidth(t *testing.T) {
	state := NewTeamState(teamViewTeam)
	state.target = teamTarget{WorkspaceID: "ws-a", PaneID: "p", TabID: "t", Cwd: "/a/very/long/cwd/path/for/the/team/panel/target"}
	state.viewLoaded = [4]bool{true, true, true, true}
	state.explainLines = []string{"A equipe " + strings.Repeat("界", 40) + " está trabalhando " + strings.Repeat("x", 80)}
	state.workers = []string{strings.Repeat("worker-name ", 6) + "codex working " + strings.Repeat("\U0001F600", 30)}
	state.selected = 1
	state.doctorLines = []string{strings.Repeat("doctor line ", 10)}
	state.gcLines = []string{"pressure 12%", strings.Repeat("gc ", 40)}
	state.frictionLines = []string{strings.Repeat("friction ", 40)}
	state.releaseAgent = "worker-1"
	state.releaseOut = []string{strings.Repeat("released ", 20)}
	state.gcYesOut = []string{strings.Repeat("gc done ", 20)}
	check := func(subview string, collectAgent string) {
		state.subview = subview
		state.confirmAgent = collectAgent
		state.collectAgent = collectAgent
		state.collectLoaded = true
		state.collectLines = []string{strings.Repeat("report ", 40) + strings.Repeat("界", 20)}
		for _, width := range []int{20, 30, 80} {
			render := RenderTeam(state, width)
			for i, line := range strings.Split(strings.TrimSuffix(render, "\n"), "\n") {
				if w := displayWidth(line); w > width {
					t.Fatalf("view %d subview %q width %d: line %d %q has display width %d", state.view, subview, width, i, line, w)
				}
			}
		}
	}
	for view := teamViewTeam; view <= teamViewFriction; view++ {
		state.view = view
		check("", "worker-1")
	}
	check("collect", "worker-1")
	check("confirm-release", "worker-1")
	check("confirm-gc", "")
}

// TestTeamBracketedPasteNeverExecutesWrites: the paste markers delimit
// content that is discarded as a whole - a pasted x/y in the equipe view
// and a pasted g/y in the recursos view never invoke release or gc --yes.
func TestTeamBracketedPasteNeverExecutesWrites(t *testing.T) {
	env, cli, _ := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	// equipe: one paste containing x and y.
	_, _ = writer.Write([]byte("\x1b[200~xy\x1b[201~"))
	// recursos: one paste containing g and y.
	_, _ = writer.Write([]byte("3"))
	teamWaitForOutput(t, output, "pressure 12%")
	_, _ = writer.Write([]byte("\x1b[200~gy\x1b[201~"))
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
	data := teamReadOutput(output)
	if strings.Contains(data, "released one") || strings.Contains(data, "gc done") {
		t.Fatalf("bracketed paste content reached the panel output:\n%s", data)
	}
	for _, call := range teamCalls(t, cli) {
		if len(call.Argv) > 0 && call.Argv[0] == "release" {
			t.Fatalf("bracketed paste invoked a write argv=%#v NOWRITE=%q", call.Argv, call.Env["SOHO_NOWRITE"])
		}
		if reflect.DeepEqual(call.Argv, []string{"gc", "--yes"}) {
			t.Fatalf("bracketed paste invoked gc --yes: %#v", call)
		}
	}
}

// TestTeamBracketedPasteNeverBecomesAnAction: paste content is not an
// action in any view - a pasted view switch and a pasted q do nothing, and
// the panel stays open on the equipe view (its bar keeps >1 equipe).
func TestTeamBracketedPasteNeverBecomesAnAction(t *testing.T) {
	env, cli, _ := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	_, _ = writer.Write([]byte("\x1b[200~2q\x1b[201~"))
	// The panel is still open on the equipe view; close it normally.
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
	data := teamReadOutput(output)
	if strings.Contains(data, ">2 doctor") {
		t.Fatalf("a pasted view switch became an action:\n%s", data)
	}
	for _, call := range teamCalls(t, cli) {
		if reflect.DeepEqual(call.Argv, []string{"doctor"}) {
			t.Fatalf("a pasted key invoked the doctor read: %#v", call)
		}
	}
}

// TestTeamConfirmationYOnlyFromItsOwnBlock: a y in the same read block as
// the key that opened the confirmation (xy) opens it but does not execute;
// neither does a block with more than the y (yy, y\r). A y in its own
// block executes.
func TestTeamConfirmationYOnlyFromItsOwnBlock(t *testing.T) {
	env, cli, cwd := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	countReleases := func() int {
		got := 0
		for _, call := range teamCalls(t, cli) {
			if len(call.Argv) > 0 && call.Argv[0] == "release" {
				got++
			}
		}
		return got
	}
	// One block with x and y: opens the confirmation, does not execute.
	_, _ = writer.Write([]byte("xy"))
	teamWaitForOutput(t, output, "release --close worker-1 in ws-a ("+cwd[:40])
	if got := countReleases(); got != 0 {
		t.Fatalf("the xy block executed the release: %d calls", got)
	}
	// A y in its own block executes the release.
	time.Sleep(teamConfirmQuiet + 100*time.Millisecond) // the confirmation's quiet window
	_, _ = writer.Write([]byte("y"))
	teamWaitForOutput(t, output, "released one")
	if got := countReleases(); got != 1 {
		t.Fatalf("release calls=%d want 1 (only the separate y)", got)
	}
	// yy in one block does not execute.
	_, _ = writer.Write([]byte("x"))
	teamWaitForOccurrences(t, output, "release --close worker-1 in ws-a ("+cwd[:40], 2, "the second release confirmation prompt")
	_, _ = writer.Write([]byte("yy"))
	_, _ = writer.Write([]byte("n")) // cancel
	teamWaitForLastFrame(t, output, func(frame string) bool { return !strings.Contains(frame, "y executa") }, "the confirmation to disappear after n")
	// y\r in one block does not execute.
	_, _ = writer.Write([]byte("x"))
	teamWaitForOccurrences(t, output, "release --close worker-1 in ws-a ("+cwd[:40], 3, "the third release confirmation prompt")
	_, _ = writer.Write([]byte("y\r"))
	_, _ = writer.Write([]byte("n")) // cancel
	teamWaitForLastFrame(t, output, func(frame string) bool { return !strings.Contains(frame, "y executa") }, "the confirmation to disappear after n")
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
	if got := countReleases(); got != 1 {
		t.Fatalf("release calls=%d want 1 (yy and y\\r must not confirm)", got)
	}
}

// TestTeamPanelEnablesAndDisablesBracketedPaste: the panel output starts
// with ESC[?2004h and the normal close ends with ESC[?2004l.
func TestTeamPanelEnablesAndDisablesBracketedPaste(t *testing.T) {
	env, cli, _ := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	_, _ = writer.Write([]byte("q"))
	_ = writer.Close()
	teamWaitExit(t, finished, 0)
	data := teamReadOutput(output)
	if !strings.HasPrefix(data, "\x1b[?2004h") {
		t.Fatalf("output does not start with the bracketed-paste enable: %q", data[:24])
	}
	if !strings.HasSuffix(data, "\x1b[?2004l") {
		t.Fatalf("output does not end with the bracketed-paste disable: %q", data[len(data)-24:])
	}
}

// TestTeamRenderWideRuneAtOneTwoThreeColumns: RenderTeam with CJK and
// emoji content at widths 1, 2 and 3 finishes (the wrap always consumes at
// least one character) and no rendered line passes the width.
func TestTeamRenderWideRuneAtOneTwoThreeColumns(t *testing.T) {
	state := NewTeamState(teamViewTeam)
	state.target = teamTarget{WorkspaceID: "ws-界", PaneID: "p", TabID: "t", Cwd: "/cwd/界/\U0001F600"}
	state.viewLoaded = [4]bool{true, true, true, true}
	state.explainLines = []string{"界", "\U0001F600", strings.Repeat("界", 10) + " fim " + strings.Repeat("\U0001F600", 5)}
	state.workers = []string{"界 codex working 界", "\U0001F600 idle task"}
	state.doctorLines = []string{"界 doctor 界"}
	state.gcLines = []string{"gc 界 --yes \U0001F600"}
	state.frictionLines = []string{"friction \U0001F600 界"}
	state.releaseAgent = "worker-界"
	state.releaseOut = []string{strings.Repeat("released 界 ", 10)}
	state.gcYesOut = []string{strings.Repeat("gc done \U0001F600 ", 10)}
	check := func(subview string) {
		state.subview = subview
		state.confirmAgent = "worker-界"
		state.collectAgent = "worker-界"
		state.collectLoaded = true
		state.collectLines = []string{strings.Repeat("report 界 ", 10)}
		for _, width := range []int{1, 2, 3} {
			done := make(chan string, 1)
			go func() {
				done <- RenderTeam(state, width)
			}()
			select {
			case render := <-done:
				for i, line := range strings.Split(strings.TrimSuffix(render, "\n"), "\n") {
					if w := displayWidth(line); w > width {
						t.Fatalf("view %d subview %q width %d: line %d %q has display width %d", state.view, subview, width, i, line, w)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("RenderTeam view %d subview %q width did not finish (the wrap loop stalled)", state.view, subview)
			}
		}
	}
	for view := teamViewTeam; view <= teamViewFriction; view++ {
		state.view = view
		check("")
	}
	state.view = teamViewTeam
	check("confirm-release")
	state.view = teamViewResources
	check("confirm-gc")
	check("collect")
}

// TestTeamEOFInConfirmationAndCollectCloses: end of input closes the panel
// for good - from the confirmation subview and from the collect subview -
// and ends the children like a normal close.
func TestTeamEOFInConfirmationAndCollectCloses(t *testing.T) {
	env, cli, _ := teamPanelFixture(t)
	writer, output, finished := teamStartWithPipes(t, env, cli, nil)
	teamWaitForOutput(t, output, "worker-1")
	_, _ = writer.Write([]byte("x"))
	teamWaitForOutput(t, output, "release --close worker-1 in ws-a (")
	_ = writer.Close() // EOF while the confirmation is on screen
	teamWaitExit(t, finished, 0)

	env2, cli2, _ := teamPanelFixture(t)
	writer2, output2, finished2 := teamStartWithPipes(t, env2, cli2, nil)
	teamWaitForOutput(t, output2, "worker-1")
	_, _ = writer2.Write([]byte("\r")) // Enter: collect worker-1
	teamWaitForOutput(t, output2, "report one line")
	_ = writer2.Close() // EOF inside the collect subview
	teamWaitExit(t, finished2, 0)
}

// A paste without bracketed paste that is longer than one read block reaches
// the panel as several blocks; its last block can be a lone `y`. Arriving
// inside the confirmation's quiet window, it never confirms (r89b).
func TestTeamUnbracketedPasteAcrossReadBlocksNeverConfirms(t *testing.T) {
	for _, mode := range []string{"release", "gc"} {
		t.Run(mode, func(t *testing.T) {
			env, cli, _ := teamPanelFixture(t)
			writer, output, finished := teamStartWithPipes(t, env, cli, nil)
			teamWaitForOutput(t, output, "worker-1")
			key, prompt := "x", "release --close worker-1"
			if mode == "gc" {
				_, _ = writer.Write([]byte("3"))
				teamWaitForOutput(t, output, "pressure")
				key, prompt = "g", "gc --yes in"
			}
			_, _ = writer.Write([]byte(strings.Repeat(key, 4096) + "y"))
			teamWaitForOutput(t, output, prompt)
			time.Sleep(teamConfirmQuiet + 200*time.Millisecond)
			_, _ = writer.Write([]byte("q"))
			_ = writer.Close()
			teamWaitExit(t, finished, 0)
			for _, call := range teamCalls(t, cli) {
				if call.Argv[0] == "release" || (len(call.Argv) == 2 && call.Argv[0] == "gc" && call.Argv[1] == "--yes") {
					t.Fatalf("a pasted payload split across reads ran %q", call.Argv)
				}
			}
		})
	}
}

// Zero-width marks before a rune that does not fit are consumed with it:
// the hard cut never splits a rune's bytes (r89b).
func TestTeamHardCutKeepsUTF8AfterCombiningMarks(t *testing.T) {
	s := NewTeamState(teamViewTeam)
	s.viewLoaded[0] = true
	for _, text := range []string{"̀界", "̀́界x", "à😀b"} {
		s.explainLines = []string{text}
		for _, width := range []int{1, 2, 3} {
			if out := RenderTeam(s, width); !utf8.ValidString(out) {
				t.Fatalf("RenderTeam(%q, %d) emitted invalid UTF-8: %q", text, width, out)
			}
		}
	}
}

// A paste into a confirmation that has already been on screen for longer
// than the quiet window still never confirms: its first block renews the
// window, so its lone `y` tail arrives inside it (r89c).
func TestTeamPasteIntoAnArmedConfirmationNeverConfirms(t *testing.T) {
	for _, mode := range []string{"release", "gc"} {
		t.Run(mode, func(t *testing.T) {
			env, cli, _ := teamPanelFixture(t)
			writer, output, finished := teamStartWithPipes(t, env, cli, nil)
			teamWaitForOutput(t, output, "worker-1")
			key, prompt := "x", "release --close worker-1"
			if mode == "gc" {
				_, _ = writer.Write([]byte("3"))
				teamWaitForOutput(t, output, "pressure")
				key, prompt = "g", "gc --yes in"
			}
			_, _ = writer.Write([]byte(key))
			teamWaitForOutput(t, output, prompt)
			time.Sleep(teamConfirmQuiet + 100*time.Millisecond)
			_, _ = writer.Write([]byte(strings.Repeat(key, 4096) + "y"))
			time.Sleep(teamConfirmQuiet + 200*time.Millisecond)
			_, _ = writer.Write([]byte("q"))
			_ = writer.Close()
			teamWaitExit(t, finished, 0)
			for _, call := range teamCalls(t, cli) {
				if call.Argv[0] == "release" || (len(call.Argv) == 2 && call.Argv[0] == "gc" && call.Argv[1] == "--yes") {
					t.Fatalf("a paste into an armed confirmation ran %q", call.Argv)
				}
			}
		})
	}
}

// Any block that cannot confirm restarts the window: `yy` and then a `y`
// right after it do not confirm; a `y` after a quiet window does.
func TestTeamQuietWindowRestartsAfterEveryOtherBlock(t *testing.T) {
	now := time.Unix(1000, 0)
	oldNow := teamNow
	teamNow = func() time.Time { return now }
	t.Cleanup(func() { teamNow = oldNow })
	s := NewTeamState(teamViewTeam)
	s.workers = []string{"worker-1 codex working task"}
	s.FeedChunk("x")
	now = now.Add(teamConfirmQuiet + time.Second)
	s.FeedChunk("yy")
	now = now.Add(time.Millisecond)
	if s.FeedChunk("y"); s.pendingLoad().doRelease {
		t.Fatal("a y right after yy confirmed")
	}
	now = now.Add(teamConfirmQuiet + time.Millisecond)
	if s.FeedChunk("y"); !s.pendingLoad().doRelease {
		t.Fatal("a y after a quiet window did not confirm")
	}
}

// A lone `y` that an unfinished escape sequence takes as its last byte does
// not confirm, and it restarts the quiet window like any block that did not
// confirm (r89d).
func TestTeamYTakenByAnEscapeSequenceRestartsTheWindow(t *testing.T) {
	now := time.Unix(2000, 0)
	oldNow := teamNow
	teamNow = func() time.Time { return now }
	t.Cleanup(func() { teamNow = oldNow })
	s := NewTeamState(teamViewTeam)
	s.workers = []string{"worker-1 codex working task"}
	s.FeedChunk("x")
	s.FeedChunk("\x1b[1")
	now = now.Add(teamConfirmQuiet + 100*time.Millisecond)
	s.FeedChunk("y")
	now = now.Add(time.Millisecond)
	if s.FeedChunk("y"); s.pendingLoad().doRelease {
		t.Fatal("a y 1 ms after a y taken by an escape sequence confirmed")
	}
	now = now.Add(teamConfirmQuiet + time.Millisecond)
	if s.FeedChunk("y"); !s.pendingLoad().doRelease {
		t.Fatal("a y after a quiet window did not confirm")
	}
}
