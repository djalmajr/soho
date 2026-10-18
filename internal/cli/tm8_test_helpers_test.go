package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func tm8CommandFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	_, repo := commandFixture(t)
	root := filepath.Dir(repo)
	bin := t.TempDir()
	for _, name := range []string{"herdr", "codex", "claude", "cursor", "grok", "gemini", "pi", "opencode"} {
		if _, err := fakecli.Install(t, bin, name, []fakecli.Rule{{AnyArgs: true, Stdout: ""}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fakecli.Install(t, bin, "git", []fakecli.Rule{
		{Argv: []string{"-C", repo, "rev-parse", "--is-inside-work-tree"}, Stdout: "true\n"},
		{Argv: []string{"-C", repo, "check-ignore", "-q", ".soho"}, Code: 1},
		{AnyArgs: true},
	}); err != nil {
		t.Fatal(err)
	}
	clean := envFrom(testutil.CleanEnv(t))
	for key := range clean {
		if strings.HasPrefix(strings.ToUpper(key), "HERDR_") || strings.HasPrefix(key, "SOHO_") || key == "PATH" {
			delete(clean, key)
		}
	}
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{tmp, filepath.Join(root, "home"), filepath.Join(root, "conf"), filepath.Join(root, "state")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	skillDir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	clean = withFakeCLI(clean, bin)
	clean["HOME"] = filepath.Join(root, "home")
	clean["USERPROFILE"] = clean["HOME"]
	clean["XDG_CONFIG_HOME"] = filepath.Join(root, "conf")
	clean["TMPDIR"] = tmp
	clean["SOHO_DIR"] = filepath.Join(root, "state")
	clean["SOHO_SKILL_DIR"] = skillDir
	clean["HERDR_WORKSPACE_ID"] = "ws"
	clean["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing.sock")
	clean["HERDR_ENV"] = "1"
	return clean, repo
}

type tm8GoldenStep struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	Err  string            `json:"err"`
	Out  string            `json:"out"`
	RC   int               `json:"rc"`
}

type tm8GoldenFile struct {
	Rel     string  `json:"rel"`
	Content *string `json:"content"`
}

type tm8GoldenScenario struct {
	Files []tm8GoldenFile `json:"files"`
	Steps []tm8GoldenStep `json:"steps"`
}

func runTM8ConfigGolden(t *testing.T, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("parity-config JS fixture specifies POSIX temporary git paths")
	}
	data, err := os.ReadFile(filepath.Join("..", "testdata", "legacy", "parity-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var scenarios map[string]tm8GoldenScenario
	if err := json.Unmarshal(data, &scenarios); err != nil {
		t.Fatal(err)
	}
	want, ok := scenarios[name]
	if !ok {
		t.Fatalf("golden scenario %q missing", name)
	}
	env, repo := tm8CommandFixture(t)
	root := filepath.Dir(repo)
	projectFile := filepath.Join(repo, ".agents", "soho.conf")
	seed := ""
	switch name {
	case "config-set-dotted", "config-set-lane-roles", "config-set-verbatim":
		seed = "# seed\n"
	case "config-set-invalid", "config-set-rewrite", "config-set-user":
		seed = "# keep this comment\nmax_workers=3 # live cap\n# tail comment\nreuse_workers=on\n\nmax_workers=1\n"
	case "session-clear", "precedence":
		seed = "lane.build.kind=codex\n"
	}
	if seed != "" {
		if err := os.MkdirAll(filepath.Dir(projectFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(projectFile, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if name == "session-errors-seeded" {
		path := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("lane.build.kind=pi\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if name == "project-roles" {
		path := filepath.Join(repo, ".agents", "herdr-roles", "implementer.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\nname: implementer\nkind: claude\neffort: low\nmode: edit\n---\nproject override\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if name == "state-gitignore" {
		for i := range want.Steps {
			if want.Steps[i].Env == nil {
				want.Steps[i].Env = map[string]string{}
			}
			want.Steps[i].Env["SOHO_DIR"] = ""
		}
	}
	// The frozen golden predates the soho name layer; the exact-substitution
	// name oracle adapts the expected output, error, file names and contents.
	// The DJA-392 oracle likewise adapts the frozen config listing that now
	// ends with the two pressure refusal keys. The DJA-393 pane_columns
	// oracle adapts the config listing that now shows the declared-columns
	// keys after split_min_pane.
	for i := range want.Files {
		want.Files[i].Rel = testutil.RewriteSohoName(want.Files[i].Rel)
		if want.Files[i].Content != nil {
			*want.Files[i].Content = testutil.RewriteSohoName(*want.Files[i].Content)
			*want.Files[i].Content = testutil.RewritePressureRefusal(*want.Files[i].Content)
			*want.Files[i].Content = testutil.RewritePaneColumns(*want.Files[i].Content)
		}
	}
	for i := range want.Steps {
		want.Steps[i].Out = testutil.RewriteSohoName(want.Steps[i].Out)
		want.Steps[i].Out = testutil.RewritePressureRefusal(want.Steps[i].Out)
		want.Steps[i].Out = testutil.RewritePaneColumns(want.Steps[i].Out)
		want.Steps[i].Err = testutil.RewriteSohoName(want.Steps[i].Err)
		want.Steps[i].Err = testutil.RewritePressureRefusal(want.Steps[i].Err)
		want.Steps[i].Err = testutil.RewritePaneColumns(want.Steps[i].Err)
	}
	for i, step := range want.Steps {
		stepEnv := env.Clone()
		for key, value := range step.Env {
			stepEnv[key] = value
		}
		if name == "no-workspace" {
			stepEnv["HERDR_WORKSPACE_ID"] = ""
		}
		if name == "precedence" && i == 2 {
			stepEnv["SOHO_LANE_BUILD_KIND"] = "grok"
		}
		code, out, errOut := runIn(t, step.Args, stepEnv, repo)
		out = strings.ReplaceAll(out, root, "<ROOT>")
		out = normalizeGoldenRootPathSeparators(out)
		errOut = strings.ReplaceAll(normalizeParityError(errOut), root, "<ROOT>")
		errOut = normalizeGoldenRootPathSeparators(errOut)
		wantErr := normalizeGoldenRootPathSeparators(strings.ReplaceAll(step.Err, root, "<ROOT>"))
		if name == "roles-role" || name == "project-roles" {
			out = strings.ReplaceAll(out, env.Get("SOHO_SKILL_DIR"), "<SKILL>")
			// The frozen golden predates the shipped job-orchestrator role;
			// the exact-insertion role oracle adapts the expected table row.
			wantOut := testutil.ApplyRoleOracleToRolesTable(strings.ReplaceAll(step.Out, env.Get("SOHO_SKILL_DIR"), "<SKILL>"))
			if code != step.RC || out != wantOut || errOut != wantErr {
				t.Fatalf("step %d args=%v code=%d/%d stdout=%q want %q stderr=%q want %q", i, step.Args, code, step.RC, out, wantOut, errOut, wantErr)
			}
		} else if code != step.RC || out != step.Out || errOut != wantErr {
			t.Fatalf("step %d args=%v code=%d/%d stdout=%q want %q stderr=%q want %q", i, step.Args, code, step.RC, out, step.Out, errOut, wantErr)
		}
	}
	for _, file := range want.Files {
		actual, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Rel)))
		if file.Content == nil {
			if !os.IsNotExist(readErr) {
				t.Fatalf("file %s should be absent, read error=%v", file.Rel, readErr)
			}
		} else if readErr != nil || string(actual) != *file.Content {
			t.Fatalf("file %s=%q error=%v want=%q", file.Rel, actual, readErr, *file.Content)
		}
	}
}

func tm8File(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
