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

// R404 F1: a read-only listing of the shared registry never incorporates the
// queue (no registry write, the queue stays).
func TestCopiesListNowriteLeavesTheQueue(t *testing.T) {
	f := hcHkCopies(t)
	sd := f.stateDir(t)
	owned := filepath.Join(f.tmp, "queued-review-resource")
	if err := os.MkdirAll(owned, 0o700); err != nil {
		t.Fatal(err)
	}
	queue, err := core.QueueCopy(f.env, filepath.Base(sd), sd, core.CopyRow{Path: owned, Owner: "-", Pane: "-", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"})
	if err != nil {
		t.Fatal(err)
	}
	worker := f.env.Clone()
	other := t.TempDir()
	worker["SOHO_DIR"], worker["SOHO_STATE_DIR"] = other, other
	worker["SOHO_REGISTRY_DIR"], worker["SOHO_NOWRITE"] = sd, "1"
	code, out, stderr := f.run(t, worker, "copies")
	rows, readErr := core.ReadCopies(sd)
	_, queueErr := os.Stat(queue)
	if code != 0 || readErr != nil || len(rows) != 0 || queueErr != nil {
		t.Fatalf("NOWRITE changed the shared registry or queue: code=%d rows=%d queue=%v read=%v out=%q err=%q", code, len(rows), queueErr, readErr, out, stderr)
	}
}
