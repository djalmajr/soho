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
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/core"
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
	declared := declaredTestLayersForReport(sd, report)
	missingSection := !layers.Found && missingSectionWarns(role, testLayersRoot(sd, report))
	warnings := TestLayerWarnings(agent, layers, validation, hasValidation, missingSection)
	if len(declared) > 0 {
		warnings = TestLayerWarningsWithDeclared(agent, layers, declared, validation, hasValidation, missingSection)
	}
	for _, warning := range warnings {
		sendWarning(warning.Message)
	}
	if len(warnings) > 0 {
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

// unmarkedExampleMax is the excerpt bound both the unmarked-line example and
// the non-pass layer name keep: at most 120 runes, the 120th becoming an
// ellipsis.
const unmarkedExampleMax = 120

// unmarkedExample quotes the first unmarked line for the warning: at most 120
// runes (the 120th becomes an ellipsis), Go-quoted so a control character in
// report text never reaches the terminal raw.
func unmarkedExample(line string) string {
	runes := []rune(line)
	if len(runes) > unmarkedExampleMax {
		runes = append(runes[:unmarkedExampleMax-1], '…')
	}
	return fmt.Sprintf("%q", string(runes))
}

// DeclaredTestLayers reads the first `Test layers: unit, mutation` line
// outside a fenced block of a brief, with the same fence and first-line
// rules BriefType uses for `Type:`. A missing line, an empty list or a line
// inside a fenced example declares nothing; names keep the brief's spelling
// and order, and empty comma items are dropped.
func DeclaredTestLayers(body string) []string {
	fence := ""
	for _, raw := range strings.Split(body, "\n") {
		if fence != "" {
			if close := metricsFenceClose.FindStringSubmatch(raw); close != nil && close[1][0] == fence[0] && len(close[1]) >= len(fence) {
				fence = ""
			}
			continue
		}
		if open := metricsFenceOpen.FindStringSubmatch(raw); open != nil {
			fence = open[1]
			continue
		}
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "Test layers:") {
			continue
		}
		var names []string
		for _, part := range strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "Test layers:")), ",") {
			if name := strings.TrimSpace(part); name != "" {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

// declaredTestLayersForReport reads the composed brief of a settled task
// through the sidecar the owned-files audit uses: the sidecar's stem, first
// its .brief.md then its .md. A task without a sidecar falls back to the
// brief beside the report, as the metrics brief read does.
func declaredTestLayersForReport(sd, report string) []string {
	stems := []string{}
	if sidecar := gittree.Sidecar(sd, report); sidecar != "" {
		stems = append(stems, strings.TrimSuffix(sidecar, ".dispatch.json"))
	}
	base := strings.TrimSuffix(filepath.Base(report), ".md")
	stems = append(stems, filepath.Join(filepath.Dir(report), base))
	for _, stem := range stems {
		for _, candidate := range []string{stem + ".brief.md", stem + ".md"} {
			data, err := core.ReadScratchReport(candidate)
			if err != nil {
				continue
			}
			return DeclaredTestLayers(string(data))
		}
	}
	return nil
}

// declaredLayerName compares a report layer with the brief's declared names
// through layerName, so an ordinary name is compared byte for byte and a
// hostile name is compared in the same quoted, bounded form the warning uses.
func declaredLayerName(declared []string, name string) bool {
	want := layerName(name)
	for _, item := range declared {
		if layerName(item) == want {
			return true
		}
	}
	return false
}

// layerName renders one report-supplied layer name for the non-pass warning:
// an ordinary name prints byte for byte, while a name carrying a control
// character or exceeding the excerpt bound is quoted and cut exactly as
// unmarkedExample quotes a line, so report text never reaches stderr raw.
func layerName(name string) string {
	if utf8.RuneCountInString(name) <= unmarkedExampleMax && strconv.Quote(name) == `"`+name+`"` {
		return name
	}
	return unmarkedExample(name)
}
