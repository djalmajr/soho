package reviewctx

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/text"
)

const (
	changedFileCap = 300
	referenceCap   = 15
	docLineCap     = 10
	referenceRunes = 160
	docRunes       = 200
)

type changedFile struct {
	status string
	path   string
	old    string // rename source, empty otherwise
}

type declaration struct {
	key   string // per-file key, including an occurrence suffix for repeats
	path  string // head path of the declaring file
	name  string // display name, e.g. (Recv).Method or Type
	bare  string // bare identifier used by git grep
	text  string // physical source text of the declaration, without its doc comment
	start int    // physical line numbers
	end   int
}

// Pack builds the Markdown context pack for one committed range. The
// repository root and both revisions are resolved once; every later Git call
// runs at that root with full SHAs. No working tree or index content is read.
func Pack(runner GitRunner, cwd, base, head string) (string, error) {
	discovery := runner.At(cwd, "")
	gitDirOutput, err := discovery.Run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSuffix(gitDirOutput, "\n")
	if gitDir == "" {
		return "", fmt.Errorf("cannot resolve Git directory from %s", quote(cwd))
	}
	// Only commits are read, so a bare repository (no working tree) is
	// enough: its Git directory is the whole repository.
	git := runner.At("", gitDir)
	if bare, err := discovery.Run("rev-parse", "--is-bare-repository"); err != nil {
		return "", err
	} else if strings.TrimSpace(bare) != "true" {
		rootOutput, err := discovery.Run("rev-parse", "--show-toplevel")
		if err != nil {
			return "", err
		}
		root := strings.TrimSuffix(rootOutput, "\n")
		if root == "" {
			return "", fmt.Errorf("cannot resolve repository root from %s", quote(cwd))
		}
		// core.worktree can point at a different checkout. Keep the repository
		// discovered from cwd even when Git reports a different working root.
		git = runner.At(root, gitDir)
	}
	baseSHA, baseShort, err := resolve(git, base)
	if err != nil {
		return "", err
	}
	headSHA, headShort, err := resolve(git, head)
	if err != nil {
		return "", err
	}
	files, err := changedFiles(git, baseSHA, headSHA)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	fmt.Fprintf(&out, "# Review context %s..%s\n\n", baseShort, headShort)

	out.WriteString("## Changed files\n")
	if len(files) == 0 {
		out.WriteString("None.\n")
	} else {
		for i, f := range files {
			if i == changedFileCap {
				fmt.Fprintf(&out, "- … (+%d more)\n", len(files)-changedFileCap)
				break
			}
			if f.old != "" {
				fmt.Fprintf(&out, "- %s %s -> %s\n", f.status, quote(f.old), quote(f.path))
			} else {
				fmt.Fprintf(&out, "- %s %s\n", f.status, quote(f.path))
			}
		}
	}
	out.WriteByte('\n')

	symbols, declLines := changedDeclarations(git, baseSHA, headSHA, files)
	out.WriteString("## Changed Go symbols\n")
	if len(declLines) == 0 {
		out.WriteString("None.\n")
	} else {
		for _, line := range declLines {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	out.WriteByte('\n')

	out.WriteString("## Direct references\n")
	out.WriteString("Textual matches of the name in Go files at head; not resolved references.\n")
	if len(symbols) == 0 {
		out.WriteString("None.\n")
	} else {
		for _, symbol := range symbols {
			fmt.Fprintf(&out, "### %s\n", quote(symbol.name))
			for _, line := range referenceLines(git, headSHA, symbol) {
				out.WriteString(line)
				out.WriteByte('\n')
			}
		}
	}
	out.WriteByte('\n')

	out.WriteString("## Docs citing the changed files\n")
	docLines, err := documentLines(git, headSHA, files)
	if err != nil {
		return "", err
	}
	if len(docLines) == 0 {
		out.WriteString("None.\n")
	} else {
		for _, line := range docLines {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String(), nil
}

func resolve(git GitRunner, revision string) (full, short string, err error) {
	out, err := git.Run("rev-parse", "--verify", "--quiet", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", "", err
	}
	full = strings.TrimSpace(out)
	if full == "" || strings.ContainsAny(full, "\r\n") {
		return "", "", fmt.Errorf("cannot resolve revision %s", quote(revision))
	}
	shortOut, err := git.Run("rev-parse", "--short=7", "--verify", "--end-of-options", full+"^{commit}")
	if err != nil {
		return "", "", err
	}
	short = strings.TrimSpace(shortOut)
	if short == "" || strings.ContainsAny(short, "\r\n") {
		return "", "", fmt.Errorf("cannot shorten revision %s", quote(revision))
	}
	return full, short, nil
}

func changedFiles(git GitRunner, base, head string) ([]changedFile, error) {
	out, err := git.Run("diff", "--name-status", "-M", "-l1000", "-z", "--no-color", "--no-ext-diff", "--no-textconv",
		"--no-relative", "--ignore-submodules=none", "-O/dev/null", base, head, "--")
	if err != nil {
		return nil, err
	}
	records := strings.Split(out, "\x00")
	files := []changedFile{}
	for i := 0; i < len(records); {
		field := records[i]
		i++
		if field == "" {
			continue
		}
		statusByte := field[0]
		switch {
		case statusByte == 'R' || statusByte == 'C':
			if i+1 >= len(records) {
				return nil, fmt.Errorf("malformed name-status for %s", quote(field))
			}
			old, path := records[i], records[i+1]
			i += 2
			files = append(files, changedFile{status: string(statusByte), path: path, old: old})
		default:
			if i >= len(records) {
				return nil, fmt.Errorf("malformed name-status for %s", quote(field))
			}
			path := records[i]
			i++
			files = append(files, changedFile{status: string(statusByte), path: path})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].path != files[j].path {
			return files[i].path < files[j].path
		}
		return files[i].old < files[j].old
	})
	return files, nil
}

// changedDeclarations compares the two corresponding blobs of every changed
// Go file, declaration by declaration, keyed within that file pair. Only
// sides with a Go path contribute declarations. Removed declarations are
// reported against the head path (or the deleted file's only path).
func changedDeclarations(git GitRunner, base, head string, files []changedFile) ([]declaration, []string) {
	symbols := []declaration{}
	lines := []string{}
	for _, f := range files {
		basePath, headPath := f.path, f.path
		if f.old != "" {
			basePath = f.old
		}
		var headDecls []declaration
		if f.status != "D" && strings.HasSuffix(headPath, ".go") {
			var err error
			headDecls, err = readDeclarations(git, head, headPath)
			if err != nil {
				lines = append(lines, notParsed(headPath, "head", err))
				continue
			}
		}
		var baseDecls []declaration
		if f.status != "A" && f.status != "C" && strings.HasSuffix(basePath, ".go") {
			var err error
			baseDecls, err = readDeclarations(git, base, basePath)
			if err != nil {
				lines = append(lines, notParsed(basePath, "base", err))
				continue
			}
		}
		headByKey := map[string]declaration{}
		for _, d := range headDecls {
			headByKey[d.key] = d
			before, present := findKey(baseDecls, d.key)
			if present && before.text == d.text {
				continue
			}
			d.path = headPath
			symbols = append(symbols, d)
			lines = append(lines, fmt.Sprintf("- %s:%d-%d %s", quote(headPath), d.start, d.end, quote(d.name)))
		}
		for _, d := range baseDecls {
			if _, present := headByKey[d.key]; present {
				continue
			}
			lines = append(lines, fmt.Sprintf("- removed: %s %s", quote(headPath), quote(d.name)))
		}
	}
	return symbols, lines
}

func readDeclarations(git GitRunner, revision, path string) ([]declaration, error) {
	src, err := git.Run("cat-file", "blob", revision+":"+path)
	if err != nil {
		return nil, err
	}
	return parseDeclarations(path, src)
}

func findKey(decls []declaration, key string) (declaration, bool) {
	for _, d := range decls {
		if d.key == key {
			return d, true
		}
	}
	return declaration{}, false
}

func notParsed(path, side string, err error) string {
	return fmt.Sprintf("- %s: not parsed at %s (%s)", quote(path), side, quote(oneLine(err.Error())))
}

func oneLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// parseDeclarations reads top-level functions, methods and individual type
// specs. Positions and source slices are physical: //line directives never
// remap the line numbers Git reports.
func parseDeclarations(filename, src string) ([]declaration, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	physical := func(p token.Pos) token.Position { return fset.PositionFor(p, false) }
	out := []declaration{}
	counts := map[string]int{}
	add := func(name, bare string, pos, end token.Pos) {
		counts[name]++
		key := name
		if counts[name] > 1 {
			key = fmt.Sprintf("%s#%d", name, counts[name])
		}
		start, finish := physical(pos), physical(end)
		text := ""
		if start.Offset >= 0 && finish.Offset >= start.Offset && finish.Offset <= len(src) {
			text = src[start.Offset:finish.Offset]
		}
		out = append(out, declaration{
			key: key, name: name, bare: bare, text: text,
			start: start.Line, end: finish.Line,
		})
	}
	for _, node := range file.Decls {
		switch d := node.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = "(" + receiverName(d.Recv.List[0].Type) + ")." + name
			}
			add(name, d.Name.Name, d.Pos(), d.End())
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				add(ts.Name.Name, ts.Name.Name, ts.Pos(), ts.End())
			}
		}
	}
	return out, nil
}

func receiverName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			var buf bytes.Buffer
			if err := printer.Fprint(&buf, token.NewFileSet(), expr); err != nil {
				return "?"
			}
			return buf.String()
		}
	}
}

// referenceLines runs git grep at head with NUL-delimited fields: the first
// field is "<sha>:<path>", the second the line number and the third the
// matched line. A malformed record is skipped and counted nowhere. Hits in
// the symbol's own head extent are excluded; the remaining hits keep Git's
// order, with non-test files before test files.
func referenceLines(git GitRunner, head string, symbol declaration) []string {
	out, err := git.Run("-c", "color.grep=never", "-c", "grep.column=false", "-c", "grep.lineNumber=false", "-c", "grep.patternType=fixed",
		"-c", "core.attributesFile=", "-c", "attr.tree="+head,
		"grep", "--no-color", "--no-column", "-z", "-n", "-w", "--text", "-F", "--no-textconv", "--no-recurse-submodules",
		"-e", symbol.bare, head, "--", "*.go")
	if err != nil {
		var commandErr *CommandError
		if errors.As(err, &commandErr) && commandErr.Status == 1 {
			return nil
		}
		return []string{fmt.Sprintf("- grep failed: %s", quote(oneLine(err.Error())))}
	}
	type hit struct {
		path string
		line int
		text string
	}
	production, tests := []hit{}, []hit{}
	prefix := head + ":"
	for offset := 0; offset < len(out); {
		meta, rest, ok := strings.Cut(out[offset:], "\x00")
		if !ok {
			break
		}
		number, rest, ok := strings.Cut(rest, "\x00")
		if !ok {
			break
		}
		excerpt, rest, _ := strings.Cut(rest, "\n")
		offset = len(out) - len(rest)
		if !strings.HasPrefix(meta, prefix) {
			continue
		}
		line, err := strconv.Atoi(number)
		if err != nil || line < 1 {
			continue
		}
		h := hit{path: strings.TrimPrefix(meta, prefix), line: line, text: excerpt}
		if h.path == symbol.path && line >= symbol.start && line <= symbol.end {
			continue
		}
		if strings.HasSuffix(h.path, "_test.go") {
			tests = append(tests, h)
		} else {
			production = append(production, h)
		}
	}
	ordered := append(production, tests...)
	lines := []string{}
	for i, h := range ordered {
		if i == referenceCap {
			lines = append(lines, fmt.Sprintf("- … (+%d more)", len(ordered)-referenceCap))
			break
		}
		lines = append(lines, fmt.Sprintf("- %s:%d %s", quote(h.path), h.line, quote(cutRunes(strings.TrimSpace(h.text), referenceRunes))))
	}
	return lines
}

func documentLines(git GitRunner, head string, files []changedFile) ([]string, error) {
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.path] = true
		if f.old != "" {
			paths[f.old] = true
		}
	}
	changed := make([]string, 0, len(paths))
	for path := range paths {
		changed = append(changed, path)
	}
	sort.Strings(changed)
	if len(changed) == 0 {
		return nil, nil
	}
	out, err := git.Run("ls-tree", "-r", "--full-tree", "--name-only", "-z", head, "--", "skills/soho/SKILL.md", "skills/soho/references", "docs/formal")
	if err != nil {
		return nil, err
	}
	docs := []string{}
	for _, doc := range strings.Split(out, "\x00") {
		if isDocumentPath(doc) {
			docs = append(docs, doc)
		}
	}
	sort.Strings(docs)
	hits := map[string][]string{}
	for _, path := range changed {
		hits[path] = []string{}
	}
	for _, doc := range docs {
		content, err := git.Run("cat-file", "blob", head+":"+doc)
		if err != nil {
			return nil, err
		}
		for n, line := range strings.Split(content, "\n") {
			line = strings.TrimSuffix(line, "\r")
			for _, path := range changed {
				if strings.Contains(line, path) {
					hits[path] = append(hits[path], fmt.Sprintf("- %s:%d %s: %s", quote(doc), n+1, quote(path), quote(cutRunes(line, docRunes))))
				}
			}
		}
	}
	lines := []string{}
	for _, path := range changed {
		found := hits[path]
		for i, line := range found {
			if i == docLineCap {
				lines = append(lines, fmt.Sprintf("- … (+%d more)", len(found)-docLineCap))
				break
			}
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func isDocumentPath(doc string) bool {
	if doc == "skills/soho/SKILL.md" {
		return true
	}
	if strings.HasPrefix(doc, "skills/soho/references/") {
		rest := strings.TrimPrefix(doc, "skills/soho/references/")
		return strings.HasSuffix(rest, ".md") && !strings.Contains(rest, "/")
	}
	if strings.HasPrefix(doc, "docs/formal/") {
		rest := strings.TrimPrefix(doc, "docs/formal/")
		return strings.HasSuffix(rest, ".md")
	}
	return false
}

func quote(s string) string { return text.QuoteControls(s) }

func cutRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit])
}
