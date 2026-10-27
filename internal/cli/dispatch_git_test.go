package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
)

func headGit(t *testing.T, env platform.Env, tree string, args ...string) string {
	t.Helper()
	clean := platform.Env{}
	for k, v := range env {
		if k != "GIT_DIR" && k != "GIT_WORK_TREE" && k != "GIT_INDEX_FILE" {
			clean[k] = v
		}
	}
	clean["GIT_CONFIG_NOSYSTEM"] = "1"
	r := platform.RunCli("git", append([]string{"-C", tree}, args...), platform.RunOptions{Env: clean, TimeoutMs: 5000})
	if r.Status == nil || *r.Status != 0 || r.Error != "" {
		t.Fatalf("git %v tree=%q status=%v error=%s stderr=%s PATH=%s", args, tree, r.Status, r.Error, r.Stderr, clean["PATH"])
	}
	return strings.TrimSpace(r.Stdout)
}

func headInit(t *testing.T, env platform.Env, tree string) string {
	t.Helper()
	headGit(t, env, tree, "init", "-b", "main")
	// The fixture's own scaffolding (fake herdr logs, state, config) lives in
	// the tree, the way a real project keeps .soho/ ignored: exclude it so the
	// settle audit (DJA-543) sees only the worker's changes.
	exclude := filepath.Join(tree, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte("bin/\nstate/\nhome/\nconf/\nroles/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	headGit(t, env, tree, "config", "user.name", "Test Worker")
	headGit(t, env, tree, "config", "user.email", "worker@example.invalid")
	headGit(t, env, tree, "config", "core.hooksPath", filepath.Join(tree, "no-hooks"))
	headGit(t, env, tree, "commit", "--allow-empty", "-m", "initial")
	return headGit(t, env, tree, "rev-parse", "HEAD")
}

func headSidecar(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDispatchHeadWait(t *testing.T) {
	for _, name := range []string{"local-declared", "unchanged", "remote", "branch", "both", "roster", "non-git", "detached", "missing-declared"} {
		t.Run(name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
			f.env["SOHO_METRICS"] = "off"
			f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
			f.env["SOHO_PRESSURE_REFUSE"], f.env["SOHO_PRESSURE_WARN"] = "off", "off"
			f.env["GIT_CONFIG_NOSYSTEM"] = "1"
			// Each fixture isolates Git configuration and all test-owned roots.
			// The production commands must ignore these inherited redirections.
			f.env["GIT_DIR"], f.env["GIT_WORK_TREE"], f.env["GIT_INDEX_FILE"] = "bad-dir", "bad-tree", "bad-index"
			tree := f.root
			body := "# Goal\nDo the task.\n"
			if name == "local-declared" {
				_ = headInit(t, f.env, f.root)
				tree = filepath.Join(t.TempDir(), "declared tree with spaces")
				if err := os.Mkdir(tree, 0o700); err != nil {
					t.Fatal(err)
				}
				body += "Worktree: `" + tree + "` (branch main)\n"
			}
			if name == "missing-declared" {
				body += "Worktree: `" + filepath.Join(f.root, "absent") + "`\n"
			}
			// Roster cwd is column 6 (zero based), inside the fixture state root.
			roster := filepath.Join(f.state, "ws", "agents.tsv")
			raw, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			reopenDispatchWrite(t, roster, strings.Replace(string(raw), "\t0\t\tnow\t", "\t0\t"+f.root+"\tnow\t", 1))
			old := ""
			if name != "non-git" {
				old = headInit(t, f.env, tree)
				if name == "detached" {
					headGit(t, f.env, tree, "checkout", "--detach", old)
				}
			}
			reopenDispatchWrite(t, f.brief, body)
			composed, report := firstDispatch(t, f)
			path := dispatch.DispatchSidecar(composed)
			side := headSidecar(t, path)
			if name == "non-git" {
				if _, ok := side["git_tree"]; ok {
					t.Fatalf("non-git metadata: %v", side)
				}
			} else {
				branch := "main"
				if name == "detached" {
					branch = ""
				}
				if side["git_tree"] != tree || side["git_head"] != old || side["git_branch"] != branch {
					t.Fatalf("identity=%v want tree=%s head=%s branch=%q", side, tree, old, branch)
				}
			}
			reopenDispatchWrite(t, report, "Completed.\n")
			// Capture the exact pre-feature output through the same public CLI.
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			baseline := headSidecar(t, path)
			for _, k := range []string{"git_head", "git_branch", "git_tree"} {
				delete(baseline, k)
			}
			data, _ := json.Marshal(baseline)
			reopenDispatchWrite(t, path, string(data))
			code, before, stderr := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "1s")
			if code != 0 || stderr != "" {
				t.Fatalf("baseline wait=%d out=%s err=%s", code, before, stderr)
			}
			reopenDispatchWrite(t, path, string(saved))
			if name == "branch" || name == "both" {
				headGit(t, f.env, tree, "checkout", "-b", "other")
			}
			local := name == "local-declared" || name == "roster" || name == "both" || name == "remote"
			next := old
			if local {
				headGit(t, f.env, tree, "commit", "--allow-empty", "-m", "worker commit")
				next = headGit(t, f.env, tree, "rev-parse", "HEAD")
			}
			if name == "remote" {
				headGit(t, f.env, tree, "update-ref", "refs/remotes/origin/main", next)
				local = false
			}
			code, out, stderr := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "1s")
			if code != 0 {
				t.Fatalf("wait=%d out=%s err=%s", code, out, stderr)
			}
			branchChange := name == "branch" || name == "both"
			if strings.Contains(stderr, "left a local commit") != local || strings.Contains(stderr, "from branch main to other") != branchChange {
				t.Fatalf("wrong warnings: %s", stderr)
			}
			if local && !strings.Contains(stderr, "HEAD moved from "+old[:7]+" to "+next[:7]) {
				t.Fatalf("missing short SHAs: %s", stderr)
			}
			if local || branchChange {
				var line map[string]any
				if err := json.Unmarshal([]byte(out), &line); err != nil {
					t.Fatal(err)
				}
				moved, ok := line["head_moved"].(map[string]any)
				if !ok || moved["from"] != old || moved["to"] != next || moved["branch_from"] != "main" {
					t.Fatalf("missing movement: %s", out)
				}
				t.Logf("wait exit=%d\n%s%s", code, out, stderr)
			} else if out != before || stderr != "" || strings.Contains(out, "head_moved") {
				t.Fatalf("unchanged output differs: before=%s after=%s err=%s", before, out, stderr)
			}
			code, again, stderr := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "1s")
			if code != 0 || stderr != "" || again != before {
				t.Fatalf("second wait repeated warning: %d %s %s", code, again, stderr)
			}
			if name != "non-git" && headSidecar(t, path)["git_checked"] != true {
				t.Fatalf("no once marker: %v", headSidecar(t, path))
			}
			t.Logf("%s: snapshot, warning/byte parity, second wait exit=0", name)
		})
	}
}

func TestDispatchHeadAmendment(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
	old := headInit(t, f.env, f.root)
	composed, _ := firstDispatch(t, f)
	first := headSidecar(t, dispatch.DispatchSidecar(composed))
	headGit(t, f.env, f.root, "commit", "--allow-empty", "-m", "worker commit")
	reopenDispatchWrite(t, f.brief, "Amend the task.\n")
	code, out, stderr := f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if code != 0 {
		t.Fatalf("amend=%d out=%s err=%s", code, out, stderr)
	}
	amended, _ := dispatchOutputField(t, out, "composed_prompt")
	side := headSidecar(t, dispatch.DispatchSidecar(amended))
	if side["git_head"] != old || side["git_tree"] != first["git_tree"] || side["git_branch"] != first["git_branch"] {
		t.Fatalf("amend re-recorded: before=%v after=%v", first, side)
	}
	// Exercise settlement of the amendment, not the original report.
	report, _ := dispatchOutputField(t, out, "report")
	reopenDispatchWrite(t, report, "Completed amendment.\n")
	code, out, stderr = reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "1s")
	if code != 0 || !strings.Contains(stderr, "left a local commit") || !strings.Contains(out, "head_moved") {
		t.Fatalf("amended wait=%d out=%s err=%s", code, out, stderr)
	}
	t.Logf("amend retained first HEAD=%s; settled amended task exit=0", old)
}

func TestCollectHead(t *testing.T) {
	for _, name := range []string{"local", "remote", "unchanged", "nowrite"} {
		t.Run(name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
			f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
			old := headInit(t, f.env, f.root)
			composed, report := firstDispatch(t, f)
			reopenDispatchWrite(t, report, "Completed.\n")
			next := old
			if name != "unchanged" {
				headGit(t, f.env, f.root, "commit", "--allow-empty", "-m", "worker commit")
				next = headGit(t, f.env, f.root, "rev-parse", "HEAD")
			}
			if name == "remote" {
				headGit(t, f.env, f.root, "update-ref", "refs/remotes/origin/main", next)
			}
			if name == "nowrite" {
				f.env["SOHO_NOWRITE"] = "1"
			}
			code, out, stderr := reopenDispatchRun(t, f.env, "collect", "worker")
			if code != 0 || !strings.Contains(out, "Completed.\n") {
				t.Fatalf("collect=%d out=%s err=%s", code, out, stderr)
			}
			if name == "local" {
				if !strings.Contains(stderr, "HEAD moved from "+old[:7]+" to "+next[:7]) {
					t.Fatalf("collect omitted warning: %s", stderr)
				}
			} else if stderr != "" {
				t.Fatalf("collect unexpected warning: %s", stderr)
			}
			if name == "nowrite" {
				if _, checked := headSidecar(t, dispatch.DispatchSidecar(composed))["git_checked"]; checked {
					t.Fatal("read-only collect marked the task checked")
				}
				return
			}
			code, again, stderr := reopenDispatchRun(t, f.env, "collect", "worker")
			if code != 0 || again != out || stderr != "" {
				t.Fatalf("repeat collect=%d out=%s err=%s", code, again, stderr)
			}
			code, out, stderr = reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "1s")
			if code != 0 || stderr != "" || strings.Contains(out, "head_moved") {
				t.Fatalf("wait after collect=%d out=%s err=%s", code, out, stderr)
			}
			t.Logf("%s: collect exit=0, report retained, repeat collect/wait silent", name)
		})
	}
}
