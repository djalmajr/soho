package wait

// R329: `soho wait --observe` is the probe the --wake child runs. It keeps
// the loop, the exit codes and every status of a foreground wait, but leaves
// a done report unsettled: no task state, metrics, warning marker, mirror or
// git audit, so the orchestrator's own wait after the wake settles it with
// every field.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const observeReportBody = "# Report\n\n| item | state |\n| --- | --- |\n| slice a | [done] |\n| slice b | [partial] |\n"

type observeFixture struct {
	*wakeFixture
	report  string
	stem    string
	sidecar string
	task    string
	repo    string
}

// observeGit runs git in dir with a minimal environment of its own.
func observeGit(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{
		"PATH=" + testutil.HostPath(), "HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newObserveFixture is a settled worker: its report sits in the private
// scratch (so a settling wait mirrors it into the state dir), it has a task
// file, a recorded git tree whose HEAD has moved by a local commit, and
// metrics=on. It is also an eligible diff audit: the sidecar records an edit
// role, the composed brief (aged an hour, so it is the dispatch cutoff) owns
// only owned.txt, and the tree holds an owned and an outside untracked file
// written after that cutoff and before the report. The fake hc answers every probe with an idle
// worker.
func newObserveFixture(t *testing.T, extra ...fakecli.Rule) *observeFixture {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	f := newHcWakeFixture(t, "1", "none")
	f.ctx.Entries["metrics"] = core.ConfigEntry{Value: "on"}
	f.env["PATH"] = f.bin + string(os.PathListSeparator) + filepath.Dir(gitPath)
	if runtime.GOOS == "windows" {
		// Git for Windows needs SystemRoot, which this minimal env lacks.
		f.env["SystemRoot"] = testutil.HostSystemRoot()
	}
	f.installHc(t, append([]fakecli.Rule{{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeIdle7}}, extra...)...)
	dir, err := core.PrivateScratchReports(f.env, "sW", core.ScratchCreate)
	if err != nil {
		t.Fatal(err)
	}
	o := &observeFixture{wakeFixture: &wakeFixture{hcWakeFixture: f}}
	o.stem = "worker-20261020T030000"
	o.report = filepath.Join(dir, o.stem+".md")
	o.task = filepath.Join(f.sd, "task-worker")
	o.sidecar = filepath.Join(f.sd, "briefs", o.stem+".dispatch.json")
	o.repo = filepath.Join(f.base, "repo")
	for _, d := range []string{o.repo, filepath.Dir(o.sidecar)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(f.base, "home")
	observeGit(t, home, o.repo, "init", "-q", "-b", "main")
	observeGit(t, home, o.repo, "commit", "-q", "--allow-empty", "-m", "base")
	old := observeGit(t, home, o.repo, "rev-parse", "HEAD")
	observeGit(t, home, o.repo, "commit", "-q", "--allow-empty", "-m", "worker commit")
	// The git audit runs git with the fixture env; fail here, with git's own
	// output, when that env cannot run it, instead of later as a missing field.
	if r := platform.RunCli("git", []string{"-C", o.repo, "rev-parse", "HEAD"}, platform.RunOptions{Env: f.env, TimeoutMs: 10000}); r.Status == nil || *r.Status != 0 {
		t.Fatalf("git cannot run with the fixture env: stdout=%q stderr=%q error=%q", r.Stdout, r.Stderr, r.Error)
	}
	// The changes precede the report, or the wait would hold the done report
	// back as "changed after writing its report".
	for _, name := range []string{"owned.txt", "outside.txt"} {
		if err := os.WriteFile(filepath.Join(o.repo, name), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	brief := filepath.Join(filepath.Dir(o.sidecar), o.stem+".md")
	for path, body := range map[string]string{
		o.report:  observeReportBody,
		o.task:    "the task\n",
		o.sidecar: observeSidecar(t, old, o.repo),
		brief:     "# Role\nrole text\n# Brief\nDo it.\n# Owned files\n- `owned.txt`\n# Report contract\nDone.\n",
		filepath.Join(f.sd, "last-report-worker"): o.report + "\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().Add(-time.Hour)
	if err := os.Chtimes(brief, cutoff, cutoff); err != nil {
		t.Fatal(err)
	}
	return o
}

// observeSidecar is the dispatch sidecar of the fixture, marshaled so a
// Windows path (with backslashes) stays valid JSON.
func observeSidecar(t *testing.T, head, repo string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{"git_head": head, "git_branch": "main", "git_tree": repo, "role": "implementer"})
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func (o *observeFixture) read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (o *observeFixture) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (o *observeFixture) markers() []string {
	out := []string{}
	for _, suffix := range []string{"partial-warned", "env-warned", "metrics-recorded", "test-layers-warned", "finding-marks-warned", "review-responses-warned"} {
		if o.exists(filepath.Join(o.sd, "wait", "worker."+suffix)) {
			out = append(out, suffix)
		}
	}
	for _, path := range []string{o.sidecar + ".git-checked", o.sidecar + ".diff-checked"} {
		if o.exists(path) {
			out = append(out, filepath.Base(path))
		}
	}
	return out
}

// Acceptance 7: with a report present, --observe exits 0 with the observe
// line and none of the settling side effects; the ordinary wait after it
// settles with the audit fields.
func TestObserveLeavesTheReportUnsettled(t *testing.T) {
	o := newObserveFixture(t)
	taskBefore, sidecarBefore := o.read(t, o.task), o.read(t, o.sidecar)
	mirrored := filepath.Join(o.sd, "reports", o.stem+".md")
	for round := 1; round <= 2; round++ {
		code, msg, out, stderr := o.cmd(t, "worker", "--observe", "--timeout", "5000")
		if code != 0 || msg != "" {
			t.Fatalf("round %d: code=%d msg=%q stderr=%q", round, code, msg, stderr)
		}
		want := map[string]any{"agent": "worker", "status": "done", "report": o.report, "observe": true}
		got := wakeJSON(t, out)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: observe line %v, want %v", round, got, want)
		}
		for _, key := range []string{"diff_stat", "outside_owned", "cited_unchanged"} {
			if _, has := got[key]; has {
				t.Fatalf("round %d: observe line carries the diff audit field %s: %v", round, key, got)
			}
		}
		if o.exists(o.sidecar + ".diff-checked") {
			t.Fatalf("round %d: the observe path consumed the diff audit", round)
		}
		if strings.Contains(stderr, "partial") || strings.Contains(stderr, "left a local commit") {
			t.Fatalf("round %d: stderr %q carries a settling warning", round, stderr)
		}
		if got := o.markers(); len(got) != 0 {
			t.Fatalf("round %d: settling markers written: %v", round, got)
		}
		if o.exists(filepath.Join(o.sd, "metrics.jsonl")) {
			t.Fatalf("round %d: a metrics line was recorded", round)
		}
		if got := o.read(t, o.task); got != taskBefore {
			t.Fatalf("round %d: task state changed: %q -> %q", round, taskBefore, got)
		}
		if got := o.read(t, o.sidecar); got != sidecarBefore {
			t.Fatalf("round %d: the dispatch sidecar changed: %q", round, got)
		}
		if o.exists(mirrored) {
			t.Fatalf("round %d: the report was mirrored into the state dir", round)
		}
	}

	// The orchestrator's own wait settles it with every field.
	code, msg, out, stderr := o.cmd(t, "worker", "--timeout", "5000")
	if code != 0 || msg != "" {
		t.Fatalf("ordinary wait: code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	got := wakeJSON(t, out)
	if _, observed := got["observe"]; observed || got["status"] != "done" || got["partial"] != float64(1) {
		t.Fatalf("ordinary wait line %v, want done with partial 1 and no observe key", got)
	}
	moved, ok := got["head_moved"].(map[string]any)
	if !ok || moved["branch_from"] != "main" || moved["to"] == moved["from"] {
		t.Fatalf("ordinary wait line %v, want the head_moved audit field", got)
	}
	if !strings.Contains(stderr, "marks 1 item(s) partial") || !strings.Contains(stderr, "left a local commit") {
		t.Fatalf("ordinary wait stderr %q, want the partial and local-commit warnings", stderr)
	}
	for _, suffix := range []string{"partial-warned", "metrics-recorded"} {
		if !o.exists(filepath.Join(o.sd, "wait", "worker."+suffix)) {
			t.Fatalf("ordinary wait left no %s marker", suffix)
		}
	}
	if !o.exists(o.sidecar + ".git-checked") {
		t.Fatal("ordinary wait did not run the git audit")
	}
	stat, ok := got["diff_stat"].(string)
	if !ok || stat == "" || !o.exists(stat) {
		t.Fatalf("ordinary wait line %v, want a diff_stat artifact", got)
	}
	if body := o.read(t, stat); !strings.Contains(body, "owned.txt") || !strings.Contains(body, "outside.txt") {
		t.Fatalf("diff_stat artifact %q lacks the changed files: %q", stat, body)
	}
	if outside, _ := got["outside_owned"].([]any); !reflect.DeepEqual(outside, []any{"outside.txt"}) {
		t.Fatalf("ordinary wait line %v, want outside_owned [outside.txt]", got)
	}
	if !strings.Contains(stderr, "outside its Owned files: outside.txt") {
		t.Fatalf("ordinary wait stderr %q, want the outside-owned warning", stderr)
	}
	if !o.exists(o.sidecar + ".diff-checked") {
		t.Fatal("ordinary wait did not run the diff audit")
	}
	if !strings.Contains(o.read(t, filepath.Join(o.sd, "metrics.jsonl")), `"agent":"worker"`) {
		t.Fatal("ordinary wait recorded no metrics line")
	}
	if got := o.read(t, o.task); got != taskBefore[:len(taskBefore)-1]+" ✓\n" {
		t.Fatalf("task state after the ordinary wait: %q", got)
	}
	if o.read(t, mirrored) != observeReportBody {
		t.Fatal("ordinary wait did not mirror the report into the state dir")
	}

	// Settling happens once: a third wait still says done, without the audit.
	_, _, out, _ = o.cmd(t, "worker", "--timeout", "5000")
	if again := wakeJSON(t, out); again["status"] != "done" || again["head_moved"] != nil || again["diff_stat"] != nil {
		t.Fatalf("second ordinary wait line %v, want done without head_moved or diff_stat", again)
	}
}

// --any under --observe returns on the first done report with the observe
// line, as a foreground --any does, and settles nothing.
func TestObserveAnyReturnsOnTheFirstDone(t *testing.T) {
	o := newObserveFixture(t)
	code, msg, out, stderr := o.cmd(t, "worker", "--any", "--observe", "--timeout", "5000")
	if code != 0 || msg != "" {
		t.Fatalf("code=%d msg=%q stderr=%q", code, msg, stderr)
	}
	if got := wakeJSON(t, out); got["observe"] != true || got["status"] != "done" {
		t.Fatalf("line %v", got)
	}
	if got := o.markers(); len(got) != 0 {
		t.Fatalf("markers written: %v", got)
	}
}

// Acceptance 7: blocked, quota, timeout and gone keep the exit codes and
// statuses of a foreground wait under --observe, and carry no observe key.
func TestObserveKeepsTheForegroundExitCodes(t *testing.T) {
	quotaScreen := `{"text":"Individual quota reached\nError: Connection error.\n"}`
	cases := []struct {
		name       string
		rules      []fakecli.Rule
		timeout    string
		wantCode   int
		wantStatus string
	}{
		{"blocked", []fakecli.Rule{{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeBlocked5}}, "5000", 7, "blocked"},
		{"quota", []fakecli.Rule{
			{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeIdle7},
			{Argv: []string{"read", "worker", "--lines", "0", "--json"}, Stdout: quotaScreen},
			{Argv: []string{"read", "worker", "--lines", "20", "--json"}, Stdout: quotaScreen},
			{Argv: []string{"read", "worker", "--lines", "40", "--json"}, Stdout: quotaScreen},
		}, "5000", 11, "quota"},
		{"timeout", []fakecli.Rule{{Argv: []string{"get", "worker", "--json"}, Stdout: hcWakeWorking5}}, "300", 9, "timeout"},
		{"gone", []fakecli.Rule{{Argv: []string{"get", "worker", "--json"}, Code: 3, Stderr: wakeNotFound}}, "5000", 6, "gone"},
	}
	for _, tc := range cases {
		for _, observe := range []bool{false, true} {
			name := tc.name + " foreground"
			argv := []string{"worker", "--timeout", tc.timeout}
			if observe {
				name = tc.name + " observe"
				argv = append(argv, "--observe")
			}
			t.Run(name, func(t *testing.T) {
				f := newHcWakeFixture(t, "1", "none")
				f.installHc(t, tc.rules...)
				code, msg, out, stderr := (&wakeFixture{hcWakeFixture: f}).cmd(t, argv...)
				if code != tc.wantCode || msg != "" {
					t.Fatalf("code=%d msg=%q stdout=%q stderr=%q, want exit %d", code, msg, out, stderr, tc.wantCode)
				}
				line := wakeJSON(t, out)
				if line["status"] != tc.wantStatus {
					t.Fatalf("status %v, want %s: %v", line["status"], tc.wantStatus, line)
				}
				if _, has := line["observe"]; has {
					t.Fatalf("a %s line carries the observe key: %v", tc.wantStatus, line)
				}
			})
		}
	}
}

// --observe is a flag of wait only: it needs no value, and --wake and
// --observe never combine (pinned in the refusals test).
func TestObserveFlagNeedsAnAgent(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t)
	code, msg, _, _ := (&wakeFixture{hcWakeFixture: f}).cmd(t, "--observe")
	if code != 2 || msg != "wait: give at least one agent name" {
		t.Fatalf("code=%d msg=%q", code, msg)
	}
}

func TestObserveAnyNamesUnwatchedAgents(t *testing.T) {
	o := newObserveFixture(t, fakecli.Rule{Argv: []string{"get", "complete", "--json"}, Stdout: hcWakeIdle7}, fakecli.Rule{Argv: []string{"get", "zother", "--json"}, Stdout: hcWakeWorking5})
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\ncomplete\tp0c\tclaude\timplementer\t\t\t\t\t\t\t\t\nzother\tp0b\tclaude\timplementer\t\t\t\t\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(o.sd, "agents.tsv"), []byte(roster), 0600); err != nil {
		t.Fatal(err)
	}
	completed := filepath.Join(o.base, "complete.md")
	if err := os.WriteFile(completed, []byte("done\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.sd, "last-report-complete"), []byte(completed+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A previous report exists for zother, but its current pointer names a missing report.
	stale := filepath.Join(o.base, "old.md")
	if err := os.WriteFile(stale, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.sd, "last-report-zother"), []byte(filepath.Join(o.base, "new.md")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, msg, out, stderr := o.cmd(t, "worker", "complete", "zother", "--observe", "--any", "--timeout", "5000")
	if code != 0 || msg != "" || !strings.Contains(out, `"unwatched":["zother"]`) || !strings.Contains(out, `soho wait zother --wake --any --timeout 5000`) {
		t.Fatalf("code=%d msg=%q out=%q stderr=%q", code, msg, out, stderr)
	}
}
