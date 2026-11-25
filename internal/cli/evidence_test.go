package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

type evidenceInventoryCandidate struct {
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Head       string `json:"head"`
}

type evidenceInventoryExpected struct {
	Layer     string `json:"layer"`
	Platform  string `json:"platform"`
	Transport string `json:"transport"`
}

type evidenceInventoryIdentity struct {
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Source     string `json:"source"`
}

type evidenceInventoryReceipt struct {
	Layer     string                    `json:"layer"`
	Platform  string                    `json:"platform"`
	Transport string                    `json:"transport"`
	Producer  string                    `json:"producer"`
	Outcome   string                    `json:"outcome"`
	ID        string                    `json:"id"`
	Since     string                    `json:"since"`
	Until     string                    `json:"until"`
	Identity  evidenceInventoryIdentity `json:"identity"`
	SHA256    string                    `json:"sha256"`
	Path      string                    `json:"path"`
}

type evidenceInventory struct {
	Schema    int                         `json:"schema"`
	Candidate evidenceInventoryCandidate  `json:"candidate"`
	Expected  []evidenceInventoryExpected `json:"expected"`
	Receipts  []evidenceInventoryReceipt  `json:"receipts"`
}

type evidenceManifestLayer struct {
	Layer     string `json:"layer"`
	Platform  string `json:"platform"`
	Transport string `json:"transport"`
	Status    string `json:"status"`
	ID        string `json:"id"`
	Source    string `json:"source"`
	Digest    string `json:"digest"`
	Since     string `json:"since"`
	Until     string `json:"until"`
}

type evidenceManifest struct {
	Schema    int                        `json:"schema"`
	Candidate evidenceInventoryCandidate `json:"candidate"`
	Result    string                     `json:"result"`
	Layers    []evidenceManifestLayer    `json:"layers"`
	Unbound   int                        `json:"unbound"`
}

var (
	evidenceBase = strings.Repeat("1", 40)
	evidenceHead = strings.Repeat("2", 40)
)

func evidenceArtifactContent(layer string) string {
	return layer + " log SECRET-LOG-LINE HOME=/private/secret\n"
}

func evidenceFixture(t *testing.T, mutate func(*evidenceInventory, string)) string {
	t.Helper()
	root := t.TempDir()
	inv := evidenceInventory{
		Schema: 1,
		Candidate: evidenceInventoryCandidate{
			Repository: "org/repo",
			Base:       evidenceBase,
			Head:       evidenceHead,
		},
		Expected: []evidenceInventoryExpected{
			{Layer: "unit", Platform: "linux", Transport: "herdr"},
			{Layer: "gate", Platform: "linux", Transport: "herdr"},
		},
	}
	for _, layer := range []string{"unit", "gate"} {
		artifact := struct{ path, content string }{
			path:    "artifacts/" + layer + ".json",
			content: evidenceArtifactContent(layer),
		}

		absolute := filepath.Join(root, filepath.FromSlash(artifact.path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(artifact.content), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(artifact.content))
		inv.Receipts = append(inv.Receipts, evidenceInventoryReceipt{
			Layer:     strings.TrimSuffix(filepath.Base(artifact.path), ".json"),
			Platform:  "linux",
			Transport: "herdr",
			Producer:  "PRIVATE-PRODUCER-DO-NOT-EMIT",
			Outcome:   "pass",
			ID:        "PRIVATE-INPUT-ID-DO-NOT-EMIT",
			Since:     "2026-11-24T12:00:00Z",
			Until:     "2026-11-24T12:05:00Z",
			Identity: evidenceInventoryIdentity{
				Repository: "org/repo",
				Base:       evidenceBase,
				Source:     evidenceHead,
			},
			SHA256: hex.EncodeToString(sum[:]),
			Path:   artifact.path,
		})
	}
	if mutate != nil {
		mutate(&inv, root)
	}
	data, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "inventory.json")
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return input
}

func evidenceCLIEnv(t *testing.T, root string) platform.Env {
	t.Helper()
	home, cwd, state, emptyBin := filepath.Join(root, "home"), filepath.Join(root, "caller"), filepath.Join(root, "state"), filepath.Join(root, "bin")
	for _, dir := range []string{home, cwd, state, emptyBin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	return platform.Env{
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"TMPDIR":          filepath.Join(root, "tmp"),
		"PATH":            emptyBin,
		"SOHO_DIR":        state,
		"SOHO_NOWRITE":    "1",
	}
}

func evidenceCLIRun(t *testing.T, env platform.Env, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(args, env)
	return code, out.String(), stderr.String()
}

func evidenceDecode(t *testing.T, out string) evidenceManifest {
	t.Helper()
	var manifest evidenceManifest
	if err := json.Unmarshal([]byte(out), &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v (%q)", err, out)
	}
	return manifest
}

func evidenceRow(t *testing.T, manifest evidenceManifest, layer string) evidenceManifestLayer {
	t.Helper()
	for _, row := range manifest.Layers {
		if row.Layer == layer {
			return row
		}
	}
	t.Fatalf("layer %q absent from manifest: %+v", layer, manifest.Layers)
	return evidenceManifestLayer{}
}

func evidenceAssertSanitized(t *testing.T, out, root string) {
	t.Helper()
	for _, leak := range []string{root, "inventory.json", "artifacts/", "SECRET-LOG-LINE", "HOME=/private/secret", "machine-lab-01", "PRIVATE-PRODUCER-DO-NOT-EMIT", "PRIVATE-INPUT-ID-DO-NOT-EMIT", "private-coordinate-marker", "private-layer-marker", "private-platform-marker", "private-transport-marker", "--label"} {
		if strings.Contains(out, leak) {
			t.Errorf("manifest leaked %q: %s", leak, out)
		}
	}
}

func TestEvidenceCLIStablePassWithoutHerdr(t *testing.T) {
	input := evidenceFixture(t, nil)
	root := filepath.Dir(input)
	env := evidenceCLIEnv(t, root)
	code, first, stderr := evidenceCLIRun(t, env, "evidence", input)
	if code != 0 || stderr != "" {
		t.Fatalf("first run code=%d stderr=%q out=%s", code, stderr, first)
	}
	code, second, stderr := evidenceCLIRun(t, env, "evidence", input)
	if code != 0 || stderr != "" || second != first {
		t.Fatalf("second run code=%d stderr=%q stable=%t out=%s", code, stderr, second == first, second)
	}
	evidenceAssertSanitized(t, first, root)
	manifest := evidenceDecode(t, first)
	if manifest.Schema != 1 || manifest.Result != "pass" || manifest.Unbound != 0 {
		t.Fatalf("unexpected manifest envelope: %+v", manifest)
	}
	if manifest.Candidate.Repository != "org/repo" || manifest.Candidate.Base != evidenceBase || manifest.Candidate.Head != evidenceHead {
		t.Fatalf("candidate changed: %+v", manifest.Candidate)
	}
	if len(manifest.Layers) != 2 || manifest.Layers[0].Layer != "gate" || manifest.Layers[1].Layer != "unit" {
		t.Fatalf("layers are not in stable order: %+v", manifest.Layers)
	}
	digestOf := func(layer string) string {
		sum := sha256.Sum256([]byte(evidenceArtifactContent(layer)))
		return hex.EncodeToString(sum[:])
	}
	want := []evidenceManifestLayer{
		{Layer: "gate", Platform: "linux", Transport: "herdr", Status: "pass", ID: "sha256:" + digestOf("gate"), Source: evidenceHead, Digest: digestOf("gate"), Since: "2026-11-24T12:00:00Z", Until: "2026-11-24T12:05:00Z"},
		{Layer: "unit", Platform: "linux", Transport: "herdr", Status: "pass", ID: "sha256:" + digestOf("unit"), Source: evidenceHead, Digest: digestOf("unit"), Since: "2026-11-24T12:00:00Z", Until: "2026-11-24T12:05:00Z"},
	}
	if !reflect.DeepEqual(manifest.Layers, want) {
		t.Fatalf("layers=%+v want %+v", manifest.Layers, want)
	}
	for _, dir := range []string{filepath.Join(root, "home"), filepath.Join(root, "state")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("%s is not empty after a NOWRITE read: %v", dir, entries)
		}
	}
}

func TestEvidenceCLIClassifiesNonPassingReceipts(t *testing.T) {
	cases := []struct {
		name   string
		target string
		status string
		mutate func(*evidenceInventory, string)
	}{
		{
			name: "stale head", target: "unit", status: "stale",
			mutate: func(inv *evidenceInventory, _ string) { inv.Receipts[0].Identity.Source = strings.Repeat("3", 40) },
		},
		{
			name: "stale base", target: "unit", status: "stale",
			mutate: func(inv *evidenceInventory, _ string) { inv.Receipts[0].Identity.Base = strings.Repeat("4", 40) },
		},
		{
			name: "foreign checkout", target: "unit", status: "stale",
			mutate: func(inv *evidenceInventory, _ string) { inv.Receipts[0].Identity.Repository = "other/repo" },
		},
		{
			name: "digest change", target: "unit", status: "digest_mismatch",
			mutate: func(_ *evidenceInventory, root string) {
				path := filepath.Join(root, "artifacts", "unit.json")
				if err := os.WriteFile(path, []byte("tampered SECRET-LOG-LINE\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missed native check", target: "native", status: "missing",
			mutate: func(inv *evidenceInventory, _ string) {
				inv.Expected = append(inv.Expected, evidenceInventoryExpected{Layer: "native", Platform: "darwin", Transport: "herdr"})
			},
		},
		{
			name: "failed outcome", target: "unit", status: "failed",
			mutate: func(inv *evidenceInventory, _ string) { inv.Receipts[0].Outcome = "failed" },
		},
		{
			name: "blocked outcome", target: "unit", status: "blocked",
			mutate: func(inv *evidenceInventory, _ string) { inv.Receipts[0].Outcome = "blocked" },
		},
		{
			name: "unavailable artifact", target: "unit", status: "unavailable",
			mutate: func(_ *evidenceInventory, root string) {
				if err := os.Remove(filepath.Join(root, "artifacts", "unit.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unbound receipt", target: "unit", status: "unbound",
			mutate: func(inv *evidenceInventory, _ string) { inv.Receipts[0].Identity.Source = "" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := evidenceFixture(t, tc.mutate)
			root := filepath.Dir(input)
			env := evidenceCLIEnv(t, root)
			code, out, stderr := evidenceCLIRun(t, env, "evidence", input)
			if code != 1 || stderr != "" {
				t.Fatalf("code=%d stderr=%q out=%s", code, stderr, out)
			}
			evidenceAssertSanitized(t, out, root)
			manifest := evidenceDecode(t, out)
			if manifest.Result == "pass" {
				t.Fatalf("non-passing manifest reported pass: %s", out)
			}
			row := evidenceRow(t, manifest, tc.target)
			if row.Status != tc.status {
				t.Fatalf("status=%q want %q: %+v", row.Status, tc.status, row)
			}
		})
	}
}

func TestEvidenceCLIRejectsBadInvocation(t *testing.T) {
	input := evidenceFixture(t, nil)
	root := filepath.Dir(input)
	env := evidenceCLIEnv(t, root)
	for _, args := range [][]string{{"evidence"}, {"evidence", "-"}, {"evidence", input, input}} {
		code, out, stderr := evidenceCLIRun(t, env, args...)
		if code != 2 || out != "" || stderr == "" {
			t.Errorf("args=%v code=%d out=%q stderr=%q", args, code, out, stderr)
		}
		if strings.Contains(stderr, root) {
			t.Errorf("usage error leaked the input path: %q", stderr)
		}
	}
}

func TestEvidenceCLITimeRangeContract(t *testing.T) {
	cases := []struct {
		name      string
		since     string
		until     string
		code      int
		status    string
		wantSince string
		wantUntil string
	}{
		{
			name:   "reversed within one second",
			since:  "2026-11-24T12:00:00.900000000Z",
			until:  "2026-11-24T12:00:00.100000000Z",
			code:   1,
			status: "unbound",
		},
		{
			name:      "forward range keeps distinct endpoints",
			since:     "2026-11-24T12:00:00.100000000Z",
			until:     "2026-11-24T12:00:00.900000000Z",
			code:      0,
			status:    "pass",
			wantSince: "2026-11-24T12:00:00.1Z",
			wantUntil: "2026-11-24T12:00:00.9Z",
		},
		{
			name:   "unrepresentable precision",
			since:  "2026-11-24T12:00:00.1234567891Z",
			until:  "2026-11-24T12:00:05Z",
			code:   1,
			status: "unbound",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := evidenceFixture(t, func(inv *evidenceInventory, _ string) {
				inv.Receipts[0].Since = tc.since
				inv.Receipts[0].Until = tc.until
			})
			root := filepath.Dir(input)
			env := evidenceCLIEnv(t, root)
			code, out, stderr := evidenceCLIRun(t, env, "evidence", input)
			if code != tc.code || stderr != "" {
				t.Fatalf("code=%d want %d stderr=%q out=%s", code, tc.code, stderr, out)
			}
			evidenceAssertSanitized(t, out, root)
			manifest := evidenceDecode(t, out)
			row := evidenceRow(t, manifest, "unit")
			if row.Status != tc.status {
				t.Fatalf("status=%q want %q: %+v", row.Status, tc.status, row)
			}
			if tc.wantSince != "" && (row.Since != tc.wantSince || row.Until != tc.wantUntil || row.Since == row.Until) {
				t.Fatalf("range=%q..%q want %q..%q", row.Since, row.Until, tc.wantSince, tc.wantUntil)
			}
		})
	}
}

func TestEvidenceCLIPublicCoordinatesAndID(t *testing.T) {
	input := evidenceFixture(t, nil)
	root := filepath.Dir(input)
	env := evidenceCLIEnv(t, root)
	code, out, stderr := evidenceCLIRun(t, env, "evidence", input)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q out=%s", code, stderr, out)
	}
	evidenceAssertSanitized(t, out, root)
	manifest := evidenceDecode(t, out)
	for _, row := range manifest.Layers {
		if row.ID != "sha256:"+row.Digest {
			t.Fatalf("public id=%q digest=%q: %+v", row.ID, row.Digest, row)
		}
	}
	cases := []struct {
		name   string
		marker string
		mutate func(*evidenceInventory)
	}{
		{name: "repository", marker: "private-coordinate-marker", mutate: func(inv *evidenceInventory) { inv.Candidate.Repository = "org/private-coordinate-marker/repo" }},
		{name: "layer", marker: "private-layer-marker", mutate: func(inv *evidenceInventory) { inv.Expected[0].Layer = "private-layer-marker" }},
		{name: "platform", marker: "private-platform-marker", mutate: func(inv *evidenceInventory) { inv.Expected[0].Platform = "private-platform-marker" }},
		{name: "transport", marker: "private-transport-marker", mutate: func(inv *evidenceInventory) { inv.Expected[0].Transport = "private-transport-marker" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			badInput := evidenceFixture(t, func(inv *evidenceInventory, _ string) { tc.mutate(inv) })
			badRoot := filepath.Dir(badInput)
			badEnv := evidenceCLIEnv(t, badRoot)
			code, badOut, badStderr := evidenceCLIRun(t, badEnv, "evidence", badInput)
			if code != 2 || badOut != "" || badStderr == "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, badOut, badStderr)
			}
			if strings.Contains(badOut, tc.marker) || strings.Contains(badStderr, tc.marker) {
				t.Errorf("marker %q reached stdout or stderr: %q %q", tc.marker, badOut, badStderr)
			}
		})
	}
}

func TestEvidenceCLIRejectsParentSegmentsInOriginalPath(t *testing.T) {
	paths := []string{
		"artifacts/../artifacts/unit.json",
		"./artifacts/unit.json",
		"artifacts//unit.json",
		"C:artifacts/unit.json",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			input := evidenceFixture(t, func(inv *evidenceInventory, _ string) { inv.Receipts[0].Path = path })
			root := filepath.Dir(input)
			env := evidenceCLIEnv(t, root)
			code, out, stderr := evidenceCLIRun(t, env, "evidence", input)
			if code != 1 || stderr != "" {
				t.Fatalf("code=%d stderr=%q out=%s", code, stderr, out)
			}
			evidenceAssertSanitized(t, out, root)
			manifest := evidenceDecode(t, out)
			if row := evidenceRow(t, manifest, "unit"); row.Status != "unbound" {
				t.Fatalf("status=%q want unbound: %+v", row.Status, row)
			}
		})
	}
}
