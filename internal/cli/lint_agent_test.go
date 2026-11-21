package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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
	sd := filepath.Join(root, "state", "ws")
	for _, dir := range []string{"home", "config", filepath.Join(sd, "briefs"), filepath.Join(sd, "reports"), workerCwd} {
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

	const inheritBody = "# Goal\nDo the slice.\n# Expected result\nDone.\n# Owned files\n- inherit: previous brief\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
	write("inherit.md", inheritBody)
	write("plain.md", strings.Replace(inheritBody, "- inherit: previous brief", "- prior.md", 1))
	hostile := "hostile\n\tbrief.md"
	write(hostile, inheritBody)

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
	run := func(args ...string) (int, string, string) {
		t.Helper()
		before := lintAgentTreeSnapshot(t, root)
		var stdout, stderr bytes.Buffer
		oldOut, oldErr := platform.Stdout, platform.Stderr
		platform.Stdout, platform.Stderr = &stdout, &stderr
		code := Run(append([]string{"lint"}, args...), env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		after := lintAgentTreeSnapshot(t, root)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("lint %v changed the state dir: before=%v after=%v", args, before, after)
		}
		return code, stdout.String(), stderr.String()
	}

	const hint = "soho: warning: brief inherit.md inherits Owned files from the worker's previous task; lint cannot check that without the worker: soho lint inherit.md --agent <name> (dispatch checks it)\n"
	code, out, errText := run("inherit.md")
	if code != 1 || out != "" || errText != hint {
		t.Fatalf("without --agent: code=%d stdout=%q stderr=%q; want %q", code, out, errText, hint)
	}

	const oldWarning = "soho: warning: brief inherit.md inherits Owned files but the worker has no previous task in this worktree\n"
	code, out, errText = run("inherit.md", "--agent", "fresh")
	if code != 1 || out != "" || errText != oldWarning {
		t.Fatalf("worker without previous task: code=%d stdout=%q stderr=%q; want %q", code, out, errText, oldWarning)
	}

	controlCode, controlOut, controlErr := run("plain.md")
	if controlCode != 0 || controlOut != "brief plain.md: ok\n" || controlErr != "" {
		t.Fatalf("control brief: code=%d stdout=%q stderr=%q", controlCode, controlOut, controlErr)
	}
	for _, args := range [][]string{
		{"inherit.md", "--agent", "worker"},
		{"--agent", "worker", "inherit.md"},
		{"--role", "implementer", "--agent", "worker", "inherit.md"},
		{"inherit.md", "--agent", "worker", "--role", "implementer"},
	} {
		code, out, errText = run(args...)
		if code != controlCode || out != "brief inherit.md: ok\n" || errText != "" {
			t.Fatalf("delivered previous task %v: code=%d stdout=%q stderr=%q", args, code, out, errText)
		}
	}

	code, out, errText = run("inherit.md", "--agent", "ghost")
	if code != 3 || out != "" || errText != "soho: lint: no agent 'ghost' in the roster\n" {
		t.Fatalf("agent outside roster: code=%d stdout=%q stderr=%q", code, out, errText)
	}
	code, out, errText = run("inherit.md", "--agent", "ghost\n\t'x")
	if code != 3 || out != "" || !strings.Contains(errText, "lint: no agent 'ghost\\n\\t'x' in the roster") {
		t.Fatalf("hostile agent name: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	const usage = "soho: usage: lint <brief.md> [--role <role>] [--agent <name>]\n"
	for _, args := range [][]string{
		{"inherit.md", "--agent"},
		{"--agent", "worker", "inherit.md", "--agent", "worker"},
		{"--role", "implementer", "--role", "reviewer", "inherit.md"},
		{"--agent", "worker", "--role"},
	} {
		code, out, errText = run(args...)
		if code != 2 || out != "" || errText != usage {
			t.Fatalf("usage %v: code=%d stdout=%q stderr=%q; want %q", args, code, out, errText, usage)
		}
	}

	code, out, errText = run(hostile)
	escaped := "brief hostile\\n\\tbrief.md inherits Owned files from the worker's previous task; lint cannot check that without the worker: soho lint hostile\\n\\tbrief.md --agent <name> (dispatch checks it)"
	if code != 1 || out != "" || !strings.Contains(errText, escaped) {
		t.Fatalf("hostile brief path: code=%d stdout=%q stderr=%q; want %q", code, out, errText, escaped)
	}
}
