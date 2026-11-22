package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

// lintAgentTreeSnapshot records every path, mode and file digest under root:
// lint must not create or change a roster, pointer, report or friction line.
func lintAgentTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		digest := ""
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest = fmt.Sprintf("%x", sha256.Sum256(body))
		}
		out[filepath.ToSlash(rel)] = fmt.Sprintf("%s:%s", info.Mode(), digest)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLintAgentInheritOwnedFiles(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	workerCwd := filepath.Join(root, "wt")
	otherCwd := filepath.Join(root, "other")
	missingCwd := filepath.Join(root, "missing-tree")
	sd := filepath.Join(root, "state", "ws")
	for _, dir := range []string{"home", "config", filepath.Join(sd, "briefs"), filepath.Join(sd, "reports"), workerCwd, otherCwd} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A delivered previous task in the worker's own cwd, built like the
	// dispatch inherit fixture: pointer, sidecar, composed brief and report.
	report := filepath.Join(sd, "reports", "prior.md")
	write(report, "## Files changed\n- prior.md\n")
	write(filepath.Join(sd, "task-report-worker.json"), fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[]}`, report, report))
	write(filepath.Join(sd, "briefs", "prior.dispatch.json"), fmt.Sprintf(`{"git_tree":%q,"task_report":%q}`, workerCwd, report))
	write(filepath.Join(sd, "briefs", "prior.md"), "# Brief\n# Owned files\n- prior.md\n# Report contract\n")
	write(filepath.Join(workerCwd, "prior.md"), "prior\n")
	write(filepath.Join(sd, "agents.tsv"), "worker\tw0:p0\tcodex\timplementer\topenai\t0\t"+workerCwd+"\tnow\tgpt-5\ttask\timplementer\n"+
		"fresh\tw0:p1\tcodex\timplementer\topenai\t0\t"+workerCwd+"\tnow\tgpt-5\ttask\timplementer\n")
	// F1: a relative agents.tsv in the process cwd must never be a fallback
	// when no state dir is admitted.
	write("agents.tsv", "stray\tw0:p9\tcodex\timplementer\topenai\t0\t"+workerCwd+"\tnow\tgpt-5\ttask\timplementer\n")

	const inheritBody = "# Goal\nDo the slice.\n# Expected result\nDone.\n# Owned files\n- inherit: previous brief\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
	withTree := func(tree string) string { return "Worktree: `" + tree + "`\n" + inheritBody }
	write("inherit.md", inheritBody)
	write("plain.md", strings.Replace(inheritBody, "- inherit: previous brief", "- prior.md", 1))
	write("space brief.md", inheritBody)
	write("worker's brief.md", inheritBody)
	write("declared-worker.md", withTree(workerCwd))
	write("declared-other.md", withTree(otherCwd))
	write("declared-missing.md", withTree(missingCwd))

	env := platform.Env{
		"PATH":               testutil.HostPath(),
		"HOME":               filepath.Join(root, "home"),
		"USERPROFILE":        filepath.Join(root, "home"),
		"XDG_CONFIG_HOME":    filepath.Join(root, "config"),
		"TMPDIR":             root,
		"SOHO_DIR":           filepath.Join(root, "state"),
		"SOHO_SKILL_DIR":     testSkillDir(t),
		"SOHO_BRIEF_LINT":    "warn",
		"HERDR_WORKSPACE_ID": "ws",
	}
	run := func(t *testing.T, runEnv platform.Env, args ...string) (int, string, string) {
		t.Helper()
		before := lintAgentTreeSnapshot(t, root)
		var stdout, stderr bytes.Buffer
		oldOut, oldErr := platform.Stdout, platform.Stderr
		platform.Stdout, platform.Stderr = &stdout, &stderr
		code := Run(append([]string{"lint"}, args...), runEnv)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		after := lintAgentTreeSnapshot(t, root)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("lint %v changed the state dir: before=%v after=%v", args, before, after)
		}
		return code, stdout.String(), stderr.String()
	}
	runEnv := func(args ...string) (int, string, string) { return run(t, env, args...) }

	const hint = "soho: warning: brief inherit.md inherits Owned files from the worker's previous task; lint cannot check that without the worker: soho lint inherit.md --agent <name> (dispatch checks it)\n"
	code, out, errText := runEnv("inherit.md")
	if code != 1 || out != "" || errText != hint {
		t.Fatalf("without --agent: code=%d stdout=%q stderr=%q; want %q", code, out, errText, hint)
	}

	const oldWarning = "brief %s inherits Owned files but the worker has no previous task in this worktree"
	code, out, errText = runEnv("inherit.md", "--agent", "fresh")
	if code != 1 || out != "" || errText != "soho: warning: "+fmt.Sprintf(oldWarning, "inherit.md")+"\n" {
		t.Fatalf("worker without previous task: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	controlCode, controlOut, controlErr := runEnv("plain.md")
	if controlCode != 0 || controlOut != "brief plain.md: ok\n" || controlErr != "" {
		t.Fatalf("control brief: code=%d stdout=%q stderr=%q", controlCode, controlOut, controlErr)
	}
	for _, args := range [][]string{
		{"inherit.md", "--agent", "worker"},
		{"--agent", "worker", "inherit.md"},
		{"--role", "implementer", "--agent", "worker", "inherit.md"},
		{"inherit.md", "--agent", "worker", "--role", "implementer"},
	} {
		code, out, errText = runEnv(args...)
		if code != controlCode || out != "brief inherit.md: ok\n" || errText != "" {
			t.Fatalf("delivered previous task %v: code=%d stdout=%q stderr=%q", args, code, out, errText)
		}
	}

	code, out, errText = runEnv("inherit.md", "--agent", "ghost")
	if code != 3 || out != "" || errText != "soho: lint: no agent 'ghost' in the roster\n" {
		t.Fatalf("agent outside roster: code=%d stdout=%q stderr=%q", code, out, errText)
	}
	code, out, errText = runEnv("inherit.md", "--agent", "ghost\n\t'x")
	if code != 3 || out != "" || !strings.Contains(errText, "lint: no agent 'ghost\\n\\t'x' in the roster") {
		t.Fatalf("hostile agent name: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	// F1: the roster in the process cwd names 'stray', but no state dir is
	// admitted, so it must not be read.
	noState := env.Clone()
	noState["SOHO_DIR"] = filepath.Join(root, "absent-state")
	code, out, errText = run(t, noState, "inherit.md", "--agent", "stray")
	if code != 3 || out != "" || errText != "soho: lint: no agent 'stray' in the roster\n" {
		t.Fatalf("no admitted state dir: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	const usage = "soho: usage: lint <brief.md> [--role <role>] [--agent <name>]\n"
	for _, args := range [][]string{
		{"inherit.md", "--agent"},
		{"--agent", "worker", "inherit.md", "--agent", "worker"},
		{"--role", "implementer", "--role", "reviewer", "inherit.md"},
		{"--agent", "worker", "--role"},
		{"", "inherit.md"},
		{"--agent", "worker", "", "inherit.md"},
		{"inherit.md", ""},
		{"inherit.md", "--agent", "-h"},
		{"inherit.md", "--agent", "--help"},
		{"inherit.md", "--role", "-x"},
	} {
		code, out, errText = runEnv(args...)
		if code != 2 || out != "" || errText != usage {
			t.Fatalf("usage %q: code=%d stdout=%q stderr=%q; want %q", args, code, out, errText, usage)
		}
	}

	for _, tc := range []struct{ brief, shellWord string }{
		{"space brief.md", "'space brief.md'"},
		{"worker's brief.md", `'worker'\''s brief.md'`},
	} {
		code, out, errText = runEnv(tc.brief)
		want := fmt.Sprintf("soho: warning: brief %[1]s inherits Owned files from the worker's previous task; lint cannot check that without the worker: soho lint %[1]s --agent <name> (dispatch checks it)\n", tc.shellWord)
		if code != 1 || out != "" || errText != want {
			t.Fatalf("hint for %q: code=%d stdout=%q stderr=%q; want %q", tc.brief, code, out, errText, want)
		}
	}

	// An existing declared tree decides the previous task. An absent or
	// nonexistent declaration falls back to the roster's cwd.
	for _, tc := range []struct {
		brief, wantOut, wantErr string
		code                    int
	}{
		{"declared-worker.md", "brief declared-worker.md: ok\n", "", 0},
		{"declared-other.md", "", "soho: warning: " + fmt.Sprintf(oldWarning, "declared-other.md") + "\n", 1},
		{"declared-missing.md", "brief declared-missing.md: ok\n", "", 0},
	} {
		code, out, errText = runEnv(tc.brief, "--agent", "worker")
		if code != tc.code || out != tc.wantOut || errText != tc.wantErr {
			t.Fatalf("worktree %s: code=%d stdout=%q stderr=%q; want %d %q %q", tc.brief, code, out, errText, tc.code, tc.wantOut, tc.wantErr)
		}
	}

	// F7: off mode still validates arguments, role and roster first.
	off := env.Clone()
	off["SOHO_BRIEF_LINT"] = "off"
	code, out, errText = run(t, off, "inherit.md", "--agent", "worker")
	if code != 0 || out != "brief inherit.md: lint off (brief_lint=off)\n" || errText != "" {
		t.Fatalf("off with valid agent: code=%d stdout=%q stderr=%q", code, out, errText)
	}
	code, out, errText = run(t, off, "inherit.md", "--agent", "ghost")
	if code != 3 || out != "" || errText != "soho: lint: no agent 'ghost' in the roster\n" {
		t.Fatalf("off with ghost agent: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	t.Run("control-bearing brief paths", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows filenames cannot contain control characters")
		}
		for _, tc := range []struct{ name, brief, escaped string }{
			{"tab", "with\ttab.md", `with\ttab.md`},
			{"newline", "with\nnewline.md", `with\nnewline.md`},
			{"other controls", "with\r\x1b\x7f\u0085controls.md", `with\r\x1b\x7f\u0085controls.md`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				write(tc.brief, inheritBody)
				code, out, errText := run(t, env, tc.brief)
				wantHint := "soho: warning: brief " + tc.escaped + " inherits Owned files from the worker's previous task; lint cannot check that without the worker: rerun the original invocation with --agent <name> (dispatch checks it)\n"
				if code != 1 || out != "" || errText != wantHint {
					t.Errorf("control path without agent: code=%d stdout=%q stderr=%q; want %q", code, out, errText, wantHint)
				}
				code, out, errText = run(t, env, tc.brief, "--agent", "fresh")
				wantFresh := "soho: warning: " + fmt.Sprintf(oldWarning, tc.escaped) + "\n"
				if code != 1 || out != "" || errText != wantFresh {
					t.Errorf("control path with fresh agent: code=%d stdout=%q stderr=%q; want %q", code, out, errText, wantFresh)
				}
				code, out, errText = run(t, env, tc.brief, "--agent", "worker")
				if code != 0 || out != "brief "+tc.escaped+": ok\n" || errText != "" {
					t.Errorf("control path with delivered agent: code=%d stdout=%q stderr=%q", code, out, errText)
				}
			})
		}

		const unseen = "unseen\n\tbrief.md"
		const unseenEscaped = `unseen\n\tbrief.md`
		write(unseen, strings.Replace(inheritBody, "- inherit: previous brief", "- absent.go", 1))
		code, out, errText = run(t, env, unseen, "--agent", "worker")
		wantUnseen := "soho: warning: brief " + unseenEscaped + ": Owned files item 'absent.go' is not a file at the worktree root; write its path from the root\n"
		if code != 1 || out != "" || errText != wantUnseen {
			t.Fatalf("agent-added warning: code=%d stdout=%q stderr=%q; want %q", code, out, errText, wantUnseen)
		}
	})

	t.Run("worker path classification", func(t *testing.T) {
		write("main-only.txt", "checkout only\n")
		write(filepath.Join(otherCwd, "main-only.txt"), "in declared worktree\n")
		base := strings.Replace(inheritBody, "- inherit: previous brief", "- None.", 1)
		const cite = "Read `main-only.txt`.\n"
		strict := env.Clone()
		strict["SOHO_BRIEF_LINT"] = "strict"
		diagnostic := func(brief string) string {
			return "brief " + brief + " line 11 cites 'main-only.txt', which the worker in " + workerCwd +
				" does not find at that relative path; a matching path exists at " + filepath.Join(root, "main-only.txt") +
				" under checkout " + root + ", outside the worker tree; use it only if this brief intends that file"
		}
		write("cited.md", base+cite)
		code, out, errText := run(t, env, "cited.md", "--agent", "worker")
		if want := "soho: warning: " + diagnostic("cited.md") + "\n"; code != 1 || out != "" || errText != want {
			t.Errorf("warn worker-only path: code=%d stdout=%q stderr=%q; want %q", code, out, errText, want)
		}
		type pathCase struct{ brief, escaped string }
		strictCases := []pathCase{{"cited.md", "cited.md"}}
		if runtime.GOOS != "windows" {
			strictCases = append(strictCases, pathCase{"cited\t\nbrief.md", `cited\t\nbrief.md`})
		}
		for _, tc := range strictCases {
			write(tc.brief, base+cite)
			code, out, errText := run(t, strict, tc.brief, "--agent", "worker")
			if want := "soho: " + diagnostic(tc.escaped) + " (brief_lint=strict)\n"; code != 2 || out != "" || errText != want {
				t.Errorf("strict worker-only path: code=%d stdout=%q stderr=%q; want %q", code, out, errText, want)
			}
		}
		write("missing-item.md", strings.Replace(base, "- None.", "- absent.go", 1))
		code, out, errText = run(t, strict, "missing-item.md", "--agent", "worker")
		if want := "soho: warning: brief missing-item.md: Owned files item 'absent.go' is not a file at the worktree root; write its path from the root\n"; code != 1 || out != "" || errText != want {
			t.Errorf("strict ordinary missing item: code=%d stdout=%q stderr=%q; want %q", code, out, errText, want)
		}
		write("declared-has-path.md", "Worktree: `"+otherCwd+"`\n"+base+cite)
		write("example.md", base+"Read example: `main-only.txt`.\n")
		for _, args := range [][]string{
			{"cited.md"},
			{"declared-has-path.md", "--agent", "worker"},
			{"example.md", "--agent", "worker"},
		} {
			code, out, errText := run(t, strict, args...)
			if code != 0 || out != "brief "+args[0]+": ok\n" || errText != "" {
				t.Errorf("strict control %v: code=%d stdout=%q stderr=%q", args, code, out, errText)
			}
		}
	})
}

func TestLintAgentSkillOffModeDoc(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(testSkillDir(t), "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "after argument, role and roster validation, `brief_lint=off` disables the brief diagnostics"
	if !strings.Contains(string(body), want) {
		t.Fatalf("SKILL.md lacks %q", want)
	}
}
