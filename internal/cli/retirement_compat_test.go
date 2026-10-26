package cli

// Command and side-effect contracts retained after retirement of the historical output corpus.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// ---------- frozen golden shapes ----------

type retireStep struct {
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Err     string            `json:"err"`
	Out     string            `json:"out"`
	RC      int               `json:"rc"`
	Columns [][]string
	JSON    []map[string]any
}

// ---------- fixture ----------

type retirementFixture struct {
	t     *testing.T
	root  string
	repo  string
	state string
	ws    string
	tmp   string
	bin   string
	skill string
	env   platform.Env
}

// newRetirementFixture builds the POSIX parity fixture: temporary git repo,
// temporary HOME/XDG/SOHO_DIR/TMPDIR, HERDR_WORKSPACE_ID=ws, a fakecli
// herdr (the scenario rules plus the generic list/pane/tab answers) and a
// fake git so the CLI's git probes resolve the fixture repo exactly like the
// legacy fixture. SOHO_SKILL_DIR points at the repository's real skill
// dir, so the composed prompt uses the native role files (the legacy
// composed prose is superseded: it is compared structurally, not pinned).
func newRetirementFixture(t *testing.T, rules []fakecli.Rule, seed func(*retirementFixture)) *retirementFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("parity goldens are POSIX fixture values (slash-normalized roots, /work roster cwd); native runs are macOS/Linux")
	}
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	conf := filepath.Join(root, "conf")
	state := filepath.Join(root, "state")
	tmp := filepath.Join(root, "tmp")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{repo, home, conf, state, tmp, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitInit := exec.Command("git", "init", "-q", repo)
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	skill := testSkillDir(t)
	f := &retirementFixture{t: t, root: root, repo: repo, state: state, ws: filepath.Join(state, "ws"), tmp: tmp, bin: bin, skill: skill}

	rules = append([]fakecli.Rule{}, rules...)
	rules = append(rules, []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
		{Argv: []string{"pane", "report-metadata"}, ArgvPrefix: true, Stdout: ``},
	}...)
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	gitRules := []fakecli.Rule{
		{Argv: []string{"rev-parse", "--show-toplevel"}, Stdout: repo + "\n"},
		{Argv: []string{"rev-parse", "--git-dir"}, Stdout: repo + "/.git\n"},
		{Argv: []string{"rev-parse", "--git-common-dir"}, Stdout: repo + "/.git\n"},
		{Argv: []string{"worktree", "list", "--porcelain"}, Stdout: ""},
		{AnyArgs: true},
	}
	if _, err := fakecli.Install(t, bin, "git", gitRules); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(f)
	}

	env := envFrom(testutil.CleanEnv(t))
	for key := range env {
		if strings.HasPrefix(strings.ToUpper(key), "HERDR_") || strings.EqualFold(key, "PATH") {
			delete(env, key)
		}
	}
	env["HOME"] = home
	env["USERPROFILE"] = home
	env["XDG_CONFIG_HOME"] = conf
	env["TMPDIR"] = tmp
	env["SOHO_DIR"] = state
	env["SOHO_SKILL_DIR"] = skill
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["HERDR_ENV"] = "1"
	// The legacy scenario env: the arrival check and the regrid are off, the
	// wait polls fast.
	env["SOHO_REGRID"] = "off"
	env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	env["SOHO_WAIT_POLL_MS"] = "1"
	f.env = withFakeCLI(env, bin)
	return f
}

// ---------- normalization ----------

var (
	retireStampRE       = regexp.MustCompile(`\d{8}T\d{6}(?:-\d+)?`)
	retireFrictionTSRE  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z?`)
	retireApprovalsTSRE = regexp.MustCompile(`^\d{8}T\d{6}`)
)

// normTS mirrors parity-dispatch.test.mjs normTs.
func normTS(v string) string { return retireStampRE.ReplaceAllString(v, "<TS>") }

// normRoots replaces the fixture root and the skill dir, longest match
// first (normalizeRoots).
func (f *retirementFixture) norm(v string) string {
	executable, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	v = strings.ReplaceAll(v, executable, "<BIN>")
	if f.skill != "" {
		v = strings.ReplaceAll(v, f.skill, "<SKILL>")
	}
	v = strings.ReplaceAll(v, f.root, "<ROOT>")
	return normTS(v)
}

func normFrictionContent(v string) string {
	lines := strings.Split(v, "\n")
	for i, line := range lines {
		lines[i] = retireFrictionTSRE.ReplaceAllString(line, "TS")
	}
	return strings.Join(lines, "\n")
}

func normApprovalsContent(v string) string {
	lines := strings.Split(v, "\n")
	for i, line := range lines {
		lines[i] = retireApprovalsTSRE.ReplaceAllString(line, "TS")
	}
	return strings.Join(lines, "\n")
}

// ---------- running ----------

// run executes one frozen step through the real CLI: the fixture env with the
// step's env overrides (an empty value removes the variable, like the legacy
// env replacement).
func (f *retirementFixture) run(t *testing.T, step retireStep) (int, string, string) {
	t.Helper()
	env := f.env.Clone()
	for key, value := range step.Env {
		if value == "" {
			delete(env, key)
		} else {
			env[key] = value
		}
	}
	return runIn(t, step.Args, env, f.repo)
}

// herdrLogLines is the fake herdr call log — the native stand-in for the
// legacy sh fake's herdr.log (one joined-argv line per call), normalized.
func (f *retirementFixture) herdrLogLines(t *testing.T) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		return []string{}
	}
	lines := make([]string, 0, len(calls))
	for _, c := range calls {
		lines = append(lines, f.norm(strings.Join(c.Argv, " ")))
	}
	return lines
}

// stampOrder is the chronological order of two original names that normalize
// alike (parity-dispatch.test.mjs stampOrder): the stamp first, then the
// same-second suffix (none = 1).
func stampOrder(rel string) (string, int) {
	m := regexp.MustCompile(`(\d{8}T\d{6})(?:-(\d+))?`).FindStringSubmatch(rel)
	if m == nil {
		return "", 1
	}
	suffix := 1
	if m[2] != "" {
		suffix, _ = strconv.Atoi(m[2])
	}
	return m[1], suffix
}

// collectState mirrors parity-dispatch.test.mjs collectState: every file
// under <state>/ws and under $TMPDIR/soho, keys and contents
// normalized, .since files as EPOCH, approvals/friction timestamps to TS.
// Two files that normalize to the same key resolve to the newest by
// stampOrder.
func (f *retirementFixture) collectState(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	orig := map[string]string{}
	put := func(rel, content string) {
		rel = filepath.ToSlash(rel)
		key := normTS(f.norm(rel))
		if prev, ok := orig[key]; ok {
			pts, psuf := stampOrder(prev)
			cst, csuf := stampOrder(rel)
			if pts > cst || (pts == cst && psuf >= csuf) {
				return
			}
		}
		orig[key] = rel
		base := filepath.Base(rel)
		var val string
		switch {
		case strings.HasSuffix(base, ".since"):
			val = "EPOCH"
		case strings.HasSuffix(base, ".approvals.log"):
			val = normApprovalsContent(f.norm(content))
		case base == "friction.log":
			val = normFrictionContent(f.norm(content))
		default:
			val = f.norm(content)
		}
		out[key] = val
	}
	var walk func(dir, prefix string)
	walk = func(dir, prefix string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			abs := filepath.Join(dir, e.Name())
			rel := prefix + "/" + e.Name()
			if e.IsDir() {
				walk(abs, rel)
				continue
			}
			b, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			put(rel, string(b))
		}
	}
	walk(f.ws, "state/ws")
	walk(filepath.Join(f.tmp, "soho"), "tmp/soho")
	return out
}

// ---------- entry ----------

func TestRetirementEntryContract(t *testing.T) {
	for name, steps := range retirementInvocations["entry"] {
		t.Run(name, func(t *testing.T) {
			f := newRetirementFixture(t, nil, nil)
			for _, step := range steps {
				code, out, stderr := f.run(t, step)
				if code != step.RC || normalizeParityError(stderr) != step.Err {
					t.Fatalf("args=%q code=%d stderr=%q", step.Args, code, stderr)
				}
				if name == "help-variants" {
					if !strings.HasPrefix(out, "soho —") || !strings.Contains(out, "dispatch") {
						t.Fatalf("help missing command contract: %q", out)
					}
				} else if out != "" {
					t.Fatalf("unknown command stdout=%q", out)
				}
			}
		})
	}
}

// ---------- status ----------

type retireStepWant struct {
	RC  int
	Out string
	Err string
}

type retireStatusScenario struct {
	seed  func(*retirementFixture)
	rules []fakecli.Rule
	// per-step env overrides (the golden stores no env: the legacy withFakes
	// steps are transcribed here, empty value = variable removed).
	stepEnv []map[string]string
	// per-step overrides for the intentional Go/JS differences above.
	want map[int]retireStepWant
	// transform applied to the raw stdout before comparison (the legacy
	// roster-table <N> normalization).
	transform func(string) string
	// extra assertions after the steps (friction lines, no herdr calls).
	extra func(t *testing.T, f *retirementFixture)
}

func runRetirementStatusScenario(t *testing.T, invocations map[string][]retireStep, name string, sc retireStatusScenario) {
	t.Helper()
	f := newRetirementFixture(t, sc.rules, sc.seed)
	rosterPath := filepath.Join(f.ws, "agents.tsv")
	before, _ := os.ReadFile(rosterPath)
	for i, step := range invocations[name] {
		if i < len(sc.stepEnv) {
			for key, value := range sc.stepEnv[i] {
				step.Env = mergeEnv(step.Env, key, value)
			}
		}
		if w, ok := sc.want[i]; ok {
			step.RC, step.Err = w.RC, w.Err
			step.Columns = nil
			for _, row := range strings.Split(strings.TrimSuffix(w.Out, "\n"), "\n") {
				step.Columns = append(step.Columns, strings.Split(row, "\t"))
			}
		}
		code, out, stderr := f.run(t, step)
		if code != step.RC || f.norm(normalizeParityError(stderr)) != step.Err {
			t.Fatalf("step=%d args=%q code=%d want=%d stderr=%q want=%q", i, step.Args, code, step.RC, stderr, step.Err)
		}
		if len(step.Columns) > 0 {
			assertStatusColumns(t, f.norm(out), step.Columns)
		}
		if name == "status-quota" {
			var obj map[string]any
			if err := json.Unmarshal([]byte(out), &obj); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{"agent": "build", "status": "quota", "report": "", "lane": "build", "kind": "grok", "model": "grok-4.7", "renewal": "", "task_s": nil, "activity_s": nil} {
				if v, ok := obj[key]; !ok || !reflect.DeepEqual(v, want) {
					t.Fatalf("quota %s=%v (present=%t), want %v", key, v, ok, want)
				}
			}
			if obj["match"] != "Error: quota exceeded for this account" {
				t.Fatalf("quota match=%v", obj["match"])
			}
		}
		switch step.Args[0] {
		case "roster":
			if !strings.HasPrefix(out, "NAME") || !strings.Contains(out, "ROLE") || !strings.Contains(out, "CWD TASK") {
				t.Fatalf("roster header=%q", out)
			}
			if name == "roster-table" {
				for _, v := range []string{"build", "tasker (designer)", "old8", "/work2"} {
					if !strings.Contains(out, v) {
						t.Fatalf("roster misses %q: %q", v, out)
					}
				}
			}
		case "friction":
			if name == "friction-empty" && !strings.Contains(out, "no friction recorded under ") {
				t.Fatal(out)
			}
			if name == "friction-seeded" {
				for _, v := range []string{"timestamp, level, command, message", "warning\tstatus\told warning", "error(exit 4)\tstatus\told error"} {
					if !strings.Contains(out, v) {
						t.Fatalf("friction misses %q", v)
					}
				}
			}
		}
	}
	after, _ := os.ReadFile(rosterPath)
	if len(before) > 0 && string(before) != string(after) {
		t.Fatalf("status changed roster: before=%q after=%q", before, after)
	}
	if len(before) == 0 && len(after) > 0 && string(after) != retireR12 {
		t.Fatalf("status initialized an unexpected roster: %q", after)
	}
	if sc.extra != nil {
		sc.extra(t, f)
	}
}

const (
	retireR8  = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\n"
	retireR11 = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\n"
	retireR12 = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"
)

var (
	retireWorker8  = "worker\tp1\tgrok\timplementer\txai\t1\t/work\tnow\n"
	retireStuck8   = "stuck\tp2\tgrok\timplementer\txai\t1\t/work\tnow\n"
	retireDead8    = "dead\tp3\tgrok\timplementer\txai\t1\t/work\tnow\n"
	retireDeniedOS = `Error: Os { code: 13, kind: PermissionDenied, message: "Permission denied" }`
)

func retireWriteRoster(t *testing.T, f *retirementFixture, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.ws, "agents.tsv"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func retireAgentGetRule(target, agentStatus string) fakecli.Rule {
	return fakecli.Rule{
		Argv:   []string{"agent", "get", target},
		Stdout: `{"result":{"agent":{"name":"` + target + `","agent_status":"` + agentStatus + `"}}}`,
	}
}

func retireScreenRule(target, screen string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"agent", "read", target}, ArgvPrefix: true, Stdout: screen}
}

var retireRosterTableAgeRE = regexp.MustCompile(`\t(-|\d+)\t(-|\d+)\n`)

func TestRetirementStatusContract(t *testing.T) {
	// JS: parity-status.test.mjs (the status/roster/friction scenarios of
	// test-status.sh, plus the roster/table cases and the env error paths).
	invocations := retirementInvocations["status"]

	names := []string{
		"status-denied", "status-denied-multi", "status-missing", "status-down",
		"status-working", "status-idle", "status-blocked", "status-report-wins",
		"status-batch", "status-unknown", "status-quota", "roster-table",
		"roster-empty", "friction-empty", "friction-seeded", "status-env-errors",
		"status-denied-friction",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if name == "status-denied-friction" {
				runRetirementDeniedFriction(t)
				return
			}
			runRetirementStatusScenario(t, invocations, name, retireStatusCase(t, name))
		})
	}
}

func retireStatusCase(t *testing.T, name string) retireStatusScenario {
	t.Helper()
	switch name {
	case "status-denied", "status-denied-multi":
		cause := retireDeniedOS
		if name == "status-denied-multi" {
			cause = retireDeniedOS + "\nsecond-line\t\x1b[31mred"
		}
		return retireStatusScenario{
			rules: []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: cause + "\n"},
				retireScreenRule("worker", "terminal-fallback"),
			},
			seed: func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8) },
		}
	case "status-missing":
		// Intentional difference: Go classifies agent_not_found as `gone`
		// (status_wait_parity_test.go:171; usage "gone = agent_not_found").
		return retireStatusScenario{
			rules: []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"agent target worker not found"},"id":"cli:agent:get"}` + "\n"},
				retireScreenRule("worker", "terminal-fallback"),
			},
			seed: func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8) },
			want: map[int]retireStepWant{
				0: {RC: 0, Out: "worker\tgone\t\t-\t-\n", Err: ""},
			},
		}
	case "status-down":
		return retireStatusScenario{
			rules: []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"id":"cli:agent:get","error":{"code":"server_not_running","message":"no herdr server is running"}}` + "\n"},
				retireScreenRule("worker", "terminal-fallback"),
			},
			seed: func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8) },
		}
	case "status-working", "status-idle", "status-blocked":
		status := strings.TrimPrefix(name, "status-")
		if status == "idle" {
			status = "no-report-yet"
		}
		return retireStatusScenario{
			rules: []fakecli.Rule{
				retireAgentGetRule("worker", status),
				retireScreenRule("worker", "terminal-fallback"),
			},
			seed: func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8) },
		}
	case "status-report-wins":
		return retireStatusScenario{
			rules: []fakecli.Rule{
				// The denied mode would apply if herdr were queried at all;
				// the ready report must win first.
				{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: retireDeniedOS + "\n"},
				retireScreenRule("worker", "terminal-fallback"),
			},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR8+retireWorker8)
				report := filepath.Join(f.ws, "reports", "worker.md")
				if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(report, []byte("report body\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.ws, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				// Deterministic task_s: pin both mtimes to the same instant
				// (the legacy seeded them microseconds apart and stored 0).
				fixed := time.Unix(1767225600, 0)
				if err := os.Chtimes(report, fixed, fixed); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(filepath.Join(f.ws, "last-report-worker"), fixed, fixed); err != nil {
					t.Fatal(err)
				}
			},
			extra: func(t *testing.T, f *retirementFixture) {
				lines := f.herdrLogLines(t)
				if len(lines) != 0 {
					t.Fatalf("a ready report wins without any herdr call; got %d calls: %v", len(lines), lines)
				}
			},
		}
	case "status-batch":
		return retireStatusScenario{
			rules: []fakecli.Rule{
				{Argv: []string{"agent", "get", "stuck"}, Code: 1, Stderr: retireDeniedOS + "\n"},
				{Argv: []string{"agent", "get", "dead"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"agent target dead not found"},"id":"cli:agent:get"}` + "\n"},
				retireScreenRule("stuck", "terminal-fallback"),
				retireScreenRule("dead", "terminal-fallback"),
			},
			seed: func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8+retireStuck8+retireDead8) },
		}
	case "status-unknown":
		// Intentional difference: the D11 probe
		// (status_not_in_roster_test.go) reports a Herdr-known unknown agent
		// as `not-in-roster` with a hint; the legacy golden predates the
		// probe (unknown-agent, no stderr, no query).
		return retireStatusScenario{
			rules: []fakecli.Rule{
				retireAgentGetRule("nosuch", "working"),
				retireScreenRule("nosuch", "terminal-fallback"),
			},
			seed: func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8) },
			want: map[int]retireStepWant{
				0: {
					RC:  0,
					Out: "nosuch\tnot-in-roster\t\t-\t-\n",
					Err: "PROG: status: 'nosuch' is not a worker of this workspace's team; herdr agent get nosuch shows its state\n",
				},
			},
		}
	case "status-quota":
		return retireStatusScenario{
			rules: []fakecli.Rule{
				retireAgentGetRule("build", "idle"),
				{Argv: []string{"agent", "read", "build"}, ArgvPrefix: true, Stdout: "Error: quota exceeded for this account\n"},
			},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR12+"build\tp1\tgrok\timplementer\txai\t1\t/work\t20260101T000000\tgrok-4.7\tfull\timplementer\tbuild\n")
			},
			extra: func(t *testing.T, f *retirementFixture) {
				log, err := os.ReadFile(filepath.Join(f.ws, "friction.log"))
				if err != nil {
					t.Fatalf("friction log: %v", err)
				}
				line := normFrictionContent(f.norm(string(log)))
				want := "TS\twarning\tstatus\tquota: agent 'build' lane=build kind=grok model=grok-4.7 : Error: quota exceeded for this account\n"
				if line != want {
					t.Fatalf("friction=%q want %q", line, want)
				}
			},
		}
	case "roster-table":
		return retireStatusScenario{
			rules: []fakecli.Rule{
				retireAgentGetRule("task", "working"),
				retireScreenRule("task", "terminal-fallback"),
				{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"build","pane_id":"p1","agent_status":"working","agent":"grok"},{"name":"orchestrator","pane_id":"p0"},{"name":"old8","pane_id":"p2","agent_status":"idle","agent":"agy"},{"name":"stray","pane_id":"p9","agent_status":"blocked","agent":"claude"}]}}`},
				{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"p1","tab_id":"t1"},{"pane_id":"p2","tab_id":"t2"},{"pane_id":"p5","tab_id":"t1"}]}}`},
				{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[{"tab_id":"t1","label":"herd-build-123456789012"},{"tab_id":"t2"}]}}`},
			},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR12+
					"build\tp1\tgrok\timplementer\txai\t1\t/work\t20260101T000000\tgrok-4.7\tfull\ttasker,implementer\tbuild\n"+
					"task\tp5\tgrok\ttasker\txai\t1\t/work\t20260101T000000\tgrok-4.7\tfull\tdesigner\t\n"+
					"old8\tp2\tagy\tdesigner\tgoogle\t1\t/work2\tnow\n")
				for _, agent := range []string{"build", "task"} {
					report := filepath.Join(f.ws, "reports", agent+".md")
					if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
						t.Fatal(err)
					}
					body := "ready report\n"
					if agent == "task" {
						body = ""
					}
					if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(f.ws, "last-report-"+agent), []byte(report+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
			// The legacy transform: the last two TSV columns (task_s,
			// activity_s) normalize to <N> (wall-clock mtime differences).
			transform: func(out string) string {
				return retireRosterTableAgeRE.ReplaceAllString(out, "\t<N>\t<N>\n")
			},
		}
	case "roster-empty":
		return retireStatusScenario{
			seed: func(f *retirementFixture) { retireWriteRoster(t, f, retireR12) },
		}
	case "friction-empty":
		return retireStatusScenario{}
	case "friction-seeded":
		return retireStatusScenario{
			seed: func(f *retirementFixture) {
				if err := os.WriteFile(filepath.Join(f.ws, "friction.log"),
					[]byte("2026-01-02T03:04:05\twarning\tstatus\told warning\n2026-01-02T03:04:06\terror(exit 4)\tstatus\told error\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		}
	case "status-env-errors":
		// Step 0 (no names) is an intentional difference: Go covers the
		// whole roster in roster order, same as the named run
		// (status_cases_test.go "status: no names covers the whole roster in
		// roster order, same as the named run"); the legacy died 2.
		return retireStatusScenario{
			rules: []fakecli.Rule{
				retireAgentGetRule("worker", "working"),
				retireScreenRule("worker", "terminal-fallback"),
			},
			seed: func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8) },
			// The legacy step envs (parity-status.test.mjs): step 1 has no
			// HERDR_ENV, step 2 points PATH at a dir without herdr.
			stepEnv: []map[string]string{{}, {"HERDR_ENV": ""}, {"HERDR_ENV": "1", "PATH": "/usr/bin:/bin"}},
			want: map[int]retireStepWant{
				0: {RC: 0, Out: "worker\tworking\t\t-\t-\n", Err: ""},
			},
		}
	default:
		t.Fatalf("no case for scenario %q", name)
		return retireStatusScenario{}
	}
}

func runRetirementDeniedFriction(t *testing.T) {
	f := newRetirementFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: retireDeniedOS + "\n"}, retireScreenRule("worker", "terminal-fallback")}, func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8) })
	code, _, _ := f.run(t, retireStep{Args: []string{"status", "worker"}})
	if code != 4 {
		t.Fatalf("code=%d", code)
	}
	log, err := os.ReadFile(filepath.Join(f.ws, "friction.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(normFrictionContent(string(log))), "\n")
	if len(lines) != 1 {
		t.Fatalf("friction=%q", log)
	}
	fields := strings.SplitN(lines[0], "\t", 4)
	if len(fields) != 4 || fields[0] != "TS" || fields[1] != "warning" || fields[2] != "status" || !strings.Contains(fields[3], "agent 'worker': herdr agent get failed: "+retireDeniedOS) {
		t.Fatalf("friction=%q", log)
	}
}

// ---------- dispatch ----------

// The legacy brief seeds (parity-dispatch.test.mjs).
const (
	retireBrief = `# Goal

Touch nothing.

# Owned files

skills/soho/scripts/soho

# Forbidden

Do not commit or push.

# Report

done.
`
	retireBriefMR = `# Goal

Confirm which role the composed prompt uses.

# Owned files

# Forbidden

Do not commit or push.

# Report

done or skipped.
`
)

var retireFullBrief = retireBriefMR + "\n# Expected result\n\nThe role is reported.\n\n# Acceptance criteria\n\n1. The composed prompt names the role.\n"

type retireDispatchCase struct {
	rules   []fakecli.Rule
	seed    func(*retirementFixture)
	stepEnv []map[string]string
	// structural assertions for the composed prompt files: the legacy
	// includes-checks (role line, brief body, the standing lines).
	role    string
	agent   string
	brief   string
	sidecar [3]string // kind, model, effort from the seeded or newly spawned worker.
	extra   func(t *testing.T, f *retirementFixture, stepOuts []string, final map[string]string)
}

var retireReportPathRE = regexp.MustCompile(`write your report to (.+) and reply with exactly that path`)

func runRetirementDispatchScenario(t *testing.T, invocations map[string][]retireStep, name string, sc retireDispatchCase) {
	t.Helper()
	f := newRetirementFixture(t, sc.rules, sc.seed)
	var outputs []string
	for i, step := range invocations[name] {
		beforeCalls := len(f.herdrLogLines(t))
		if i < len(sc.stepEnv) {
			for k, v := range sc.stepEnv[i] {
				step.Env = mergeEnv(step.Env, k, v)
			}
		}
		code, out, stderr := f.run(t, step)
		if code != step.RC {
			t.Fatalf("step=%d args=%q code=%d want=%d stdout=%q stderr=%q", i, step.Args, code, step.RC, out, stderr)
		}
		out = f.norm(out)
		assertRetirementJSON(t, out, step.JSON)
		calls := f.herdrLogLines(t)[beforeCalls:]
		prompts := 0
		for _, call := range calls {
			if strings.HasPrefix(call, "agent prompt ") {
				prompts++
				if !strings.HasPrefix(call, "agent prompt "+sc.agent+" Read the file ") {
					t.Fatalf("prompt targeted the wrong agent: %q", call)
				}
			}
			if step.Args[0] == "dispatch" && strings.HasPrefix(call, "agent start ") {
				t.Fatalf("dispatch started an agent: %q", call)
			}
		}
		wantPrompts := 1
		if code == 2 || code == 5 {
			wantPrompts = 0
		}
		if prompts != wantPrompts {
			t.Fatalf("step %d prompt count=%d want=%d; calls=%q", i, prompts, wantPrompts, calls)
		}
		if code == 2 && !strings.Contains(stderr, "brief_lint=strict") {
			t.Fatalf("strict lint refusal=%q", stderr)
		}
		outputs = append(outputs, out)
	}
	checkDispatchFinalFiles(t, f, sc, outputs)
	if sc.extra != nil {
		sc.extra(t, f, outputs, f.collectState(t))
	}
}

func mergeEnv(env map[string]string, key, value string) map[string]string {
	if env == nil {
		env = map[string]string{}
	}
	env[key] = value
	return env
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// stable order for the failure message
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// compareComposed asserts the legacy includes-checks of the composed prompt:
// the role line (column 4 of the roster), the brief body, the report
// contract line and the launcher line.
func compareComposed(t *testing.T, key, got string, sc retireDispatchCase, stepOuts []string) {
	t.Helper()
	checks := []string{
		"# Role: " + sc.role + "\n",
		"You are running as the `" + sc.role + "` role, agent name `" + sc.agent + "`, inside a multi-agent run",
		sc.brief,
		"- Run every `soho` command this prompt names through the launcher at `<BIN>`, not through PATH.",
		"# Report contract",
	}
	if sc.role == "researcher" {
		checks = append(checks,
			"- Nobody watches this terminal: do not ask interactive questions or wait for a confirmation.",
			"- Never invent names, endpoints, flags, credentials, URLs or requirements.",
		)
	}
	for _, line := range checks {
		if !strings.Contains(got, line) {
			t.Fatalf("composed %s misses %q", key, line)
		}
	}
	if strings.Contains(got, "<SKILL>/scripts/soho") {
		t.Fatalf("composed %s still names the retired script launcher", key)
	}
	if sc.role == "researcher" && strings.Contains(got, "the `scouter` role") {
		t.Fatalf("composed %s names the scouter role: the role line must use column 4", key)
	}
	// The report contract line carries the dispatch's report path.
	for _, out := range stepOuts {
		m := regexp.MustCompile(`"report":"(.*?)"`).FindStringSubmatch(out)
		if m == nil {
			continue
		}
		want := "- Write your report as Markdown to `" + m[1] + "` (create parent directories if needed)"
		if strings.Contains(got, want) {
			return
		}
	}
	t.Fatalf("composed %s misses the report contract line for any step report path", key)
}

func TestRetirementDispatchContract(t *testing.T) {
	invocations := retirementInvocations["dispatch"]

	// seedRoster appends a trailing newline to each row (parity-dispatch.
	// test.mjs); build12 does the same so the on-disk roster matches.
	build12 := func(f *retirementFixture) string {
		return "build\tp1\tgrok\timplementer\txai\t1\t" + f.repo + "\tnow\tgrok-4.7\tfull\timplementer\tbuild\n"
	}
	writeBrief := func(f *retirementFixture, name, body string) {
		if err := os.WriteFile(filepath.Join(f.repo, name), []byte(body), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}

	cases := map[string]retireDispatchCase{
		// test-quota.sh:171 — an idle worker whose screen carries a provider
		// line is a quota (rc 11, the lane fields in the JSON).
		"dispatch-quota": {
			rules: []fakecli.Rule{
				retireAgentGetRule("build", "idle"),
				{Argv: []string{"agent", "read", "build"}, ArgvPrefix: true, Stdout: "RESOURCE_EXHAUSTED\n"},
			},
			stepEnv: []map[string]string{{"SOHO_BRIEF_LINT": "off"}},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR12+build12(f))
				writeBrief(f, "brief.md", retireBrief)
			},
			role: "implementer", agent: "build", brief: retireBrief,
			extra: func(t *testing.T, f *retirementFixture, stepOuts []string, final map[string]string) {
				if !strings.Contains(final["state/ws/friction.log"], "quota:") {
					t.Fatalf("the friction log carries the quota warning: %q", final["state/ws/friction.log"])
				}
				var q map[string]any
				if err := json.Unmarshal([]byte(stepOuts[0]), &q); err != nil {
					t.Fatal(err)
				}
				for field, want := range map[string]any{"wait_status": "quota", "lane": "build", "kind": "grok", "model": "grok-4.7"} {
					if q[field] != want {
						t.Fatalf("JSON %s=%v want %v", field, q[field], want)
					}
				}
				if !strings.Contains(fmt.Sprint(q["match"]), "RESOURCE_EXHAUSTED") {
					t.Fatalf("match=%v", q["match"])
				}
			},
		},
		// test-quota.sh:214 + 229 — the pane title: `# Brief — <task>` gives
		// the task, a brief whose first H1 is a contract section falls back
		// to the file name.
		"dispatch-titled": {
			rules: []fakecli.Rule{
				retireAgentGetRule("build", "idle"),
				{Argv: []string{"agent", "read", "build"}, ArgvPrefix: true, Stdout: ""},
			},
			stepEnv: []map[string]string{{}, {}},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR12+build12(f))
				writeBrief(f, "tbrief.md", "# Brief — porte da config\n\n"+retireBrief)
				writeBrief(f, "brief.md", retireBrief)
			},
			role:  "implementer",
			agent: "build",
			brief: retireBrief,
		},
		// test-status.sh:199 — a denied worker: the dispatch reports
		// `unavailable` (rc 4), never `gone`; a sandboxed codex worker whose
		// cwd is outside the repo routes the report and the composed prompt
		// through $TMPDIR/soho/<ws>/reports/ (DJA-538: only a sandbox that
		// confines writes to the cwd takes the scratch dir).
		"dispatch-denied": {
			rules: []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: retireDeniedOS + "\n"},
				retireScreenRule("worker", "terminal-fallback"),
			},
			stepEnv: []map[string]string{{"SOHO_BRIEF_LINT": "off"}},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR8+"worker\tp1\tcodex\timplementer\topenai\t1\t"+t.TempDir()+"\tnow\n")
				writeBrief(f, "brief.md", retireBrief)
			},
			role: "implementer", agent: "worker", brief: retireBrief,
			extra: func(t *testing.T, f *retirementFixture, stepOuts []string, final map[string]string) {
				if strings.Contains(stepOuts[0], "gone") {
					t.Fatalf("stderr/out never says gone: %q", stepOuts[0])
				}
				for key := range final {
					if strings.HasPrefix(key, "state/ws/briefs/") {
						t.Fatalf("nothing landed in the state briefs dir: %v", sortedKeys(final))
					}
				}
			},
		},
		// test-multi-role.sh — reviewer submission and strict lint.
		"dispatch-family": {
			rules: []fakecli.Rule{
				retireAgentGetRule("rev", "idle"),
				retireScreenRule("rev", "terminal-fallback"),
			},
			stepEnv: []map[string]string{{}, {}, {"SOHO_BRIEF_LINT": "strict"}, {}},
			seed: func(f *retirementFixture) {
				work := t.TempDir()
				retireWriteRoster(t, f, retireR11+
					"rev\tp9\tcodex\treviewer\topenai\t1\t"+work+"\tnow\tgpt-5\ttask\treviewer\n"+
					"ex\tp1\tcodex\tscouter\topenai\t1\t"+work+"\tnow\tgpt-5\tfull\timplementer,scouter\n")
				writeBrief(f, "brief.md", retireBriefMR)
				writeBrief(f, "full.md", retireFullBrief)
			},
			role: "reviewer", agent: "rev", brief: retireBriefMR,
		},
		// test-multi-role.sh — the role of the composed prompt is column 4
		// of the roster; the composed prompt carries the standing lines.
		"dispatch-col4": {
			rules: []fakecli.Rule{
				retireAgentGetRule("res", "idle"),
				retireScreenRule("res", "terminal-fallback"),
			},
			stepEnv: []map[string]string{{"SOHO_BRIEF_LINT": "off"}},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR11+"res\tp3\tgrok\tresearcher\txai\t1\t"+t.TempDir()+"\tnow\tgrok-4.7\task\tresearcher\n")
				writeBrief(f, "brief.md", retireBriefMR)
			},
			role: "researcher", agent: "res", brief: retireBriefMR,
		},
		// run — spawn (fresh, layout tab) + dispatch --no-wait.
		"run-no-wait": {
			rules: []fakecli.Rule{
				{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
				{Argv: []string{"tab", "get", "t-herd"}, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
				{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
				{Argv: []string{"agent", "rename"}, ArgvPrefix: true, Stdout: `{"result":{}}`},
				retireAgentGetRule("build", "idle"),
				retireScreenRule("build", "terminal-fallback"),
			},
			stepEnv: []map[string]string{{"SOHO_LAYOUT": "tab"}},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR12)
				writeBrief(f, "brief.md", retireFullBrief)
				// The fake grok so a fresh spawn finds the executable (no
				// warning), like the legacy writeFakeGrok.
				if _, err := fakecli.Install(t, f.bin, "grok", []fakecli.Rule{{AnyArgs: true}}); err != nil {
					f.t.Fatal(err)
				}
			},
			role: "implementer", agent: "build", brief: retireFullBrief,
			extra: func(t *testing.T, f *retirementFixture, stepOuts []string, final map[string]string) {
				out := stepOuts[0]
				at := strings.Index(out, `{"wait_status":`)
				if at <= 0 {
					t.Fatalf("the dispatch JSON follows the spawn JSON: %q", out)
				}
				var spawn map[string]any
				if err := json.Unmarshal([]byte(out[:strings.Index(out, "\n}\n")+2]), &spawn); err != nil {
					t.Fatalf("spawn JSON: %v: %q", err, out[:strings.Index(out, "\n}\n")+2])
				}
				if spawn["name"] != "build" || spawn["placement"] != "herd" {
					t.Fatalf("spawn name/placement: %v / %v", spawn["name"], spawn["placement"])
				}
			},
		},
		// run-wait runs in its own runner (runRetirementRunWait): the
		// report-writes worker needs the marker path inside the fake herdr
		// rules before the fixture exists.
	}

	for _, name := range []string{"dispatch-quota", "dispatch-titled", "dispatch-denied", "dispatch-family", "dispatch-col4", "run-no-wait"} {
		t.Run(name, func(t *testing.T) {
			sc := cases[name]
			sc.sidecar = map[string][3]string{
				"dispatch-quota":  {"grok", "grok-4.7", ""},
				"dispatch-titled": {"grok", "grok-4.7", ""},
				"dispatch-denied": {"codex", "", ""},
				"dispatch-family": {"codex", "gpt-5", ""},
				"dispatch-col4":   {"grok", "grok-4.7", ""},
				"run-no-wait":     {"grok", "grok", "xhigh"},
			}[name]
			runRetirementDispatchScenario(t, invocations, name, sc)
		})
	}
	t.Run("run-wait", func(t *testing.T) {
		runRetirementRunWait(t)
	})
}

// runRetirementRunWait runs run-wait with the report-writes worker: a
// goroutine watches the fake herdr call log for the `agent prompt` call,
// parses the report path from the prompt text (like a worker) and writes the
// report, after which the fake herdr returns — so dispatch settles `done`
// from the report file, exactly like the legacy prompt-writes marker.
func runRetirementRunWait(t *testing.T) {
	t.Helper()
	// JS: "run: with the wait, the report settles and collect prints it" —
	// the fake herdr writes the report on `agent prompt` (like a worker);
	// dispatch settles done (rc 0) and collect prints the report under its
	// marker; the pane title gains the check mark. The legacy used a
	// prompt-writes marker file read by the sh fake; here a goroutine
	// watches the fake herdr call log for the prompt call, parses the
	// report path from the prompt text (like a worker) and writes the
	// report before the fake returns, so dispatch settles `done` from the
	// report file.
	// The marker the fake `agent prompt` waits on (absent until the
	// report-writes worker creates it).
	marker := filepath.Join(t.TempDir(), "prompt-writes-marker")
	rules := []fakecli.Rule{
		{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
		{Argv: []string{"tab", "get", "t-herd"}, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "rename"}, ArgvPrefix: true, Stdout: `{"result":{}}`},
		{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"name":"build","agent_status":"idle"}}}`},
		{Argv: []string{"agent", "read", "build"}, ArgvPrefix: true, Stdout: "terminal-fallback"},
		{Argv: []string{"agent", "prompt", "build"}, ArgvPrefix: true, WaitFile: marker, Stdout: `{"result":{"submitted":true}}`},
		{Argv: []string{"pane", "report-metadata"}, ArgvPrefix: true, Stdout: ``},
	}
	sc := retireDispatchCase{
		rules:   rules,
		role:    "implementer",
		agent:   "build",
		brief:   retireFullBrief,
		sidecar: [3]string{"grok", "grok", "xhigh"},
		stepEnv: []map[string]string{{"SOHO_LAYOUT": "tab"}},
	}
	f := newRetirementFixture(t, sc.rules, func(f *retirementFixture) {
		retireWriteRoster(t, f, retireR12)
		if err := os.WriteFile(filepath.Join(f.repo, "brief.md"), []byte(retireFullBrief), 0o600); err != nil {
			f.t.Fatal(err)
		}
	})
	// The fake grok so a fresh spawn finds the executable (no warning).
	if _, err := fakecli.Install(t, f.bin, "grok", []fakecli.Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	var (
		reportPath string
		runErr     error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err == nil {
				for i := len(calls) - 1; i >= 0; i-- {
					argv := calls[i].Argv
					if len(argv) >= 4 && argv[0] == "agent" && argv[1] == "prompt" {
						if m := retireReportPathRE.FindStringSubmatch(argv[3]); m != nil {
							reportPath = m[1]
							break
						}
					}
				}
			}
			if reportPath != "" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if reportPath == "" {
			runErr = fmt.Errorf("the agent prompt call never appeared in the fake herdr log")
			return
		}
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o700); err != nil {
			runErr = err
			return
		}
		if err := os.WriteFile(reportPath, []byte("done report\n"), 0o600); err != nil {
			runErr = err
			return
		}
		if err := os.WriteFile(marker, []byte("1\n"), 0o600); err != nil {
			runErr = err
			return
		}
	}()
	defer func() {
		<-done
		if runErr != nil {
			t.Fatal(runErr)
		}
	}()

	step := mergeStepEnv(retirementInvocations["dispatch"]["run-wait"][0], sc.stepEnv)
	code, out, errOut := f.run(t, step)
	out = f.norm(out)
	if code != step.RC {
		t.Fatalf("run-wait code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	assertRetirementJSON(t, out, step.JSON)
	if !strings.Contains(out, "done report\n") {
		t.Fatalf("collect did not print report: %q", out)
	}
	checkDispatchFinalFiles(t, f, sc, []string{out})
}

func checkDispatchFinalFiles(t *testing.T, f *retirementFixture, sc retireDispatchCase, outputs []string) {
	t.Helper()
	final := f.collectState(t)
	found := false
	for key, content := range final {
		if strings.HasSuffix(key, ".dispatch.json") {
			found = true
			var obj map[string]any
			if err := json.Unmarshal([]byte(content), &obj); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"version", "kind", "model", "effort", "submission", "brief_sha256", "task_report"} {
				if _, ok := obj[field]; !ok {
					t.Fatalf("sidecar %s missing %s", key, field)
				}
			}
			if obj["version"] != float64(1) || obj["submission"] != "accepted" {
				t.Fatalf("sidecar %s: %v", key, obj)
			}
			for i, field := range []string{"kind", "model", "effort"} {
				if obj[field] != sc.sidecar[i] {
					t.Fatalf("sidecar %s %s=%v want=%q", key, field, obj[field], sc.sidecar[i])
				}
			}
			if !strings.Contains(key, "/reports/") && !strings.HasPrefix(key, "state/ws/briefs/") {
				t.Fatalf("sidecar path=%s", key)
			}
		}
		if strings.HasSuffix(key, ".brief.md") {
			compareComposed(t, key, content, sc, outputs)
		}
	}
	// A refused invocation can leave no sidecar; every submitted JSON must have one.
	submitted := false
	for _, out := range outputs {
		if strings.Contains(out, "\"wait_status\"") {
			submitted = true
		}
	}
	if submitted && !found {
		t.Fatalf("missing dispatch sidecar: %v", sortedKeys(final))
	}
	if submitted {
		pointer := final["state/ws/last-report-"+sc.agent]
		if !strings.Contains(pointer, "/reports/"+sc.agent+"-<TS>.md") {
			t.Fatalf("last-report=%q", pointer)
		}
	}
}

func mergeStepEnv(step retireStep, envs []map[string]string) retireStep {
	for _, env := range envs {
		for key, value := range env {
			step.Env = mergeEnv(step.Env, key, value)
		}
	}
	return step
}
