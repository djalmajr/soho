package cli

// Retirement (C1): native Go consumers for the three legacy golden groups no
// native test had consumed — parity-entry, parity-status and parity-dispatch.
// The frozen values come from ../testdata/legacy/parity-{entry,status,dispatch}.json,
// byte-identical copies of the legacy
// skills/herdr-soho/scripts/test/golden/parity-{entry,status,dispatch}.json
// (verified in the 2026-10-06 go-audit, 091729 report §files). The legacy
// suites ran only the JS entry and compared it against those stored values;
// this file reruns the same fixtures through the real native CLI (Run) with a
// fakecli herdr and temporary state, and compares the stable observable
// contracts: exit code, normalized stdout/stderr, on-disk state and the fake
// herdr call log (the native stand-in for the sh fake's herdr.log, whose
// lines are the joined argv).
//
// Normalization mirrors the legacy harness:
//   - the fixture root becomes <ROOT> and the skill dir <SKILL>
//     (test/golden.mjs normalizeRoots);
//   - the dispatch/report stamp \d{8}T\d{6}(?:-\d+)? becomes <TS>
//     (parity-dispatch.test.mjs normTs);
//   - friction timestamps become TS (the legacy per-line regex). Go writes
//     them in UTC with a Z suffix (core.FrictionISO,
//     internal/core/state.go:23-26) where the legacy wrote local time
//     without a Z: the Z is an intentional Go/JS difference and is absorbed
//     by the normalized prefix here;
//   - the stderr program prefix becomes PROG: (parity.mjs normalizeErr; the
//     existing normalizeParityError);
//   - .since files become EPOCH and .approvals.log stamps become TS
//     (parity-dispatch.test.mjs collectState).
//
// Intentional Go/JS differences are asserted here against the Go contract
// (with the existing validated Go consumer cited) instead of re-approving
// the stored legacy value:
//   - status: agent_not_found is `gone`, not the legacy `unavailable`
//     (internal/cli/status_wait_parity_test.go:171 "parity wait: denied
//     (unavailable) and gone (test-status.sh)"; the usage line says "gone =
//     agent_not_found; unavailable = agent get failed (exit 4)");
//   - status: an unknown agent Herdr still knows is `not-in-roster` with a
//     stderr hint (internal/wait/status_not_in_roster_test.go, D11 probe);
//   - status with no names covers the whole roster in roster order, same as
//     the named run (internal/cli/status_cases_test.go "status: no names
//     covers the whole roster in roster order, same as the named run");
//     the legacy died 2 with "give at least one agent name";
//   - the dispatch sidecar is a superset of the legacy sidecar (task_report,
//     brief_sha256, for, session: the one-send-once guard): the stable legacy
//     fields are compared, the superseded shape is not pinned;
//   - the composed prompt is compared structurally (role line, brief body,
//     report contract line, launcher line), not byte-for-byte: the stored
//     prose predates the native role files;
//   - the entry help variants print the current native usage text, not the
//     stored legacy prose: the usage text is the current contract, pinned by
//     TestHelpAndUnknown (internal/cli/cli_test.go "help variants print the
//     usage"); the scenario consumes the argument matrix against it.
//   - the prompt's command path is the native executable (<BIN>), never
//     the retired skill scripts launcher; other frozen prompt/state
//     expectations remain unchanged.

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
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	Err  string            `json:"err"`
	Out  string            `json:"out"`
	RC   int               `json:"rc"`
}

type retireFile struct {
	Rel     string  `json:"rel"`
	Content *string `json:"content"`
}

// entry and status scenarios (status-denied-friction is the shapeless one).
type retireStepsScenario struct {
	Files []retireFile `json:"files"`
	Steps []retireStep `json:"steps"`
}

type retireFrictionScenario struct {
	Friction string `json:"friction"`
	RC       int    `json:"rc"`
}

type retireDispatchScenario struct {
	Files     map[string]string   `json:"files"`
	StepFiles []map[string]string `json:"stepFiles"`
	Steps     []retireStep        `json:"steps"`
}

func loadRetirementGolden(t *testing.T, suite string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "legacy", "parity-"+suite+".json"))
	if err != nil {
		t.Fatalf("read frozen parity-%s.json: %v", suite, err)
	}
	var goldens map[string]json.RawMessage
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatalf("parse frozen parity-%s.json: %v", suite, err)
	}
	return goldens
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

func TestRetirementEntryGolden(t *testing.T) {
	// JS: parity-entry.test.mjs (help-variants, unknown-command).
	// The stored legacy help prose is superseded: the native usage text is
	// the current contract, pinned by TestHelpAndUnknown
	// (cli_test.go "help variants print the usage", "soho —
	// role-agent layer"). The scenario is consumed against that Go
	// contract: the same argument matrix, rc 0, the current usage on
	// stdout, nothing on stderr.
	goldens := loadRetirementGolden(t, "entry")
	for _, name := range []string{"help-variants", "unknown-command"} {
		t.Run(name, func(t *testing.T) {
			var sc retireStepsScenario
			if err := json.Unmarshal(goldens[name], &sc); err != nil {
				t.Fatal(err)
			}
			f := newRetirementFixture(t, nil, nil)
			for i, step := range sc.Steps {
				code, out, errOut := f.run(t, step)
				out, errOut = f.norm(out), f.norm(normalizeParityError(errOut))
				if name == "help-variants" {
					if code != 0 || out != usage || errOut != "" {
						t.Fatalf("step %d args=%v: code=%d want 0; stderr=%q; first stdout diff vs current usage: %s",
							i, step.Args, code, errOut, firstGoldenLineDifference(out, usage))
					}
					continue
				}
				if code != step.RC || out != step.Out || errOut != step.Err {
					t.Fatalf("step %d args=%v: code=%d want %d out=%q want %q err=%q want %q",
						i, step.Args, code, step.RC, out, step.Out, errOut, step.Err)
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

func runRetirementStatusScenario(t *testing.T, goldens map[string]json.RawMessage, name string, sc retireStatusScenario) {
	t.Helper()
	var stored retireStepsScenario
	if err := json.Unmarshal(goldens[name], &stored); err != nil {
		t.Fatalf("golden %s: %v", name, err)
	}
	f := newRetirementFixture(t, sc.rules, sc.seed)
	for i, step := range stored.Steps {
		if i < len(sc.stepEnv) {
			for key, value := range sc.stepEnv[i] {
				step.Env = mergeEnv(step.Env, key, value)
			}
		}
		code, out, errOut := f.run(t, step)
		if sc.transform != nil {
			out = sc.transform(out)
		}
		out = f.norm(out)
		errOut = f.norm(normalizeParityError(errOut))
		wantRC, wantOut, wantErr := step.RC, step.Out, step.Err
		if w, ok := sc.want[i]; ok {
			wantRC, wantOut, wantErr = w.RC, w.Out, w.Err
		}
		if code != wantRC || out != wantOut || errOut != wantErr {
			t.Fatalf("step %d args=%v: code=%d want %d; first stdout diff: %s; stderr=%q want %q",
				i, step.Args, code, wantRC, firstGoldenLineDifference(out, wantOut), errOut, wantErr)
		}
	}
	for _, file := range stored.Files {
		actual, readErr := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(file.Rel)))
		if file.Content == nil {
			if !os.IsNotExist(readErr) {
				t.Fatalf("file %s should be absent (error=%v)", file.Rel, readErr)
			}
			continue
		}
		if readErr != nil {
			t.Fatalf("file %s: %v", file.Rel, readErr)
		}
		got, want := f.norm(string(actual)), f.norm(*file.Content)
		if got != want {
			t.Fatalf("file %s:\ngot  %q\nwant %q", file.Rel, got, want)
		}
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

func TestRetirementStatusGolden(t *testing.T) {
	// JS: parity-status.test.mjs (the status/roster/friction scenarios of
	// test-status.sh, plus the roster/table cases and the env error paths).
	goldens := loadRetirementGolden(t, "status")

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
				runRetirementDeniedFriction(t, goldens)
				return
			}
			runRetirementStatusScenario(t, goldens, name, retireStatusCase(t, name))
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

// status-denied-friction is stored by shape (the friction line with its
// timestamp normalized to TS) rather than as steps/files.
func runRetirementDeniedFriction(t *testing.T, goldens map[string]json.RawMessage) {
	var stored retireFrictionScenario
	if err := json.Unmarshal(goldens["status-denied-friction"], &stored); err != nil {
		t.Fatalf("golden status-denied-friction: %v", err)
	}
	f := newRetirementFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: retireDeniedOS + "\n"},
		retireScreenRule("worker", "terminal-fallback"),
	}, func(f *retirementFixture) { retireWriteRoster(t, f, retireR8+retireWorker8) })
	code, _, _ := f.run(t, retireStep{Args: []string{"status", "worker"}})
	if code != stored.RC {
		t.Fatalf("code=%d want %d", code, stored.RC)
	}
	log, err := os.ReadFile(filepath.Join(f.ws, "friction.log"))
	if err != nil {
		t.Fatalf("friction log: %v", err)
	}
	got := strings.TrimSpace(normFrictionContent(f.norm(string(log))))
	want := strings.TrimSpace(stored.Friction)
	if got != want {
		t.Fatalf("friction=%q want %q", got, want)
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

skills/soho/scripts/soho

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
	role  string
	agent string
	brief string
	extra func(t *testing.T, f *retirementFixture, stepOuts []string, final map[string]string)
}

var retireReportPathRE = regexp.MustCompile(`write your report to (.+) and reply with exactly that path`)

func runRetirementDispatchScenario(t *testing.T, goldens map[string]json.RawMessage, name string, sc retireDispatchCase) {
	t.Helper()
	var stored retireDispatchScenario
	if err := json.Unmarshal(goldens[name], &stored); err != nil {
		t.Fatalf("golden %s: %v", name, err)
	}
	f := newRetirementFixture(t, sc.rules, sc.seed)
	stepOuts := make([]string, 0, len(stored.Steps))
	for i, step := range stored.Steps {
		env := map[string]string{}
		if i < len(sc.stepEnv) {
			env = sc.stepEnv[i]
		}
		for key, value := range env {
			step.Env = mergeEnv(step.Env, key, value)
		}
		code, out, errOut := f.run(t, step)
		out = f.norm(out)
		// The frozen goldens predate the name renames; the oracle rewrites
		// only the anchored name/message forms, everything else is
		// byte-identical.
		wantOut, wantErr := testutil.RewriteSohoName(step.Out), testutil.RewriteSohoName(step.Err)
		// DJA-392 intentional contract adaptation: roles with mcp=none add
		// the kind's MCP-off flag after the effort args; the frozen bytes
		// are unchanged.
		if adapted, adErr := testutil.RewriteMcpSpawnJSON(wantOut); adErr != nil {
			t.Fatalf("DJA-392 spawn JSON adaptation: %s", adErr)
		} else {
			wantOut = adapted
		}
		if adaptedErr, adErr := testutil.RewriteMcpErr(wantErr, wantOut); adErr != nil {
			t.Fatalf("DJA-392 dispatch stderr adaptation: %s", adErr)
		} else {
			wantErr = adaptedErr
		}
		errOut = f.norm(normalizeParityError(errOut))
		if code != step.RC || out != wantOut || errOut != wantErr {
			t.Fatalf("step %d args=%v: code=%d want %d; first stdout diff: %s; stderr=%q want %q",
				i, step.Args, code, step.RC, firstGoldenLineDifference(out, wantOut), errOut, wantErr)
		}
		stepOuts = append(stepOuts, out)
		// The fake herdr call log after the step is the legacy herdr.log:
		// one joined-argv line per call (an empty log is an empty slice).
		if wantRaw, ok := stored.StepFiles[i]["herdr.log"]; ok {
			wantRaw = testutil.RewriteSohoName(wantRaw)
			if adapted, adErr := testutil.RewriteMcpAgentStartLog(wantRaw); adErr != nil {
				t.Fatalf("DJA-392 dispatch log adaptation: %s", adErr)
			} else {
				wantRaw = adapted
			}
			want := []string{}
			if trimmed := strings.TrimSuffix(wantRaw, "\n"); trimmed != "" {
				want = strings.Split(trimmed, "\n")
			}
			got := f.herdrLogLines(t)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("herdr call log after step %d:\ngot  %v\nwant %v", i, got, want)
			}
		}
	}
	checkDispatchFinalFiles(t, f, stored, sc, stepOuts)
	if sc.extra != nil {
		sc.extra(t, f, stepOuts, f.collectState(t))
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

// compareSidecar compares the stable legacy fields of the dispatch sidecar;
// the native sidecar is a superset (task_report, brief_sha256, for, session:
// the one-send-once guard) and its superseded shape is not pinned.
func compareSidecar(t *testing.T, key, got, want string) {
	t.Helper()
	parse := func(v string) map[string]any {
		var obj map[string]any
		if err := json.Unmarshal([]byte(v), &obj); err != nil {
			t.Fatalf("sidecar %s: %v (content %q)", key, err, v)
		}
		return obj
	}
	g, w := parse(got), parse(want)
	for _, field := range []string{"version", "kind", "model", "effort", "submission"} {
		if !reflect.DeepEqual(g[field], w[field]) {
			t.Fatalf("sidecar %s field %s: got %v want %v", key, field, g[field], w[field])
		}
	}
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

// TestDJA379DispatchOracle exercises the DJA-379 adaptation itself:
// applied to a fragment of the frozen golden it replaces exactly the three
// documented pairs and nothing else; applied to a text without a pair it
// fails.
func TestDJA379DispatchOracle(t *testing.T) {
	// A controlled fragment carrying all three pairs, plus the distractors
	// the exact-string contract must leave alone: the .current.md report
	// pointer, another agent's path and the state-dir files the pairs do
	// not cover.
	frag := `{"files":{"state/ws/briefs/build-<TS>.md":"<ROOT>/state/ws/reports/build-<TS>.md","state/ws/reports/build-<TS>.current.md":"old","state/ws/briefs/build-<TS>.dispatch.json":"{}","state/ws/last-report-build":"state/ws/reports/build-<TS>.md","herdr.log":"agent prompt build <ROOT>/state/ws/briefs/build-<TS>.md <ROOT>/state/ws/reports/build-<TS>.md","tmp/soho/ws/reports/rev-<TS>.md":"rev"}}`
	want := `{"files":{"tmp/soho/ws/reports/build-<TS>.brief.md":"<ROOT>/tmp/soho/ws/reports/build-<TS>.md","state/ws/reports/build-<TS>.current.md":"old","tmp/soho/ws/reports/build-<TS>.dispatch.json":"{}","state/ws/last-report-build":"tmp/soho/ws/reports/build-<TS>.md","herdr.log":"agent prompt build <ROOT>/tmp/soho/ws/reports/build-<TS>.brief.md <ROOT>/tmp/soho/ws/reports/build-<TS>.md","tmp/soho/ws/reports/rev-<TS>.md":"rev"}}`
	adapted, err := testutil.ApplyDispatchReportDirOracle(frag)
	if err != nil {
		t.Fatalf("adapt the fragment: %v", err)
	}
	if adapted != want {
		t.Fatalf("fragment adaptation:\n got  %s\n want %s", adapted, want)
	}

	// A real fragment of the frozen golden: the dispatch-quota scenario
	// document. The adaptation must produce exactly the three pair swaps,
	// computed here independently of the implementation.
	pairs := [][2]string{
		{testutil.DispatchBriefOraclePrevious, testutil.DispatchBriefOracleNative},
		{testutil.DispatchReportOraclePrevious, testutil.DispatchReportOracleNative},
		{testutil.DispatchSidecarOraclePrevious, testutil.DispatchSidecarOracleNative},
	}
	raw := string(loadRetirementGolden(t, "dispatch")["dispatch-quota"])
	independent := raw
	for _, pair := range pairs {
		independent = strings.ReplaceAll(independent, pair[0], pair[1])
	}
	adapted, err = testutil.ApplyDispatchReportDirOracle(raw)
	if err != nil {
		t.Fatalf("adapt the dispatch-quota fragment: %v", err)
	}
	if adapted != independent {
		t.Fatalf("the golden fragment adaptation differs from the exact pair swaps: %s",
			firstGoldenLineDifference(adapted, independent))
	}

	// Without a pair the adaptation fails, per missing pair: it is never
	// applied silently over a changed golden.
	if _, err := testutil.ApplyDispatchReportDirOracle(`{"files":{"herdr.log":"agent list"}}`); err == nil {
		t.Fatal("a text without any pair must fail")
	}
	for i, missing := range pairs {
		text := ""
		for j, pair := range pairs {
			if j != i {
				text += pair[0] + " "
			}
		}
		if _, err := testutil.ApplyDispatchReportDirOracle(text); err == nil {
			t.Fatalf("missing %s: expected an error", missing[0])
		} else if !strings.Contains(err.Error(), missing[0]) {
			t.Fatalf("missing %s: the error does not name it: %v", missing[0], err)
		}
	}
	// A second application over the adapted text fails: no pair remains, so
	// re-adapting a document is always loud.
	if _, err := testutil.ApplyDispatchReportDirOracle(adapted); err == nil {
		t.Fatal("a second application over the adapted text must fail")
	}

	// The run-wait-only mirror step: applied to the pair-adapted run-wait
	// document, the state entries exist and carry the same content as the
	// adapted tmp/soho entries (mirrorReport copies them); applied to a
	// document without the tmp entries it fails.
	runWaitRaw := string(loadRetirementGolden(t, "dispatch")["run-wait"])
	adaptedRunWait, err := testutil.ApplyDispatchReportDirOracle(runWaitRaw)
	if err != nil {
		t.Fatalf("adapt the run-wait fragment: %v", err)
	}
	mirrored, err := testutil.ApplyDispatchMirrorEntries(adaptedRunWait)
	if err != nil {
		t.Fatalf("mirror the run-wait fragment: %v", err)
	}
	var mirroredDoc struct {
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal([]byte(mirrored), &mirroredDoc); err != nil {
		t.Fatalf("parse the mirrored run-wait scenario: %v", err)
	}
	for stateKey, tmpKey := range map[string]string{
		"state/ws/reports/build-<TS>.md":           "tmp/soho/ws/reports/build-<TS>.md",
		"state/ws/briefs/build-<TS>.md":            "tmp/soho/ws/reports/build-<TS>.brief.md",
		"state/ws/briefs/build-<TS>.dispatch.json": "tmp/soho/ws/reports/build-<TS>.dispatch.json",
	} {
		stateContent, okState := mirroredDoc.Files[stateKey]
		tmpContent, okTmp := mirroredDoc.Files[tmpKey]
		if !okState {
			t.Fatalf("the mirrored inventory lost the state entry %s", stateKey)
		}
		if !okTmp {
			t.Fatalf("the mirrored inventory lost the tmp entry %s", tmpKey)
		}
		if stateContent != tmpContent {
			t.Fatalf("the mirror entry %s differs from its tmp copy %s:\n got  %q\n want %q", stateKey, tmpKey, stateContent, tmpContent)
		}
	}
	// Without the adapted tmp entries the mirror step fails: it is never
	// applied silently over a document the pair adaptation did not produce.
	if _, err := testutil.ApplyDispatchMirrorEntries(runWaitRaw); err == nil {
		t.Fatal("the mirror step over the unadapted document must fail")
	} else if !strings.Contains(err.Error(), "tmp/soho/ws/reports/build-<TS>.md") {
		t.Fatalf("the mirror error does not name the missing tmp entry: %v", err)
	}
}

func TestRetirementDispatchGolden(t *testing.T) {
	// JS: parity-dispatch.test.mjs (test-quota.sh, test-status.sh:199,
	// test-multi-role.sh, and run with/without the wait).
	goldens := loadRetirementGolden(t, "dispatch")

	// DJA-379 intentional contract adaptation: with the state dir outside the worker's cwd, the composed prompt and the report go to $TMPDIR/soho/<ws>/reports; the frozen bytes are unchanged. run-wait also keeps the state-dir entries: wait's mirrorReport copies the settled report, the composed brief and the sidecar back to the state dir.
	for _, name := range []string{"dispatch-quota", "dispatch-titled", "run-no-wait", "run-wait"} {
		adapted, err := testutil.ApplyDispatchReportDirOracle(string(goldens[name]))
		if err != nil {
			t.Fatalf("golden %s: %v", name, err)
		}
		goldens[name] = json.RawMessage(adapted)
	}
	// The run-wait-only step of the adaptation: the wait settles done, so
	// mirrorReport copied the settled report, the composed brief and the
	// sidecar back to the state dir; the expected final inventory keeps the
	// three state entries, each with the content of its adapted tmp/soho
	// copy.
	if mirrored, err := testutil.ApplyDispatchMirrorEntries(string(goldens["run-wait"])); err != nil {
		t.Fatalf("golden run-wait: %v", err)
	} else {
		goldens["run-wait"] = json.RawMessage(mirrored)
	}

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
		// `unavailable` (rc 4), never `gone`; the worker cwd outside the repo
		// routes the report and the composed prompt through
		// $TMPDIR/soho/<ws>/reports/.
		"dispatch-denied": {
			rules: []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: retireDeniedOS + "\n"},
				retireScreenRule("worker", "terminal-fallback"),
			},
			stepEnv: []map[string]string{{"SOHO_BRIEF_LINT": "off"}},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR8+retireWorker8)
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
		// test-multi-role.sh — the reviewer family check and the strict lint.
		"dispatch-family": {
			rules: []fakecli.Rule{
				retireAgentGetRule("rev", "idle"),
				retireScreenRule("rev", "terminal-fallback"),
			},
			stepEnv: []map[string]string{{}, {}, {"SOHO_BRIEF_LINT": "strict"}, {}},
			seed: func(f *retirementFixture) {
				retireWriteRoster(t, f, retireR11+
					"rev\tp9\tcodex\treviewer\topenai\t1\t/work\tnow\tgpt-5\ttask\treviewer\n"+
					"ex\tp1\tcodex\tscouter\topenai\t1\t/work\tnow\tgpt-5\tfull\timplementer,scouter\n")
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
				retireWriteRoster(t, f, retireR11+"res\tp3\tgrok\tresearcher\txai\t1\t/work\tnow\tgrok-4.7\task\tresearcher\n")
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
			runRetirementDispatchScenario(t, goldens, name, cases[name])
		})
	}
	t.Run("run-wait", func(t *testing.T) {
		runRetirementRunWait(t, goldens)
	})
}

// runRetirementRunWait runs run-wait with the report-writes worker: a
// goroutine watches the fake herdr call log for the `agent prompt` call,
// parses the report path from the prompt text (like a worker) and writes the
// report, after which the fake herdr returns — so dispatch settles `done`
// from the report file, exactly like the legacy prompt-writes marker.
func runRetirementRunWait(t *testing.T, goldens map[string]json.RawMessage) {
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
	var stored retireDispatchScenario
	if err := json.Unmarshal(goldens["run-wait"], &stored); err != nil {
		t.Fatal(err)
	}
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

	step := mergeStepEnv(stored.Steps[0], sc.stepEnv)
	code, out, errOut := f.run(t, step)
	out = f.norm(out)
	// The frozen goldens predate the name renames; the oracle rewrites
	// only the anchored name/message forms, everything else is
	// byte-identical.
	wantOut, wantErr := testutil.RewriteSohoName(step.Out), testutil.RewriteSohoName(step.Err)
	// DJA-392 intentional contract adaptation: roles with mcp=none add the
	// kind's MCP-off flag after the effort args; the frozen bytes are
	// unchanged.
	if adapted, adErr := testutil.RewriteMcpSpawnJSON(wantOut); adErr != nil {
		t.Fatalf("DJA-392 spawn JSON adaptation: %s", adErr)
	} else {
		wantOut = adapted
	}
	if adaptedErr, adErr := testutil.RewriteMcpErr(wantErr, wantOut); adErr != nil {
		t.Fatalf("DJA-392 run-wait stderr adaptation: %s", adErr)
	} else {
		wantErr = adaptedErr
	}
	errOut = f.norm(normalizeParityError(errOut))
	if code != step.RC || out != wantOut || errOut != wantErr {
		t.Fatalf("step 0 args=%v: code=%d want %d; first stdout diff: %s; stderr=%q want %q",
			step.Args, code, step.RC, firstGoldenLineDifference(out, wantOut), errOut, wantErr)
	}
	stepOuts := []string{out}
	if wantRaw, ok := stored.StepFiles[0]["herdr.log"]; ok {
		wantRaw = testutil.RewriteSohoName(wantRaw)
		if adapted, adErr := testutil.RewriteMcpAgentStartLog(wantRaw); adErr != nil {
			t.Fatalf("DJA-392 run-wait log adaptation: %s", adErr)
		} else {
			wantRaw = adapted
		}
		want := strings.Split(strings.TrimSuffix(wantRaw, "\n"), "\n")
		if got := f.herdrLogLines(t); !reflect.DeepEqual(got, want) {
			t.Fatalf("herdr call log:\ngot  %v\nwant %v", got, want)
		}
	}
	checkDispatchFinalFiles(t, f, stored, sc, stepOuts)
}

// checkDispatchFinalFiles compares the final on-disk state with the legacy
// file set: exact for stable files, semantic for the sidecar superset,
// structural for the composed prompt; every file the native run leaves that
// the legacy run did not is a failure.
func checkDispatchFinalFiles(t *testing.T, f *retirementFixture, stored retireDispatchScenario, sc retireDispatchCase, stepOuts []string) {
	t.Helper()
	final := f.collectState(t)
	// The frozen file set predates the name renames; the oracle rewrites
	// only the anchored name/message forms, keys and contents alike.
	wantFiles := make(map[string]string, len(stored.Files))
	for key, want := range stored.Files {
		wantFiles[testutil.RewriteSohoName(key)] = testutil.RewriteSohoName(want)
	}
	for key, want := range wantFiles {
		if key == "herdr.log" {
			continue
		}
		if key == "state/ws/agents.tsv" {
			// DJA-392 fatia 2 R2: the spawn records the effective MCP
			// choice as the roster's last field; adapt the frozen rows the
			// scenario's run spawned (the frozen bytes stay unchanged).
			adapted, adErr := testutil.RewriteMcpRoster(want, testutil.McpSpawnedAgents(stepOuts...))
			if adErr != nil {
				t.Fatalf("DJA-392 R2 roster adaptation for %s: %s", key, adErr)
			}
			want = adapted
		}
		got, ok := final[key]
		if !ok {
			t.Fatalf("missing state file %s (have %v)", key, sortedKeys(final))
		}
		if strings.HasSuffix(key, ".dispatch.json") {
			compareSidecar(t, key, got, want)
			continue
		}
		if strings.HasPrefix(want, "# Role:") {
			compareComposed(t, key, got, sc, stepOuts)
			continue
		}
		if got != want {
			t.Fatalf("state file %s:\ngot  %q\nwant %q", key, got, want)
		}
	}
	for key := range final {
		if _, ok := wantFiles[key]; !ok {
			// DJA-392 intentional contract adaptation: the fresh-spawn MCP
			// warning is the only friction line the frozen scenarios can
			// gain; anything else is still a failure.
			if testutil.IsMcpFrictionLog(key, final[key]) {
				continue
			}
			t.Fatalf("unexpected state file %s: %q (the legacy run left no such file)", key, final[key])
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

// ---------- the UTF-16 family order, through the real CLI ----------

// The legacy `dispatch family conflict details use deterministic UTF-16
// order` (test/dispatch.test.mjs:1260) and its sibling
// `forSpecFamily: conflicting recorded families are reported in code-unit
// order` (test/dispatch.test.mjs:1314; the native sibling is
// internal/dispatch/dispatch_test.go:352, two families) are consumed here
// end-to-end: four accepted sidecars of a released agent across four
// families must fail `--for` with the families in code-unit order —
// anthropic, google, openai, xai — whatever the locale. The family alphabet
// is closed ASCII, so sort.Strings (byte order) and UTF-16 code-unit order
// agree; a locale-dependent comparison would not.
func TestRetirementDispatchFamilyOrderUTF16(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX fixture contract (see the suite header)")
	}
	f := newRetirementFixture(t, nil, func(f *retirementFixture) {
		// `build` is NOT in the roster: the roster branch of ForSpecFamily
		// returns the row's family (derived from kind/model) without the
		// sidecar scan; the scan — and the conflict — only run for a spec
		// that is a released agent, not a live one.
		retireWriteRoster(t, f, retireR11+
			"rev\tp9\tcodex\treviewer\topenai\t1\t/work\tnow\tgpt-5\ttask\treviewer\n")
		if err := os.WriteFile(filepath.Join(f.repo, "brief.md"), []byte("# Goal\n\nwork\n"), 0o600); err != nil {
			f.t.Fatal(err)
		}
		briefs := filepath.Join(f.ws, "briefs")
		if err := os.MkdirAll(briefs, 0o700); err != nil {
			f.t.Fatal(err)
		}
		for _, row := range []struct{ ts, kind, model string }{
			{"20260925T100001", "grok", "grok-4"},
			{"20260925T100002", "codex", "gpt-5"},
			{"20260925T100003", "gemini", "gemini-2.5"},
			{"20260925T100004", "claude", "claude-sonnet-4"},
		} {
			data := fmt.Sprintf(`{"version":1,"kind":%q,"model":%q,"effort":"full","submission":"accepted"}`, row.kind, row.model)
			if err := os.WriteFile(filepath.Join(briefs, "build-"+row.ts+".dispatch.json"), []byte(data), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}
	})
	code, out, errOut := f.run(t, retireStep{Args: []string{"dispatch", "rev", "brief.md", "--for", "build", "--no-wait"}, Env: map[string]string{"SOHO_BRIEF_LINT": "off"}})
	if code != 2 {
		t.Fatalf("code=%d out=%q err=%q, want the family-conflict refusal 2", code, out, errOut)
	}
	wantErr := "PROG: dispatch: --for 'build': the recorded dispatches of this released agent do not agree on one known model family (anthropic ×1, google ×1, openai ×1, xai ×1); pass the family instead (anthropic|openai|xai|google|alibaba)"
	if got := strings.TrimSpace(f.norm(normalizeParityError(errOut))); got != wantErr {
		t.Fatalf("stderr=%q\nwant %q", got, wantErr)
	}
	// The friction line carries the same detail, with the timestamp to TS.
	log, err := os.ReadFile(filepath.Join(f.ws, "friction.log"))
	if err != nil {
		t.Fatalf("friction log: %v", err)
	}
	wantLine := "TS\terror(exit 2)\tdispatch\t" + strings.TrimPrefix(wantErr, "PROG: ")
	if got := strings.TrimSpace(normFrictionContent(f.norm(string(log)))); got != wantLine {
		t.Fatalf("friction=%q\nwant %q", got, wantLine)
	}
	// Nothing was sent: no herdr call, no composed prompt, no task pointer,
	// no $TMPDIR tree (the send and the writes happen after the family
	// check, so a refusal touches nothing).
	if lines := f.herdrLogLines(t); len(lines) != 0 {
		t.Fatalf("the refused --for sends nothing; got calls %v", lines)
	}
	if _, err := os.Stat(filepath.Join(f.ws, "task-rev")); !os.IsNotExist(err) {
		t.Fatalf("a refused --for leaves no task pointer: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.ws, "briefs"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "rev-") {
			t.Fatalf("a refused --for leaves no composed brief: %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(f.tmp, "soho")); !os.IsNotExist(err) {
		t.Fatalf("no $TMPDIR tree for a refused dispatch: %v", err)
	}
}
