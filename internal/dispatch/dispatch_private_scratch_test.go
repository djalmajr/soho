package dispatch

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

// The --for family reads the scratch sidecars only when the scratch
// reports dir is private to the user, and each sidecar never through a
// link (DJA-443).

const familySidecar = "{\"version\":1,\"kind\":\"claude\",\"model\":\"m\",\"effort\":\"high\",\"submission\":\"accepted\"}\n"

func familyScratch(t *testing.T) (platform.Env, string, *strings.Builder) {
	t.Helper()
	env := forModelEnv(t)
	dir, err := core.PrivateScratchReports(env, "ws", core.ScratchCreate)
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	old := platform.Stderr
	platform.Stderr = &stderr
	t.Cleanup(func() { platform.Stderr = old })
	return env, dir, &stderr
}

func TestForSpecFamilyReadsAPrivateScratchSidecar(t *testing.T) {
	env, dir, stderr := familyScratch(t)
	if err := os.WriteFile(filepath.Join(dir, "solo-20261020T030000.dispatch.json"), []byte(familySidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ForSpecFamily("solo", t.TempDir(), env, t.TempDir(), forModelConfig())
	if err != nil || got != "anthropic" || stderr.Len() != 0 {
		t.Fatalf("got %q, %v, stderr=%q; want anthropic", got, err, stderr.String())
	}
}

func TestForSpecFamilySkipsALinkedScratchSidecar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows without developer mode")
	}
	env, dir, stderr := familyScratch(t)
	victim := filepath.Join(t.TempDir(), "victim.json")
	if err := os.WriteFile(victim, []byte(familySidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "solo-20261020T030000.dispatch.json")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if got, err := ForSpecFamily("solo", t.TempDir(), env, t.TempDir(), forModelConfig()); err == nil {
		t.Fatalf("got %q; the linked sidecar must not resolve a family", got)
	}
	want := "dispatch: skipped solo-20261020T030000.dispatch.json (reports: refusing " + link + ": it is not a regular file)"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr=%q; want %q", stderr.String(), want)
	}
}

func TestForSpecFamilySkipsARefusedScratchDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the scratch's owner is checked on Unix only")
	}
	env, dir, stderr := familyScratch(t)
	if err := os.WriteFile(filepath.Join(dir, "solo-20261020T030000.dispatch.json"), []byte(familySidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	old := core.ScratchOwnerOf
	core.ScratchOwnerOf = func(info fs.FileInfo) (int, bool) {
		if info.IsDir() && info.Name() == "reports" {
			return os.Getuid() + 1, true
		}
		return old(info)
	}
	t.Cleanup(func() { core.ScratchOwnerOf = old })
	if got, err := ForSpecFamily("solo", t.TempDir(), env, t.TempDir(), forModelConfig()); err == nil {
		t.Fatalf("got %q; a refused scratch must not resolve a family", got)
	}
	want := "dispatch: skipped the scratch sidecars of 'solo' (reports: refusing " + dir + ": it belongs to another user)"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr=%q; want %q", stderr.String(), want)
	}
}

func TestForSpecFamilyCreatesNoScratch(t *testing.T) {
	env := forModelEnv(t)
	_, _ = ForSpecFamily("solo", t.TempDir(), env, t.TempDir(), forModelConfig())
	if _, err := os.Lstat(filepath.Join(env.Get("TMPDIR"), "soho")); !os.IsNotExist(err) {
		t.Fatalf("the family reader created the scratch: %v", err)
	}
}
