package core

// Deterministic replacement consumer at the directory retention boundary
// (DJA-611 F1): the appended session directory w1 is replaced, after its
// complete check (pre-entry check, retained open, handle identity,
// post-entry check) and before the final file's check, by a symlink to
// another session directory w2 inside the same trusted base. The public
// loader must never read w2's policy: the final file's checks and read
// resolve through the retained w1 handle, so the session layer reads the
// original bytes (or is refused), never the alternate. The per-call
// barrier dependency of the private loadReadOnlyConfig coordinates the
// window deterministically (no timing-only race is the sole proof); the
// public LoadReadOnlyConfig passes nil and carries no test slot.
//
// This test lives in package core because the seam is unexported and the
// real inventory consumer imports core (an import cycle would make the
// external package unusable here); it therefore asserts the consumer's
// own input — the merged entries — directly.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestReadOnlyBarrieredParentReplacement(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME":        home,
		"USERPROFILE": home,
		"PATH":        filepath.Join(root, "bin"),
	}

	foreign := filepath.Join(root, "foreign")
	base := filepath.Join(foreign, ".soho")
	// The real session (original policy) and the alternate session (the
	// policy that must never surface through the replacement).
	if err := os.MkdirAll(filepath.Join(base, "w1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "w1", "session.conf"), []byte("inventory=off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "w2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "w2", "session.conf"), []byte("inventory=show\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	windowOpen := make(chan struct{})
	release := make(chan struct{})
	fired := 0
	// The per-call dependency: installed for this one load call only,
	// never a package-global hook.
	barrier := func() {
		fired++
		close(windowOpen)
		<-release
	}

	type result struct {
		rc  ReadOnlyConfig
		err error
	}
	done := make(chan result, 1)
	go func() {
		rc, err := loadReadOnlyConfig(env, foreign, "w1", UserSelection{}, barrier)
		done <- result{rc: rc, err: err}
	}()

	// The fixture has no project layer, so the seam fires exactly once,
	// at the session directory w1, after its complete check. The swap
	// renames the original directory aside (its content stays intact) and
	// puts a symlink to w2 at the w1 name: the retained handle must keep
	// the original directory, the original path the alternate.
	<-windowOpen
	if err := os.Rename(filepath.Join(base, "w1"), filepath.Join(base, "w1-real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("w2", filepath.Join(base, "w1")); err != nil {
		t.Fatalf("symlinks unavailable: %v", err)
	}
	close(release)
	res := <-done

	if fired != 1 {
		t.Fatalf("the seam fired %d time(s), want exactly the one appended session directory", fired)
	}
	if res.err != nil {
		t.Fatalf("the load returned an error: %v", res.err)
	}
	alt, err := os.ReadFile(filepath.Join(base, "w2", "session.conf"))
	if err != nil || string(alt) != "inventory=show\n" {
		t.Fatalf("the alternate session file changed: %q %v", alt, err)
	}
	var session ConfigLayer
	for _, layer := range res.rc.Config.Layers {
		if layer.Source == "session" {
			session = layer
		}
	}
	if session.ReadError != nil {
		t.Fatalf("the session layer must read the original through the retained handle, got refusal: %v", session.ReadError)
	}
	if v, ok := session.Entries["inventory"]; !ok || v.Value != "off" {
		t.Fatalf("the session layer read %q; the retained directory handle must keep the original, never the alternate", v.Value)
	}
	if v, ok := res.rc.Config.Entries["inventory"]; ok && v.Value == "show" {
		t.Fatalf("the merged entries carry the alternate session's policy %q", v.Value)
	}
}
