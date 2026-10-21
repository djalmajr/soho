package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/job"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// jobFix is one machine with an isolated HOME/XDG and a job_repos_root under
// a temporary tree; jobs are created through the internal/job API.
type jobFix struct {
	root, repos, home string
	env               platform.Env
}

func newJobFix(t *testing.T) *jobFix {
	t.Helper()
	root := t.TempDir()
	f := &jobFix{
		root:  root,
		repos: filepath.Join(root, "repos"),
		home:  filepath.Join(root, "home"),
	}
	for _, dir := range []string{f.home, filepath.Join(root, "xdg"), filepath.Join(root, "bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.env = platform.Env{
		"HOME": f.home, "USERPROFILE": f.home, "XDG_CONFIG_HOME": filepath.Join(root, "xdg"),
		"SOHO_SKILL_DIR": testSkillDir(t), "PATH": filepath.Join(root, "bin"),
	}
	userFile := platform.UserConfigPath(platform.Current(), f.env)
	if err := os.MkdirAll(filepath.Dir(userFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte("job_repos_root="+f.repos+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *jobFix) checkout(t *testing.T, org, repo string) string {
	t.Helper()
	dir := filepath.Join(f.repos, org, repo)
	if err := os.MkdirAll(filepath.Join(dir, ".soho", "jobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (f *jobFix) store(t *testing.T, checkout string) *job.Store {
	t.Helper()
	return job.Open(filepath.Join(checkout, ".soho"))
}

const jobTestBrief = `{"schema":1,"id":"job-1","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship the change.","aceite":[{"criterio":"tests pass","prova":"go test ./internal/cli"}]}`

func (f *jobFix) startJob(t *testing.T, checkout string) {
	t.Helper()
	if _, err := f.store(t, checkout).Start("job-1", []byte(jobTestBrief)); err != nil {
		t.Fatal(err)
	}
}

// setStatus writes the job's state.json directly (no lifecycle validation).
func (f *jobFix) setStatus(t *testing.T, checkout, id, status string) {
	t.Helper()
	body := fmt.Sprintf(`{"schema":1,"id":%q,"status":%q,"brief_sha256":"x","decisions_acked_seq":0,"motivo":null}`, id, status)
	if err := os.WriteFile(filepath.Join(checkout, ".soho", "jobs", id, "state.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *jobFix) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return f.runEnv(t, f.env, args...)
}

func (f *jobFix) runEnv(t *testing.T, env platform.Env, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run(append([]string{"job"}, args...), env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

type treeEntry struct {
	size    int64
	modTime int64
	mode    os.FileMode
}

// treeSnapshot records (path, size, mtime, mode) of every entry below root,
// files and directories. It stats each path with os.Lstat instead of the
// WalkDir entry's d.Info, whose metadata on Windows comes from the directory
// enumeration and is updated lazily, so two walks of an unchanged tree can
// differ.
func treeSnapshot(t *testing.T, root string) map[string]treeEntry {
	t.Helper()
	snap := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		snap[path] = treeEntry{size: info.Size(), modTime: info.ModTime().UnixNano(), mode: info.Mode()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// jobTreeChanges lists the paths that differ between two tree snapshots,
// sorted: paths present in after that are new (as-is) or whose entry moved
// (with each changed field as "field old->new"), and paths of before missing
// from after (suffixed " (removed)").
func jobTreeChanges(before, after map[string]treeEntry) []string {
	changed := []string{}
	for path, entry := range after {
		prev, ok := before[path]
		if !ok {
			changed = append(changed, path)
			continue
		}
		fields := []string{}
		if prev.size != entry.size {
			fields = append(fields, fmt.Sprintf("size %d->%d", prev.size, entry.size))
		}
		if prev.modTime != entry.modTime {
			fields = append(fields, fmt.Sprintf("mtime %d->%d", prev.modTime, entry.modTime))
		}
		if prev.mode != entry.mode {
			fields = append(fields, fmt.Sprintf("mode %v->%v", prev.mode, entry.mode))
		}
		if len(fields) > 0 {
			changed = append(changed, fmt.Sprintf("%s (%s)", path, strings.Join(fields, ", ")))
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed = append(changed, path+" (removed)")
		}
	}
	sort.Strings(changed)
	return changed
}

// assertJobTreeUnchanged fails t if the two tree snapshots differ, naming the
// changed paths so a CI failure shows what moved.
func assertJobTreeUnchanged(t *testing.T, before, after map[string]treeEntry, msg string) {
	t.Helper()
	if changed := jobTreeChanges(before, after); len(changed) > 0 {
		t.Fatalf("%s: %v", msg, changed)
	}
}

func TestJobTreeSnapshotReportsChangedPaths(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	c := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(a, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("bye"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := treeSnapshot(t, dir)
	// Grow a (size and a fixed mtime), remove b, add c.
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.WriteFile(a, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(a, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := treeSnapshot(t, dir)
	// The root's own mtime and size move when its children are created or
	// removed; the report under test is about the three entries, so drop the
	// root from both snapshots.
	delete(before, dir)
	delete(after, dir)
	want := []string{
		a + " (size 5->11, mtime " + strconv.FormatInt(before[a].modTime, 10) + "->" + strconv.FormatInt(fixed.UnixNano(), 10) + ")",
		b + " (removed)",
		c,
	}
	sort.Strings(want)
	if got := jobTreeChanges(before, after); !reflect.DeepEqual(got, want) {
		t.Fatalf("jobTreeChanges=%v want=%v", got, want)
	}
}

func TestJobStatusPrintsTheSnapshotLine(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	// The legacy state (no colaboracoes, collab ids or terminal intent on
	// disk) prints the empty partial values, and the read stays
	// write-free: the state file's bytes and mtime stand through the
	// status line.
	statePath := filepath.Join(co, ".soho", "jobs", "job-1", "state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.run(t, "status", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	want := `{"id":"job-1","status":"accepted","motivo":null,"eventos":{"total":1,"ultimo_seq":1},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"held_controls":[]}`
	if out != want+"\n" {
		t.Fatalf("out=%q", out)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || beforeInfo.ModTime() != afterInfo.ModTime() {
		t.Fatalf("the status read rewrote state.json (bytes or mtime moved)")
	}
}

// TestJobStatusLineHeldControls: the status line's held_controls is always
// present, at the end of the line, [] when no request is held; in a
// blocked job with the amend pending in control/ and its control_held mark
// it is the held request, and the other fields and their order stand.
func TestJobStatusLineHeldControls(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")

	// No held request: the field is present and empty, last in the line.
	code, out, errOut := f.run(t, "status", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	want := `{"id":"job-1","status":"accepted","motivo":null,"eventos":{"total":1,"ultimo_seq":1},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"held_controls":[]}`
	if out != want+"\n" {
		t.Fatalf("out=%q", out)
	}

	// A blocked job with the amend pending in control/ and its
	// control_held mark: the held request is last in the line, and the
	// other fields and their order stand.
	f.setStatus(t, co, "job-1", "blocked")
	ctl := filepath.Join(jobDir, "control")
	if err := os.MkdirAll(ctl, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctl, "amend-000007.md"), []byte("held amend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store(t, co).Append("job-1", job.EventIn{
		Tipo:   "control_held",
		Resumo: "held: the job orchestrator is blocked",
		Refs:   map[string]string{"seq_ref": "7"},
	}); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = f.run(t, "status", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("blocked code=%d out=%q err=%q", code, out, errOut)
	}
	want = `{"id":"job-1","status":"blocked","motivo":null,"eventos":{"total":2,"ultimo_seq":2},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"held_controls":[{"kind":"amend","seq":7}]}`
	if out != want+"\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestJobWaitExitByState(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")

	// A blocked job needs a person: exit 7.
	f.setStatus(t, co, "job-1", "blocked")
	code, out, errOut := f.run(t, "wait", "--id", "job-1", "--timeout", "1000")
	if code != 7 || errOut != "" || !strings.Contains(out, `"status":"blocked"`) {
		t.Fatalf("blocked code=%d out=%q err=%q", code, out, errOut)
	}

	for _, tc := range []struct {
		status string
		exit   int
	}{
		{status: "done", exit: 0},
		{status: "failed", exit: 19},
		{status: "timeout", exit: 9},
		{status: "canceled", exit: 21},
	} {
		f.setStatus(t, co, "job-1", tc.status)
		code, _, _ := f.run(t, "wait", "--id", "job-1")
		if code != tc.exit {
			t.Fatalf("%s code=%d", tc.status, code)
		}
	}

	// Collected/closed use the stored report's terminal outcome; no report is 0.
	f.setStatus(t, co, "job-1", "collected")
	code, out, _ = f.run(t, "wait", "--id", "job-1")
	if code != 0 || !strings.Contains(out, `"status":"collected"`) {
		t.Fatalf("collected code=%d out=%q", code, out)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "report.json"), []byte(`{"schema":1,"id":"job-1","status":"failed","motivo":"gate"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ = f.run(t, "wait", "--id", "job-1")
	if code != 19 {
		t.Fatalf("collected failed report code=%d", code)
	}
	f.setStatus(t, co, "job-1", "closed")
	code, _, _ = f.run(t, "wait", "--id", "job-1")
	if code != 19 {
		t.Fatalf("closed failed report code=%d", code)
	}

	// The wait bound expires first: same JSON, exit 9.
	f.setStatus(t, co, "job-1", "accepted")
	code, out, _ = f.run(t, "wait", "--id", "job-1", "--timeout", "150")
	if code != 9 || !strings.Contains(out, `"status":"accepted"`) {
		t.Fatalf("expired code=%d out=%q", code, out)
	}
}

func TestJobEventsPagesAndFlagLimits(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	if _, err := f.store(t, co).Note("job-1", job.EventIn{Tipo: "note", Resumo: "second"}); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := f.run(t, "events", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines=%q", lines)
	}
	if lines[2] != `{"eventos":"fim","ultimo_seq":2,"estado":"accepted"}` {
		t.Fatalf("trailer=%q", lines[2])
	}

	code, out, _ = f.run(t, "events", "--id", "job-1", "--since", "1")
	if code != 0 || len(strings.Split(strings.TrimSuffix(out, "\n"), "\n")) != 2 {
		t.Fatalf("since 1 code=%d out=%q", code, out)
	}

	// A --since beyond the last seq prints only the trailer.
	code, out, _ = f.run(t, "events", "--id", "job-1", "--since", "99")
	if code != 0 || out != `{"eventos":"fim","ultimo_seq":2,"estado":"accepted"}`+"\n" {
		t.Fatalf("since 99 code=%d out=%q", code, out)
	}

	// The 600000 bound is accepted.
	code, _, _ = f.run(t, "events", "--id", "job-1", "--wait", "600000")
	if code != 0 {
		t.Fatalf("wait 600000 code=%d", code)
	}

	for _, args := range [][]string{
		{"wait", "--id", "job-1", "--timeout", "600001"},
		{"wait", "--id", "job-1", "--timeout", "-1"},
		{"events", "--id", "job-1", "--wait", "600001"},
		{"events", "--id", "job-1", "--since", "-1"},
		{"status", "--id", "bad id"},
		{"status", "--id", "."},
		{"status", "--id", ".."},
		{"status", "--id", "job-1", "--nope"},
		{"status", "--id"},
		{"status", "--id", "job-1", "extra"},
		{"status", "--id", "job-1", "--id", "job-1"},
		{"list", "--state", "bogus"},
	} {
		code, out, errOut := f.run(t, args...)
		if code != 2 || out != "" {
			t.Fatalf("%v code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
}

func TestJobCollect(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")

	// No report.json yet.
	code, out, errOut := f.run(t, "collect", "--id", "job-1")
	if code != 0 || errOut != "" || out != `{"id":"job-1","status":"accepted","report":null}`+"\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}

	// A stored report prints with decisions_pendentes added.
	reportMD := "# report\n"
	if err := os.WriteFile(filepath.Join(jobDir, "report.md"), []byte(reportMD), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(reportMD))
	reportJSON := fmt.Sprintf(`{"schema":1,"id":"job-1","status":"done","motivo":null,"resumo":"ok","pr":null,"artefatos":[{"path":"report.md","sha256":"%s"}]}`, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(filepath.Join(jobDir, "report.json"), []byte(reportJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = f.run(t, "collect", "--id", "job-1")
	if code != 0 {
		t.Fatalf("code=%d out=%q", code, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "done" || got["resumo"] != "ok" || got["id"] != "job-1" {
		t.Fatalf("collect = %#v", got)
	}
	pend, ok := got["decisions_pendentes"]
	if !ok || !reflect.DeepEqual(pend, []any{}) {
		t.Fatalf("decisions_pendentes = %#v", got["decisions_pendentes"])
	}
	if artefatos, ok := got["artefatos"].([]any); !ok || len(artefatos) != 1 {
		t.Fatalf("artefatos = %#v", got["artefatos"])
	}

	// --verify passes, then catches a changed and a missing artefato.
	code, _, _ = f.run(t, "collect", "--id", "job-1", "--verify")
	if code != 0 {
		t.Fatalf("verify code=%d", code)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "report.md"), []byte("# changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = f.run(t, "collect", "--id", "job-1", "--verify")
	if code != 16 || !strings.Contains(errOut, "report.md") {
		t.Fatalf("changed code=%d err=%q", code, errOut)
	}
	missingJSON := `{"schema":1,"id":"job-1","status":"done","artefatos":[{"path":"gone.md","sha256":"` + strings.Repeat("0", 64) + `"}]}`
	if err := os.WriteFile(filepath.Join(jobDir, "report.json"), []byte(missingJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ = f.run(t, "collect", "--id", "job-1", "--verify")
	if code != 16 {
		t.Fatalf("missing code=%d", code)
	}

	// --md prints report.md verbatim; absent report.md exits 4.
	if err := os.WriteFile(filepath.Join(jobDir, "report.md"), []byte(reportMD), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = f.run(t, "collect", "--id", "job-1", "--md")
	if code != 0 || out != reportMD {
		t.Fatalf("md code=%d out=%q", code, out)
	}
	if err := os.Remove(filepath.Join(jobDir, "report.md")); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = f.run(t, "collect", "--id", "job-1", "--md")
	if code != 4 || out != "" || !strings.Contains(errOut, "report.md") {
		t.Fatalf("md absent code=%d out=%q err=%q", code, out, errOut)
	}

	// A JSON null report body is a refused exit 2, never a panic, and the
	// file is left untouched.
	nullBody := "null\n"
	if err := os.WriteFile(filepath.Join(jobDir, "report.json"), []byte(nullBody), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = f.run(t, "collect", "--id", "job-1")
	if code != 2 || out != "" || !strings.Contains(errOut, "report.json unreadable: expected an object") {
		t.Fatalf("null report code=%d out=%q err=%q", code, out, errOut)
	}
	if got, err := os.ReadFile(filepath.Join(jobDir, "report.json")); err != nil || string(got) != nullBody {
		t.Fatalf("null report file changed: %q (%v)", got, err)
	}

	// Another non-object body keeps the existing exit-2 path (refused while
	// the store reads the report; no panic, empty stdout).
	if err := os.WriteFile(filepath.Join(jobDir, "report.json"), []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = f.run(t, "collect", "--id", "job-1")
	if code != 2 || out != "" || !strings.Contains(errOut, "cannot unmarshal") {
		t.Fatalf("non-object report code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestJobListCounts(t *testing.T) {
	f := newJobFix(t)
	co1 := f.checkout(t, "example-org", "example-repo")
	co2 := f.checkout(t, "other-org", "other-repo")
	f.startJob(t, co1)
	f.startJob(t, co2)
	f.setStatus(t, co2, "job-1", "blocked")
	brief2 := strings.Replace(jobTestBrief, `"job-1"`, `"job-2"`, 1)
	if _, err := f.store(t, co1).Start("job-2", []byte(brief2)); err != nil {
		t.Fatal(err)
	}
	f.setStatus(t, co1, "job-2", "done")

	code, out, errOut := f.run(t, "list")
	if code != 0 || errOut != "" || out != `{"total":3,"por_estado":{"accepted":1,"blocked":1,"done":1}}`+"\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, _ = f.run(t, "list", "--state", "done")
	if code != 0 || out != `{"total":1,"por_estado":{"done":1}}`+"\n" {
		t.Fatalf("state code=%d out=%q", code, out)
	}
	code, _, _ = f.run(t, "list", "--state", "bogus")
	if code != 2 {
		t.Fatalf("bogus state code=%d", code)
	}

	f2 := newJobFix(t)
	code, out, _ = f2.run(t, "list")
	if code != 0 || out != `{"total":0,"por_estado":{}}`+"\n" {
		t.Fatalf("empty code=%d out=%q", code, out)
	}
}

func TestJobUnknownIdIsNotFound(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	for _, sub := range [][]string{
		{"status", "--id", "nope"},
		{"wait", "--id", "nope"},
		{"events", "--id", "nope"},
		{"collect", "--id", "nope"},
	} {
		code, out, errOut := f.run(t, sub...)
		if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
			t.Fatalf("%v code=%d out=%q err=%q", sub, code, out, errOut)
		}
	}

	// A machine without checkouts reports the same.
	f2 := newJobFix(t)
	code, out, _ := f2.run(t, "status", "--id", "job-1")
	if code != 3 || out != `{"status":"not_found"}`+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}

	// The same id in two checkouts is refused.
	f3 := newJobFix(t)
	f3.startJob(t, f3.checkout(t, "example-org", "repo-a"))
	f3.startJob(t, f3.checkout(t, "example-org", "repo-b"))
	code, out, errOut := f3.run(t, "status", "--id", "job-1")
	if code != 2 || out != "" || !strings.Contains(errOut, "exists in more than one checkout") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestJobPhase2SubsValidateThenRefuse(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("amend body\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// supervise still validates and, outside a Herdr workspace, refuses
	// before the job is looked up; amend, send, cancel and checkpoint now
	// queue control requests (TestJobControl* below). That is the
	// intentional adaptation of the phase-1 rows for DJA-194 slice 7b and
	// the supervise row for slice 6c.
	for _, args := range [][]string{
		{"supervise", "--id", "job-1"},
		{"supervise", "--id", "nope"},
	} {
		code, out, errOut := f.run(t, args...)
		want := "soho: job supervise: run it inside the job's Herdr workspace\n"
		if code != 2 || out != "" || errOut != want {
			t.Fatalf("%v code=%d out=%q err=%q", args, code, out, errOut)
		}
	}

	// Unknown ids are not_found before the refusal; missing bodies and bad
	// grace values are exit 2.
	for _, args := range [][]string{
		{"amend", "--id", "nope", bodyFile},
		{"send", "--id", "nope", "-"},
		{"cancel", "--id", "nope"},
		{"checkpoint", "--id", "nope"},
	} {
		code, out, _ := f.run(t, args...)
		if code != 3 || out != `{"status":"not_found"}`+"\n" {
			t.Fatalf("%v code=%d out=%q", args, code, out)
		}
	}
	for _, args := range [][]string{
		{"amend", "--id", "job-1"},
		{"send", "--id", "job-1"},
		{"cancel", "--id", "job-1", "--grace", "-1"},
	} {
		code, out, _ := f.run(t, args...)
		if code != 2 || out != "" {
			t.Fatalf("%v code=%d out=%q", args, code, out)
		}
	}

	code, out, errOut := f.run(t, "gate", "--id", "job-1")
	if code != 2 || out != "" || !strings.Contains(errOut, "unknown subcommand") {
		t.Fatalf("gate code=%d out=%q err=%q", code, out, errOut)
	}
	code, _, _ = f.run(t)
	if code != 2 {
		t.Fatalf("bare job code=%d", code)
	}
}

// jobControlStatusLine is the fresh status line of the fixture job after a
// control request. The partial fields are the additive contract change of
// the collaboration partial facts: always present, empty here.
const jobControlStatusLine = `{"id":"job-1","status":"accepted","motivo":null,"eventos":{"total":1,"ultimo_seq":1},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"held_controls":[]}` + "\n"

// jobRunningStatusLine is the fixture job's status line once it is
// running: the live path of job cancel, which queues the request for the
// supervisor.
const jobRunningStatusLine = `{"id":"job-1","status":"running","motivo":null,"eventos":{"total":1,"ultimo_seq":1},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"held_controls":[]}` + "\n"

// TestJobControlAmendSendWriteRequests: amend and send queue the request
// file with the exact body, print the fresh status line and exit 0; the
// body caps, unknown ids and supervise refusal are unchanged.
func TestJobControlAmendSendWriteRequests(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	ctl := filepath.Join(co, ".soho", "jobs", "job-1", "control")
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("amend body\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := f.run(t, "amend", "--id", "job-1", bodyFile)
	if code != 0 || out != jobControlStatusLine || errOut != "" {
		t.Fatalf("amend code=%d out=%q err=%q", code, out, errOut)
	}
	data, err := os.ReadFile(filepath.Join(ctl, "amend-000001.md"))
	if err != nil || string(data) != "amend body\n" {
		t.Fatalf("amend file: %q err=%v", data, err)
	}

	// send from stdin, then amend again: the counter crosses kinds.
	old := jobStdin
	jobStdin = strings.NewReader("send note\n")
	t.Cleanup(func() { jobStdin = old })
	code, out, errOut = f.run(t, "send", "--id", "job-1", "-")
	if code != 0 || out != jobControlStatusLine || errOut != "" {
		t.Fatalf("send code=%d out=%q err=%q", code, out, errOut)
	}
	data, err = os.ReadFile(filepath.Join(ctl, "send-000002.md"))
	if err != nil || string(data) != "send note\n" {
		t.Fatalf("send file: %q err=%v", data, err)
	}
	bodyFile2 := filepath.Join(t.TempDir(), "body2.md")
	if err := os.WriteFile(bodyFile2, []byte("second amend"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = f.run(t, "amend", "--id", "job-1", bodyFile2)
	if code != 0 || out != jobControlStatusLine {
		t.Fatalf("amend 2 code=%d out=%q", code, out)
	}
	data, err = os.ReadFile(filepath.Join(ctl, "amend-000003.md"))
	if err != nil || string(data) != "second amend" {
		t.Fatalf("amend 2 file: %q err=%v", data, err)
	}

	// The caps are unchanged: over 64 KiB amend and over 16 KiB send refuse
	// with exit 2; the exact caps are accepted.
	bigFile := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(bigFile, []byte(strings.Repeat("a", 64<<10+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = f.run(t, "amend", "--id", "job-1", bigFile)
	if code != 2 || out != "" || !strings.Contains(errOut, "exceeds 64 KiB") {
		t.Fatalf("big amend code=%d out=%q err=%q", code, out, errOut)
	}
	if err := os.WriteFile(bigFile, []byte(strings.Repeat("a", 64<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ = f.run(t, "amend", "--id", "job-1", bigFile)
	if code != 0 {
		t.Fatalf("64 KiB amend code=%d", code)
	}
	old2 := jobStdin
	jobStdin = strings.NewReader(strings.Repeat("b", 16<<10+1))
	t.Cleanup(func() { jobStdin = old2 })
	code, out, errOut = f.run(t, "send", "--id", "job-1", "-")
	if code != 2 || out != "" || !strings.Contains(errOut, "exceeds 16 KiB") {
		t.Fatalf("big send code=%d out=%q err=%q", code, out, errOut)
	}

	// Unknown ids are not_found, before any write.
	code, out, errOut = f.run(t, "amend", "--id", "nope", bodyFile)
	if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
		t.Fatalf("unknown amend code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = f.run(t, "send", "--id", "nope", "-")
	if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
		t.Fatalf("unknown send code=%d out=%q err=%q", code, out, errOut)
	}

	// supervise, outside a Herdr workspace, refuses (the phase-1 row
	// adapted for slice 6c: the supervisor arrives in the same slice).
	code, out, errOut = f.run(t, "supervise", "--id", "job-1")
	if code != 2 || out != "" || errOut != "soho: job supervise: run it inside the job's Herdr workspace\n" {
		t.Fatalf("supervise code=%d out=%q err=%q", code, out, errOut)
	}
}

// TestJobControlCancelCheckpoint: cancel queues {"grace":120} by default
// (or the --grace value), checkpoint queues the empty marker; a repeat while
// one is pending is a no-op with exit 0; a terminal job refuses amend/send
// with exit 2 and writes nothing for cancel/checkpoint. The cancel rows run
// on a running job: an accepted or preparing job is a crashed start that
// the cancel recovers (TestJobCancelRecoversTheCrashedStart).
func TestJobControlCancelCheckpoint(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	ctl := filepath.Join(co, ".soho", "jobs", "job-1", "control")
	f.setStatus(t, co, "job-1", "running")

	code, out, errOut := f.run(t, "cancel", "--id", "job-1")
	if code != 0 || out != jobRunningStatusLine || errOut != "" {
		t.Fatalf("cancel code=%d out=%q err=%q", code, out, errOut)
	}
	data, err := os.ReadFile(filepath.Join(ctl, "cancel.json"))
	if err != nil || string(data) != `{"grace":120}` {
		t.Fatalf("cancel file: %q err=%v", data, err)
	}

	// A second cancel while one is pending is a no-op with exit 0: the
	// first file stays with its original grace.
	code, out, errOut = f.run(t, "cancel", "--id", "job-1", "--grace", "30")
	if code != 0 || out != jobRunningStatusLine || errOut != "" {
		t.Fatalf("cancel again code=%d out=%q err=%q", code, out, errOut)
	}
	data, _ = os.ReadFile(filepath.Join(ctl, "cancel.json"))
	if string(data) != `{"grace":120}` {
		t.Fatalf("cancel overwritten: %q", data)
	}

	// --grace applies to a fresh queue: retire the file, then ask again.
	if err := os.Remove(filepath.Join(ctl, "cancel.json")); err != nil {
		t.Fatal(err)
	}
	code, out, _ = f.run(t, "cancel", "--id", "job-1", "--grace", "30")
	if code != 0 || out != jobRunningStatusLine {
		t.Fatalf("cancel grace code=%d out=%q", code, out)
	}
	data, err = os.ReadFile(filepath.Join(ctl, "cancel.json"))
	if err != nil || string(data) != `{"grace":30}` {
		t.Fatalf("cancel grace file: %q err=%v", data, err)
	}

	// While the cancel is pending, a checkpoint is refused with
	// cancel_pending (exit 2) and nothing is queued.
	code, out, errOut = f.run(t, "checkpoint", "--id", "job-1")
	if code != 2 || out != "" || !strings.Contains(errOut, "cancel_pending") {
		t.Fatalf("checkpoint while canceling code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(ctl, "checkpoint")); !os.IsNotExist(err) {
		t.Fatalf("checkpoint queued while canceling: %v", err)
	}

	// Once the cancel is retired, a checkpoint is queued again.
	if err := os.Remove(filepath.Join(ctl, "cancel.json")); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = f.run(t, "checkpoint", "--id", "job-1")
	if code != 0 || out != jobRunningStatusLine || errOut != "" {
		t.Fatalf("checkpoint code=%d out=%q err=%q", code, out, errOut)
	}
	data, err = os.ReadFile(filepath.Join(ctl, "checkpoint"))
	if err != nil || len(data) != 0 {
		t.Fatalf("checkpoint file: %q err=%v", data, err)
	}
	code, out, _ = f.run(t, "checkpoint", "--id", "job-1")
	if code != 0 || out != jobRunningStatusLine {
		t.Fatalf("checkpoint again code=%d out=%q", code, out)
	}

	// Unknown ids are not_found for all four subcommands.
	for _, sub := range []string{"cancel", "checkpoint"} {
		code, out, errOut := f.run(t, sub, "--id", "nope")
		if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
			t.Fatalf("%s code=%d out=%q err=%q", sub, code, out, errOut)
		}
	}

	// A terminal job: amend and send refuse with exit 2, cancel and
	// checkpoint are no-ops with exit 0 and write nothing.
	f2 := newJobFix(t)
	co2 := f2.checkout(t, "example-org", "example-repo")
	f2.startJob(t, co2)
	f2.setStatus(t, co2, "job-1", "done")
	ctl2 := filepath.Join(co2, ".soho", "jobs", "job-1", "control")
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("amend body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = f2.run(t, "amend", "--id", "job-1", bodyFile)
	if code != 2 || out != "" || !strings.Contains(errOut, "job is not active") {
		t.Fatalf("terminal amend code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = f2.run(t, "send", "--id", "job-1", bodyFile)
	if code != 2 || out != "" || !strings.Contains(errOut, "job is not active") {
		t.Fatalf("terminal send code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = f2.run(t, "cancel", "--id", "job-1", "--grace", "30")
	if code != 0 || out == "" || errOut != "" {
		t.Fatalf("terminal cancel code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = f2.run(t, "checkpoint", "--id", "job-1")
	if code != 0 || out == "" || errOut != "" {
		t.Fatalf("terminal checkpoint code=%d out=%q err=%q", code, out, errOut)
	}
	if entries, err := os.ReadDir(ctl2); err != nil || len(entries) != 0 {
		t.Fatalf("terminal job wrote control files: %v err=%v", entries, err)
	}

	// A single stray positional argument is refused before any write:
	// exit 2 naming the argument, nothing under control/.
	t.Run("a stray positional argument is refused before any write", func(t *testing.T) {
		f3 := newJobFix(t)
		co3 := f3.checkout(t, "example-org", "example-repo")
		f3.startJob(t, co3)
		ctl3 := filepath.Join(co3, ".soho", "jobs", "job-1", "control")

		for _, sub := range []string{"cancel", "checkpoint"} {
			code, out, errOut := f3.run(t, sub, "--id", "job-1", "stray")
			if code != 2 || out != "" || !strings.Contains(errOut, "job "+sub+": unexpected argument 'stray'") {
				t.Fatalf("%s code=%d out=%q err=%q", sub, code, out, errOut)
			}
		}
		// The store start pre-creates the control directory; the refusal
		// must leave it without a single file.
		if entries, err := os.ReadDir(ctl3); err == nil {
			if len(entries) != 0 {
				t.Fatalf("stray argument wrote control files: %v", entries)
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("stray argument: %v", err)
		}
	})
}

// TestJobCancelRecoversTheCrashedStart: a job cancel of a crashed preparing
// job (the starter died before the supervisor launched) recovers it instead
// of queueing: the job ends failed with the report, the status line shows
// failed, and nothing is queued for a supervisor that will never run.
func TestJobCancelRecoversTheCrashedStart(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	// The crashed preparing start: no workspace id and no intent recorded
	// (a crash before the intent), so there is nothing to close.
	f.setStatus(t, co, "job-1", "preparing")
	code, out, errOut := f.run(t, "cancel", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, `"status":"failed"`) || !strings.Contains(out, `"motivo":"job: start interrupted"`) {
		t.Fatalf("status line = %q", out)
	}
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")
	if _, err := os.Stat(filepath.Join(jobDir, "report.json")); err != nil {
		t.Fatalf("report.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(jobDir, "control", "cancel.json")); !os.IsNotExist(err) {
		t.Fatalf("the cancel was queued anyway: %v", err)
	}
}

func TestJobCapabilitiesLine(t *testing.T) {
	var out, errOut bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run([]string{"capabilities", "--json"}, platform.Env{})
	platform.Stdout, platform.Stderr = oldOut, oldErr
	want := `{"schema":1,"worker_collaboration":1,"ephemeral_job":1,"job_events":1}` + "\n"
	if code != 0 || out.String() != want || errOut.Len() != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestJobNowrite(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	f.setStatus(t, co, "job-1", "done")
	env := platform.Env{}
	for name, value := range f.env {
		env[name] = value
	}
	env["SOHO_NOWRITE"] = "1"

	tree := treeSnapshot(t, f.root)
	for _, args := range [][]string{
		{"status", "--id", "job-1"},
		{"wait", "--id", "job-1", "--timeout", "1000"},
		{"events", "--id", "job-1"},
		{"collect", "--id", "job-1"},
		{"list"},
	} {
		code, out, errOut := f.runEnv(t, env, args...)
		if code != 0 || errOut != "" {
			t.Fatalf("%v code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
	assertJobTreeUnchanged(t, tree, treeSnapshot(t, f.root), "NOWRITE reads changed the tree")

	for _, sub := range []string{"start", "supervise", "amend", "send", "ack", "cancel", "close", "checkpoint", "note"} {
		code, out, errOut := f.runEnv(t, env, sub, "--id", "job-1")
		want := "soho: SOHO_NOWRITE=1 is read-only: job " + sub + " writes; only job status, wait, events, collect and list run\n"
		if code != 2 || out != "" || errOut != want {
			t.Fatalf("%s code=%d out=%q err=%q", sub, code, out, errOut)
		}
	}
}

func TestJobUsage(t *testing.T) {
	if !strings.Contains(usage, "job status") {
		t.Fatal("usage missing the job status line")
	}
	if !strings.Contains(usage, "24 job close") {
		t.Fatal("usage missing the 24 job close exit code")
	}
	for _, banned := range []string{"job gate", "job_gate", " 23 "} {
		if strings.Contains(usage, banned) {
			t.Fatalf("usage contains %q", banned)
		}
	}
	if !strings.Contains(usage, "Only the `job` supervisor pushes the job branch and opens a draft pull request") {
		t.Fatal("usage opening not amended")
	}
	if strings.Contains(usage, "It never commits") {
		t.Fatal("old usage opening still present")
	}

	var out, errOut bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run([]string{"job", "--help"}, platform.Env{})
	platform.Stdout, platform.Stderr = oldOut, oldErr
	if code != 0 || errOut.Len() != 0 || !strings.Contains(out.String(), "soho job") {
		t.Fatalf("job --help code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

// jobStartEE stages a non-dry-run job start: a real git on the fixture
// PATH, a local bare remote with a main branch, an existing checkout
// cloned from it, and fake gh and herdr binaries.
type jobStartEE struct {
	f        *jobFix
	env      platform.Env
	gitEnv   platform.Env
	bare     string
	checkout string
	fakeDir  string
	selfExe  string
}

// linkSystemGit puts the real git on the fixture PATH (symlink, or a copy
// where symlinks are unavailable).
func linkSystemGit(t *testing.T, dir string) {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate the system git: %v", err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
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
}

// runGit runs a real git with the given env, bounded by a 60 s deadline.
func runGit(t *testing.T, env platform.Env, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = time.Second
	cmd.Dir = dir
	cmd.Env = env.List()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func newJobStartEE(t *testing.T) *jobStartEE {
	t.Helper()
	f := newJobFix(t)
	f.setMachine(t)
	f.setTeam(t)
	bin := filepath.Join(f.root, "bin")
	linkSystemGit(t, bin)

	// A hermetic git env for the fixture's own git commands.
	gitEnv := f.env.Clone()
	gitEnv["GIT_CONFIG_NOSYSTEM"] = "1"
	gitEnv["GIT_CONFIG_GLOBAL"] = filepath.Join(f.root, "gitconfig")
	gitEnv["GIT_AUTHOR_NAME"] = "fixture"
	gitEnv["GIT_AUTHOR_EMAIL"] = "fixture@example.org"
	gitEnv["GIT_COMMITTER_NAME"] = "fixture"
	gitEnv["GIT_COMMITTER_EMAIL"] = "fixture@example.org"
	if err := os.WriteFile(gitEnv["GIT_CONFIG_GLOBAL"], []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	// A bare remote with a main branch, then an existing checkout of it.
	bare := filepath.Join(f.root, "bare.git")
	seed := filepath.Join(f.root, "seed")
	if err := os.MkdirAll(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitEnv, f.root, "init", "-q", "--bare", bare)
	for _, args := range [][]string{
		{"init", "-q"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
		{"branch", "-M", "main"},
		{"remote", "add", "origin", bare},
		{"push", "-q", "origin", "main"},
	} {
		runGit(t, gitEnv, seed, args...)
	}
	runGit(t, gitEnv, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	checkout := filepath.Join(f.repos, "example-org", "example-repo")
	if err := os.MkdirAll(filepath.Dir(checkout), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, gitEnv, f.root, "clone", "-q", bare, checkout)

	// Fake gh and herdr on a PATH that resolves them before the fixture git.
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "gh", []fakecli.Rule{{
		Argv: []string{"auth", "status"},
	}}); err != nil {
		t.Fatalf("install the fake gh: %v", err)
	}
	worktree := filepath.Join(checkout, ".worktrees", "job-job-1")
	// The reachability probe and the create-intent record both read the
	// workspace list; the list is the contract's real shape. The create
	// hands the root pane the start's user-configuration locator.
	herdrRules := []fakecli.Rule{
		{Argv: []string{"workspace", "list"}, Stdout: `{"result":{"workspaces":[]}}`},
		{Argv: []string{"workspace", "create", "--cwd", worktree, "--label", "job-job-1", "--env", "_SOHO_LAUNCH_CONFIG_BASE=" + jobStartXDGBase(f.env), "--no-focus"},
			Stdout: `{"result":{"workspace_id":"w9","root_pane":{"pane_id":"w9:p1"}}}`},
		{Argv: []string{"pane", "run", "w9:p1"}, ArgvPrefix: true},
	}
	if _, err := fakecli.Install(t, fakeDir, "herdr", herdrRules); err != nil {
		t.Fatalf("install the fake herdr: %v", err)
	}
	env := platform.Env{}
	for _, item := range fakecli.Env(f.env.List(), fakeDir, fakecli.EnvOptions{SystemPath: bin}) {
		key, value, ok := strings.Cut(item, "=")
		if ok && key != "" {
			env[key] = value
		}
	}
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve the test executable: %v", err)
	}
	return &jobStartEE{f: f, env: env, gitEnv: gitEnv, bare: bare, checkout: checkout, fakeDir: fakeDir, selfExe: selfExe}
}

// herdrCalls reads the fake herdr call log; a missing log means no call.
func (ee *jobStartEE) herdrCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(ee.fakeDir, "herdr.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read the herdr call log: %v", err)
	}
	return calls
}

func (ee *jobStartEE) start(t *testing.T, briefFile string) (int, string, string) {
	t.Helper()
	stop := ee.confirmLaunches()
	defer stop()
	return ee.f.runEnv(t, ee.env, "start", "--id", "job-1", "--repo", "example-org/example-repo", "--brief", briefFile)
}

// jobStartXDGBase is the user-configuration base the start hands to the
// supervisor: the directory that holds herdr-soho/config.
func jobStartXDGBase(env platform.Env) string {
	return filepath.Dir(filepath.Dir(platform.UserConfigPath(platform.Current(), env)))
}

// confirmLaunches stands in for the supervisor the fake herdr never runs:
// until stop, it confirms job-1's pending launch from the fake root pane
// (w9:p1 of w9), as `job supervise` does before it runs. This confirmation
// is implementation-generated, never evidence of a real Herdr launch.
func (ee *jobStartEE) confirmLaunches() func() {
	cfg := core.LoadConfig(ee.env, ee.checkout)
	store := job.Open(core.StateRootPath(&cfg, ee.env, ee.checkout))
	pane := platform.Env{"HERDR_WORKSPACE_ID": "w9", "HERDR_PANE_ID": "w9:p1"}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			if _, err := os.Stat(filepath.Join(store.Root, "jobs", "job-1", "launch.json")); err == nil {
				_, _ = job.ConfirmLaunch(store, "job-1", pane)
			}
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func TestJobStartStartsTheJob(t *testing.T) {
	ee := newJobStartEE(t)
	briefFile := ee.f.briefFile(t, jobStartBrief)
	code, out, errOut := ee.start(t, briefFile)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout = %q, want one line", out)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &line); err != nil {
		t.Fatalf("status line: %v: %q", err, out)
	}
	if line["id"] != "job-1" || line["status"] != "running" || line["motivo"] != nil {
		t.Fatalf("status line = %#v", line)
	}
	eventos, _ := line["eventos"].(map[string]any)
	if eventos["total"] != float64(2) || eventos["ultimo_seq"] != float64(2) {
		t.Fatalf("eventos = %#v", line["eventos"])
	}
	if _, ok := line["duplicate_of"]; ok {
		t.Fatalf("duplicate_of on a fresh start: %#v", line)
	}

	// Exactly the contract's herdr calls: the reachability list, the
	// create-intent list, the create, and the supervisor run.
	calls := ee.herdrCalls(t)
	if len(calls) != 4 {
		t.Fatalf("herdr calls = %+v", calls)
	}
	worktree := filepath.Join(ee.checkout, ".worktrees", "job-job-1")
	if want := []string{"workspace", "list"}; !reflect.DeepEqual(calls[0].Argv, want) {
		t.Fatalf("call 0 = %v", calls[0].Argv)
	}
	if want := []string{"workspace", "list"}; !reflect.DeepEqual(calls[1].Argv, want) {
		t.Fatalf("call 1 = %v", calls[1].Argv)
	}
	if want := []string{"workspace", "create", "--cwd", worktree, "--label", "job-job-1", "--env", "_SOHO_LAUNCH_CONFIG_BASE=" + jobStartXDGBase(ee.f.env), "--no-focus"}; !reflect.DeepEqual(calls[2].Argv, want) {
		t.Fatalf("call 2 = %v", calls[2].Argv)
	}
	if want := []string{"pane", "run", "w9:p1", ee.selfExe, "job", "supervise", "--id", "job-1", "--repo", "example-org/example-repo"}; !reflect.DeepEqual(calls[3].Argv, want) {
		t.Fatalf("call 3 = %v", calls[3].Argv)
	}
	// gh ran exactly once: auth status.
	ghCalls, err := fakecli.ReadCalls(filepath.Join(ee.fakeDir, "gh.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ghCalls) != 1 || !reflect.DeepEqual(ghCalls[0].Argv, []string{"auth", "status"}) {
		t.Fatalf("gh calls = %+v", ghCalls)
	}

	// The worktree exists on the job branch.
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree: %v", err)
	}
	branchOut, err := exec.Command("git", "-C", worktree, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(branchOut) != "job/job-1\n" {
		t.Fatalf("branch = %q", branchOut)
	}

	// The session file: the team pairs in order, mode 0600.
	cfg := core.LoadConfig(ee.env, ee.checkout)
	stateRoot := core.StateRootPath(&cfg, ee.env, ee.checkout)
	session := filepath.Join(stateRoot, "w9", "session.conf")
	raw, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}
	want := "panes=3\nlane.build.roles=implementer\nlane.review.roles=reviewer\nlane.review.effort=high\n"
	if string(raw) != want {
		t.Fatalf("session.conf = %q, want %q", raw, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(session)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("session.conf mode = %v", info.Mode().Perm())
		}
	}

	// The run facts in state.json.
	machine, err := job.LoadMachine(platform.Current(), ee.env)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ee.f.store(t, ee.checkout).Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	st := snap.State
	if st.Checkout != ee.checkout || st.Dir != worktree || st.Branch != "job/job-1" ||
		st.Base != "main" || len(st.BaseSHA) != 40 || st.Modo != "worktree" ||
		st.TimeoutMin != machine.TimeoutMin || st.WorkspaceID != "w9" || st.RootPane != "w9:p1" ||
		st.StartedAt == "" {
		t.Fatalf("run facts = %+v (timeout want %d)", st, machine.TimeoutMin)
	}
}

func TestJobStartDuplicateAndConflict(t *testing.T) {
	ee := newJobStartEE(t)
	briefFile := ee.f.briefFile(t, jobStartBrief)
	changed := ee.f.briefFile(t, strings.Replace(jobStartBrief, "Ship the change.", "Ship another change.", 1))

	code, out, errOut := ee.start(t, briefFile)
	if code != 0 || errOut != "" {
		t.Fatalf("first code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, `"status":"running"`) {
		t.Fatalf("first out=%q", out)
	}

	// The id is now held: the same brief returns duplicate_of and exits 0,
	// without a second workspace.
	code, out, errOut = ee.start(t, briefFile)
	if code != 0 || errOut != "" {
		t.Fatalf("duplicate code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, `"duplicate_of":"job-1"`) || !strings.Contains(out, `"status":"running"`) {
		t.Fatalf("duplicate out=%q", out)
	}
	creates := 0
	for _, call := range ee.herdrCalls(t) {
		if len(call.Argv) >= 2 && call.Argv[0] == "workspace" && call.Argv[1] == "create" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("workspace create calls = %d, want 1", creates)
	}

	// A changed brief is exit 20.
	code, out, errOut = ee.start(t, changed)
	if code != 20 || out != "" || !strings.Contains(errOut, "different brief") {
		t.Fatalf("conflict code=%d out=%q err=%q", code, out, errOut)
	}
}

// TestJobStartDuplicateRecoversTheCrashedStart: the duplicate job start of
// a crashed preparing job (the start recorded the create intent, created
// the workspace, and died before recording it) recovers it: the Herdr
// lists the preexisting and the new job-job-1 workspaces, the recovery
// closes exactly the new one, and the status line shows failed with
// duplicate_of.
func TestJobStartDuplicateRecoversTheCrashedStart(t *testing.T) {
	ee := newJobStartEE(t)
	store := ee.f.store(t, ee.checkout)
	// The crashed preparing job: recorded, moved to preparing, with the
	// create intent; the workspace it created is still open in the Herdr.
	if _, err := store.Start("job-1", []byte(jobStartBrief)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", job.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record("job-1", func(st *job.State) {
		st.WorkspaceLabel = "job-job-1"
		st.WorkspacePreexisting = []string{"w0"}
	}); err != nil {
		t.Fatal(err)
	}
	// The fake Herdr lists the preexisting job-job-1 workspace (w0) and
	// the crashed start's new one (w9), and accepts the close of w9.
	configPath := filepath.Join(ee.fakeDir, "herdr.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Log          string         `json:"log"`
		Rules        []fakecli.Rule `json:"rules"`
		CaptureStdin bool           `json:"capture_stdin,omitempty"`
		CaptureEnv   []string       `json:"capture_env,omitempty"`
	}
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	foundList := false
	for i := range script.Rules {
		if reflect.DeepEqual(script.Rules[i].Argv, []string{"workspace", "list"}) {
			script.Rules[i].Stdout = `{"result":{"workspaces":[{"workspace_id":"w0","label":"job-job-1","pane_count":1},{"workspace_id":"w9","label":"job-job-1","pane_count":1}]}}`
			foundList = true
		}
	}
	if !foundList {
		t.Fatal("the workspace list rule is missing")
	}
	script.Rules = append(script.Rules, fakecli.Rule{Argv: []string{"workspace", "close", "w9"}})
	rewritten, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}

	briefFile := ee.f.briefFile(t, jobStartBrief)
	code, out, errOut := ee.start(t, briefFile)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, `"status":"failed"`) || !strings.Contains(out, `"duplicate_of":"job-1"`) ||
		!strings.Contains(out, `"motivo":"job: start interrupted"`) {
		t.Fatalf("status line = %q", out)
	}
	// The recovery made exactly the list and the close of the new
	// workspace: no create, no pane run.
	calls := ee.herdrCalls(t)
	if len(calls) != 2 {
		t.Fatalf("herdr calls = %+v", calls)
	}
	if want := []string{"workspace", "list"}; !reflect.DeepEqual(calls[0].Argv, want) {
		t.Fatalf("call 0 = %v", calls[0].Argv)
	}
	if want := []string{"workspace", "close", "w9"}; !reflect.DeepEqual(calls[1].Argv, want) {
		t.Fatalf("call 1 = %v", calls[1].Argv)
	}
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Status != job.StatusFailed || snap.State.WorkspaceID != "w9" || !snap.State.WorkspaceClosed {
		t.Fatalf("state = %+v", snap.State)
	}
	cfg := core.LoadConfig(ee.env, ee.checkout)
	stateRoot := core.StateRootPath(&cfg, ee.env, ee.checkout)
	if _, err := os.Stat(filepath.Join(stateRoot, "jobs", "job-1", "report.json")); err != nil {
		t.Fatalf("report.json: %v", err)
	}
}

// TestJobSuperviseCLIRefusals covers the supervisor entry: outside a Herdr
// workspace it refuses before the job is looked up, an unknown id inside
// one is not_found, and NOWRITE still refuses the writing subcommand.
func TestJobSuperviseCLIRefusals(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	want := "soho: job supervise: run it inside the job's Herdr workspace\n"

	// Outside Herdr, both a known and an unknown id refuse with exit 2.
	for _, args := range [][]string{
		{"supervise", "--id", "job-1"},
		{"supervise", "--id", "nope"},
	} {
		code, out, errOut := f.run(t, args...)
		if code != 2 || out != "" || errOut != want {
			t.Fatalf("%v code=%d out=%q err=%q", args, code, out, errOut)
		}
	}

	// Inside Herdr, an unknown id is not_found before the supervisor runs.
	herdr := platform.Env{}
	for name, value := range f.env {
		herdr[name] = value
	}
	herdr["HERDR_ENV"] = "1"
	code, out, errOut := f.runEnv(t, herdr, "supervise", "--id", "nope")
	if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
		t.Fatalf("unknown id code=%d out=%q err=%q", code, out, errOut)
	}

	// NOWRITE refuses the writing subcommand, even inside Herdr.
	nowrite := platform.Env{}
	for name, value := range herdr {
		nowrite[name] = value
	}
	nowrite["SOHO_NOWRITE"] = "1"
	code, out, errOut = f.runEnv(t, nowrite, "supervise", "--id", "job-1")
	if code != 2 || out != "" || !strings.Contains(errOut, "SOHO_NOWRITE=1 is read-only: job supervise writes") {
		t.Fatalf("NOWRITE code=%d out=%q err=%q", code, out, errOut)
	}
}

// TestJobSuperviseRepoLookup: --repo makes supervise read only the named
// checkout of the same selection the scan would use. With the same id in
// a second checkout, the scan exits 2 (the id exists in more than one
// checkout) while --repo finds the job in the named checkout and only it:
// the supervisor is built from that store and stops unbound before it
// runs, and the other checkout's job directory stays untouched. A --repo
// checkout without the job is not_found, and an invalid --repo is exit 2.
func TestJobSuperviseRepoLookup(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	co2 := f.checkout(t, "example-org", "example-repo-2")
	f.startJob(t, co)
	f.startJob(t, co2)
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")
	body := fmt.Sprintf(`{"schema":1,"id":"job-1","status":"running","brief_sha256":"x","decisions_acked_seq":0,"motivo":null,"dir":%q}`+"\n", co)
	if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	herdr := withEnv(f.env, "HERDR_ENV", "1", "HERDR_WORKSPACE_ID", "w9", "HERDR_PANE_ID", "w9:p1")

	// The scan sees the id in both checkouts and exits 2.
	code, out, errOut := f.runEnv(t, herdr, "supervise", "--id", "job-1")
	if code != 2 || out != "" || !strings.Contains(errOut, "exists in more than one checkout") {
		t.Fatalf("scan code=%d out=%q err=%q", code, out, errOut)
	}

	// An invalid --repo is exit 2 before any lookup.
	code, out, errOut = f.runEnv(t, herdr, "supervise", "--id", "job-1", "--repo", "a/b/c")
	if code != 2 || out != "" || !strings.Contains(errOut, "invalid --repo") {
		t.Fatalf("invalid repo code=%d out=%q err=%q", code, out, errOut)
	}

	// A --repo checkout without the job is not_found, exit 3.
	code, out, errOut = f.runEnv(t, herdr, "supervise", "--id", "job-1", "--repo", "example-org/absent")
	if code != 3 || out != `{"status":"not_found"}`+"\n" || errOut != "" {
		t.Fatalf("absent code=%d out=%q err=%q", code, out, errOut)
	}

	// With --repo the lookup reads only the named checkout: the job is
	// found there, the supervisor is built from it, and it stops unbound
	// (no launch record) before it runs, exactly as a scoped supervisor
	// does.
	base := filepath.Clean(filepath.Join(f.root, "xdg"))
	code, out, errOut = f.runEnv(t, withEnv(herdr, testLaunchBaseVar, base), "supervise", "--id", "job-1", "--repo", "example-org/example-repo")
	if code != 4 || !strings.Contains(out, `"status":"running"`) || errOut != "soho: warning: job: the supervisor is not bound to its launch: the job has no launch record\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	// The friction line went to the named checkout's job only: the other
	// checkout's job directory was never written.
	friction, err := os.ReadFile(filepath.Join(jobDir, "friction.log"))
	if err != nil || !strings.Contains(string(friction), "the supervisor is not bound to its launch") {
		t.Fatalf("friction = %q err=%v", friction, err)
	}
	if _, err := os.Lstat(filepath.Join(co2, ".soho", "jobs", "job-1", "friction.log")); !os.IsNotExist(err) {
		t.Fatalf("the other checkout's job was touched: %v", err)
	}
}

// TestJobSuperviseRunsTheSupervisor runs the whole path: job supervise
// builds the supervisor with the fake executable as the team command
// runner, the supervisor spawns and dispatches the orchestrator, watches
// it to done, copies the report and exits 0 with the done status line.
func TestJobSuperviseRunsTheSupervisor(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")

	// The start slice's run facts: a running job with its worktree dir.
	body := fmt.Sprintf(`{"schema":1,"id":"job-1","status":"running","brief_sha256":"x","decisions_acked_seq":0,"motivo":null,"dir":%q}`+"\n", co)
	if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	reportDir := t.TempDir()
	reportPath := filepath.Join(reportDir, "report.md")
	if err := os.WriteFile(reportPath, []byte("# Report — job\n\n- Item 1 [done] did the thing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fakeDir := t.TempDir()
	exe, err := fakecli.Install(t, fakeDir, "herdr-soho", []fakecli.Rule{
		{Argv: []string{"session", "set", "lane.job.roles", "job-orchestrator"}},
		{Argv: []string{"session", "set", "lane.job.panes", "1"}},
		{Argv: []string{"config"}, Stdout: "KEY                VALUE                          SOURCE\nmax_workers        3                              defaults\n"},
		{Argv: []string{"spawn", "job-orchestrator", "--cwd", co, "--approvals", "full", "--fresh"}, Stdout: `{"name":"orch-1","pane_id":"w9:p2","status":"ready"}` + "\n"},
		{Argv: []string{"dispatch", "orch-1", filepath.Join(jobDir, "orchestrator-brief.md"), "--no-wait"}},
		{Argv: []string{"status", "orch-1"}, Stdout: "orch-1\tdone\t" + reportPath + "\t-\t-\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for name, value := range f.env {
		env[name] = value
	}
	for _, item := range fakecli.Env(nil, fakeDir) {
		key, value, ok := strings.Cut(item, "=")
		if ok && key != "" {
			env[key] = value
		}
	}
	// The launcher resolves the override's symlinks, so it must point at a
	// regular file with the fake's own name, including the platform
	// extension (.exe on Windows): the fake derives its config file name
	// from its executable's base name. On Windows that file prefers a hard
	// link of the prepared image (exe already is one; the running parent
	// image is never linked), so the slow first exec of a fresh full copy
	// is not paid again; when the link is unsupported it falls back to a
	// byte copy (slower first exec, same behavior). On the other OSes it
	// stays a byte copy.
	realDir := t.TempDir()
	real := filepath.Join(realDir, filepath.Base(exe))
	if runtime.GOOS == "windows" {
		if err := os.Link(exe, real); err != nil {
			t.Logf("falling back to an independent byte copy: %v", err)
			fakeData, readErr := os.ReadFile(exe)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if writeErr := os.WriteFile(real, fakeData, 0o755); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
	} else {
		fakeData, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(real, fakeData, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env["HERDR_ENV"] = "1"
	env["SOHO_BIN"] = real

	code, out, errOut := f.runEnv(t, env, "supervise", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, `"status":"done"`) || !strings.Contains(out, `"id":"job-1"`) {
		t.Fatalf("status line out=%q", out)
	}
	// The orchestrator's report is the job report now, byte for byte.
	copied, err := os.ReadFile(filepath.Join(jobDir, "report.md"))
	if err != nil || string(copied) != "# Report — job\n\n- Item 1 [done] did the thing\n" {
		t.Fatalf("report.md = %q err=%v", copied, err)
	}
	brief, err := os.ReadFile(filepath.Join(jobDir, "orchestrator-brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "- Job id: job-1\n") || !strings.Contains(string(brief), "soho job checkpoint --id job-1") {
		t.Fatalf("orchestrator brief = %q", brief)
	}
	state, err := os.ReadFile(filepath.Join(jobDir, "state.json"))
	if err != nil || !strings.Contains(string(state), `"status":"done"`) || !strings.Contains(string(state), `"orchestrator":"orch-1"`) {
		t.Fatalf("state.json = %q err=%v", state, err)
	}
	events, err := os.ReadFile(filepath.Join(jobDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tipo := range []string{`"tipo":"worker_spawned"`, `"tipo":"worker_done"`, `"tipo":"terminal"`} {
		if !strings.Contains(string(events), tipo) {
			t.Fatalf("events missing %s: %s", tipo, events)
		}
	}
}

// TestJobStartUnknownEnvelopeGoesOnlyToFriction pins the unknown
// workspace-create envelope as friction-only: the start exits 4 with the
// fixed stderr line, and the raw envelope reaches none of stdout, stderr,
// state.json, or report.json — it lands in the job's friction.log as an
// error line, never as a warning.
func TestJobStartUnknownEnvelopeGoesOnlyToFriction(t *testing.T) {
	ee := newJobStartEE(t)
	configPath := filepath.Join(ee.fakeDir, "herdr.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Log          string         `json:"log"`
		Rules        []fakecli.Rule `json:"rules"`
		CaptureStdin bool           `json:"capture_stdin,omitempty"`
		CaptureEnv   []string       `json:"capture_env,omitempty"`
	}
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	const secret = `{"result":{"token":"SECRET_MARKER_r2"}}`
	found := false
	for i := range script.Rules {
		if len(script.Rules[i].Argv) >= 2 && script.Rules[i].Argv[0] == "workspace" && script.Rules[i].Argv[1] == "create" {
			script.Rules[i].Stdout = secret
			found = true
		}
	}
	if !found {
		t.Fatal("create rule missing")
	}
	rewritten, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}

	briefFile := ee.f.briefFile(t, jobStartBrief)
	code, out, errOut := ee.start(t, briefFile)
	if code != 4 {
		t.Fatalf("code=%d out=%q err=%q, want exit 4", code, out, errOut)
	}
	const marker = "SECRET_MARKER_r2"
	if strings.Contains(out, marker) {
		t.Fatalf("stdout carries the raw envelope: %q", out)
	}
	if strings.Contains(errOut, marker) {
		t.Fatalf("stderr carries the raw envelope: %q", errOut)
	}
	if !strings.Contains(errOut, "job: herdr workspace create returned an unknown result") {
		t.Fatalf("stderr = %q, want the fixed unknown-result line", errOut)
	}
	cfg := core.LoadConfig(ee.env, ee.checkout)
	stateRoot := core.StateRootPath(&cfg, ee.env, ee.checkout)
	jobDir := filepath.Join(stateRoot, "jobs", "job-1")
	for _, name := range []string{"state.json", "report.json"} {
		body, err := os.ReadFile(filepath.Join(jobDir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(body), marker) {
			t.Fatalf("%s carries the raw envelope: %s", name, body)
		}
	}
	friction, err := os.ReadFile(filepath.Join(jobDir, "friction.log"))
	if err != nil {
		t.Fatalf("friction.log: %v", err)
	}
	markerInFriction := false
	for _, line := range strings.Split(string(friction), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		markerInFriction = true
		if !strings.Contains(line, "\terror(exit 4)\t") {
			t.Fatalf("friction line carries the envelope outside the error sink: %q", line)
		}
	}
	if !markerInFriction {
		t.Fatalf("friction.log is missing the raw envelope: %s", friction)
	}
}

// The legacy collect view: a report.json whose state predates the partial
// fields collects with "partial":false and "partial_collaborations":[],
// and the read changes nothing on disk (the bytes and the mtime stand,
// and --verify keeps hashing the original bytes); a report that already
// carries the fields prints them unchanged.
//
// Catches the mutation "the legacy view dropped from jobCollect" (the
// decision-5 revert): the collect line of a pre-fields report would then
// lack the partial keys.
func TestJobCollectLegacyPartialView(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")
	reportFile := filepath.Join(jobDir, "report.json")

	// The legacy report: no partial fields at all.
	legacy := `{"schema":1,"id":"job-1","status":"done","motivo":null,"resumo":"ok"}`
	if err := os.WriteFile(reportFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	mtimeBefore, err := os.Stat(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.run(t, "collect", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &got); err != nil {
		t.Fatal(err)
	}
	if got["partial"] != false {
		t.Fatalf("partial = %#v, want false", got["partial"])
	}
	if ids, ok := got["partial_collaborations"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("partial_collaborations = %#v, want the empty array", got["partial_collaborations"])
	}
	// The read changes nothing: the on-disk bytes and the mtime stand.
	after, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("report.json bytes changed by the read: %s", after)
	}
	mtimeAfter, err := os.Stat(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	if !mtimeBefore.ModTime().Equal(mtimeAfter.ModTime()) {
		t.Fatalf("report.json mtime changed by the read: %s -> %s", mtimeBefore.ModTime(), mtimeAfter.ModTime())
	}

	// --verify keeps hashing the original on-disk bytes (the legacy
	// report has no artefatos, so it passes over the untouched file).
	if code, _, errOut := f.run(t, "collect", "--id", "job-1", "--verify"); code != 0 || errOut != "" {
		t.Fatalf("verify code=%d err=%q, want the original bytes to verify", code, errOut)
	}

	// The control: a report that already carries the fields prints them
	// unchanged, and the file is left untouched.
	owned := `{"schema":1,"id":"job-1","status":"done","partial":true,"partial_collaborations":["c-0123456789abcdef"],"resumo":"ok"}`
	if err := os.WriteFile(reportFile, []byte(owned), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = f.run(t, "collect", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var kept map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &kept); err != nil {
		t.Fatal(err)
	}
	if kept["partial"] != true {
		t.Fatalf("partial = %#v, want the stored true to stand", kept["partial"])
	}
	if ids, ok := kept["partial_collaborations"].([]any); !ok || len(ids) != 1 || ids[0] != "c-0123456789abcdef" {
		t.Fatalf("partial_collaborations = %#v, want the stored ids to stand", kept["partial_collaborations"])
	}
	after, err = os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(owned), after) {
		t.Fatalf("report.json changed by the read: %s", after)
	}
}

// A legacy terminal state without a stored report reads as missing, not as
// complete: the wait/status line and the collect missing line print
// "report_missing":true only when the status carries a report (the
// reportSetState set) and the report is absent, with the partial facts
// still false and []; with a stored report, or in accepted and running,
// the field stays out and the line is exactly today's. The repeated reads
// write nothing: the state.json bytes, the job dir listing and the
// events.jsonl bytes stand after three waits and three collects.
//
// Catches the mutation "the report_missing marker dropped from the status
// or the collect-missing line": a legacy done job would then read as
// complete (report: null, exit 0).
func TestJobReportMissingLegacyTerminal(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")

	// The legacy terminal states without a report: the marker stands in
	// the wait and status lines and in the collect missing line, the
	// partial facts stay false and [], and both reads exit 0.
	for _, status := range []string{"done", "collected", "closed"} {
		f.setStatus(t, co, "job-1", status)
		wantWait := `{"id":"job-1","status":"` + status + `","motivo":null,"eventos":{"total":1,"ultimo_seq":1},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"report_missing":true,"held_controls":[]}`
		code, out, errOut := f.run(t, "wait", "--id", "job-1")
		if code != 0 || errOut != "" || out != wantWait+"\n" {
			t.Fatalf("%s wait code=%d out=%q err=%q", status, code, out, errOut)
		}
		// The status shares the line.
		code, out, errOut = f.run(t, "status", "--id", "job-1")
		if code != 0 || errOut != "" || out != wantWait+"\n" {
			t.Fatalf("%s status code=%d out=%q err=%q", status, code, out, errOut)
		}
		wantCollect := `{"id":"job-1","status":"` + status + `","report":null,"report_missing":true}`
		code, out, errOut = f.run(t, "collect", "--id", "job-1")
		if code != 0 || errOut != "" || out != wantCollect+"\n" {
			t.Fatalf("%s collect code=%d out=%q err=%q", status, code, out, errOut)
		}
	}

	// Control with a stored report: the marker is out and the wait line is
	// exactly today's; the collect prints the report body without the
	// marker.
	if err := os.WriteFile(filepath.Join(jobDir, "report.json"), []byte(`{"schema":1,"id":"job-1","status":"done","resumo":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.setStatus(t, co, "job-1", "done")
	code, out, errOut := f.run(t, "wait", "--id", "job-1")
	want := `{"id":"job-1","status":"done","motivo":null,"eventos":{"total":1,"ultimo_seq":1},"decisions_acked_seq":0,"decisions_pendentes":[],"partial":false,"partial_collaborations":[],"held_controls":[]}`
	if code != 0 || errOut != "" || out != want+"\n" {
		t.Fatalf("wait with report code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = f.run(t, "collect", "--id", "job-1")
	if code != 0 || errOut != "" || strings.Contains(out, "report_missing") {
		t.Fatalf("collect with report code=%d out=%q err=%q", code, out, errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "done" || got["resumo"] != "ok" {
		t.Fatalf("collect = %#v", got)
	}

	// Controls in the non-report states without a report: the marker is
	// out and the rest of the lines stands (wait exits 9 on the bound,
	// collect exits 0 with the missing line).
	if err := os.Remove(filepath.Join(jobDir, "report.json")); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"accepted", "running"} {
		f.setStatus(t, co, "job-1", status)
		code, out, errOut := f.run(t, "wait", "--id", "job-1", "--timeout", "100")
		if code != 9 || errOut != "" || !strings.Contains(out, `"status":"`+status+`"`) || strings.Contains(out, "report_missing") {
			t.Fatalf("%s wait code=%d out=%q err=%q", status, code, out, errOut)
		}
		wantCollect := `{"id":"job-1","status":"` + status + `","report":null}`
		code, out, errOut = f.run(t, "collect", "--id", "job-1")
		if code != 0 || errOut != "" || out != wantCollect+"\n" {
			t.Fatalf("%s collect code=%d out=%q err=%q", status, code, out, errOut)
		}
	}

	// The repeated reads write nothing: the state.json bytes, the job dir
	// listing and the events.jsonl bytes stand after three waits and
	// three collects.
	f.setStatus(t, co, "job-1", "done")
	stateFile := filepath.Join(jobDir, "state.json")
	eventsFile := filepath.Join(jobDir, "events.jsonl")
	listDir := func(t *testing.T, dir string) []string {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}
	hash := func(t *testing.T, path string) [32]byte {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(raw)
	}
	stateSum := hash(t, stateFile)
	eventsSum := hash(t, eventsFile)
	namesBefore := listDir(t, jobDir)
	treeBefore := treeSnapshot(t, jobDir)
	for i := 0; i < 3; i++ {
		if code, _, errOut := f.run(t, "wait", "--id", "job-1"); code != 0 || errOut != "" {
			t.Fatalf("wait %d code=%d err=%q", i, code, errOut)
		}
		if code, _, errOut := f.run(t, "collect", "--id", "job-1"); code != 0 || errOut != "" {
			t.Fatalf("collect %d code=%d err=%q", i, code, errOut)
		}
	}
	if got := hash(t, stateFile); got != stateSum {
		t.Fatalf("state.json changed by the repeated reads")
	}
	if got := hash(t, eventsFile); got != eventsSum {
		t.Fatalf("events.jsonl changed by the repeated reads")
	}
	if got := listDir(t, jobDir); !reflect.DeepEqual(namesBefore, got) {
		t.Fatalf("job dir listing changed by the repeated reads: %v -> %v", namesBefore, got)
	}
	assertJobTreeUnchanged(t, treeBefore, treeSnapshot(t, jobDir), "the repeated reads moved the job dir")
}
