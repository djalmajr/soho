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

var citationPattern = regexp.MustCompile("(internal/[A-Za-z0-9_/-]+\\.go):([0-9]+(?:-[0-9]+)?(?:,[0-9]+(?:-[0-9]+)?)*)`?")
var firstSymbol = regexp.MustCompile("^[ \\t]+`([A-Za-z_][A-Za-z0-9_]*|\\(\\*[A-Za-z_][A-Za-z0-9_]*\\)\\.[A-Za-z_][A-Za-z0-9_]*|[A-Za-z_][A-Za-z0-9_]*\\.[A-Za-z_][A-Za-z0-9_]*)`")
var nextSymbol = regexp.MustCompile("^,[ \\t]+`([A-Za-z_][A-Za-z0-9_]*|\\(\\*[A-Za-z_][A-Za-z0-9_]*\\)\\.[A-Za-z_][A-Za-z0-9_]*|[A-Za-z_][A-Za-z0-9_]*\\.[A-Za-z_][A-Za-z0-9_]*)`")

type citationSpan struct{ start, end int }

func TestDocumentationCodeCitations(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, doc := range []string{"skills/soho/references/feature-map.md", "docs/formal/README.md"} {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(doc)))
		if err != nil {
			t.Errorf("%s: %v", doc, err)
			continue
		}
		text := string(content)
		for _, at := range citationPattern.FindAllStringSubmatchIndex(text, -1) {
			path, ranges := text[at[2]:at[3]], text[at[4]:at[5]]
			tail := text[at[1]:]
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
				t.Errorf("%s: %s:%s: %v", doc, path, ranges, err)
				continue
			}
			lines := strings.Count(string(source), "\n")
			if len(source) > 0 && source[len(source)-1] != '\n' {
				lines++
			}
			var spans []citationSpan
			for _, part := range strings.Split(ranges, ",") {
				ends := strings.Split(part, "-")
				start, _ := strconv.Atoi(ends[0])
				end := start
				if len(ends) == 2 {
					end, _ = strconv.Atoi(ends[1])
				}
				spans = append(spans, citationSpan{start, end})
				if start < 1 || end < start || end > lines {
					t.Errorf("%s: %s:%s: range outside file (lines: %d)", doc, path, part, lines)
				}
			}
			if len(symbols) == 0 {
				if doc == "docs/formal/README.md" {
					t.Errorf("%s: %s:%s: missing symbol", doc, path, ranges)
				}
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, source, 0)
			if err != nil {
				t.Errorf("%s: %s: %v", doc, path, err)
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
					t.Errorf("%s: %s:%s: symbol %s not found", doc, path, ranges, symbol)
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
					t.Errorf("%s: %s:%d-%d: outside symbol extent %s (declarations %s)", doc, path, span.start, span.end, strings.Join(symbols, ", "), fmt.Sprint(extents))
				}
			}
		}
	}
}
