package core

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) {
	fakecli.RunTests(m)
}

func TestResolveApprovalConfigFallbackAutoAndExplicitAsk(t *testing.T) {
	env, cwd := fixture(t)
	env["SOHO_SKILL_DIR"], env["SOHO_ROLES"], env["SOHO_LANES"] = t.TempDir(), t.TempDir(), "off"
	ctx := &Config{}
	got := ResolveRoleSettings("missing", ctx, env, cwd, RoleFlags{})
	if got.Approvals != "auto" || got.ApprovalsFrom != "default" {
		t.Fatalf("fallback: %+v", got)
	}
	env["SOHO_APPROVALS"] = "ask"
	got = ResolveRoleSettings("missing", ctx, env, cwd, RoleFlags{})
	if got.Approvals != "ask" || !strings.Contains(got.ApprovalsFrom, "approvals (") {
		t.Fatalf("explicit ask: %+v", got)
	}
}

func fixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	root := t.TempDir()
	// Canonicalize the existing temporary root before deriving any
	// fixture path: the platform's git and filepath.EvalSymlinks report
	// the canonical form (on Windows the long path behind the short 8.3
	// alias), while t.TempDir can hand back the short alias; one shared
	// form keeps expected and actual paths comparable on every platform.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize the fixture root: %v", err)
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
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
	skillDir := testSkillDir(t)
	env := platform.Env{
		"HOME": home, "XDG_CONFIG_HOME": conf, "SOHO_DIR": state,
		"SOHO_SKILL_DIR": skillDir, "HERDR_WORKSPACE_ID": "ws",
		// Restricted PATH: the production code under test resolves git
		// from the environment, and nothing else; a directory holding
		// only the system git keeps the env hermetic (no shell, no
		// interpreter, no sleep resolvable).
		"PATH": gitOnlyDir(t),
	}
	return env, repo
}

// gitOnlyDir returns a directory holding only a link to the system git:
// the restricted PATH tail the fixture envs carry (git is a known native
// binary the production root/ignore paths resolve; nothing else is
// resolvable on the PATH). The link is staged under the platform-native
// name: on Windows FindExecutable only tries the PATHEXT extensions (the
// bare name is never attempted), so the fixture must hold git.exe there,
// and on Unix it holds git. The FindExecutable PATHEXT contract is left
// unchanged; only the staged name follows it.
func gitOnlyDir(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate the system git: %v", err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	dir := t.TempDir()
	target := filepath.Join(dir, name)
	if err := os.Symlink(path, target); err != nil {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading the system git: %v", readErr)
		}
		if writeErr := os.WriteFile(target, data, 0o755); writeErr != nil {
			t.Fatalf("copying the system git: %v", writeErr)
		}
	}
	return dir
}

func testSkillDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillDir := filepath.Join(root, "skills", "soho")
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
		return skillDir
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	skillDir = filepath.Clean(filepath.Join(cwd, "..", "..", "skills", "soho"))
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("skill directory %q: %v", skillDir, err)
	}
	return skillDir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func capture(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	fn()
	return out.String(), errOut.String()
}

func TestLoadConfigPrecedence(t *testing.T) {
	// JS: "precedence: session > project, env > session"
	env, cwd := fixture(t)
	user := platform.UserConfigPath(platform.Current(), env)
	project := filepath.Join(cwd, ".agents", "soho.conf")
	session := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
	write(t, user, "layout=columns\nmax_workers=1\n")
	write(t, project, "layout=split\nmax_workers=2\n")
	write(t, session, "max_workers=3\n")
	ctx := LoadConfig(env, cwd)
	if got := Cfg(&ctx, "layout", "", env); got != "split" {
		t.Fatalf("project over user = %q", got)
	}
	if got := Cfg(&ctx, "max_workers", "", env); got != "3" || CfgSource(&ctx, "max_workers", env) != "session" {
		t.Fatalf("session = %q from %q", got, CfgSource(&ctx, "max_workers", env))
	}
	env["SOHO_MAX_WORKERS"] = "4"
	if got := Cfg(&ctx, "max_workers", "", env); got != "4" || CfgSource(&ctx, "max_workers", env) != "env" {
		t.Fatalf("env = %q from %q", got, CfgSource(&ctx, "max_workers", env))
	}
	if strings.Join(ctx.Sources, ",") != "defaults,user,project,session" {
		t.Fatalf("sources = %#v", ctx.Sources)
	}
}

func TestNormalizeKey(t *testing.T) {
	// JS: "key normalization matches the bash sed/tr pipeline"
	for _, tc := range []struct{ in, want string }{
		{"role.security-reviewer.kind", "role_security_reviewer_kind"},
		{" a\t-b.c !", "a_b_c"},
		{"x\u00a0y\ufeffz", "xyz"},
		{"é😀_OK", "_OK"},
	} {
		if got := NormalizeKey(tc.in); got != tc.want {
			t.Errorf("NormalizeKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	// JS: "configKeyOk: scalar plus dotted patterns"
	// JS: "configValueOk: enums, ladders and role resolution"
	env, cwd := fixture(t)
	for _, key := range []string{"layout", "role.reviewer.kind", "lane.build.roles", "model.codex.worker", "args.codex"} {
		if !ConfigKeyOk(key) {
			t.Errorf("known key rejected: %s", key)
		}
	}
	for _, key := range []string{"nope", "role.Reviewer.kind", "lane.build.unknown", "args."} {
		if ConfigKeyOk(key) {
			t.Errorf("unknown key accepted: %s", key)
		}
	}
	for _, tc := range []struct {
		key, value string
		want       bool
	}{
		{"max_workers", "0", true}, {"max_workers", "-1", false},
		{"multi_role", "off", true}, {"multi_role", "yes", false},
		{"role.reviewer.kind", "pi", true}, {"role.reviewer.kind", "notepad", false},
		{"approvals", "edits", true}, {"approvals", "maybe", false},
		{"args.codex", "-c a=b", true}, {"args.codex", "a#b", false},
		{"flex_roles", "reviewer", true}, {"flex_roles", "not-a-real-role", false},
	} {
		if got := ConfigValueOk(tc.key, tc.value, env, cwd); got != tc.want {
			t.Errorf("ConfigValueOk(%q,%q) = %t, want %t", tc.key, tc.value, got, tc.want)
		}
	}
}

func TestConfigSurvivingMutationInputs(t *testing.T) {
	// Mutation captured: empty env values keep the stored source, and malformed or out-of-range input stays harmless and invalid.
	env, cwd := fixture(t)
	write(t, filepath.Join(cwd, ".agents", "soho.conf"), "panes=3\nlayout=\"\nfeedback=ask\u00a0\ninbound=off\ufeff\n")
	env["SOHO_PANES"] = ""
	ctx := LoadConfig(env, cwd)
	if got := CfgSource(&ctx, "panes", env); got != "project" {
		t.Fatalf("empty env source=%q, want project", got)
	}
	if got := Cfg(&ctx, "panes", "", env); got != "3" {
		t.Fatalf("empty env value=%q, want project value", got)
	}
	if got := Cfg(&ctx, "layout", "", env); got != `"` {
		t.Fatalf("single quote value=%q, want a literal quote", got)
	}
	if got := Cfg(&ctx, "feedback", "", env); got != "ask" {
		t.Fatalf("NBSP-trimmed value=%q, want ask", got)
	}
	if got := Cfg(&ctx, "inbound", "", env); got != "off" {
		t.Fatalf("BOM-trimmed value=%q, want off", got)
	}
	for _, tc := range []struct{ key, value string }{
		{"panes", "1"},
		{"stuck_warn_minutes", "abc"},
		{"lane.build.panes", "0"},
		{"lane.build.kind", "not-a-kind"},
		{"lane.build.effort", "unbounded"},
	} {
		if ConfigValueOk(tc.key, tc.value, env, cwd) {
			t.Errorf("ConfigValueOk(%q, %q) accepted invalid input", tc.key, tc.value)
		}
	}
}

func TestConfigRolesTrimWhitespaceAroundNames(t *testing.T) {
	// Mutation captured: role CSV items are trimmed before role resolution.
	env, cwd := fixture(t)
	roles := filepath.Join(t.TempDir(), "roles")
	write(t, filepath.Join(roles, "reviewer.md"), "role\n")
	env["SOHO_ROLES"] = roles
	if !ConfigValueOk("lane.build.roles", " reviewer ", env, cwd) {
		t.Fatal("valid role surrounded by whitespace was rejected")
	}
}

func TestConfigFileParsingAndEmptySource(t *testing.T) {
	// JS: "a CRLF config file loads the same as the same file with LF (decision 7)"
	// JS: "empty file values fall back but keep their layer as source"
	// JS: "surrounding double quotes are stripped once (no escape handling)"
	env, cwd := fixture(t)
	file := filepath.Join(cwd, ".agents", "soho.conf")
	write(t, file, "layout=\"\" # empty\r\nmax_workers=2\r\nmax_workers=3\r\nmodel.claude.worker=\"a\\\\b\"\r\n")
	ctx := LoadConfig(env, cwd)
	if got := Cfg(&ctx, "layout", "split", env); got != "split" || CfgSource(&ctx, "layout", env) != "project" {
		t.Fatalf("empty value got=%q source=%q", got, CfgSource(&ctx, "layout", env))
	}
	if got := Cfg(&ctx, "max_workers", "", env); got != "3" {
		t.Fatalf("last assignment = %q", got)
	}
	if got := Cfg(&ctx, "model_claude_worker", "", env); got != `a\\b` {
		t.Fatalf("quoted value = %q", got)
	}
}

func TestLegacyPaths(t *testing.T) {
	env, cwd := fixture(t)
	root := platform.ProjectRoot(env, cwd)
	legacy := filepath.Join(root, ".herdr-soho")
	if got := LegacyProjectConfigPath(root); got != filepath.Join(root, ".agents", "herdr-soho.conf") {
		t.Fatalf("legacy project config = %q", got)
	}
	if got := LegacyStatePath(root); got != legacy {
		t.Fatalf("legacy state path = %q", got)
	}
}

func TestDottedKeyNameRestoresKnownRoleAndLaneSpellings(t *testing.T) {
	// JS: "config prints the dotted keys as written in the files, rebuilt for env-only keys"
	env, cwd := fixture(t)
	roles := filepath.Join(t.TempDir(), "roles")
	write(t, filepath.Join(roles, "build-ui_v2.md"), "role\n")
	env["SOHO_ROLES"] = roles
	write(t, filepath.Join(cwd, ".agents", "soho.conf"), "lane.build-ui_v2.roles=reviewer\n")
	ctx := LoadConfig(env, cwd)
	for key, want := range map[string]string{
		"role_build_ui_v2_effort": "role.build-ui_v2.effort",
		"lane_build_ui_v2_model":  "lane.build-ui_v2.model",
		"model_codex_worker":      "model.codex.worker",
		"args_codex":              "args.codex",
	} {
		if got := DottedKeyName(key, &ctx, env, cwd); got != want {
			t.Errorf("DottedKeyName(%q)=%q want %q", key, got, want)
		}
	}
}

func TestConfigWritePair(t *testing.T) {
	// JS: "config set preserves whole-line and trailing comments, collapses duplicates"
	// JS: "config set appends a missing key and does not duplicate existing ones"
	env, cwd := fixture(t)
	file := filepath.Join(cwd, ".agents", "soho.conf")
	write(t, file, "# keep this comment\nmax_workers=3 # live cap\n# tail comment\nreuse_workers=on\n\nmax_workers=1\n")
	ConfigWritePair(file, "max_workers", "5", env, cwd)
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := "# keep this comment\nmax_workers=5 # live cap\n# tail comment\nreuse_workers=on\n\n"
	if string(content) != want {
		t.Fatalf("rewrite = %q, want %q", content, want)
	}
	ConfigWritePair(file, "multi_role", "on", env, cwd)
	content, _ = os.ReadFile(file)
	if !strings.Contains(string(content), "multi_role=on\n") || strings.Count(string(content), "max_workers=") != 1 {
		t.Fatalf("append/collapse result = %q", content)
	}
}

func TestConfigWritePairJavaScriptLineTerminatorsInComments(t *testing.T) {
	// Mutation captured: treating CR, LS, or PS as part of a trailing comment preserves bytes JS discards.
	for _, tc := range []struct{ name, comment string }{
		{"cr", " #a\rb"}, {"line-separator", " # a\u2028b"}, {"paragraph-separator", " # a\u2029b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd := fixture(t)
			file := filepath.Join(cwd, ".agents", "soho.conf")
			write(t, file, "panes=2"+tc.comment+"\nlayout=tab\n")
			ConfigWritePair(file, "panes", "3", env, cwd)
			got, err := os.ReadFile(file)
			if err != nil || string(got) != "panes=3\nlayout=tab\n" {
				t.Fatalf("rewritten bytes=%q err=%v", got, err)
			}
		})
	}
}

func TestConfigReadAndClearJavaScriptLineTerminatorsInComments(t *testing.T) {
	// Mutation captured: treating JavaScript line terminators as comment text changes parsed values and clear behavior.
	for _, tc := range []struct{ name, comment string }{
		{"cr", " #a\rb"}, {"line-separator", " # a\u2028b"}, {"paragraph-separator", " # a\u2029b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cwd := fixture(t)
			file := filepath.Join(cwd, "session.conf")
			write(t, file, "panes=2"+tc.comment+"\nlayout=tab\n")
			if got := FileKeyValue(file, "panes"); got != "2"+tc.comment {
				t.Fatalf("parsed value=%q", got)
			}
			ConfigClearKey(file, "panes")
			got, err := os.ReadFile(file)
			if err != nil || string(got) != "layout=tab\n" {
				t.Fatalf("cleared bytes=%q err=%v", got, err)
			}
		})
	}
}

func TestSessionCommands(t *testing.T) {
	// JS: "session set writes the session file in the state dir"
	// JS: "session show lists the session entries"
	// JS: "session clear <key> drops one key only"
	// JS: "session clear (no key) removes the layer; config falls back to project"
	env, cwd := fixture(t)
	ctx := LoadConfig(env, cwd)
	file := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
	out, _ := capture(t, func() { CmdSession([]string{"set", "lane.build.kind=pi"}, &ctx, env, cwd) })
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "lane.build.kind=pi\n" || !strings.Contains(out, "session: this Herdr workspace only") {
		t.Fatalf("set out=%q content=%q err=%v", out, content, err)
	}
	out, _ = capture(t, func() { CmdSession([]string{"show"}, &ctx, env, cwd) })
	if !strings.Contains(out, "lane.build.kind=pi\n\nsession file: "+file) {
		t.Fatalf("show = %q", out)
	}
	_, _ = capture(t, func() { CmdSession([]string{"set", "lane.review.kind", "codex"}, &ctx, env, cwd) })
	_, _ = capture(t, func() { CmdSession([]string{"clear", "lane.build.kind"}, &ctx, env, cwd) })
	content, _ = os.ReadFile(file)
	if strings.Contains(string(content), "lane.build.kind") || !strings.Contains(string(content), "lane.review.kind=codex") {
		t.Fatalf("clear key result = %q", content)
	}
	_, _ = capture(t, func() { CmdSession([]string{"clear"}, &ctx, env, cwd) })
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("clear layer left file: %v", err)
	}
}

func TestSessionWithoutWorkspace(t *testing.T) {
	// JS: "without a resolvable workspace, set refuses; config still works"
	env, cwd := fixture(t)
	delete(env, "HERDR_WORKSPACE_ID")
	ctx := LoadConfig(env, cwd)
	if path := SessionConfPath(&ctx, env, cwd); path != "" {
		t.Fatalf("session path outside workspace = %q", path)
	}
	defer func() {
		value := recover()
		exit, ok := value.(*platform.ExitError)
		if !ok || exit.Code != 2 || !strings.Contains(exit.Msg, "no Herdr workspace") {
			t.Fatalf("session set panic = %#v", value)
		}
	}()
	CmdSessionSet([]string{"lanes=off"}, &ctx, env, cwd)
}

func TestSessionPathUsesHerdrFallback(t *testing.T) {
	// JS: "session path falls back to the workspace herdr reports"
	env, cwd := fixture(t)
	delete(env, "HERDR_WORKSPACE_ID")
	env["HERDR_ENV"] = "1"
	// The shared fakecli herdr answers the workspace query (no shell
	// script behind the name): the re-executed test binary dispatches as
	// the fake on every platform.
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{AnyArgs: true, Stdout: `{"result":{"pane":{"workspace_id":"ws-from-herdr"}}}`}}); err != nil {
		t.Fatal(err)
	}
	env["PATH"] = bin + string(os.PathListSeparator) + env.Get("PATH")
	env["SOHO_FAKECLI_CONFIG"] = bin
	ctx := LoadConfig(env, cwd)
	want := filepath.Join(env.Get("SOHO_DIR"), "ws-from-herdr", "session.conf")
	if got := SessionConfPath(&ctx, env, cwd); got != want {
		t.Fatalf("session path = %q, want %q", got, want)
	}
}

func TestConfigOutputSources(t *testing.T) {
	// JS: "config shows defaults for unset keys (multi_role on defaults)"
	env, cwd := fixture(t)
	ctx := LoadConfig(env, cwd)
	out, _ := capture(t, func() { CmdConfig(&ctx, env, cwd) })
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "multi_role") && !strings.Contains(line, "on") {
			t.Fatalf("multi_role row = %q", line)
		}
	}
	if !strings.Contains(out, "layers read: defaults") || !strings.Contains(out, "session file: "+filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")) {
		t.Fatalf("config output missing source/path:\n%s", out)
	}
}

func TestConfigOutputUsesEffectiveStateAndUTF16Width(t *testing.T) {
	// Mutation captured: config displays the legacy-aware default state directory and pads astral values by UTF-16 units.
	env, cwd := fixture(t)
	delete(env, "SOHO_DIR")
	env["SOHO_ARGS_CODEX"] = "-c a=b"
	env["SOHO_EFFORT_XHIGH"] = ""
	write(t, filepath.Join(cwd, ".agents", "soho.conf"), "model.codex.worker=😀\n")
	if err := os.MkdirAll(filepath.Join(cwd, ".herdr-soho"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := LoadConfig(env, cwd)
	out, _ := capture(t, func() { CmdConfig(&ctx, env, cwd) })
	wantValueRow := "model.codex.worker 😀" + strings.Repeat(" ", 29) + "project"
	if !strings.Contains(out, wantValueRow) {
		t.Fatalf("astral config row missing %q:\n%s", wantValueRow, out)
	}
	if !strings.Contains(out, "state_dir          .soho                          defaults") {
		t.Fatalf("legacy-aware state_dir row missing:\n%s", out)
	}
	if !strings.Contains(out, "args.codex") || strings.Contains(out, "effort.xhigh") {
		t.Fatalf("environment-only dotted keys are wrong:\n%s", out)
	}
}

func TestGitignoreAfter(t *testing.T) {
	// JS: "gitignoreAfter: the entry always lands on a line of its own"
	for _, tc := range []struct{ in, want string }{{"", "state/\n"}, {"x", "x\nstate/\n"}, {"x\n", "x\nstate/\n"}} {
		if got := GitignoreAfter(tc.in, "state"); got != tc.want {
			t.Errorf("GitignoreAfter(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestStateRootGitignoreSideEffect(t *testing.T) {
	// JS: "stateRoot: the .gitignore entry is added once, only on a definite not ignored"
	t.Run(`stateRoot: the .gitignore entry is added once, only on a definite "not ignored"`, func(t *testing.T) { // JS: "stateRoot: the .gitignore entry is added once, only on a definite \"not ignored\""
		env, cwd := fixture(t)
		env["SOHO_DIR"] = ""
		ctx := LoadConfig(env, cwd)
		want := filepath.Join(platform.StateProjectRoot(env, cwd), ".soho")
		if got := StateRoot(&ctx, env, cwd); got != want {
			t.Fatalf("StateRoot()=%q want %q", got, want)
		}
		if got, err := os.ReadFile(filepath.Join(cwd, ".gitignore")); err != nil || string(got) != ".soho/\n" {
			t.Fatalf("gitignore=%q err=%v", got, err)
		}
		StateRoot(&ctx, env, cwd)
		if got, _ := os.ReadFile(filepath.Join(cwd, ".gitignore")); string(got) != ".soho/\n" {
			t.Fatalf("duplicate gitignore entry: %q", got)
		}
	})
	t.Run("NOWRITE leaves the ignore file alone", func(t *testing.T) {
		env, cwd := fixture(t)
		env["SOHO_DIR"] = ""
		env["SOHO_NOWRITE"] = "1"
		ctx := LoadConfig(env, cwd)
		StateRoot(&ctx, env, cwd)
		if _, err := os.Stat(filepath.Join(cwd, ".gitignore")); !os.IsNotExist(err) {
			t.Fatalf("NOWRITE created .gitignore: %v", err)
		}
	})
	t.Run("NOWRITE only applies to the exact value one", func(t *testing.T) {
		env, cwd := fixture(t)
		env["SOHO_DIR"] = ""
		env["SOHO_NOWRITE"] = "0"
		ctx := LoadConfig(env, cwd)
		if Nowrite(env) {
			t.Fatal("NOWRITE=0 was treated as read-only")
		}
		StateRoot(&ctx, env, cwd)
		if got, err := os.ReadFile(filepath.Join(cwd, ".gitignore")); err != nil || string(got) != ".soho/\n" {
			t.Fatalf("NOWRITE=0 ignore entry=%q err=%v", got, err)
		}
	})
}
