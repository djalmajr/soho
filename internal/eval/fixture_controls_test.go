package eval

// Fixture-control integration for the real distributed implementer-prune
// fixture: the actual fixture is prepared through the real Prepare and
// measured through the real Probes with the durable Go controls stored as
// data under evals/test/fixtures. Exactly four six-probe runs happen (stub,
// reference, duplicate mutant, unrelated mutant); each control's observable
// file mutations are additionally proven directly through a bounded scratch
// "go run" module, so no banned engine is involved and the native Go
// toolchain is the only executor.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil"
)

// moduleRootForTest resolves the repository root relative to this package.
func moduleRootForTest(t *testing.T) string {
	t.Helper()
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(filepath.Join(packageDir, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(abs, "go.mod"))
	fields := strings.Fields(string(module))
	if err != nil || len(fields) < 2 || fields[0] != "module" || fields[1] != "github.com/djalmajr/soho" {
		t.Fatalf("run this test binary from its repository internal/eval directory: %v", err)
	}
	return abs
}

// fixtureRootForTest resolves the real distributed fixture.
func fixtureRootForTest(t *testing.T) string {
	root := filepath.Join(moduleRootForTest(t), "evals", "fixtures", "implementer-prune")
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("real fixture missing at %s: %v", root, err)
	}
	return root
}

// controlSourceForTest reads one durable control .go.txt beside the repo
// evals tree.
func controlSourceForTest(t *testing.T, name string) string {
	raw, err := os.ReadFile(filepath.Join(moduleRootForTest(t), "evals", "test", "fixtures", name))
	if err != nil {
		t.Fatalf("read control %s: %v", name, err)
	}
	return string(raw)
}

// goExecutableForTest trusts the explicit native path (EVAL_GO_BIN) or the
// actually resolved absolute go, never a bare lookup name.
func goExecutableForTest(t *testing.T) string {
	bin := os.Getenv("EVAL_GO_BIN")
	if bin == "" {
		resolved, err := exec.LookPath("go")
		if err != nil {
			t.Fatal("set EVAL_GO_BIN to an absolute go path")
		}
		bin = resolved
	}
	if !filepath.IsAbs(bin) {
		t.Fatalf("EVAL_GO_BIN must be an absolute path, got %q", bin)
	}
	info, err := os.Stat(bin)
	if err != nil || info.IsDir() {
		t.Fatalf("go executable %s missing or not a file: %v", bin, err)
	}
	return bin
}

// workerHashesForTest hashes every worker file (relative path -> sha256),
// skipping the generated .eval-run.json like workerFiles does.
func workerHashesForTest(t *testing.T, dest string) map[string]string {
	entries, err := workerFiles(dest)
	if err != nil {
		t.Fatalf("list worker %s: %v", dest, err)
	}
	hashes := map[string]string{}
	for _, entry := range entries {
		sum, err := sha256File(filepath.Join(dest, filepath.FromSlash(entry.Relative)))
		if err != nil {
			t.Fatalf("hash worker file %s: %v", entry.Relative, err)
		}
		hashes[entry.Relative] = sum
	}
	return hashes
}

// controlMainSource is the scratch runner used to observe each control's
// file mutations directly: it writes one scenario, calls PruneBackups once
// and prints a JSON report of the resulting directory.
const controlMainSource = `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"prune-control/src"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: prune-control <duplicate|unrelated> <data-dir>")
		os.Exit(2)
	}
	mode, dir := os.Args[1], os.Args[2]
	now := time.Date(2026, 9, 27, 21, 19, 50, 0, time.UTC)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			panic(err)
		}
	}
	switch mode {
	case "duplicate":
		write("backup-0000000000000001-20260101T000000000Z.json", "opaque 1")
		write("backup-0000000000000001-20260102T000000000Z.json", "opaque 1")
	case "unrelated":
		write("backup-0000000000000001-20260101T000000000Z.json", "opaque 1")
		write("backup-0000000000000002-20260101T000000000Z.json", "opaque 2")
		write("backup-final.json", "leave")
		write("backup-0000000000000003-20260101T000000000Z.json.tmp", "leave")
		if err := os.Mkdir(filepath.Join(dir, "backup-0000000000000004-20260101T000000000Z.json"), 0o755); err != nil {
			panic(err)
		}
		write("outside", "leave")
		if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(dir, "backup-0000000000000005-20260101T000000000Z.json")); err != nil {
			panic(err)
		}
	default:
		panic("unknown mode " + mode)
	}
	removed, err := src.PruneBackups(src.Options{Dir: dir, Keep: 1, Now: now})
	out := map[string]any{"removed": removed}
	if err != nil {
		out["error"] = err.Error()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		panic(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		suffix := ""
		if entry.Type()&os.ModeSymlink != 0 {
			suffix = "->symlink"
		} else if entry.IsDir() {
			suffix = "+dir"
		}
		names = append(names, entry.Name()+suffix)
	}
	sort.Strings(names)
	out["entries"] = names
	data, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(data))
}
`

// TestFixtureControlsPrune prepares the real distributed fixture through
// the real Prepare and measures it through the real Probes with the durable
// Go controls: stub 1/6, reference 6/6, each mutant 5/6 with its own failed
// probe, scope clean for the owned replacement, the out-of-scope change
// detected, worker bytes unchanged after each Probes run and the owned
// scratch removed.
func TestFixtureControlsPrune(t *testing.T) {
	goBin := goExecutableForTest(t)
	// The gate constructs its own transient restricted Go-only PATH and
	// passes it (with the explicit trusted GOROOT and the cleared GOENV /
	// GOWORK) to the probe and control children through the test process
	// environment; the caller's normal PATH may contain node, bun and bash.
	restricted := restrictedGoPathForTest(t, goBin)
	goRoot := testutil.HostGoRoot()
	if goRoot == "" {
		t.Fatal("the restricted toolchain GOROOT is not installed in the test environment")
	}
	controlCache := t.TempDir()
	fixture := fixtureRootForTest(t)
	reference := controlSourceForTest(t, "reference-prune.go.txt")
	duplicate := controlSourceForTest(t, "mutation-duplicate-prune.go.txt")
	unrelated := controlSourceForTest(t, "mutation-unrelated-prune.go.txt")

	// The distributed stub ships the unfinished implementation only: the
	// reference and the hidden answers never live in the worker source.
	stub, err := os.ReadFile(filepath.Join(fixture, "src", "prune.go"))
	if err != nil {
		t.Fatalf("read distributed stub: %v", err)
	}
	if !strings.Contains(string(stub), "not implemented") {
		t.Fatal("distributed stub no longer returns the unfinished error")
	}
	for _, leak := range []string{"collectBackups", "duplicate backup sequence", "validateKeep"} {
		if strings.Contains(string(stub), leak) {
			t.Fatalf("distributed stub contains reference material %q", leak)
		}
	}

	outside := t.TempDir()
	caller := t.TempDir()

	measure := func(t *testing.T, dest string) ProbeResult {
		tempRoot := t.TempDir()
		result, err := Probes(context.Background(), Options{
			Fixture:      fixture,
			Destination:  dest,
			Repository:   caller,
			TempRoot:     tempRoot,
			GoExecutable: goBin,
		})
		if err != nil {
			t.Fatalf("probes: %v", err)
		}
		entries, err := os.ReadDir(tempRoot)
		if err != nil || len(entries) != 0 {
			t.Fatalf("owned work not removed: %d entries left in %s: %v", len(entries), tempRoot, err)
		}
		return result
	}

	setWorkerSource := func(t *testing.T, dest, content string) {
		if err := os.WriteFile(filepath.Join(dest, "src", "prune.go"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	assertCleanWorker := func(t *testing.T, dest, phase string) {
		entries, err := workerFiles(dest)
		if err != nil {
			t.Fatalf("list worker %s: %v", dest, err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Relative)
		}
		sort.Strings(names)
		if !equalStrings(names, []string{"brief.md", "go.mod", "src/prune.go"}) {
			t.Fatalf("%s: worker files %v, want the manifest set only (no hidden probes, no leaked answer)", phase, names)
		}
	}

	t.Run("stub measures 1 of 6 with the five success probes failing", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-stub")
		if _, err := Prepare(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller}); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		assertCleanWorker(t, dest, "stub prepare")
		if got := sha256FileSum(t, filepath.Join(dest, "src", "prune.go")); got != sha256FileSum(t, filepath.Join(fixture, "src", "prune.go")) {
			t.Fatal("prepared worker src/prune.go differs from the distributed stub")
		}
		before := workerHashesForTest(t, dest)
		result := measure(t, dest)
		if !equalMaps(workerHashesForTest(t, dest), before) {
			t.Fatal("worker bytes changed by the probes run")
		}
		if result.Fixture != "implementer-prune" || !result.ManifestOK {
			t.Fatalf("result = %+v, want fixture implementer-prune with manifest ok", result)
		}
		if len(result.ScopeViolations) != 0 {
			t.Fatalf("scope_violations = %v, want none", result.ScopeViolations)
		}
		if result.Probes.Total != 6 || result.Probes.Passed != 1 {
			t.Fatalf("probes = passed %d / total %d, want 1/6", result.Probes.Passed, result.Probes.Total)
		}
		if !equalStrings(result.Probes.Failed, []string{
			"TestKeepsHighestSequencesNotLatestTimestampOrMtime",
			"TestCrashAfterDeletingOlderFilesRetriesWithoutLosingNewest",
			"TestPublishedBackupSurvivesPublishBeforePruneBoundaryAndRetryConverges",
			"TestClockRollbackCannotChangeSequenceOrdering",
			"TestIgnoresMalformedNamesTemporaryFilesDirectoriesAndSymlinks",
		}) {
			t.Fatalf("failed = %v, want the five success-expecting probes in declaration order", result.Probes.Failed)
		}
	})

	t.Run("reference owned replacement measures 6 of 6 with scope unchanged", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-ref")
		if _, err := Prepare(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller}); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		setWorkerSource(t, dest, reference)
		want := sha256BytesHex(t, []byte(reference))
		if got := sha256FileSum(t, filepath.Join(dest, "src", "prune.go")); got != want {
			t.Fatalf("worker src/prune.go hash %s, want the reference control bytes %s", got, want)
		}
		assertCleanWorker(t, dest, "reference prepare")
		before := workerHashesForTest(t, dest)
		result := measure(t, dest)
		if !equalMaps(workerHashesForTest(t, dest), before) {
			t.Fatal("worker bytes changed by the probes run")
		}
		if len(result.ScopeViolations) != 0 {
			t.Fatalf("scope_violations = %v, want none for the owned replacement", result.ScopeViolations)
		}
		if result.Probes.Total != 6 || result.Probes.Passed != 6 || len(result.Probes.Failed) != 0 {
			t.Fatalf("probes = %+v, want 6/6 with no failures", result.Probes)
		}
	})

	t.Run("duplicate mutant measures 5 of 6 with the duplicate probe failing", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-duplicate")
		if _, err := Prepare(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller}); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		setWorkerSource(t, dest, duplicate)
		assertCleanWorker(t, dest, "duplicate prepare")
		before := workerHashesForTest(t, dest)
		result := measure(t, dest)
		if !equalMaps(workerHashesForTest(t, dest), before) {
			t.Fatal("worker bytes changed by the probes run")
		}
		if len(result.ScopeViolations) != 0 {
			t.Fatalf("scope_violations = %v, want none for the owned replacement", result.ScopeViolations)
		}
		if result.Probes.Total != 6 || result.Probes.Passed != 5 {
			t.Fatalf("probes = passed %d / total %d, want 5/6", result.Probes.Passed, result.Probes.Total)
		}
		if !equalStrings(result.Probes.Failed, []string{"TestInvalidArgumentsAndDuplicateSequenceFailBeforeMutation"}) {
			t.Fatalf("failed = %v, want only the duplicate-sequence probe", result.Probes.Failed)
		}
	})

	t.Run("unrelated mutant measures 5 of 6 and the out-of-scope change is detected", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-unrelated")
		if _, err := Prepare(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller}); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		setWorkerSource(t, dest, unrelated)
		brief, err := os.ReadFile(filepath.Join(dest, "brief.md"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "brief.md"), append(brief, []byte("\nunauthorized change\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
		assertCleanWorker(t, dest, "unrelated prepare")
		before := workerHashesForTest(t, dest)
		result := measure(t, dest)
		if !equalMaps(workerHashesForTest(t, dest), before) {
			t.Fatal("worker bytes changed by the probes run")
		}
		if !equalStrings(result.ScopeViolations, []string{"brief.md"}) {
			t.Fatalf("scope_violations = %v, want [brief.md] (the out-of-scope change)", result.ScopeViolations)
		}
		if result.Probes.Total != 6 || result.Probes.Passed != 5 {
			t.Fatalf("probes = passed %d / total %d, want 5/6", result.Probes.Passed, result.Probes.Total)
		}
		if !equalStrings(result.Probes.Failed, []string{"TestIgnoresMalformedNamesTemporaryFilesDirectoriesAndSymlinks"}) {
			t.Fatalf("failed = %v, want only the unrelated-entries probe", result.Probes.Failed)
		}
	})

	t.Run("controls observed directly on a bounded scratch run", func(t *testing.T) {
		scratch := t.TempDir()
		if err := os.MkdirAll(filepath.Join(scratch, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(scratch, "go.mod"), []byte("module prune-control\n\ngo 1.25\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(scratch, "main.go"), []byte(controlMainSource), 0o644); err != nil {
			t.Fatal(err)
		}

		runControl := func(t *testing.T, name, control, mode string) (map[string]any, string) {
			if err := os.WriteFile(filepath.Join(scratch, "src", "prune.go"), []byte(control), 0o644); err != nil {
				t.Fatal(err)
			}
			dataDir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, goBin, "run", ".", mode, dataDir)
			cmd.Dir = scratch
			// The restricted hermetic environment: the private PATH resolves
			// only the go alias, the explicit GOROOT survives relocation, and
			// the cleared GOENV/GOWORK/GOFLAGS with the isolated GOCACHE keep
			// the caller workspace and settings out of the scratch module.
			cmd.Env = childToolchainEnv(restricted, goRoot, controlCache)
			cmd.WaitDelay = 5 * time.Second
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("go run %s %s: %v (output %s)", name, mode, err, string(out))
			}
			var report map[string]any
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatalf("go run %s %s: report %q does not parse: %v", name, mode, string(out), err)
			}
			return report, dataDir
		}

		report, _ := runControl(t, "reference", reference, "duplicate")
		if errText, _ := report["error"].(string); !strings.Contains(errText, "duplicate backup sequence") {
			t.Fatalf("reference duplicate: error = %q, want the duplicate refusal", errText)
		}
		if !equalStrings(stringSlice(report), []string{
			"backup-0000000000000001-20260101T000000000Z.json",
			"backup-0000000000000001-20260102T000000000Z.json",
		}) {
			t.Fatalf("reference duplicate: entries %v, want both files retained (refusal before mutation)", stringSlice(report))
		}

		report, _ = runControl(t, "duplicate", duplicate, "duplicate")
		if _, present := report["error"]; present {
			t.Fatalf("duplicate mutant: error = %v, want success (the guard is the mutation)", report["error"])
		}
		if removed, _ := report["removed"].(float64); removed != 1 {
			t.Fatalf("duplicate mutant: removed = %v, want 1", report["removed"])
		}
		if !equalStrings(stringSlice(report), []string{
			"backup-0000000000000001-20260102T000000000Z.json",
		}) {
			t.Fatalf("duplicate mutant: entries %v, want only the later duplicate (the mutation happened)", stringSlice(report))
		}

		report, _ = runControl(t, "reference", reference, "unrelated")
		if removed, _ := report["removed"].(float64); removed != 1 {
			t.Fatalf("reference unrelated: removed = %v, want 1", report["removed"])
		}
		if !equalStrings(stringSlice(report), []string{
			"backup-0000000000000002-20260101T000000000Z.json",
			"backup-0000000000000003-20260101T000000000Z.json.tmp",
			"backup-0000000000000004-20260101T000000000Z.json+dir",
			"backup-0000000000000005-20260101T000000000Z.json->symlink",
			"backup-final.json",
			"outside",
		}) {
			t.Fatalf("reference unrelated: entries %v, want the unrelated entries untouched", stringSlice(report))
		}

		report, dataDir := runControl(t, "unrelated", unrelated, "unrelated")
		if removed, _ := report["removed"].(float64); removed != 1 {
			t.Fatalf("unrelated mutant: removed = %v, want 1", report["removed"])
		}
		if !equalStrings(stringSlice(report), []string{
			"backup-0000000000000002-20260101T000000000Z.json",
			"backup-0000000000000004-20260101T000000000Z.json+dir",
			"backup-final.json",
			"outside",
		}) {
			t.Fatalf("unrelated mutant: entries %v, want the .tmp file and the symlink removed", stringSlice(report))
		}
		content, err := os.ReadFile(filepath.Join(dataDir, "outside"))
		if err != nil || string(content) != "leave" {
			t.Fatalf("unrelated mutant: outside target = %q (err %v), want leave (only the link is removed)", content, err)
		}
	})
}

// stringSlice extracts the report entries list in order.
func stringSlice(report map[string]any) []string {
	raw, _ := report["entries"].([]any)
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		out = append(out, value.(string))
	}
	return out
}

// sha256FileSum is a small wrapper so the comparisons above stay readable.
func sha256FileSum(t *testing.T, path string) string {
	t.Helper()
	sum, err := sha256File(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return sum
}

// sha256BytesHex hashes raw bytes for the control byte comparisons.
func sha256BytesHex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
