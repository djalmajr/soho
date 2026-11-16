package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestConfigGetCopiesDir(t *testing.T) {
	env, cwd := commandFixture(t)
	env["SOHO_COPIES_DIR"] = "~/private copies"
	code, out, errOut := runIn(t, []string{"config", "get", "copies_dir"}, env, cwd)
	if code != 0 || out != "~/private copies\n" || errOut != "" {
		t.Fatalf("config get code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestMutationCopyConfiguredCopiesDirLifecycle(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
		{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
	})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	dir := filepath.Join(f.root, "private", "copies")
	f.env["SOHO_COPIES_DIR"] = dir
	code, out, errOut := f.run(t, f.env, "mutation-copy", "--source", f.repo, "--print-path")
	if code != 0 {
		t.Fatalf("mutation-copy code=%d out=%q err=%q", code, out, errOut)
	}
	copy := strings.TrimSpace(out)
	if filepath.Dir(copy) != dir {
		t.Fatalf("copy=%q, want child of %q", copy, dir)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{filepath.Dir(dir), dir} {
			st, err := os.Stat(p)
			if err != nil || st.Mode().Perm() != 0700 {
				t.Fatalf("parent %s mode=%v err=%v", p, st, err)
			}
		}
	}
	rows := f.copiesRows(t)
	if len(rows) != 1 || rows[0].Path != copy || rows[0].Owner != "worker" {
		t.Fatalf("rows=%+v", rows)
	}
	code, out, errOut = f.run(t, f.env, "copies")
	if code != 0 || !strings.Contains(out, copy+"  worker  ") {
		t.Fatalf("copies code=%d out=%q err=%q", code, out, errOut)
	}
	addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
	code, out, errOut = f.run(t, f.env, "release", "worker")
	if code != 0 || !strings.Contains(out, "removed copy "+copy) {
		t.Fatalf("release code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatalf("copy remains: %v", err)
	}
}

func TestMutationCopyConfiguredCopiesDirFileRefused(t *testing.T) {
	f := newCopiesFixture(t, nil)
	file := filepath.Join(f.root, "file")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_COPIES_DIR"] = file
	code, out, errOut := f.run(t, f.env, "mutation-copy", "--source", f.repo, "--print-path")
	if code != 2 || out != "" || !strings.Contains(errOut, "cannot be created") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	b, err := os.ReadFile(file)
	if err != nil || string(b) != "unchanged" {
		t.Fatalf("file=%q err=%v", b, err)
	}
	entries, err := os.ReadDir(f.tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("tmp=%v err=%v", entries, err)
	}
}

func TestMutationCopyConfiguredCopiesDirTildeAndExplicit(t *testing.T) {
	f := newCopiesFixture(t, nil)
	f.env["SOHO_COPIES_DIR"] = "~/cache/copies"
	code, out, errOut := f.run(t, f.env, "mutation-copy", "--source", f.repo, "--print-path")
	if code != 0 || filepath.Dir(strings.TrimSpace(out)) != filepath.Join(f.home, "cache", "copies") {
		t.Fatalf("tilde code=%d out=%q err=%q", code, out, errOut)
	}
	f.env["SOHO_COPIES_DIR"] = "invalid-relative"
	dest := filepath.Join(f.root, "explicit")
	code, out, errOut = f.run(t, f.env, "mutation-copy", "--source", f.repo, "--dest", dest, "--print-path")
	if code != 0 || strings.TrimSpace(out) != dest {
		t.Fatalf("explicit code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = f.run(t, f.env, "mutation-copy", "--source", f.repo, "--print-path")
	if code != 2 || out != "" || !strings.Contains(errOut, "invalid copies_dir") {
		t.Fatalf("invalid code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestGcConfiguredCopiesDirUnregistered(t *testing.T) {
	f := gcEmptyFixture(t)
	dir := filepath.Join(f.root, "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_COPIES_DIR"] = dir
	own := mkOldUnregCopy(t, dir, "soho-mutation-own", 40)
	writeTestOriginMarker(t, own, gitCommonDirOf(t, f.repo, f.env), f.repo)
	tmp := mkOldUnregCopy(t, f.tmp, "soho-mutation-tmp", 40)
	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || !strings.Contains(out, own+"  40 B  3h  this project") || !strings.Contains(out, tmp+"  40 B  3h  no origin marker") {
		t.Fatalf("gc code=%d out=%q err=%q", code, out, errOut)
	}
	if !gcUnregisteredOffers(own, f.env, f.repo) {
		t.Fatal("own configured copy must be offered again")
	}
	if _, err := os.Stat(own); err != nil {
		t.Fatalf("dry run changed copy: %v", err)
	}
}

// R431 F1: copies_dir naming TMPDIR under another spelling (here the
// upper-case name on a case-insensitive filesystem) is the same directory:
// gc lists its unregistered copy once.
func TestGcCopiesDirSameDirectoryOtherSpelling(t *testing.T) {
	f := gcEmptyFixture(t)
	alias := filepath.Join(filepath.Dir(f.tmp), strings.ToUpper(filepath.Base(f.tmp)))
	if alias == f.tmp {
		t.Skip("the temp directory name has no case to fold")
	}
	a, errA := os.Stat(f.tmp)
	b, errB := os.Stat(alias)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skip("case-sensitive filesystem")
	}
	f.env["SOHO_COPIES_DIR"] = alias
	tmp := mkOldUnregCopy(t, f.tmp, "soho-mutation-tmp", 40)
	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || strings.Count(out, "soho-mutation-tmp") != 1 {
		t.Fatalf("one physical copy must be listed once: code=%d out=%q err=%q (copy %s)", code, out, errOut, tmp)
	}
}
