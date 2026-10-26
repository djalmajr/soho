package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-318 C2b: when the worker's cwd is outside the project, dispatch
// routes the report through $TMPDIR/soho/<workspace>/reports — and wait
// settles done by reading that same report path (the old $TMPDIR/herdr-soho
// scratch name is gone).
func TestDispatchTmpSohoReportIsReadByWait(t *testing.T) {
	skillDir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	state := filepath.Join(root, "state")
	bin := filepath.Join(root, "bin")
	tmp := filepath.Join(root, "tmp")
	roles := filepath.Join(root, "roles")
	for _, dir := range []string{repo, bin, tmp, roles, filepath.Join(state, "ws")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(repo, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nrun the slice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The worker's cwd is /work: outside the fixture repo, so the report
	// routes through the TMPDIR scratch directory.
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp1\tcodex\timplementer\tanthropic\t\t/work\tnow\tmodel-x\tfull\timplementer\tbuild\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
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
	clean["SOHO_DIR"] = state
	clean["SOHO_SKILL_DIR"] = skillDir
	clean["SOHO_ROLES"] = roles
	clean["SOHO_BRIEF_LINT"] = "off"
	clean["SOHO_WAIT_POLL_MS"] = "1"
	clean["SOHO_PROMPT_CHECK_SECONDS"] = "0"

	code, out, stderr := runIn(t, []string{"dispatch", "worker", brief, "--no-wait"}, clean, repo)
	if code != 0 {
		t.Fatalf("dispatch code=%d stderr=%q", code, stderr)
	}
	line := strings.TrimSpace(out)
	value, err := jsonjs.Parse([]byte(line))
	if err != nil {
		t.Fatalf("dispatch JSON %q: %v", line, err)
	}
	report, _ := value.(*jsonjs.Object).Get("report")
	reportPath, _ := report.(string)
	base := filepath.Join(tmp, "soho", "ws", "reports")
	if !strings.HasPrefix(reportPath, base+string(filepath.Separator)) {
		t.Fatalf("report path %q, want it under %s (the soho scratch dir)", reportPath, base)
	}
	if err := os.WriteFile(reportPath, []byte("done report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runIn(t, []string{"wait", "worker", "--timeout", "500"}, clean, repo)
	if code != 0 {
		t.Fatalf("wait code=%d stderr=%q", code, stderr)
	}
	// Compare the decoded fields: the JSON line escapes the backslashes of a
	// Windows path, so the raw path is not a substring of it.
	settled, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
	if err != nil {
		t.Fatalf("wait JSON %q: %v", out, err)
	}
	status, _ := settled.(*jsonjs.Object).Get("status")
	settledReport, _ := settled.(*jsonjs.Object).Get("report")
	if status != "done" || settledReport != reportPath {
		t.Fatalf("wait did not settle done on the soho-scratch report: out=%q", out)
	}
}
