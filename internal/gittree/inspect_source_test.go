package gittree

// F2 source-bound gittree seams: the sidecar location and the
// changed-path owned scope follow the source's retained roots — the
// same two candidate probes, the same composed-brief/pointer/history
// walk — so a renamed or replaced state dir does not change the
// decision, and a capped or confined-refused read reports nil like
// every other metadata failure.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

// sourceFixture opens a session source over a minimal state root and
// returns the fixture's paths. The session dir is a safe plain
// directory; the scratch dir is the approved scratch base.
func sourceFixture(t *testing.T) (src *core.SessionSource, sd, scratch string) {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "PATH"} {
		t.Setenv(key, home)
	}
	base := t.TempDir()
	sd = filepath.Join(base, "s1")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	scratch = t.TempDir()
	src, err := core.OpenSessionSource(base, "s1", scratch)
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src, sd, scratch
}

// TestSidecarInspectSourceLocatesTheSameSidecar: the source-bound
// location makes the same two candidate decisions as SidecarInspect
// (state briefs first, then beside the report) and nothing else.
func TestSidecarInspectSourceLocatesTheSameSidecar(t *testing.T) {
	src, sd, scratch := sourceFixture(t)
	report := filepath.Join(scratch, "soho", "s1", "reports", "worker-final.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The state candidate wins when both exist.
	stateSidecar := filepath.Join(sd, "briefs", "worker-final.dispatch.json")
	if err := os.MkdirAll(filepath.Dir(stateSidecar), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateSidecar, []byte(sidecarValidJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := SidecarInspectSource(src, sd, report), stateSidecar; got != want {
		t.Fatalf("SidecarInspectSource = %q, want the state candidate %q", got, want)
	}
	if got, want := SidecarInspect(sd, report), stateSidecar; got != want {
		t.Fatalf("SidecarInspect = %q, want the same state candidate %q", got, want)
	}
	// Beside the report only: the read-only reader probes the candidate
	// beside the report, while the source-bound reader is confined to the
	// approved locations and refuses an arbitrary sidecar root (it reports
	// no sidecar without reading the unapproved candidate).
	onlySd := t.TempDir()
	onlyReport := filepath.Join(t.TempDir(), "worker-final.md")
	if err := os.WriteFile(onlyReport, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(filepath.Dir(onlyReport), "worker-final.dispatch.json")
	if err := os.WriteFile(beside, []byte(sidecarValidJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SidecarInspectSource(src, onlySd, onlyReport); got != "" {
		t.Fatalf("SidecarInspectSource = %q, want no sidecar (the candidate beside the report is outside the approved locations and must not be read)", got)
	}
	if got, want := SidecarInspect(onlySd, onlyReport), beside; got != want {
		t.Fatalf("SidecarInspect = %q, want the beside-report candidate %q", got, want)
	}
	// The same candidate inside the approved scratch session dir is read
	// through the retained scratch root.
	scratchBesideReport := filepath.Join(scratch, "soho", "s1", "reports", "worker-final.md")
	if err := os.WriteFile(scratchBesideReport, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratchBeside := filepath.Join(scratch, "soho", "s1", "reports", "worker-final.dispatch.json")
	if err := os.WriteFile(scratchBeside, []byte(sidecarValidJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := SidecarInspectSource(src, onlySd, scratchBesideReport), scratchBeside; got != want {
		t.Fatalf("SidecarInspectSource = %q, want the approved scratch candidate %q", got, want)
	}
	// Absent everywhere approved.
	if got := SidecarInspectSource(src, onlySd, filepath.Join(t.TempDir(), "worker-final.md")); got != "" {
		t.Fatalf("SidecarInspectSource = %q, want no sidecar", got)
	}
}

// TestSidecarInspectSourcePinnedRoot: the state session dir is renamed
// and replaced by a link to a decoy with no sidecar; the source-bound
// location keeps the retained candidate, while the raw read-only
// location follows the link and finds none — the two readers must
// disagree, proving the inspection follows the retained root.
func TestSidecarInspectSourcePinnedRoot(t *testing.T) {
	src, sd, scratch := sourceFixture(t)
	report := filepath.Join(scratch, "soho", "s1", "reports", "worker-final.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	retainedSidecar := filepath.Join(sd, "briefs", "worker-final.dispatch.json")
	if err := os.MkdirAll(filepath.Dir(retainedSidecar), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retainedSidecar, []byte(sidecarValidJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SidecarInspect(sd, report); got != retainedSidecar {
		t.Fatalf("control SidecarInspect = %q, want the state candidate", got)
	}
	if err := os.Rename(sd, sd+"-retained"); err != nil {
		t.Fatalf("cannot rename the session dir: %v", err)
	}
	decoy := filepath.Join(filepath.Dir(sd), "decoy")
	if err := os.MkdirAll(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, sd); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	if got := SidecarInspectSource(src, sd, report); got != retainedSidecar {
		t.Fatalf("SidecarInspectSource = %q, want the retained candidate %q", got, retainedSidecar)
	}
	// The raw read-only location follows the link to the decoy, which has
	// no sidecar: it locates nothing.
	if got := SidecarInspect(sd, report); got != "" {
		t.Fatalf("SidecarInspect = %q, want no sidecar (the raw path follows the link to the decoy)", got)
	}
}

// TestChangedPathsInspectSourceRefusesOversizeSidecar: a sidecar longer
// than the inspection cap refuses through the source's confined read
// (nil, like an unreadable sidecar — report authority preserved), while
// the composed brief read of the same source stays available for the
// owned scope it carries.
func TestChangedPathsInspectSourceRefusesOversizeSidecar(t *testing.T) {
	src, sd, _ := sourceFixture(t)
	sidecar := filepath.Join(sd, "briefs", "worker-final.dispatch.json")
	if err := os.MkdirAll(filepath.Dir(sidecar), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{
		"git_tree": "/tmp/none",
		"git_head": "0123456789012345678901234567890123456789",
		"role":     "implementer",
		"pad":      strings.Repeat("x", sidecarInspectLimit+4096),
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if paths := ChangedPathsInspectSource(src, sidecar, "worker", envNoGit(t)); paths != nil {
		t.Fatalf("the overlong sidecar produced changed paths: %#v", paths)
	}
}

// TestDiffEffectiveOwnedSourceSeamsPinned: the owned-scope walk's
// composed brief read follows the retained root: the brief (with the
// Owned scope) stands under the state briefs dir before the swap; after
// the session dir is renamed and replaced by a link to a decoy whose
// brief has no scope, the source-bound walk keeps the retained scope,
// while the read-only walk over the raw path loses it and reports the
// scope unavailable.
func TestDiffEffectiveOwnedSourceSeamsPinned(t *testing.T) {
	src, sd, _ := sourceFixture(t)
	stem := filepath.Join(sd, "briefs", "worker-final")
	if err := os.MkdirAll(filepath.Dir(stem), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stem+".dispatch.json", []byte(sidecarValidJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stem+".brief.md", []byte("## Owned files\n- `owned.txt`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := parseObjectBytes([]byte(sidecarValidJSON), nil)
	if o == nil {
		t.Fatalf("the fixture sidecar is not a valid object")
	}
	owned, hasOwned, scopeOK := diffEffectiveOwnedRead(sd+"/briefs/worker-final.dispatch.json", "worker", o, sourceOwnedSeams(src))
	if !scopeOK || !hasOwned || len(owned) != 1 || owned[0] != "owned.txt" {
		t.Fatalf("control walk = %v %v %v, want the retained scope", owned, hasOwned, scopeOK)
	}
	// Rename and swap the session dir; the decoy's brief has no scope.
	if err := os.Rename(sd, sd+"-retained"); err != nil {
		t.Fatalf("cannot rename the session dir: %v", err)
	}
	decoy := filepath.Join(filepath.Dir(sd), "decoy")
	if err := os.MkdirAll(filepath.Join(decoy, "briefs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, sd); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "briefs", "worker-final.brief.md"), []byte("no scope here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The source-bound walk keeps the retained scope (it reads the
	// retained brief).
	owned, hasOwned, scopeOK = diffEffectiveOwnedRead(sd+"/briefs/worker-final.dispatch.json", "worker", o, sourceOwnedSeams(src))
	if !scopeOK || !hasOwned || len(owned) != 1 || owned[0] != "owned.txt" {
		t.Fatalf("pinned walk = %v %v %v, want the retained scope (the decoy's brief must not be read)", owned, hasOwned, scopeOK)
	}
	// The read-only walk over the raw path follows the link to the
	// decoy's brief (no scope) and loses the owned decision.
	owned, hasOwned, scopeOK = diffEffectiveOwnedRead(sd+"/briefs/worker-final.dispatch.json", "worker", o, readOnlyOwnedSeams())
	if scopeOK && hasOwned {
		t.Fatalf("raw walk = %v %v, want the lost scope (the raw path follows the link)", owned, hasOwned)
	}
}
