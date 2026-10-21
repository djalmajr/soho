package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// packageDir is the directory supplied by go test, before suite isolation.
var packageDir string
var packageDirErr error

func TestMain(m *testing.M) {
	// CLI fixtures must not depend on this host's disk/swap pressure. Tests
	// of the warning override these existing hooks explicitly and restore
	// this calm default; production measurements are unchanged.
	platform.DiskFree = func(string) (int64, int64, bool) { return 50, 100, true }
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return 0, 100, true }
	managedCLIHelperMain()
	fakecli.Main()
	fakecli.GuardSuiteRun()
	// These test helpers keep their caller's cwd and environment. Managed
	// CLI and init-based fakes have already dispatched before this point.
	for _, key := range []string{
		procsHelperRoleEnv,            // TestProcFixtureHelper runs native process roles.
		explainFootprintHelperRoleEnv, // TestExplainFootprintHelper stands in for a worker.
		compactPhaseLivenessChildEnv,  // TestCompactPhaseLivenessChild sleeps, then exits.
		f11CompactPhaseChildEnv,       // TestF11CompactPhaseChild is killed during dispatch.
	} {
		if os.Getenv(key) != "" {
			fakecli.RunTests(m)
			return
		}
	}
	packageDir, packageDirErr = os.Getwd()
	if packageDirErr != nil {
		fmt.Fprintln(os.Stderr, packageDirErr)
		os.Exit(1)
	}
	// Tests re-run this binary through os.Args[0]: a relative path (a binary
	// run as ./cli.test) would stop resolving once the suite leaves the
	// package directory, so make it absolute first. A bare name is looked up
	// in PATH and stays as it is.
	if !filepath.IsAbs(os.Args[0]) && strings.ContainsAny(os.Args[0], `/\`) {
		if abs, err := filepath.Abs(os.Args[0]); err == nil {
			os.Args[0] = abs
		}
	}
	root, err := os.MkdirTemp("", "soho-cli-suite-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	fixtureIsolationRoot = root
	cleanup := func() {
		if err := os.Chdir(packageDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	for _, name := range []string{"cwd", "home", "conf", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			cleanup()
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	for key, value := range map[string]string{
		"HOME": filepath.Join(root, "home"), "USERPROFILE": filepath.Join(root, "home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "conf"),
		"TMPDIR":          filepath.Join(root, "tmp"), "TMP": filepath.Join(root, "tmp"), "TEMP": filepath.Join(root, "tmp"),
		// Go 1.27's testing.TempDir prefers GOTMPDIR over os.TempDir.
		"GOTMPDIR": filepath.Join(root, "tmp"),
	} {
		if err := os.Setenv(key, value); err != nil {
			cleanup()
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := os.Chdir(filepath.Join(root, "cwd")); err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakecli.RunTests(m, cleanup)
}

func TestStateCommandParity(t *testing.T) {
	t.Run("roster reads live agent objects and records panes before skipping nameless rows", func(t *testing.T) { // Mutation captured: refusing *jsonjs.Object makes the worker gone and lists the nameless pane owner.
		env, _ := commandFixture(t)
		state := filepath.Join(env.Get("SOHO_DIR"), "ws")
		if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("# header\nworker\tp1\tclaude\timplementer\t\t\t/w\n\tp2\t\t\t\t\t/w\n"), 0o600)
		_ = os.WriteFile(filepath.Join(state, "task-worker"), []byte("do x\r\n"), 0o600)
		fakeDir := t.TempDir()
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker","pane_id":"p1","agent_status":"working","agent":"claude"},{"name":"orphan","pane_id":"p2","agent_status":"idle","agent":"codex"}]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"p1","tab_id":"t1"},{"pane_id":"p2","tab_id":"t2"}]}}`},
			{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[{"tab_id":"t1","label":"main"},{"tab_id":"t2","label":"other"}]}}`},
		}
		_, err := fakecli.Install(t, fakeDir, "herdr", rules)
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, fakeDir)
		env["HERDR_ENV"] = "1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"roster"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		fields := strings.Fields(strings.Split(out.String(), "\n")[1])
		if code != 0 || stderr.Len() != 0 || len(fields) < 9 || fields[0] != "worker" || fields[5] != "working" || fields[8] != "do" || !strings.Contains(out.String(), "do x\r") || strings.Contains(out.String(), "orphan") {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
	})
	t.Run("roster pane and tab identifiers compare with JavaScript strict equality", func(t *testing.T) { // Mutation captured: fmt.Sprint equality treats numeric pane IDs as strings.
		env, _ := commandFixture(t)
		state := filepath.Join(env.Get("SOHO_DIR"), "ws")
		if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("# header\nworker\t1\tclaude\timplementer\n"), 0o600)
		fakeDir := t.TempDir()
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker","pane_id":1,"agent_status":"working","agent":"claude"}]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":1,"tab_id":2}]}}`},
			{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[{"tab_id":"2","label":"wrong"}]}}`},
		}
		_, err := fakecli.Install(t, fakeDir, "herdr", rules)
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, fakeDir)
		env["HERDR_ENV"] = "1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"roster"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		fields := strings.Fields(strings.Split(out.String(), "\n")[1])
		if code != 0 || stderr.Len() != 0 || len(fields) < 6 || fields[4] != "-" || fields[5] != "gone" || strings.Contains(out.String(), "wrong") {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
	})
	t.Run("clean preserves live objects and does not match null pane IDs to empty panes", func(t *testing.T) { // Mutation captured: coercing null pane_id to an empty string keeps the dead row and drops the live one.
		env, _ := commandFixture(t)
		state := filepath.Join(env.Get("SOHO_DIR"), "ws")
		if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("# header\nlive\tp1\tclaude\timplementer\nnull-pane\t\tclaude\timplementer\n"), 0o600)
		_ = os.WriteFile(filepath.Join(state, "last-report-live"), []byte("reports/live.md\n"), 0o600)
		_ = os.WriteFile(filepath.Join(state, "wait", "live.json"), []byte("{}\n"), 0o600)
		_ = os.WriteFile(filepath.Join(state, "wait", "null-pane.json"), []byte("{}\n"), 0o600)
		fakeDir := t.TempDir()
		_, err := fakecli.Install(t, fakeDir, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"live","pane_id":"p1","agent_status":"working"},{"name":"unrelated","pane_id":null,"agent_status":"idle"}]}}`}})
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, fakeDir)
		env["HERDR_ENV"] = "1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"clean"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 0 || !strings.Contains(out.String(), "dropped gone agent null-pane\n") || strings.Contains(out.String(), "dropped gone agent live") || stderr.Len() != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
		if len(core.RosterRows(state)) != 1 || core.RosterRows(state)[0][:4] != "live" {
			t.Fatalf("roster=%q", core.RosterRows(state))
		}
		for _, path := range []string{filepath.Join(state, "last-report-live"), filepath.Join(state, "wait", "live.json")} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("live state %s missing: %v", path, err)
			}
		}
		if _, err := os.Stat(filepath.Join(state, "wait", "null-pane.json")); !os.IsNotExist(err) {
			t.Fatalf("null-pane wait file remains: %v", err)
		}
	})
	t.Run("recovered command errors append one friction error row", func(t *testing.T) { // Mutation captured: removing friction logging from Run's recover leaves the error row absent.
		env, _ := commandFixture(t)
		fakeDir := t.TempDir()
		_, err := fakecli.Install(t, fakeDir, "herdr", nil)
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, fakeDir)
		env["HERDR_ENV"] = "1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"friction", "bogus"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		log, readErr := os.ReadFile(filepath.Join(env.Get("SOHO_DIR"), "ws", "friction.log"))
		if code != 2 || readErr != nil || strings.Count(string(log), "error(exit 2)\tfriction\tfriction: unknown subcommand 'bogus'\n") != 1 || stderr.String() != "soho: friction: unknown subcommand 'bogus'\n" {
			t.Fatalf("code=%d log=%q stderr=%q readErr=%v", code, log, stderr.String(), readErr)
		}
	})
	t.Run("friction add records a sanitized note row and prints recorded", func(t *testing.T) { // JS: "friction add: the text is sanitized into one four-column line"
		env, cwd := commandFixture(t)
		dir := t.TempDir()
		_, err := fakecli.Install(t, dir, "herdr", nil)
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, dir)
		env["HERDR_ENV"] = "1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"friction", "add", "one\nline"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 0 || stderr.String() != "" || out.String() != "recorded\n" {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
		log, err := os.ReadFile(filepath.Join(env.Get("SOHO_DIR"), "ws", "friction.log"))
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(strings.TrimSpace(string(log)), "\t")
		if len(fields) != 4 || fields[1] != "note" || fields[2] != "friction" || fields[3] != "one line" {
			t.Fatalf("log=%q", log)
		}
		_ = cwd
	})
	t.Run("title joins positionals, stores the title, and prints ordered JSON", func(t *testing.T) { // JS: "title: the positionals are joined with one space and stored once"
		env, _ := commandFixture(t)
		dir := t.TempDir()
		_, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{{Argv: []string{"pane", "report-metadata", "p1", "--source", "soho", "--title", "orchestrator: a b"}}})
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, dir)
		env["HERDR_ENV"], env["HERDR_PANE_ID"] = "1", "p1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"title", "a", "b"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		want := "{\n  \"pane_id\": \"p1\",\n  \"title\": \"orchestrator: a b\"\n}\n"
		if code != 0 || out.String() != want || stderr.String() != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
	})
	t.Run("roster renders the header and configuration footer from an empty live list", func(t *testing.T) { // JS: "roster: prints the NAME/ROLE/KIND/PANE/TAB/STATE/REPORT/CWD table"
		env, _ := commandFixture(t)
		dir := t.TempDir()
		rules := []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}, {Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`}, {Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`}}
		_, err := fakecli.Install(t, dir, "herdr", rules)
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, dir)
		env["HERDR_ENV"] = "1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"roster"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 0 || !strings.Contains(out.String(), "NAME                 ROLE               KIND") || !strings.HasSuffix(out.String(), "layout=split reuse_workers=on multi_role=on auto_approve=off\n") || stderr.String() != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
	})
	t.Run("clean drops gone roster rows and reports old file cleanup", func(t *testing.T) { // Mutation captured: removing the .current.md exclusion and last-report keep check deletes protected files.
		env, cwd := commandFixture(t)
		state := filepath.Join(env.Get("SOHO_DIR"), "ws")
		if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("# header\ngone\tp1\tgrok\timplementer\nlive\tp2\tclaude\timplementer\n"), 0o600)
		_ = os.WriteFile(filepath.Join(state, "wait", "gone.x"), []byte("x"), 0o600)
		old := time.Now().Add(-10 * 24 * time.Hour)
		_ = os.MkdirAll(filepath.Join(state, "reports"), 0o700)
		oldFile := filepath.Join(state, "reports", "old.md")
		_ = os.WriteFile(oldFile, []byte("x"), 0o600)
		_ = os.Chtimes(oldFile, old, old)
		currentFile := filepath.Join(state, "reports", "keep.current.md")
		_ = os.WriteFile(currentFile, []byte("x"), 0o600)
		_ = os.Chtimes(currentFile, old, old)
		keptFile := filepath.Join(state, "reports", "kept.md")
		_ = os.WriteFile(keptFile, []byte("x"), 0o600)
		_ = os.Chtimes(keptFile, old, old)
		_ = os.WriteFile(filepath.Join(state, "last-report-live"), []byte(keptFile), 0o600)
		dir := t.TempDir()
		_, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"live","pane_id":"p2"}]}}`}})
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, dir)
		env["HERDR_ENV"] = "1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"clean"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 0 || !strings.Contains(out.String(), "dropped gone agent gone\n") || !strings.Contains(out.String(), fmt.Sprintf("removed 1 files older than 7 days under %s\n", state)) || stderr.String() != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
		if rows := core.RosterRows(state); len(rows) != 1 || !strings.HasPrefix(rows[0], "live\t") {
			t.Fatalf("roster after clean=%q", rows)
		}
		if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
			t.Fatalf("old file remains: %v", err)
		}
		if _, err := os.Stat(currentFile); err != nil {
			t.Fatalf("current file removed: %v", err)
		}
		if _, err := os.Stat(keptFile); err != nil {
			t.Fatalf("referenced file removed: %v", err)
		}
		_ = cwd
	})
	t.Run("title trim matches JavaScript and slices by UTF-16 units", func(t *testing.T) { // Mutation captured: Unicode IsSpace trimming strips U+0085 and rune slicing keeps 31 emoji.
		if got := normalizeObjective("\u0085x\u0085"); got != "\u0085x\u0085" {
			t.Fatalf("NEL trim=%q", got)
		}
		if got := normalizeObjective("\ufeff x \ufeff"); got != "x" {
			t.Fatalf("BOM trim=%q", got)
		}
		if got := normalizeObjective(strings.Repeat("😀", 31) + strings.Repeat("x", 30)); got != strings.Repeat("😀", 31)+strings.Repeat("x", 29) {
			t.Fatalf("UTF-16 slice rune count=%d value=%q", len([]rune(got)), got)
		}
	})
	t.Run("feedback send sanitizes project names per UTF-16 unit and replaces invalid UTF-8", func(t *testing.T) { // Mutation captured: rune replacement emits two hyphens for the emoji and raw copy preserves invalid bytes.
		root := t.TempDir()
		project := filepath.Join(root, "p😀")
		if err := os.MkdirAll(project, 0o700); err != nil {
			t.Fatal(err)
		}
		init := exec.Command("git", "init", "-q")
		init.Dir = project
		if output, err := init.CombinedOutput(); err != nil {
			t.Fatalf("git init: %s: %v", output, err)
		}
		dir := filepath.Join(root, "feedback")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(root, "report.md")
		if err := os.WriteFile(report, []byte{'a', 0xff, 0xfe, 0xc3, 'b'}, 0o600); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"HERDR_WORKSPACE_ID": "ws", "SOHO_FEEDBACK": "local", "SOHO_FEEDBACK_DIR": dir, "SOHO_DIR": filepath.Join(root, "state")}
		oldOut := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := cmdFeedback([]string{"send", report, "summary"}, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, project)
		platform.Stdout = oldOut
		if code != 0 || !strings.Contains(out.String(), `"status":"sent"`) {
			t.Fatalf("code=%d out=%q", code, out.String())
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "from-p---") {
			t.Fatalf("feedback entries=%v", entries)
		}
		data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		if err != nil || string(data) != "a\xef\xbf\xbd\xef\xbf\xbd\xef\xbf\xbdb" {
			t.Fatalf("feedback bytes=%x err=%v", data, err)
		}
	})
	t.Run("feedback collision exhaustion reports the JavaScript EEXIST code", func(t *testing.T) { // Mutation captured: reporting Go's file-exists text instead of EEXIST changes the command error contract.
		root := t.TempDir()
		project := filepath.Join(root, "project")
		if err := os.Mkdir(project, 0o700); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "feedback")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(root, "report.md")
		_ = os.WriteFile(report, []byte("report\n"), 0o600)
		now := platform.Now()
		base := fmt.Sprintf("from-project-%04d-%02d-%02d", now.Year(), now.Month(), now.Day())
		for i := 0; i < 26; i++ {
			name := base + ".md"
			if i > 0 {
				name = fmt.Sprintf("%s-%c.md", base, rune('a'+i))
			}
			_ = os.WriteFile(filepath.Join(dir, name), []byte("existing"), 0o600)
		}
		env := platform.Env{"SOHO_FEEDBACK": "local", "SOHO_FEEDBACK_DIR": dir}
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			cmdFeedback([]string{"send", report, "summary"}, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, project)
		}()
		exitErr, ok := recovered.(*platform.ExitError)
		wantCollision := "(EEXIST)"
		if !ok || exitErr.Code != 4 || !strings.Contains(exitErr.Msg, wantCollision) {
			t.Fatalf("recovered=%#v", recovered)
		}
	})
	t.Run("friction add diagnoses a log path that is a directory", func(t *testing.T) { // Mutation captured: ignoring OpenFile errors records success when friction.log is a directory.
		root := t.TempDir()
		state := filepath.Join(root, "state", "ws")
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(state, "friction.log"), 0o700); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"SOHO_DIR": filepath.Join(root, "state"), "HERDR_WORKSPACE_ID": "ws", "SOHO_NOWRITE": "1"}
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			cmdFriction([]string{"add", "note"}, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, root)
		}()
		exitErr, ok := recovered.(*platform.ExitError)
		if !ok || exitErr.Code != 4 || !strings.Contains(exitErr.Msg, "friction add: could not write") {
			t.Fatalf("recovered=%#v", recovered)
		}
	})
	t.Run("clean refuses a wait path it cannot read before changing the roster", func(t *testing.T) { // Mutation captured: ignoring ReadDir failure removes a gone roster entry.
		root := t.TempDir()
		state := filepath.Join(root, "state", "ws")
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		roster := filepath.Join(state, "agents.tsv")
		_ = os.WriteFile(roster, []byte("# header\ngone\tp1\n"), 0o600)
		_ = os.WriteFile(filepath.Join(state, "wait"), []byte("not a directory"), 0o600)
		fakeDir := t.TempDir()
		_, err := fakecli.Install(t, fakeDir, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}})
		if err != nil {
			t.Fatal(err)
		}
		env := withFakeCLI(platform.Env{"SOHO_DIR": filepath.Join(root, "state"), "HERDR_WORKSPACE_ID": "ws", "SOHO_NOWRITE": "1"}, fakeDir)
		env["HERDR_ENV"] = "1"
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			cmdClean(nil, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, root)
		}()
		if exitErr, ok := recovered.(*platform.ExitError); !ok || exitErr.Code != 4 {
			t.Fatalf("recovered=%#v", recovered)
		}
		if rows := core.RosterRows(state); len(rows) != 1 || !strings.HasPrefix(rows[0], "gone\t") {
			t.Fatalf("roster mutated after wait read failure: %q", rows)
		}
	})
	t.Run("feedback send files a report without notifying when feedback_to is empty", func(t *testing.T) { // JS: "feedback send: without feedback_to it files the report and prints the sent JSON"
		env, cwd := commandFixture(t)
		dir := filepath.Join(t.TempDir(), "feedback")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(t.TempDir(), "report.md")
		_ = os.WriteFile(report, []byte("report contents\n"), 0o600)
		env["SOHO_FEEDBACK"] = "local"
		env["SOHO_FEEDBACK_DIR"] = dir
		env["HERDR_ENV"] = "1"
		fakeDir := t.TempDir()
		_, err := fakecli.Install(t, fakeDir, "herdr", nil)
		if err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, fakeDir)
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"feedback", "send", report, "summary"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 0 || !strings.Contains(out.String(), `"status":"sent"`) || !strings.Contains(out.String(), `"notified":null`) || stderr.String() != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("feedback files=%v", entries)
		}
		data, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		if string(data) != "report contents\n" {
			t.Fatalf("content=%q", data)
		}
		_ = cwd
	})
}

func TestTabLabelStringUsesJavaScriptNumberFormatting(t *testing.T) {
	// Mutation captured: fmt.Sprint renders the same large float in scientific notation instead of JavaScript String(number).
	if got := tabLabelString(float64(12345678901234567890)); got != "1234567890123456" {
		t.Fatalf("large numeric tab label=%q, want 1234567890123456", got)
	}
	if got := tabLabelString("12345678901234567890"); got != "1234567890123456" {
		t.Fatalf("string tab label=%q, want unchanged string prefix", got)
	}
}

func TestFeedbackOpenErrorCodeRecognizesWrappedFilesystemErrors(t *testing.T) {
	// Mutation captured: comparing platform errno constants misses Windows errors that match fs.ErrExist through errors.Is.
	wrappedExist := fmt.Errorf("create report: %w", fs.ErrExist)
	if got := feedbackOpenErrorCode(wrappedExist); got != "EEXIST" {
		t.Fatalf("wrapped fs.ErrExist code=%q", got)
	}
	wrappedPermission := fmt.Errorf("create report: %w", fs.ErrPermission)
	if got := feedbackOpenErrorCode(wrappedPermission); got != "EACCES" {
		t.Fatalf("wrapped fs.ErrPermission code=%q", got)
	}
}

func withFakeCLI(env platform.Env, dir string) platform.Env {
	list := fakecli.Env(env.List(), dir)
	out := platform.Env{}
	for _, item := range list {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}
