package wait

// Test-layers coverage of settled reports.
//
// checkTestLayers publishes the report's ## Test layers section (the counts
// and the layers that did not pass) and the validator's second-line result
// on the done line, and warns — once per report, never changing the wait
// result — when a layer did not pass, a section line has no result, the
// validator's result is not PASS, or a role that reports layers has no
// section while its project has a VALIDATION.md. The wait never parses or
// runs the file, and never blocks on any of it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/reportscan"
)

// checkTestLayers is the done case's test-layers and validation coverage:
// the section's counts go to the line when the section exists, the
// validator's line goes to the line when it is recognized, and the warnings
// fire at most once per report, through the test-layers-warned wait file.
func checkTestLayers(sd, agent, report, text string, line *jsonjs.Object, role string) {
	if text == "" {
		return // an unreadable report stays exactly as today
	}
	layers := reportscan.TestLayers(text)
	if layers.Found {
		line.Set("test_layers", jsonjs.O("pass", layers.Pass, "fail", layers.Fail, "not_run", layers.NotRun, "blocked", layers.Blocked, "na", layers.NA, "unmarked", layers.Unmarked))
	}
	validation, hasValidation := reportscan.ValidationLine(text)
	if hasValidation {
		line.Set("validation", validation)
	}
	prev, _ := readWaitFile(sd, agent, "test-layers-warned")
	if prev == report {
		return
	}
	warned := false
	warn := func(format string, args ...any) {
		sendWarning(fmt.Sprintf(format, args...))
		warned = true
	}
	if layers.Found && len(layers.NonPass) > 0 {
		names := make([]string, len(layers.NonPass))
		for i, entry := range layers.NonPass {
			names[i] = entry.Name + " " + entry.Result
		}
		warn("report of '%s' lists %d test layer(s) that did not pass (%s): read them before commit, push or release", agent, len(layers.NonPass), strings.Join(names, ", "))
	}
	if layers.Found && layers.Unmarked > 0 {
		warn("report of '%s' has %d Test layers line(s) without a result", agent, layers.Unmarked)
	}
	if hasValidation && validation != "PASS" {
		warn("validation of '%s' is %s: read it before commit, push or release", agent, validation)
	}
	if !layers.Found && missingSectionWarns(role, testLayersRoot(sd, report)) {
		warn("report of '%s' has no Test layers section, and the project has a VALIDATION.md", agent)
	}
	if warned {
		// The warning already went out; a marker that cannot persist only
		// lets it repeat on the next poll, never aborting the settlement.
		_ = os.WriteFile(filepath.Join(sd, "wait", agent+".test-layers-warned"), []byte(report+"\n"), 0o666)
	}
}

// missingSectionWarns is the missing-section condition: a role that reports
// layers (an unreadable role is no role), and a VALIDATION.md present in the
// project root.
func missingSectionWarns(role, root string) bool {
	switch role {
	case "implementer", "critical-implementer", "tester":
	default:
		return false
	}
	return validationFilePresent(root)
}

// testLayersRoot is the worker's recorded project root: the sidecar's
// git_tree, written by the dispatch from the composed brief's Worktree:
// declaration or the worker's cwd; "" when there is no sidecar or no
// field, and then no warning (including a project outside Git).
func testLayersRoot(sd, report string) string { return gittree.GitTree(sd, report) }

// validationFilePresent reports whether root/VALIDATION.md is itself a
// regular file: a link, a directory or any other kind is absent. The wait
// opens it through the platform boundary and reads nothing from it.
func validationFilePresent(root string) bool {
	if root == "" {
		return false
	}
	path := filepath.Join(root, "VALIDATION.md")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	f, err := platform.OpenRegularFile(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
