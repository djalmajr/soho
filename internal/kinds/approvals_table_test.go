package kinds

// The per-assistant approvals table of skills/soho/references/kinds.md is
// rendered here from KindApprovalArgs, the mapping spawn uses, and compared to
// the file between its two markers: a mapping change that leaves the table
// behind fails this test.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const (
	approvalsTableStart = "<!-- approvals-table:start (rendered from KindApprovalArgs by internal/kinds/approvals_table_test.go) -->"
	approvalsTableEnd   = "<!-- approvals-table:end -->"
)

var (
	approvalsTableKinds = []string{"claude", "codex", "grok", "agy", "gemini", "cursor", "pi", "opencode"}
	approvalsTableModes = []string{"ask", "edits", "auto", "full", "unsandboxed"}
	runsAsRE            = regexp.MustCompile(`runs as (\w+)`)
)

// approvalsCell renders one mapping result: the exact native flags, or
// "warning, runs as <mode>" / "warning, no flags" when the mapping warns.
func approvalsCell(kind, mode string) string {
	var warnings []string
	args, err := KindApprovalArgs(kind, mode, func(msg string) { warnings = append(warnings, msg) })
	if err != nil {
		return "refused: " + err.Error()
	}
	flags := "no flags"
	if len(args) > 0 {
		flags = "`" + strings.Join(args, " ") + "`"
	}
	if len(warnings) == 0 {
		return flags
	}
	lead := "warning, no flags"
	if m := runsAsRE.FindStringSubmatch(strings.Join(warnings, " ")); m != nil {
		lead = "warning, runs as " + m[1]
	}
	if len(args) == 0 {
		return lead
	}
	return lead + ": " + flags
}

func renderApprovalsTable() string {
	var b strings.Builder
	b.WriteString("| `approvals` |")
	for _, kind := range approvalsTableKinds {
		b.WriteString(" `" + kind + "` |")
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(approvalsTableKinds)) + "\n")
	for _, mode := range approvalsTableModes {
		b.WriteString("| `" + mode + "` |")
		for _, kind := range approvalsTableKinds {
			b.WriteString(" " + approvalsCell(kind, mode) + " |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func readKindsDoc(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "skills", "soho", "references", "kinds.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestKindsDocApprovalsTableMatchesTheMapping(t *testing.T) {
	doc := readKindsDoc(t)
	start := strings.Index(doc, approvalsTableStart)
	end := strings.Index(doc, approvalsTableEnd)
	if start < 0 || end < start {
		t.Fatalf("skills/soho/references/kinds.md must hold the approvals table between %q and %q", approvalsTableStart, approvalsTableEnd)
	}
	got := strings.TrimSpace(doc[start+len(approvalsTableStart) : end])
	want := strings.TrimSpace(renderApprovalsTable())
	if got != want {
		t.Fatalf("the approvals table of kinds.md differs from the mapping.\nfile:\n%s\n\nrendered from KindApprovalArgs:\n%s", got, want)
	}
}

// The line people were told wrongly: Codex `full` keeps the sandbox.
func TestKindsDocStatesCodexFullKeepsTheSandbox(t *testing.T) {
	const line = "For Codex, `full` keeps the `workspace-write` sandbox (`.git` read-only) and only `unsandboxed` uses `danger-full-access`."
	if !strings.Contains(readKindsDoc(t), line) {
		t.Fatalf("skills/soho/references/kinds.md must state: %s", line)
	}
	if cell := approvalsCell("codex", "full"); !strings.Contains(cell, "-s workspace-write") || strings.Contains(cell, "danger-full-access") {
		t.Fatalf("codex full maps to %s: the documented line no longer holds", cell)
	}
	if cell := approvalsCell("codex", "unsandboxed"); !strings.Contains(cell, "-s danger-full-access") {
		t.Fatalf("codex unsandboxed maps to %s: the documented line no longer holds", cell)
	}
	for _, mode := range []string{"ask", "edits", "auto", "full"} {
		if cell := approvalsCell("codex", mode); strings.Contains(cell, "danger-full-access") {
			t.Fatalf("codex %s maps to %s: only unsandboxed may use danger-full-access", mode, cell)
		}
	}
}
