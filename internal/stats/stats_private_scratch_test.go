package stats

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

// The stats read the scratch reports dir only when it is private to the
// user, and its files never through a link (DJA-443).

func statsScratch(t *testing.T) (*amendLostFixture, string, *strings.Builder) {
	t.Helper()
	f := newAmendLostFixture(t)
	dir, err := core.PrivateScratchReports(f.env(), "ws", core.ScratchCreate)
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	old := platform.Stderr
	platform.Stderr = &stderr
	t.Cleanup(func() { platform.Stderr = old })
	return f, dir, &stderr
}

const acceptedSidecar = "{\"version\":1,\"kind\":\"claude\",\"model\":\"m\",\"effort\":\"high\",\"submission\":\"accepted\"}\n"

func TestStatsCountsAPrivateScratchPair(t *testing.T) {
	f, dir, stderr := statsScratch(t)
	stem := filepath.Join(dir, "worker-20261020T030000")
	for path, body := range map[string]string{stem + ".brief.md": "Role: implementer\n", stem + ".dispatch.json": acceptedSidecar, stem + ".md": "report\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pairs := collectPairs(f.sd, dir, "")
	if len(pairs) != 1 || !pairs[0].scratch {
		t.Fatalf("pairs=%+v", pairs)
	}
	loadMeta(&pairs[0], "")
	if !pairs[0].counted || !pairs[0].hasReport || stderr.Len() != 0 {
		t.Fatalf("pair=%+v stderr=%q; want it counted with its report", pairs[0], stderr.String())
	}
}

func TestStatsSkipsALinkInTheScratchWithAWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows without developer mode")
	}
	t.Run("a sidecar that is a link", func(t *testing.T) {
		f, dir, stderr := statsScratch(t)
		stem := filepath.Join(dir, "worker-20261020T030000")
		victim := filepath.Join(t.TempDir(), "victim.json")
		if err := os.WriteFile(victim, []byte(acceptedSidecar), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stem+".brief.md", []byte("Role: implementer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, stem+".dispatch.json"); err != nil {
			t.Fatal(err)
		}
		pairs := collectPairs(f.sd, dir, "")
		if len(pairs) != 1 {
			t.Fatalf("pairs=%+v", pairs)
		}
		loadMeta(&pairs[0], "")
		want := "stats: skipped " + filepath.Base(stem) + ".dispatch.json (reports: refusing " + stem + ".dispatch.json: it is not a regular file)"
		if pairs[0].counted || pairs[0].snapshot != nil || !strings.Contains(stderr.String(), want) {
			t.Fatalf("pair=%+v stderr=%q; want the sidecar skipped with %q", pairs[0], stderr.String(), want)
		}
	})
	t.Run("a brief that is a link", func(t *testing.T) {
		f, dir, stderr := statsScratch(t)
		stem := filepath.Join(dir, "worker-20261020T030000")
		victim := filepath.Join(t.TempDir(), "victim.md")
		if err := os.WriteFile(victim, []byte("Role: implementer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, stem+".brief.md"); err != nil {
			t.Fatal(err)
		}
		if pairs := collectPairs(f.sd, dir, ""); len(pairs) != 0 {
			t.Fatalf("pairs=%+v; want the linked brief skipped", pairs)
		}
		if !strings.Contains(stderr.String(), "stats: skipped "+filepath.Base(stem)+".brief.md (reports: refusing "+stem+".brief.md: it is not a regular file)") {
			t.Fatalf("stderr=%q", stderr.String())
		}
	})
}

func TestStatsSkipsARefusedScratchDirWithAWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the scratch's owner is checked on Unix only")
	}
	f, dir, stderr := statsScratch(t)
	stem := filepath.Join(dir, "worker-20261020T030000")
	for path, body := range map[string]string{stem + ".brief.md": "Role: implementer\n", stem + ".dispatch.json": acceptedSidecar} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := core.ScratchOwnerOf
	core.ScratchOwnerOf = func(info fs.FileInfo) (int, bool) {
		if info.IsDir() && info.Name() == "reports" {
			return os.Getuid() + 1, true
		}
		return old(info)
	}
	t.Cleanup(func() { core.ScratchOwnerOf = old })
	out, code := f.run(t)
	want := "stats: skipped the scratch reports dir (reports: refusing " + dir + ": it belongs to another user)"
	if code != 0 || !strings.Contains(stderr.String(), want) || !strings.Contains(out, "no dispatches recorded") {
		t.Fatalf("code=%d out=%q stderr=%q; want %q and nothing counted", code, out, stderr.String(), want)
	}
}

func TestStatsCreatesNoScratch(t *testing.T) {
	f := newAmendLostFixture(t)
	if _, code := f.run(t); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if _, err := os.Lstat(filepath.Join(f.root, "tmp", "soho")); !os.IsNotExist(err) {
		t.Fatalf("the stats created the scratch: %v", err)
	}
}
