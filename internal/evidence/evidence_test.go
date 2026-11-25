package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func evidenceTestBase() string { return strings.Repeat("1", 40) }

func evidenceTestHead() string { return strings.Repeat("2", 40) }

func evidenceTestFixture(t *testing.T) (Inventory, string) {
	t.Helper()
	root := t.TempDir()
	content := []byte("fixture log with SECRET-LOG-LINE\n")
	path := filepath.Join(root, "artifact.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	inventory := Inventory{
		Schema: Schema,
		Candidate: Candidate{
			Repository: "org/repo",
			Base:       evidenceTestBase(),
			Head:       evidenceTestHead(),
		},
		Expected: []Expected{{Layer: "unit", Platform: "linux", Transport: "herdr"}},
		Receipts: []Receipt{{
			Layer:     "unit",
			Platform:  "linux",
			Transport: "herdr",
			Producer:  "machine-lab-01",
			Outcome:   "pass",
			ID:        "rcpt-unit",
			Since:     "2026-11-24T12:00:00Z",
			Until:     "2026-11-24T12:05:00Z",
			Identity: Identity{
				Repository: "org/repo",
				Base:       evidenceTestBase(),
				Source:     evidenceTestHead(),
			},
			SHA256: hex.EncodeToString(sum[:]),
			Path:   "artifact.json",
		}},
	}
	return inventory, root
}

func evidenceStatus(t *testing.T, inventory Inventory, baseDir, want string) Manifest {
	t.Helper()
	manifest := Assemble(inventory, baseDir)
	if manifest.Result != "fail" {
		t.Fatalf("manifest reported pass: %+v", manifest)
	}
	if len(manifest.Layers) != 1 || manifest.Layers[0].Status != want {
		t.Fatalf("statuses=%+v want %q", manifest.Layers, want)
	}
	return manifest
}

func TestAssembleRejectsHostilePaths(t *testing.T) {
	inventory, root := evidenceTestFixture(t)
	outside := filepath.Join(filepath.Dir(root), "outside.json")
	if err := os.WriteFile(outside, []byte("fixture log with SECRET-LOG-LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	dir := filepath.Join(root, "not-a-file")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
	}{
		{name: "parent escape", path: "../outside.json"},
		{name: "absolute", path: outside},
		{name: "backslash", path: "..\\outside.json"},
		{name: "directory", path: "not-a-file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := inventory
			changed.Receipts = append([]Receipt(nil), inventory.Receipts...)
			changed.Receipts[0].Path = tc.path
			evidenceStatus(t, changed, root, "unbound")
		})
	}
}

func TestAssembleRejectsSymlinkArtifacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	inventory, root := evidenceTestFixture(t)
	if err := os.Symlink("artifact.json", filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}
	inventory.Receipts[0].Path = "link.json"
	evidenceStatus(t, inventory, root, "unbound")
}

func TestAssembleRejectsLegacyHeadRecord(t *testing.T) {
	_, root := evidenceTestFixture(t)
	raw := `{"schema":1,"candidate":{"repository":"org/repo","base":"` + evidenceTestBase() + `","head":"` + evidenceTestHead() + `"},"expected":[{"layer":"unit","platform":"linux","transport":"herdr"}],"receipts":[{"layer":"unit","platform":"linux","transport":"herdr","producer":"machine-lab-01","outcome":"pass","id":"rcpt-legacy","since":"2026-11-24T12:00:00Z","until":"2026-11-24T12:05:00Z","identity":{"repository":"org/repo","base":"` + evidenceTestBase() + `","head":"` + evidenceTestHead() + `"},"sha256":"` + strings.Repeat("a", 64) + `","path":"artifact.json"}]}`
	var inventory Inventory
	if err := json.Unmarshal([]byte(raw), &inventory); err != nil {
		t.Fatal(err)
	}
	evidenceStatus(t, inventory, root, "unbound")
}

func TestAssembleBoundsArtifactReads(t *testing.T) {
	inventory, root := evidenceTestFixture(t)
	content := make([]byte, int(maxArtifactSize)+1)
	sum := sha256.Sum256(content)
	path := filepath.Join(root, "artifact.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	inventory.Receipts[0].SHA256 = hex.EncodeToString(sum[:])
	evidenceStatus(t, inventory, root, "unavailable")
}

func TestAssembleDuplicateReceiptsAreUnbound(t *testing.T) {
	inventory, root := evidenceTestFixture(t)
	inventory.Receipts = append(inventory.Receipts, inventory.Receipts[0])
	manifest := evidenceStatus(t, inventory, root, "unbound")
	if manifest.Unbound != 1 {
		t.Fatalf("unbound=%d want 1: %+v", manifest.Unbound, manifest)
	}
}

func TestRunRejectsOversizedInventoryWithoutPath(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "inventory.json")
	payload := []byte(`{"padding":"` + strings.Repeat("x", int(maxInventorySize)) + `"}`)
	if err := os.WriteFile(input, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	var errOut strings.Builder
	code := Run([]string{input}, &out, &errOut)
	if code != 2 || out.String() != "" || errOut.String() == "" || strings.Contains(errOut.String(), root) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunAcceptsInventoryAtReadBound(t *testing.T) {
	inventory, root := evidenceTestFixture(t)
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	// Unknown fields remain accepted, including numbers that need not fit float64.
	data = append(data[:len(data)-1], `,"extra":[{"number":1e400},{"number":2}]}`...)
	data = append(data, strings.Repeat(" ", int(maxInventorySize)-len(data))...)
	input := filepath.Join(root, "inventory.json")
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := Run([]string{input}, &out, &errOut)
	var manifest Manifest
	if err := json.Unmarshal([]byte(out.String()), &manifest); code != 0 || errOut.Len() != 0 || err != nil || manifest.Result != "pass" {
		t.Fatalf("code=%d stdout=%q stderr=%q parse=%v", code, out.String(), errOut.String(), err)
	}
}

func evidenceRunInventory(t *testing.T, inventory Inventory, root string) (int, string, string) {
	t.Helper()
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "inventory.json")
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := Run([]string{input}, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestEvidencePublicCoordinateAllowlistCoverage(t *testing.T) {
	for _, layer := range []string{"unit", "race", "build", "vet", "format", "contract", "integration", "e2e", "native", "review", "ci", "gate", "mutation"} {
		t.Run("layer "+layer, func(t *testing.T) {
			inventory, root := evidenceTestFixture(t)
			inventory.Expected[0].Layer = layer
			inventory.Receipts[0].Layer = layer
			code, out, errOut := evidenceRunInventory(t, inventory, root)
			if code != 0 || errOut != "" {
				t.Fatalf("code=%d stderr=%q out=%s", code, errOut, out)
			}
			var manifest Manifest
			if err := json.Unmarshal([]byte(out), &manifest); err != nil || len(manifest.Layers) != 1 || manifest.Layers[0].Layer != layer {
				t.Fatalf("manifest=%s err=%v", out, err)
			}
		})
	}
	for _, platform := range []string{"linux", "darwin", "windows", "any"} {
		t.Run("platform "+platform, func(t *testing.T) {
			inventory, root := evidenceTestFixture(t)
			inventory.Expected[0].Platform = platform
			inventory.Receipts[0].Platform = platform
			code, out, errOut := evidenceRunInventory(t, inventory, root)
			if code != 0 || errOut != "" {
				t.Fatalf("code=%d stderr=%q out=%s", code, errOut, out)
			}
		})
	}
	for _, transport := range []string{"hc", "herdr", "none"} {
		t.Run("transport "+transport, func(t *testing.T) {
			inventory, root := evidenceTestFixture(t)
			inventory.Expected[0].Transport = transport
			inventory.Receipts[0].Transport = transport
			code, out, errOut := evidenceRunInventory(t, inventory, root)
			if code != 0 || errOut != "" {
				t.Fatalf("code=%d stderr=%q out=%s", code, errOut, out)
			}
		})
	}
}

func TestEvidencePublicCoordinateGrammarRejectsMarkers(t *testing.T) {
	cases := []struct {
		name   string
		marker string
		mutate func(*Inventory)
	}{
		{name: "repository", marker: "private-coordinate-marker", mutate: func(inv *Inventory) { inv.Candidate.Repository = "org/private-coordinate-marker/repo" }},
		{name: "layer", marker: "private-layer-marker", mutate: func(inv *Inventory) { inv.Expected[0].Layer = "private-layer-marker" }},
		{name: "platform", marker: "private-platform-marker", mutate: func(inv *Inventory) { inv.Expected[0].Platform = "private-platform-marker" }},
		{name: "transport", marker: "private-transport-marker", mutate: func(inv *Inventory) { inv.Expected[0].Transport = "private-transport-marker" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inventory, root := evidenceTestFixture(t)
			tc.mutate(&inventory)
			code, out, errOut := evidenceRunInventory(t, inventory, root)
			if code != 2 || out != "" || errOut == "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
			}
			if strings.Contains(out, tc.marker) || strings.Contains(errOut, tc.marker) || strings.Contains(errOut, root) {
				t.Errorf("marker %q or root reached output: %q %q", tc.marker, out, errOut)
			}
		})
	}
}

func TestEvidenceRepositoryGrammarEdgeCases(t *testing.T) {
	for _, repository := range []string{"/repo", "org/", "-org/repo", "org/bad+repo", "org/repo/extra"} {
		t.Run("reject "+repository, func(t *testing.T) {
			inventory, root := evidenceTestFixture(t)
			inventory.Candidate.Repository = repository
			code, out, errOut := evidenceRunInventory(t, inventory, root)
			if code != 2 || out != "" || errOut == "" || strings.Contains(errOut, repository) {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
			}
		})
	}
	for _, repository := range []string{"org/repo", "my_org/repo.v2-beta"} {
		t.Run("accept "+repository, func(t *testing.T) {
			inventory, root := evidenceTestFixture(t)
			inventory.Candidate.Repository = repository
			inventory.Receipts[0].Identity.Repository = repository
			code, out, errOut := evidenceRunInventory(t, inventory, root)
			if code != 0 || errOut != "" || !strings.Contains(out, repository) {
				t.Fatalf("code=%d stderr=%q out=%s", code, errOut, out)
			}
		})
	}
}

func TestEvidenceArtifactPathRejectsOriginalParentSegment(t *testing.T) {
	inventory, root := evidenceTestFixture(t)
	inventory.Receipts[0].Path = "artifacts/../artifact.json"
	manifest := Assemble(inventory, root)
	if manifest.Result != "fail" || len(manifest.Layers) != 1 || manifest.Layers[0].Status != "unbound" {
		t.Fatalf("manifest=%+v", manifest)
	}
}
