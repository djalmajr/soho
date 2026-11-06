package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA659 F1: a deterministic real-CmdSpawn regression for the window
// between the native lane rename and the roster publication. The selected
// source row ('old', pane p-old, started now) is replaced or removed while
// the fake native rename `agent rename p-old fresh` is still completing:
// the fakecli wait_file facility holds the rename in flight, the test
// performs the roster mutation through its own file operations on the
// test-owned synthetic state while the rename is recorded and not yet
// released, and only then releases the rename with the marker file. The
// later core publication must fail (exit 4, no readiness output),
// preserve the replacement incarnation unchanged, and publish no
// destination data row or history: the replacement or absence is not
// renamed, no readiness is claimed, and the native rename is neither
// reversed nor retried (exactly one rename call, no start). The old name
// is never a delivery alias: the fixtures are synthetic test-owned files,
// no tombstone or rename-history record is fabricated, and the test
// asserts the failure instead of resolving the old name.
//
// The two window cases are RED on the pre-fix code, which renames whatever
// row carries the source name at publication time — publishing the
// replacement's pane as the destination — or exits 0 claiming the rename
// when the row is gone, instead of comparing the selected incarnation.
// The two controls pass on the pre-fix code and must keep passing after
// the fix: the unchanged source publishes the rename with its pane
// retained, and a failed native rename leaves the roster untouched
// without a retry, a reversal or a new pane.
//
// Coverage scope: the spawn consumer across the real native rename and the
// roster publication, on the real CmdSpawn boundary. No race was executed:
// the window is opened deterministically by the wait_file hold, and the
// test asserts the rename call was recorded (and is the last call) before
// the mutation, so the rename is proven in flight when the source changes.

// dja659WindowRename is the exact native rename the both-full fixture
// takes: the selected source's pane to the requested name.
var dja659WindowRename = []string{"agent", "rename", "p-old", "fresh"}

// dja659RosterHeader is the exact header line spawnFixture.roster writes.
const dja659RosterHeader = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tburst\targs\teffort\n"

// dja659WindowFixture builds the both-full lane fixture (one slot, one
// live idle occupant, so the compatible rename path is taken on the
// pre-fix and post-fix code alike) with the given rename rule. The
// wait_file timeout is bounded so a rename that is never released fails
// the run quickly instead of hanging.
func dja659WindowFixture(t *testing.T, rename fakecli.Rule) spawnFixture {
	t.Helper()
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"old","pane_id":"p-old","agent_status":"idle"}]}}`},
		idleHerdrRule("old"),
		rename,
	}
	rules = append(rules, freshSpawnRules()...)
	f := a12Fixture(t, rules, "1")
	f.env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "20000"
	f.roster(t, a12Row("old", "grok", "grok-4.7", f.cwd))
	return f
}

// dja659SpawnOutcome runs CmdSpawn the way runCmdSpawn does — same stdout
// and stderr capture, same call-log read — and additionally keeps the
// recovered exit message, so the publication-failure case can pin the
// diagnostic's presence without pinning its wording.
func dja659SpawnOutcome(t *testing.T, f spawnFixture, args []string) (code int, message, stdout, stderr string, calls []fakecli.Call) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut strings.Builder
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					code, message = e.Code, e.Msg
					return
				}
				panic(value)
			}
		}()
		CmdSpawn(args, f.ctx, f.env, f.cwd)
	}()
	calls, _ = fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	return code, message, out.String(), errOut.String(), calls
}

// dja659WaitForRenameInFlight blocks until the fake logs the rename call.
// The fake records a call before it honors the rule's wait_file hold, so
// at that moment the rename is recorded, in flight and unable to have
// completed: the window is open. A torn or partial log line is retried.
func dja659WaitForRenameInFlight(t *testing.T, log string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		calls, err := fakecli.ReadCalls(log)
		if err == nil && len(calls) > 0 {
			last := calls[len(calls)-1]
			if dja659SameArgv(last.Argv, dja659WindowRename) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the native rename was never recorded in flight; the window never opened (calls=%#v err=%v)", calls, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func dja659SameArgv(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// dja659WindowOutcome is one real CmdSpawn run through the rename window.
type dja659WindowOutcome struct {
	code    int
	message string
	stdout  string
	stderr  string
	calls   []fakecli.Call
	roster  string
	// written is the replacement row the test wrote into the roster
	// ("" when the source row was removed instead).
	written   string
	stateKept bool
}

// dja659RunWindow runs the deterministic window: the real CmdSpawn runs in
// a goroutine; once the native rename is recorded in flight (held on the
// marker file) the test replaces the source row — or removes it when
// remove is set — in the test-owned roster, then releases the rename with
// the marker and joins the spawn. The mutation is strictly before the
// rename returns, so the publication strictly follows it.
func dja659RunWindow(t *testing.T, remove bool) dja659WindowOutcome {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "release")
	f := dja659WindowFixture(t, fakecli.Rule{Argv: dja659WindowRename, Stdout: `{"result":{}}`, WaitFile: marker})
	log := filepath.Join(f.bin, "herdr.calls.jsonl")
	stateDir := filepath.Join(f.state, "ws")
	stateMarker := filepath.Join(stateDir, "task-old")
	if err := os.WriteFile(stateMarker, []byte("synthetic source state"), 0o600); err != nil {
		t.Fatal(err)
	}
	spawned := make(chan dja659WindowOutcome, 1)
	joined := make(chan struct{})
	// Register before launching: even a failed stage/setup assertion releases
	// the owned fake native call and joins its spawn before fixture cleanup.
	t.Cleanup(func() {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			if err := os.WriteFile(marker, []byte("release"), 0o600); err != nil {
				t.Errorf("cleanup release: %v", err)
			}
		}
		<-joined
	})
	go func() {
		defer close(joined)
		code, message, stdout, stderr, calls := dja659SpawnOutcome(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
		spawned <- dja659WindowOutcome{code: code, message: message, stdout: stdout, stderr: stderr, calls: calls}
	}()
	dja659WaitForRenameInFlight(t, log)
	rosterPath := filepath.Join(f.state, "ws", "agents.tsv")
	written := ""
	if remove {
		if err := os.WriteFile(rosterPath, []byte(dja659RosterHeader), 0o600); err != nil {
			t.Fatalf("remove the source row in the window: %v", err)
		}
	} else {
		// A different incarnation under the same source name: a new pane
		// and a new started stamp, as a release and recreation would
		// leave.
		repl := strings.Split(a12Row("old", "grok", "grok-4.7", f.cwd), "\t")
		repl[1] = "p-repl"
		repl[7] = "later"
		written = strings.Join(repl, "\t")
		if err := os.WriteFile(rosterPath, []byte(dja659RosterHeader+written+"\n"), 0o600); err != nil {
			t.Fatalf("replace the source row in the window: %v", err)
		}
	}
	if err := os.WriteFile(marker, []byte("release"), 0o600); err != nil {
		t.Fatalf("release the rename with the marker: %v", err)
	}
	out := <-spawned
	<-joined
	b, err := os.ReadFile(rosterPath)
	if err != nil {
		t.Fatalf("read the roster after the window: %v", err)
	}
	out.roster = string(b)
	out.written = written
	state, stateErr := os.ReadFile(stateMarker)
	_, destinationErr := os.Stat(filepath.Join(stateDir, "task-fresh"))
	out.stateKept = stateErr == nil && string(state) == "synthetic source state" && os.IsNotExist(destinationErr)
	return out
}

func TestDJA659SourceIncarnationWindow(t *testing.T) {
	// The native-rename evidence is asserted before the discriminating
	// failure in both window cases: exactly one rename call and no start,
	// so the recorded failure is about the publication, not about the
	// native step, and no retry or reversal is ever made.
	assertWindowNative := func(t *testing.T, out dja659WindowOutcome) {
		t.Helper()
		if renamed := dja659AgentCalls(out.calls, "rename"); renamed != 1 {
			t.Fatalf("native renames=%d want exactly one (no retry, no reversal): %#v", renamed, out.calls)
		}
		if started := dja659AgentCalls(out.calls, "start"); started != 0 {
			t.Fatalf("native starts=%d want none (no new pane, no reversal): %#v", started, out.calls)
		}
	}
	assertWindowFailure := func(t *testing.T, out dja659WindowOutcome) {
		t.Helper()
		if !out.stateKept {
			t.Fatal("source-conflict publication moved or changed the per-name state marker")
		}
		if out.code != 4 {
			t.Fatalf("exit=%d want 4: the publication must fail when the selected source changed; stdout=%q stderr=%q message=%q", out.code, out.stdout, out.stderr, out.message)
		}
		if out.message == "" {
			t.Fatalf("the exit-4 diagnostic is empty; stdout=%q stderr=%q", out.stdout, out.stderr)
		}
		if out.stdout != "" {
			t.Fatalf("readiness output after the source changed: %q", out.stdout)
		}
		if strings.Contains(out.roster, "fresh\t") {
			t.Fatalf("a destination row was published: %q", out.roster)
		}
	}

	t.Run("a replaced source is not published as the destination", func(t *testing.T) {
		// RED on the pre-fix code. The behavior protected: the publication
		// is bound to the incarnation selected before the native rename,
		// so a row recreated under the source name while the rename is in
		// flight is not renamed, not claimed as ready and not published.
		// The believable regression that makes it fail: publishing by name
		// alone, which renames the replacement's pane as the destination
		// and exits 0 with a readiness JSON for a worker the native
		// operation never renamed. The native and state evidence is
		// captured before the discriminating failure.
		out := dja659RunWindow(t, false)
		assertWindowNative(t, out)
		if !strings.Contains(out.roster, out.written) {
			t.Fatalf("the replacement incarnation was changed: roster=%q want the row unchanged %q", out.roster, out.written)
		}
		assertWindowFailure(t, out)
	})

	t.Run("a removed source is not published as the destination", func(t *testing.T) {
		// RED on the pre-fix code. The behavior protected: a source row
		// gone before publication fails the spawn instead of letting it
		// exit 0 and claim the rename in the warning. The believable
		// regression: the pre-fix flow, which renames nothing, claims the
		// rename anyway and exits 0. The native and state evidence is
		// captured before the discriminating failure.
		out := dja659RunWindow(t, true)
		assertWindowNative(t, out)
		if strings.Contains(out.roster, "old\t") {
			t.Fatalf("the roster still carries the removed source row: %q", out.roster)
		}
		assertWindowFailure(t, out)
	})

	t.Run("an unchanged source publishes the rename", func(t *testing.T) {
		// Control: no mutation in the window (the rename rule completes as
		// soon as it runs), the source is still the selected incarnation,
		// and the publication succeeds with the pane retained. This must
		// keep passing after the fix.
		f := dja659WindowFixture(t, fakecli.Rule{Argv: dja659WindowRename, Stdout: `{"result":{}}`})
		code, _, stdout, stderr, calls := dja659SpawnOutcome(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if renamed, started := dja659AgentCalls(calls, "rename"), dja659AgentCalls(calls, "start"); renamed != 1 || started != 0 {
			t.Fatalf("native calls for the unchanged source: renamed=%d started=%d calls=%#v", renamed, started, calls)
		}
		if !strings.Contains(stdout, `"name": "fresh"`) || !strings.Contains(stdout, `"reused": true`) || !strings.Contains(stdout, `"pane_id": "p-old"`) || !strings.Contains(stdout, `"status": "ready"`) {
			t.Fatalf("JSON=%q want the rename published with the old pane retained", stdout)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(roster), "fresh\tp-old\tgrok\tworker") || strings.Contains(string(roster), "old\tp-old") {
			t.Fatalf("the roster rename did not keep the selected incarnation: %q", roster)
		}
		if !strings.Contains(stderr, "renamed to 'fresh'") {
			t.Fatalf("stderr=%q lost the established rename warning", stderr)
		}
	})

	t.Run("an unchanged CRLF source publishes the rename", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "release")
		f := dja659WindowFixture(t, fakecli.Rule{Argv: dja659WindowRename, Stdout: `{"result":{}}`, WaitFile: marker})
		stateDir := filepath.Join(f.state, "ws")
		rosterPath := filepath.Join(stateDir, "agents.tsv")
		// ReadTextFile supports CRLF delimiters, not arbitrary whitespace
		// trimming. Keep spaces and an empty trailing column as row data.
		selected := a12Row("old", "grok", "grok-4.7", f.cwd) + "\t\t\t future \t"
		starting := strings.ReplaceAll(dja659RosterHeader, "\n", "\r\n") + selected + "\r\n"
		if err := os.WriteFile(rosterPath, []byte(starting), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(rosterPath)
		if err != nil || string(before) != starting || strings.Count(string(before), "\r\n") != 2 {
			t.Fatalf("literal CRLF setup: roster=%q err=%v", before, err)
		}
		if got := core.RosterLine(stateDir, "old"); got != selected {
			t.Fatalf("CRLF selection changed column data: got=%q want=%q", got, selected)
		}
		stateMarker := filepath.Join(stateDir, "task-old")
		if err := os.WriteFile(stateMarker, []byte("synthetic source state"), 0o600); err != nil {
			t.Fatal(err)
		}
		spawned := make(chan dja659WindowOutcome, 1)
		joined := make(chan struct{})
		t.Cleanup(func() {
			if _, err := os.Stat(marker); os.IsNotExist(err) {
				if err := os.WriteFile(marker, []byte("release"), 0o600); err != nil {
					t.Errorf("cleanup release: %v", err)
				}
			}
			<-joined
		})
		go func() {
			defer close(joined)
			code, message, stdout, stderr, calls := dja659SpawnOutcome(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
			spawned <- dja659WindowOutcome{code: code, message: message, stdout: stdout, stderr: stderr, calls: calls}
		}()
		dja659WaitForRenameInFlight(t, filepath.Join(f.bin, "herdr.calls.jsonl"))
		inFlight, err := os.ReadFile(rosterPath)
		if err != nil || string(inFlight) != starting {
			t.Fatalf("CRLF source changed before native completion: roster=%q want=%q err=%v", inFlight, starting, err)
		}
		select {
		case <-joined:
			t.Fatal("native rename completed before its release marker")
		default:
		}
		if err := os.WriteFile(marker, []byte("release"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := <-spawned
		<-joined
		assertWindowNative(t, out)
		roster, rosterErr := os.ReadFile(rosterPath)
		oldState, oldErr := os.ReadFile(stateMarker)
		newState, newErr := os.ReadFile(filepath.Join(stateDir, "task-fresh"))
		if out.code != 0 {
			t.Fatalf("unchanged literal CRLF source reached native completion and joined; exactly one rename/no start; exit=%d want 0; message=%q stdout=%q stderr=%q roster=%q rosterErr=%v oldState=%q oldErr=%v newState=%q newErr=%v", out.code, out.message, out.stdout, out.stderr, roster, rosterErr, oldState, oldErr, newState, newErr)
		}
		if rosterErr != nil {
			t.Fatal(rosterErr)
		}
		wantRow := "fresh\t" + strings.TrimPrefix(selected, "old\t")
		if got := core.RosterLine(stateDir, "fresh"); got != wantRow || core.RosterLine(stateDir, "old") != "" {
			t.Fatalf("published CRLF row/column identity: got=%q want=%q roster=%q", got, wantRow, roster)
		}
		wantHistory := `# soho-rename {"v":1,"from":"old","to":"fresh","pane":"p-old","started":"now"}`
		if strings.Count(string(roster), "# soho-rename ") != 1 || !strings.Contains(string(roster), wantHistory+"\n") || core.RosterRenameHint(stateDir, "old") != "fresh" {
			t.Fatalf("CRLF publication lost its exact history: %q", roster)
		}
		if !os.IsNotExist(oldErr) || newErr != nil || string(newState) != "synthetic source state" {
			t.Fatalf("CRLF publication did not move state: old=%q err=%v new=%q err=%v", oldState, oldErr, newState, newErr)
		}
		if !strings.Contains(out.stdout, `"name": "fresh"`) || !strings.Contains(out.stdout, `"reused": true`) || !strings.Contains(out.stdout, `"pane_id": "p-old"`) || !strings.Contains(out.stdout, `"status": "ready"`) || !strings.Contains(out.stderr, "renamed to 'fresh'") {
			t.Fatalf("CRLF publication readiness/warning: stdout=%q stderr=%q", out.stdout, out.stderr)
		}
	})

	t.Run("a failed native rename changes nothing and is not retried", func(t *testing.T) {
		// Control: the established native-rename failure behavior the fix
		// must preserve — exit 4 with the cause, the roster untouched, no
		// readiness output, exactly one rename attempt (no retry) and no
		// new pane or reversal.
		f := dja659WindowFixture(t, fakecli.Rule{
			Argv:   dja659WindowRename,
			Stderr: `{"error":{"code":"agent_name_taken","message":"an agent named fresh already exists"}}`,
			Code:   1,
		})
		code, message, stdout, _, calls := dja659SpawnOutcome(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--name", "fresh"})
		if code != 4 {
			t.Fatalf("exit=%d message=%q stdout=%q", code, message, stdout)
		}
		want := "spawn: could not rename worker 'old' to 'fresh': agent_name_taken: an agent named fresh already exists"
		if message != want {
			t.Fatalf("message=%q want %q", message, want)
		}
		if stdout != "" {
			t.Fatalf("readiness output after the failed rename: %q", stdout)
		}
		if renamed, started := dja659AgentCalls(calls, "rename"), dja659AgentCalls(calls, "start"); renamed != 1 || started != 0 {
			t.Fatalf("native calls after the failed rename: renamed=%d started=%d calls=%#v", renamed, started, calls)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(roster), "old\tp-old\tgrok\tworker") || strings.Contains(string(roster), "fresh\t") {
			t.Fatalf("the roster changed after the failed rename: %q", roster)
		}
	})
}
