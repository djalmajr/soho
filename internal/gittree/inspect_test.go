package gittree

// Focused tests for the inspection's sidecar seam: SidecarInspect locates
// the same sidecar as Sidecar through the capped read, an overlong
// sidecar refuses instead of being read whole, and the inspection's
// changed-path reader keeps the ordinary algorithm on a valid sidecar
// while refusing the overlong one.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

const sidecarValidJSON = `{"git_tree":"/tmp/none","git_head":"0123456789012345678901234567890123456789","role":"implementer"}`

// TestSidecarInspectLocatesTheSameSidecar: both candidate probes (state
// briefs first, then beside the report) locate through the capped read,
// and an absent sidecar locates nothing like the ordinary reader.
func TestSidecarInspectLocatesTheSameSidecar(t *testing.T) {
	scratchDir := t.TempDir()
	report := filepath.Join(scratchDir, "worker-final.md")
	if err := os.WriteFile(report, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The state candidate wins when both exist.
	sd := t.TempDir()
	stateSidecar := filepath.Join(sd, "briefs", "worker-final.dispatch.json")
	if err := os.MkdirAll(filepath.Dir(stateSidecar), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateSidecar, []byte(sidecarValidJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := SidecarInspect(sd, report), stateSidecar; got != want {
		t.Fatalf("SidecarInspect = %q, want the state candidate %q", got, want)
	}
	if got, want := Sidecar(sd, report), stateSidecar; got != want {
		t.Fatalf("Sidecar = %q, want the state candidate %q", got, want)
	}
	// Beside the report only.
	onlyScratchDir := t.TempDir()
	onlyReport := filepath.Join(onlyScratchDir, "worker-final.md")
	if err := os.WriteFile(onlyReport, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(onlyScratchDir, "worker-final.dispatch.json")
	if err := os.WriteFile(beside, []byte(sidecarValidJSON+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := SidecarInspect(onlyScratchDir, onlyReport), beside; got != want {
		t.Fatalf("SidecarInspect = %q, want the beside-report candidate %q", got, want)
	}
	// Absent everywhere: a report without any candidate sidecar.
	emptySd := t.TempDir()
	emptyDir := t.TempDir()
	emptyReport := filepath.Join(emptyDir, "worker-final.md")
	if err := os.WriteFile(emptyReport, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SidecarInspect(emptySd, emptyReport); got != "" {
		t.Fatalf("SidecarInspect = %q, want no sidecar", got)
	}
	if got := Sidecar(emptySd, emptyReport); got != "" {
		t.Fatalf("Sidecar = %q, want no sidecar", got)
	}
}

// TestSidecarInspectRefusesOversizeWithoutTruncation: a sidecar longer
// than the inspection cap refuses (nil, like an unreadable sidecar) on
// the inspection path while the ordinary command reader keeps its
// behavior on the same file — the cap is the inspection seam's bound,
// not a new truncation of the shared reader.
func TestSidecarInspectRefusesOversizeWithoutTruncation(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "worker-final.md")
	if err := os.WriteFile(report, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(dir, "worker-final.dispatch.json")
	// A valid sidecar with a padding field longer than the 1 MiB cap.
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
	if o := readSidecarInspect(sidecar); o != nil {
		t.Fatalf("the overlong sidecar was read through the inspection cap")
	}
	if o := read(sidecar); o == nil {
		t.Fatalf("the ordinary reader refused the overlong sidecar it always reads")
	}
}

// TestChangedPathsReadOnlyRefusesOversizeSidecar: the inspection's
// changed-path reader reports nil on the overlong sidecar (report
// authority preserved) and keeps the ordinary algorithm's result on a
// valid sidecar.
func TestChangedPathsReadOnlyRefusesOversizeSidecar(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "worker-final.md")
	if err := os.WriteFile(report, []byte("r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(dir, "worker-final.dispatch.json")
	raw := map[string]any{
		"git_tree": "/tmp/none",
		"git_head": "0123456789012345678901234567890123456789",
		"role":     "implementer",
		"pad":      strings.Repeat("y", sidecarInspectLimit+4096),
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if paths := ChangedPathsReadOnly(sidecar, "worker", envNoGit(t)); paths != nil {
		t.Fatalf("the overlong sidecar produced changed paths: %#v", paths)
	}
}

// envNoGit is a pinned env whose PATH has no Git: the changed-path reads
// fail after the gate, so the two readers' outcome is the gate's.
func envNoGit(t *testing.T) (env platform.Env) {
	t.Helper()
	home := t.TempDir()
	return platform.Env{
		"PATH":            filepath.Join(home, "none"),
		"HOME":            home,
		"USERPROFILE":     home,
		"TMPDIR":          home,
		"XDG_CONFIG_HOME": home,
		"XDG_CACHE_HOME":  filepath.Join(home, "cache"),
	}
}
