package core_test

// Deterministic public-loader regressions for the directory replacement
// boundary (DJA-611 F1) and the bounded existing-cache discovery (DJA-611
// F2): a session directory that is a symlink to another session inside
// the same trusted base is refused, never followed; an oversized cached
// config.defaults is refused without its body being read during source
// discovery. The real inventory consumer and the fakecli canaries keep
// the ordinary invariants.

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/inventory"
	skill "github.com/djalmajr/soho/skills/soho"
)

// TestReadOnlyReplacedSessionDirectoryRefused: the session directory w1
// is replaced by a symlink to the session directory w2 inside the same
// base BEFORE the load. The public loader must refuse the session layer
// (never follow the link to the alternate policy), keep the real files
// untouched, and the committed consumer must stay closed.
func TestReadOnlyReplacedSessionDirectoryRefused(t *testing.T) {
	env, root := roFixture(t)
	bin := filepath.Join(root, "bin")
	roInstallCanaries(t, bin)
	roStaleMarkers(env, root)

	foreign := filepath.Join(root, "foreign")
	roWrite(t, filepath.Join(foreign, ".soho", "w1", "session.conf"), "inventory=off\n")
	roWrite(t, filepath.Join(foreign, ".soho", "w2", "session.conf"), "inventory=show\n")
	w1 := filepath.Join(foreign, ".soho", "w1")
	if err := os.RemoveAll(w1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(foreign, ".soho", "w2"), w1); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	before := roEnvCopy(env)
	snap := roSnapshot(t, root)
	rc, err := core.LoadReadOnlyConfig(env, foreign, "w1", core.UserSelection{})
	if err != nil {
		t.Fatalf("refusals are recorded as layer errors, not returned: %v", err)
	}
	roEnvUnchanged(t, env, before)
	roCanaryFired(t, bin)
	if got := roSnapshot(t, root); !reflect.DeepEqual(got, snap) {
		t.Fatalf("the load created or changed fixture files: %s", diffMaps(got, snap))
	}

	session, ok := roLayer(rc.Config.Layers, "session")
	if !ok {
		t.Fatal("the session layer is missing")
	}
	if session.ReadError == nil {
		t.Fatalf("a session directory that is a symlink must be refused, got entries %+v", session.Entries)
	}
	if len(session.Entries) != 0 {
		t.Fatalf("the refused layer contributed entries: %+v", session.Entries)
	}
	if v, present := rc.Config.Entries["inventory"]; present && v.Source == "session" {
		t.Fatalf("the merged entries carry a policy %q from a replaced directory", v.Value)
	}
	if mode := inventory.ResolveMode(&rc.Config, env); mode != inventory.ModeOff {
		t.Fatalf("the committed consumer resolves %q, want off (fail closed)", mode)
	}
	if got := roRead(t, filepath.Join(foreign, ".soho", "w2", "session.conf")); got != "inventory=show\n" {
		t.Fatalf("the alternate session file changed: %q", got)
	}
}

// TestReadOnlyOversizedCachedDefaultsBounded: a valid existing published
// cache is the defaults source; enlarging only its config.defaults to a
// raw 16MiB must make the load refuse the cache (falling back to the
// embedded defaults) WITHOUT reading or allocating the oversized body
// during source discovery: the known embedded resource length is checked
// before any body allocation or consumption.
func TestReadOnlyOversizedCachedDefaultsBounded(t *testing.T) {
	env, root := roFixture(t)
	bin := filepath.Join(root, "bin")
	roInstallCanaries(t, bin)
	cache := filepath.Join(homeOf(t, env), ".cache")

	// The existing-source control: a valid published cache is the source.
	dir, err := skill.Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := core.LoadReadOnlyConfig(env, "", "", core.UserSelection{})
	if err != nil {
		t.Fatal(err)
	}
	def, _ := roLayer(rc.Config.Layers, "defaults")
	if def.ReadError != nil {
		t.Fatalf("the valid existing cache must load as the defaults source: %v", def.ReadError)
	}
	if def.File != filepath.Join(dir, "config.defaults") {
		t.Fatalf("defaults layer file = %q, want the existing cache's config.defaults", def.File)
	}
	roCanaryFired(t, bin)

	// Now enlarge only the cached config.defaults to 16MiB (outside the
	// measurement window) and reload.
	if err := os.WriteFile(filepath.Join(dir, "config.defaults"), make([]byte, 16<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(dir, "config.defaults")
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	rc, err = core.LoadReadOnlyConfig(env, "", "", core.UserSelection{})
	runtime.ReadMemStats(&m1)
	if err != nil {
		t.Fatal(err)
	}
	def, _ = roLayer(rc.Config.Layers, "defaults")
	if def.ReadError != nil {
		t.Fatalf("a refused cache must fall back to the embedded defaults, not error: %v", def.ReadError)
	}
	if def.File != "" {
		t.Fatalf("defaults layer file = %q, want the embedded source (no File) after the cache refusal", def.File)
	}
	allocated := m1.TotalAlloc - m0.TotalAlloc
	if allocated >= 4<<20 {
		t.Fatalf("the oversized cache load allocated %d bytes; the 16MiB body must not be read or allocated during source discovery", allocated)
	}
	data, err := os.ReadFile(oversized)
	if err != nil || len(data) != 16<<20 {
		t.Fatalf("the oversized fixture changed: %v %d", err, len(data))
	}
	roCanaryFired(t, bin)
}
