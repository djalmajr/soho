package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/eval"
)

// validateAllPassMD is a filled VALIDATION.md with two n/a layers in the
// middle, so a passing run proves the n/a layers sit between the ones that
// run (DJA-695 fatia 3, scenario 1).
const validateAllPassMD = "## Test layers\n" +
	"- unit: `echo unit-pass`\n" +
	"- integration: n/a: no separate integration layer\n" +
	"- contract: `echo contract-pass`\n" +
	"- e2e: `echo e2e-pass`\n" +
	"- mutation: `echo mutation-pass`\n"

// validateSentinelMD is a filled file whose unit layer creates a sentinel
// file: if any command of the file runs, the sentinel exists.
const validateSentinelMD = "## Test layers\n" +
	"- unit: `touch sentinel`\n" +
	"- integration: n/a: no separate integration layer\n" +
	"- contract: n/a: no separate contract layer\n" +
	"- e2e: n/a: no separate e2e layer\n" +
	"- mutation: n/a: no separate mutation layer\n"

func writeValidationMD(t *testing.T, cwd, content string) string {
	t.Helper()
	p := filepath.Join(cwd, "VALIDATION.md")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// hasLinePrefix reports whether out has a line starting with prefix.
func hasLinePrefix(out, prefix string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// hasLineMatch reports whether out has a line matching re (full line).
func hasLineMatch(out string, re *regexp.Regexp) bool {
	for _, line := range strings.Split(out, "\n") {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// requireSh skips, with the reason, when the host has no sh: the tests
// run the file's layers through `sh -c`, which the command requires.
func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not in PATH: %v", err)
	}
}

// validateEvidenceDir reads the evidence directory from a validate text run.
func validateEvidenceDir(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "evidence: "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no evidence line in:\n%s", out)
	return ""
}

// TestValidateAllPassCoversNAInMiddle covers scenario 1: every layer passes
// with n/a layers in the middle — validation: PASS, exit 0.
func TestValidateAllPassCoversNAInMiddle(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateAllPassMD)
	code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) == 0 || lines[0] != "validation: PASS" {
		t.Fatalf("first line = %q, want validation: PASS", lines[0])
	}
	// The fixture repo has no commit yet: the commit line is none.
	if !hasLinePrefix(out, "commit: none") {
		t.Fatalf("no 'commit: none' line in:\n%s", out)
	}
	for _, prefix := range []string{"unit: PASS (", "integration: n/a", "contract: PASS (", "e2e: PASS (", "mutation: PASS ("} {
		if !hasLinePrefix(out, prefix) {
			t.Fatalf("missing layer line %q in:\n%s", prefix, out)
		}
	}
	dir := validateEvidenceDir(t, out)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("the evidence directory does not exist: %s (%v)", dir, err)
	}
	// Each executed layer wrote a log, and the summary exists.
	for _, name := range []string{"unit.log", "contract.log", "e2e.log", "mutation.log", "summary.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing evidence file %s: %v", name, err)
		}
	}
}

// TestValidateCommitLineShowsTheCommit covers decision 10: inside a
// repository the commit line carries the sha and the tree state; a dirty
// tree is shown, never refused.
func TestValidateCommitLineShowsTheCommit(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateAllPassMD)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s (%v)", args, out, err)
		}
	}
	// Commit the file too, so the tree is clean before the run.
	git("add", "VALIDATION.md")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "seed")
	code, out, _ := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 5 {
		t.Fatalf("code=%d (unit PASS, the other command layer NOT RUN: the overall is NOT RUN) out=%s", code, out)
	}
	commitRE := regexp.MustCompile(`^commit: [0-9a-f]{40} \(clean\)$`)
	if !hasLineMatch(out, commitRE) {
		t.Fatalf("no clean commit line in:\n%s", out)
	}
	if !hasLinePrefix(out, "contract: NOT RUN: not selected") {
		t.Fatalf("the unselected command layer is not NOT RUN in:\n%s", out)
	}
	// Dirty: show, do not refuse.
	if err := os.WriteFile(filepath.Join(cwd, "dirty.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 5 || !hasLineMatch(out, regexp.MustCompile(`^commit: [0-9a-f]{40} \(dirty\)$`)) {
		t.Fatalf("dirty run code=%d out=%s", code, out)
	}
}

// TestValidateFailureDoesNotStopTheRun covers scenario 2: one layer fails,
// the following layers still run, the overall is FAIL, exit 1.
func TestValidateFailureDoesNotStopTheRun(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `exit 1`\n" +
		"- integration: n/a: no separate integration layer\n" +
		"- contract: `touch sentinel-after-fail`\n" +
		"- e2e: n/a: no separate e2e layer\n" +
		"- mutation: n/a: no separate mutation layer\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
	if code != 1 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: FAIL" {
		t.Fatalf("first line = %q, want validation: FAIL", lines[0])
	}
	if !hasLinePrefix(out, "unit: FAIL (") || !strings.Contains(out, ": exit 1") {
		t.Fatalf("the failing layer is not FAIL with its exit in:\n%s", out)
	}
	// The contract layer's touch prints nothing and passes; the sentinel
	// file proves the layer ran to completion.
	if !hasLinePrefix(out, "contract: PASS") {
		t.Fatalf("the layer after the failure did not run:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(cwd, "sentinel-after-fail")); err != nil {
		t.Fatalf("the layer after the failure did not run (no sentinel): %v", err)
	}
}

// TestValidateBlockedOutcomes covers scenario 3: exit 127, exit 126 and a
// missing sh are BLOCKED with the reason; the overall is BLOCKED, exit 5.
func TestValidateBlockedOutcomes(t *testing.T) {
	for _, c := range []struct {
		name    string
		command string
		reason  string
		noSh    bool
	}{
		{"exit 127 is BLOCKED", "exit 127", "exit 127", false},
		{"exit 126 is BLOCKED", "exit 126", "exit 126", false},
		{"sh not in PATH is BLOCKED", "echo x", "sh not in PATH", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !c.noSh {
				requireSh(t)
			}
			env, cwd := commandFixture(t)
			if c.noSh {
				bin := t.TempDir()
				env["PATH"] = bin
			}
			content := "## Test layers\n" +
				"- unit: `" + c.command + "`\n" +
				"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
			writeValidationMD(t, cwd, content)
			code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
			if code != 5 || errOut != "" {
				t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
			}
			if lines := strings.Split(out, "\n"); lines[0] != "validation: BLOCKED" {
				t.Fatalf("first line = %q, want validation: BLOCKED", lines[0])
			}
			if !hasLinePrefix(out, "unit: BLOCKED") || !strings.Contains(out, c.reason) {
				t.Fatalf("the layer is not BLOCKED with %q in:\n%s", c.reason, out)
			}
		})
	}
}

// TestValidateExercisedNothingIsNotRun covers scenario 5: a command that
// prints the go-test "nothing ran" marker and exits 0 is NOT RUN, and the
// overall is NOT RUN, exit 5.
func TestValidateExercisedNothingIsNotRun(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `printf 'ok  example.com 0.1s [no tests to run]\\n'`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 5 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: NOT RUN" {
		t.Fatalf("first line = %q, want validation: NOT RUN", lines[0])
	}
	if !hasLinePrefix(out, "unit: NOT RUN") || !strings.Contains(out, "exercised nothing") {
		t.Fatalf("the layer is not NOT RUN: exercised nothing in:\n%s", out)
	}
}

// TestValidateSilentCommandThatPassesIsNotRun covers the brief's scenario
// for a tool that says nothing when it passes: a go vet that passes prints
// nothing, the evidence log carries only the header and the tail, and the
// run exercised nothing: NOT RUN, exit 5.
func TestValidateSilentCommandThatPassesIsPass(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	for name, content := range map[string]string{
		"go.mod": "module example\n\ngo 1.21\n",
		"a.go":   "package main\n\nfunc A() int { return 1 }\n",
	} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeValidationMD(t, cwd, "## Test layers\n"+
		"- unit: `go vet ./...`\n"+
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n")
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: PASS" {
		t.Fatalf("first line = %q, want validation: PASS", lines[0])
	}
	if !hasLinePrefix(out, "unit: PASS") || strings.Contains(out, "exercised nothing") {
		t.Fatalf("a silent pass is not PASS in:\n%s", out)
	}
}

// TestValidateManualAndNAOnlyIsNotRun covers scenario 6: manual layers are
// NOT RUN with the reason manual; with no command layer the overall is NOT
// RUN, exit 5.
func TestValidateManualAndNAOnlyIsNotRun(t *testing.T) {
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: manual: run by hand\n" +
		"- integration: n/a: r\n" +
		"- contract: manual: another hand\n" +
		"- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
	if code != 5 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: NOT RUN" {
		t.Fatalf("first line = %q, want validation: NOT RUN", lines[0])
	}
	if !hasLinePrefix(out, "unit: NOT RUN: manual") || !hasLinePrefix(out, "contract: NOT RUN: manual") {
		t.Fatalf("manual layers are not NOT RUN: manual in:\n%s", out)
	}
	if !hasLinePrefix(out, "integration: n/a") {
		t.Fatalf("n/a layers are not shown as n/a in:\n%s", out)
	}
}

// TestValidateProblemsBlockEverything covers scenario 7: with a placeholder,
// a malformed line or a missing layer, nothing runs — the valid command that
// would create the sentinel does not — the overall is BLOCKED, exit 5, and
// every problem appears with the doctor's text.
func TestValidateProblemsBlockEverything(t *testing.T) {
	contents := []struct {
		name    string
		content string
		problem string
	}{
		{"a placeholder blocks", "## Test layers\n- unit: `touch sentinel`\n- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: `<command>`\n", "still has the template placeholder"},
		{"a malformed line blocks", "## Test layers\n- unit: `touch sentinel`\n- integration: oops not a form\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n", "malformed layer line"},
		{"a missing layer blocks", "## Test layers\n- unit: `touch sentinel`\n- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n", "is not declared"},
	}
	for _, c := range contents {
		t.Run(c.name, func(t *testing.T) {
			env, cwd := commandFixture(t)
			writeValidationMD(t, cwd, c.content)
			code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
			if code != 5 || errOut != "" {
				t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
			}
			if lines := strings.Split(out, "\n"); lines[0] != "validation: BLOCKED" {
				t.Fatalf("first line = %q, want validation: BLOCKED", lines[0])
			}
			if !strings.Contains(out, c.problem) {
				t.Fatalf("the problem %q is not shown in:\n%s", c.problem, out)
			}
			if _, err := os.Stat(filepath.Join(cwd, "sentinel")); !os.IsNotExist(err) {
				t.Fatalf("a command of the file ran (sentinel exists, err=%v)", err)
			}
		})
	}
}

// TestValidatePlanRunsNothingAndWritesNothing covers scenario 8: --plan
// prints validation: plan with what each layer would do, runs nothing and
// creates no evidence directory.
func TestValidatePlanRunsNothingAndWritesNothing(t *testing.T) {
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateSentinelMD)
	code, out, errOut := runIn(t, []string{"validate", "--plan"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "validation: plan" {
		t.Fatalf("first line = %q, want validation: plan", lines[0])
	}
	if !hasLinePrefix(out, "unit: run touch sentinel") {
		t.Fatalf("the plan does not name the unit command in:\n%s", out)
	}
	if !hasLinePrefix(out, "integration: n/a") {
		t.Fatalf("the plan does not show the n/a layer in:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(cwd, "sentinel")); !os.IsNotExist(err) {
		t.Fatalf("--plan ran a command of the file (sentinel exists, err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(env["SOHO_DIR"], "validation")); !os.IsNotExist(err) {
		t.Fatalf("--plan created the evidence directory (err=%v)", err)
	}
}

// TestValidateLayerSelection covers scenario 9: --layer selects one layer
// (the others are NOT RUN: not selected), and a name that is not declared
// exits 2 before anything runs.
func TestValidateLayerSelection(t *testing.T) {
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `echo unit-pass`\n" +
		"- integration: n/a: r\n" +
		"- contract: `touch sentinel`\n" +
		"- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)

	t.Run("the unselected command layer is NOT RUN", func(t *testing.T) {
		requireSh(t)
		code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
		if code != 5 || errOut != "" {
			t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
		}
		if !hasLinePrefix(out, "unit: PASS (") || !hasLinePrefix(out, "contract: NOT RUN: not selected") {
			t.Fatalf("selection wrong in:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(cwd, "sentinel")); !os.IsNotExist(err) {
			t.Fatalf("the unselected layer ran (sentinel exists, err=%v)", err)
		}
	})

	t.Run("an undeclared name exits 2 before anything runs", func(t *testing.T) {
		// A fresh fixture: the other subtest already created an evidence
		// directory in the shared state root.
		env2, cwd2 := commandFixture(t)
		writeValidationMD(t, cwd2, content)
		code, out, errOut := runIn(t, []string{"validate", "--layer", "nada"}, env2, cwd2)
		if code != 2 || out != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if !strings.Contains(errOut, "layer 'nada' is not declared in VALIDATION.md") {
			t.Fatalf("stderr = %q", errOut)
		}
		if _, err := os.Stat(filepath.Join(cwd2, "sentinel")); !os.IsNotExist(err) {
			t.Fatalf("a command ran (sentinel exists, err=%v)", err)
		}
		if _, err := os.Stat(filepath.Join(env2["SOHO_DIR"], "validation")); !os.IsNotExist(err) {
			t.Fatalf("the evidence directory was created (err=%v)", err)
		}
	})
}

// readPidFile reads a pid file the layer command wrote (the `sh -c 'sleep
// 300 & echo $! > pid'` pattern) and returns the pid.
func readPidFile(t *testing.T, dir, name string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("the layer did not write the pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("pid file holds %q", data)
	}
	return pid
}

// requirePidGone polls, within a bounded settle window, until the process
// no longer exists: the kernel's teardown of a killed member (reaping by
// the new parent after re-parenting) can take milliseconds after the group
// kill is proven, and a member still alive at the end of the window fails
// the test.
func requirePidGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive after the layer ended", pid)
}

// TestValidateLayerEndsTheWholeProcessTree covers amendment scenario 1:
// `sh -c 'sleep 300 & echo $! > pid'` — the layer itself ends (sh exits 0),
// and the process of that PID is not alive any more: ending the layer ends
// the whole process tree, not only the direct sh.
func TestValidateLayerEndsTheWholeProcessTree(t *testing.T) {
	requireSh(t)
	requireTreeProbe(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `sleep 300 & echo $! > tree.pid`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if !hasLinePrefix(out, "unit: PASS") {
		t.Fatalf("the layer is not PASS in:\n%s", out)
	}
	requirePidGone(t, readPidFile(t, cwd, "tree.pid"))
}

// TestValidateClosesStdoutAndTheProcessStillDies covers the edge case: a
// command that closes stdout and keeps a child running is ended at the end
// of the layer — the child dies with the tree, the closed stdout does not
// change that.
func TestValidateClosesStdoutAndTheProcessStillDies(t *testing.T) {
	requireSh(t)
	requireTreeProbe(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `sleep 300 & echo $! > closed.pid; exec 1>&-`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if !hasLinePrefix(out, "unit: PASS") {
		t.Fatalf("the layer is not PASS in:\n%s", out)
	}
	requirePidGone(t, readPidFile(t, cwd, "closed.pid"))
}

// TestValidateTimeoutEndsTheWholeTree covers amendment scenario 2:
// --timeout 200 with a command that sleeps and would print `depois` after:
// the layer is FAIL, the sleep process is not alive any more (the
// cancellation ends the whole tree, not only the direct sh), and the text
// after the sleep never reached the log.
func TestValidateTimeoutEndsTheWholeTree(t *testing.T) {
	requireSh(t)
	requireTreeProbe(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `sleep 300 & echo $! > sleep.pid; wait; echo depois`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	start := time.Now()
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit", "--timeout", "200"}, env, cwd)
	elapsed := time.Since(start)
	if code != 1 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: FAIL" {
		t.Fatalf("first line = %q, want validation: FAIL", lines[0])
	}
	if !hasLinePrefix(out, "unit: FAIL (") || !strings.Contains(out, "timed out after 200ms") {
		t.Fatalf("the layer is not FAIL: timed out after 200ms in:\n%s", out)
	}
	if elapsed >= 15*time.Second {
		t.Fatalf("the run took %s: it should end a few seconds after the 200ms timeout", elapsed)
	}
	requirePidGone(t, readPidFile(t, cwd, "sleep.pid"))
	log, err := os.ReadFile(filepath.Join(validateEvidenceDir(t, out), "unit.log"))
	if err != nil {
		t.Fatalf("the evidence log is missing: %v", err)
	}
	if hasLinePrefix(string(log), "depois") {
		// The echo's output is a standalone line; the command header (which
		// names the command, `echo depois` included) is not a line that
		// starts with depois.
		t.Fatalf("the text after the killed sleep reached the log's output:\n%s", log)
	}
}

// TestValidateFiftyMibCommandStreamsToTheLog covers amendment scenario 3:
// a command with 50 MiB of output goes entirely to the evidence log while
// soho's own process output stays short.
func TestValidateFiftyMibCommandStreamsToTheLog(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	const wanted = 50 * 1024 * 1024
	content := "## Test layers\n" +
		"- unit: `yes | head -c " + strconv.Itoa(wanted) + "`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if !hasLinePrefix(out, "unit: PASS (") {
		t.Fatalf("the layer is not PASS in:\n%s", out)
	}
	if len(out) > 4096 || strings.Contains(out, "\ny\n") {
		t.Fatalf("soho's output is not short or leaked the command output (%d bytes):\n%s", len(out), out)
	}
	logPath := filepath.Join(validateEvidenceDir(t, out), "unit.log")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// The log carries the whole 50 MiB plus the small header and tail.
	if info.Size() < wanted || info.Size() > wanted+8192 {
		t.Fatalf("log size = %d, want about %d", info.Size(), wanted)
	}
}

// TestValidateAccentedLongCommandStaysWhole covers amendment scenario 4: a
// command with accents and more than 300 characters is shown whole —
// neither truncated nor accent-stripped — in --plan and in the log's
// command header.
func TestValidateAccentedLongCommandStaysWhole(t *testing.T) {
	requireSh(t)
	// The parser trims the backticked value, so the fixture ends without a
	// trailing space; the accents stay, and the length is far past the 200
	// units the old filter truncated at.
	command := "echo " + "açãéíõú " + strings.Repeat("long-command-word-para-testar ", 19) + "long-command-word-para-testar"
	if utf8.RuneCountInString(command) < 300 {
		t.Fatalf("the fixture command has %d runes, want at least 300", utf8.RuneCountInString(command))
	}
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `" + command + "`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--plan"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("plan code=%d err=%q out=%s", code, errOut, out)
	}
	if !strings.Contains(out, command) {
		t.Fatalf("the plan does not show the whole command (truncated or accent-stripped):\n%s", out)
	}
	code, out, errOut = runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("run code=%d err=%q out=%s", code, errOut, out)
	}
	log, err := os.ReadFile(filepath.Join(validateEvidenceDir(t, out), "unit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(log), "command: sh -c '"+command+"'\n") {
		t.Fatalf("the log's command header is not the whole command:\n%s", string(log)[:min(len(string(log)), 400)])
	}
}

// TestValidateCreatesNoFileInTheProjectRoot covers amendment scenario 7:
// after a run, the project root gained only the file the command itself
// created — the command and the platform never drop a capture or any other
// file at the root (the evidence lives under the state root).
func TestValidateCreatesNoFileInTheProjectRoot(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `echo made > from-command.out`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	before := dirNames(t, cwd)
	code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	inBefore := make(map[string]bool, len(before))
	for _, n := range before {
		inBefore[n] = true
	}
	var newEntries []string
	for _, n := range dirNames(t, cwd) {
		if !inBefore[n] {
			newEntries = append(newEntries, n)
		}
	}
	if len(newEntries) != 1 || newEntries[0] != "from-command.out" {
		t.Fatalf("the project root gained %v, want only [from-command.out]", newEntries)
	}
}

// TestValidateTimeoutFlagUsage covers decision 14: the timeout is a positive
// integer of milliseconds; anything else exits 2.
func TestValidateTimeoutFlagUsage(t *testing.T) {
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateSentinelMD)
	for _, value := range []string{"0", "-5", "abc", "90s"} {
		t.Run(value, func(t *testing.T) {
			code, out, errOut := runIn(t, []string{"validate", "--timeout", value}, env, cwd)
			if code != 2 || out != "" || !strings.Contains(errOut, "usage: soho validate") {
				t.Fatalf("--timeout %s: code=%d out=%q err=%q", value, code, out, errOut)
			}
		})
	}
}

// TestValidateEvidenceModesAndSummary covers scenario 11: the evidence
// directory is 0700 and the logs 0600 (skipped on Windows), the log carries
// the command, stdout and stderr, and summary.json is the --json object
// (the run-specific evidence and log paths aside).
func TestValidateEvidenceModesAndSummary(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `printf 'OUT-LINE-7\\n'; printf 'ERR-LINE-7\\n' 1>&2`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	dir := validateEvidenceDir(t, out)
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("the evidence directory mode = %v (%v), want 0700", info.Mode().Perm(), err)
		}
	}
	logPath := filepath.Join(dir, "unit.log")
	logInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("unit.log missing: %v", err)
	}
	if runtime.GOOS != "windows" && logInfo.Mode().Perm() != 0o600 {
		t.Fatalf("unit.log mode = %v, want 0600", logInfo.Mode().Perm())
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(log)
	// The merged log: the command header first, the merged output (stdout
	// and stderr in the order they were written), then the exit line and
	// the duration line last.
	if !strings.HasPrefix(logText, "command: sh -c '") {
		t.Fatalf("the log does not start with the command header:\n%s", logText)
	}
	if !strings.Contains(logText, "OUT-LINE-7") || !strings.Contains(logText, "ERR-LINE-7") {
		t.Fatalf("the merged output is missing from the log:\n%s", logText)
	}
	if exitIdx := strings.Index(logText, "exit: 0"); exitIdx < 0 || strings.Index(logText, "ERR-LINE-7") > exitIdx {
		t.Fatalf("the exit line must come after the merged output:\n%s", logText)
	}
	if !regexp.MustCompile(`\nduration: \d+ms\n$`).MatchString(logText) {
		t.Fatalf("the log must end with the duration line:\n%s", logText)
	}
	summaryPath := filepath.Join(dir, "summary.json")
	summaryInfo, err := os.Stat(summaryPath)
	if err != nil {
		t.Fatalf("summary.json missing: %v", err)
	}
	if runtime.GOOS != "windows" && summaryInfo.Mode().Perm() != 0o600 {
		t.Fatalf("summary.json mode = %v, want 0600", summaryInfo.Mode().Perm())
	}
	summaryRaw, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	code, outJSON, errOut := runIn(t, []string{"validate", "--layer", "unit", "--json"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("--json code=%d err=%q out=%s", code, errOut, outJSON)
	}
	var summary, jsonDoc map[string]any
	if err := json.Unmarshal(summaryRaw, &summary); err != nil {
		t.Fatalf("summary.json does not parse: %v", err)
	}
	if err := json.Unmarshal([]byte(outJSON), &jsonDoc); err != nil {
		t.Fatalf("--json does not parse: %v\n%s", err, outJSON)
	}
	for _, key := range []string{"overall", "commit", "clean", "problems"} {
		if fmt.Sprint(summary[key]) != fmt.Sprint(jsonDoc[key]) {
			t.Fatalf("summary %s = %v, --json %s = %v", key, summary[key], key, jsonDoc[key])
		}
	}
	sLayers, ok := summary["layers"].([]any)
	jLayers, ok2 := jsonDoc["layers"].([]any)
	if !ok || !ok2 || len(sLayers) != len(jLayers) {
		t.Fatalf("layers differ: summary=%v json=%v", sLayers, jLayers)
	}
	for i := range sLayers {
		s := sLayers[i].(map[string]any)
		j := jLayers[i].(map[string]any)
		// duration_ms is the only run-specific number: the summary is the
		// first run's and the --json output the second's, and the same
		// command takes a different number of milliseconds on each.
		for _, key := range []string{"name", "kind", "result", "reason", "exit"} {
			if fmt.Sprint(s[key]) != fmt.Sprint(j[key]) {
				t.Fatalf("layer %d %s = %v, want %v", i, key, s[key], j[key])
			}
		}
	}
	// The run-specific paths exist where they are named.
	if dir2, _ := jsonDoc["evidence"].(string); dir2 == "" || !isDir(dir2) {
		t.Fatalf("--json evidence = %q", jsonDoc["evidence"])
	}
	if log2, _ := sLayers[0].(map[string]any)["log"].(string); log2 == "" || !isFile(log2) {
		t.Fatalf("summary layer log = %q", sLayers[0])
	}
}

func isDir(p string) bool  { info, err := os.Stat(p); return err == nil && info.IsDir() }
func isFile(p string) bool { info, err := os.Stat(p); return err == nil && info.Mode().IsRegular() }

// TestValidateNowriteRefusal covers scenario 12: under SOHO_NOWRITE=1 the
// command is refused like every command outside the read list, --plan
// included, and nothing runs or is created.
func TestValidateNowriteRefusal(t *testing.T) {
	env, cwd := commandFixture(t)
	env["SOHO_NOWRITE"] = "1"
	writeValidationMD(t, cwd, validateSentinelMD)
	const messagePrefix = "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: "
	const messageTail = " — unset SOHO_NOWRITE to write\n"
	for _, c := range []struct {
		args    []string
		refused string
	}{
		{[]string{"validate"}, "validate"},
		{[]string{"validate", "--plan"}, "validate (1 extra argument not allowed)"},
		{[]string{"validate", "--layer", "unit"}, "validate (2 extra arguments not allowed)"},
	} {
		t.Run(strings.Join(c.args, "_"), func(t *testing.T) {
			code, out, errOut := runIn(t, c.args, env, cwd)
			if code != 2 || out != "" {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
			if want := messagePrefix + c.refused + messageTail; errOut != want {
				t.Fatalf("stderr:\n got:  %q\nwant: %q", errOut, want)
			}
			if _, err := os.Stat(filepath.Join(cwd, "sentinel")); !os.IsNotExist(err) {
				t.Fatalf("a command ran under NOWRITE (sentinel exists, err=%v)", err)
			}
			if _, err := os.Stat(filepath.Join(env["SOHO_DIR"], "validation")); !os.IsNotExist(err) {
				t.Fatalf("the evidence directory was created under NOWRITE (err=%v)", err)
			}
		})
	}
}

// TestValidateCommandsAreNeverRunByDoctorHookOrInit covers scenario 13:
// with a command-sentinel in the file, soho doctor, soho hook doctor and
// soho init never execute it.
func TestValidateCommandsAreNeverRunByDoctorHookOrInit(t *testing.T) {
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateSentinelMD)

	t.Run("soho doctor", func(t *testing.T) {
		code, out, errOut := runIn(t, []string{"doctor"}, env, cwd)
		if code != 0 {
			t.Fatalf("doctor code=%d err=%q out=%s", code, errOut, out)
		}
		assertNoSentinel(t, cwd)
	})

	t.Run("soho hook doctor", func(t *testing.T) {
		// Outside Herdr the hook returns early (its documented behavior);
		// inside Herdr it runs the doctor, which the soho doctor subtest
		// proves is read-only. Either way the file's command does not run.
		code, out, errOut := runIn(t, []string{"hook", "doctor"}, env, cwd)
		if code != 0 {
			t.Fatalf("hook doctor code=%d err=%q out=%s", code, errOut, out)
		}
		assertNoSentinel(t, cwd)
	})

	t.Run("soho init", func(t *testing.T) {
		bin := t.TempDir()
		herdr := filepath.Join(bin, "herdr")
		if err := os.WriteFile(herdr, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		env["HERDR_ENV"] = "1"
		env["PATH"] = bin + string(filepath.ListSeparator) + env["PATH"]
		code, out, errOut := runIn(t, []string{"init"}, env, cwd)
		if code != 0 && !strings.Contains(errOut, "soho:") {
			t.Fatalf("init code=%d err=%q out=%s", code, errOut, out)
		}
		assertNoSentinel(t, cwd)
	})
}

func assertNoSentinel(t *testing.T, cwd string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(cwd, "sentinel")); !os.IsNotExist(err) {
		t.Fatalf("a command of the file ran (sentinel exists, err=%v)", err)
	}
}

// TestValidateControlCharactersNeverReachTheOutput covers scenario 14 and
// amendment scenario 5: a control character (a NUL-like byte or an ANSI
// escape) in a command or in the manual text never reaches soho's output
// (the plan lines, the run lines, the --json output) nor the log's command
// header; the raw output section of the log keeps what the command printed.
func TestValidateControlCharactersNeverReachTheOutput(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `echo bad\x01char`\n" +
		"- integration: n/a: r\n" +
		"- contract: `printf 'ESC\x1b[31mred\\n'`\n" +
		"- e2e: manual: text\x02here\n" +
		"- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--plan"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("plan code=%d err=%q out=%s", code, errOut, out)
	}
	if strings.ContainsRune(out, '\x01') || strings.ContainsRune(out, '\x02') || strings.ContainsRune(out, '\x1b') {
		t.Fatalf("a control character reached the output:\n%s", out)
	}
	if !hasLinePrefix(out, "unit: run echo badchar") || !hasLinePrefix(out, "e2e: manual: texthere") {
		t.Fatalf("the sanitized plan lines are wrong in:\n%s", out)
	}
	if !hasLinePrefix(out, "contract: run printf 'ESC[31mred\\n'") {
		t.Fatalf("the escape was stripped from the plan line (only the control byte may go):\n%s", out)
	}
	code, out, errOut = runIn(t, []string{"validate", "--plan", "--json"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("plan --json code=%d err=%q out=%s", code, errOut, out)
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("an escape reached the --json plan output:\n%s", out)
	}
	if !strings.Contains(out, "ESC[31mred") {
		t.Fatalf("the command text without the control byte is not in the --json plan output:\n%s", out)
	}
	code, out, errOut = runIn(t, []string{"validate"}, env, cwd)
	// The e2e layer is manual: the overall is NOT RUN, exit 5 — the run
	// happened and its output is what is checked here.
	if code != 5 || errOut != "" {
		t.Fatalf("run code=%d err=%q out=%s", code, errOut, out)
	}
	if strings.ContainsRune(out, '\x01') || strings.ContainsRune(out, '\x02') || strings.ContainsRune(out, '\x1b') {
		t.Fatalf("a control character reached the run output:\n%s", out)
	}
	dir := validateEvidenceDir(t, out)
	unitLog, err := os.ReadFile(filepath.Join(dir, "unit.log"))
	if err != nil {
		t.Fatal(err)
	}
	unitHeader := strings.Split(string(unitLog), "\n")[0]
	if strings.ContainsRune(unitHeader, '\x01') || !strings.Contains(unitHeader, "echo badchar") {
		t.Fatalf("the unit log's command header is not sanitized: %q", unitHeader)
	}
	contractLog, err := os.ReadFile(filepath.Join(dir, "contract.log"))
	if err != nil {
		t.Fatal(err)
	}
	contractHeader := strings.Split(string(contractLog), "\n")[0]
	if strings.ContainsRune(contractHeader, '\x1b') || !strings.Contains(contractHeader, "ESC[31mred") {
		t.Fatalf("the contract log's command header is not sanitized: %q", contractHeader)
	}
}

// TestValidateParallelRunsGetSeparateEvidenceDirs covers scenario 15: two
// soho validate runs in the same second create two evidence directories.
func TestValidateParallelRunsGetSeparateEvidenceDirs(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateAllPassMD)
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	var out1, out2 bytes.Buffer
	res1 := make(chan int, 1)
	res2 := make(chan int, 1)
	go func() { res1 <- doValidate(nil, &ctx, env, cwd, &out1) }()
	go func() { res2 <- doValidate(nil, &ctx, env, cwd, &out2) }()
	code1 := <-res1
	code2 := <-res2
	if code1 != 0 || code2 != 0 {
		t.Fatalf("codes = %d %d (out1=%s out2=%s)", code1, code2, out1.String(), out2.String())
	}
	dir1 := validateEvidenceDir(t, out1.String())
	dir2 := validateEvidenceDir(t, out2.String())
	if dir1 == dir2 {
		t.Fatalf("both runs used the same evidence directory: %s", dir1)
	}
	if !isDir(dir1) || !isDir(dir2) {
		t.Fatalf("evidence directories missing: %s, %s", dir1, dir2)
	}
}

// TestValidateUsageLineIsRegistered covers decision 15: the command is in
// the usage, --help prints its lines, and unknown flags exit 2 with the
// usage.
func TestValidateUsageLineIsRegistered(t *testing.T) {
	env, cwd := commandFixture(t)
	const want = "  soho validate [--layer <name>]... [--timeout MS] [--plan] [--json]\n" +
		"                                             # run VALIDATION.md's test layers in file order (sh -c at the project root; 30m per layer by default, never stopping at a failure) and report one result per layer plus the worst overall; --plan runs nothing; evidence under <state>/validation/<stamp>/; exits 0 PASS, 1 FAIL, 5 BLOCKED or NOT RUN\n"
	code, out, errOut := runIn(t, []string{"validate", "--help"}, env, cwd)
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("validate --help: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"help"}, env, cwd)
	if code != 0 || !strings.Contains(out, want) {
		t.Fatalf("the full usage is missing the validate lines: code=%d err=%q", code, errOut)
	}
	code, out, errOut = runIn(t, []string{"validate", "--bogus"}, env, cwd)
	if code != 2 || out != "" || !strings.Contains(errOut, "usage: soho validate") {
		t.Fatalf("validate --bogus: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"validate", "extra"}, env, cwd)
	if code != 2 || out != "" || !strings.Contains(errOut, "usage: soho validate") {
		t.Fatalf("validate extra: code=%d out=%q err=%q", code, out, errOut)
	}
}

// TestValidatePipeAndChainCommandsCoveredByShC covers the edge case: a
// command with a pipe and a chain runs — it is `sh -c`, not a parsed argv.
func TestValidatePipeAndChainCommandsCoveredByShC(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `echo a | grep a && echo b | grep b`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if !hasLinePrefix(out, "unit: PASS (") {
		t.Fatalf("the piped command did not PASS in:\n%s", out)
	}
}

// TestValidateLoudCommandStaysInTheLog covers the edge case: a command that
// writes a lot goes to the log; soho's output shows only the result.
func TestValidateLoudCommandStaysInTheLog(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `seq 1 2000`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	// Standalone-line checks: the evidence stamp in the output carries the
	// run's wall clock and must not count as a leak of the command's output.
	if strings.Contains(out, "\n2000\n") {
		t.Fatalf("the loud output leaked into soho's output:\n%s", out)
	}
	log, err := os.ReadFile(filepath.Join(validateEvidenceDir(t, out), "unit.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "\n2000\n") {
		t.Fatalf("the loud output is not in the log")
	}
}

// TestValidateUnwritableStateDirIsBlocked covers the edge case: a state
// root without permission is BLOCKED with the reason, and nothing runs.
func TestValidateUnwritableStateDirIsBlocked(t *testing.T) {
	requireSh(t)
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission refusal is not meaningful on this host (Windows or root)")
	}
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateSentinelMD)
	if err := os.MkdirAll(env["SOHO_DIR"], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(env["SOHO_DIR"], 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(env["SOHO_DIR"], 0o755)
	code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
	if code != 5 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: BLOCKED" {
		t.Fatalf("first line = %q, want validation: BLOCKED", lines[0])
	}
	if !strings.Contains(out, "could not create the evidence directory") {
		t.Fatalf("the refusal reason is missing in:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(cwd, "sentinel")); !os.IsNotExist(err) {
		t.Fatalf("a command ran (sentinel exists, err=%v)", err)
	}
}

// TestValidateEvidenceDirRefusesALink covers the edge case: a link in place
// of the evidence directory is refused, never followed.
func TestValidateEvidenceDirRefusesALink(t *testing.T) {
	requireSh(t)
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateSentinelMD)
	target := filepath.Join(t.TempDir(), "evidence-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	validationPath := filepath.Join(env["SOHO_DIR"], "validation")
	if err := os.MkdirAll(filepath.Dir(validationPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, validationPath); err != nil {
		t.Skipf("symlink creation refused: %v", err)
	}
	code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
	if code != 5 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if !strings.Contains(out, "could not create the evidence directory") || !strings.Contains(out, "link") {
		t.Fatalf("the link refusal is missing in:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(target, "unit.log")); !os.IsNotExist(err) {
		t.Fatalf("the link was followed (unit.log in the target, err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "sentinel")); !os.IsNotExist(err) {
		t.Fatalf("a command ran (sentinel exists, err=%v)", err)
	}
}

// TestValidateRerunMakesANewEvidenceDir covers the edge case: running again
// creates a new evidence directory (never reusing the previous run's).
func TestValidateRerunMakesANewEvidenceDir(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateAllPassMD)
	_, out1, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if errOut != "" {
		t.Fatalf("first run err=%q out=%s", errOut, out1)
	}
	_, out2, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if errOut != "" {
		t.Fatalf("second run err=%q out=%s", errOut, out2)
	}
	dir1 := validateEvidenceDir(t, out1)
	dir2 := validateEvidenceDir(t, out2)
	if dir1 == dir2 {
		t.Fatalf("the rerun reused the evidence directory: %s", dir1)
	}
	if !isDir(dir1) || !isDir(dir2) {
		t.Fatalf("evidence directories missing: %s, %s", dir1, dir2)
	}
}

// TestValidateUnsuccessfulExitEndsTheTree covers the security amendment
// scenario 1 (F1): `sleep 300 & echo $! > pid; exit 3` — the shell exits
// nonzero with a live descendant; the layer is FAIL with the exit code, the
// run exits 1, and the descendant is not alive after the layer.
func TestValidateUnsuccessfulExitEndsTheTree(t *testing.T) {
	requireSh(t)
	requireTreeProbe(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `sleep 300 & echo $! > fail.pid; exit 3`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 1 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: FAIL" {
		t.Fatalf("first line = %q, want validation: FAIL", lines[0])
	}
	if !hasLinePrefix(out, "unit: FAIL (") || !strings.Contains(out, "exit 3") {
		t.Fatalf("the layer is not FAIL: exit 3 in:\n%s", out)
	}
	requirePidGone(t, readPidFile(t, cwd, "fail.pid"))
}

// TestValidateSignaledShellEndsTheTree covers the security amendment
// scenario 2 (F1): `sleep 300 & echo $! > pid; kill -9 $$` — the shell is
// killed by a signal with a live descendant; the layer is FAIL with the
// signal, the run exits 1, and the descendant is not alive after the layer.
func TestValidateSignaledShellEndsTheTree(t *testing.T) {
	requireSh(t)
	requireTreeProbe(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `sleep 300 & echo $! > sig.pid; kill -9 $$`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 1 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: FAIL" {
		t.Fatalf("first line = %q, want validation: FAIL", lines[0])
	}
	// The signal name is platform-specific ("killed" on Darwin, SIGKILL on
	// Linux): assert the FAIL form with the signal reason, not the name.
	if !hasLinePrefix(out, "unit: FAIL (") || !strings.Contains(out, ": signal ") {
		t.Fatalf("the layer is not FAIL with the signal in:\n%s", out)
	}
	requirePidGone(t, readPidFile(t, cwd, "sig.pid"))
}

// TestValidateSummaryJSONEqualsStdout covers the security amendment
// scenario 6 (F2): a --json run writes summary.json in its own evidence
// directory, byte-equal to what the run printed on stdout.
func TestValidateSummaryJSONEqualsStdout(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `echo sum-check`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	var outBuf bytes.Buffer
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	if code := doValidate([]string{"--layer", "unit", "--json"}, &ctx, env, cwd, &outBuf); code != 0 {
		t.Fatalf("code=%d out=%s", code, outBuf.String())
	}
	jsonOut := outBuf.String()
	var doc map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("--json does not parse: %v\n%s", err, jsonOut)
	}
	dir, _ := doc["evidence"].(string)
	if dir == "" {
		t.Fatalf("the --json evidence is missing:\n%s", jsonOut)
	}
	summaryRaw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatalf("summary.json is missing from the --json run's own evidence directory: %v", err)
	}
	if !bytes.Equal(summaryRaw, []byte(jsonOut)) {
		t.Fatalf("summary.json differs from the --json stdout:\nfile:\n%s\nstdout:\n%s", summaryRaw, jsonOut)
	}
}

// TestValidateSummaryWriteFailureBlocks covers the security amendment
// scenario 4 (F2): a deterministic summary.json write failure (the seam,
// not a full disk) makes the overall BLOCKED, the run exits 5, the problem
// is in the output, and --json stays valid with the problem.
func TestValidateSummaryWriteFailureBlocks(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `echo sum-fail`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	orig := validateSummaryWriter
	validateSummaryWriter = func(dir string, data []byte) error {
		return errors.New("injected summary failure")
	}
	defer func() { validateSummaryWriter = orig }()

	const problem = "could not write the evidence: injected summary failure"
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 5 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: BLOCKED" {
		t.Fatalf("first line = %q, want validation: BLOCKED", lines[0])
	}
	if !strings.Contains(out, problem) {
		t.Fatalf("the problem is missing in:\n%s", out)
	}
	if !hasLinePrefix(out, "unit: PASS") {
		t.Fatalf("the layer result must stay PASS in:\n%s", out)
	}

	// --json stays valid, with the problem in problems.
	code, out, errOut = runIn(t, []string{"validate", "--layer", "unit", "--json"}, env, cwd)
	if code != 5 || errOut != "" {
		t.Fatalf("--json code=%d err=%q out=%s", code, errOut, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json is not valid: %v\n%s", err, out)
	}
	if doc["overall"] != "BLOCKED" {
		t.Fatalf("--json overall = %v, want BLOCKED", doc["overall"])
	}
	problems, _ := doc["problems"].([]any)
	found := false
	for _, p := range problems {
		if strings.Contains(fmt.Sprint(p), problem) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the problem is missing from the --json problems: %v", problems)
	}
}

// TestValidateTailWriteFailureBlocks covers the security amendment
// scenario 5 (F2) and the edge case (one layer's log write fails, the
// others fine): the first layer's log tail fails deterministically (the
// seam), the second layer is fine, the overall is BLOCKED, the run exits
// 5, the problem is in the output, and both layers keep their results.
func TestValidateTailWriteFailureBlocks(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	content := "## Test layers\n" +
		"- unit: `echo tail-fail-a`\n" +
		"- contract: `echo tail-fail-b`\n" +
		"- integration: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	orig := validateLogTailWriter
	calls := 0
	validateLogTailWriter = func(f *os.File, exitLine string, durMs int64) error {
		calls++
		if calls == 1 {
			return errors.New("injected tail failure")
		}
		return orig(f, exitLine, durMs)
	}
	defer func() { validateLogTailWriter = orig }()

	const problem = "could not write the evidence: injected tail failure"
	code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
	if code != 5 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: BLOCKED" {
		t.Fatalf("first line = %q, want validation: BLOCKED", lines[0])
	}
	if !strings.Contains(out, problem) {
		t.Fatalf("the problem is missing in:\n%s", out)
	}
	if !hasLinePrefix(out, "unit: PASS") || !hasLinePrefix(out, "contract: PASS") {
		t.Fatalf("the layer results must stay PASS in:\n%s", out)
	}
}

// TestValidateLayerLogSwapDoesNotChangeTheResult covers the security
// amendment scenario 7 (Q2): the layer replaces its own evidence log with a
// link to another file carrying the [no tests to run] marker; the result
// does not change, because the exercised-nothing check reads the log over
// the descriptor the child wrote into, not by reopening the path.
func TestValidateLayerLogSwapDoesNotChangeTheResult(t *testing.T) {
	requireSh(t)
	if runtime.GOOS == "windows" {
		t.Skip("Windows: the log-swap fixture uses POSIX symlink semantics (skipped per the security amendment)")
	}
	env, cwd := commandFixture(t)
	fixed := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	origClock := validateStampClock
	validateStampClock = func() time.Time { return fixed }
	defer func() { validateStampClock = origClock }()
	stamp := fixed.UTC().Format("20060102T150405.000")
	decoy := filepath.Join(t.TempDir(), "decoy.log")
	if err := os.WriteFile(decoy, []byte("command: decoy\n[no tests to run]\nexit: 0\nduration: 1ms\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(env["SOHO_DIR"], "validation", stamp, "unit.log")
	content := "## Test layers\n" +
		"- unit: `ln -sfn " + decoy + " " + logPath + "`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	if lines := strings.Split(out, "\n"); lines[0] != "validation: PASS" {
		t.Fatalf("first line = %q, want validation: PASS", lines[0])
	}
	if !hasLinePrefix(out, "unit: PASS") {
		t.Fatalf("the layer is not PASS in:\n%s", out)
	}
	// The swap really happened: by path, the log now reads the decoy.
	raw, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(raw), "[no tests to run]") {
		t.Fatalf("the swap did not happen (the path still reads the original log, err=%v):\n%s", err, raw)
	}
}

// TestValidateTimeoutBoundRejectsOverflow covers the security amendment
// scenario 8 (F3): the largest representable whole-millisecond timeout
// (9223372036854 = MaxInt64 ns / 1e6) is accepted, and the next value —
// which would wrap the duration multiplication — is rejected with the
// usage.
func TestValidateTimeoutBoundRejectsOverflow(t *testing.T) {
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateSentinelMD)
	code, out, errOut := runIn(t, []string{"validate", "--plan", "--timeout", "9223372036854"}, env, cwd)
	if code != 0 || !strings.HasPrefix(out, "validation: plan") {
		t.Fatalf("the max timeout must be accepted: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runIn(t, []string{"validate", "--plan", "--timeout", "9223372036855"}, env, cwd)
	if code != 2 || out != "" || !strings.Contains(errOut, "usage: soho validate") {
		t.Fatalf("9223372036855 must be rejected with the usage: code=%d out=%q err=%q", code, out, errOut)
	}
}

// TestValidateCommandArgumentsSurviveExactly covers the security amendment
// scenario 9: a command with spaces, quotes and a backslash — the log's
// output section is exactly the command's own output, so the argv reached
// sh untouched. It runs on Windows too (where sh is Git Bash and the
// quoting is done by sh, so the output is the same).
func TestValidateCommandArgumentsSurviveExactly(t *testing.T) {
	requireSh(t)
	env, cwd := commandFixture(t)
	// printf '%s' does not interpret the argument's escapes (echo does,
	// on at least the bash-as-sh of macOS), so the exact output is
	// platform-stable: the argv reached sh untouched.
	command := `printf '%s\n' "sp ace 'q' \"q2\" \\zback"`
	content := "## Test layers\n" +
		"- unit: `" + command + "`\n" +
		"- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n"
	writeValidationMD(t, cwd, content)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	log, err := os.ReadFile(filepath.Join(validateEvidenceDir(t, out), "unit.log"))
	if err != nil {
		t.Fatal(err)
	}
	// The log is exactly: the header line, the command's output line, the
	// runner's offset-boundary blank line, the exit line and the duration
	// line.
	lines := strings.Split(strings.TrimRight(string(log), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("the log has %d lines, want 5 (header, output, blank, exit, duration):\n%s", len(lines), log)
	}
	if lines[1] != `sp ace 'q' "q2" \zback` {
		t.Fatalf("the log's output = %q, want the exact command output sp ace 'q' \"q2\" \\zback", lines[1])
	}
	if lines[2] != "" || !strings.HasPrefix(lines[3], "exit: 0") {
		t.Fatalf("the log's tail structure is wrong:\n%s", log)
	}
}

// TestValidateUnknownLayerDiagnosticIsPrintable covers the security
// amendment text item: a --layer name with a control character is refused
// with the usage exit 2, and the diagnostic shows the name without the
// control character (printable).
func TestValidateUnknownLayerDiagnosticIsPrintable(t *testing.T) {
	env, cwd := commandFixture(t)
	writeValidationMD(t, cwd, validateSentinelMD)
	code, out, errOut := runIn(t, []string{"validate", "--layer", "unit\x01x"}, env, cwd)
	if code != 2 || out != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "layer 'unitx' is not declared in VALIDATION.md") {
		t.Fatalf("the sanitized diagnostic is missing in:\n%q", errOut)
	}
	if strings.ContainsRune(errOut, '\x01') {
		t.Fatalf("a control character reached the diagnostic:\n%q", errOut)
	}
}

// TestValidateUnprovenCleanupIsNeverAPass: whatever the exit code says, a layer
// whose process tree may still be running fails.
func TestValidateUnprovenCleanupIsNeverAPass(t *testing.T) {
	requireSh(t)
	cases := []struct {
		name string
		res  eval.OwnedProcessResult
	}{
		{"exit 0", eval.OwnedProcessResult{Started: true, CleanupErr: errors.New("group stuck")}},
		{"exit 3", eval.OwnedProcessResult{Started: true, ExitCode: 3, CleanupErr: errors.New("group stuck")}},
		{"signal", eval.OwnedProcessResult{Started: true, Signaled: true, Signal: 9, CleanupErr: errors.New("group stuck")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, cwd := commandFixture(t)
			writeValidationMD(t, cwd, "## Test layers\n- unit: `echo hi`\n- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n")
			orig := validateRunOwned
			validateRunOwned = func(context.Context, string, []string, eval.OwnedProcessOptions) (eval.OwnedProcessResult, error) {
				return c.res, nil
			}
			defer func() { validateRunOwned = orig }()
			code, out, errOut := runIn(t, []string{"validate"}, env, cwd)
			if code != 1 || errOut != "" {
				t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
			}
			if !hasLinePrefix(out, "unit: FAIL") || !strings.Contains(out, "could not prove the process tree ended: group stuck") {
				t.Fatalf("out=%s", out)
			}
		})
	}
}

// TestValidateFinishClosesTheLogOnEveryPath: a failed tail write must not leave
// the evidence log open for the rest of the run.
func TestValidateFinishClosesTheLogOnEveryPath(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "unit.log"))
	if err != nil {
		t.Fatal(err)
	}
	orig := validateLogTailWriter
	validateLogTailWriter = func(*os.File, string, int64) error { return errors.New("injected tail failure") }
	defer func() { validateLogTailWriter = orig }()
	var outcome validateLayerResult
	finishValidateEvidence(f, "0", 1, true, &outcome)
	if err := f.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("the log was left open: second close = %v", err)
	}
}
