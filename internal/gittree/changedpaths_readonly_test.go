package gittree

// Focused tests for ChangedPathsReadOnly (review-2 F2): the same
// changed-path algorithm with the read-only role gate — unavailable custom
// role metadata reports nil (report authority preserved) without
// materializing the skill bundle or dying; the built-in role keeps the
// algorithm; and the ordinary ChangedPaths keeps its command behavior.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func changedPathsReadOnlyFixture(t *testing.T, role string) (string, platform.Env) {
	t.Helper()
	tree := t.TempDir()
	home := t.TempDir()
	env := platform.Env{
		"PATH":            filepath.Join(tree, "no-such-bin"),
		"HOME":            home,
		"USERPROFILE":     home,
		"TMPDIR":          tree,
		"XDG_CONFIG_HOME": home,
		"XDG_CACHE_HOME":  filepath.Join(home, "cache"),
	}
	sidecar := filepath.Join(t.TempDir(), "report.dispatch.json")
	raw, err := json.Marshal(map[string]any{
		"git_tree": tree,
		"git_head": "0123456789012345678901234567890123456789",
		"role":     role,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return sidecar, env
}

// TestChangedPathsReadOnlyUnavailableRoleMetadata: a custom role with no
// project, env or existing-skill metadata is a nil result (report
// authority preserved) — no materialization, no Die — and a missing
// sidecar stays nil like every other metadata failure.
func TestChangedPathsReadOnlyUnavailableRoleMetadata(t *testing.T) {
	sidecar, env := changedPathsReadOnlyFixture(t, "custom-review")
	if paths := ChangedPathsReadOnly(sidecar, "worker", env); paths != nil {
		t.Fatalf("unavailable role metadata paths = %#v, want nil", paths)
	}
	if paths := ChangedPathsReadOnly(filepath.Join(t.TempDir(), "absent.dispatch.json"), "worker", env); paths != nil {
		t.Fatalf("absent sidecar paths = %#v, want nil", paths)
	}
	if entries, err := os.ReadDir(filepath.Join(env.Get("HOME"), "cache")); err == nil {
		t.Fatalf("the read-only reader materialized the cache: %v", entries)
	}
}

// TestChangedPathsReadOnlyBuiltInKeepsTheAlgorithm: the built-in role
// passes the gate and runs the same changed-path reads; on a tree without
// a Git executable the reads fail and the result is nil, exactly as the
// ordinary reader reports on the same fixture.
func TestChangedPathsReadOnlyBuiltInKeepsTheAlgorithm(t *testing.T) {
	sidecar, env := changedPathsReadOnlyFixture(t, "implementer")
	got := ChangedPathsReadOnly(sidecar, "worker", env)
	want := ChangedPaths(sidecar, "worker", env)
	if (got == nil) != (want == nil) {
		t.Fatalf("read-only = %#v, ordinary = %#v on the same fixture", got, want)
	}
}
