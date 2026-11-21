package reviewctx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

type fixture struct {
	repo string
	env  platform.Env
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return newFixtureAt(t, t.TempDir(), "repo")
}

func newFixtureAt(t *testing.T, root, name string) fixture {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME":                root,
		"USERPROFILE":         root,
		"TMPDIR":              root,
		"XDG_CONFIG_HOME":     filepath.Join(root, "cfg"),
		"PATH":                testutil.HostPath(),
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_AUTHOR_NAME":     "reviewctx fixture",
		"GIT_AUTHOR_EMAIL":    "fixture@example.invalid",
		"GIT_COMMITTER_NAME":  "reviewctx fixture",
		"GIT_COMMITTER_EMAIL": "fixture@example.invalid",
	}
	f := fixture{repo: repo, env: env}
	f.git(t, "init", "-q")
	f.git(t, "config", "--local", "maintenance.auto", "false")
	f.git(t, "config", "--local", "gc.auto", "0")
	return f
}

func (f fixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = f.repo, f.env.List()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (f fixture) write(t *testing.T, path, body string) {
	t.Helper()
	full := filepath.Join(f.repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) commit(t *testing.T, message string) {
	t.Helper()
	f.git(t, "add", "-A")
	f.git(t, "commit", "-qm", message)
}

func (f fixture) pack(t *testing.T, cwd string) string {
	t.Helper()
	out, err := Pack(NewRunner(f.env, cwd), cwd, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const richEdits = `package pkg

func Interior() int {
	println("remove")
	return 1
}

func Edge() int {
	return 1
}

type (
	A int
	B string
)

type Receiver[T, U any] struct{}

func (*Receiver[T, U]) Method() int {
	return 1
}
`

const richEditsHead = `package pkg

func Interior() int {
	return 1
}

func Edge() int {
	println("first added line")
	return 1
}

type (
	A int
	B bool
)

type Receiver[T, U any] struct{}

func (*Receiver[T, U]) Method() int {
	return 2
}
`

func newRichFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	f.write(t, "pkg/edits.go", richEdits)
	f.write(t, "pkg/line.go", "package pkg\n//line generated.go:301\nfunc Remap() int {\n\treturn 1\n}\n")
	f.write(t, "pkg/refs.go", "package pkg\nfunc Consult() int {\n\treturn 1\n}\nfunc Other() { Consult() } // same-file outside extent\n")
	f.write(t, "pkg/a_test.go", "package pkg\nvar _ = Consult() // test\n")
	f.write(t, "pkg/z_prod.go", "package pkg\nvar _ = Consult() // control\nvar _ = Consult() // note: prose\nvar _ = Consult() // issue:42:end\n")
	f.write(t, "pkg/gone.go", "package pkg\nfunc DeletedFile() {}\n")
	f.write(t, "pkg/partial.go", "package pkg\nfunc DeletedHere() {}\nfunc Unchanged() {}\n")
	f.write(t, "pkg/rename.go", "package pkg\nfunc Remains() {}\nfunc GoneOnRename() {}\nfunc AlsoRemains() {}\nfunc ThirdRemains() {}\n")
	f.write(t, "pkg/pure.go", "package pkg\nfunc UnchangedRename() {}\n")
	f.write(t, "pkg/rename_change.go", "package pkg\nfunc K1() {}\nfunc K2() {}\nfunc K3() {}\nfunc ChangedOnRename() int { return 1 }\nfunc K4() {}\n")
	f.write(t, "pkg/file[1].go", "package pkg\nfunc Bracket() int { return 1 }\n")
	f.write(t, "pkg/marked.go", "package pkg\nfunc Marked() int { return 1 }\n")
	f.write(t, "pkg/gap.go", "package pkg\n\n// a line between declarations\n\nfunc GapA() {}\n\nfunc GapB() {}\n")
	f.write(t, "other/code.go", "package other\nfunc OtherPkg() int { return 1 }\n")
	f.write(t, "other/refs.go", "package other\nvar _ = Consult() // other package text\n")
	f.write(t, "docs/formal/sub/guide.md", "   pkg/pure.go and pkg/pure-new.go\n\tpkg/gone.go\n")
	f.write(t, "docs/formal/top.md", "pkg/marked.go top-level formal\n")
	f.write(t, "skills/soho/references/direct.md", "pkg/marked.go direct reference doc\n")
	f.write(t, "skills/soho/references/deep/excluded.md", "pkg/marked.go nested reference doc excluded\n")
	f.write(t, "docs/formal/notes.txt", "pkg/marked.go not a document\n")
	f.commit(t, "base")

	f.write(t, "pkg/edits.go", richEditsHead)
	f.write(t, "pkg/line.go", "package pkg\n//line generated.go:301\nfunc Remap() int {\n\treturn 2\n}\n")
	f.write(t, "pkg/refs.go", "package pkg\nfunc Consult() int {\n\treturn 2\n}\nfunc Other() { Consult() } // same-file outside extent\n")
	f.git(t, "mv", "pkg/pure.go", "pkg/pure-new.go")
	f.git(t, "mv", "pkg/rename.go", "pkg/rename-new.go")
	f.write(t, "pkg/rename-new.go", "package pkg\nfunc Remains() {}\nfunc AlsoRemains() {}\nfunc ThirdRemains() {}\n")
	f.git(t, "mv", "pkg/rename_change.go", "pkg/rename_change_new.go")
	f.write(t, "pkg/rename_change_new.go", "package pkg\nfunc K1() {}\nfunc K2() {}\nfunc K3() {}\nfunc ChangedOnRename() int { return 2 }\nfunc K4() {}\n")
	f.git(t, "rm", "-q", "pkg/gone.go")
	f.write(t, "pkg/partial.go", "package pkg\nfunc Unchanged() {}\n")
	f.write(t, "pkg/file[1].go", "package pkg\nfunc Bracket() int { return 2 }\n")
	f.write(t, "pkg/marked.go", "package pkg\nfunc Marked() int { return 2 }\n")
	f.write(t, "pkg/gap.go", "package pkg\n\nfunc GapA() {}\n\nfunc GapB() {}\n")
	f.write(t, "other/code.go", "package other\nfunc OtherPkg() int { return 2 }\n")
	f.write(t, "docs/formal/sub/guide.md", "   pkg/pure.go and pkg/pure-new.go\n\tpkg/gone.go\n   "+strings.Repeat("z", 210)+" pkg/marked.go\n")
	f.commit(t, "head")
	return f
}

func TestPackComparisonFixture(t *testing.T) {
	f := newRichFixture(t)
	got := f.pack(t, f.repo)
	for _, want := range []string{
		"## Changed files\n", "## Changed Go symbols\n", "## Direct references\n", "## Docs citing the changed files\n",
		"- R pkg/pure.go -> pkg/pure-new.go\n", "- R pkg/rename.go -> pkg/rename-new.go\n", "- R pkg/rename_change.go -> pkg/rename_change_new.go\n",
		"- pkg/edits.go:", " Interior\n", " Edge\n", " B\n", " (Receiver).Method\n",
		"- pkg/line.go:3-5 Remap\n", "- pkg/rename_change_new.go:5-5 ChangedOnRename\n",
		"- removed: pkg/gone.go DeletedFile\n", "- removed: pkg/partial.go DeletedHere\n",
		"- removed: pkg/rename-new.go GoneOnRename\n",
		"### Consult\n", "- pkg/z_prod.go:3 var _ = Consult() // note: prose\n",
		"- pkg/z_prod.go:4 var _ = Consult() // issue:42:end\n",
		"- pkg/refs.go:5 func Other() { Consult() } // same-file outside extent\n",
		"- pkg/a_test.go:2 var _ = Consult() // test\n",
		"- docs/formal/sub/guide.md:1 pkg/pure.go:    pkg/pure.go and pkg/pure-new.go\n",
		"- docs/formal/sub/guide.md:2 pkg/gone.go: \\tpkg/gone.go\n",
		"- docs/formal/top.md:1 pkg/marked.go: pkg/marked.go top-level formal\n",
		"- skills/soho/references/direct.md:1 pkg/marked.go: pkg/marked.go direct reference doc\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pack missing %q;\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"UnchangedRename", "pkg/rename-new.go:2-2 Remains", "pkg/rename-new.go:", " A\n",
		"GapA", "GapB", "- D pkg/rename_change.go", "- A pkg/rename_change_new.go", " K1\n",
		"skills/soho/references/deep/excluded.md", "docs/formal/notes.txt",
		"generated.go:303", "pkg/edits.go:4 func Interior",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected %q in pack:\n%s", unwanted, got)
		}
	}
	if old := strings.Index(got, "- pkg/z_prod.go:3 "); old < 0 || strings.Index(got, "- pkg/a_test.go:2 ") < old {
		t.Error("a _test.go hit must follow the production hit even when Git lists it first")
	}
	if strings.Contains(got, "\x01") || strings.Contains(got, "\nref.go") {
		t.Error("a control character reached the pack raw")
	}
}

func TestPackWorktreeAndRevisionShapedEntryAreIgnored(t *testing.T) {
	f := newRichFixture(t)
	baseline := f.pack(t, f.repo)
	f.write(t, "pkg/marked.go", "package pkg\nfunc StageOnly() {}\n")
	f.git(t, "add", "--", "pkg/marked.go")
	f.write(t, "pkg/marked.go", "package pkg\nfunc WorkingOnly() {}\n")
	f.write(t, ".gitattributes", "pkg/marked.go -diff\n")
	before := f.git(t, "status", "--porcelain")
	if after := f.pack(t, f.repo); after != baseline {
		t.Errorf("worktree edits or attributes changed the pack:\n%s", after)
	}
	if after := f.git(t, "status", "--porcelain"); after != before {
		t.Errorf("the command changed checkout status: before=%q after=%q", before, after)
	}
	short := strings.Fields(strings.SplitN(baseline, "\n", 2)[0])[3]
	short = strings.Split(short, "..")[0]
	f.write(t, short, "")
	if after := f.pack(t, f.repo); after != baseline {
		t.Errorf("a revision-shaped checkout entry broke the pack:\n%s", after)
	}
}

func TestPackNestedCwdResolvesRoot(t *testing.T) {
	f := newRichFixture(t)
	rootPack := f.pack(t, f.repo)
	nestedPack := f.pack(t, filepath.Join(f.repo, "pkg"))
	if nestedPack != rootPack {
		t.Errorf("nested cwd changed the pack:\n--- root\n%s\n--- nested\n%s", rootPack, nestedPack)
	}
}

func TestPackNeighborFixture(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a/a.go", "package a\nfunc init() { print(1) }\n")
	f.write(t, "a/two.go", "package a\nfunc init() { print(1) }\nfunc init() { print(2) }\n")
	f.write(t, "b/b.go", "package b\nfunc init() { print(1) }\nfunc RemovedControl() {}\nfunc Remains() {}\n")
	f.write(t, "a/dead.go", "package a\nfunc WholeFile() {}\n")
	f.write(t, "a/move.go", "package a\nfunc Retained() int { return 1 }\nfunc PaddedA() {}\nfunc PaddedB() {}\nfunc PaddedC() {}\n")
	f.write(t, "b/file1.go", "package b\nfunc OnlyNeighbor() {}\nfunc Keep() {}\nfunc Bracket() int { return 1 }\n")
	f.write(t, "b/file[1].go", "package b\nfunc Pad() {}\nfunc FalsePositive() {}\nfunc BracketLiteral() int { return 1 }\n")
	f.commit(t, "base")

	f.write(t, "a/a.go", "package a\nfunc init() { print(2) }\n")
	f.write(t, "a/two.go", "package a\nfunc init() { print(1) }\n")
	f.write(t, "b/b.go", "package b\nfunc Remains() {}\n")
	f.git(t, "rm", "-q", "a/dead.go")
	f.git(t, "mv", "a/move.go", "a/moved.go")
	f.write(t, "a/moved.go", "package a\nfunc Retained() int { return 2 }\nfunc PaddedA() {}\nfunc PaddedB() {}\nfunc PaddedC() {}\n")
	f.write(t, "b/file1.go", "package b\nfunc OnlyNeighbor() {}\nfunc Keep() {}\nfunc Bracket() int { return 2 }\n")
	f.write(t, "b/file[1].go", "package b\nfunc NewPad() {}\nfunc FalsePositive() {}\nfunc BracketLiteral() int { return 1 }\n")
	f.commit(t, "head")

	got := f.pack(t, f.repo)
	for _, want := range []string{
		"- a/a.go:2-2 init\n", "- a/moved.go:2-2 Retained\n",
		"- b/file1.go:4-4 Bracket\n", "- b/file[1].go:2-2 NewPad\n",
		"- removed: a/dead.go WholeFile\n", "- removed: b/b.go init\n",
		"- removed: b/b.go RemovedControl\n", "- removed: b/file[1].go Pad\n", "- removed: a/two.go init\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"PaddedA", "PaddedB", "PaddedC", "BracketLiteral", "OnlyNeighbor", "func Keep"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unchanged neighbor %q was listed:\n%s", unwanted, got)
		}
	}
	if strings.Count(got, "- removed: a/two.go init\n") != 1 {
		t.Errorf("the second init in one file must be keyed and reported once:\n%s", got)
	}
	if strings.Count(got, "- removed: b/b.go init\n") != 1 {
		t.Errorf("removed init from b/b.go must be keyed separately from a/a.go:\n%s", got)
	}
}

func TestPackEmptySectionsAndBadRevision(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "one\n")
	f.commit(t, "base")
	f.write(t, "a.txt", "two\n")
	f.commit(t, "head")
	got := f.pack(t, f.repo)
	for _, section := range []string{"## Changed Go symbols\nNone.\n", "## Direct references\nTextual matches of the name in Go files at head; not resolved references.\nNone.\n", "## Docs citing the changed files\nNone.\n"} {
		if !strings.Contains(got, section) {
			t.Errorf("missing %q:\n%s", section, got)
		}
	}
	if _, err := Pack(NewRunner(f.env, f.repo), f.repo, "no-such-revision", "HEAD"); err == nil {
		t.Error("a bad revision was accepted")
	}
}

// fakeRunner answers from a fixed table, so raw Git order and the caps can
// be checked without depending on a fixture's path order.
type fakeRunner struct {
	replies map[string]string
}

func fakeKey(args ...string) string { return strings.Join(args, "\x1f") }

func (f fakeRunner) At(string, string) GitRunner { return f }

func (f fakeRunner) Run(args ...string) (string, error) {
	out, ok := f.replies[fakeKey(args...)]
	if !ok {
		return "", fmt.Errorf("unexpected git %s", strings.Join(args, " "))
	}
	return out, nil
}

const (
	fakeBase = "1111111111111111111111111111111111111111"
	fakeHead = "2222222222222222222222222222222222222222"
)

func fakeDiffKey() string {
	return fakeKey("diff", "--name-status", "-M", "-l1000", "-z", "--no-color", "--no-ext-diff", "--no-textconv",
		"--no-relative", "--ignore-submodules=none", "-O/dev/null", fakeBase, fakeHead, "--")
}

func fakeReplies(grep, docs string) map[string]string {
	return map[string]string{
		fakeKey("rev-parse", "--show-toplevel"):                                                 "/repo\n",
		fakeKey("rev-parse", "--is-bare-repository"):                                            "false\n",
		fakeKey("rev-parse", "--absolute-git-dir"):                                              "/repo/.git\n",
		fakeKey("rev-parse", "--verify", "--quiet", "--end-of-options", "base^{commit}"):        fakeBase + "\n",
		fakeKey("rev-parse", "--verify", "--quiet", "--end-of-options", "head^{commit}"):        fakeHead + "\n",
		fakeKey("rev-parse", "--short=7", "--verify", "--end-of-options", fakeBase+"^{commit}"): "aaaaaaa\n",
		fakeKey("rev-parse", "--short=7", "--verify", "--end-of-options", fakeHead+"^{commit}"): "bbbbbbb\n",
		fakeDiffKey(): "M\x00main.go\x00",
		fakeKey("cat-file", "blob", fakeBase+":main.go"): "package main\n\nfunc Consult() int { return 1 }\n",
		fakeKey("cat-file", "blob", fakeHead+":main.go"): "package main\n\nfunc Consult() int { return 2 }\n",
		fakeKey("-c", "color.grep=never", "-c", "grep.column=false", "-c", "grep.lineNumber=false", "-c", "grep.patternType=fixed",
			"-c", "core.attributesFile=", "-c", "attr.tree="+fakeHead,
			"grep", "--no-color", "--no-column", "-z", "-n", "-w", "--text", "-F", "--no-textconv", "--no-recurse-submodules",
			"-e", "Consult", fakeHead, "--", "*.go"): grep,
		fakeKey("ls-tree", "-r", "--full-tree", "--name-only", "-z", fakeHead, "--", "skills/soho/SKILL.md", "skills/soho/references", "docs/formal"): docs,
	}
}

func grepRecord(path, line, text string) string {
	return fakeHead + ":" + path + "\x00" + line + "\x00" + text + "\n"
}

func TestPackFakeRunnerOrdersTestHitsLast(t *testing.T) {
	grep := grepRecord("z_test.go", "2", "var _ = Consult() // test first in Git order") +
		grepRecord("dir/a:b.go", "4", "var _ = Consult() // issue:42:end") +
		grepRecord("dir/new\nline.go", "7", "var _ = Consult() // newline path")
	got, err := Pack(fakeRunner{replies: fakeReplies(grep, "")}, "/repo", "base", "head")
	if err != nil {
		t.Fatal(err)
	}
	production := strings.Index(got, "- dir/a:b.go:4 ")
	test := strings.Index(got, "- z_test.go:2 ")
	if production < 0 || test < 0 || test < production {
		t.Fatalf("test hit must follow production hits:\n%s", got)
	}
	for _, want := range []string{
		"- dir/a:b.go:4 var _ = Consult() // issue:42:end\n",
		"- dir/new\\nline.go:7 var _ = Consult() // newline path\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestPackFakeRunnerCapsMixedHits(t *testing.T) {
	var grep strings.Builder
	for i := 0; i < 9; i++ {
		grep.WriteString(grepRecord(fmt.Sprintf("prod-%02d.go", i), "2", "var _ = Consult() // production"))
	}
	for i := 0; i < 8; i++ {
		grep.WriteString(grepRecord(fmt.Sprintf("test-%02d_test.go", i), "2", "var _ = Consult() // test"))
	}
	got, err := Pack(fakeRunner{replies: fakeReplies(grep.String(), "")}, "/repo", "base", "head")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- prod-08.go:2 ", "- test-05_test.go:2 ", "- … (+2 more)\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- test-06_test.go:2 ") || strings.Contains(got, "- test-07_test.go:2 ") {
		t.Errorf("hits beyond the cap were printed:\n%s", got)
	}
}
