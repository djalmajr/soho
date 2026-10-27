package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
)

func reviewSnapshotTreeBytes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			out[path] = sha256.Sum256(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestReviewSnapshotGcHeldRegistryLock(t *testing.T) {
	f := newSnapshotFixture(t)
	obj := f.freeze(t, "worker")
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# no live owners\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(f.sd, "copies.tsv.lock")
	if err := os.WriteFile(lock, []byte(fmt.Sprintf("%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))), 0600); err != nil {
		t.Fatal(err)
	}
	old := core.CopiesLockTimeout
	core.CopiesLockTimeout = 300 * time.Millisecond
	t.Cleanup(func() { core.CopiesLockTimeout = old })
	before := reviewSnapshotTreeBytes(t, obj["snapshot"])
	saved := map[string]string{}
	for _, path := range []string{obj["patch"], obj["patch"] + ".sha256", filepath.Join(f.sd, "snapshots.tsv"), lock} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		saved[path] = string(data)
	}
	code, out, stderr := f.run(t, "gc", "--yes")
	want := "soho: copies: registry is locked by another soho (" + lock + ")\n"
	if code != 4 || stderr != want {
		t.Fatalf("exit=%d out=%s stderr=%q want=%q", code, out, stderr, want)
	}
	if !reflect.DeepEqual(before, reviewSnapshotTreeBytes(t, obj["snapshot"])) {
		t.Fatal("snapshot changed under held lock")
	}
	for path, want := range saved {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("registered artifact changed %s: %v", path, err)
		}
	}
	t.Logf("exit=%d stderr=%sregistered snapshot, patch, hash, registry and lock unchanged", code, stderr)
}
func TestReviewSnapshotNowrite(t *testing.T) {
	for _, name := range []string{"gc-registered", "gc-no-state", "release", "review-snapshot"} {
		t.Run(name, func(t *testing.T) {
			nr := newNowriteReads(t, name != "gc-no-state")
			if name != "gc-no-state" {
				path := filepath.Join(nr.f.repo, ".worktrees", "snap-nowrite-"+strings.Repeat("a", 7))
				patch := filepath.Join(nr.sd, "snapshots", "nowrite-"+strings.Repeat("a", 7)+".patch")
				for _, dir := range []string{path, filepath.Dir(patch)} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				for p, body := range map[string]string{filepath.Join(path, "sentinel"): "snapshot\n", patch: "patch\n", patch + ".sha256": "hash\n", filepath.Join(nr.sd, "snapshots.tsv"): snapshotHeader + "gone\t" + path + "\t" + strings.Repeat("a", 40) + "\t" + patch + "\tnow\n"} {
					if err := os.WriteFile(p, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			args := []string{"gc"}
			wantCode := 0
			if name == "release" {
				args = []string{"release", "worker"}
				wantCode = 2
			}
			if name == "review-snapshot" {
				args = []string{"review-snapshot", nr.f.repo}
				wantCode = 2
			}
			before := reviewSnapshotTreeBytes(t, nr.f.root)
			code, out, stderr := nr.runNowrite(t, args...)
			if code != wantCode {
				t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", code, wantCode, out, stderr)
			}
			if wantCode == 2 {
				want := "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: " + args[0] + " (1 extra argument not allowed) — unset SOHO_NOWRITE to write\n"
				if stderr != want {
					t.Fatalf("refusal=%q want=%q", stderr, want)
				}
			} else if stderr != "" {
				t.Fatalf("gc stderr=%s", stderr)
			}
			if !reflect.DeepEqual(before, reviewSnapshotTreeBytes(t, nr.f.root)) {
				t.Fatal("file bytes changed under SOHO_NOWRITE=1")
			}
			t.Logf("exit=%d tree paths/sizes/mtimes/file bytes unchanged; stdout=%s stderr=%s", code, out, stderr)
		})
	}
}
