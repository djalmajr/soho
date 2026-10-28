package cli

// D25: a fresh brief typed while a worker is still working (or blocked) on
// an open task makes its CLI read the brief mid-turn and drop the open
// task. The dispatch refuses (exit 10) before typing anything or writing
// any task state; --amend and --queue keep today's behavior. The check
// rides the dispatch's pre-send agent get, so it runs with
// prompt_check_seconds > 0 (a 0 dispatch probes nothing, as today).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// busyStateJSON renders the agent get result the fake herdr returns.
func busyStateJSON(state string, seq int) string {
	return `{"result":{"agent":{"agent_status":"` + state + `","state_change_seq":` + strconv.Itoa(seq) + `}}}`
}

// busyDispatchFixture is a hermetic dispatch: a fake herdr on a PATH that
// holds only the fake, its own state dir, roles and briefs, and a pinned
// clock so the dispatch's stamp (20261003T152648) is deterministic. The
// clock advances on every read so every wait window stays bounded.
type busyDispatchFixture struct {
	root       string
	stateDir   string
	bin        string
	env        platform.Env
	brief      string
	openReport string // the open task's report (the pointer's current)
}

func newBusyDispatchFixture(t *testing.T) *busyDispatchFixture {
	t.Helper()
	root := t.TempDir()
	state, roles, bin := filepath.Join(root, "state"), filepath.Join(root, "roles"), filepath.Join(root, "bin")
	for _, dir := range []string{filepath.Join(state, "ws", "briefs"), filepath.Join(state, "ws", "reports"), filepath.Join(state, "ws", "wait"), roles, bin, filepath.Join(root, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nDispatch a prompt.\n# Expected result\n\nObserved.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\t\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 3, 15, 26, 48, 0, time.UTC)
	origNow := platform.Now
	platform.Now = func() time.Time {
		value := clock
		clock = clock.Add(500 * time.Millisecond)
		return value
	}
	t.Cleanup(func() { platform.Now = origNow })
	f := &busyDispatchFixture{
		root:       root,
		stateDir:   filepath.Join(state, "ws"),
		bin:        bin,
		brief:      brief,
		openReport: filepath.Join(state, "ws", "reports", "worker-20261003T100000.md"),
		env: isolatedUserEnv(platform.Env{
			"PATH":                       bin,
			"SOHO_FAKECLI_CONFIG":        bin,
			"HERDR_ENV":                  "1",
			"SOHO_DIR":                   state,
			"HERDR_WORKSPACE_ID":         "ws",
			"SOHO_ROLES":                 roles,
			"SOHO_SKILL_DIR":             testSkillDir(t),
			"SOHO_PROMPT_CHECK_SECONDS":  "1",
			"SOHO_PROMPT_SETTLE_SECONDS": "0",
			"SOHO_BRIEF_LINT":            "off",
			"SOHO_WAIT_POLL_MS":          "50",
			"TMPDIR":                     root,
			"HOME":                       filepath.Join(root, "home"),
			"HERDR_SOCKET_PATH":          filepath.Join(root, "missing.sock"),
		}),
	}
	// A race-instrumented fake child otherwise pauses for one second on
	// exit; the fixture sets only GORACE and leaves the rest of the env as built.
	f.env["GORACE"] = fakecli.RaceOptions(f.env.List())
	return f
}

// openPointer points the worker at the fixture's open report, which does
// not exist yet (or is the file the test controls).
func (f *busyDispatchFixture) openPointer(t *testing.T) {
	t.Helper()
	stable := filepath.Join(f.stateDir, "reports", "worker-20261003T100000.current.md")
	// json.Marshal escapes the Windows backslashes a concatenated string
	// would leave raw (invalid JSON there).
	raw, err := json.Marshal(map[string]any{"version": 1, "task_report": stable, "current": f.openReport, "history": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	pointer := string(raw)
	if err := os.WriteFile(taskreport.TaskReportPointerPath(f.stateDir, "worker"), []byte(pointer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *busyDispatchFixture) install(t *testing.T, rules []fakecli.Rule) {
	t.Helper()
	if _, err := fakecli.Install(t, f.bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
}

func (f *busyDispatchFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"dispatch"}, args...), f.env)
	}()
	return code, out.String(), stderr.String()
}

func (f *busyDispatchFixture) calls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func (f *busyDispatchFixture) composedPath() string {
	return filepath.Join(f.stateDir, "briefs", "worker-20261003T152648.md")
}

func (f *busyDispatchFixture) sidecarPath() string {
	return filepath.Join(f.stateDir, "briefs", "worker-20261003T152648.dispatch.json")
}

// snapshotTree maps every regular file under root to its sha256, for the
// before/after proof that the refusal writes no state.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return out
}

func TestDispatchBusyRefusesANewBrief(t *testing.T) {
	t.Run("working with an open task refuses with 10 and writes nothing", func(t *testing.T) {
		f := newBusyDispatchFixture(t)
		f.openPointer(t)
		before := snapshotTree(t, f.stateDir)
		f.install(t, []fakecli.Rule{
			{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("working", 1)},
			{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		})
		code, out, errText := f.run(t, "worker", f.brief)
		if code != 10 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errText)
		}
		want := "soho: dispatch: 'worker' is still working on its open task (no report at " + f.openReport + "); wait for it (soho wait worker), send an amendment (--amend), or pass --queue to type this brief now (its CLI may read it mid-turn and drop the open task), or pass --when-idle to wait for it and send then\n"
		if errText != want {
			t.Fatalf("stderr=%q; want %q", errText, want)
		}
		if out != "" {
			t.Fatalf("stdout must be empty on the refusal, got %q", out)
		}
		if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
			t.Fatalf("the refusal typed %d prompt(s); want none", n)
		}
		after := snapshotTree(t, f.stateDir)
		for rel, sum := range before {
			if after[rel] != sum {
				t.Fatalf("the refusal changed the state file %s", rel)
			}
		}
		for rel := range after {
			if _, ok := before[rel]; ok || rel == "friction.log" {
				continue
			}
			t.Fatalf("the refusal wrote a new state file %s", rel)
		}
		data, err := os.ReadFile(filepath.Join(f.stateDir, "friction.log"))
		if err != nil {
			t.Fatalf("the refusal did not record its friction line: %v", err)
		}
		if !strings.Contains(string(data), "still working on its open task") {
			t.Fatalf("friction.log missed the refusal: %s", data)
		}
	})

	t.Run("blocked with an open task refuses with the same message", func(t *testing.T) {
		f := newBusyDispatchFixture(t)
		f.openPointer(t)
		f.install(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("blocked", 1)},
			{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		})
		code, _, errText := f.run(t, "worker", f.brief)
		if code != 10 {
			t.Fatalf("code=%d err=%q", code, errText)
		}
		want := "soho: dispatch: 'worker' is still working on its open task (no report at " + f.openReport + "); wait for it (soho wait worker), send an amendment (--amend), or pass --queue to type this brief now (its CLI may read it mid-turn and drop the open task), or pass --when-idle to wait for it and send then\n"
		if errText != want {
			t.Fatalf("stderr=%q; want %q", errText, want)
		}
		if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
			t.Fatalf("the refusal typed %d prompt(s); want none", n)
		}
	})

	t.Run("working with an empty report refuses: the task is still open", func(t *testing.T) {
		f := newBusyDispatchFixture(t)
		if err := os.WriteFile(f.openReport, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		f.openPointer(t)
		f.install(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("working", 1)},
			{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		})
		code, out, errText := f.run(t, "worker", f.brief)
		if code != 10 || out != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errText)
		}
		if !strings.Contains(errText, "no report at "+f.openReport) {
			t.Fatalf("stderr=%q; want the open report path named", errText)
		}
		if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 0 {
			t.Fatalf("the refusal typed %d prompt(s); want none", n)
		}
	})
}

// queuedRules scripts the fake herdr for the wasWorking path: the target
// stays working on the same seq (so the prompt ends up queued) and the
// composed path is on the screen as the prompt evidence.
func (f *busyDispatchFixture) queuedRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("working", 1)},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "idle screen\n"},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "Read the file " + f.composedPath() + " in full\n"},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
	}
}

func TestDispatchBusyQueueKeepsTodayBehavior(t *testing.T) {
	for _, reportExists := range []bool{false, true} {
		t.Run("report_exists="+strconv.FormatBool(reportExists), func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			if reportExists {
				if err := os.WriteFile(f.openReport, []byte("# Draft report\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f.openPointer(t)
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
			f.install(t, f.queuedRules())
			code, out, errText := f.run(t, "worker", f.brief, "--queue", "--no-wait")
			if code != 0 {
				t.Fatalf("code=%d out=%q err=%q", code, out, errText)
			}
			if status := dispatchOutputStatus(t, out); status != "queued" {
				t.Fatalf("wait_status=%s out=%s", status, out)
			}
			want := "soho: prompt queued: 'worker' is working; it takes the prompt when its turn ends\n"
			if errText != want {
				t.Fatalf("stderr=%q; want %q", errText, want)
			}
			if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 1 {
				t.Fatalf("--queue typed %d prompt(s); want 1", n)
			}
			sidecar, err := os.ReadFile(f.sidecarPath())
			if err != nil || !strings.Contains(string(sidecar), `"arrival":"queued"`) {
				t.Fatalf("sidecar=%q err=%v; want the queued arrival", sidecar, err)
			}
			if _, err := os.Stat(filepath.Join(f.stateDir, "wait", "worker.queued")); err != nil {
				t.Fatalf("queued marker missing: %v", err)
			}
		})
	}
}

func TestDispatchBusyAmendKeepsTodayBehavior(t *testing.T) {
	for _, reportExists := range []bool{false, true} {
		t.Run("report_exists="+strconv.FormatBool(reportExists), func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			if reportExists {
				if err := os.WriteFile(f.openReport, []byte("# Draft report\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f.openPointer(t)
			if err := os.WriteFile(filepath.Join(f.stateDir, "last-report-worker"), []byte(f.openReport+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			amendBrief := filepath.Join(f.root, "amend.md")
			if err := os.WriteFile(amendBrief, []byte("# Amendment\n\nFix the report path.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
			f.install(t, f.queuedRules())
			code, out, errText := f.run(t, "worker", amendBrief, "--amend", "--no-wait")
			if code != 0 {
				t.Fatalf("code=%d out=%q err=%q", code, out, errText)
			}
			if status := dispatchOutputStatus(t, out); status != "queued" {
				t.Fatalf("wait_status=%s out=%s", status, out)
			}
			if !strings.Contains(out, `"amend":true`) {
				t.Fatalf("the amendment flag is missing: %s", out)
			}
			want := "soho: prompt queued: 'worker' is working; it takes the prompt when its turn ends\n"
			if errText != want {
				t.Fatalf("stderr=%q; want %q", errText, want)
			}
			if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 1 {
				t.Fatalf("--amend typed %d prompt(s); want 1", n)
			}
			// The amendment keeps the task's stable report and moves the pointer's
			// current to the new report.
			raw, err := os.ReadFile(taskreport.TaskReportPointerPath(f.stateDir, "worker"))
			if err != nil {
				t.Fatal(err)
			}
			value, perr := jsonjs.Parse(raw)
			if perr != nil {
				t.Fatalf("pointer %s is not JSON: %v", raw, perr)
			}
			pointer, ok := value.(*jsonjs.Object)
			if !ok {
				t.Fatalf("pointer %s is not an object", raw)
			}
			current, _ := pointer.Get("current")
			if current != filepath.Join(f.stateDir, "reports", "worker-20261003T152648.md") {
				t.Fatalf("pointer current=%v; want the amendment's report", current)
			}
			taskReport, _ := pointer.Get("task_report")
			if taskReport != filepath.Join(f.stateDir, "reports", "worker-20261003T100000.current.md") {
				t.Fatalf("pointer task_report=%v; want the task's stable report kept", taskReport)
			}
		})
	}
}

func TestDispatchBusyWithAReportStillRefuses(t *testing.T) {
	for _, state := range []string{"working", "blocked"} {
		t.Run(state, func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			if err := os.WriteFile(f.openReport, []byte("# Report\n\ndone\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			f.openPointer(t)
			f.install(t, []fakecli.Rule{
				{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
				{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON(state, 1)},
				{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
			})
			before := snapshotTree(t, f.stateDir)
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			if code != 10 || out != "" {
				t.Fatalf("code=%d out=%q err=%q; want refusal 10 with empty stdout", code, out, errText)
			}
			want := "soho: dispatch: 'worker' is still working although its report exists at " + f.openReport + " (it may be a draft); nothing was sent. Retry once its pane is idle (herdr agent get worker; after report_working_max, soho wait returns done without waiting for the pane), send an amendment (--amend), or pass --queue to type this brief now (its CLI may read it mid-turn), or pass --when-idle to wait for it and send then\n"
			if errText != want {
				t.Fatalf("stderr=%q; want %q", errText, want)
			}
			calls := f.calls(t)
			if n := countArgvPrefix(calls, []string{"agent", "get", "worker"}); n != 1 {
				t.Fatalf("pre-send state reads=%d; want 1", n)
			}
			for _, call := range calls {
				if len(call.Argv) < 2 || call.Argv[0] != "agent" || (call.Argv[1] != "get" && call.Argv[1] != "list") {
					t.Fatalf("refusal made a non-read Herdr call: %#v", call)
				}
			}
			after := snapshotTree(t, f.stateDir)
			delete(after, "friction.log")
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed task state: before=%v after=%v", before, after)
			}
			friction, err := os.ReadFile(filepath.Join(f.stateDir, "friction.log"))
			if err != nil || !strings.Contains(string(friction), strings.TrimPrefix(strings.TrimSpace(want), "soho: ")) {
				t.Fatalf("friction log=%q err=%v; want refusal recorded", friction, err)
			}
		})
	}
}

func TestDispatchBusyWithoutCurrentReportKeepsTodayBehavior(t *testing.T) {
	for _, pointer := range []string{"absent", "empty current"} {
		t.Run(pointer, func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			if pointer == "empty current" {
				f.openReport = ""
				f.openPointer(t)
			}
			f.install(t, f.queuedRules())
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			if code != 0 || dispatchOutputStatus(t, out) != "queued" {
				t.Fatalf("code=%d out=%q err=%q; want first dispatch queued", code, out, errText)
			}
			if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 1 {
				t.Fatalf("first dispatch typed %d prompt(s); want 1", n)
			}
		})
	}
}

func TestDispatchIdleWithAnOpenTaskKeepsTodayBehavior(t *testing.T) {
	f := newBusyDispatchFixture(t)
	f.openPointer(t)
	// prompt_check_seconds=0: the check makes no agent get, and the
	// dispatch types the brief as today.
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	f.install(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("idle", 1)},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errText)
	}
	if status := dispatchOutputStatus(t, out); status != "submitted" {
		t.Fatalf("wait_status=%s out=%s", status, out)
	}
	if errText != "" {
		t.Fatalf("stderr=%q; want none", errText)
	}
	if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("typed %d prompt(s); want 1", n)
	}
}

func TestDispatchIdleWithAReportKeepsTodayBehavior(t *testing.T) {
	f := newBusyDispatchFixture(t)
	f.openPointer(t)
	if err := os.WriteFile(f.openReport, []byte("# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := f.queuedRules()
	// Observe idle in the pre-send read, then a new working sequence after
	// submission, so the arrival proves the idle target accepted the brief.
	rules = append([]fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: busyStateJSON("idle", 1)}}, rules...)
	for i := 1; i < len(rules); i++ {
		if reflect.DeepEqual(rules[i].Argv, []string{"agent", "get", "worker"}) {
			rules[i].Stdout = busyStateJSON("working", 2)
		}
	}
	f.install(t, rules)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchOutputStatus(t, out) != "submitted" || errText != "" {
		t.Fatalf("code=%d out=%q err=%q; want idle target to receive the brief", code, out, errText)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"agent", "get", "worker"}); n < 2 {
		t.Fatalf("state reads=%d; want pre-send and arrival reads", n)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("idle dispatch typed %d prompt(s); want 1", n)
	}
}

func TestDispatchUnavailableAgentGetKeepsTodayBehavior(t *testing.T) {
	f := newBusyDispatchFixture(t)
	f.openPointer(t)
	f.install(t, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: "connection refused"},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "idle screen\n"},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	// The original submission still occurs, but unavailable state and an
	// unrecognized screen cannot authorize a repeated prompt.
	if code != 15 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errText)
	}
	if status := dispatchOutputStatus(t, out); status != "not-received" {
		t.Fatalf("wait_status=%s out=%s", status, out)
	}
	if strings.Contains(errText, "still working on its open task") {
		t.Fatalf("the refusal fired for an unavailable agent get: %q", errText)
	}
	if strings.Contains(errText, "sending it once more") || !strings.Contains(errText, "unconfirmed input region") {
		t.Fatalf("unavailable state must remain uncertain without resend: %q", errText)
	}
	if n := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); n != 1 {
		t.Fatalf("typed %d prompt(s); want only the original submission", n)
	}
}
