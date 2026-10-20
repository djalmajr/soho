package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type setupTM5GoldenStep struct {
	Args []string `json:"args"`
	Err  string   `json:"err"`
	Out  string   `json:"out"`
	RC   int      `json:"rc"`
}

type setupTM5Golden struct {
	Files []struct {
		Rel     string  `json:"rel"`
		Content *string `json:"content"`
	} `json:"files"`
	StateWS string               `json:"stateWs"`
	Steps   []setupTM5GoldenStep `json:"steps"`
}

func runSetupTM5InitGolden(t *testing.T, name string, seedConfig bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "legacy", "parity-setup-probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var goldens map[string]setupTM5Golden
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatal(err)
	}
	want, ok := goldens[name]
	if !ok {
		t.Fatalf("golden %q missing", name)
	}
	// The doctor step runs with HERDR_ENV=1, no HERDR_PANE_ID and no herdr on PATH:
	// the Go doctor reports that context as invalid (PLAN §9 A2), where the frozen
	// JS golden still says inside Herdr.
	for i := range want.Steps {
		// The frozen golden predates the soho name layer; the exact-substitution
		// name oracle adapts the expected output and error, and the DJA-318
		// rename oracle adapts the herdr-soho → soho generation change.
		want.Steps[i].Out = testutil.ApplySohoRenameOracle(testutil.ApplyHookOracle(testutil.RewriteSohoName(want.Steps[i].Out))).(string)
		want.Steps[i].Err = testutil.ApplySohoRenameOracle(testutil.ApplyHookOracle(testutil.RewriteSohoName(want.Steps[i].Err))).(string)
		want.Steps[i].Out = testutil.RewriteApprovalsAutoDoctor(want.Steps[i].Out)
		want.Steps[i].Err = testutil.RewriteApprovalsAutoDoctor(want.Steps[i].Err)
		if strings.Join(want.Steps[i].Args, " ") == "doctor" {
			out := want.Steps[i].Out
			if !strings.Contains(out, "ok     inside Herdr (HERDR_ENV=1)\n") {
				t.Fatalf("golden %q no longer has the inside-Herdr line", name)
			}
			out = strings.Replace(out, "ok     inside Herdr (HERDR_ENV=1)\n", "warn   Herdr context invalid: HERDR_PANE_ID is unset and herdr pane current failed (herdr CLI not found in PATH)\n", 1)
			// The summary line moves one ok to the warnings.
			out = regexp.MustCompile(`(?m)^(\d+) ok, (\d+) warning\(s\)$`).ReplaceAllStringFunc(out, func(line string) string {
				var ok, warn int
				fmt.Sscanf(line, "%d ok, %d warning(s)", &ok, &warn)
				return fmt.Sprintf("%d ok, %d warning(s)", ok-1, warn+1)
			})
			want.Steps[i].Out = out
		}
	}
	env, repo := setupTM5CommandFixture(t)
	root := filepath.Dir(repo)
	skillDir := setupTM5SkillDir(t)
	env["USERPROFILE"] = env.Get("HOME")
	env["HERDR_ENV"] = "1"
	env["SOHO_SKILL_DIR"] = skillDir
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", []fakecli.Rule{{Argv: []string{"--version"}, Stdout: "herdr 9.9.9\n"}, {AnyArgs: true, Stdout: "herdr 9.9.9\n"}}); err != nil {
		t.Fatal(err)
	}
	withHerdr := withFakeCLI(env, fakeDir)
	withHerdr["SOHO_SKILL_DIR"] = skillDir
	if seedConfig {
		conf := filepath.Join(repo, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(conf, []byte("lane.build.kind=grok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var gotSteps []setupTM5GoldenStep
	for i, args := range [][]string{{"doctor"}, {"init"}} {
		stepEnv := env
		if i == 1 {
			stepEnv = withHerdr
		}
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		rc := Run(args, stepEnv)
		stdoutText := normalizeGoldenRoot(out.String(), root)
		stderrText := normalizeGoldenRoot(stderr.String(), root)
		program := filepath.Join(skillDir, "scripts", "herdr-soho")
		if platform.Current() == "win32" {
			program += ".cmd"
		}
		// The setup guidance now names the native executable (platform.LauncherPath
		// of the running test binary, no SOHO_BIN override), not the
		// obsolete scripts launcher; both map to the golden's PROG token.
		launcher := platform.LauncherPath(env)
		stdoutText = strings.ReplaceAll(stdoutText, launcher, "PROG")
		stderrText = strings.ReplaceAll(stderrText, launcher, "PROG")
		stdoutText = strings.ReplaceAll(stdoutText, program, "PROG")
		stderrText = strings.ReplaceAll(stderrText, program, "PROG")
		stdoutText = normalizeGoldenRootPathSeparators(stdoutText)
		stderrText = normalizeGoldenRootPathSeparators(stderrText)
		stderrText = strings.ReplaceAll(stderrText, "soho: ", "PROG: ")
		gotSteps = append(gotSteps, setupTM5GoldenStep{Args: args, RC: rc, Out: stdoutText, Err: stderrText})
	}
	platform.Stdout, platform.Stderr = oldOut, oldErr
	if !equalSetupTM5GoldenSteps(gotSteps, want.Steps) {
		got, _ := json.MarshalIndent(gotSteps, "", "  ")
		exp, _ := json.MarshalIndent(want.Steps, "", "  ")
		for i := range min(len(gotSteps), len(want.Steps)) {
			if gotSteps[i].RC != want.Steps[i].RC || gotSteps[i].Out != want.Steps[i].Out || gotSteps[i].Err != want.Steps[i].Err || strings.Join(gotSteps[i].Args, "\x00") != strings.Join(want.Steps[i].Args, "\x00") {
				t.Fatalf("steps differ at index %d: stdout %s; stderr %s\ngot=%s\nwant=%s", i, firstGoldenLineDifference(gotSteps[i].Out, want.Steps[i].Out), firstGoldenLineDifference(gotSteps[i].Err, want.Steps[i].Err), got, exp)
			}
		}
		t.Fatalf("steps differ\ngot=%s\nwant=%s", got, exp)
	}
	state := filepath.Join(root, "state", "ws", "agents.tsv")
	content, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Files) != 1 || want.Files[0].Rel != "state/ws/agents.tsv" || want.Files[0].Content == nil || string(content) != *want.Files[0].Content {
		t.Fatalf("state file=%q golden=%+v", content, want.Files)
	}
	if want.StateWS != "<ROOT>/state/ws" {
		t.Fatalf("golden stateWs=%q", want.StateWS)
	}
}

func setupTM5CommandFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for setup TM5 CLI fixtures")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	conf := filepath.Join(root, "conf")
	state := filepath.Join(root, "state")
	for _, dir := range []string{repo, home, conf, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	init := exec.Command(git, "init", "-q")
	init.Dir = repo
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
	env := platform.Env{}
	for _, item := range testutil.CleanEnv(t) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	for key := range env {
		if key == "SOHO_FAKECLI_CONFIG" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(key), "HERDR_") || strings.HasPrefix(key, "SOHO_") {
			delete(env, key)
		}
	}
	env["HOME"], env["USERPROFILE"] = home, home
	env["XDG_CONFIG_HOME"], env["SOHO_DIR"] = conf, state
	env["SOHO_SKILL_DIR"] = setupTM5SkillDir(t)
	env["HERDR_WORKSPACE_ID"], env["PATH"] = "ws", ""
	return env, repo
}

func setupTM5SkillDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("skill directory %q: %v", dir, err)
	}
	return dir
}

func equalSetupTM5GoldenSteps(a, b []setupTM5GoldenStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].RC != b[i].RC || a[i].Out != b[i].Out || a[i].Err != b[i].Err || strings.Join(a[i].Args, "\x00") != strings.Join(b[i].Args, "\x00") {
			return false
		}
	}
	return true
}

func TestSetupTM5InitParityGoldens(t *testing.T) {
	t.Run(`// JS: "parity: init — doctor on stderr, the JSON context, first_run true"`, func(t *testing.T) { runSetupTM5InitGolden(t, "init-first", false) })
	t.Run(`// JS: "parity: init with a project config that makes the team choice — first_run false"`, func(t *testing.T) { runSetupTM5InitGolden(t, "init-config", true) })
}

func TestSetupTM5LocalDoctorCases(t *testing.T) {
	t.Run(`// JS: "doctor: a valid local block is enough; setup_target=local without one points at setup --local"`, func(t *testing.T) {
		env, repo := setupTM5CommandFixture(t)
		env["HERDR_ENV"] = "1"
		env["SOHO_SKILL_DIR"] = setupTM5SkillDir(t)
		old, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(repo); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })
		conf := filepath.Join(repo, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(conf, []byte("setup_target=local\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := func() string {
			t.Helper()
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			if code := Run([]string{"doctor"}, env); code != 0 || stderr.Len() != 0 {
				t.Fatalf("doctor code=%d stderr=%q", code, stderr.String())
			}
			return out.String()
		}
		missing := run()
		if !strings.Contains(missing, "no soho block in CLAUDE.local.md") || !strings.Contains(missing, "setup --local") || strings.Contains(missing, "no soho block in AGENTS.md/CLAUDE.md") {
			t.Fatalf("local setup target warning=%s", missing)
		}
		if err := os.WriteFile(filepath.Join(repo, "CLAUDE.local.md"), []byte("<!-- soho:start -->\n<!-- soho:end -->\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		valid := run()
		if !strings.Contains(valid, "instruction block present in CLAUDE.local.md") || strings.Contains(valid, "no soho block") {
			t.Fatalf("valid local block status=%s", valid)
		}
	})
	t.Run(`// JS: "doctor: missing hooks point at setup --local for local setups, canonical otherwise (exact text, no tracked mutation)"`, func(t *testing.T) {
		env, repo := setupTM5CommandFixture(t)
		env["HERDR_ENV"] = "1"
		env["SOHO_SKILL_DIR"] = setupTM5SkillDir(t)
		old, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(repo); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })
		tracked := filepath.Join(repo, "CLAUDE.md")
		if err := os.WriteFile(tracked, []byte("upstream\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		local := filepath.Join(repo, "CLAUDE.local.md")
		if err := os.WriteFile(local, []byte("<!-- soho:start -->\n<!-- soho:end -->\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		conf := filepath.Join(repo, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(conf, []byte("setup_target=local\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		beforeTracked, _ := os.ReadFile(tracked)
		// The setup guidance names the native executable (the running test
		// binary; no SOHO_BIN override in this fixture env), not the
		// obsolete skill scripts launcher.
		entry := platform.LauncherPath(env)
		run := func() string {
			t.Helper()
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			code := Run([]string{"doctor"}, env)
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("doctor code=%d stderr=%q", code, stderr.String())
			}
			return out.String()
		}
		localOutput := run()
		localWant := "warn   no soho hooks in .claude/settings.json: run '" + entry + " setup --local' (UserPromptSubmit reminder + SessionStart doctor)"
		if !strings.Contains(normalizeGoldenRootPathSeparators(localOutput), normalizeGoldenRootPathSeparators(localWant)) {
			t.Fatalf("missing local hooks warning %q in %s", localWant, localOutput)
		}
		if err := os.WriteFile(conf, []byte("# canonical default\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(local); err != nil {
			t.Fatal(err)
		}
		canonicalOutput := run()
		canonicalWant := "warn   no soho hooks in .claude/settings.json: run '" + entry + " setup' (UserPromptSubmit reminder + SessionStart doctor)"
		if !strings.Contains(normalizeGoldenRootPathSeparators(canonicalOutput), normalizeGoldenRootPathSeparators(canonicalWant)) {
			t.Fatalf("missing canonical hooks warning %q in %s", canonicalWant, canonicalOutput)
		}
		afterTracked, _ := os.ReadFile(tracked)
		if !bytes.Equal(beforeTracked, afterTracked) {
			t.Fatalf("doctor mutated tracked instruction: before=%q after=%q", beforeTracked, afterTracked)
		}
	})
}
