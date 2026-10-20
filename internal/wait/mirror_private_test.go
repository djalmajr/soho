package wait

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// privateMirror is a scratch reports dir created the way the dispatch
// creates it (three private levels), a state dir, and a pair stem in it.
func privateMirror(t *testing.T) (*core.Config, platform.Env, string, string, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state", "ws")
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	env := platform.Env{"TMPDIR": filepath.Join(root, "tmp"), "HERDR_WORKSPACE_ID": "ws"}
	dir, err := core.PrivateScratchReports(env, "ws", core.ScratchCreate)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, env, state, dir, filepath.Join(dir, "worker-20261020T030000")
}

func captureStderr(t *testing.T) *strings.Builder {
	t.Helper()
	var stderr strings.Builder
	oldErr := platform.Stderr
	platform.Stderr = &stderr
	t.Cleanup(func() { platform.Stderr = oldErr })
	return &stderr
}

func writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMirrorCopiesThePairFromAPrivateScratch(t *testing.T) {
	ctx, env, state, _, stem := privateMirror(t)
	writeFiles(t, map[string]string{stem + ".md": "report\n", stem + ".brief.md": "brief\n", stem + ".dispatch.json": "{\"v\":1}\n"})
	stderr := captureStderr(t)
	mirrorReport(state, "worker", stem+".md", ctx, env, "")
	base := filepath.Base(stem)
	for dst, want := range map[string]string{
		filepath.Join(state, "reports", base+".md"):           "report\n",
		filepath.Join(state, "briefs", base+".md"):            "brief\n",
		filepath.Join(state, "briefs", base+".dispatch.json"): "{\"v\":1}\n",
	} {
		if data, err := os.ReadFile(dst); err != nil || string(data) != want {
			t.Fatalf("%s=%q err=%v; want %q", dst, data, err, want)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestMirrorRefusesALinkInPlaceOfTheReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows without developer mode")
	}
	ctx, env, state, _, stem := privateMirror(t)
	victim := filepath.Join(t.TempDir(), "secret.txt")
	writeFiles(t, map[string]string{victim: "another user's secret\n", stem + ".brief.md": "brief\n"})
	if err := os.Symlink(victim, stem+".md"); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t)
	mirrorReport(state, "worker", stem+".md", ctx, env, "")
	want := "wait: the report of 'worker' was refused and not copied to the state dir (reports: refusing " + stem + ".md: it is not a regular file)"
	if strings.Count(stderr.String(), want) != 1 {
		t.Fatalf("stderr=%q; want one line %q", stderr.String(), want)
	}
	dst := filepath.Join(state, "reports", filepath.Base(stem)+".md")
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("the link's target was copied to the state dir: %v", err)
	}
}

func TestMirrorRefusesAScratchLevelOfAnotherOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the scratch's owner is checked on Unix only")
	}
	ctx, env, state, _, stem := privateMirror(t)
	writeFiles(t, map[string]string{stem + ".md": "report\n", stem + ".brief.md": "brief\n"})
	old := core.ScratchOwnerOf
	core.ScratchOwnerOf = func(info fs.FileInfo) (int, bool) {
		if info.IsDir() && info.Name() == "ws" {
			return os.Getuid() + 1, true
		}
		return old(info)
	}
	t.Cleanup(func() { core.ScratchOwnerOf = old })
	stderr := captureStderr(t)
	mirrorReport(state, "worker", stem+".md", ctx, env, "")
	want := "wait: the report of 'worker' was refused and not copied to the state dir (reports: refusing " + filepath.Dir(filepath.Dir(stem)) + ": it belongs to another user)"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr=%q; want %q", stderr.String(), want)
	}
	if _, err := os.Stat(filepath.Join(state, "reports")); !os.IsNotExist(err) {
		t.Fatalf("something was copied to the state dir: %v", err)
	}
}

func TestMirrorReadsNothingWithoutTheScratch(t *testing.T) {
	root := t.TempDir()
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	env := platform.Env{"TMPDIR": filepath.Join(root, "tmp"), "HERDR_WORKSPACE_ID": "ws"}
	stderr := captureStderr(t)
	mirrorReport(filepath.Join(root, "state"), "worker", filepath.Join(core.ScratchReportsDir(env, "ws"), "worker-x.md"), ctx, env, "")
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if _, err := os.Lstat(filepath.Join(root, "tmp", "soho")); !os.IsNotExist(err) {
		t.Fatalf("the reader created the scratch: %v", err)
	}
}
