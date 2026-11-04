package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/platform"
)

// TestHookFlaggedWorkerSilence covers the DJA-668 worker-silence decision:
// with --codex or --pi, `hook reminder` and `hook doctor` print nothing when
// the calling pane holds a row of the project roster (a worker another
// orchestrator opened); the peer check always runs, so a refused prompt still
// blocks; and an unreadable roster is treated as the orchestrator (prints
// normally, creates nothing). Outside Herdr the flag prints nothing.
func TestHookFlaggedWorkerSilence(t *testing.T) {
	for _, flag := range []string{"--codex", "--pi"} {
		t.Run(flag, func(t *testing.T) {
			h := newHookFix(t)
			// Orchestrator (no roster row): the reminder prints inside Herdr.
			code, out, stderr := h.runFlagged("reminder", flag, "ordinary prompt")
			if code != 0 || out != hookReminder+"\n" || stderr != "" {
				t.Fatalf("orchestrator reminder: code=%d out=%q stderr=%q", code, out, stderr)
			}
			// Outside Herdr nothing prints.
			h.env["HERDR_ENV"] = ""
			code, out, stderr = h.runFlagged("reminder", flag, "ordinary prompt")
			if code != 0 || out != "" || stderr != "" {
				t.Fatalf("outside Herdr: code=%d out=%q stderr=%q", code, out, stderr)
			}
			h.env["HERDR_ENV"] = "1"
			// A refused prompt blocks before the reminder: the block JSON
			// alone on stdout, for the orchestrator as well as for the worker.
			h.projectOff()
			id := nextID()
			code, out, _ = h.runFlagged("reminder", flag, localMessage(id))
			reason := refusedLine(id, peerauth.RefusedInboundOff) + "\nThe message did not reach the model; nothing needs clearing."
			raw, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
			if code != 0 || out != string(raw)+"\n" {
				t.Fatalf("orchestrator refusal: code=%d out=%q want=%s", code, out, raw)
			}
			// The caller pane holds a roster row (a worker): the reminder and
			// the doctor's warnings stay silent.
			writeWorkerRoster(t, h)
			code, out, stderr = h.runFlagged("reminder", flag, "ordinary prompt")
			if code != 0 || out != "" || stderr != "" {
				t.Fatalf("worker reminder: code=%d out=%q stderr=%q", code, out, stderr)
			}
			code, out, stderr = h.runFlagged("doctor", flag, "ordinary prompt")
			if code != 0 || out != "" || stderr != "" {
				t.Fatalf("worker doctor: code=%d out=%q stderr=%q", code, out, stderr)
			}
			// A refused prompt still blocks, with the block and no reminder.
			workerID := nextID()
			code, out, _ = h.runFlagged("reminder", flag, localMessage(workerID))
			workerReason := refusedLine(workerID, peerauth.RefusedInboundOff) + "\nThe message did not reach the model; nothing needs clearing."
			workerRaw, _ := json.Marshal(map[string]string{"decision": "block", "reason": workerReason})
			if code != 0 || out != string(workerRaw)+"\n" {
				t.Fatalf("worker refusal: code=%d out=%q want=%s", code, out, workerRaw)
			}
			// An unreadable roster is the orchestrator: the reminder prints and
			// the check creates nothing.
			h2 := newHookFix(t)
			if err := os.MkdirAll(filepath.Join(h2.env["SOHO_DIR"], "ws", "agents.tsv"), 0700); err != nil {
				t.Fatal(err)
			}
			before := stateDirNames(t, h2.env["SOHO_DIR"])
			code, out, stderr = h2.runFlagged("reminder", flag, "ordinary prompt")
			if code != 0 || out != hookReminder+"\n" || stderr != "" {
				t.Fatalf("unreadable roster reminder: code=%d out=%q stderr=%q", code, out, stderr)
			}
			after := stateDirNames(t, h2.env["SOHO_DIR"])
			if strings.Join(before, ",") != strings.Join(after, ",") {
				t.Fatalf("unreadable roster created %q -> %q", before, after)
			}
		})
	}
}

// writeWorkerRoster writes a roster row whose pane column is the caller pane
// (the fixture's w6X:p2), so hookCallerInRoster recognises it as a worker.
func writeWorkerRoster(t *testing.T, h *hookFix) {
	t.Helper()
	roster := filepath.Join(h.env["SOHO_DIR"], "ws", "agents.tsv")
	if err := os.WriteFile(roster, []byte("worker-1\tw6X:p2\tclaude\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// stateDirNames recursively lists every path under the state dir for the
// creates-nothing assertion.
func stateDirNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			t.Errorf("WalkDir path %q outside %q: %v", path, dir, err)
			return nil
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	return names
}

func TestNativeHookReminderAndOutsideHerdr(t *testing.T) {
	env, cwd := commandFixture(t)
	env["PATH"] = t.TempDir()
	for _, nowrite := range []string{"", "SOHO_NOWRITE", "SOHO_NOWRITE"} {
		delete(env, "SOHO_NOWRITE")
		delete(env, "SOHO_NOWRITE")
		if nowrite != "" {
			env[nowrite] = "1"
		}
		for _, mode := range []string{"reminder", "doctor"} {
			for _, value := range []string{"", "0"} {
				env["HERDR_ENV"] = value
				code, out, stderr := runIn(t, []string{"hook", mode}, env, cwd)
				if code != 0 || out != "" || stderr != "" {
					t.Fatalf("outside Herdr %s %s: %d %q %q", nowrite, mode, code, out, stderr)
				}
			}
		}
		env["HERDR_ENV"] = "1"
		code, out, stderr := runIn(t, []string{"hook", "reminder"}, env, cwd)
		if code != 0 || out != hookReminder+"\n" || stderr != "" {
			t.Fatalf("%s reminder: %d %q %q", nowrite, code, out, stderr)
		}
		code, out, stderr = runIn(t, []string{"hook", "doctor"}, env, cwd)
		if code != 0 || !strings.Contains(out, "soho doctor: ") || stderr != "" {
			t.Fatalf("%s doctor: %d %q %q", nowrite, code, out, stderr)
		}
	}
	delete(env, "SOHO_NOWRITE")
	delete(env, "SOHO_NOWRITE")
	env["HERDR_ENV"] = "1"
	code, out, stderr := runIn(t, []string{"hook", "reminder"}, env, cwd)
	if code != 0 || out != hookReminder+"\n" || stderr != "" {
		t.Fatalf("reminder: %d %q %q", code, out, stderr)
	}
	for _, args := range [][]string{{"hook"}, {"hook", "other"}, {"hook", "doctor", "--fix"}} {
		code, out, stderr := runIn(t, args, env, cwd)
		if code != 2 || out != "" || !strings.Contains(stderr, "usage: hook") {
			t.Fatalf("invalid hook %v: %d %q %q", args, code, out, stderr)
		}
	}
}

func TestNativeDoctorHookUsesClaudeProject(t *testing.T) {
	env, project := commandFixture(t)
	configDir := filepath.Join(project, ".agents")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "soho.conf")
	const config = "panes=9\n"
	if err := os.WriteFile(configFile, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	env["HERDR_ENV"], env["CLAUDE_PROJECT_DIR"], env["PATH"] = "1", project, t.TempDir()
	code, out, stderr := runIn(t, []string{"hook", "doctor"}, env, cwd)
	if code != 0 || !strings.Contains(out, "panes='9'") || stderr != "" {
		t.Fatalf("project doctor: %d %q %q", code, out, stderr)
	}
	data, err := os.ReadFile(configFile)
	if err != nil || string(data) != config {
		t.Fatalf("doctor changed project config: %q %v", data, err)
	}
	for _, dir := range []string{project, cwd} {
		for _, name := range []string{".herdr-soho", ".gitignore", ".claude"} {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Fatalf("doctor created %s in %s: %v", name, dir, err)
			}
		}
	}
}

func TestNativeDoctorHookReadOnlyWarnings(t *testing.T) {
	env, cwd := commandFixture(t)
	env["HERDR_ENV"] = "1"
	env["PATH"] = t.TempDir()
	// No installed shell, Herdr, or agent CLI is available. Doctor warnings
	// must still be reported without creating state or failing startup.
	code, out, stderr := runIn(t, []string{"hook", "doctor"}, env, cwd)
	if code != 0 || out == "" || stderr != "" {
		t.Fatalf("doctor hook: %d %q %q", code, out, stderr)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if !strings.HasPrefix(line, "soho doctor: ") {
			t.Fatalf("hook leaked non-warning doctor output: %q", line)
		}
	}
	for _, name := range []string{".herdr-soho", ".gitignore", ".claude"} {
		if _, err := os.Stat(filepath.Join(cwd, name)); !os.IsNotExist(err) {
			t.Fatalf("read-only hook created %s: %v", name, err)
		}
	}
}

func TestNativeDoctorHookMissingSkillIsAdvisory(t *testing.T) {
	env := platform.Env{"HERDR_ENV": "1", "SOHO_SKILL_DIR": filepath.Join(t.TempDir(), "missing"), "PATH": t.TempDir(), "HOME": t.TempDir(), "USERPROFILE": t.TempDir()}
	code, out, stderr := runIn(t, []string{"hook", "doctor"}, env, t.TempDir())
	if code != 0 || !strings.Contains(out, "soho doctor: ") || stderr != "" {
		t.Fatalf("missing skill hook: %d %q %q", code, out, stderr)
	}
}
