package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-665/DJA-663: a cited path the worker's tree lacks but the
// orchestrator's checkout holds warns with the conditional message (warn
// exit 0, strict exit 2), and a span marked `example:` is an explicit
// example citation, not a reference. The fixture is a real temporary Git
// main checkout with a linked worktree; the worker branch is created
// before the main-only file is added.

func newGitCheckoutPair(t *testing.T) (main, worker string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	worker = filepath.Join(base, "worker")
	if err := os.MkdirAll(main, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(main, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Test")
	write(".gitignore", ".soho/\n")
	write("shared.txt", "shared\n")
	run("add", "-A")
	run("commit", "-qm", "fixture")
	// The worker branch and its linked worktree exist before the main-only
	// file, so the worktree holds the shared file and nothing main-only.
	run("worktree", "add", "-q", "-b", "worker", worker)
	write("main-only.txt", "main only\n")
	run("add", "main-only.txt")
	run("commit", "-qm", "main only")
	return main, worker
}

func TestDispatchUnseenCheckoutConditionalWarning(t *testing.T) {
	const cited = "main-only.txt"
	briefBody := func(worktree string) string {
		body := "# Goal\nrun\n# Expected result\nok\n# Owned files\nnone\n# Forbidden\nno commit or push\n# Report\ndone\n\nRead `" + cited + "`.\n"
		if worktree != "" {
			body = "Worktree: `" + worktree + "`\n\n" + body
		}
		return body
	}
	citeLine := func(body string) int {
		for i, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "Read `"+cited+"`") {
				return i + 1
			}
		}
		t.Fatalf("cite line not found in %q", body)
		return 0
	}
	setup := func(t *testing.T, lint string, main, worker string) *dispatchArrivalFixture {
		t.Helper()
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		// The orchestrator's cwd is the main checkout, so the lookup roots
		// are the main checkout; the roster cwd is the linked worktree.
		t.Chdir(main)
		roster := "worker\tw0test:p0a\tcodex\timplementer\topenai\t0\t" + worker + "\tnow\tgpt-5\ttask\timplementer\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_BRIEF_LINT"] = lint
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		return f
	}
	promptCalls := func(t *testing.T, f *dispatchArrivalFixture) int {
		t.Helper()
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return countArgvPrefix(calls, []string{"agent", "prompt"})
	}

	t.Run("warn mode exits 0 with the conditional message for a real missing reference", func(t *testing.T) {
		main, worker := newGitCheckoutPair(t)
		f := setup(t, "warn", main, worker)
		body := briefBody(worker)
		writeRoleFixture(t, f.brief, body)
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
		want := "soho: warning: brief " + f.brief + " line " + strconv.Itoa(citeLine(body)) +
			" cites '" + cited + "', which the worker in " + worker +
			" does not find at that relative path; a matching path exists at " + filepath.Join(main, cited) +
			" under checkout " + main + ", outside the worker tree; use it only if this brief intends that file"
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr is missing the conditional message:\n%s", stderr)
		}
		if status := dispatchOutputStatus(t, out); status != "submitted" {
			t.Fatalf("the warning must not block the send: status=%q out=%s", status, out)
		}
		if n := promptCalls(t, f); n != 1 {
			t.Fatalf("prompt calls = %d, want 1", n)
		}
	})

	t.Run("strict mode exits 2 for an unmarked reference and sends no prompt", func(t *testing.T) {
		main, worker := newGitCheckoutPair(t)
		f := setup(t, "strict", main, worker)
		writeRoleFixture(t, f.brief, briefBody(worker))
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 2 || !strings.Contains(stderr, "which the worker in "+worker) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
		if n := promptCalls(t, f); n != 0 {
			t.Fatalf("a strict refusal must send no prompt: prompt calls = %d", n)
		}
	})

	t.Run("strict mode sends a marked example", func(t *testing.T) {
		main, worker := newGitCheckoutPair(t)
		f := setup(t, "strict", main, worker)
		body := briefBody(worker)
		// The worker tree's `.git` is a file, so the relative path is
		// absent there while the main checkout holds the real .git/config.
		body = strings.Replace(body, "Read `"+cited+"`.", "Read example: `.git/config` for the config.", 1)
		writeRoleFixture(t, f.brief, body)
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("the marked example must not refuse: code=%d stderr=%s", code, stderr)
		}
		if strings.Contains(stderr, "which the worker in") {
			t.Fatalf("the marked example must not warn:\n%s", stderr)
		}
		if status := dispatchOutputStatus(t, out); status != "submitted" {
			t.Fatalf("status=%q out=%s", status, out)
		}
		if n := promptCalls(t, f); n != 1 {
			t.Fatalf("prompt calls = %d, want 1", n)
		}
	})

	t.Run("a path the declared tree holds is silent", func(t *testing.T) {
		main, worker := newGitCheckoutPair(t)
		f := setup(t, "warn", main, worker)
		body := briefBody(worker)
		body = strings.Replace(body, "Read `"+cited+"`.", "Read `shared.txt`.", 1)
		writeRoleFixture(t, f.brief, body)
		before := promptCalls(t, f)
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || strings.Contains(stderr, "which the worker in") {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
		if status := dispatchOutputStatus(t, out); status != "submitted" {
			t.Fatalf("status=%q out=%s", status, out)
		}
		if n := promptCalls(t, f) - before; n != 1 {
			t.Fatalf("the successful case must send one new prompt: new prompt calls = %d", n)
		}
	})

	amendment := func(t *testing.T, f *dispatchArrivalFixture, original string) {
		t.Helper()
		dir := filepath.Join(f.state, "ws", "briefs")
		report := filepath.Join(f.state, "ws", "reports", "worker-20200101T100000.md")
		stable := filepath.Join(f.state, "ws", "reports", "worker-20200101T100000.current.md")
		writeRoleFixture(t, filepath.Join(dir, "worker-20200101T100000.md"), "You are running as the `implementer` role\nWorktree: "+original+"\n")
		pointer, err := json.Marshal(map[string]any{"version": 1, "task_report": stable, "current": report, "history": []string{}})
		if err != nil {
			t.Fatal(err)
		}
		writeRoleFixture(t, taskreport.TaskReportPointerPath(filepath.Join(f.state, "ws"), "worker"), string(pointer)+"\n")
		writeRoleFixture(t, filepath.Join(f.state, "ws", "last-report-worker"), report+"\n")
	}

	t.Run("an amendment without a Worktree inherits the original brief's tree", func(t *testing.T) {
		main, worker := newGitCheckoutPair(t)
		f := setup(t, "strict", main, worker)
		// The original brief's tree holds the cited file; the roster cwd
		// (the worktree) does not, so only the inherited tree keeps the
		// dispatch silent.
		treeWith := t.TempDir()
		if err := os.WriteFile(filepath.Join(treeWith, cited), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		amendment(t, f, treeWith)
		amend := filepath.Join(f.root, "amend.md")
		writeRoleFixture(t, amend, "# Amendment\n\nRead `"+cited+"`.\n")
		before := promptCalls(t, f)
		code, _, stderr := f.run(t, "worker", amend, "--amend", "--no-wait")
		if code != 0 || strings.Contains(stderr, "which the worker in") {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
		if n := promptCalls(t, f) - before; n != 1 {
			t.Fatalf("the successful case must send one new prompt: new prompt calls = %d", n)
		}
	})

	t.Run("an amendment's own Worktree wins over the original's", func(t *testing.T) {
		main, worker := newGitCheckoutPair(t)
		f := setup(t, "strict", main, worker)
		own := t.TempDir()
		amend := filepath.Join(f.root, "amend.md")
		amendment(t, f, main)
		writeRoleFixture(t, amend, "# Amendment\n\nWorktree: "+own+"\n\nRead `"+cited+"`.\n")
		before := promptCalls(t, f)
		code, _, stderr := f.run(t, "worker", amend, "--amend", "--no-wait")
		if code != 2 || !strings.Contains(stderr, "which the worker in "+own) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
		if n := promptCalls(t, f) - before; n != 0 {
			t.Fatalf("a strict refusal must send no prompt: new prompt calls = %d", n)
		}
	})

	// F1 regression: a harmless line that ends exactly in the example word
	// used to panic the in-process dispatch. The regression recovers any
	// panic and fails it as an assertion, then requires the ordinary
	// successful submission with one new prompt.
	for _, line := range []string{"Read an example", "Café example"} {
		t.Run("a harmless line ending in the example word submits with a new prompt: "+line, func(t *testing.T) {
			main, worker := newGitCheckoutPair(t)
			f := setup(t, "warn", main, worker)
			body := briefBody(worker)
			body = strings.Replace(body, "Read `"+cited+"`.", line, 1)
			writeRoleFixture(t, f.brief, body)
			before := promptCalls(t, f)
			var code int
			var out, stderr string
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("dispatch panicked on the harmless line (F1 regression): %v", r)
					}
				}()
				code, out, stderr = f.run(t, "worker", f.brief, "--no-wait")
			}()
			if code != 0 {
				t.Fatalf("the harmless line must not fail the dispatch: code=%d stderr=%s", code, stderr)
			}
			if strings.Contains(stderr, "which the worker in") {
				t.Fatalf("the harmless line must not warn:\n%s", stderr)
			}
			if status := dispatchOutputStatus(t, out); status != "submitted" {
				t.Fatalf("status=%q out=%s", status, out)
			}
			if n := promptCalls(t, f) - before; n != 1 {
				t.Fatalf("new prompt calls = %d, want 1", n)
			}
		})
	}

	// F2 regression: a malformed closing backtick run must not gain the
	// example exemption; the citation stays visible, so strict mode
	// refuses it and sends no prompt.
	t.Run("strict mode refuses a malformed closing backtick run and sends no prompt", func(t *testing.T) {
		main, worker := newGitCheckoutPair(t)
		f := setup(t, "strict", main, worker)
		body := briefBody(worker)
		body = strings.Replace(body, "Read `"+cited+"`.", "Read example: `"+cited+"`` for the config.", 1)
		writeRoleFixture(t, f.brief, body)
		before := promptCalls(t, f)
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 2 || !strings.Contains(stderr, "which the worker in "+worker) {
			t.Fatalf("the malformed citation must stay visible and be refused: code=%d stderr=%s", code, stderr)
		}
		if n := promptCalls(t, f) - before; n != 0 {
			t.Fatalf("a strict refusal must send no prompt: new prompt calls = %d", n)
		}
	})
}
