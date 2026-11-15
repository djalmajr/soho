package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil"
)

// The tests exercise the real package API and the real CLI binary against
// tiny Go fixtures built in t.TempDir using the exact legacy contracts
// (MANIFEST.sha256, brief.md Owned files, hidden probes/). No legacy JS
// fixture is shipped or run, and no production behavior is mocked: the probe
// runs invoke the real go test executable on isolated copies.

var cliBinary string

func TestMain(m *testing.M) {
	stage, err := os.MkdirTemp("", "soho-eval-cli-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli stage:", err)
		os.Exit(1)
	}
	name := "soho-eval"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cliBinary = filepath.Join(stage, name)
	build := exec.Command("go", "build", "-o", cliBinary, filepath.Join("..", "..", "cmd", "soho-eval"))
	build.Env = hermeticEnv(stage)
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "soho-eval build failed: %v\n%s\n", err, out)
		if cerr := os.RemoveAll(stage); cerr != nil {
			fmt.Fprintf(os.Stderr, "cli stage cleanup failed: %v\n", cerr)
		}
		os.Exit(1)
	}
	code := m.Run()
	if cerr := os.RemoveAll(stage); cerr != nil {
		fmt.Fprintf(os.Stderr, "cli stage cleanup failed: %v\n", cerr)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// hermeticEnv points the Go toolchain at an isolated stage (fresh build
// cache, no toolchain downloads) so runs do not depend on shared caches.
func hermeticEnv(stage string) []string {
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !testutil.IsSessionVar(key) {
			env = append(env, item)
		}
	}
	overrides := map[string]string{
		"GOCACHE":     filepath.Join(stage, "gocache"),
		"GOPATH":      filepath.Join(stage, "gopath"),
		"GOMODCACHE":  filepath.Join(stage, "modcache"),
		"GOTOOLCHAIN": "local",
	}
	for i, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if value, ok := overrides[kv[:eq]]; ok {
			env[i] = kv[:eq+1] + value
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

// cliEnv returns a fresh hermetic environment for one CLI subprocess.
func cliEnv(t *testing.T) []string {
	t.Helper()
	return hermeticEnv(t.TempDir())
}

type cliRun struct {
	stdout string
	stderr string
	code   int
}

// runCLI executes the real CLI binary as a subprocess.
func runCLI(t *testing.T, dir string, env []string, args ...string) cliRun {
	t.Helper()
	cmd := exec.Command(cliBinary, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(runErr, &exitErr):
		code = exitErr.ExitCode()
	case runErr != nil:
		t.Fatalf("cli did not run: %v (stderr: %s)", runErr, stderr.String())
	}
	return cliRun{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// --- fixture materials -------------------------------------------------

const briefMD = `# Brief — go add scratch

Role: implementer · Agent: go-eval · Run: frozen-fixture · Report language: en

## Goal

Implement ` + "`Add`" + ` in ` + "`src/add.go`" + `. Return the sum of its arguments.

## Owned files

- ` + "`src/add.go`" + ` — implement the function

## Forbidden

- Do not modify, add, or delete any other path.
`

const referenceAdd = `package src

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`

const brokenAdd = `package src

// Add is the intentionally broken implementation for the negative control.
func Add(a, b int) int {
	return a
}
`

const compileBrokenAdd = `package src

// Add does not compile: missingSymbol is undefined.
func Add(a, b int) int {
	return missingSymbol(a, b)
}
`

// addProbes is the hidden probe file with exactly six top-level tests; the
// fixture directory must be named go-add-fixture so the default module path
// matches the import.
const addProbes = `package probes

import (
	"testing"

	"go-add-fixture/src"
)

func TestAddOneTwo(t *testing.T) {
	if got := src.Add(1, 2); got != 3 {
		t.Fatalf("Add(1, 2) = %d, want 3", got)
	}
}

func TestAddNegative(t *testing.T) {
	if got := src.Add(-1, -2); got != -3 {
		t.Fatalf("Add(-1, -2) = %d, want -3", got)
	}
}

func TestAddZeroZero(t *testing.T) {
	if got := src.Add(0, 0); got != 0 {
		t.Fatalf("Add(0, 0) = %d, want 0", got)
	}
}

func TestAddIdentity(t *testing.T) {
	if got := src.Add(0, 5); got != 5 {
		t.Fatalf("Add(0, 5) = %d, want 5", got)
	}
}

func TestAddCommutative(t *testing.T) {
	if src.Add(1, 2) != src.Add(2, 1) {
		t.Fatal("Add is not commutative")
	}
}

func TestAddBig(t *testing.T) {
	if got := src.Add(1000, 2000); got != 3000 {
		t.Fatalf("Add(1000, 2000) = %d, want 3000", got)
	}
}
`

// buildGoFixture writes a tiny Go fixture with the exact legacy layout:
// MANIFEST.sha256 over brief.md and src/add.go, hidden probes under probes/
// and an ANSWER-KEY.md that must never reach a worker.
func buildGoFixture(t *testing.T, root, name, brief, worker string, probes map[string]string) string {
	t.Helper()
	fixture := filepath.Join(root, name)
	writeTreeFile(t, fixture, "brief.md", brief)
	writeTreeFile(t, fixture, "src/add.go", worker)
	for rel, content := range probes {
		writeTreeFile(t, fixture, "probes/"+rel, content)
	}
	writeTreeFile(t, fixture, "ANSWER-KEY.md", "reviewer-only answer key — never copied\n")
	manifest := ""
	for _, rel := range []string{"brief.md", "src/add.go"} {
		hash, err := sha256File(filepath.Join(fixture, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("manifest hash %s: %v", rel, err)
		}
		manifest += hash + "  " + rel + "\n"
	}
	writeTreeFile(t, fixture, "MANIFEST.sha256", manifest)
	return fixture
}

func writeTreeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy dir: %v", err)
	}
}

// snapshotTree hashes every file below root for worker-byte comparisons.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hash, err := sha256File(path)
		if err != nil {
			return err
		}
		snap[filepath.ToSlash(rel)] = hash
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

type prepareOutput struct {
	Fixture     string `json:"fixture"`
	Destination string `json:"destination"`
	PreparedAt  string `json:"prepared_at"`
	ManifestOK  bool   `json:"manifest_ok"`
}

type probeOutput struct {
	Fixture string `json:"fixture"`
	Probes  struct {
		Passed int      `json:"passed"`
		Total  int      `json:"total"`
		Failed []string `json:"failed"`
	} `json:"probes"`
	ScopeViolations []string `json:"scope_violations"`
	ManifestOK      bool     `json:"manifest_ok"`
}

// --- prepare -------------------------------------------------------------

func TestPrepareRefusesDestinationInsideRepository(t *testing.T) {
	root := repoRoot(t)
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(root, "internal", "eval", ".inside-eval-dest")
	defer os.RemoveAll(dest)

	t.Run("cli with explicit repo from outside the checkout", func(t *testing.T) {
		res := runCLI(t, caller, cliEnv(t), "--repo", root, "prepare", fixture, dest)
		if res.code != 2 {
			t.Fatalf("exit = %d, want 2 (stderr: %s)", res.code, res.stderr)
		}
		if !strings.Contains(res.stderr, "outside the repository") {
			t.Fatalf("stderr = %q, want repository boundary refusal", res.stderr)
		}
		if res.stdout != "" {
			t.Fatalf("stdout = %q, want empty", res.stdout)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("destination state leaked: %v", err)
		}
	})

	t.Run("cli with repo as repo=value form", func(t *testing.T) {
		res := runCLI(t, caller, cliEnv(t), "--repo="+root, "prepare", fixture, dest)
		if res.code != 2 || !strings.Contains(res.stderr, "outside the repository") {
			t.Fatalf("exit = %d stderr = %q, want 2 with repository boundary refusal", res.code, res.stderr)
		}
	})

	t.Run("package with default repo as invoking cwd", func(t *testing.T) {
		t.Chdir(root)
		_, err := Prepare(context.Background(), Options{Fixture: fixture, Destination: dest})
		if err == nil || !strings.Contains(err.Error(), "outside the repository") {
			t.Fatalf("err = %v, want repository boundary refusal", err)
		}
		if _, lerr := os.Lstat(dest); !os.IsNotExist(lerr) {
			t.Fatalf("destination state leaked: %v", lerr)
		}
	})

	t.Run("explicit repo elsewhere is the boundary, not the checkout", func(t *testing.T) {
		elsewhere := t.TempDir()
		allowed := filepath.Join(outside, "worker")
		result, err := Prepare(context.Background(), Options{
			Fixture:     fixture,
			Destination: allowed,
			Repository:  elsewhere,
		})
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		defer os.RemoveAll(allowed)
		if result.Destination != allowed {
			t.Fatalf("destination = %q, want %q", result.Destination, allowed)
		}
	})
}

func TestPrepareCopiesOnlyManifestListedFiles(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker")

	res := runCLI(t, caller, cliEnv(t), "prepare", fixture, dest)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", res.code, res.stderr)
	}
	var out prepareOutput
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("stdout is not one JSON object %q: %v", res.stdout, err)
	}
	if !strings.HasPrefix(res.stdout, `{"fixture":"go-add-fixture","destination":`) {
		t.Fatalf("stdout = %q, want legacy field order", res.stdout)
	}
	if out.Fixture != "go-add-fixture" || out.Destination != dest || !out.ManifestOK {
		t.Fatalf("prepare fields = %+v", out)
	}
	if _, err := time.Parse(time.RFC3339Nano, out.PreparedAt); err != nil {
		t.Fatalf("prepared_at %q is not a parseable timestamp: %v", out.PreparedAt, err)
	}
	if !preparedAtRE.MatchString(res.stdout) {
		t.Fatalf("prepared_at in stdout does not use the legacy toISOString form: %s", res.stdout)
	}

	entries := listDir(t, dest)
	if len(entries) != 3 || entries[0] != ".eval-run.json" || entries[1] != "brief.md" || entries[2] != "src" {
		t.Fatalf("entries = %v, want [.eval-run.json brief.md src]", entries)
	}
	if hasEntry(entries, "probes") || hasEntry(entries, "ANSWER-KEY.md") {
		t.Fatalf("hidden content leaked into the worker copy: %v", entries)
	}

	meta := readRunMetadata(t, dest)
	if meta["fixture"] != "go-add-fixture" || meta["manifest_ok"] != true {
		t.Fatalf("metadata = %v", meta)
	}
	preparedAt, ok := meta["prepared_at"].(string)
	if !ok || !preparedAtValueRE.MatchString(preparedAt) {
		t.Fatalf("metadata prepared_at = %#v, want a toISOString-form string", meta["prepared_at"])
	}

	// Worker bytes match the fixture bytes exactly.
	for _, rel := range []string{"brief.md", "src/add.go"} {
		fixtureHash, _ := sha256File(filepath.Join(fixture, filepath.FromSlash(rel)))
		workerHash, err := sha256File(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil || workerHash != fixtureHash {
			t.Fatalf("%s: worker hash = %s (err %v), want %s", rel, workerHash, err, fixtureHash)
		}
	}
}

var preparedAtValueRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

var preparedAtRE = regexp.MustCompile(`"prepared_at":"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z"`)

func TestPrepareRejectsAdulteratedManifest(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})

	t.Run("altered digest", func(t *testing.T) {
		altered := filepath.Join(outside, "altered-digest")
		copyDir(t, fixture, altered)
		manifestPath := filepath.Join(altered, "MANIFEST.sha256")
		manifest, _ := os.ReadFile(manifestPath)
		manifest = bytes.Replace(manifest, []byte(manifest[:64]), []byte(strings.Repeat("0", 64)), 1)
		if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
			t.Fatalf("alter manifest: %v", err)
		}
		dest := filepath.Join(outside, "digest-worker")
		res := runCLI(t, caller, cliEnv(t), "prepare", altered, dest)
		if res.code != 2 || !strings.Contains(res.stderr, "manifest checksum mismatch") {
			t.Fatalf("exit = %d stderr = %q, want 2 with checksum mismatch", res.code, res.stderr)
		}
		if res.stdout != "" {
			t.Fatalf("stdout = %q, want empty", res.stdout)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("destination state leaked: %v", err)
		}
	})

	t.Run("mutated source", func(t *testing.T) {
		altered := filepath.Join(outside, "altered-source")
		copyDir(t, fixture, altered)
		if err := os.WriteFile(filepath.Join(altered, "src", "add.go"), []byte("package src\n"), 0o644); err != nil {
			t.Fatalf("mutate source: %v", err)
		}
		dest := filepath.Join(outside, "source-worker")
		res := runCLI(t, caller, cliEnv(t), "prepare", altered, dest)
		if res.code != 2 || !strings.Contains(res.stderr, "manifest checksum mismatch") {
			t.Fatalf("exit = %d stderr = %q, want 2 with checksum mismatch", res.code, res.stderr)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("destination state leaked: %v", err)
		}
	})

	t.Run("invalid manifest line", func(t *testing.T) {
		altered := filepath.Join(outside, "altered-line")
		copyDir(t, fixture, altered)
		manifestPath := filepath.Join(altered, "MANIFEST.sha256")
		manifest, _ := os.ReadFile(manifestPath)
		manifest = bytes.Replace(manifest, []byte("  brief.md"), []byte("not-a-hash  brief.md"), 1)
		if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
			t.Fatalf("alter manifest: %v", err)
		}
		res := runCLI(t, caller, cliEnv(t), "prepare", altered, filepath.Join(outside, "line-worker"))
		if res.code != 2 || !strings.Contains(res.stderr, "invalid manifest line") {
			t.Fatalf("exit = %d stderr = %q, want 2 with invalid manifest line", res.code, res.stderr)
		}
	})
}

func TestPrepareRejectsNonfreshDestination(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker")

	first := runCLI(t, caller, cliEnv(t), "prepare", fixture, dest)
	if first.code != 0 {
		t.Fatalf("first prepare exit = %d (stderr: %s)", first.code, first.stderr)
	}
	before := snapshotTree(t, dest)

	second := runCLI(t, caller, cliEnv(t), "prepare", fixture, dest)
	if second.code != 2 || !strings.Contains(second.stderr, "destination already exists") {
		t.Fatalf("second prepare exit = %d stderr = %q, want 2 with already-exists refusal", second.code, second.stderr)
	}
	if got := snapshotTree(t, dest); !equalMaps(got, before) {
		t.Fatalf("existing destination was modified")
	}
}

func TestPrepareRejectsSymlinkedDestination(t *testing.T) {
	root := repoRoot(t)
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	links := filepath.Join(outside, "links")
	if err := os.MkdirAll(links, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("final component is a symlink into the repository", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(root, "evals"), filepath.Join(links, "in-repo")); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(links, "in-repo", ".eval-worker")
		res := runCLI(t, caller, cliEnv(t), "--repo", root, "prepare", fixture, dest)
		if res.code != 2 || !strings.Contains(res.stderr, "outside the repository") {
			t.Fatalf("exit = %d stderr = %q, want 2 with repository boundary refusal", res.code, res.stderr)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("destination state leaked through the symlink: %v", err)
		}
	})

	t.Run("intermediate component is a symlink into the repository", func(t *testing.T) {
		if err := os.Symlink(root, filepath.Join(links, "root-link")); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(links, "root-link", "internal", "eval", ".eval-worker")
		res := runCLI(t, caller, cliEnv(t), "--repo", root, "prepare", fixture, dest)
		if res.code != 2 || !strings.Contains(res.stderr, "outside the repository") {
			t.Fatalf("exit = %d stderr = %q, want 2 with repository boundary refusal", res.code, res.stderr)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("destination state leaked through the symlinked ancestor: %v", err)
		}
	})
}

func TestPrepareRejectsInsideFixture(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(fixture, "worker-copy")
	res := runCLI(t, caller, cliEnv(t), "prepare", fixture, dest)
	if res.code != 2 || !strings.Contains(res.stderr, "must not be inside the fixture") {
		t.Fatalf("exit = %d stderr = %q, want 2 with inside-fixture refusal", res.code, res.stderr)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("destination state leaked inside the fixture: %v", err)
	}
}

func TestPrepareRemovesIncompleteDestinationOnFailure(t *testing.T) {
	t.Run("unreadable source refuses the real CLI", func(t *testing.T) {
		outside := t.TempDir()
		fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
		release := testutil.DenyFileReads(t, filepath.Join(fixture, "src", "add.go"))
		defer release()
		dest := filepath.Join(outside, "worker")
		res := runCLI(t, t.TempDir(), cliEnv(t), "prepare", fixture, dest)
		if res.code != 2 {
			t.Fatalf("exit = %d, want 2 for a failed read (stderr: %s)", res.code, res.stderr)
		}
		if res.stdout != "" {
			t.Fatalf("stdout = %q, want empty", res.stdout)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("refused preparation left a destination: %v", err)
		}
	})
	t.Run("cancellation removes an actually partial copy", func(t *testing.T) {
		outside := t.TempDir()
		fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
		dest := filepath.Join(outside, "worker")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		partialSeen := false
		observed := copyCleanupContext{Context: ctx, observe: func() {
			if raw, err := os.ReadFile(filepath.Join(dest, "brief.md")); err == nil {
				if string(raw) != briefMD {
					t.Fatalf("partial copy contains an unexpected brief: %q", raw)
				}
				partialSeen = true
				cancel()
			}
		}}
		_, err := Prepare(observed, Options{Fixture: fixture, Destination: dest, Repository: t.TempDir()})
		if !partialSeen || !errors.Is(err, context.Canceled) {
			t.Fatalf("partialSeen=%v err=%v; want cancellation after a real file was copied", partialSeen, err)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatalf("incomplete destination was not removed: %v", err)
		}
	})
}

// copyCleanupContext observes filesystem progress at cancellation checks,
// allowing cancellation after a real copy without sleeps or copy mocks.
type copyCleanupContext struct {
	context.Context
	observe func()
}

func (c copyCleanupContext) Err() error {
	c.observe()
	return c.Context.Err()
}

func TestPrepareRejectsMissingParent(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "missing-parent", "worker")
	res := runCLI(t, caller, cliEnv(t), "prepare", fixture, dest)
	if res.code != 2 || !strings.Contains(res.stderr, "destination parent is not a directory") {
		t.Fatalf("exit = %d stderr = %q, want 2 with missing-parent refusal", res.code, res.stderr)
	}
}

func TestPrepareRejectsUnsafeManifestEntries(t *testing.T) {
	t.Run("manifest lists a hidden path", func(t *testing.T) {
		outside := t.TempDir()
		fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
		manifest, _ := os.ReadFile(filepath.Join(fixture, "MANIFEST.sha256"))
		keyHash, _ := sha256File(filepath.Join(fixture, "ANSWER-KEY.md"))
		manifest = append(manifest, []byte(keyHash+"  ANSWER-KEY.md\n")...)
		if err := os.WriteFile(filepath.Join(fixture, "MANIFEST.sha256"), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Prepare(context.Background(), Options{
			Fixture:     fixture,
			Destination: filepath.Join(outside, "worker"),
			Repository:  outside,
		})
		if err == nil || !strings.Contains(err.Error(), "manifest must not include hidden or generated path") {
			t.Fatalf("err = %v, want hidden-path refusal", err)
		}
	})

	t.Run("duplicate manifest path", func(t *testing.T) {
		outside := t.TempDir()
		fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
		manifestPath := filepath.Join(fixture, "MANIFEST.sha256")
		manifest, _ := os.ReadFile(manifestPath)
		lineEnd := bytes.IndexByte(manifest, '\n')
		firstLine := manifest[:lineEnd+1]
		if err := os.WriteFile(manifestPath, append(append([]byte{}, firstLine...), manifest...), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Prepare(context.Background(), Options{
			Fixture:     fixture,
			Destination: filepath.Join(outside, "worker"),
			Repository:  outside,
		})
		if err == nil || !strings.Contains(err.Error(), "duplicate manifest path") {
			t.Fatalf("err = %v, want duplicate-path refusal", err)
		}
	})

	t.Run("manifest path traverses a symlink", func(t *testing.T) {
		outside := t.TempDir()
		fixture := filepath.Join(outside, "go-add-fixture")
		if err := os.MkdirAll(fixture, 0o755); err != nil {
			t.Fatal(err)
		}
		real := filepath.Join(outside, "real-src")
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(real, "add.go"), []byte("package src\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(fixture, "src")); err != nil {
			t.Fatal(err)
		}
		writeTreeFile(t, fixture, "brief.md", briefMD)
		hash, err := sha256File(filepath.Join(real, "add.go"))
		if err != nil {
			t.Fatal(err)
		}
		writeTreeFile(t, fixture, "MANIFEST.sha256", hash+"  src/add.go\n"+sha256Of(briefMD)+"  brief.md\n")
		_, err = Prepare(context.Background(), Options{
			Fixture:     fixture,
			Destination: filepath.Join(outside, "worker"),
			Repository:  outside,
		})
		if err == nil || !strings.Contains(err.Error(), "manifest path traverses a symlink") {
			t.Fatalf("err = %v, want symlink-traversal refusal", err)
		}
	})

	t.Run("unlisted fixture file", func(t *testing.T) {
		outside := t.TempDir()
		fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
		writeTreeFile(t, fixture, "notes.md", "unlisted\n")
		_, err := Prepare(context.Background(), Options{
			Fixture:     fixture,
			Destination: filepath.Join(outside, "worker"),
			Repository:  outside,
		})
		if err == nil || !strings.Contains(err.Error(), "manifest file set mismatch") {
			t.Fatalf("err = %v, want file-set mismatch", err)
		}
	})

	t.Run("fixture contains a symlink", func(t *testing.T) {
		outside := t.TempDir()
		fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
		if err := os.Symlink("brief.md", filepath.Join(fixture, "extra-link")); err != nil {
			t.Fatal(err)
		}
		_, err := Prepare(context.Background(), Options{
			Fixture:     fixture,
			Destination: filepath.Join(outside, "worker"),
			Repository:  outside,
		})
		if err == nil || !strings.Contains(err.Error(), "fixture contains a symlink") {
			t.Fatalf("err = %v, want symlink refusal", err)
		}
	})
}

// --- probes --------------------------------------------------------------

func TestProbesMeasureAllSixOnReference(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)

	res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", res.code, res.stderr)
	}
	var out probeOutput
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("stdout is not one JSON object %q: %v", res.stdout, err)
	}
	if out.Fixture != "go-add-fixture" || !out.ManifestOK {
		t.Fatalf("result = %+v", out)
	}
	if out.Probes.Total != 6 || out.Probes.Passed != 6 {
		t.Fatalf("probes = passed %d / total %d, want 6/6", out.Probes.Passed, out.Probes.Total)
	}
	if len(out.Probes.Failed) != 0 || out.Probes.Failed == nil {
		t.Fatalf("failed = %#v, want an empty array", out.Probes.Failed)
	}
	if len(out.ScopeViolations) != 0 || out.ScopeViolations == nil {
		t.Fatalf("scope_violations = %#v, want an empty array", out.ScopeViolations)
	}
	if !strings.Contains(res.stdout, `"scope_violations":[]`) || !strings.Contains(res.stdout, `"failed":[]`) {
		t.Fatalf("stdout must emit empty arrays, not null: %s", res.stdout)
	}
	if !strings.HasPrefix(res.stdout, `{"fixture":"go-add-fixture","probes":`) {
		t.Fatalf("stdout = %q, want legacy field order", res.stdout)
	}
}

func TestProbesWorkerBytesUnchangedAndScratchRemoved(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)
	before := snapshotTree(t, dest)
	// The prepared destination itself is not part of the measurement; verify
	// the whole tree (including .eval-run.json) stays byte-identical.

	probesRoot := filepath.Join(outside, "scratch")
	if err := os.MkdirAll(probesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Probes(context.Background(), Options{
		Fixture:     fixture,
		Destination: dest,
		Repository:  caller,
		TempRoot:    probesRoot,
	})
	if err != nil {
		t.Fatalf("probes: %v", err)
	}
	if result.Probes.Total != 6 || result.Probes.Passed != 6 {
		t.Fatalf("probes = passed %d / total %d, want 6/6", result.Probes.Passed, result.Probes.Total)
	}
	if got := snapshotTree(t, dest); !equalMaps(got, before) {
		t.Fatalf("worker destination was modified by the probe run")
	}
	entries := listDir(t, probesRoot)
	if len(entries) != 0 {
		t.Fatalf("probe temporary copy was not removed: %v", entries)
	}
}

func TestProbesReportBrokenImplementation(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, brokenAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)

	res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
	if res.code != 0 {
		t.Fatalf("exit = %d, want 0 for a measured failure (stderr: %s)", res.code, res.stderr)
	}
	var out probeOutput
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("stdout is not one JSON object %q: %v", res.stdout, err)
	}
	if out.Probes.Total != 6 {
		t.Fatalf("total = %d, want 6", out.Probes.Total)
	}
	wantFailed := []string{
		"TestAddOneTwo",
		"TestAddNegative",
		"TestAddIdentity",
		"TestAddCommutative",
		"TestAddBig",
	}
	if out.Probes.Passed != 6-len(wantFailed) || !equalStrings(out.Probes.Failed, wantFailed) {
		t.Fatalf("failed = %v (passed %d), want %v (passed 1)", out.Probes.Failed, out.Probes.Passed, wantFailed)
	}
	if len(out.ScopeViolations) != 0 {
		t.Fatalf("scope_violations = %v, want none for an owned-file change", out.ScopeViolations)
	}
}

func TestProbesScopeViolations(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})

	t.Run("changed and added unowned paths", func(t *testing.T) {
		dest := filepath.Join(outside, "worker")
		prepareCLI(t, caller, fixture, dest)
		brief, _ := os.ReadFile(filepath.Join(dest, "brief.md"))
		if err := os.WriteFile(filepath.Join(dest, "brief.md"), append(brief, []byte("\nunauthorized change\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "outside owned.txt"), []byte("unauthorized addition\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
		if res.code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr: %s)", res.code, res.stderr)
		}
		var out probeOutput
		mustJSONProbe(t, res.stdout, &out)
		if !equalStrings(out.ScopeViolations, []string{"brief.md", "outside owned.txt"}) {
			t.Fatalf("scope_violations = %v, want [brief.md outside owned.txt]", out.ScopeViolations)
		}
		if out.Probes.Passed != out.Probes.Total || out.Probes.Total != 6 {
			t.Fatalf("probes = passed %d / total %d, want all passing", out.Probes.Passed, out.Probes.Total)
		}
	})

	t.Run("deleted unowned path", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-deleted")
		prepareCLI(t, caller, fixture, dest)
		if err := os.Remove(filepath.Join(dest, "brief.md")); err != nil {
			t.Fatal(err)
		}
		res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
		if res.code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr: %s)", res.code, res.stderr)
		}
		var out probeOutput
		mustJSONProbe(t, res.stdout, &out)
		if !equalStrings(out.ScopeViolations, []string{"brief.md"}) {
			t.Fatalf("scope_violations = %v, want [brief.md]", out.ScopeViolations)
		}
	})

	t.Run("replaced unowned path with a directory", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-dirt")
		prepareCLI(t, caller, fixture, dest)
		if err := os.Remove(filepath.Join(dest, "brief.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dest, "brief.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
		if res.code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr: %s)", res.code, res.stderr)
		}
		var out probeOutput
		mustJSONProbe(t, res.stdout, &out)
		if !equalStrings(out.ScopeViolations, []string{"brief.md"}) {
			t.Fatalf("scope_violations = %v, want [brief.md] for the type change", out.ScopeViolations)
		}
	})
}

func TestProbesRejectsInvalidUsage(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker")

	for name, args := range map[string][]string{
		"no arguments":         {},
		"missing destination":  {fixture},
		"extra argument":       {"probes", fixture, dest, "extra"},
		"unknown subcommand":   {"frobnicate", fixture, dest},
		"flag without a value": {"--repo"},
		"flag without value2":  {"--repo", "probes", fixture, dest},
	} {
		t.Run(name, func(t *testing.T) {
			res := runCLI(t, caller, nil, args...)
			if res.code != 2 {
				t.Fatalf("exit = %d, want 2", res.code)
			}
			if res.stdout != "" {
				t.Fatalf("stdout = %q, want empty", res.stdout)
			}
			if !strings.Contains(res.stderr, "usage: soho-eval") {
				t.Fatalf("stderr = %q, want a usage line", res.stderr)
			}
		})
	}
}

func TestProbesRejectsMissingOrWrongPrepareMetadata(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})

	t.Run("no metadata file", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-nometa")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Probes(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller})
		if err == nil || !strings.Contains(err.Error(), "missing valid prepare metadata") {
			t.Fatalf("err = %v, want missing-metadata refusal", err)
		}
	})

	t.Run("fixture name mismatch", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-mismatch")
		prepareCLI(t, caller, fixture, dest)
		other := buildGoFixture(t, outside, "other-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
		_, err := Probes(context.Background(), Options{Fixture: other, Destination: dest, Repository: caller})
		if err == nil || !strings.Contains(err.Error(), "missing valid prepare metadata") {
			t.Fatalf("err = %v, want missing-metadata refusal", err)
		}
	})

	t.Run("manifest_ok false", func(t *testing.T) {
		dest := filepath.Join(outside, "worker-badok")
		prepareCLI(t, caller, fixture, dest)
		if err := os.WriteFile(filepath.Join(dest, ".eval-run.json"), []byte(`{"fixture":"go-add-fixture","prepared_at":"2026-01-01T00:00:00.000Z","manifest_ok":false}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Probes(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller})
		if err == nil || !strings.Contains(err.Error(), "missing valid prepare metadata") {
			t.Fatalf("err = %v, want missing-metadata refusal", err)
		}
	})
}

func TestProbesRejectsNonexistentAndInsideDestination(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	root := repoRoot(t)

	res := runCLI(t, caller, cliEnv(t), "--repo", root, "probes", fixture, filepath.Join(outside, "never-prepared"))
	if res.code != 2 || !strings.Contains(res.stderr, "must be an existing directory") {
		t.Fatalf("exit = %d stderr = %q, want 2 with existing-directory refusal", res.code, res.stderr)
	}

	dest := filepath.Join(root, "internal", "eval", ".eval-inside")
	defer os.RemoveAll(dest)
	res = runCLI(t, caller, cliEnv(t), "--repo", root, "probes", fixture, dest)
	if res.code != 2 || !strings.Contains(res.stderr, "outside the repository") {
		t.Fatalf("exit = %d stderr = %q, want 2 with repository boundary refusal", res.code, res.stderr)
	}
}

func TestProbesRejectsUnusableBriefOwnedSection(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	probes := map[string]string{"add_test.go": addProbes}

	t.Run("missing Owned files section", func(t *testing.T) {
		fixture := buildGoFixture(t, outside, "go-add-fixture", "# Brief\n\n## Goal\n\nNo owned section here.\n", referenceAdd, probes)
		dest := filepath.Join(outside, "worker-noown")
		prepareCLI(t, caller, fixture, dest)
		_, err := Probes(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller})
		if err == nil || !strings.Contains(err.Error(), "missing an Owned files section") {
			t.Fatalf("err = %v, want missing-section refusal", err)
		}
	})

	t.Run("empty Owned files section", func(t *testing.T) {
		brief := "# Brief\n\n## Owned files\n\n## Forbidden\n\nNothing.\n"
		fixture := buildGoFixture(t, outside, "go-add-fixture", brief, referenceAdd, probes)
		dest := filepath.Join(outside, "worker-emptyown")
		prepareCLI(t, caller, fixture, dest)
		_, err := Probes(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller})
		if err == nil || !strings.Contains(err.Error(), "Owned files section is empty") {
			t.Fatalf("err = %v, want empty-section refusal", err)
		}
	})

	t.Run("invalid Owned files entry", func(t *testing.T) {
		brief := "# Brief\n\n## Owned files\n\n- `../escape.go` — escape attempt\n"
		fixture := buildGoFixture(t, outside, "go-add-fixture", brief, referenceAdd, probes)
		dest := filepath.Join(outside, "worker-badown")
		prepareCLI(t, caller, fixture, dest)
		_, err := Probes(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller})
		if err == nil || !strings.Contains(err.Error(), "invalid Owned files entry") {
			t.Fatalf("err = %v, want invalid-entry refusal", err)
		}
	})
}

func TestProbesRejectsSymlinkedProbeTree(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	if err := os.Symlink("add_test.go", filepath.Join(fixture, "probes", "link_test.go")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)
	_, err := Probes(context.Background(), Options{
		Fixture:     fixture,
		Destination: dest,
		Repository:  caller,
	})
	if err == nil || !strings.Contains(err.Error(), "probe tree contains a symlink") {
		t.Fatalf("err = %v, want probe symlink refusal", err)
	}
}

func TestProbesWithoutProbeFiles(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"not-a-test.txt": "no probes here\n"})
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)
	_, err := Probes(context.Background(), Options{
		Fixture:     fixture,
		Destination: dest,
		Repository:  caller,
	})
	if err == nil || !strings.Contains(err.Error(), "no hidden *_test.go probes") {
		t.Fatalf("err = %v, want no-probes refusal", err)
	}
}

func TestProbesCompilerErrorIsUnmeasurable(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, compileBrokenAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)

	res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2 for an unmeasurable run (stdout: %s stderr: %s)", res.code, res.stdout, res.stderr)
	}
	if res.stdout != "" {
		t.Fatalf("stdout = %q, want empty for an unmeasurable run", res.stdout)
	}
	if !strings.Contains(res.stderr, "did not return complete results") {
		t.Fatalf("stderr = %q, want a complete-results refusal", res.stderr)
	}
	if !strings.Contains(res.stderr, "undefined: missingSymbol") {
		t.Fatalf("stderr = %q, want the compiler diagnostic", res.stderr)
	}
}

func TestProbesNoResultIsUnmeasurable(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"noop_test.go": "package probes\n"})
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)

	res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2 for a no-result run (stdout: %s stderr: %s)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "did not return complete results (status 0)") {
		t.Fatalf("stderr = %q, want a no-result refusal with the observed status", res.stderr)
	}
}

func TestProbesTimeoutIsUnmeasurable(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	slowProbe := `package probes

import (
	"testing"
	"time"
)

func TestSlowProbe(t *testing.T) {
	time.Sleep(40 * time.Second)
}
`
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"slow_test.go": slowProbe})
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)

	start := time.Now()
	res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
	elapsed := time.Since(start)
	if res.code != 2 {
		t.Fatalf("exit = %d, want 2 for a timed-out run (stdout: %s stderr: %s)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "did not return complete results") {
		t.Fatalf("stderr = %q, want a complete-results refusal", res.stderr)
	}
	if elapsed < 25*time.Second {
		t.Fatalf("elapsed = %s, want at least ~30s so the -timeout deadline actually fired", elapsed)
	}
}

func TestProbesPreserveQuotedDestinationPath(t *testing.T) {
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker copy 'quoted'")

	prepare := runCLI(t, caller, cliEnv(t), "prepare", fixture, dest)
	if prepare.code != 0 {
		t.Fatalf("prepare exit = %d (stderr: %s)", prepare.code, prepare.stderr)
	}
	res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
	if res.code != 0 {
		t.Fatalf("probes exit = %d (stderr: %s)", res.code, res.stderr)
	}
	var out probeOutput
	mustJSONProbe(t, res.stdout, &out)
	if out.Probes.Passed != 6 || out.Probes.Total != 6 {
		t.Fatalf("probes = passed %d / total %d, want 6/6", out.Probes.Passed, out.Probes.Total)
	}
}

func TestProbesUseTrustedGoExecutable(t *testing.T) {
	resolved, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go not on PATH: %v", err)
	}
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := buildGoFixture(t, outside, "go-add-fixture", briefMD, referenceAdd, map[string]string{"add_test.go": addProbes})
	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)

	result, err := Probes(context.Background(), Options{
		Fixture:      fixture,
		Destination:  dest,
		Repository:   caller,
		GoExecutable: resolved,
	})
	if err != nil {
		t.Fatalf("probes: %v", err)
	}
	if result.Probes.Passed != 6 || result.Probes.Total != 6 {
		t.Fatalf("probes = passed %d / total %d, want 6/6", result.Probes.Passed, result.Probes.Total)
	}
}

func TestProbesFixtureSuppliesGoMod(t *testing.T) {
	// A fixture that supplies its own go.mod through the manifest keeps the
	// worker-provided module path instead of the generated one.
	outside := t.TempDir()
	caller := t.TempDir()
	fixture := filepath.Join(outside, "gomod-fixture")
	writeTreeFile(t, fixture, "brief.md", briefMD)
	writeTreeFile(t, fixture, "src/add.go", referenceAdd)
	writeTreeFile(t, fixture, "go.mod", "module custom-module\n\ngo 1.25\n")
	writeTreeFile(t, fixture, "probes/add_test.go", `package probes

import (
	"testing"

	"custom-module/src"
)

func TestCustomModule(t *testing.T) {
	if got := src.Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}
`)
	writeTreeFile(t, fixture, "ANSWER-KEY.md", "reviewer-only answer key — never copied\n")
	manifest := ""
	for _, rel := range []string{"brief.md", "go.mod", "src/add.go"} {
		hash, err := sha256File(filepath.Join(fixture, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		manifest += hash + "  " + rel + "\n"
	}
	writeTreeFile(t, fixture, "MANIFEST.sha256", manifest)

	dest := filepath.Join(outside, "worker")
	prepareCLI(t, caller, fixture, dest)
	res := runCLI(t, caller, cliEnv(t), "probes", fixture, dest)
	if res.code != 0 {
		t.Fatalf("probes exit = %d (stderr: %s)", res.code, res.stderr)
	}
	var out probeOutput
	mustJSONProbe(t, res.stdout, &out)
	if out.Probes.Passed != 1 || out.Probes.Total != 1 {
		t.Fatalf("probes = passed %d / total %d, want 1/1 with the fixture-supplied module", out.Probes.Passed, out.Probes.Total)
	}
}

// --- small helpers -------------------------------------------------------

func prepareCLI(t *testing.T, dir, fixture, dest string) {
	t.Helper()
	res := runCLI(t, dir, cliEnv(t), "prepare", fixture, dest)
	if res.code != 0 {
		t.Fatalf("prepare exit = %d (stderr: %s)", res.code, res.stderr)
	}
}

func mustJSONProbe(t *testing.T, raw string, out *probeOutput) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		t.Fatalf("stdout is not one JSON object %q: %v", raw, err)
	}
}

func readRunMetadata(t *testing.T, dest string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dest, ".eval-run.json"))
	if err != nil {
		t.Fatalf("read .eval-run.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf(".eval-run.json is not an object: %v", err)
	}
	return meta
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func hasEntry(names []string, name string) bool {
	for _, entry := range names {
		if entry == name {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func sha256Of(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
