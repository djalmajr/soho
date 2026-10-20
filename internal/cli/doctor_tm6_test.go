package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type tm6GoldenStep struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	Err  string            `json:"err"`
	Out  string            `json:"out"`
	RC   int               `json:"rc"`
}

type tm6GoldenScenario struct {
	Conf  *string         `json:"conf"`
	Steps []tm6GoldenStep `json:"steps"`
}

const tm6LegacyConfig = "# keep this comment\nsplit_max_panes=6\nrole.implementer.kind=grok\nrole.designer.kind=grok\nrole.tasker.kind=grok\nrole.scouter.kind=grok\nrole.researcher.kind=grok\nrole.reviewer.kind=grok\nrole.security-reviewer.kind=grok\nrole.ui-reviewer.kind=grok\nrole.inspector.kind=grok\nrole.planner.model=fable\n# tail comment\n"
const tm6DivergentConfig = "# keep this comment\nsplit_max_panes=6\nrole.reviewer.kind=codex\nrole.security-reviewer.kind=claude\nrole.planner.model=fable\n# tail comment\n"
const tm6FixInput = "role.implementer.kind=grok\nrole.designer.kind=grok\nrole.tasker.kind=grok\nrole.scouter.kind=grok\nrole.researcher.kind=grok\nrole.reviewer.kind=grok\nrole.security-reviewer.kind=grok\nrole.ui-reviewer.kind=grok\nrole.inspector.kind=grok\nrole.planner.model=fable\nmax_workers=9\nsplit_max_panes=8\n"

func tm6CLIEnv(t *testing.T) (platform.Env, string) {
	t.Helper()
	base, cwd := commandFixture(t)
	clean := platform.Env{}
	for _, item := range testutil.CleanEnv(t) {
		if key, value, ok := strings.Cut(item, "="); ok {
			clean[key] = value
		}
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "HERDR_WORKSPACE_ID"} {
		clean[key] = base.Get(key)
	}
	clean["SOHO_DIR"] = base.Get("SOHO_DIR")
	clean["SOHO_SKILL_DIR"] = base.Get("SOHO_SKILL_DIR")
	clean["USERPROFILE"] = base.Get("HOME")
	clean["PATH"] = t.TempDir()
	clean["TMPDIR"] = t.TempDir()
	clean["HERDR_ENV"] = ""
	clean["HERDR_SOCKET_PATH"] = filepath.Join(t.TempDir(), "missing.sock")
	return clean, cwd
}

func tm6Write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorTM6CLIAndParity(t *testing.T) {
	t.Run(`stateRootPath: relative state dirs use the native path separator`, func(t *testing.T) { // JS: "stateRootPath: relative state dirs use the native path separator"
		// Mutation captured: resolving a relative state root from the process cwd instead of the project root changes its path.
		env, cwd := tm6CLIEnv(t)
		env["SOHO_DIR"] = "relative-state"
		ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
		got := core.StateRootPath(&ctx, env, cwd)
		if got != filepath.Join(cwd, "relative-state") {
			t.Fatalf("state root=%q", got)
		}
	})
	t.Run(`explainActivity: report, working, quota, waiting, closed and unknown states`, func(t *testing.T) { // JS: "explainActivity: report, working, quota, waiting, closed and unknown states"
		// Mutation captured: changing state precedence or report existence changes explain's user-visible activity label.
		env, cwd := tm6CLIEnv(t)
		bin := t.TempDir()
		_, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "work"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "quota"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "quota", "--source", "visible", "--lines", "20"}, Stdout: "Individual quota reached\n"},
			{Argv: []string{"agent", "get", "blocked"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "get", "gone"}, Stderr: `{"error":{"code":"agent_not_found"}}`, Code: 1},
			{Argv: []string{"agent", "get", "unknown"}, Stderr: `{"error":{"code":"boom"}}`, Code: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, bin)
		sd := filepath.Join(env.Get("SOHO_DIR"), "ws")
		if err := os.MkdirAll(sd, 0o700); err != nil {
			t.Fatal(err)
		}
		ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
		for _, tc := range []struct{ name, want string }{{"work", "working"}, {"quota", "out of quota"}, {"blocked", "waiting for approval"}, {"gone", "closed"}, {"unknown", "state unknown"}} {
			if got := explainActivity(tc.name, sd, &ctx, env, cwd); got != tc.want {
				t.Errorf("%s=%q want %q", tc.name, got, tc.want)
			}
		}
		report := filepath.Join(sd, "done.md")
		tm6Write(t, report, "report body\n")
		tm6Write(t, filepath.Join(sd, "last-report-done"), report+"\n")
		if got := explainActivity("done", sd, &ctx, env, cwd); got != "idle" {
			t.Fatalf("completed report activity=%q", got)
		}
		missing := filepath.Join(sd, "last-report-waiting")
		tm6Write(t, missing, "/missing/report.md\n")
		if got := explainActivity("waiting", sd, &ctx, env, cwd); got != "waiting for report" {
			t.Fatalf("waiting activity=%q", got)
		}
	})
	t.Run(`explainStateDir: an unreadable state root lists nothing (bash find 2>/dev/null)`, func(t *testing.T) { // JS: "explainStateDir: an unreadable state root lists nothing (bash find 2>/dev/null)"
		// Mutation captured: treating an unreadable state root as an empty or discovered roster changes the state-dir result.
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not enforce POSIX directory permission bits")
		}
		env, cwd := tm6CLIEnv(t)
		state := filepath.Join(t.TempDir(), "state")
		tm6Write(t, filepath.Join(state, "ws1", "agents.tsv"), "# header\nagent\tp1\n")
		env["SOHO_DIR"] = state
		env["HERDR_WORKSPACE_ID"] = ""
		if os.Geteuid() == 0 {
			t.Skip("root bypasses mode 000 directory permissions")
		}
		if err := os.Chmod(state, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(state, 0o700) })
		ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
		if got := explainStateDir(&ctx, env, cwd); got.rc != 1 || got.dir != "" {
			t.Fatalf("state result=%+v", got)
		}
	})
	t.Run(`cmdDoctor: unknown option dies 2; doctor --fix --user re-runs the check in-process`, func(t *testing.T) { // JS: "cmdDoctor: unknown option dies 2; doctor --fix --user re-runs the check in-process"
		// Mutation captured: silently accepting unknown doctor options or omitting the post-fix check changes exit/output.
		env, cwd := tm6CLIEnv(t)
		code, out, stderr := runIn(t, []string{"doctor", "--bogus"}, env, cwd)
		if code != 2 || out != "" || !strings.Contains(stderr, "doctor: unknown option '--bogus'") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		tm6Write(t, filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config"), "panes=3\n")
		// The public --user flow writes only the user file and emits the rerun summary.
		code, out, stderr = runIn(t, []string{"doctor", "--fix", "--user", "--panes", "4"}, env, cwd)
		if code != 0 || !strings.Contains(out, "first_run:") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run(`parity: explain with two rosters and no current workspace names the ambiguity (test-friendly.sh)`, func(t *testing.T) { // JS: "parity: explain with two rosters and no current workspace names the ambiguity (test-friendly.sh)"
		// Mutation captured: failing to count data-bearing workspace rosters hides the ambiguity response.
		env, cwd := tm6CLIEnv(t)
		env["HERDR_WORKSPACE_ID"] = ""
		state := env.Get("SOHO_DIR")
		for _, ws := range []string{"ws-a", "ws-b"} {
			tm6Write(t, filepath.Join(state, ws, "agents.tsv"), "# header\nbuild\tp1\tgrok\timplementer\n")
		}
		code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
		if code != 0 || stderr != "" || !strings.Contains(out, "more than one Herdr workspace") || strings.Contains(out, "Nothing is running") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run(`parity: explain with two header-only rosters answers with the idle paragraph (test-friendly.sh)`, func(t *testing.T) { // JS: "parity: explain with two header-only rosters answers with the idle paragraph (test-friendly.sh)"
		// Mutation captured: counting a header-only roster as live falsely reports multiple workspaces.
		env, cwd := tm6CLIEnv(t)
		env["HERDR_WORKSPACE_ID"] = ""
		state := env.Get("SOHO_DIR")
		for _, ws := range []string{"ws-c", "ws-d"} {
			tm6Write(t, filepath.Join(state, ws, "agents.tsv"), "# name\tpane\n")
		}
		code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
		if code != 0 || stderr != "" || !strings.Contains(out, "Nothing is running yet.") || strings.Contains(out, "more than one Herdr workspace") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	for _, tc := range []struct{ title, golden string }{
		// JS: "parity: doctor --fix without panes dies 2 and leaves the file (test-doctor-fix.sh)"
		{`parity: doctor --fix without panes dies 2 and leaves the file (test-doctor-fix.sh)`, "fix-nopanes"},
		// JS: "parity: doctor on the legacy config warns about the missing panes and the cap (test-doctor-fix.sh)"
		{`parity: doctor on the legacy config warns about the missing panes and the cap (test-doctor-fix.sh)`, "doctor-legacy"},
		// JS: "parity: doctor --fix --panes 3 writes the preset 3 (test-doctor-fix.sh)"
		{`parity: doctor --fix --panes 3 writes the preset 3 (test-doctor-fix.sh)`, "fix3"},
		// JS: "parity: doctor --fix --panes 4 writes the preset 4 (test-doctor-fix.sh)"
		{`parity: doctor --fix --panes 4 writes the preset 4 (test-doctor-fix.sh)`, "fix4"},
		// JS: "parity: doctor --fix --panes 4 on a divergent review lane keeps the per-role kinds (test-doctor-fix.sh)"
		{`parity: doctor --fix --panes 4 on a divergent review lane keeps the per-role kinds (test-doctor-fix.sh)`, "fix4-divergent"},
		// JS: "parity: doctor warns only about the kinds the effective config uses (test-doctor-fix.sh, controlled PATH)"
		{`parity: doctor warns only about the kinds the effective config uses (test-doctor-fix.sh, controlled PATH)`, "kinds"},
		// JS: "parity: doctor with lanes off counts no planner kind (test-doctor-fix.sh, controlled PATH)"
		{`parity: doctor with lanes off counts no planner kind (test-doctor-fix.sh, controlled PATH)`, "kinds-lanes-off"},
		// JS: "parity: setup --panes / --detect / the config-prompt steps (test-doctor-fix.sh tail, slices 7a/7b)"
		{`parity: setup --panes / --detect / the config-prompt steps (test-doctor-fix.sh tail, slices 7a/7b)`, "setup-tail"},
		// JS: "parity: doctor first-run detection (test-friendly.sh)"
		{`parity: doctor first-run detection (test-friendly.sh)`, "first-run"},
	} {
		t.Run(tc.title, func(t *testing.T) { // JS: parity-doctor.test.mjs golden scenario; exact title is the subtest name.
			// Mutation captured: changing Go output or file effects from the approved JS result fails the golden comparison.
			if tc.golden == "setup-tail" || tc.golden == "kinds-lanes-off" {
				// Mutation captured: inheriting TMPDIR exposes ambient Codex/Cursor model caches in setup --detect.
				ambientTmp := t.TempDir()
				for kind, models := range map[string]string{
					"codex":  "ambient-codex-model",
					"cursor": "ambient-cursor-model",
				} {
					if err := os.WriteFile(filepath.Join(ambientTmp, "soho-models-"+kind+".txt"), []byte(models+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("TMPDIR", ambientTmp)
			}
			goldens := tm6LoadDoctorGoldens(t)
			golden, ok := goldens[tc.golden]
			if !ok {
				t.Fatalf("missing golden scenario %q", tc.golden)
			}
			env, cwd := tm6CLIEnv(t)
			root := filepath.Dir(cwd)
			conf := filepath.Join(cwd, ".agents", "soho.conf")
			// golden.Conf is the file after the last step; first-run starts with no
			// project file (the steps write and remove it), as the JS scenario does.
			if golden.Conf != nil && tc.golden != "first-run" {
				seed := *golden.Conf
				switch tc.golden {
				case "fix3", "fix4", "setup-tail":
					seed = tm6LegacyConfig
				case "fix4-divergent":
					seed = tm6DivergentConfig
				case "kinds":
					seed = tm6LegacyConfig
				}
				tm6Write(t, conf, seed)
			}
			if tc.golden == "kinds" || tc.golden == "kinds-lanes-off" || tc.golden == "setup-tail" {
				bin := env.Get("PATH")
				_, err := fakecli.Install(t, bin, "grok", []fakecli.Rule{{Argv: []string{"models"}, Stdout: "grok-4.7\ngrok-4\ngrok-3\n"}})
				if err != nil {
					t.Fatal(err)
				}
				env = withFakeCLI(env, bin)
			}
			if tc.golden == "setup-tail" {
				bin := env.Get("PATH")
				if _, err := fakecli.Install(t, bin, "git", []fakecli.Rule{{AnyArgs: true}}); err != nil {
					t.Fatal(err)
				}
				env = withFakeCLI(env, bin)
				env["SOHO_MODELS_TIMEOUT"] = "30"
			}
			for i, want := range golden.Steps {
				stepEnv := env.Clone()
				for key, value := range want.Env {
					stepEnv[key] = value
				}
				if tc.golden == "kinds" && i == 1 {
					tm6Write(t, conf, "role.implementer.kind=codex\nrole.designer.kind=codex\nrole.tasker.kind=codex\nrole.scouter.kind=codex\nrole.researcher.kind=codex\nrole.reviewer.kind=codex\nrole.security-reviewer.kind=codex\nrole.ui-reviewer.kind=codex\nrole.inspector.kind=codex\n")
				}
				if tc.golden == "first-run" && i == 1 {
					tm6Write(t, filepath.Join(stepEnv.Get("SOHO_DIR"), "ws", "agents.tsv"), "# header\n")
				}
				if tc.golden == "first-run" && i == 2 {
					tm6Write(t, conf, "lane.build.kind=grok\n")
				}
				if tc.golden == "first-run" && i == 3 {
					_ = os.Remove(conf)
					tm6Write(t, filepath.Join(stepEnv.Get("SOHO_DIR"), "ws", "agents.tsv"), "# header\nbuild\tp1\tgrok\timplementer\n")
				}
				if tc.golden == "first-run" && i == 4 {
					tm6Write(t, conf, "# note\nmax_workers=3\n")
					tm6Write(t, filepath.Join(stepEnv.Get("SOHO_DIR"), "ws", "agents.tsv"), "# header\n")
				}
				if tc.golden == "setup-tail" && i == 2 {
					tm6Write(t, conf, "max_workers=3\n")
				}
				if tc.golden == "setup-tail" && i == 3 {
					tm6Write(t, conf, "lane.build.kind=grok\n")
				}
				code, out, errOut := runIn(t, want.Args, stepEnv, cwd)
				program, programErr := os.Executable()
				if programErr != nil {
					t.Fatal(programErr)
				}
				out = strings.ReplaceAll(out, program, "PROG")
				out = normalizeGoldenRoot(out, root)
				outLines := strings.Split(out, "\n")
				for i, line := range outLines {
					if strings.HasPrefix(line, "--- a/") {
						outLines[i] = "--- FIXDIFF"
					} else if strings.HasPrefix(line, "+++ b/") {
						outLines[i] = "+++ FIXDIFF"
					}
				}
				out = strings.Join(outLines, "\n")
				out = normalizeGoldenRootPathSeparators(out)
				errOut = strings.ReplaceAll(errOut, program, "PROG")
				errOut = strings.ReplaceAll(errOut, "herdr-soho:", "PROG:")
				// The native binary now carries the soho prefix; the frozen
				// goldens normalize it to PROG: like the legacy prefixes
				// (after the legacy ones, so a herdr-soho: prefix is not
				// rewritten to herdr-PROG:).
				errOut = strings.ReplaceAll(errOut, "soho:", "PROG:")
				errOut = normalizeGoldenRoot(errOut, root)
				errOut = normalizeGoldenRootPathSeparators(errOut)
				// The Go doctor measures split_max_panes against the whole team,
				// 1 + max_workers (PLAN §9 A9); the frozen JS golden says panes. In
				// these strict-default fixtures the team is panes-1 workers.
				wantOut := tm6SplitCapAgainstTeam(want.Out)
				// The frozen golden predates the shipped job-orchestrator
				// role; the exact-insertion role oracle adapts the expected
				// role_kinds entry of the setup --detect output.
				wantOut = testutil.ApplyRoleOracleToDetectDocument(wantOut)
				if tc.golden == "kinds-lanes-off" {
					// With lanes=off the shipped job-orchestrator role adds its
					// kind to "kinds in use"; see role_oracle.go.
					wantOut = testutil.ApplyRoleOracleToLanesOffDoctor(wantOut)
				}
				// The frozen golden predates the herdr-soho -> soho rename; the
				// exact-substitution name oracle adapts the expected names, and the
				// DJA-318 rename oracle (outermost) adapts the setup block, the hook
				// literals and the doctor advisory lines to the soho contract.
				wantOut = testutil.ApplySohoRenameOracle(testutil.ApplyHookOracle(testutil.RewriteSohoName(wantOut))).(string)
				wantOut = testutil.RewriteApprovalsAutoDoctor(wantOut)
				wantErr := testutil.ApplySohoRenameOracle(testutil.ApplyHookOracle(testutil.RewriteSohoName(want.Err))).(string)
				if code != want.RC || out != wantOut || errOut != wantErr {
					want.Out = wantOut
					t.Fatalf("golden %s step %d args=%v\ncode=%d want=%d\nstdout difference: %s\nstderr difference: %s\nstdout=%q\nwant=%q\nstderr=%q\nwant=%q", tc.golden, i, want.Args, code, want.RC, firstGoldenLineDifference(out, want.Out), firstGoldenLineDifference(errOut, wantErr), out, want.Out, errOut, wantErr)
				}
			}
		})
	}
}

func tm6LoadDoctorGoldens(t *testing.T) map[string]tm6GoldenScenario {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "legacy", "parity-doctor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]tm6GoldenScenario
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDoctorTM6FixTargets(t *testing.T) {
	t.Run(`doctorFix: the preset 3 and 4 rewrites (no frozen roles or limits, planner dropped)`, func(t *testing.T) { // JS: "doctorFix: the preset 3 and 4 rewrites (no frozen roles or limits, planner dropped)"
		// Mutation captured: retaining role or capacity overrides in a preset file freezes choices the fix is meant to release.
		env, cwd := tm6CLIEnv(t)
		for _, panes := range []string{"3", "4"} {
			t.Run(panes, func(t *testing.T) {
				tm6Write(t, filepath.Join(cwd, ".agents", "soho.conf"), tm6FixInput)
				code, out, stderr := runIn(t, []string{"doctor", "--fix", "--panes", panes}, env, cwd)
				data, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
				if err != nil {
					t.Fatal(err)
				}
				if code != 0 || stderr != "" || !strings.Contains(out, "set panes="+panes) || strings.Contains(string(data), "role.planner") || strings.Contains(string(data), "max_workers") || strings.Contains(string(data), "split_max_panes") {
					t.Fatalf("code=%d out=%q stderr=%q conf=%q", code, out, stderr, data)
				}
			})
		}
	})
	t.Run(`doctorFix: a divergent review lane keeps the per-role kinds and warns on stderr`, func(t *testing.T) { // JS: "doctorFix: a divergent review lane keeps the per-role kinds and warns on stderr"
		// Mutation captured: forcing a shared lane kind would discard user-selected divergent role kinds.
		env, cwd := tm6CLIEnv(t)
		tm6Write(t, filepath.Join(cwd, ".agents", "soho.conf"), "role.reviewer.kind=codex\nrole.security-reviewer.kind=claude\n")
		code, out, stderr := runIn(t, []string{"doctor", "--fix", "--panes", "4"}, env, cwd)
		data, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || !strings.Contains(stderr, "lane 'review' kinds differ") || !strings.Contains(string(data), "role.reviewer.kind=codex") || !strings.Contains(string(data), "role.security-reviewer.kind=claude") {
			t.Fatalf("code=%d out=%q stderr=%q conf=%q", code, out, stderr, data)
		}
	})
	t.Run(`doctorFix: an orphan lane args key is removed like the other orphan lane keys`, func(t *testing.T) { // JS: "doctorFix: an orphan lane args key is removed like the other orphan lane keys"
		// Mutation captured: omitting args from orphan cleanup leaves an inactive configuration key behind.
		env, cwd := tm6CLIEnv(t)
		conf := filepath.Join(cwd, ".agents", "soho.conf")
		tm6Write(t, conf, "lane.explore.args=--extra\npanes=4\n")
		code, out, stderr := runIn(t, []string{"doctor", "--fix", "--panes", "4"}, env, cwd)
		data, err := os.ReadFile(conf)
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || strings.Contains(string(data), "lane.explore.args") || !strings.Contains(out, "removed lane.explore.args") {
			t.Fatalf("code=%d out=%q stderr=%q conf=%q", code, out, stderr, data)
		}
	})
	t.Run(`doctorFix: --user targets the user file`, func(t *testing.T) { // JS: "doctorFix: --user targets the user file"
		// Mutation captured: writing --user into project config changes the wrong configuration layer.
		env, cwd := tm6CLIEnv(t)
		user := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
		tm6Write(t, user, "panes=3\n")
		code, out, stderr := runIn(t, []string{"doctor", "--fix", "--user", "--panes", "4"}, env, cwd)
		data, err := os.ReadFile(user)
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || !strings.Contains(string(data), "panes=4") || !strings.Contains(out, "updated "+user) {
			t.Fatalf("code=%d out=%q stderr=%q user=%q", code, out, stderr, data)
		}
		if _, err := os.Stat(filepath.Join(cwd, ".agents", "soho.conf")); !os.IsNotExist(err) {
			t.Fatalf("project config was written: %v", err)
		}
	})
	t.Run(`doctorFix: --session normalizes the workspace session.conf like the other files`, func(t *testing.T) { // JS: "doctorFix: --session normalizes the workspace session.conf like the other files"
		// Mutation captured: targeting project config instead of the workspace session file leaves session overrides unnormalized.
		env, cwd := tm6CLIEnv(t)
		path := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
		tm6Write(t, path, "panes=3\nrole.planner.model=fable\n")
		code, out, stderr := runIn(t, []string{"doctor", "--fix", "--session", "--panes", "4"}, env, cwd)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || !strings.Contains(string(data), "panes=4") || strings.Contains(string(data), "role.planner") {
			t.Fatalf("code=%d out=%q stderr=%q conf=%q", code, out, stderr, data)
		}
	})
	t.Run(`cmdDoctor: --fix --session rewrites the session.conf and re-runs the check`, func(t *testing.T) { // JS: "cmdDoctor: --fix --session rewrites the session.conf and re-runs the check"
		// Mutation captured: returning after a session fix omits the fresh doctor result from command output.
		env, cwd := tm6CLIEnv(t)
		env["HERDR_WORKSPACE_ID"] = "ws"
		path := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
		tm6Write(t, path, "panes=3\n")
		code, out, stderr := runIn(t, []string{"doctor", "--fix", "--session", "--panes", "4"}, env, cwd)
		if code != 0 || !strings.Contains(out, "doctor --fix: updated") || !strings.Contains(out, "first_run:") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

var tm6SplitCapGreaterRE = regexp.MustCompile(`split_max_panes=(\d+) is greater than panes=(\d+)\. Set split_max_panes=(\d+) `)

func tm6SplitCapAgainstTeam(out string) string {
	return tm6SplitCapGreaterRE.ReplaceAllStringFunc(out, func(m string) string {
		g := tm6SplitCapGreaterRE.FindStringSubmatch(m)
		panes, _ := strconv.Atoi(g[2])
		return fmt.Sprintf("split_max_panes=%s is greater than 1 + max_workers=%d. Set split_max_panes=%s ", g[1], panes-1, g[3])
	})
}
