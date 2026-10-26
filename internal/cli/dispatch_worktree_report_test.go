package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-379: a dispatch run from a shell inside a linked worktree, for a
// worker opened in that same worktree, used to compare the worker's cwd
// with the caller's project root — inside the worktree both coincide, so
// the report landed in the state dir, which sits in the main checkout and
// a sandboxed worker cannot write. The rule keeps the report and the
// composed prompt in the state dir only when that dir is inside the
// worker's cwd; otherwise they route through $TMPDIR/soho/<ws>/reports,
// and the readers (wait, stats, the family lookup) already scan both.
func TestDispatchWorktreeReportRoute(t *testing.T) {
	root := t.TempDir()
	var err error
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	// Real git fixture: the main checkout and a linked worktree.
	main := filepath.Join(root, "main")
	if err := os.MkdirAll(main, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %s: %v", args, out, e)
		}
	}
	git(main, "init", "-q")
	git(main, "config", "user.email", "test@example.invalid")
	git(main, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(main, "tracked"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(main, "add", "tracked")
	git(main, "commit", "-qm", "initial")
	wt := filepath.Join(main, ".worktrees", "wt")
	if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
		t.Fatal(err)
	}
	git(main, "worktree", "add", "-qb", "wt", wt)

	// The state dir sits in the main checkout, the root the real worktree
	// resolution gives it; SOHO_DIR pins that root for the fixture.
	sohoDir := filepath.Join(main, ".soho")
	sd := filepath.Join(sohoDir, "ws")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	row := func(name, pane, cwd string) string {
		return strings.Join([]string{name, pane, "codex", "implementer", "anthropic", "", cwd, "now", "model-x", "full", "implementer", "build"}, "\t")
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" +
		row("a-wt", "p1", wt) + "\n" +
		row("b-wt", "p2", wt) + "\n" +
		row("c-main", "p3", main) + "\n" +
		row("d-main", "p4", main) + "\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(root, "tmp")
	roles := filepath.Join(root, "roles")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(roles, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(wt, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nrun the slice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		{AnyArgs: true, Stdout: ""},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	clean := envFrom(testutil.CleanEnv(t))
	for key := range clean {
		if strings.HasPrefix(key, "SOHO_") {
			delete(clean, key)
		}
	}
	clean = withFakeCLI(clean, bin)
	clean["HOME"] = clean["HOME"]
	clean["PATH"] = bin
	clean["HERDR_ENV"] = "1"
	clean["HERDR_WORKSPACE_ID"] = "ws"
	clean["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing.sock")
	clean["TMPDIR"] = tmp
	clean["SOHO_DIR"] = sohoDir
	clean["SOHO_SKILL_DIR"] = testSkillDir(t)
	clean["SOHO_ROLES"] = roles
	clean["SOHO_BRIEF_LINT"] = "off"
	clean["SOHO_WAIT_POLL_MS"] = "1"
	clean["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	clean["SOHO_PROMPT_SETTLE_SECONDS"] = "0"

	under := func(p, dir string) bool {
		return strings.HasPrefix(p, dir+string(filepath.Separator))
	}
	dispatch := func(agent, callerCwd string) (composed, report string) {
		t.Helper()
		code, out, stderr := runIn(t, []string{"dispatch", agent, brief, "--no-wait"}, clean, callerCwd)
		if code != 0 {
			t.Fatalf("dispatch %s (cwd %s) code=%d stderr=%q", agent, callerCwd, code, stderr)
		}
		value, e := jsonjs.Parse([]byte(strings.TrimSpace(out)))
		if e != nil {
			t.Fatalf("dispatch %s output %q is not a single JSON line: %v", agent, out, e)
		}
		obj, ok := value.(*jsonjs.Object)
		if !ok {
			t.Fatalf("dispatch %s output %q is not a JSON object", agent, out)
		}
		composedValue, _ := obj.Get("composed_prompt")
		composed, _ = composedValue.(string)
		reportValue, _ := obj.Get("report")
		report, _ = reportValue.(string)
		if composed == "" || report == "" {
			t.Fatalf("dispatch %s missing paths: %q", agent, out)
		}
		return composed, report
	}

	t.Run("caller in the worktree, worker in the worktree, sd in the main checkout: report and composed prompt route to TMPDIR and wait settles there", func(t *testing.T) {
		composed, report := dispatch("a-wt", wt)
		base := filepath.Join(tmp, "soho", "ws", "reports")
		if !under(report, base) || !under(composed, base) {
			t.Fatalf("report=%q composed=%q, want both under %s", report, composed, base)
		}
		// The dispatch itself must have left no pair in the main checkout's
		// sd. (The wait below mirrors the pair back into the sd by design,
		// so this check runs before it.)
		for _, dir := range []string{filepath.Join(sd, "briefs"), filepath.Join(sd, "reports")} {
			entries, _ := os.ReadDir(dir)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "a-wt-") {
					t.Fatalf("a-wt pair landed in the main checkout state dir: %s", filepath.Join(dir, entry.Name()))
				}
			}
		}
		if err := os.WriteFile(report, []byte("done report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runIn(t, []string{"wait", "a-wt", "--timeout", "500"}, clean, wt)
		if code != 0 {
			t.Fatalf("wait code=%d stderr=%q", code, stderr)
		}
		settled, e := jsonjs.Parse([]byte(strings.TrimSpace(out)))
		if e != nil {
			t.Fatalf("wait output %q is not a single JSON line: %v", out, e)
		}
		obj, ok := settled.(*jsonjs.Object)
		if !ok {
			t.Fatalf("wait output %q is not a JSON object", out)
		}
		status, _ := obj.Get("status")
		settledReport, _ := obj.Get("report")
		if status != "done" || settledReport != report {
			t.Fatalf("wait did not settle done on the TMPDIR report: out=%q", out)
		}
	})
	t.Run("caller at the main checkout root, worker in the worktree: TMPDIR, as today", func(t *testing.T) {
		composed, report := dispatch("b-wt", main)
		base := filepath.Join(tmp, "soho", "ws", "reports")
		if !under(report, base) || !under(composed, base) {
			t.Fatalf("report=%q composed=%q, want both under %s", report, composed, base)
		}
	})
	t.Run("caller and worker at the main checkout root, sd inside the worker: the report stays in the sd, as today", func(t *testing.T) {
		composed, report := dispatch("c-main", main)
		if !under(report, filepath.Join(sd, "reports")) || !under(composed, filepath.Join(sd, "briefs")) {
			t.Fatalf("report=%q composed=%q, want report under %s and composed under %s", report, composed, filepath.Join(sd, "reports"), filepath.Join(sd, "briefs"))
		}
	})
	t.Run("caller in the worktree, worker at the main checkout root, sd inside the worker: the report stays in the sd", func(t *testing.T) {
		composed, report := dispatch("d-main", wt)
		if !under(report, filepath.Join(sd, "reports")) || !under(composed, filepath.Join(sd, "briefs")) {
			t.Fatalf("report=%q composed=%q, want report under %s and composed under %s", report, composed, filepath.Join(sd, "reports"), filepath.Join(sd, "briefs"))
		}
	})
}

// DJA-379 review: a worker whose cwd is the filesystem root contains every
// state dir, so the report and the composed prompt stay in the state dir
// (the "/" parent must not turn into the prefix "//" and push them to
// TMPDIR).
func TestDispatchWorkerAtRootKeepsReportInState(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_SKILL_DIR"] = testSkillDir(t)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	cwd := filepath.VolumeName(f.root) + string(filepath.Separator)
	roster := filepath.Join(f.state, "ws", "agents.tsv")
	data, err := os.ReadFile(roster)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	cols := strings.Split(lines[1], "\t")
	cols[6] = cwd
	lines[1] = strings.Join(cols, "\t")
	if err := os.WriteFile(roster, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runIn(t, []string{"dispatch", "worker", f.brief, "--no-wait"}, f.env, cwd)
	if code != 0 {
		t.Fatalf("dispatch code=%d out=%s stderr=%q", code, out, stderr)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("dispatch output is not a JSON object: %s (%v)", out, err)
	}
	report, _ := obj["report"].(string)
	composed, _ := obj["composed_prompt"].(string)
	stateReports := filepath.Join(f.state, "ws", "reports")
	if filepath.Dir(report) != stateReports {
		t.Fatalf("worker cwd %q contains the state dir, but report=%q left %q", cwd, report, stateReports)
	}
	if filepath.Dir(composed) != filepath.Join(f.state, "ws", "briefs") || strings.HasSuffix(composed, ".brief.md") {
		t.Fatalf("composed prompt %q left the state briefs dir", composed)
	}
}
