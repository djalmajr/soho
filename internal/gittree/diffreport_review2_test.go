package gittree_test

// Review round 2 (forms-r-20261119T180825): the settled audit keeps a
// deleted tracked root entry owned through the baseline it already
// records, and a negative section label excludes its token the same way
// the lint and the overlap reader exclude it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/jsonjs"
)

// F1: the root-entry exception also reads the root names the audit already
// records at dispatch, so a tracked root file the task deleted stays owned.
func TestDiffReportOwnedDeletedRootEntryBaseline(t *testing.T) {
	for _, tc := range []struct {
		name string
		item string
	}{
		{"mixed_root_name", "- `Dockerfile` and `go.mod`"},
		{"singleton_control", "- `Dockerfile`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiffFixtureFiles(t, "# Owned files\n"+tc.item+"\n", "Completed.\n", "implementer", map[string]string{"Dockerfile": "baseline\n"})
			// A real dispatch snapshot: it is empty because the worktree
			// matches the recorded HEAD, which is exactly the case the
			// root name must come from the Git baseline.
			snapshot, ok := gittree.Snapshot(f.env, f.tree).Get("git_snapshot")
			if !ok {
				t.Fatal("fixture did not capture a dispatch snapshot")
			}
			body, err := os.ReadFile(f.sidecar)
			if err != nil {
				t.Fatal(err)
			}
			var side map[string]json.RawMessage
			if err := json.Unmarshal(body, &side); err != nil {
				t.Fatal(err)
			}
			side["git_snapshot"] = json.RawMessage(jsonjs.Stringify(snapshot))
			body, err = json.Marshal(side)
			if err != nil {
				t.Fatal(err)
			}
			diffWrite(t, f.sidecar, string(body))
			if err := os.Remove(filepath.Join(f.tree, "Dockerfile")); err != nil {
				t.Fatal(err)
			}
			value, out, stderr := f.wait(t)
			got := ownedOutside(value)
			t.Logf("out=%s", out)
			if value["diff_stat"] == nil {
				t.Fatalf("the audit did not run: diff_stat is absent (stderr=%q)", stderr)
			}
			if len(got) != 0 {
				t.Errorf("an audited deletion of a declared root entry must stay inside scope: outside_owned=%q", got)
			}
		})
	}
}

// F2: Forbidden: (and every negative label the lint knows) contributes
// nothing to ownership in the audit, under Owned files and under
// Orchestrator files.
func TestDiffReportOwnedNegativeLabel(t *testing.T) {
	for _, tc := range []struct {
		name, brief string
	}{
		{"negative_label_in_owned_scope", "# Owned files\n- Forbidden: `Dockerfile`\n- Owned: `go.mod`\n"},
		{"negative_label_in_orchestrator_scope", "# Owned files\n- `go.mod`\n# Orchestrator files\n- Forbidden: `Dockerfile`\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiffFixtureFiles(t, tc.brief, "Completed.\n", "implementer", map[string]string{"Dockerfile": "baseline\n"})
			if err := os.WriteFile(filepath.Join(f.tree, "Dockerfile"), []byte("changed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			value, _, stderr := f.wait(t)
			got, want := ownedOutside(value), []string{"Dockerfile"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("outside_owned=%q want %q stderr=%q", got, want, stderr)
			}
		})
	}
}
