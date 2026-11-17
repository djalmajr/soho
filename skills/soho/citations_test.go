package skill

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var citationPattern = regexp.MustCompile("(internal/[A-Za-z0-9_/-]+\\.go):([0-9][A-Za-z0-9_-]*(?:,[ \t]*[A-Za-z0-9_-]*)*)`?")
var rangePattern = regexp.MustCompile(`^[0-9]+(?:-[0-9]+)?(?:,[ \t]*[0-9]+(?:-[0-9]+)?)*$`)
var unquotedRangePrefix = regexp.MustCompile(`^[0-9]+(?:-[0-9]+)?(?:,[ \t]*[0-9]+(?:-[0-9]+)?)*`)

// isRangeWordByte reports a byte that cannot follow a line number directly
// in an unquoted citation (a letter, an underscore or a dangling dash).
func isRangeWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_' || b == '-'
}

const symbolPattern = `([A-Za-z_][A-Za-z0-9_]*|\(\*[A-Za-z_][A-Za-z0-9_]*\)\.[A-Za-z_][A-Za-z0-9_]*|[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*)`

var firstSymbol = regexp.MustCompile("^(?:[ \t]+\\([ \t]*|[ \t]*:[ \t]*|[ \t]+)\x60" + symbolPattern + "\x60")
var nextSymbol = regexp.MustCompile("^,[ \t]+\x60" + symbolPattern + "\x60")

type citationSpan struct{ start, end int }

func TestDocumentationCodeCitations(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, doc := range []string{"skills/soho/references/feature-map.md", "docs/formal/README.md"} {
		checkCitations(root, doc, t.Errorf)
	}
}

func checkCitations(root, doc string, report func(string, ...any)) {
	{
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(doc)))
		if err != nil {
			report("%s: %v", doc, err)
			return
		}
		text := string(content)
		// The scan resumes where each citation's line list really ends, not
		// where the broad pattern stopped: an unquoted list cut short at a
		// comma leaves the next citation to be found on its own.
		for pos := 0; pos < len(text); {
			rel := citationPattern.FindStringSubmatchIndex(text[pos:])
			if rel == nil {
				break
			}
			at := make([]int, len(rel))
			for i, v := range rel {
				at[i] = v
				if v >= 0 {
					at[i] = v + pos
				}
			}
			pos = at[1]
			path, ranges := text[at[2]:at[3]], text[at[4]:at[5]]
			tailStart := at[1]
			quoted := false
			if at[0] > 0 && text[at[0]-1] == '`' {
				if end := strings.IndexByte(text[at[4]:], '`'); end >= 0 {
					ranges = text[at[4] : at[4]+end]
					tailStart = at[4] + end + 1
					quoted = true
					pos = tailStart
				}
			}
			if !quoted {
				// Unquoted, the line list ends where the numbers end: a comma
				// followed by prose or by another citation is punctuation, not
				// part of the list. A letter glued to the number is malformed.
				rest := text[at[4]:]
				n := len(unquotedRangePrefix.FindString(rest))
				if n < len(rest) && isRangeWordByte(rest[n]) {
					end := n
					for end < len(rest) && (isRangeWordByte(rest[end]) || rest[end] >= '0' && rest[end] <= '9') {
						end++
					}
					report("%s: %s:%s: malformed range", doc, path, rest[:end])
					pos = at[4] + end
					continue
				}
				ranges, tailStart = rest[:n], at[4]+n
				pos = tailStart
			}
			if !rangePattern.MatchString(ranges) {
				report("%s: %s:%s: malformed range", doc, path, ranges)
				continue
			}
			tail := text[tailStart:]
			var symbols []string
			if m := firstSymbol.FindStringSubmatchIndex(tail); m != nil {
				symbols = append(symbols, tail[m[2]:m[3]])
				tail = tail[m[1]:]
				for {
					m = nextSymbol.FindStringSubmatchIndex(tail)
					if m == nil {
						break
					}
					symbols = append(symbols, tail[m[2]:m[3]])
					tail = tail[m[1]:]
				}
			}
			source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil {
				report("%s: %s:%s: %v", doc, path, ranges, err)
				continue
			}
			lines := strings.Count(string(source), "\n")
			if len(source) > 0 && source[len(source)-1] != '\n' {
				lines++
			}
			var spans []citationSpan
			for _, part := range strings.Split(ranges, ",") {
				part = strings.TrimSpace(part)
				ends := strings.Split(part, "-")
				start, _ := strconv.Atoi(ends[0])
				end := start
				if len(ends) == 2 {
					end, _ = strconv.Atoi(ends[1])
				}
				spans = append(spans, citationSpan{start, end})
				if start < 1 || end < start || end > lines {
					report("%s: %s:%s: range outside file (lines: %d)", doc, path, part, lines)
				}
			}
			if len(symbols) == 0 {
				if doc == "docs/formal/README.md" {
					report("%s: %s:%s: missing symbol", doc, path, ranges)
				}
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, source, 0)
			if err != nil {
				report("%s: %s: %v", doc, path, err)
				continue
			}
			var extents []citationSpan
			for _, symbol := range symbols {
				name, receiver := symbol, ""
				if i := strings.LastIndex(symbol, "."); i >= 0 {
					name = symbol[i+1:]
					receiver = strings.Trim(symbol[:i], "()*")
				}
				var found []citationSpan
				add := func(id *ast.Ident, node ast.Node) {
					if id != nil && id.Name == name {
						found = append(found, citationSpan{fset.Position(node.Pos()).Line, fset.Position(node.End()).Line})
					}
				}
				ast.Inspect(file, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.FuncDecl:
						if receiver != "" {
							if x.Recv == nil || len(x.Recv.List) != 1 {
								break
							}
							typ := x.Recv.List[0].Type
							if star, ok := typ.(*ast.StarExpr); ok {
								typ = star.X
							}
							switch indexed := typ.(type) {
							case *ast.IndexExpr:
								typ = indexed.X
							case *ast.IndexListExpr:
								typ = indexed.X
							}
							id, ok := typ.(*ast.Ident)
							if !ok || id.Name != receiver {
								break
							}
						}
						add(x.Name, x)
					case *ast.TypeSpec:
						if receiver == "" {
							add(x.Name, x)
						}
					case *ast.ValueSpec:
						if receiver == "" {
							for _, id := range x.Names {
								add(id, x)
							}
						}
					case *ast.AssignStmt:
						if receiver == "" && x.Tok == token.DEFINE {
							for _, v := range x.Lhs {
								if id, ok := v.(*ast.Ident); ok {
									add(id, x)
								}
							}
						}
					}
					return true
				})
				if len(found) == 0 {
					report("%s: %s:%s: symbol %s not found", doc, path, ranges, symbol)
				}
				extents = append(extents, found...)
			}
			for _, span := range spans {
				matched := false
				for _, extent := range extents {
					if (span.start >= extent.start && span.end <= extent.end) || (extent.start >= span.start && extent.start <= span.end) {
						matched = true
						break
					}
				}
				if !matched {
					report("%s: %s:%d-%d: outside symbol extent %s (declarations %s)", doc, path, span.start, span.end, strings.Join(symbols, ", "), fmt.Sprint(extents))
				}
			}
		}
	}
}

func TestCitationFixtures(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0755); err != nil {
		t.Fatal(err)
	}
	source := "package p\ntype Box[T any] struct{}\nfunc (b *Box[T]) Foo() {}\ntype Pair[K, V any] struct{}\nfunc (p Pair[K, V]) Bar() {}\nfunc Other() {}\n"
	if err := os.WriteFile(filepath.Join(root, "internal", "probe.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, cite, want string }{
		{"parenthesized", "`internal/probe.go:3` (`(*Box).Foo` note)", ""},
		{"parenthesized wrong", "`internal/probe.go:3` (`Other`)", "outside symbol extent"},
		{"colon", "`internal/probe.go:3`: `(*Box).Foo`", ""},
		{"colon wrong", "`internal/probe.go:3`: `Other`", "outside symbol extent"},
		{"space range", "`internal/probe.go:3, 999` `(*Box).Foo`", "range outside file"},
		{"malformed", "`internal/probe.go:3, 4x` `(*Box).Foo`", "malformed range"},
		{"malformed punctuation", "`internal/probe.go:3.4` `(*Box).Foo`", "malformed range"},
		{"generic pointer", "`internal/probe.go:3` `Box.Foo`", ""},
		{"generic pair", "`internal/probe.go:5` `Pair.Bar`", ""},
		{"unquoted then prose", "See internal/probe.go:3, where the method is declared.", ""},
		{"unquoted pair", "See internal/probe.go:3, internal/probe.go:6.", ""},
		{"unquoted pair second out of file", "See internal/probe.go:3, internal/probe.go:999.", "range outside file"},
		{"unquoted pair second missing", "See internal/probe.go:3, internal/missing_probe.go:6.", "missing_probe.go"},
		{"unquoted list", "<!-- internal/probe.go:3, 5 -->", ""},
		{"unquoted list out of file", "<!-- internal/probe.go:3, 999 -->", "range outside file"},
		{"unquoted glued letter", "<!-- internal/probe.go:3x -->", "malformed range"},
		{"unquoted symbol checked", "<!-- internal/probe.go:3 (`Other`) -->", "outside symbol extent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "fixture.md"
			if err := os.WriteFile(filepath.Join(root, doc), []byte(tc.cite), 0600); err != nil {
				t.Fatal(err)
			}
			var errors []string
			checkCitations(root, doc, func(format string, args ...any) { errors = append(errors, fmt.Sprintf(format, args...)) })
			got := strings.Join(errors, "\n")
			if (tc.want == "" && got != "") || (tc.want != "" && !strings.Contains(got, tc.want)) {
				t.Fatalf("want %q; got %s", tc.want, got)
			}
		})
	}
}
