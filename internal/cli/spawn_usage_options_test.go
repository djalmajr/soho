package cli

// Drift guard: the spawn parser's own option lists (internal/spawn/spawn.go:
// the case lists of spawnValueOption and parseArgs) are read from the source,
// and every option they accept must be named, with its value shape, by the
// spawn usage (`soho spawn --help`) and by the spawn synopsis line of the
// skill's command block.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// spawnParserOptions returns the options the spawn parser accepts, split into
// value options (spawnValueOption) and options without a value (the other
// `--` case labels of parseArgs).
func spawnParserOptions(t *testing.T) (values, flags []string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	path := filepath.Join(filepath.Dir(file), "..", "spawn", "spawn.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	caseOptions := func(fn *ast.FuncDecl) []string {
		var out []string
		ast.Inspect(fn, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if s, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(s, "--") && len(s) > 2 {
					out = append(out, s)
				}
			}
			return true
		})
		return out
	}
	var valueFn, parseFn *ast.FuncDecl
	for _, decl := range parsed.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
			switch fn.Name.Name {
			case "spawnValueOption":
				valueFn = fn
			case "parseArgs":
				parseFn = fn
			}
		}
	}
	if valueFn == nil || parseFn == nil {
		t.Fatal("spawn.go no longer defines spawnValueOption and parseArgs: update the drift guard")
	}
	isValue := map[string]bool{}
	for _, o := range caseOptions(valueFn) {
		isValue[o] = true
		values = append(values, o)
	}
	seen := map[string]bool{}
	for _, o := range caseOptions(parseFn) {
		if !isValue[o] && !seen[o] {
			seen[o] = true
			flags = append(flags, o)
		}
	}
	sort.Strings(values)
	sort.Strings(flags)
	// A parser change that empties either list must not turn this guard green.
	if len(values) < 10 || len(flags) < 3 {
		t.Fatalf("read %d value options and %d flags from spawn.go (%v, %v): the guard no longer sees the parser's lists", len(values), len(flags), values, flags)
	}
	return values, flags
}

// requireNamedWithShape fails for every option the text does not name; a value
// option must also be followed by a value shape (not a bracket, a bar or
// another option).
func requireNamedWithShape(t *testing.T, label, text string, values, flags []string) {
	t.Helper()
	for _, option := range flags {
		if !regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(option) + `($|[^A-Za-z0-9-])`).MatchString(text) {
			t.Errorf("%s does not name the spawn option %s that the parser accepts", label, option)
		}
	}
	for _, option := range values {
		re := regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(option) + ` [A-Za-z<]`)
		if !re.MatchString(text) {
			t.Errorf("%s does not name the spawn option %s with a value shape (for example `%s VALUE`)", label, option, option)
		}
	}
}

func TestSpawnUsageNamesEveryParserOption(t *testing.T) {
	values, flags := spawnParserOptions(t)
	requireNamedWithShape(t, "the spawn usage (soho spawn --help)", commandHelp("spawn"), values, flags)
}

func TestSpawnSynopsisInSkillNamesEveryParserOption(t *testing.T) {
	values, flags := spawnParserOptions(t)
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "skills", "soho", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	var synopsis []string
	inFence := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence && strings.HasPrefix(line, "soho spawn <role>") {
			synopsis = append(synopsis, line)
		}
	}
	if len(synopsis) != 1 {
		t.Fatalf("want exactly one `soho spawn <role> …` synopsis line in a fenced block of skills/soho/SKILL.md, found %d", len(synopsis))
	}
	// The trailing comment explains options; the synopsis is what precedes it.
	command, _, _ := strings.Cut(synopsis[0], "   #")
	requireNamedWithShape(t, "the spawn synopsis line of skills/soho/SKILL.md", command, values, flags)
}
