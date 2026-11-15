package eval

// Durable consumer for the real seeded reviewer fixture: the actual Prepare
// delivers exactly the four manifest worker files with the exact verified
// bytes and never the hidden ANSWER-KEY; the actual change.diff applies
// cleanly to base/transfer.go through a scoped in-process unified-diff
// consumer (byte-exact verification, no shell, no external engine); and the
// base and patched modules both compile and run their bounded scenario
// tests on the native Go toolchain. The patched run proves the three seeded
// defects (inverted ownership, zero acceptance, rounded credit) with
// discriminating scenarios, and the diff's added post-change lines are
// asserted to land exactly on the answer-key lines 53/56/60.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

// verifiedReviewerFileHashes are the durable verified byte hashes of the
// four worker files the fixture must deliver unchanged (the hidden
// ANSWER-KEY is deliberately absent from the set).
var verifiedReviewerFileHashes = map[string]string{
	"base/transfer.go": "aa8e1ffda076104365e63666f8ae274cceab4f00161dbfb888ccb6971d1038fa",
	"brief.md":         "61a99e53a3fdc2aa17b7f7a022d14af9c9062775b6a73aef54b8aedc71f97c11",
	"change.diff":      "52c9da05c6e6e53563bda51329e817b66bb43ea576e350adb2480b050fe6a267",
	"go.mod":           "46df620172bb26608e7ec4c14cb2d4a79b5ce18b458497cfcd2e45d724a1078b",
}

// verifiedReviewerAnswerKeyHash is the durable verified hash of the hidden
// answer key that must stay in the fixture root and never be delivered.
const verifiedReviewerAnswerKeyHash = "7662c139e1f72650f67a843cdb8bca4d4494524142467bd85dbdae1b1f50d77c"

// bannedEngines are the engines that must not resolve in the restricted Go
// PATH directories this consumer builds.
var bannedEngines = []string{"node", "bun", "bash", "sh"}

// reviewerFixtureRootForTest resolves the real seeded reviewer fixture.
func reviewerFixtureRootForTest(t *testing.T) string {
	t.Helper()
	root := filepath.Join(moduleRootForTest(t), "evals", "fixtures", "reviewer-seeded")
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("reviewer fixture missing at %s: %v", root, err)
	}
	return root
}

// engineResolves reports whether the bare name resolves to an executable
// file inside any directory of the given PATH value, using the host
// executable extensions (Windows PATHEXT included). It never executes
// anything, and on Windows bare-file absence is not equated with
// non-resolution: a node.exe or node.cmd under the PATH still resolves.
func engineResolves(pathValue, engine string) (string, bool) {
	env := platform.EnvFromOS()
	env["PATH"] = pathValue
	return platform.FindExecutable(engine, env, platform.Current())
}

// assertBannedEnginesUnresolvable proves with host executable resolution
// (not mere bare-file absence) that no banned engine name resolves under
// the given restricted PATH value: only the native go tool may resolve
// there.
func assertBannedEnginesUnresolvable(t *testing.T, pathValue string) {
	t.Helper()
	for _, engine := range bannedEngines {
		if candidate, resolved := engineResolves(pathValue, engine); resolved {
			t.Fatalf("restricted PATH %q resolves the banned engine %s at %s; only the native go tool may resolve there", pathValue, engine, candidate)
		}
	}
}

// nativeGoAliasName is the platform executable name the private PATH alias
// must carry for the host resolution to find it: go.exe on Windows, go
// elsewhere.
func nativeGoAliasName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

// installGoAlias installs the trusted native go executable into the
// private directory under the platform alias name: os.Link first on
// Unix (no symlink privilege, no Developer Mode), and a verified
// byte-identical executable copy when the hard link is unavailable. On
// Windows the alias is always the verified byte copy: a hard link to
// the running go tool image can block removal of the fixture directory
// entry on the Windows Server CI hosts (the mapped image pins it) while
// go test is active, breaking the TempDir cleanup. linkFunc is the
// hard-link operation (os.Link in production; the failing seam in the
// helper control that forces the copy fallback).
func installGoAlias(t *testing.T, priv, goBin string, linkFunc func(src, dst string) error) string {
	t.Helper()
	alias := filepath.Join(priv, nativeGoAliasName())
	if runtime.GOOS == "windows" {
		return copyGoAlias(t, priv, goBin)
	}
	if err := linkFunc(goBin, alias); err == nil {
		return alias
	}
	return copyGoAlias(t, priv, goBin)
}

// copyGoAlias copies the native executable bytes into the alias path with
// executable permissions and verifies the result is byte-identical and
// executable; no giant copied toolchain, only the executable itself.
func copyGoAlias(t *testing.T, priv, goBin string) string {
	t.Helper()
	alias := filepath.Join(priv, nativeGoAliasName())
	data, err := os.ReadFile(goBin)
	if err != nil {
		t.Fatalf("read native go %s: %v", goBin, err)
	}
	if err := os.WriteFile(alias, data, 0o755); err != nil {
		t.Fatalf("copy native go bytes into %s: %v", alias, err)
	}
	copied, err := os.ReadFile(alias)
	if err != nil {
		t.Fatalf("read back alias %s: %v", alias, err)
	}
	if !bytes.Equal(data, copied) {
		t.Fatalf("alias %s is not byte-identical to %s after the copy", alias, goBin)
	}
	info, err := os.Stat(alias)
	if err != nil {
		t.Fatalf("stat alias %s: %v", alias, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("alias %s permissions %v lack the executable bit", alias, info.Mode().Perm())
	}
	return alias
}

// restrictedGoPathDir builds the transient restricted Go-only PATH: a
// private directory holding only the native go alias (hard link on Unix,
// the verified byte copy on Windows or when the hard link is
// unavailable), proven free of every banned engine by host resolution.
func restrictedGoPathDir(t *testing.T, goBin string) string {
	t.Helper()
	priv := t.TempDir()
	installGoAlias(t, priv, goBin, os.Link)
	assertBannedEnginesUnresolvable(t, priv)
	return priv
}

// trustedGoRootForTest determines the trusted toolchain GOROOT from the
// original native executable with a bounded direct go env GOROOT and
// validates the nonempty actual toolchain directory. A relocated alias may
// lose its embedded GOROOT, so every child receives the root explicitly
// instead of being inferred from a successful relocated run.
func trustedGoRootForTest(t *testing.T, goBin string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "env", "GOROOT")
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go env GOROOT from %s: %v (output %s)", goBin, err, string(out))
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatalf("go env GOROOT returned an empty toolchain root")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("trusted toolchain root %s is not a directory: %v", root, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		t.Fatalf("trusted toolchain root %s is missing or empty: %v", root, err)
	}
	return root
}

// overriddenToolchainKey are the environment keys the restricted children
// environment replaces; the comparison is case-insensitive (Windows
// environment key spelling) so an overridden key never survives in the
// caller's spelling.
var overriddenToolchainKey = map[string]bool{
	"path":        true,
	"goroot":      true,
	"gocache":     true,
	"gotoolchain": true,
	"goproxy":     true,
	"goenv":       true,
	"gowork":      true,
	"goflags":     true,
}

// childToolchainEnv builds the restricted hermetic environment for the
// relocated alias, probe and scenario children: only the private PATH
// resolves the native go alias, the trusted GOROOT is explicit (a
// relocated alias may lose its embedded root), GOENV=off and GOWORK=off disable
// the caller settings and workspace so they cannot contaminate the scratch
// modules, and the toolchain stays local and offline with a cleared
// GOFLAGS.
func childToolchainEnv(restricted, goRoot, cache string) []string {
	var env []string
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq > 0 && testutil.IsSessionVar(kv[:eq]) {
			continue
		}
		if eq < 0 || !overriddenToolchainKey[strings.ToLower(kv[:eq])] {
			env = append(env, kv)
		}
	}
	return append(env,
		"PATH="+restricted,
		"GOROOT="+goRoot,
		"GOCACHE="+cache,
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"GOENV=off",
		"GOWORK=off",
		"GOFLAGS=",
	)
}

// restrictedGoPathForTest installs the transient restricted PATH and the
// relocated toolchain environment (explicit trusted GOROOT, cleared GOENV
// and GOWORK) into the test process environment with the narrow
// non-parallel t.Setenv, because Probes Options carries no explicit
// environment field and the probe and control children inherit the test
// process environment. The caller's normal PATH may contain node/bun/bash;
// nothing depends on the caller having stripped it.
func restrictedGoPathForTest(t *testing.T, goBin string) string {
	t.Helper()
	restricted := restrictedGoPathDir(t, goBin)
	t.Setenv("PATH", restricted)
	t.Setenv("GOROOT", trustedGoRootForTest(t, goBin))
	t.Setenv("GOENV", "off")
	t.Setenv("GOWORK", "off")
	return restricted
}

// addedDiffLine is one line added by the diff with its post-change line
// number in the new file.
type addedDiffLine struct {
	lineNumber int
	content    string
}

var diffHunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+),(\d+) \+(\d+),(\d+) @@`)

// applyScopedUnifiedDiff applies a single-file, single-hunk unified diff
// with byte-exact verification: the file headers must name the expected
// path, every context and removed line must match the base file exactly
// (no fuzzy matching, no external engine), and the hunk must consume
// exactly the declared old and new line counts. It returns the patched
// bytes and every added line with its post-change line number.
func applyScopedUnifiedDiff(t *testing.T, baseBytes, diffBytes []byte, path string) (patched []byte, added []addedDiffLine) {
	t.Helper()
	baseLines := strings.Split(string(baseBytes), "\n")
	realBase := len(baseLines)
	baseHasTrailingNewline := false
	if realBase > 0 && baseLines[realBase-1] == "" {
		baseHasTrailingNewline = true
		realBase--
	}
	body := strings.Split(string(diffBytes), "\n")
	if len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	if len(body) < 4 {
		t.Fatalf("diff for %s: expected two file headers and one hunk, got %d lines", path, len(body))
	}
	if body[0] != "--- a/"+path || body[1] != "+++ b/"+path {
		t.Fatalf("diff for %s: file headers %q / %q, want a/%s and b/%s", path, body[0], body[1], path, path)
	}
	match := diffHunkHeaderPattern.FindStringSubmatch(body[2])
	if match == nil {
		t.Fatalf("diff for %s: hunk header %q is not a unified diff hunk", path, body[2])
	}
	oldStart, _ := strconv.Atoi(match[1])
	oldCount, _ := strconv.Atoi(match[2])
	newStart, _ := strconv.Atoi(match[3])
	newCount, _ := strconv.Atoi(match[4])
	if oldStart != newStart || oldStart < 1 || newStart < 1 {
		t.Fatalf("diff for %s: hunk starts at old %d / new %d; only single-file hunks starting on the same line are supported", path, oldStart, newStart)
	}
	baseReal := baseLines[:realBase]
	hunkBody := body[3:]
	walked := make([]string, 0, newCount)
	oldPos, newPos := oldStart, newStart
	for i, line := range hunkBody {
		if len(line) == 0 {
			t.Fatalf("diff for %s: empty hunk body line %d", path, i+1)
		}
		switch line[0] {
		case ' ':
			if oldPos > realBase || baseReal[oldPos-1] != line[1:] {
				t.Fatalf("diff for %s: context line %d does not match base line %d byte-exactly", path, i+1, oldPos)
			}
			walked = append(walked, line[1:])
			oldPos++
			newPos++
		case '-':
			if oldPos > realBase || baseReal[oldPos-1] != line[1:] {
				t.Fatalf("diff for %s: removed line %d does not match base line %d byte-exactly", path, i+1, oldPos)
			}
			oldPos++
		case '+':
			added = append(added, addedDiffLine{lineNumber: newPos, content: line[1:]})
			walked = append(walked, line[1:])
			newPos++
		case '@':
			t.Fatalf("diff for %s: second hunk at body line %d; only a single hunk is supported", path, i+1)
		default:
			t.Fatalf("diff for %s: hunk body line %d starts with %q, not a unified diff operator", path, i+1, line[:1])
		}
	}
	if oldPos-oldStart != oldCount {
		t.Fatalf("diff for %s: hunk declared %d old lines but consumed %d", path, oldCount, oldPos-oldStart)
	}
	if newPos-newStart != newCount {
		t.Fatalf("diff for %s: hunk declared %d new lines but produced %d", path, newCount, newPos-newStart)
	}
	patchedLines := append(append([]string{}, baseReal[:oldStart-1]...), walked...)
	if end := oldStart + oldCount - 1; end < realBase {
		patchedLines = append(patchedLines, baseReal[end:]...)
	}
	patched = []byte(strings.Join(patchedLines, "\n"))
	if baseHasTrailingNewline {
		patched = append(patched, '\n')
	}
	return patched, added
}

// listPreparedFiles lists the prepared worker files as sorted relative
// slash paths, skipping the generated .eval-run.json marker.
func listPreparedFiles(t *testing.T, dest string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == ".eval-run.json" {
			return nil
		}
		rel, err := filepath.Rel(dest, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("list prepared %s: %v", dest, err)
	}
	sort.Strings(out)
	return out
}

func lineNumbers(added []addedDiffLine) []int {
	out := make([]int, 0, len(added))
	for _, a := range added {
		out = append(out, a.lineNumber)
	}
	return out
}

func equalInts(a, b []int) bool {
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

// runScenarioModule compiles and executes the bounded scenario tests of a
// scratch module on the native Go toolchain: restricted PATH (only the
// trusted go alias resolves), explicit trusted GOROOT, hermetic GOCACHE,
// GOTOOLCHAIN=local, GOPROXY=off, cleared GOFLAGS and cleared GOENV/GOWORK,
// with a two-minute parent bound and a 60s in-test timeout.
func runScenarioModule(t *testing.T, goBin, root, restricted, goRoot string) string {
	t.Helper()
	env := childToolchainEnv(restricted, goRoot, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "test", "-count=1", "-timeout=60s", "./...")
	cmd.Dir = root
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scenario go test: %v (output %s)", err, string(out))
	}
	if strings.Contains(string(out), "FAIL") || !strings.Contains(string(out), "reviewer-seeded/base") {
		t.Fatalf("scenario go test: output lacks the clean ok result for reviewer-seeded/base: %s", string(out))
	}
	return string(out)
}

// scenarioLedgerSource is the shared recording ledger used by both scenario
// test files: it records every Debit and Credit with identity and amount so
// the exact call order and values can be asserted.
const scenarioLedgerSource = `type scenarioCall struct {
    op     string
    id     string
    amount float64
}

type scenarioLedger struct {
    calls []scenarioCall
}

func (l *scenarioLedger) Debit(id string, amount float64) {
    l.calls = append(l.calls, scenarioCall{"Debit", id, amount})
}

func (l *scenarioLedger) Credit(id string, amount float64) {
    l.calls = append(l.calls, scenarioCall{"Credit", id, amount})
}

func assertCalls(t *testing.T, l *scenarioLedger, want []scenarioCall) {
    t.Helper()
    if len(l.calls) != len(want) {
        t.Fatalf("ledger calls = %v, want %v", l.calls, want)
    }
    for i, c := range l.calls {
        if c.op != want[i].op || c.id != want[i].id || c.amount != want[i].amount {
            t.Fatalf("ledger call %d = %+v, want %+v", i, c, want[i])
        }
    }
}
`

// baseScenarioHeader is the package and import block of the base scenario
// tests.
const baseScenarioHeader = `package base

import (
    "errors"
    "math"
    "testing"
)

`

// baseScenarioSource validates the pre-change contract: required Actor and
// Account, a strictly positive finite amount, ownership, and the exact
// Debit-then-Credit order with the same amount 24.75.
const baseScenarioSource = baseScenarioHeader + scenarioLedgerSource + `
func TestScenarioBaseValidationAndOrder(t *testing.T) {
    owner := &Actor{ID: "actor"}
    account := &Account{ID: "acct", OwnerID: "actor"}

    ledger := &scenarioLedger{}
    if err := Transfer(Options{Account: account, Amount: 24.75, Ledger: ledger}); !errors.Is(err, ErrRequired) {
        t.Fatalf("nil actor: err = %v, want ErrRequired", err)
    }
    if err := Transfer(Options{Actor: owner, Amount: 24.75, Ledger: ledger}); !errors.Is(err, ErrRequired) {
        t.Fatalf("nil account: err = %v, want ErrRequired", err)
    }

    for _, amount := range []float64{-1, 0, math.NaN(), math.Inf(1)} {
        if err := Transfer(Options{Actor: owner, Account: account, Amount: amount, Ledger: ledger}); !errors.Is(err, ErrAmount) {
            t.Fatalf("amount %v: err = %v, want ErrAmount", amount, err)
        }
    }
    if len(ledger.calls) != 0 {
        t.Fatalf("ledger = %v after refusals, want no calls", ledger.calls)
    }

    if err := Transfer(Options{Actor: &Actor{ID: "other"}, Account: account, Amount: 24.75, Ledger: ledger}); !errors.Is(err, ErrNotAuthorized) {
        t.Fatalf("foreign owner: err = %v, want ErrNotAuthorized", err)
    }
    if len(ledger.calls) != 0 {
        t.Fatalf("ledger = %v after the ownership refusal, want no calls", ledger.calls)
    }

    ledger = &scenarioLedger{}
    if err := Transfer(Options{Actor: owner, Account: account, Amount: 24.75, Ledger: ledger}); err != nil {
        t.Fatalf("valid transfer: err = %v, want nil", err)
    }
    assertCalls(t, ledger, []scenarioCall{
        {"Debit", "actor", 24.75},
        {"Credit", "acct", 24.75},
    })
}
`

// patchedScenarioHeader is the package and import block of the patched
// scenario tests.
const patchedScenarioHeader = `package base

import (
    "errors"
    "testing"
)

`

// patchedScenarioSource proves the three seeded defects with
// discriminating scenarios that run through the (also defective) ownership
// gate: the legitimate owner is denied with no ledger call; an
// unauthorized caller is accepted; the zero amount is accepted on the
// bypassed ownership path and debits and credits zero; and the fractional
// amount debits the original value while the credit is rounded.
const patchedScenarioSource = patchedScenarioHeader + scenarioLedgerSource + `
func TestScenarioPatchedSeededDefects(t *testing.T) {
    owner := &Actor{ID: "owner"}
    victim := &Account{ID: "victim-acct", OwnerID: "owner"}
    attacker := &Actor{ID: "attacker"}

    // Seeded defect (post-change line 56): the ownership check is
    // inverted, so the legitimate owner is denied.
    ledger := &scenarioLedger{}
    if err := Transfer(Options{Actor: owner, Account: victim, Amount: 10, Ledger: ledger}); !errors.Is(err, ErrNotAuthorized) {
        t.Fatalf("legitimate owner: err = %v, want ErrNotAuthorized", err)
    }
    if len(ledger.calls) != 0 {
        t.Fatalf("legitimate owner: ledger = %v, want no calls", ledger.calls)
    }

    // ... and an unauthorized caller is accepted through the inverted
    // check, reaching both ledger calls.
    ledger = &scenarioLedger{}
    if err := Transfer(Options{Actor: attacker, Account: victim, Amount: 10, Ledger: ledger}); err != nil {
        t.Fatalf("unauthorized caller: err = %v, want nil (accepted through the inverted check)", err)
    }
    assertCalls(t, ledger, []scenarioCall{
        {"Debit", "attacker", 10},
        {"Credit", "victim-acct", 10},
    })

    // Seeded defect (post-change line 53): the amount check accepts zero,
    // so a zero transfer is accepted on the bypassed ownership path and
    // debits and credits zero.
    ledger = &scenarioLedger{}
    if err := Transfer(Options{Actor: attacker, Account: victim, Amount: 0, Ledger: ledger}); err != nil {
        t.Fatalf("zero amount: err = %v, want nil (zero passes the less-than-zero check)", err)
    }
    assertCalls(t, ledger, []scenarioCall{
        {"Debit", "attacker", 0},
        {"Credit", "victim-acct", 0},
    })

    // Seeded defect (post-change line 60): the credit is rounded to an
    // integer while the debit keeps the original fractional amount.
    ledger = &scenarioLedger{}
    if err := Transfer(Options{Actor: attacker, Account: victim, Amount: 1.6, Ledger: ledger}); err != nil {
        t.Fatalf("fractional amount: err = %v, want nil", err)
    }
    assertCalls(t, ledger, []scenarioCall{
        {"Debit", "attacker", 1.6},
        {"Credit", "victim-acct", 2},
    })
}
`

// TestReviewerSeededFixtureConsumer prepares the real seeded reviewer
// fixture through the real Prepare, applies the real change.diff with the
// scoped diff consumer, asserts the answer-key post-change lines 53/56/60,
// and compiles and runs the bounded base and patched scenario modules.
func TestReviewerSeededFixtureConsumer(t *testing.T) {
	goBin := goExecutableForTest(t)
	fixture := reviewerFixtureRootForTest(t)
	restricted := restrictedGoPathDir(t, goBin)
	goRoot := trustedGoRootForTest(t, goBin)

	// The hidden answer key stays in the fixture root with the verified
	// bytes; it is never part of the worker manifest.
	if sum, err := sha256File(filepath.Join(fixture, "ANSWER-KEY.md")); err != nil || sum != verifiedReviewerAnswerKeyHash {
		t.Fatalf("fixture ANSWER-KEY.md hash = %s (err %v), want the verified hidden answer key %s", sum, err, verifiedReviewerAnswerKeyHash)
	}

	caller := t.TempDir()
	dest := filepath.Join(t.TempDir(), "reviewer-worker")
	prepared, err := Prepare(context.Background(), Options{Fixture: fixture, Destination: dest, Repository: caller})
	if err != nil || !prepared.ManifestOK {
		t.Fatalf("prepare: %v (result %+v)", err, prepared)
	}
	if prepared.Fixture != "reviewer-seeded" {
		t.Fatalf("prepare fixture = %q, want reviewer-seeded", prepared.Fixture)
	}

	// Exactly the four manifest files, byte-exact, and no answer leak.
	worker := listPreparedFiles(t, dest)
	if !equalStrings(worker, []string{"base/transfer.go", "brief.md", "change.diff", "go.mod"}) {
		t.Fatalf("prepared worker files = %v, want exactly the four manifest files (no hidden answer)", worker)
	}
	for _, rel := range worker {
		sum, err := sha256File(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil || sum != verifiedReviewerFileHashes[rel] {
			t.Fatalf("prepared %s hash = %s (err %v), want the verified bytes %s", rel, sum, err, verifiedReviewerFileHashes[rel])
		}
		data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read prepared %s: %v", rel, err)
		}
		if bytes.Contains(data, []byte("Answer key")) {
			t.Fatalf("prepared %s leaks the withheld answer key text", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, ".eval-run.json")); err != nil {
		t.Fatalf("prepared .eval-run.json marker missing: %v", err)
	}

	baseBytes, err := os.ReadFile(filepath.Join(dest, "base", "transfer.go"))
	if err != nil {
		t.Fatal(err)
	}
	diffBytes, err := os.ReadFile(filepath.Join(dest, "change.diff"))
	if err != nil {
		t.Fatal(err)
	}
	patchedBytes, added := applyScopedUnifiedDiff(t, baseBytes, diffBytes, "base/transfer.go")

	// The answer key's post-change line numbers 53/56/60 stay correct:
	// the diff's added lines land exactly on those lines, and the defect
	// constructs live exactly there in the patched file.
	if got := lineNumbers(added); !equalInts(got, []int{53, 56, 60}) {
		t.Fatalf("added post-change lines = %v, want the answer key lines 53/56/60", got)
	}
	patchedLines := strings.Split(string(patchedBytes), "\n")
	if got := strings.TrimSpace(patchedLines[52]); got != "if math.IsNaN(opts.Amount) || math.IsInf(opts.Amount, 0) || opts.Amount < 0 {" {
		t.Fatalf("patched line 53 = %q, want the zero-accepting amount check", got)
	}
	if got := strings.TrimSpace(patchedLines[55]); got != "if opts.Account.OwnerID == opts.Actor.ID {" {
		t.Fatalf("patched line 56 = %q, want the inverted ownership check", got)
	}
	if got := strings.TrimSpace(patchedLines[59]); got != "opts.Ledger.Credit(opts.Account.ID, math.Round(opts.Amount))" {
		t.Fatalf("patched line 60 = %q, want the rounding credit", got)
	}

	goModBytes, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	runScenarioModuleBytes := func(t *testing.T, transferSource, scenarioSource string) string {
		t.Helper()
		module := t.TempDir()
		if err := os.MkdirAll(filepath.Join(module, "base"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(module, "go.mod"), goModBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(module, "base", "transfer.go"), []byte(transferSource), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(module, "base", "scenario_test.go"), []byte(scenarioSource), 0o644); err != nil {
			t.Fatal(err)
		}
		return runScenarioModule(t, goBin, module, restricted, goRoot)
	}

	baseOut := runScenarioModuleBytes(t, string(baseBytes), baseScenarioSource)
	if !strings.Contains(baseOut, "ok") {
		t.Fatalf("base scenario module did not report ok: %s", baseOut)
	}
	patchedOut := runScenarioModuleBytes(t, string(patchedBytes), patchedScenarioSource)
	if !strings.Contains(patchedOut, "ok") {
		t.Fatalf("patched scenario module did not report ok: %s", patchedOut)
	}
}

// TestRestrictedGoPathHelper controls the restricted-PATH helper itself:
// the private alias actually executes go env GOROOT and reports the
// trusted toolchain root; the native go alias resolves under the private
// PATH through host resolution; the banned engines do not resolve there;
// and the forced hard-link-failure seam produces a verified
// byte-identical executable alias with the executable bit set.
func TestRestrictedGoPathHelper(t *testing.T) {
	goBin := goExecutableForTest(t)
	goRoot := trustedGoRootForTest(t, goBin)

	runAliasGoEnv := func(t *testing.T, alias, restricted string) {
		t.Helper()
		env := childToolchainEnv(restricted, goRoot, t.TempDir())
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, alias, "env", "GOROOT")
		cmd.Env = env
		cmd.WaitDelay = 5 * time.Second
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("alias %s env GOROOT: %v (output %s)", alias, err, string(out))
		}
		if got := strings.TrimSpace(string(out)); got != goRoot {
			t.Fatalf("alias %s env GOROOT = %q, want the trusted toolchain root %q", alias, got, goRoot)
		}
	}

	// Production alias path (hard link on Unix, verified byte copy on
	// Windows).
	priv := t.TempDir()
	alias := installGoAlias(t, priv, goBin, os.Link)
	if _, resolved := engineResolves(priv, "go"); !resolved {
		t.Fatalf("native go alias %s does not resolve under the private PATH %s", alias, priv)
	}
	assertBannedEnginesUnresolvable(t, priv)
	runAliasGoEnv(t, alias, priv)

	// Forced hard-link-failure seam: the verified byte-identical copy.
	privCopy := t.TempDir()
	forcedCopy := func(src, dst string) error {
		return errors.New("hard-link seam failure: force the verified copy fallback")
	}
	aliasCopy := installGoAlias(t, privCopy, goBin, forcedCopy)
	if base := filepath.Base(aliasCopy); base != nativeGoAliasName() {
		t.Fatalf("forced-copy alias name = %q, want the platform alias %q", base, nativeGoAliasName())
	}
	if got, want := sha256FileSum(t, aliasCopy), sha256FileSum(t, goBin); got != want {
		t.Fatalf("forced-copy alias %s hash = %s, want the byte-identical executable %s", aliasCopy, got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(aliasCopy)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("forced-copy alias %s permissions %v lack the executable bit", aliasCopy, info.Mode().Perm())
		}
	}
	assertBannedEnginesUnresolvable(t, privCopy)
	runAliasGoEnv(t, aliasCopy, privCopy)
}
