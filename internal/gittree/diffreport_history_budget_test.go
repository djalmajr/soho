package gittree

// Permanent source-bound regressions for the single aggregate per-walk
// historical content budget: the locate-candidate sidecar reads, the
// selected sidecar reads and the composed historical briefs all charge
// the D7 4 MiB per-walk aggregate; per-file valid but aggregate-oversized
// history refuses with the existing unavailable-scope outcome, and a
// small valid history still resolves the owned scope. The initial
// current composed brief keeps its separate per-file cap.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// historyBudgetScope runs the source's owned-scope walk over a pointer
// history of the given member count: largeBriefs plants 3 MiB historical
// briefs (per-file valid, aggregate oversize) and largeSidecars plants
// near-cap historical sidecars (per-file valid under sidecarInspectLimit,
// aggregate oversize across the locate and entry reads). The owned
// section stands in the oldest member, scanned last.
func historyBudgetScope(t *testing.T, members int, largeBriefs, largeSidecars bool) ([]string, bool, bool) {
	t.Helper()
	src, sd, _ := sourceFixture(t)
	write := func(p string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	task := filepath.Join(sd, "reports", "worker.current.md")
	current := filepath.Join(sd, "reports", "current.md")
	history := make([]string, 0, members)
	for i := 0; i < members; i++ {
		history = append(history, filepath.Join(sd, "reports", fmt.Sprintf("hist%d.md", i)))
	}
	pointer, err := json.Marshal(map[string]any{"version": 1, "task_report": task, "current": current, "history": history})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(sd, "task-report-worker.json"), pointer)
	currentSidecarRaw, err := json.Marshal(map[string]string{"task_report": task})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(sd, "briefs", "current.dispatch.json"), currentSidecarRaw)
	write(filepath.Join(sd, "briefs", "current.brief.md"), []byte("# Brief\n"))
	for i := 0; i < members; i++ {
		stem := fmt.Sprintf("hist%d", i)
		sidecarRaw := currentSidecarRaw
		if largeSidecars {
			// A near-cap padded sidecar that still parses: the pad keeps
			// the object valid and the total under the per-file cap.
			padded, err := json.Marshal(map[string]string{
				"task_report": task,
				"pad":         strings.Repeat("x", (1<<20)-256-len(currentSidecarRaw)),
			})
			if err != nil {
				t.Fatal(err)
			}
			sidecarRaw = padded
		}
		write(filepath.Join(sd, "briefs", stem+".dispatch.json"), sidecarRaw)
		text := "# Brief\n"
		if largeBriefs {
			text += strings.Repeat("x", 3<<20) + "\n"
		}
		if i == 0 {
			text += "## Owned files\n- `owned.txt`\n"
		}
		write(filepath.Join(sd, "briefs", stem+".brief.md"), []byte(text))
	}
	o := parseObjectBytes(currentSidecarRaw, nil)
	return diffEffectiveOwnedRead(filepath.Join(sd, "briefs", "current.dispatch.json"), "worker", o, sourceOwnedSeams(src))
}

func TestDiffEffectiveOwnedSourceHistoryAggregateBudget(t *testing.T) {
	// A small valid history resolves the owned scope through the walk.
	owned, has, ok := historyBudgetScope(t, 2, false, false)
	if !ok || !has || len(owned) != 1 || owned[0] != "owned.txt" {
		t.Fatalf("small history = %v %v %v, want the owned scope", owned, has, ok)
	}
	// Per-file valid but aggregate-oversized historical briefs (two 3 MiB
	// briefs against the 4 MiB per-walk aggregate) refuse with the
	// existing unavailable-scope outcome.
	owned, has, ok = historyBudgetScope(t, 2, true, false)
	if ok || has {
		t.Fatalf("aggregate-oversized briefs = %v %v %v, want the refusal", owned, has, ok)
	}
	// Per-file valid but aggregate-oversized historical sidecars (five
	// near-cap sidecars: the locate and entry reads together charge the
	// aggregate) refuse the same way.
	owned, has, ok = historyBudgetScope(t, 5, false, true)
	if ok || has {
		t.Fatalf("aggregate-oversized sidecars = %v %v %v, want the refusal", owned, has, ok)
	}
	// A small sidecar-heavy history stays inside the aggregate and
	// resolves.
	owned, has, ok = historyBudgetScope(t, 5, false, false)
	if !ok || !has || len(owned) != 1 || owned[0] != "owned.txt" {
		t.Fatalf("small sidecar history = %v %v %v, want the owned scope", owned, has, ok)
	}
}
