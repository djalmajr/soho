package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// DJA-564: run from inside a soho pane, the mutation-copy and mutation-guard
// tests inherited the session's state roots and registered their fixture
// copies in the workspace's real copies.tsv. The fixture environment drops
// the inherited roots (a value the fixture sets itself still wins), so a
// successful copy registers only inside the fixture.
func TestMutationCopyFixtureDropsInheritedStateRoots(t *testing.T) {
	registry := t.TempDir()
	state := t.TempDir()
	t.Setenv("SOHO_REGISTRY_DIR", registry)
	t.Setenv("SOHO_DIR", state)
	t.Setenv("SOHO_STATE_DIR", state)
	t.Setenv("HERDR_WORKSPACE_ID", "ambient-ws")

	f := newMutationCopyFixture(t)
	for _, key := range []string{"SOHO_REGISTRY_DIR", "SOHO_DIR", "SOHO_STATE_DIR", "HERDR_WORKSPACE_ID"} {
		if value := f.env.Get(key); value != "" {
			t.Fatalf("inherited %s=%q reached the fixture environment", key, value)
		}
	}

	// A successful copy registers itself: never in the ambient registry dir.
	code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", filepath.Join(f.root, "regcopy"))
	if code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	entries, err := os.ReadDir(registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the ambient registry directory was written: %v", names)
	}
}
