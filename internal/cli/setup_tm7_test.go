package cli

import (
	"bytes"

	"os"
	"path/filepath"
	"reflect"

	"sort"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type setupTM7Step struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	Err  string            `json:"err"`
	Out  string            `json:"out"`
	RC   int               `json:"rc"`
}

type setupTM7Tree struct {
	Repo  [][2]string `json:"repo"`
	Conf  [][2]string `json:"conf"`
	State [][2]string `json:"state"`
}

type setupTM7Fixture struct {
	Conf  string
	Env   platform.Env
	Home  string
	Repo  string
	Root  string
	State string
	Tmp   string
}

func newSetupTM7Fixture(t *testing.T) setupTM7Fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fix := setupTM7Fixture{
		Root:  root,
		Repo:  filepath.Join(root, "repo"),
		Home:  filepath.Join(root, "home"),
		Conf:  filepath.Join(root, "conf"),
		State: filepath.Join(root, "state"),
		Tmp:   filepath.Join(root, "tmp"),
	}
	for _, dir := range []string{fix.Repo, fix.Home, fix.Conf, fix.State, fix.Tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	gitRules := []fakecli.Rule{
		{Argv: []string{"rev-parse", "--show-toplevel"}, Stdout: fix.Repo + "\n"},
		{Argv: []string{"rev-parse", "--git-dir"}, Code: 1},
		{Argv: []string{"rev-parse", "--git-common-dir"}, Code: 1},
		{Argv: []string{"-C", fix.Repo, "rev-parse", "--is-inside-work-tree"}, Stdout: "true\n"},
		{ArgvPrefix: true, Argv: []string{"-C", fix.Repo, "check-ignore", "-q"}, Code: 1},
		{AnyArgs: true, Code: 1},
	}
	if _, err := fakecli.Install(t, bin, "git", gitRules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	fix.Env = platform.Env{}
	for _, item := range fakecli.Env(testutil.CleanEnv(t), bin) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			fix.Env[key] = value
		}
	}
	for key := range fix.Env {
		if key == "SOHO_FAKECLI_CONFIG" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(key), "HERDR_") || strings.HasPrefix(key, "SOHO_") {
			delete(fix.Env, key)
		}
	}
	fix.Env["HOME"], fix.Env["USERPROFILE"] = fix.Home, fix.Home
	fix.Env["XDG_CONFIG_HOME"], fix.Env["SOHO_DIR"] = fix.Conf, fix.State
	fix.Env["HERDR_WORKSPACE_ID"], fix.Env["TMPDIR"] = "ws", fix.Tmp
	fix.Env["SOHO_SKILL_DIR"] = setupTM7SkillDir(t)
	return fix
}

func writeSetupTM7Seed(t *testing.T, root, rel, content string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runSetupTM7Steps(t *testing.T, fix setupTM7Fixture, steps []setupTM7Step) []setupTM7Step {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(fix.Repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	t.Setenv("PATH", fix.Env.Get("PATH"))
	oldOut, oldErr := platform.Stdout, platform.Stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	got := make([]setupTM7Step, 0, len(steps))
	for _, step := range steps {
		env := fix.Env.Clone()
		for key, value := range step.Env {
			env[key] = value
		}
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		code := Run(step.Args, env)
		stdout := normalizeGoldenRoot(out.String(), fix.Root)
		stderr := normalizeGoldenRoot(errOut.String(), fix.Root)
		program := filepath.Join(env.Get("SOHO_SKILL_DIR"), "scripts", "herdr-soho")
		if platform.Current() == "win32" {
			program += ".cmd"
		}
		stdout = strings.ReplaceAll(stdout, program, "PROG")
		stderr = strings.ReplaceAll(stderr, program, "PROG")
		stderr = strings.ReplaceAll(stderr, "soho: ", "PROG: ")
		got = append(got, setupTM7Step{Args: step.Args, RC: code, Out: stdout, Err: stderr})
	}
	return got
}

func setupTM7ReadRel(root, rel string) *string {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	content := string(data)
	return &content
}

func setupTM7SetupStep(args []string) setupTM7Step { return setupTM7Step{Args: args} }

func setupTM7SkillDir(t *testing.T) string {
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

func setupTM7TreeOf(t *testing.T, root string) setupTM7Tree {
	t.Helper()
	return setupTM7Tree{Repo: setupTM7Walk(t, root, "repo"), Conf: setupTM7Walk(t, root, "conf"), State: setupTM7Walk(t, root, "state")}
}

func setupTM7Walk(t *testing.T, root, relRoot string) [][2]string {
	t.Helper()
	base := filepath.Join(root, relRoot)
	var out [][2]string
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == base {
			return nil
		}
		if entry.Name() == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			out = append(out, [2]string{filepath.ToSlash(rel), "<dir>"})
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out = append(out, [2]string{filepath.ToSlash(rel), "<symlink>" + target})
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, [2]string{filepath.ToSlash(rel), string(data)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func TestSetupTM7SetupPlanEndToEndCLI(t *testing.T) {
	baseFiles := func(t *testing.T, f setupTM7Fixture) {
		writeSetupTM7Seed(t, f.Repo, "AGENTS.md", "# Agent instructions\n")
		writeSetupTM7Seed(t, f.Repo, ".claude/settings.json", "{\"hooks\":{\"PreToolUse\":[{\"hooks\":[{\"type\":\"command\",\"command\":\"echo keep-me\"}]}]}}\n")
	}
	assertNoWrite := func(t *testing.T, f setupTM7Fixture, args [][]string, wantCodes []int, wantOut [][]string) {
		t.Helper()
		before := setupTM7TreeOf(t, f.Root)
		steps := make([]setupTM7Step, len(args))
		for i := range args {
			steps[i] = setupTM7SetupStep(args[i])
		}
		got := runSetupTM7Steps(t, f, steps)
		for i, step := range got {
			if step.RC != wantCodes[i] {
				t.Fatalf("step %d got rc=%d out=%q err=%q, want rc=%d", i, step.RC, step.Out, step.Err, wantCodes[i])
			}
			for _, want := range wantOut[i] {
				if !strings.Contains(step.Out, want) {
					t.Fatalf("step %d output %q does not contain %q", i, step.Out, want)
				}
			}
		}
		if after := setupTM7TreeOf(t, f.Root); !reflect.DeepEqual(before, after) {
			t.Fatalf("setup --plan wrote files: before=%+v after=%+v", before, after)
		}
	}
	t.Run(`// JS: "e2e: setup --plan --panes prints the plan and writes nothing"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		baseFiles(t, f)
		writeSetupTM7Seed(t, f.Repo, ".agents/soho.conf", "panes=3\n")
		assertNoWrite(t, f, [][]string{{"setup", "--plan", "--panes", "4"}}, []int{0}, [][]string{{"plan (nothing is written):", "panes                3 → 4"}})
	})
	t.Run(`// JS: "e2e: --panes 2 plans the 2-pane preset (nothing written)"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		baseFiles(t, f)
		assertNoWrite(t, f, [][]string{{"setup", "--plan", "--panes", "2"}}, []int{0}, [][]string{{"panes                (unset) → 2", "reuse_workers        (unset) → on"}})
	})
	t.Run(`// JS: "e2e: --set, --user-set and --session-set together — the three sections, nothing written"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		baseFiles(t, f)
		assertNoWrite(t, f, [][]string{{"setup", "--plan", "--set", "max_workers", "5", "--user-set", "model.pi.worker", "my-provider/my-model", "--session-set", "lane.build.kind", "pi"}}, []int{0}, [][]string{{"max_workers", "model.pi.worker", "lane.build.kind"}})
	})
	t.Run(`// JS: "e2e: invalid keys/values and a missing flag value die 2 before anything is shown or written"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		baseFiles(t, f)
		assertNoWrite(t, f, [][]string{{"setup", "--plan", "--set", "nope", "1"}, {"setup", "--plan", "--set", "max_workers", "-1"}, {"setup", "--plan", "--lane"}}, []int{2, 2, 2}, [][]string{{}, {}, {}})
		before := setupTM7TreeOf(t, f.Root)
		got := runSetupTM7Steps(t, f, []setupTM7Step{setupTM7SetupStep([]string{"setup", "--plan", "--bogus"})})[0]
		if got.RC != 2 || got.Out != "" || !strings.Contains(got.Err, "setup --plan: unknown option '--bogus'") {
			t.Fatalf("unknown plan option rc=%d out=%q err=%q", got.RC, got.Out, got.Err)
		}
		if after := setupTM7TreeOf(t, f.Root); !reflect.DeepEqual(before, after) {
			t.Fatalf("unknown plan option changed files: before=%+v after=%+v", before, after)
		}
	})
	t.Run(`// JS: "e2e: a write the real setup would refuse is refused by the plan (rc 4), file untouched"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		writeSetupTM7Seed(t, f.Repo, "AGENTS.md", "# Agent instructions\n")
		writeSetupTM7Seed(t, f.Repo, ".claude/settings.json", "not-json\n")
		before := setupTM7TreeOf(t, f.Root)
		got := runSetupTM7Steps(t, f, []setupTM7Step{setupTM7SetupStep([]string{"setup", "--plan", "--panes", "4"})})[0]
		if got.RC != 4 || !strings.Contains(got.Out, "plan (nothing is written):") || !strings.Contains(got.Err, "settings.json") {
			t.Fatalf("rc=%d out=%q err=%q", got.RC, got.Out, got.Err)
		}
		if after := setupTM7TreeOf(t, f.Root); !reflect.DeepEqual(before, after) {
			t.Fatalf("refused plan changed files: before=%+v after=%+v", before, after)
		}
	})
	t.Run(`// JS: "e2e: --target on a file with the block plans the in-place replacement; --no-hooks skips the hooks"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		writeSetupTM7Seed(t, f.Repo, "AGENTS.md", "# head\n"+setuptext.SetupStart+"\nold block\n"+setuptext.SetupEnd+"\n# tail\n")
		assertNoWrite(t, f, [][]string{{"setup", "--plan", "--target", "AGENTS.md", "--no-hooks"}}, []int{0}, [][]string{{"AGENTS.md", "old block"}})
	})
	t.Run(`// JS: "e2e: the .gitignore entry is planned (not written) when the state dir would not be ignored"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		writeSetupTM7Seed(t, f.Repo, ".gitignore", "*.log\n")
		before := setupTM7TreeOf(t, f.Root)
		got := runSetupTM7Steps(t, f, []setupTM7Step{{Args: []string{"setup", "--plan", "--session-set", "lane.build.kind", "pi"}, Env: map[string]string{"SOHO_DIR": ""}}})[0]
		if got.RC != 0 || !strings.Contains(got.Out, ".gitignore") {
			t.Fatalf("rc=%d out=%q err=%q", got.RC, got.Out, got.Err)
		}
		if after := setupTM7TreeOf(t, f.Root); !reflect.DeepEqual(before, after) {
			t.Fatalf("planned gitignore changed files: before=%+v after=%+v", before, after)
		}
	})
	t.Run(`// JS: "e2e: --session-set without a resolvable workspace dies 2"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		before := setupTM7TreeOf(t, f.Root)
		got := runSetupTM7Steps(t, f, []setupTM7Step{{Args: []string{"setup", "--plan", "--session-set", "lane.build.kind", "pi"}, Env: map[string]string{"HERDR_WORKSPACE_ID": ""}}})[0]
		if got.RC != 2 || got.Out != "" {
			t.Fatalf("rc=%d out=%q err=%q", got.RC, got.Out, got.Err)
		}
		if after := setupTM7TreeOf(t, f.Root); !reflect.DeepEqual(before, after) {
			t.Fatalf("unresolvable session plan changed files: before=%+v after=%+v", before, after)
		}
	})
	t.Run(`// JS: "cmdSetupPlan: in-process — DieError 2 for a bad lane spec, stdout untouched"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		before := setupTM7TreeOf(t, f.Root)
		got := runSetupTM7Steps(t, f, []setupTM7Step{setupTM7SetupStep([]string{"setup", "--plan", "--lane", "build=boguskind"})})[0]
		if got.RC != 2 || got.Out != "" {
			t.Fatalf("rc=%d out=%q err=%q", got.RC, got.Out, got.Err)
		}
		if after := setupTM7TreeOf(t, f.Root); !reflect.DeepEqual(before, after) {
			t.Fatalf("bad lane plan changed files: before=%+v after=%+v", before, after)
		}
	})
}

func TestSetupTM7PanesWriteCLI(t *testing.T) {
	t.Run(`// JS: "setup --panes: 2 writes the 2-pane preset (no frozen roles/limits), 5 dies 2"`, func(t *testing.T) {
		f := newSetupTM7Fixture(t)
		got := runSetupTM7Steps(t, f, []setupTM7Step{setupTM7SetupStep([]string{"setup", "--panes", "2"})})[0]
		content := setupTM7ReadRel(f.Repo, ".agents/soho.conf")
		if got.RC != 0 || content == nil || !strings.Contains(*content, "panes=2") || !strings.Contains(*content, "reuse_workers=on") || strings.Contains(*content, "lane.build.roles=") || strings.Contains(*content, "max_workers=") || strings.Contains(*content, "split_max_panes=") || strings.Contains(got.Err, "setup --panes 2|3|4") == false {
			t.Fatalf("setup --panes 2 rc=%d out=%q err=%q config=%v", got.RC, got.Out, got.Err, content)
		}
		before := setupTM7TreeOf(t, f.Root)
		bad := runSetupTM7Steps(t, f, []setupTM7Step{setupTM7SetupStep([]string{"setup", "--panes", "5"})})[0]
		if bad.RC != 2 || bad.Out != "" || !strings.Contains(bad.Err, "--panes must be 2, 3 or 4") || !reflect.DeepEqual(before, setupTM7TreeOf(t, f.Root)) {
			t.Fatalf("invalid --panes result rc=%d out=%q err=%q", bad.RC, bad.Out, bad.Err)
		}
	})
}
