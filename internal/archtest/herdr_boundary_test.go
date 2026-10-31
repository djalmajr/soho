package archtest

// The Herdr boundary: every Herdr interaction goes through the transport
// interface (internal/transport, backend internal/herdr). This test parses
// every non-test .go file of the module and fails when a production file
// outside the allowed places
//   1. runs the herdr executable — a call whose first argument is the
//      string literal "herdr" (platform.RunCli("herdr", ...),
//      platform.RunExecutable, exec.Command, or a local step helper such as
//      runStep("herdr", ...)); or
//   2. reads a HERDR_* variable — a call to a Get, Getenv or LookupEnv
//      method or function whose first argument is a string literal starting
//      with "HERDR_".
//
// Writes and deletes (env["HERDR_X"] = ..., delete(env, "HERDR_X")) are not
// reads and are allowed. The test does not depend on line numbers: findings
// are keyed by file and enclosing function (code outside any function is
// keyed to <package-level> and is never covered by a function-keyed entry),
// so unrelated edits do not break it. An entry whose file is no longer
// scanned (deleted or renamed) is stale and must be deleted.
//
// Allowed places, whole directories:
//   - internal/herdr: the backend itself; every Herdr execution and the
//     HERDR_* caller reads (env.Get in Caller, the RequireEnv-style checks)
//     live here.
//   - internal/plugin: the integration with Herdr's own plugin system
//     (HERDR_BIN_PATH, HERDR_PLUGIN_CONTEXT_JSON), which has no counterpart
//     in another backend.
//   - internal/codexenv: the zero-runner fallback used by herdr.RequireEnv,
//     plus the variable names it filters.
//
// Every other kept read or call is listed individually below, by file and
// enclosing function, with its reason.
import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var allowedDirs = map[string]string{
	"internal/herdr":    "the backend itself",
	"internal/plugin":   "Herdr's own plugin system (HERDR_BIN_PATH, HERDR_PLUGIN_CONTEXT_JSON)",
	"internal/codexenv": "the zero-runner fallback used by herdr.RequireEnv, plus the variable names it filters",
}

// allowedSite names one kept read or call outside the allowed directories,
// by file, enclosing function and rule, with the reason it is kept. Rules:
// "1" runs the herdr executable, "2" reads a HERDR_* variable, "1+2" both.
// A site allowed for one rule is not allowed for the other: a new direct
// herdr call inside a function whose HERDR_* read is allowed is still a
// violation.
type allowedSite struct {
	file     string
	function string
	rules    string
	reason   string
}

var allowedSites = []allowedSite{
	{
		file:     "internal/peer/assignment.go",
		function: "SendAssignmentMessage",
		rules:    "1+2",
		reason:   "the FindExecutable(\"herdr\") check looks up (never runs) the native binary for the Windows refusal; the HERDR_WORKSPACE_ID read feeds the child process environment: the derived id is written into the env clone the delivery runs with",
	},
	{
		file:     "internal/peer/sessions.go",
		function: "oneSnapshot",
		rules:    "1",
		reason:   "the opts.Run(\"herdr\", ...) seam: the Herdr plugin's loadPickerEntries (internal/plugin) injects pickerDiscoveryRun, which runs Herdr through the plugin's HERDR_BIN_PATH — the same exception class as internal/plugin — and the peer tests inject their own runner; the default path (Run nil) goes through the backend's Legacy.SnapshotRaw and makes no direct herdr run (TestPeerSessionsDefaultPathUsesTheBackend)",
	},
	{
		file:     "internal/peer/sessions.go",
		function: "listMachines",
		rules:    "1",
		reason:   "the opts.Run(\"herdr\", ...) seam: the Herdr plugin's loadBoardEntries (internal/plugin) injects pickerDiscoveryRun, which runs Herdr through the plugin's HERDR_BIN_PATH — the same exception class as internal/plugin — and the peer tests inject their own runner; the default path (Run nil) goes through the backend's Legacy.MachineListRaw and makes no direct herdr run (TestPeerSessionsDefaultPathUsesTheBackend)",
	},
	{
		file:     "internal/cli/env.go",
		function: "cmdEnv",
		rules:    "1",
		reason:   "the soho env command reports the installed herdr version with a one-shot external binary call, not a transport interaction",
	},
	{
		file:     "internal/doctor/doctor.go",
		function: "DoctorCheck",
		rules:    "1",
		reason:   "the FindExecutable(\"herdr\") lookup reports whether the binary is installed; it never runs it",
	},
	{
		file:     "internal/cli/explain.go",
		function: "explainStateDir",
		rules:    "1",
		reason:   "the FindExecutable(\"herdr\") lookup reports whether the binary is installed; it never runs it",
	},
	{
		file:     "internal/cli/explain.go",
		function: "explainActivity",
		rules:    "1",
		reason:   "the FindExecutable(\"herdr\") lookup reports whether the binary is installed; it never runs it",
	},
	{
		file:     "internal/cli/cli.go",
		function: "Run",
		rules:    "1+2",
		reason:   "the two FindExecutable(\"herdr\") lookups report the binary's presence in the decision env and never run it; the HERDR_WORKSPACE_ID read feeds the command environment: the derived id is written into decisionEnv for the collaborate command",
	},
	{
		file:     "internal/cli/dispatch.go",
		function: "cmdDispatchWithOptions",
		rules:    "2",
		reason:   "the HERDR_WORKSPACE_ID read feeds the command environment: the derived id is written into the env the participant checks run with",
	},
	{
		file:     "internal/cli/release.go",
		function: "collaborationReleaseRefusal",
		rules:    "2",
		reason:   "the HERDR_WORKSPACE_ID read feeds the command environment: the derived id is written into the env the participant checks run with",
	},
	{
		file:     "internal/wait/status.go",
		function: "DerivedWorkspaceEnv",
		rules:    "2",
		reason:   "the sanctioned child-environment exception: the HERDR_WORKSPACE_ID presence decides whether the id is derived and written into the clone the collaboration helpers take",
	},
}

type finding struct {
	rule     int // 1 = herdr executable call, 2 = HERDR_* env read
	file     string
	line     int
	function string
	what     string
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// isAllowedDir reports whether the file-relative path is under one of the
// allowed whole directories.
func isAllowedDir(rel string) bool {
	for dir := range allowedDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

func firstStringLit(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

func callName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func TestHerdrBoundary(t *testing.T) {
	root := moduleRoot(t)
	var (
		finders   []finding
		seenFiles = map[string]bool{}
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isAllowedDir(rel) {
			return nil
		}
		seenFiles[rel] = true
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		lines := strings.Split(string(src), "\n")
		// Findings are attributed to the enclosing top-level declaration: a
		// FuncDecl's body (closures included) is keyed to that function, and
		// every other declaration — package-level initializers among them —
		// is keyed to <package-level>, never to a neighboring function.
		inspect := func(decl ast.Node, function string) {
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				pos := fset.Position(call.Pos())
				lineText := ""
				if pos.Line >= 1 && pos.Line <= len(lines) {
					lineText = strings.TrimSpace(lines[pos.Line-1])
				}
				if value, ok := firstStringLit(call); ok && value == "herdr" {
					finders = append(finders, finding{rule: 1, file: rel, line: pos.Line, function: function, what: lineText})
				}
				name := callName(call)
				if name == "Get" || name == "Getenv" || name == "LookupEnv" {
					if value, ok := firstStringLit(call); ok && strings.HasPrefix(value, "HERDR_") {
						finders = append(finders, finding{rule: 2, file: rel, line: pos.Line, function: function, what: lineText})
					}
				}
				return true
			})
		}
		for _, decl := range file.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				inspect(fd, fd.Name.Name)
				continue
			}
			inspect(decl, "<package-level>")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	for _, site := range allowedSites {
		t.Logf("allow %s / %s (rule %s) — %s", site.file, site.function, site.rules, site.reason)
	}

	covers := func(site allowedSite, rule int) bool {
		tag := strconv.Itoa(rule)
		return site.rules == tag || site.rules == "1+2"
	}

	var failures []string
	matched := map[string]bool{}
	for _, f := range finders {
		ok := false
		for _, site := range allowedSites {
			if site.file == f.file && site.function == f.function && covers(site, f.rule) {
				ok = true
				matched[site.file+" :: "+site.function+" :: "+strconv.Itoa(f.rule)] = true
				break
			}
		}
		if !ok {
			failure := ""
			if f.rule == 1 {
				failure = "rule 1 (runs the herdr executable): "
			} else {
				failure = "rule 2 (reads a HERDR_* variable): "
			}
			failures = append(failures, fmt.Sprintf("%s%s:%d in %s: %s", failure, f.file, f.line, f.function, f.what))
			continue
		}
		t.Logf("allowed %s:%d in %s: %s", f.file, f.line, f.function, f.what)
	}
	// Every rule an allowlist entry covers must still hold a finding in a
	// scanned file: an entry whose site moved, disappeared, or whose file
	// was deleted or renamed is stale and gets deleted at the same time.
	for _, site := range allowedSites {
		for rule := 1; rule <= 2; rule++ {
			if !covers(site, rule) {
				continue
			}
			if !matched[site.file+" :: "+site.function+" :: "+strconv.Itoa(rule)] {
				if seenFiles[site.file] {
					failures = append(failures, fmt.Sprintf("stale allowlist entry: %s / %s (rule %d) holds no finding", site.file, site.function, rule))
				} else {
					failures = append(failures, fmt.Sprintf("stale allowlist entry: %s / %s (rule %d) — the file %s is no longer scanned (deleted or renamed); delete the entry", site.file, site.function, rule, site.file))
				}
			}
		}
	}
	if len(failures) > 0 {
		for _, failure := range failures {
			t.Error(failure)
		}
		t.Fatalf("%d Herdr boundary violation(s); move the call through the transport or the read through Runtime.Caller(), or add a reasoned allowlist entry", len(failures))
	}
	t.Logf("checked %d production files outside the allowed directories; %d finding(s) all in the allowlist", len(seenFiles), len(finders))
}
