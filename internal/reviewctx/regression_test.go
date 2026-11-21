package reviewctx

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func newReferenceFixture(t *testing.T, references string) fixture {
	t.Helper()
	f := newFixture(t)
	f.write(t, "decl.go", "package p\nfunc Consult() int { return 1 }\n")
	f.write(t, "caller.go", "package p\n"+references)
	f.commit(t, "base")
	f.write(t, "decl.go", "package p\nfunc Consult() int { return 2 }\n")
	f.write(t, "docs/formal/guide.md", "See decl.go.\n")
	f.commit(t, "head")
	return f
}

func TestPackGitGrepConfiguration(t *testing.T) {
	f := newReferenceFixture(t, "var _ = Consult() // issue:42:end\n")
	want := f.pack(t, f.repo)
	requirePackLine(t, want, "- caller.go:2 var _ = Consult() // issue:42:end")
	for _, c := range []struct{ key, value string }{
		{"grep.column", "true"},
		{"color.grep", "always"},
		{"grep.patternType", "perl"},
		{"grep.lineNumber", "true"},
		{"grep.extendedRegexp", "true"},
	} {
		t.Run(c.key, func(t *testing.T) {
			f.git(t, "config", "--local", c.key, c.value)
			t.Cleanup(func() { f.git(t, "config", "--local", "--unset", c.key) })
			if got := f.pack(t, f.repo); got != want {
				t.Fatalf("setting %s=%s changed the pack:\n%s", c.key, c.value, got)
			}
		})
	}
}

func TestPackGitOtherConfiguration(t *testing.T) {
	f := newReferenceFixture(t, "var _ = Consult()\n")
	want := f.pack(t, f.repo)
	requirePackLine(t, want, "- docs/formal/guide.md:1 decl.go: See decl.go.")
	for _, c := range []struct{ key, value string }{
		{"core.abbrev", "12"},
		{"core.quotePath", "false"},
		{"color.diff", "always"},
		{"diff.relative", "true"},
		{"diff.renames", "copies"},
		{"diff.orderFile", filepath.Join(f.repo, "absent-order-file")},
	} {
		t.Run(c.key, func(t *testing.T) {
			f.git(t, "config", "--local", c.key, c.value)
			t.Cleanup(func() { f.git(t, "config", "--local", "--unset", c.key) })
			if got := f.pack(t, f.repo); got != want {
				t.Fatalf("setting %s=%s changed the pack:\n%s", c.key, c.value, got)
			}
		})
	}
	t.Run("global", func(t *testing.T) {
		globalConfig := filepath.Join(t.TempDir(), "gitconfig")
		if err := os.WriteFile(globalConfig, []byte("[grep]\n\tcolumn = true\n\tpatternType = perl\n[color]\n\tgrep = always\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["GIT_CONFIG_GLOBAL"] = globalConfig
		if got := f.pack(t, f.repo); got != want {
			t.Fatalf("global config changed the pack:\n%s", got)
		}
	})
}

func TestPackGitDiscoveredDirectoryKeepsRepository(t *testing.T) {
	selected := newReferenceFixture(t, "var _ = Consult()\n")
	want := selected.pack(t, selected.repo)
	neighbor := newFixture(t)
	neighbor.write(t, "neighbor.go", "package p\n")
	neighbor.commit(t, "base")
	neighbor.write(t, "neighbor.go", "package p\nfunc Neighbor() {}\n")
	neighbor.commit(t, "head")
	selected.git(t, "config", "--local", "core.worktree", filepath.ToSlash(neighbor.repo))
	if got := selected.pack(t, selected.repo); got != want {
		t.Fatalf("core.worktree redirected the committed range to another repository:\n%s", got)
	}
}

func TestPackGitReferenceAttributesUseHead(t *testing.T) {
	f := newReferenceFixture(t, "var _ = Consult()\n")
	want := f.pack(t, f.repo)
	requirePackLine(t, want, "- caller.go:2 var _ = Consult()")
	f.write(t, ".gitattributes", "caller.go -diff\n")
	if got := f.pack(t, f.repo); got != want {
		t.Errorf("a worktree attribute changed the pack:\n%s", got)
	}
	f.git(t, "add", "--", ".gitattributes")
	if err := os.Remove(filepath.Join(f.repo, ".gitattributes")); err != nil {
		t.Fatal(err)
	}
	if got := f.pack(t, f.repo); got != want {
		t.Errorf("a staged-only attribute changed the pack:\n%s", got)
	}
	attrs := filepath.Join(t.TempDir(), "attributes")
	if err := os.WriteFile(attrs, []byte("caller.go -diff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(t, "config", "--local", "core.attributesFile", filepath.ToSlash(attrs))
	if got := f.pack(t, f.repo); got != want {
		t.Errorf("a configured attributes file changed the pack:\n%s", got)
	}
	info := filepath.Join(f.repo, ".git", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "attributes"), []byte("caller.go -diff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.pack(t, f.repo); got != want {
		t.Errorf("a repository-local info/attributes entry changed the pack:\n%s", got)
	}
}

func TestPackGitReplacementRefsDoNotChangeTheRange(t *testing.T) {
	f := newReferenceFixture(t, "var _ = Consult()\n")
	want := f.pack(t, f.repo)
	f.git(t, "replace", "HEAD", "HEAD~1")
	if got := f.pack(t, f.repo); got != want {
		t.Errorf("replacement refs changed the committed range:\n%s", got)
	}
}

func TestPackTrimsReferencesBeforeRuneCut(t *testing.T) {
	prefix := "// Consult issue:42:end "
	boundary := prefix + strings.Repeat("界", 160-len(prefix))
	f := newReferenceFixture(t, "\t\tvar _ = Consult() // note: prose    \n"+
		"\t   "+boundary+"終    \n")
	got := f.pack(t, f.repo)
	requirePackLine(t, got, "- caller.go:2 var _ = Consult() // note: prose")
	requirePackLine(t, got, "- caller.go:3 "+boundary)
}

func TestPackLargeGrepOutputAllocation(t *testing.T) {
	// A malformed line number between good records must not consume the
	// following hit or count toward the overflow.
	grep := strings.Repeat(grepRecord("caller.go", "8", "Consult()"), 14) +
		grepRecord("caller.go", "bad", "Consult()") +
		strings.Repeat(grepRecord("caller.go", "9", "Consult()"), 7986)
	runner := fakeRunner{replies: fakeReplies(grep, "")}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := Pack(runner, "/repo", "base", "head")
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	requirePackLine(t, got, "- caller.go:9 Consult()")
	requirePackLine(t, got, "- … (+7985 more)")
	n := after.TotalAlloc - before.TotalAlloc
	t.Logf("pack allocated %d bytes for 8000 matches", n)
	if n > 4<<20 {
		t.Errorf("pack allocated %d bytes for 8000 matches, want at most 4 MiB", n)
	}
}

func TestPackPreservesUnusualRootBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support these checkout names through normal path APIs")
	}
	for _, tc := range []struct{ label, name string }{
		{"trailing space", "repo "},
		{"embedded newline", "re\npo"},
		{"trailing newline", "repo\n"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			root := t.TempDir()
			// A trimmed root can resolve and still read the wrong repository.
			neighbor := newFixtureAt(t, root, "repo")
			neighbor.write(t, "neighbor.go", "package p\n")
			neighbor.commit(t, "base")
			neighbor.write(t, "neighbor.go", "package p\nfunc Neighbor() {}\n")
			neighbor.commit(t, "head")

			f := newFixtureAt(t, root, tc.name)
			f.write(t, "nested/selected.go", "package p\n")
			f.commit(t, "base")
			f.write(t, "nested/selected.go", "package p\nfunc Selected() {}\n")
			f.commit(t, "head")
			got := f.pack(t, filepath.Join(f.repo, "nested"))
			base := strings.TrimSpace(f.git(t, "rev-parse", "--short=7", "HEAD~1"))
			head := strings.TrimSpace(f.git(t, "rev-parse", "--short=7", "HEAD"))
			requirePackLine(t, got, "# Review context "+base+".."+head)
			requirePackLine(t, got, "- nested/selected.go:2-2 Selected")
		})
	}
}

func TestPackIgnoresInheritedGitRepositoryAndConfig(t *testing.T) {
	f := newReferenceFixture(t, "var _ = Consult()\n")
	want := f.pack(t, f.repo)
	requirePackLine(t, want, "- caller.go:2 var _ = Consult()")
	neighbor := newFixture(t)
	neighbor.write(t, "target.go", "package p\n")
	neighbor.commit(t, "neighbor")
	neighborGit := filepath.Join(neighbor.repo, ".git")
	neighborObjects := filepath.Join(neighborGit, "objects")
	neighborConfig := filepath.Join(neighborGit, "config")
	objects := filepath.Join(f.repo, ".git", "objects")
	for _, tc := range []struct {
		name string
		vars map[string]string
	}{
		{"common directory", map[string]string{"GIT_COMMON_DIR": neighborGit}},
		{"object directory", map[string]string{"GIT_OBJECT_DIRECTORY": neighborObjects}},
		{"config parameters", map[string]string{"GIT_CONFIG_PARAMETERS": "'include.path'='" + filepath.ToSlash(neighborConfig) + "' 'grep.column'='true'"}},
		{"config count and entries", map[string]string{
			"GIT_CONFIG_COUNT": "2",
			"GIT_CONFIG_KEY_0": "include.path", "GIT_CONFIG_VALUE_0": neighborConfig,
			"GIT_CONFIG_KEY_1": "grep.column", "GIT_CONFIG_VALUE_1": "true",
		}},
		{"invalid config count", map[string]string{"GIT_CONFIG_COUNT": "invalid"}},
		{"alternative object directories", map[string]string{"GIT_ALTERNATE_OBJECT_DIRECTORIES": neighborObjects}},
		{"namespace", map[string]string{"GIT_NAMESPACE": "neighbor"}},
		{"literal pathspecs", map[string]string{"GIT_LITERAL_PATHSPECS": "1"}},
		{"ambient attribute tree", map[string]string{"GIT_ATTR_SOURCE": "no-such-tree"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, value := range tc.vars {
				f.env[name] = value
				t.Cleanup(func() { delete(f.env, name) })
			}
			if got := f.pack(t, f.repo); got != want {
				t.Fatalf("inherited Git environment changed the pack (objects should come from %s):\n%s", objects, got)
			}
		})
	}
}

func TestPackGitLinkedWorktreeKeepsItsCommonDirectory(t *testing.T) {
	f := newReferenceFixture(t, "var _ = Consult()\n")
	want := f.pack(t, f.repo)
	linked := filepath.Join(t.TempDir(), "linked")
	f.git(t, "worktree", "add", "-q", "--detach", linked, "HEAD")
	f.env["GIT_COMMON_DIR"] = filepath.Join(t.TempDir(), "unrelated")
	if got := f.pack(t, linked); got != want {
		t.Errorf("linked worktree lost its common directory:\n%s", got)
	}
}

func TestRunnerCleansGitOverridesCaseInsensitively(t *testing.T) {
	env := platform.Env{
		"gIt_cOmMoN_dIr": "neighbor", "git_Object_directory": "objects", "gIt_alternate_OBJECT_directories": "alternates",
		"git_Namespace": "namespace", "GIT_config_PARAMETERS": "parameters", "git_config_Count": "2",
		"git_config_KEY_0": "test.key", "git_CONFIG_value_0": "value",
		"git_dir": "dir", "giT_Work_Tree": "tree", "git_index_FILE": "index", "PATH": "fixture path",
	}
	r := NewRunner(env, "/repo").(runner)
	for key := range env {
		if key != "PATH" {
			if _, present := r.env[key]; present {
				t.Errorf("%s was retained in the child environment", key)
			}
		}
	}
	if r.env["PATH"] != env["PATH"] || r.env["GIT_OPTIONAL_LOCKS"] != "0" || env["gIt_cOmMoN_dIr"] != "neighbor" {
		t.Error("runner must retain PATH, decline optional Git locks and leave its input environment intact")
	}
}

func TestPackGoMembershipOnEachRenameSide(t *testing.T) {
	f := newFixture(t)
	archive := "package p\nfunc Archive() int { return %d }\nfunc UnchangedArchive() {}\nfunc ArchivedHelper() {}\n"
	f.write(t, "archive.go", fmt.Sprintf(archive, 1))
	f.write(t, "promoted.txt", "package p\nfunc Promoted() {}\n")
	f.commit(t, "base")
	f.git(t, "mv", "archive.go", "archive.txt")
	f.write(t, "archive.txt", fmt.Sprintf(archive, 2))
	f.git(t, "mv", "promoted.txt", "promoted.go")
	f.write(t, "docs/formal/guide.md", "archive.go archive.txt promoted.txt promoted.go\n")
	f.commit(t, "head")
	got := f.pack(t, f.repo)
	for _, line := range []string{
		"- R archive.go -> archive.txt",
		"- R promoted.txt -> promoted.go",
		"- removed: archive.txt Archive",
		"- removed: archive.txt UnchangedArchive",
		"- removed: archive.txt ArchivedHelper",
		"- promoted.go:2-2 Promoted",
		"### Promoted",
	} {
		requirePackLine(t, got, line)
	}
	for _, path := range []string{"archive.go", "archive.txt", "promoted.go", "promoted.txt"} {
		requirePackLine(t, got, "- docs/formal/guide.md:1 "+path+": archive.go archive.txt promoted.txt promoted.go")
	}
	if strings.Contains(got, "archive.txt:2-2") || strings.Contains(got, "### Archive") {
		t.Errorf("the non-Go head contributed declarations:\n%s", got)
	}
}

func TestPackParseFailureIdentifiesSide(t *testing.T) {
	f := newFixture(t)
	f.write(t, "repaired.go", "package p\nfunc Repaired(\n")
	f.write(t, "broken.go", "package p\nfunc Broken() {}\n")
	f.write(t, "deleted.go", "package p\nfunc Deleted(\n")
	f.commit(t, "base")
	f.write(t, "repaired.go", "package p\nfunc Repaired() {}\n")
	f.write(t, "broken.go", "package p\nfunc Broken(\n")
	f.git(t, "rm", "-q", "deleted.go")
	f.commit(t, "head")
	got := f.pack(t, f.repo)
	for _, want := range []string{
		"- broken.go: not parsed at head (broken.go:",
		"- repaired.go: not parsed at base (repaired.go:",
		"- deleted.go: not parsed at base (deleted.go:",
		"## Direct references\nTextual matches of the name in Go files at head; not resolved references.\nNone.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- removed: ") || strings.Contains(got, " Repaired\n") {
		t.Errorf("a partially parsed pair contributed declarations:\n%s", got)
	}
}

func TestPackChangedFileAndDocumentCaps(t *testing.T) {
	const doc = "docs/formal/guide.md"
	replies := fakeReplies("", doc+"\x00")
	var files strings.Builder
	for i := 0; i < 301; i++ {
		fmt.Fprintf(&files, "M\x00file-%03d.txt\x00", i)
	}
	replies[fakeDiffKey()] = files.String()
	excerpt := "   file-000.txt " + strings.Repeat("界", 200-len("   file-000.txt "))
	replies[fakeKey("cat-file", "blob", fakeHead+":"+doc)] = excerpt + "終\r\n" +
		strings.Repeat("file-000.txt other hit\n", 11) + "file-300.txt hit beyond changed-file cap\n"
	got, err := Pack(fakeRunner{replies: replies}, "/repo", "base", "head")
	if err != nil {
		t.Fatal(err)
	}
	_, changed, _ := strings.Cut(got, "## Changed files\n")
	changed, _, _ = strings.Cut(changed, "\n## Changed Go symbols\n")
	if count := strings.Count(changed, "- M "); count != 300 {
		t.Errorf("changed-file rows = %d, want 300", count)
	}
	if !strings.HasSuffix(changed, "- M file-299.txt\n- … (+1 more)\n") {
		t.Errorf("wrong changed-file boundary:\n%s", changed)
	}
	_, docs, _ := strings.Cut(got, "## Docs citing the changed files\n")
	if count := strings.Count(docs, " file-000.txt: "); count != 10 {
		t.Errorf("file-000.txt doc hits = %d, want 10", count)
	}
	requirePackLine(t, docs, "- "+doc+":1 file-000.txt: "+excerpt)
	if !strings.HasSuffix(docs, "- "+doc+":10 file-000.txt: file-000.txt other hit\n"+
		"- … (+2 more)\n- "+doc+":13 file-300.txt: file-300.txt hit beyond changed-file cap\n") {
		t.Errorf("wrong per-path doc boundary:\n%s", docs)
	}
}

func requirePackLine(t *testing.T, pack, line string) {
	t.Helper()
	if !strings.Contains("\n"+pack, "\n"+line+"\n") {
		t.Errorf("missing exact line %q in:\n%s", line, pack)
	}
}

// A bare clone holds every commit the pack reads, so it gives the same pack
// as the checkout it was cloned from.
func TestPackReadsABareRepository(t *testing.T) {
	f := newReferenceFixture(t, "var _ = Consult()\n")
	want := f.pack(t, f.repo)
	bare := filepath.Join(t.TempDir(), "mirror.git")
	f.git(t, "clone", "-q", "--bare", f.repo, bare)
	if got := f.pack(t, bare); got != want {
		t.Errorf("bare clone pack differs:\n%s\nwant:\n%s", got, want)
	}
}
