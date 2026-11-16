package spawn

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// SOH-309: warnOnce prints once per existing state dir, always prints when
// there is none (and creates nothing), and an admission-only pass neither
// prints nor leaves a marker.
func TestWarnOnce(t *testing.T) {
	root := t.TempDir()
	env := platform.Env{"HOME": root, "USERPROFILE": root, "TMPDIR": root, "PATH": root, "SOHO_DIR": filepath.Join(root, "state"), "HERDR_WORKSPACE_ID": "ws"}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	got := core.StateDirPath(ctx, env, root)
	count := 0
	count0 := func(string, *core.Config, platform.Env, string) { count++ }

	warnOnce("k", "m", ctx, env, root, false, count0)
	warnOnce("k", "m", ctx, env, root, false, count0)
	if count != 2 {
		t.Fatalf("without a state dir every call must warn: %d", count)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("warnOnce created the state dir %s: %v", got, err)
	}

	if err := os.MkdirAll(got, 0o700); err != nil {
		t.Fatal(err)
	}
	count = 0
	warnOnce("k", "m", ctx, env, root, true, count0)
	if count != 0 {
		t.Fatal("an admission-only pass printed")
	}
	warnOnce("k", "m", ctx, env, root, false, count0)
	warnOnce("k", "m", ctx, env, root, false, count0)
	warnOnce("other", "m", ctx, env, root, false, count0)
	if count != 2 {
		t.Fatalf("want one warning per key in a session, got %d", count)
	}

	env["SOHO_NOWRITE"] = "1"
	count = 0
	warnOnce("third", "m", ctx, env, root, false, count0)
	warnOnce("third", "m", ctx, env, root, false, count0)
	if count != 2 {
		t.Fatalf("NOWRITE must leave no marker and warn every time: %d", count)
	}
}
