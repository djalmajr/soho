package cli

import (
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func durableScratchDispatch(t *testing.T, kind, approvals string, extra ...fakecli.Rule) (*dispatchArrivalFixture, *jsonjs.Object) {
	t.Helper()
	extra = append(extra, fakecli.Rule{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Stdout: cursorTUIScreen})
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", extra...)
	worker := filepath.Join(f.root, "ignored-worktree")
	if err := os.Mkdir(worker, 0700); err != nil {
		t.Fatal(err)
	}
	roster := filepath.Join(f.state, "ws", "agents.tsv")
	raw, err := os.ReadFile(roster)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	cols := strings.Split(lines[1], "\t")
	cols[2], cols[6], cols[9] = kind, worker, approvals
	lines[1] = strings.Join(cols, "\t")
	if err := os.WriteFile(roster, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_PROMPT_CHECK_SECONDS"], f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "0", "1"
	f.env["TMPDIR"] = filepath.Join(f.root, "tmp")
	if err := os.Mkdir(f.env["TMPDIR"], 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		t.Setenv(key, f.env[key])
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || stderr != "" {
		t.Fatalf("dispatch exit=%d out=%s err=%s", code, out, stderr)
	}
	value, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
	if err != nil {
		t.Fatal(err)
	}
	return f, value.(*jsonjs.Object)
}

func durableField(t *testing.T, obj *jsonjs.Object, key string) string {
	t.Helper()
	value, _ := obj.Get(key)
	s, ok := value.(string)
	if !ok || s == "" {
		t.Fatalf("missing %s: %v", key, value)
	}
	return s
}

func TestDispatchScratchDurableRouting(t *testing.T) {
	for _, tc := range []struct {
		kind, approvals string
		scratch         bool
	}{{"pi", "full", false}, {"codex", "full", true}, {"codex", "unsandboxed", false}, {"cursor", "full", false}, {"claude", "full", false}} {
		t.Run(tc.kind+"-"+tc.approvals, func(t *testing.T) {
			f, obj := durableScratchDispatch(t, tc.kind, tc.approvals)
			report, brief, task := durableField(t, obj, "report"), durableField(t, obj, "composed_prompt"), durableField(t, obj, "task_report")
			want := filepath.Join(f.state, "ws", "reports")
			wantBrief := filepath.Join(f.state, "ws", "briefs")
			if tc.scratch {
				want = filepath.Join(f.env["TMPDIR"], "soho", "ws", "reports")
				wantBrief = want
			}
			volatile, present := obj.Get("report_volatile")
			if filepath.Dir(report) != want || filepath.Dir(brief) != wantBrief || filepath.Dir(task) != want || present != tc.scratch || (tc.scratch && volatile != true) {
				t.Fatalf("kind=%s approvals=%s want_dir=%s JSON=%s", tc.kind, tc.approvals, want, jsonjs.Stringify(obj))
			}
			t.Logf("kind=%s approvals=%s JSON=%s", tc.kind, tc.approvals, jsonjs.Stringify(obj))
		})
	}
}

func TestScratchDurableMirrorAndFallback(t *testing.T) {
	f, obj := durableScratchDispatch(t, "codex", "full")
	report := durableField(t, obj, "report")
	mirror := filepath.Join(f.state, "ws", "reports", filepath.Base(report))
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(report, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		body, err := os.ReadFile(mirror)
		if err != nil || string(body) != want {
			t.Fatalf("mirror=%q err=%v want=%q", body, err, want)
		}
	}
	write("draft one\n")
	code, out, stderr := runIn(t, []string{"status", "worker"}, f.env, f.root)
	if code != 0 {
		t.Fatalf("status=%d %s %s", code, out, stderr)
	}
	check("draft one\n")
	before, err := os.Stat(report)
	if err != nil {
		t.Fatal(err)
	}
	write("draft two\n")
	if err := os.Chtimes(report, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runIn(t, []string{"status", "worker"}, f.env, f.root)
	if code != 0 {
		t.Fatalf("status=%d %s %s", code, out, stderr)
	}
	check("draft one\n")
	code, out, stderr = runIn(t, []string{"wait", "worker", "--timeout", "1s"}, f.env, f.root)
	if code != 0 {
		t.Fatalf("wait=%d %s %s", code, out, stderr)
	}
	check("draft one\n")
	if _, err := os.Stat(report); err != nil {
		t.Fatalf("worker scratch report was removed: %v", err)
	}
	// This fixture-owned removal simulates reboot loss, as required by DJA-538.
	if err := os.RemoveAll(filepath.Join(f.env["TMPDIR"], "soho", "ws")); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runIn(t, []string{"status", "worker"}, f.env, f.root)
	if code != 0 || !strings.Contains(out, "\tdone\t") || !strings.Contains(out, mirror) {
		t.Fatalf("reboot status=%d out=%s err=%s", code, out, stderr)
	}
	code, out, stderr = runIn(t, []string{"wait", "worker", "--timeout", "1s"}, f.env, f.root)
	if code != 0 {
		t.Fatalf("reboot wait=%d out=%s err=%s", code, out, stderr)
	}
	value, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
	if err != nil {
		t.Fatal(err)
	}
	got := value.(*jsonjs.Object)
	status, _ := got.Get("status")
	path, _ := got.Get("report")
	if status != "done" || path != mirror {
		t.Fatalf("fallback JSON=%s", out)
	}
	t.Logf("recovered mirror=%s wait=%s", mirror, out)
}

func TestScratchDurableMirrorDoesNotFollowDestinationLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require developer mode or elevation")
	}
	f, obj := durableScratchDispatch(t, "codex", "full")
	report := durableField(t, obj, "report")
	dst := filepath.Join(f.state, "ws", "reports", filepath.Base(report))
	victim := filepath.Join(f.root, "keep.txt")
	if err := os.WriteFile(victim, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, dst); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("report\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runIn(t, []string{"status", "worker"}, f.env, f.root)
	if code != 0 {
		t.Fatalf("status=%d %s %s", code, out, stderr)
	}
	body, err := os.ReadFile(victim)
	if err != nil || string(body) != "keep\n" {
		t.Fatalf("victim=%q err=%v", body, err)
	}
	info, err := os.Lstat(dst)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unowned mirror link was replaced: %v %v", info, err)
	}
	body, err = os.ReadFile(dst)
	if err != nil || string(body) != "keep\n" {
		t.Fatalf("mirror=%q err=%v", body, err)
	}
}

func TestScratchDurableWaitDoesNotMirrorWhileWorking(t *testing.T) {
	f, obj := durableScratchDispatch(t, "codex", "full", fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("working", 1)})
	report := durableField(t, obj, "report")
	if err := os.WriteFile(report, []byte("unfinished draft\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_REPORT_WORKING_MAX"] = "600"
	code, out, stderr := runIn(t, []string{"wait", "worker", "--timeout", "200ms"}, f.env, f.root)
	if code != 9 {
		t.Fatalf("expected pending working report to time out: exit=%d out=%s err=%s", code, out, stderr)
	}
	mirror := filepath.Join(f.state, "ws", "reports", filepath.Base(report))
	if _, err := os.Lstat(mirror); !os.IsNotExist(err) {
		t.Fatalf("unfinished working report was mirrored: %v", err)
	}
	t.Logf("wait exit=%d without publishing an unfinished mirror", code)
}
