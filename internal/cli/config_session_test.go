package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

func commandFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
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
	init := exec.Command("git", "init", "-q")
	init.Dir = repo
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
	skillDir := testSkillDir(t)
	return platform.Env{
		"HOME": home, "XDG_CONFIG_HOME": conf, "SOHO_DIR": state,
		"SOHO_SKILL_DIR": skillDir, "HERDR_WORKSPACE_ID": "ws", "PATH": os.Getenv("PATH"),
	}, repo
}

// packageDir is the working directory the test binary started in.
var packageDir, packageDirErr = os.Getwd()

func testSkillDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillDir := filepath.Join(root, "skills", "soho")
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
		return skillDir
	}
	// Built elsewhere (the Windows round), the source path does not exist:
	// fall back to the package directory the test binary started in, which
	// a test that changed directory no longer has as its cwd.
	if packageDirErr != nil {
		t.Fatal(packageDirErr)
	}
	skillDir = filepath.Clean(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("skill directory %q: %v", skillDir, err)
	}
	return skillDir
}

func TestApprovalsAutoOracleExactDefaultRow(t *testing.T) {
	old := "approvals          ask                            defaults"
	want := "approvals          auto                           defaults"
	for _, tc := range []struct{ input, output string }{
		{old, want}, {old + "\n", want + "\n"},
		{"before\n" + old + "\nafter\n", "before\n" + want + "\nafter\n"},
		{want, want},
		{"approvals=ask\n", "approvals=ask\n"},
		{strings.Replace(old, "defaults", "project", 1), strings.Replace(old, "defaults", "project", 1)},
		{strings.Replace(old, "defaults", "user", 1), strings.Replace(old, "defaults", "user", 1)},
		{"prefix " + old, "prefix " + old},
		{old + " suffix", old + " suffix"},
		{old + "\r\n", old + "\r\n"},
		{strings.Replace(old, "ask ", "ask", 1), strings.Replace(old, "ask ", "ask", 1)},
	} {
		if got := testutil.RewriteApprovalsAuto(tc.input); got != tc.output {
			t.Errorf("input=%q got=%q want=%q", tc.input, got, tc.output)
		}
	}
}

func TestApprovalsAutoOracleSpawnBoundaries(t *testing.T) {
	doctor := "ok     config: layout=split approvals=ask auto_approve=off reuse_workers=on multi_role=on worker_context=full"
	if got := testutil.RewriteApprovalsAutoDoctor("before\n" + doctor + "\nafter\n"); got != "before\n"+strings.Replace(doctor, "approvals=ask", "approvals=auto", 1)+"\nafter\n" {
		t.Fatalf("doctor exact row: %q", got)
	}
	for _, line := range []string{"prefix " + doctor, doctor + " suffix", strings.Replace(doctor, "layout=split", "layout=tab", 1), doctor + "\r\n"} {
		if got := testutil.RewriteApprovalsAutoDoctor(line); got != line {
			t.Fatalf("unrelated doctor row changed: %q", got)
		}
	}
	for _, tc := range []struct{ kind, flag string }{{"codex", "--approve-for-me"}, {"grok", "--permission-mode auto"}, {"agy", "--mode auto"}, {"pi", ""}} {
		original := "{\n  \"name\": \"fresh\",\n  \"kind\": \"" + tc.kind + "\",\n  \"approvals\": \"ask\",\n  \"agent_args\": \"--model fixed\",\n  \"reused\": false\n}\n"
		want := strings.Replace(original, `"approvals": "ask"`, `"approvals": "auto"`, 1)
		if tc.flag != "" {
			want = strings.Replace(want, `"agent_args": "`, `"agent_args": "`+tc.flag+" ", 1)
		}
		trailer := "{\"wait_status\":\"sent\"}\nreport bytes\n"
		if got := testutil.RewriteApprovalsAutoSpawnJSON(original+trailer, nil); got != want+trailer {
			t.Fatalf("kind=%s got=%q want=%q", tc.kind, got, want+trailer)
		}
		for _, request := range [][]string{{"--approvals", "ask"}, {"--approvals=ask"}, {"--approvals", "full"}} {
			if got := testutil.RewriteApprovalsAutoSpawnJSON(original, request); got != original {
				t.Fatalf("explicit request changed: %q", got)
			}
		}
		for _, old := range []string{strings.Replace(original, `"reused": false`, `"reused": true`, 1), strings.Replace(original, `"approvals": "ask"`, `"approvals": "full"`, 1), strings.Replace(original, `"kind": "`+tc.kind+`"`, `"kind": "other"`, 1), "not json\n"} {
			if got := testutil.RewriteApprovalsAutoSpawnJSON(old, nil); got != old {
				t.Fatalf("unrelated spawn changed: %q", got)
			}
		}
		log := "agent get seeded\nagent start fresh --kind " + tc.kind + " -- --model fixed\n"
		wantLog := log
		if tc.flag != "" {
			wantLog = strings.Replace(log, " -- --model", " -- "+tc.flag+" --model", 1)
		}
		if got := testutil.RewriteApprovalsAutoStartLog(log, nil); got != wantLog {
			t.Fatalf("log got=%q want=%q", got, wantLog)
		}
		if got := testutil.RewriteApprovalsAutoStartLog(log, []string{"--approvals", "ask"}); got != log {
			t.Fatalf("explicit log changed: %q", got)
		}
	}
	row := "fresh\tp1\tcodex\tworker\topenai\t1\t/work\tnow\tmodel\task\tworker\n"
	seeded := strings.Replace(row, "fresh", "seeded", 1)
	legacy := "fresh\tp1\tcodex\tworker\topenai\t1\t/work\tnow\n"
	want := strings.Replace(row, "\task\t", "\tauto\t", 1) + seeded + legacy
	if got := testutil.RewriteApprovalsAutoRoster(row+seeded+legacy, []string{"fresh"}); got != want {
		t.Fatalf("roster got=%q want=%q", got, want)
	}
	if got := testutil.RewriteApprovalsAutoRoster(row, nil); got != row {
		t.Fatal("unselected row changed")
	}
}

func TestParityConfigGolden(t *testing.T) {
	// JS: "parity: config and session command goldens"
	goldenPath := filepath.Join("..", "testdata", "legacy", "parity-config.json")
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		Args []string          `json:"args"`
		Env  map[string]string `json:"env"`
		Err  string            `json:"err"`
		Out  string            `json:"out"`
		RC   int               `json:"rc"`
	}
	type fileEntry struct {
		Rel     string  `json:"rel"`
		Content *string `json:"content"`
	}
	type scenario struct {
		Files []fileEntry `json:"files"`
		Steps []step      `json:"steps"`
	}
	var goldens map[string]scenario
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatal(err)
	}
	names := []string{"config-fresh", "config-set-dotted", "config-set-fresh", "config-set-invalid", "config-set-lane-roles", "config-set-rewrite", "config-set-user", "config-set-verbatim", "no-workspace", "precedence", "session-bare-show", "session-clear", "session-errors-seeded", "session-set", "state-gitignore"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			golden, ok := goldens[name]
			if !ok {
				t.Fatalf("golden scenario %q missing", name)
			}
			env, cwd := commandFixture(t)
			root := filepath.Dir(cwd)
			projectFile := filepath.Join(cwd, ".agents", "soho.conf")
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
				if err := os.MkdirAll(filepath.Join(env.Get("SOHO_DIR"), "ws"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf"), []byte("lane.build.kind=pi\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "state-gitignore" {
				for i := range golden.Steps {
					if golden.Steps[i].Env == nil {
						golden.Steps[i].Env = make(map[string]string)
					}
					golden.Steps[i].Env["SOHO_DIR"] = ""
				}
			}
			for i, want := range golden.Steps {
				stepEnv := env.Clone()
				for key, value := range want.Env {
					stepEnv[key] = value
				}
				if name == "no-workspace" {
					stepEnv["HERDR_WORKSPACE_ID"] = ""
				}
				if name == "precedence" && i == 2 {
					stepEnv["SOHO_LANE_BUILD_KIND"] = "grok"
				}
				code, out, errOut := runIn(t, want.Args, stepEnv, cwd)
				out = strings.ReplaceAll(out, root, "<ROOT>")
				out = normalizeGoldenRootPathSeparators(out)
				errOut = normalizeParityError(errOut)
				errOut = strings.ReplaceAll(errOut, root, "<ROOT>")
				errOut = normalizeGoldenRootPathSeparators(errOut)
				wantErr := testutil.RewriteSohoName(want.Err)
				wantErr = strings.ReplaceAll(wantErr, root, "<ROOT>")
				wantErr = normalizeGoldenRootPathSeparators(wantErr)
				wantErr = testutil.RewritePressureRefusal(wantErr)
				wantErr = testutil.RewritePaneColumns(wantErr)
				wantErr = testutil.RewriteFootprint(wantErr)
				wantErr = testutil.RewriteCompactTimeout(wantErr)
				wantErr = testutil.RewriteBackgroundPatterns(wantErr)
				wantErr = testutil.RewriteCallerCheck(wantErr)
				wantErr = testutil.RewriteApprovalsAuto(wantErr)
				want.Out = testutil.RewriteSohoName(want.Out)
				want.Out = testutil.RewritePressureRefusal(want.Out)
				want.Out = testutil.RewritePaneColumns(want.Out)
				want.Out = testutil.RewriteFootprint(want.Out)
				want.Out = testutil.RewriteCompactTimeout(want.Out)
				want.Out = testutil.RewriteBackgroundPatterns(want.Out)
				want.Out = testutil.RewriteCallerCheck(want.Out)
				want.Out = testutil.RewriteApprovalsAuto(want.Out)
				if code != want.RC || out != want.Out || errOut != wantErr {
					t.Fatalf("step %d args=%v\ncode=%d want=%d\nstdout:\n%s\nwant:\n%s\nstderr=%q want=%q", i, want.Args, code, want.RC, out, want.Out, errOut, wantErr)
				}
			}
			for _, expected := range golden.Files {
				expected.Rel = testutil.RewriteSohoName(expected.Rel)
				path := filepath.Join(root, filepath.FromSlash(expected.Rel))
				actual, readErr := os.ReadFile(path)
				if expected.Content == nil {
					if !os.IsNotExist(readErr) {
						t.Fatalf("file %s should be absent, read err=%v", expected.Rel, readErr)
					}
					continue
				}
				wantContent := testutil.RewriteSohoName(*expected.Content)
				wantContent = testutil.RewritePressureRefusal(wantContent)
				wantContent = testutil.RewritePaneColumns(wantContent)
				wantContent = testutil.RewriteFootprint(wantContent)
				wantContent = testutil.RewriteCompactTimeout(wantContent)
				wantContent = testutil.RewriteBackgroundPatterns(wantContent)
				wantContent = testutil.RewriteCallerCheck(wantContent)
				wantContent = testutil.RewriteApprovalsAuto(wantContent)
				if readErr != nil || string(actual) != wantContent {
					t.Fatalf("file %s=%q err=%v want=%q", expected.Rel, actual, readErr, wantContent)
				}
			}
		})
	}
}

func normalizeGoldenRootPathSeparators(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if runtime.GOOS != "windows" {
		return value
	}
	var normalized strings.Builder
	for {
		start := strings.Index(value, "<ROOT>")
		if start < 0 {
			normalized.WriteString(value)
			return normalized.String()
		}
		normalized.WriteString(value[:start])
		end := start + len("<ROOT>")
		for end < len(value) && value[end] != ' ' && value[end] != '\t' && value[end] != '\r' && value[end] != '\n' && value[end] != '"' {
			end++
		}
		segment := value[start:end]
		segment = strings.ReplaceAll(segment, `\\`, "/")
		segment = strings.ReplaceAll(segment, `\`, "/")
		normalized.WriteString(segment)
		value = value[end:]
	}
}

func normalizeGoldenRoot(value, root string) string {
	for _, path := range []string{root, filepath.ToSlash(root)} {
		value = strings.ReplaceAll(value, strings.ReplaceAll(path, `\`, `\\`), "<ROOT>")
		value = strings.ReplaceAll(value, path, "<ROOT>")
	}
	return normalizeGoldenRootPathSeparators(value)
}

func firstGoldenLineDifference(got, want string) string {
	got = strings.ReplaceAll(got, "\r\n", "\n")
	want = strings.ReplaceAll(want, "\r\n", "\n")
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		var actual, expected string
		if i < len(gotLines) {
			actual = gotLines[i]
		} else {
			actual = "<eof>"
		}
		if i < len(wantLines) {
			expected = wantLines[i]
		} else {
			expected = "<eof>"
		}
		if actual != expected {
			return fmt.Sprintf("line %d Go=%q golden=%q", i+1, actual, expected)
		}
	}
	return "no differing line"
}

func normalizeParityError(value string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		// The frozen goldens normalize the legacy program prefixes to
		// PROG:; the native binary now carries the soho prefix (and the
		// frozen double-prefix form soho: soho: in the NOWRITE refusal),
		// so the first prefix maps to PROG: exactly like the legacy ones.
		if strings.HasPrefix(line, "soho:") {
			lines[i] = "PROG:" + strings.TrimPrefix(line, "soho:")
		} else if strings.HasPrefix(line, "herdr-soho:") {
			lines[i] = "PROG:" + strings.TrimPrefix(line, "herdr-soho:")
		} else if strings.HasPrefix(line, "herdr-soho.mjs:") {
			lines[i] = "PROG:" + strings.TrimPrefix(line, "herdr-soho.mjs:")
		}
	}
	return strings.Join(lines, "\n")
}

func runIn(t *testing.T, args []string, env platform.Env, cwd string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCwd)
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := Run(args, env)
	return code, out.String(), errOut.String()
}

func TestConfigCommandRoutesToGo(t *testing.T) {
	// JS: "config shows defaults for unset keys (multi_role on defaults)"
	env, cwd := commandFixture(t)
	code, out, errOut := runIn(t, []string{"config"}, env, cwd)
	if code != 0 || errOut != "" || !strings.Contains(out, "multi_role         on                             defaults") {
		t.Fatalf("config code=%d stderr=%q output contains default=%t", code, errOut, strings.Contains(out, "multi_role         on                             defaults"))
	}
	code, out, errOut = runIn(t, []string{"config", "set", "max_workers=5"}, env, cwd)
	if code != 0 || errOut != "" || !strings.Contains(out, "set max_workers=5 in ") {
		t.Fatalf("config set code=%d out=%q err=%q", code, out, errOut)
	}
	content, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
	if err != nil || string(content) != "max_workers=5\n" {
		t.Fatalf("config file=%q err=%v", content, err)
	}
	info, err := os.Stat(filepath.Join(cwd, ".agents", "soho.conf"))
	if err != nil || (runtime.GOOS == "windows" && info.Mode().Perm()&0o200 == 0) || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("new config mode=%v err=%v", info, err)
	}
	if runtime.GOOS != "windows" {
		path := filepath.Join(cwd, ".agents", "soho.conf")
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if code, _, errOut = runIn(t, []string{"config", "set", "layout", "tab"}, env, cwd); code != 0 || errOut != "" {
			t.Fatalf("rewrite existing config code=%d err=%q", code, errOut)
		}
		info, err = os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("rewritten config mode=%v err=%v", info, err)
		}
	}
	code, out, errOut = runIn(t, []string{"config", "set", "nope", "1"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: config set: unknown key 'nope'\n" {
		t.Fatalf("invalid config set code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestConfigCLIReadsCRLFLikeLF(t *testing.T) {
	// JS: "config CLI: a CRLF config file behaves like the same file with LF"
	env, repo := commandFixture(t)
	configFile := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
	if err := os.MkdirAll(filepath.Dir(configFile), 0o700); err != nil {
		t.Fatal(err)
	}
	const lf = "max_workers=9\nlanes=off\n"
	readRows := func(content string) (string, string) {
		t.Helper()
		if err := os.WriteFile(configFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runIn(t, []string{"config"}, env, repo)
		if code != 0 || errOut != "" {
			t.Fatalf("config code=%d stderr=%q output=%q", code, errOut, out)
		}
		row := func(key string) string {
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, key) {
					return line
				}
			}
			return ""
		}
		return row("max_workers"), row("lanes")
	}
	lfWorkers, lfLanes := readRows(lf)
	if !strings.Contains(lfWorkers, "9") || !strings.Contains(lfWorkers, "user") {
		t.Fatalf("LF max_workers row=%q", lfWorkers)
	}
	crlfWorkers, crlfLanes := readRows(strings.ReplaceAll(lf, "\n", "\r\n"))
	if lfWorkers != crlfWorkers || lfLanes != crlfLanes {
		t.Fatalf("CRLF rows differ: max_workers %q != %q; lanes %q != %q", crlfWorkers, lfWorkers, crlfLanes, lfLanes)
	}
}

func TestConfigSetArgumentForms(t *testing.T) {
	// Mutation captured: config set must join dash-prefixed values, split key=value, and diagnose a shell-unsplit pair.
	t.Run("joins dash-prefixed value", func(t *testing.T) {
		env, cwd := commandFixture(t)
		code, _, errOut := runIn(t, []string{"config", "set", "args.codex", "-c", "a=b"}, env, cwd)
		path := filepath.Join(cwd, ".agents", "soho.conf")
		data, err := os.ReadFile(path)
		if code != 0 || errOut != "" || err != nil || string(data) != "args.codex=-c a=b\n" {
			t.Fatalf("dash value code=%d err=%q file=%q readErr=%v", code, errOut, data, err)
		}
	})
	t.Run("accepts key=value", func(t *testing.T) {
		env, cwd := commandFixture(t)
		code, _, errOut := runIn(t, []string{"config", "set", "layout=columns"}, env, cwd)
		data, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
		if code != 0 || errOut != "" || err != nil || string(data) != "layout=columns\n" {
			t.Fatalf("assignment code=%d err=%q file=%q readErr=%v", code, errOut, data, err)
		}
	})
	t.Run("empty assignment value requests a missing value", func(t *testing.T) {
		env, cwd := commandFixture(t)
		code, out, errOut := runIn(t, []string{"config", "set", "layout="}, env, cwd)
		if code != 2 || out != "" || errOut != "soho: usage: config set <key> <value> | <key>=<value> [--project|--user]\n" {
			t.Fatalf("empty assignment code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(filepath.Join(cwd, ".agents", "soho.conf")); !os.IsNotExist(err) {
			t.Fatalf("empty assignment wrote config: %v", err)
		}
	})
	t.Run("explicit empty value is rejected", func(t *testing.T) {
		env, cwd := commandFixture(t)
		code, out, errOut := runIn(t, []string{"config", "set", "layout", ""}, env, cwd)
		if code != 2 || out != "" || errOut != "soho: config set: empty value\n" {
			t.Fatalf("explicit empty value code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run("reports shell-unsplit pair", func(t *testing.T) {
		env, cwd := commandFixture(t)
		code, out, errOut := runIn(t, []string{"config", "set", "layout columns"}, env, cwd)
		if code != 2 || out != "" || !strings.Contains(errOut, "arrived as one argument; pass the key and the value as two arguments or as key=value") {
			t.Fatalf("unsplit pair code=%d out=%q err=%q", code, out, errOut)
		}
	})
}

func TestSessionCommandRoutesToGoAndEnvIsApplied(t *testing.T) {
	// JS: "session set writes the session file in the state dir"
	// JS: "config shows the session value with source `session`"
	env, cwd := commandFixture(t)
	env["SOHO_LAYOUT"] = "columns"
	code, out, errOut := runIn(t, []string{"session", "set", "lane.build.kind=codex"}, env, cwd)
	if code != 0 || errOut != "" || !strings.Contains(out, "session: this Herdr workspace only") {
		t.Fatalf("session set code=%d out=%q err=%q", code, out, errOut)
	}
	session := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
	content, err := os.ReadFile(session)
	if err != nil || string(content) != "lane.build.kind=codex\n" {
		t.Fatalf("session file=%q err=%v", content, err)
	}
	code, out, errOut = runIn(t, []string{"config"}, env, cwd)
	if code != 0 || errOut != "" || !strings.Contains(out, "lane.build.kind    codex") || !strings.Contains(out, "layout             columns") || !strings.Contains(out, "session") {
		t.Fatalf("config session row code=%d out=%q err=%q", code, out, errOut)
	}
	code, _, errOut = runIn(t, []string{"session", "clear"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("session clear code=%d err=%q", code, errOut)
	}
}

func TestSessionSetJoinsDashPrefixedValues(t *testing.T) {
	// Mutation captured: session set joins later arguments when the first value begins with a dash.
	env, cwd := commandFixture(t)
	code, _, errOut := runIn(t, []string{"session", "set", "args.codex", "-c", "a=b"}, env, cwd)
	path := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
	data, err := os.ReadFile(path)
	if code != 0 || errOut != "" || err != nil || string(data) != "args.codex=-c a=b\n" {
		t.Fatalf("session dash value code=%d err=%q file=%q readErr=%v", code, errOut, data, err)
	}
}

func TestNowriteRejectsPortedCommandsBeforeWriting(t *testing.T) {
	// JS: "nowrite: the entry guard rejects non-exact invocations before any project write"
	env, cwd := commandFixture(t)
	env["SOHO_NOWRITE"] = "1"
	code, out, errOut := runIn(t, []string{"config", "set", "max_workers", "5"}, env, cwd)
	if code != 2 || out != "" || !strings.Contains(errOut, "SOHO_NOWRITE=1 is read-only") {
		t.Fatalf("NOWRITE code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".agents", "soho.conf")); !os.IsNotExist(err) {
		t.Fatalf("NOWRITE created project config: %v", err)
	}
}

func TestNowriteRejectsConfigSetWithTheNewMessage(t *testing.T) {
	// DJA-331: the exact config is now an accepted NOWRITE read; config set stays
	// refused with the message that lists the accepted reads (config and roles included).
	env, cwd := commandFixture(t)
	env["SOHO_NOWRITE"] = "1"
	code, out, errOut := runIn(t, []string{"config", "set", "panes", "3"}, env, cwd)
	wantErr := "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: config (3 extra arguments not allowed) — unset SOHO_NOWRITE to write\n"
	if code != 2 || out != "" || errOut != wantErr {
		t.Fatalf("config set under NOWRITE code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".agents", "soho.conf")); !os.IsNotExist(err) {
		t.Fatalf("NOWRITE created project config: %v", err)
	}
}

func TestSessionClearDanglingSymlinkIsEmpty(t *testing.T) {
	// Mutation captured: os.Lstat succeeds on a dangling symlink, but JS existsSync treats its missing target as an empty session.
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	env, cwd := commandFixture(t)
	sessionFile := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(sessionFile), "missing"), sessionFile); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runIn(t, []string{"session", "clear"}, env, cwd)
	if code != 0 || out != "session is empty (nothing to clear)\n" || errOut != "" {
		t.Fatalf("dangling session clear code=%d out=%q err=%q", code, out, errOut)
	}
	if info, err := os.Lstat(sessionFile); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dangling session link was removed: info=%v err=%v", info, err)
	}
}

func TestFormerConfigIsNotMigratedThroughConfigSet(t *testing.T) {
	// The former herdr-soho config is no longer read: config set writes the
	// current file only, leaves the former file untouched and warns nothing.
	for _, where := range []string{"project", "user"} {
		t.Run(where, func(t *testing.T) {
			env, cwd := commandFixture(t)
			var oldPath, newPath string
			args := []string{"config", "set", "layout", "tab"}
			if where == "user" {
				oldPath = filepath.Join(env.Get("XDG_CONFIG_HOME"), "herdr-soho", "config")
				newPath = filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
				args = append(args, "--user")
			} else {
				oldPath = filepath.Join(cwd, ".agents", "herdr-soho.conf")
				newPath = filepath.Join(cwd, ".agents", "soho.conf")
			}
			if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(oldPath, []byte("# old\npanes=2\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			code, listing, listErr := runIn(t, []string{"config"}, env, cwd)
			if code != 0 || listErr != "" || !strings.Contains(listing, newPath) || strings.Contains(listing, oldPath) {
				t.Fatalf("listing code=%d out=%q err=%q; want only the current file named", code, listing, listErr)
			}
			code, out, errOut := runIn(t, args, env, cwd)
			oldData, oldErr := os.ReadFile(oldPath)
			newData, newErr := os.ReadFile(newPath)
			if code != 0 || out == "" || errOut != "" || oldErr != nil || string(oldData) != "# old\npanes=2\n" || newErr != nil || string(newData) != "layout=tab\n" {
				t.Fatalf("code=%d out=%q err=%q old=%q/%v new=%q/%v; want a plain write to the current file with the former file untouched", code, out, errOut, oldData, oldErr, newData, newErr)
			}
		})
	}
	t.Run("former symlinked config is not read", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires privileges on Windows")
		}
		env, cwd := commandFixture(t)
		target := filepath.Join(filepath.Dir(cwd), "legacy-target.conf")
		oldPath := filepath.Join(cwd, ".agents", "herdr-soho.conf")
		newPath := filepath.Join(cwd, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("panes=2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, oldPath); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := runIn(t, []string{"config", "set", "layout", "tab"}, env, cwd)
		link, linkErr := os.Readlink(oldPath)
		newData, newErr := os.ReadFile(newPath)
		if code != 0 || errOut != "" || linkErr != nil || link != target || newErr != nil || string(newData) != "layout=tab\n" {
			t.Fatalf("code=%d err=%q link=%q/%v new=%q/%v; want the write without reading the former file", code, errOut, link, linkErr, newData, newErr)
		}
	})
}

func TestFormerUnreadableConfigDoesNotBlockConfigSet(t *testing.T) {
	// The former config is not read at all, so an unreadable former file
	// neither blocks the write nor leaves the new file absent.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission denial is not observable for this process")
	}
	env, cwd := commandFixture(t)
	oldPath := filepath.Join(cwd, ".agents", "herdr-soho.conf")
	newPath := filepath.Join(cwd, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("lane.build.kind=codex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oldPath, 0); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runIn(t, []string{"config", "set", "layout", "tab"}, env, cwd)
	if code != 0 || out == "" || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q; want the plain write (the former file is not read)", code, out, errOut)
	}
	newData, newErr := os.ReadFile(newPath)
	if newErr != nil || string(newData) != "layout=tab\n" {
		t.Fatalf("new file = %q/%v", newData, newErr)
	}
	if err := os.Chmod(oldPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(oldPath); err != nil || string(got) != "lane.build.kind=codex\n" {
		t.Fatalf("former file changed: %q err=%v", got, err)
	}
}

func TestConfigAndSessionIOFailuresUseCommandExitCode(t *testing.T) {
	// Mutation captured: I/O failures must return code 4 with command-specific diagnostics and preserve the failing path.
	t.Run("destination directory", func(t *testing.T) {
		env, cwd := commandFixture(t)
		dest := filepath.Join(cwd, ".agents", "soho.conf")
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runIn(t, []string{"config", "set", "layout", "tab"}, env, cwd)
		if code != 4 || out != "" || !strings.Contains(errOut, "config set: could not rewrite "+dest+" (file left untouched)") {
			t.Fatalf("directory destination code=%d out=%q err=%q", code, out, errOut)
		}
		if info, err := os.Stat(dest); err != nil || !info.IsDir() {
			t.Fatalf("destination changed: %v err=%v", info, err)
		}
		entries, err := os.ReadDir(filepath.Dir(dest))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".soho.conf.") && strings.HasSuffix(entry.Name(), ".tmp") {
				t.Fatalf("failed config write left temporary file %q", entry.Name())
			}
		}
	})
	t.Run("destination unreadable", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("permission denial is not observable for this process")
		}
		env, cwd := commandFixture(t)
		dest := filepath.Join(cwd, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte("layout=split\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dest, 0); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runIn(t, []string{"config", "set", "layout", "tab"}, env, cwd)
		if code != 4 || out != "" || !strings.Contains(errOut, "config set: could not rewrite "+dest+" (file left untouched)") {
			t.Fatalf("unreadable destination code=%d out=%q err=%q", code, out, errOut)
		}
		if err := os.Chmod(dest, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(dest); err != nil || string(got) != "layout=split\n" {
			t.Fatalf("destination changed after failed read: %q err=%v", got, err)
		}
	})
	t.Run("agents is a file", func(t *testing.T) {
		env, cwd := commandFixture(t)
		if err := os.WriteFile(filepath.Join(cwd, ".agents"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(cwd, ".agents", "soho.conf")
		code, out, errOut := runIn(t, []string{"config", "set", "layout", "tab"}, env, cwd)
		if code != 4 || out != "" || !strings.Contains(errOut, "config set: could not rewrite "+dest) {
			t.Fatalf("agents file code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run("user directory parent is a file", func(t *testing.T) {
		env, cwd := commandFixture(t)
		parent := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho")
		if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(parent, "config")
		code, out, errOut := runIn(t, []string{"config", "set", "layout", "tab", "--user"}, env, cwd)
		if code != 4 || out != "" || !strings.Contains(errOut, "config set: could not rewrite "+dest) {
			t.Fatalf("user parent file code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run("session state parent is a file", func(t *testing.T) {
		env, cwd := commandFixture(t)
		state := env.Get("SOHO_DIR")
		if err := os.RemoveAll(state); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(state, "ws", "session.conf")
		code, out, errOut := runIn(t, []string{"session", "set", "layout", "tab"}, env, cwd)
		if code != 4 || out != "" || !strings.Contains(errOut, "session set: could not rewrite "+dest) {
			t.Fatalf("session parent file code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run("session file directory", func(t *testing.T) {
		env, cwd := commandFixture(t)
		file := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
		if err := os.MkdirAll(file, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runIn(t, []string{"session", "clear"}, env, cwd)
		if code != 4 || out != "" || !strings.Contains(errOut, "session clear: could not clear "+file) {
			t.Fatalf("session directory code=%d out=%q err=%q", code, out, errOut)
		}
		if info, err := os.Stat(file); err != nil || !info.IsDir() {
			t.Fatalf("session directory removed: %v err=%v", info, err)
		}
	})
	t.Run("session file unreadable clear key", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("permission denial is not observable for this process")
		}
		env, cwd := commandFixture(t)
		file := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("layout=split\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, 0); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runIn(t, []string{"session", "clear", "layout"}, env, cwd)
		if code != 4 || out != "" || !strings.Contains(errOut, "session clear: could not rewrite "+file+" (file left untouched)") {
			t.Fatalf("unreadable session code=%d out=%q err=%q", code, out, errOut)
		}
		if err := os.Chmod(file, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(file); err != nil || string(got) != "layout=split\n" {
			t.Fatalf("session file changed: %q err=%v", got, err)
		}
	})
	t.Run("session set destination directory", func(t *testing.T) {
		env, cwd := commandFixture(t)
		file := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
		if err := os.MkdirAll(file, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runIn(t, []string{"session", "set", "layout", "tab"}, env, cwd)
		if code != 4 || out != "" || !strings.Contains(errOut, "session set: could not rewrite "+file) {
			t.Fatalf("session set directory code=%d out=%q err=%q", code, out, errOut)
		}
		if info, err := os.Stat(file); err != nil || !info.IsDir() {
			t.Fatalf("session destination changed: %v err=%v", info, err)
		}
	})
}
