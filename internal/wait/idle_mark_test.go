package wait

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// idleMarkFixture is the DJA-565 wait fixture: a temp tree with the
// workspace state dir (roster + wait dir), a fake herdr on PATH (plus a
// fake hc when hc is true), a per-minute fake clock that advances with
// every `agent get worker` call, and idle_alert_minutes=1 so the idle
// alert fires on the second working poll without any real sleep.
type idleMarkFixture struct {
	root       string
	sd         string
	bin        string
	env        platform.Env
	ctx        *core.Config
	callFile   string
	hcCallFile string
	clockFile  string
	clockKey   string
}

func newIdleMarkFixture(t *testing.T, hc bool) *idleMarkFixture {
	t.Helper()
	root := t.TempDir()
	sd := filepath.Join(root, "state")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\tclaude\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &idleMarkFixture{root: root, sd: sd, bin: bin, callFile: filepath.Join(bin, "herdr.calls.jsonl"), hcCallFile: filepath.Join(bin, "hc.calls.jsonl"), clockFile: filepath.Join(bin, "herdr.calls.jsonl"), clockKey: "agent\x00get\x00worker"}
	if hc {
		f.clockFile, f.clockKey = f.hcCallFile, "get\x00worker\x00--json"
	}
	f.env = platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "GORACE": "atexit_sleep_ms=0", "SOHO_WAIT_POLL_MS": "1", "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "HERDR_SOCKET_PATH": filepath.Join(root, "none.sock")}
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{"idle_alert_minutes": {Value: "1"}, "settled_grace": {Value: "0"}, "stuck_warn_minutes": {Value: "0"}}}
	return f
}

// installHerdr installs the fake herdr with the get rules plus one rule
// per argv variant wait may read a screen with, all answering with the
// same constant screen.
func (f *idleMarkFixture) installHerdr(t *testing.T, gets ...fakecli.Rule) {
	t.Helper()
	rules := append([]fakecli.Rule{}, gets...)
	for _, argv := range [][]string{{"agent", "read", "worker", "--source", "visible"}, {"agent", "read", "worker", "--source", "visible", "--lines", "40"}, {"agent", "read", "worker", "--source", "visible", "--lines", "20"}, {"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}} {
		rules = append(rules, fakecli.Rule{Argv: argv, Stdout: "command output"})
	}
	if _, err := fakecli.Install(t, f.bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
}

// installHc installs the fake hc (get + visible read, each with the client's
// --json placement) and a sentinel fake herdr that must never be called.
func (f *idleMarkFixture) installHc(t *testing.T, gets ...fakecli.Rule) {
	t.Helper()
	rules := append([]fakecli.Rule{}, gets...)
	rules = append(rules, fakecli.Rule{Argv: []string{"read", "worker", "--lines", "0", "--json"}, Stdout: `{"text":"command output"}`})
	if _, err := fakecli.Install(t, f.bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, f.bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
}

// callsFor returns the joined argv of every call the fake binary recorded.
func callsFor(t *testing.T, file string) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range calls {
		out = append(out, strings.Join(c.Argv, "\x00"))
	}
	return out
}

// runWait runs WaitFor with the fake writers, the per-minute fake clock and
// a bounded real deadline (the fake clock settles the wait long before it).
func (f *idleMarkFixture) runWait(t *testing.T) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	oldNow, oldOut, oldErr, oldFriction := platform.Now, platform.Stdout, platform.Stderr, frictionLogFile
	friction := filepath.Join(f.root, "friction.log")
	platform.Stdout, platform.Stderr = &out, &stderr
	SetFrictionLogFile(friction)
	platform.Now = func() time.Time {
		n := 0
		for _, c := range callsFor(t, f.clockFile) {
			if c == f.clockKey {
				n++
			}
		}
		return time.Unix(1700000000, 0).Add(time.Duration(n) * time.Minute)
	}
	t.Cleanup(func() {
		platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
		SetFrictionLogFile(oldFriction)
	})
	code := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, float64((100*time.Minute)/time.Millisecond), false, f.root)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

// DJA-565 decision 1: the moment observe warns, wait writes the
// <agent>.idle-alert marker with the task, the minutes it warned and the
// unix seconds when it warned; the herdr hint wording stays byte-identical.
func TestWaitWritesIdleAlertMarker(t *testing.T) {
	f := newIdleMarkFixture(t, false)
	f.installHerdr(t,
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":2}}}`},
	)
	code, out, stderr := f.runWait(t)
	if code != 6 {
		t.Fatalf("code=%d out=%q stderr=%q, want settled-no-report 6", code, out, stderr)
	}
	wantWarning := "'worker' has been working for 1 min with no new output; read its pane (herdr agent read worker --source visible) — it may be stuck on a long command"
	if strings.Count(stderr, wantWarning) != 1 {
		t.Fatalf("stderr=%q, want exactly one herdr-worded alert", stderr)
	}
	// The task of a worker without a report or task pointer is the empty
	// report path: the marker carries it verbatim (task\tminutes\tunix).
	got, err := os.ReadFile(filepath.Join(f.sd, "wait", "worker.idle-alert"))
	if err != nil {
		t.Fatalf("marker missing after the alert: %v", err)
	}
	if string(got) != "\t1\t1700000120\n" {
		t.Fatalf("marker=%q, want %q (task \"\", 1 min, unix of the second poll)", string(got), "\t1\t1700000120\n")
	}
}

// DJA-565 decision 3: a marker for another task is dropped at the worker's
// next sample; a marker whose task matches the current task stays, byte for
// byte, when no alert rewrites it.
func TestWaitClearsIdleAlertMarkerOfAnotherTask(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		keep    bool
	}{
		{"another task is removed", "other-task\t20\t1700000000\n", false},
		{"matching task is kept", "\t20\t1700000000\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdleMarkFixture(t, false)
			if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.idle-alert"), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			// The agent is idle from the first probe: it settles without an
			// alert, so only the clear (or its absence) may touch the marker.
			f.installHerdr(t, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`})
			code, out, stderr := f.runWait(t)
			if code != 6 {
				t.Fatalf("code=%d out=%q stderr=%q, want settled-no-report 6", code, out, stderr)
			}
			path := filepath.Join(f.sd, "wait", "worker.idle-alert")
			if tc.keep {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != tc.content {
					t.Fatalf("marker=%q err=%v, want untouched %q", string(got), err, tc.content)
				}
			} else if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("marker still present (%v), want removed at the stale sample", err)
			}
		})
	}
}

// DJA-565 decisions 1 and 5 under transport=hc: the alert runs through the
// fake hc, the warning names `hc read`, and the marker is written the same
// way; the herdr binary is never called.
func TestWaitHcIdleAlertMarkerAndHint(t *testing.T) {
	f := newIdleMarkFixture(t, true)
	f.env["SOHO_TRANSPORT"] = "hc"
	f.installHc(t,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: `{"status":{"state":"working"},"turn":1}`},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: `{"status":{"state":"working"},"turn":1}`},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: `{"status":{"state":"idle"},"turn":2}`},
	)
	code, out, stderr := f.runWait(t)
	if code != 6 {
		t.Fatalf("code=%d out=%q stderr=%q, want settled-no-report 6", code, out, stderr)
	}
	wantWarning := "'worker' has been working for 1 min with no new output; read its terminal (hc read worker) — it may be stuck on a long command"
	if strings.Count(stderr, wantWarning) != 1 {
		t.Fatalf("stderr=%q, want exactly one hc-worded alert", stderr)
	}
	if strings.Contains(stderr, "herdr agent read") {
		t.Fatalf("stderr=%q, want no herdr wording under transport=hc", stderr)
	}
	got, err := os.ReadFile(filepath.Join(f.sd, "wait", "worker.idle-alert"))
	if err != nil {
		t.Fatalf("marker missing after the hc alert: %v", err)
	}
	if string(got) != "\t1\t1700000120\n" {
		t.Fatalf("marker=%q, want %q", string(got), "\t1\t1700000120\n")
	}
	if calls := callsFor(t, f.callFile); len(calls) != 0 {
		t.Fatalf("herdr calls=%#v, want none under transport=hc", calls)
	}
}

// idleMarkStatusFixture is the DJA-565 status fixture: the D11-style temp
// tree (roster + wait dir) with one worker, a fake herdr answering the get,
// the alive-in-other-pane list and the visible screen read, an optional
// pre-written idle-alert marker, and a run method that captures
// CmdStatus's stdout and stderr.
type idleMarkStatusFixture struct {
	cwd   string
	state string
	ctx   *core.Config
	env   platform.Env
	bin   string
}

// newIdleMarkStatusFixture builds the fixture with the default visible
// screen; newIdleMarkStatusFixtureWithScreen picks the screen the fake herdr
// answers the visible read with. state is the fake agent state and marker
// the pre-written idle-alert content ("" = no file).
func newIdleMarkStatusFixture(t *testing.T, row, state, marker string) *idleMarkStatusFixture {
	return newIdleMarkStatusFixtureWithScreen(t, row, state, marker, "Busy on the task 30%\n")
}

func newIdleMarkStatusFixtureWithScreen(t *testing.T, row, state, marker, screen string) *idleMarkStatusFixture {
	t.Helper()
	base := t.TempDir()
	cwd := filepath.Join(base, "cwd")
	stateDir := filepath.Join(base, "state")
	bin := filepath.Join(base, "bin")
	for _, dir := range []string{cwd, filepath.Join(stateDir, "ws", "wait"), bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + row
	if err := os.WriteFile(filepath.Join(stateDir, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(stateDir, "ws", "wait", "worker.idle-alert"), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + state + `"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	return &idleMarkStatusFixture{
		cwd:   cwd,
		state: stateDir,
		ctx:   &core.Config{Entries: map[string]core.ConfigEntry{}},
		env: platform.Env{
			"PATH":                bin,
			"HERDR_ENV":           "1",
			"SOHO_DIR":            stateDir,
			"HERDR_WORKSPACE_ID":  "ws",
			"HERDR_SOCKET_PATH":   filepath.Join(base, "none.sock"),
			"SOHO_FAKECLI_CONFIG": bin,
			"HOME":                base,
			"USERPROFILE":         base,
		},
		bin: bin,
	}
}

// run swaps the platform writers around the CmdStatus call and returns the
// exit code plus the captured stdout and stderr.
func (f *idleMarkStatusFixture) run(t *testing.T, agents []string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, err bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &err
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := CmdStatus(agents, f.ctx, f.env, f.cwd)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), err.String()
}

// callLog returns the ordered argv of every fake herdr call, joined with
// NUL.
func (f *idleMarkStatusFixture) callLog(t *testing.T) []string {
	t.Helper()
	return callsFor(t, filepath.Join(f.bin, "herdr.calls.jsonl"))
}

// DJA-565 decision 2: status shows the mark from the local marker. The mark
// is a suffix on the row's state field — a marker for another task, a
// non-working state, and a missing marker all keep the bare row.
func TestStatusShowsIdleAlertMark(t *testing.T) {
	row := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	for _, tc := range []struct {
		name    string
		state   string
		marker  string // "" = no marker file
		wantRow string
	}{
		{"matching marker shows the mark", "working", "\t47\t1700000000\n", "worker\tworking\t\tidle: no new output for 47 min\t-\t-\n"},
		{"another task shows nothing", "working", "other-task\t47\t1700000000\n", "worker\tworking\t\t-\t-\n"},
		{"no marker shows nothing", "working", "", "worker\tworking\t\t-\t-\n"},
		{"non-working state shows nothing", "idle", "\t47\t1700000000\n", "worker\tno-report-yet\t\t-\t-\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdleMarkStatusFixture(t, row, tc.state, tc.marker)
			code, out, err := f.run(t, []string{"worker"})
			if code != 0 || out != tc.wantRow {
				t.Fatalf("code=%d out=%q stderr=%q, want %q", code, out, err, tc.wantRow)
			}
		})
	}
}

// DJA-565 decision 2, the JSON form: the row status prints as JSON gets
// "idle_minutes" for the matching marker; the other cases keep today's
// shape. The working worker's JSON row is the lost-connection row — the
// one row of a working worker that status prints as JSON.
func TestStatusIdleAlertMarkJSON(t *testing.T) {
	row := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	for _, tc := range []struct {
		name    string
		marker  string
		wantKey bool
	}{
		{"matching marker adds idle_minutes", "\t47\t1700000000\n", true},
		{"another task keeps the shape", "other-task\t47\t1700000000\n", false},
		{"no marker keeps the shape", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdleMarkStatusFixtureWithScreen(t, row, "working", tc.marker, "server connection could not be restored\n")
			code, out, err := f.run(t, []string{"worker"})
			if code != 14 {
				t.Fatalf("code=%d out=%q stderr=%q, want 14", code, out, err)
			}
			var line struct {
				Status      string `json:"status"`
				IdleMinutes *int64 `json:"idle_minutes"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &line); err != nil {
				t.Fatalf("out=%q: %v", out, err)
			}
			if line.Status != "provider-error" {
				t.Fatalf("out=%q, want the provider-error row", out)
			}
			if tc.wantKey {
				if line.IdleMinutes == nil || *line.IdleMinutes != 47 {
					t.Fatalf("out=%q, want idle_minutes 47", out)
				}
			} else if line.IdleMinutes != nil {
				t.Fatalf("out=%q, want no idle_minutes key", out)
			}
		})
	}
}

// DJA-565 decision 4: the mark is a local file read — the herdr call
// sequence of a marked row is byte for byte the one of an unmarked row.
func TestStatusIdleAlertMarkMakesNoTransportCall(t *testing.T) {
	row := "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	marked := newIdleMarkStatusFixture(t, row, "working", "\t47\t1700000000\n")
	code, out, err := marked.run(t, []string{"worker"})
	if code != 0 || out != "worker\tworking\t\tidle: no new output for 47 min\t-\t-\n" {
		t.Fatalf("marked: code=%d out=%q stderr=%q", code, out, err)
	}
	logMarked := marked.callLog(t)
	plain := newIdleMarkStatusFixture(t, row, "working", "")
	code, out, err = plain.run(t, []string{"worker"})
	if code != 0 || out != "worker\tworking\t\t-\t-\n" {
		t.Fatalf("unmarked: code=%d out=%q stderr=%q", code, out, err)
	}
	logPlain := plain.callLog(t)
	if strings.Join(logMarked, " | ") != strings.Join(logPlain, " | ") {
		t.Fatalf("marked=%#v unmarked=%#v, want the identical herdr sequence", logMarked, logPlain)
	}
	t.Logf("identical herdr sequence with and without the marker: %q", strings.Join(logMarked, " | "))
}
