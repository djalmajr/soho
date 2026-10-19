package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// reopenLaneKindFixture is a reopen fixture whose worker (kind grok, role
// implementer, lane build) is idle with a report, and whose lane "build"
// holds the role "implementer" with lane.build.kind=laneKind.
func reopenLaneKindFixture(t *testing.T, laneKind string) *releaseFixture {
	t.Helper()
	f := newReopenFixture(t, "idle")
	report := filepath.Join(f.state, "reports", "worker.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("# done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(f.state, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setReopenRules(t, f)
	f.env["SOHO_LAYOUT"] = "split"
	f.env["SOHO_LANES"] = "on"
	f.env["SOHO_LANE_BUILD_ROLES"] = "implementer"
	if laneKind != "" {
		f.env["SOHO_LANE_BUILD_KIND"] = laneKind
	}
	return f
}

// runCommand runs one command through Run and returns what reopen's helper
// returns: code, output and the herdr calls.
func (f *releaseFixture) runCommand(t *testing.T, argv ...string) releaseRun {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(argv, f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	calls, err := fakecli.ReadCallsForConfig(f.configFile)
	if os.IsNotExist(err) {
		calls = []fakecli.Call{}
	} else if err != nil {
		t.Fatal(err)
	}
	return releaseRun{code, out.String(), stderr.String(), calls, f.state, f.configFile}
}

func (f *releaseFixture) rosterFile(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.state, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// DJA-397: a reopen of a worker whose kind differs from the lane's kind by
// now is refused before the release half closes the pane or drops the roster
// line, so no worker is lost.
func TestReopenRefusesAWorkerOfAnotherKindThanTheLaneBeforeReleasingIt(t *testing.T) {
	// Mutation captured: moving the check after the release half closes the
	// pane and drops the roster line before the spawn half refuses.
	f := reopenLaneKindFixture(t, "codex")
	before := f.rosterFile(t)
	run := f.reopen(t, "worker")
	if run.code != 13 {
		t.Fatalf("exit=%d, want 13\nstdout=%q\nstderr=%q", run.code, run.stdout, run.stderr)
	}
	wantJSON := `{"status":"kind-mismatch","lane":"build","role":"implementer","lane_kind":"codex","requested_kind":"grok"}` + "\n"
	if run.stdout != wantJSON {
		t.Fatalf("stdout=%q\nwant   %q", run.stdout, wantJSON)
	}
	text := "reopen: worker 'worker' runs grok, but lane 'build' is now codex; reopening would put a grok worker in it. Release it with 'soho release worker --close' and spawn the role again."
	if want := "soho: warning: " + text + "\n"; strings.Count(run.stderr, want) != 1 || !strings.HasSuffix(run.stderr, want) {
		t.Fatalf("stderr=%q\nwant it to end with %q once", run.stderr, want)
	}
	if log, _ := os.ReadFile(filepath.Join(f.state, "friction.log")); !strings.Contains(string(log), text) {
		t.Fatalf("friction.log lacks the refusal:\n%s", log)
	}
	for _, c := range run.calls {
		if len(c.Argv) >= 2 {
			switch c.Argv[0] + " " + c.Argv[1] {
			case "pane close", "pane split", "tab create", "agent start":
				t.Fatalf("herdr acted before the refusal: %v (all calls %#v)", c.Argv, run.calls)
			}
		}
	}
	if after := f.rosterFile(t); after != before {
		t.Fatalf("the roster changed\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// DJA-397: the controls that keep reopening: the worker's kind is the
// lane's, the lane has no kind, the lanes are off for the invocation.
func TestReopenKeepsWorkingWhenTheWorkerKindIsNotInConflictWithTheLane(t *testing.T) {
	for _, tc := range []struct {
		name, laneKind, lanes string
	}{
		{"the worker kind is the lane's", "grok", "on"},
		{"the lane has no kind", "", "on"},
		{"lanes off for the invocation", "codex", "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reopenLaneKindFixture(t, tc.laneKind)
			f.env["SOHO_LANES"] = tc.lanes
			run := f.reopen(t, "worker")
			if run.code != 0 || strings.Contains(run.stdout, "kind-mismatch") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
			}
			for _, want := range []string{`"name": "worker"`, `"kind": "grok"`, `"created_pane": true`} {
				if !strings.Contains(run.stdout, want) {
					t.Fatalf("spawn JSON missing %s: %s", want, run.stdout)
				}
			}
			closed := false
			for _, c := range run.calls {
				if len(c.Argv) >= 3 && c.Argv[0] == "pane" && c.Argv[1] == "close" && c.Argv[2] == "p-worker" {
					closed = true
				}
			}
			if !closed {
				t.Fatalf("the old pane was not closed: %#v", run.calls)
			}
		})
	}
}

// DJA-397: spawn and reopen use one helper, so for the same role, lane
// config and kind they refuse together or not at all, with the same JSON.
func TestSpawnAndReopenAgreeOnTheLaneKindConflict(t *testing.T) {
	for _, tc := range []struct {
		name, laneKind, lanes, kind string
		conflict                    bool
	}{
		{"a different kind", "codex", "on", "grok", true},
		{"the lane's kind", "grok", "on", "grok", false},
		{"a lane without kind", "", "on", "grok", false},
		{"lanes off", "codex", "off", "grok", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reopenLaneKindFixture(t, tc.laneKind)
			f.env["SOHO_LANES"] = tc.lanes
			lane, laneKind, got := spawn.LaneKindConflict("implementer", tc.kind, &core.Config{Entries: map[string]core.ConfigEntry{}}, f.env)
			if got != tc.conflict || (got && (lane != "build" || laneKind != tc.laneKind)) {
				t.Fatalf("LaneKindConflict = %q %q %v, want conflict=%v in lane build", lane, laneKind, got, tc.conflict)
			}
			s := f.runCommand(t, "spawn", "implementer", "--kind", tc.kind, "--fresh")
			g := reopenLaneKindFixture(t, tc.laneKind)
			g.env["SOHO_LANES"] = tc.lanes
			r := g.reopen(t, "worker")
			refused := func(run releaseRun) bool { return run.code == 13 && strings.Contains(run.stdout, `"lane_kind"`) }
			if refused(s) != tc.conflict || refused(r) != tc.conflict {
				t.Fatalf("conflict=%v, spawn refused=%v (exit %d, %q), reopen refused=%v (exit %d, %q)", tc.conflict, refused(s), s.code, s.stdout, refused(r), r.code, r.stdout)
			}
			if tc.conflict && s.stdout != r.stdout {
				t.Fatalf("different JSON\nspawn:  %q\nreopen: %q", s.stdout, r.stdout)
			}
		})
	}
}
