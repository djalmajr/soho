package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// SOH-311: a worker whose own state dir differs from the orchestrator's lists
// the registry named by SOHO_REGISTRY_DIR, the one its copies are added to.
func TestCopiesListReadsTheRegistryDir(t *testing.T) {
	f := hcHkCopies(t)
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	sd := f.stateDir(t)
	owned := filepath.Join(f.tmp, "owned")
	if err := os.MkdirAll(owned, 0o700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: owned, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now().Add(-time.Minute)), Source: "-", Origin: "mutation-copy"}})
	worker := f.env.Clone()
	other := t.TempDir()
	worker["SOHO_DIR"] = other
	worker["SOHO_STATE_DIR"] = other
	worker["SOHO_REGISTRY_DIR"] = sd
	code, out, errOut := f.run(t, worker, "copies")
	if code != 0 || !strings.Contains(out, owned+"  worker  ") {
		t.Fatalf("code=%d out=%q err=%q, want the registered copy from %s", code, out, errOut, sd)
	}
	// Without the hint the worker reads its own (empty) state, as before.
	delete(worker, "SOHO_REGISTRY_DIR")
	code, out, _ = f.run(t, worker, "copies")
	if code != 0 || strings.Contains(out, owned) {
		t.Fatalf("without SOHO_REGISTRY_DIR the worker must not see the orchestrator's registry: code=%d out=%q", code, out)
	}
}
