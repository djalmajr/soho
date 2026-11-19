package core_test

// Focused regressions for LoadLocalConfigReadOnly, the local read-only
// configuration path `soho report <agent>` resolves its state directory
// with: the ordinary layer files and parser without materializing the skill,
// symlinked config files read as the ordinary loader reads them, and a layer
// that exists and cannot be read returned as an error instead of being
// skipped. Fixtures pin HOME, USERPROFILE and XDG_CONFIG_HOME at the
// temporary root.

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// localFixture returns an isolated environment with no skill source, so the
// defaults layer is the embedded config.defaults, and the caller workspace
// named by the environment (no transport call).
func localFixture(t *testing.T) (platform.Env, string, string) {
	t.Helper()
	env, root := roFixture(t)
	conf := filepath.Join(root, "conf")
	cwd := filepath.Join(root, "project")
	env["XDG_CONFIG_HOME"] = conf
	env["SOHO_NOWRITE"] = "1"
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["SOHO_TRANSPORT"] = "herdr"
	for _, dir := range []string{conf, cwd} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return env, root, cwd
}

// treeEntries lists every path below root, for the write-nothing check.
func treeEntries(t *testing.T, root string) []string {
	t.Helper()
	found := []string{}
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(found)
	return found
}

// TestLoadLocalConfigReadOnlyReadsSymlinkedLayers: a user, project and
// session config file that is a symlink to a regular file is read and its
// entries apply in layer order, so the session layer's state_dir redirect
// resolves the caller's state dir; the defaults layer is the embedded
// config.defaults; and the load writes nothing.
func TestLoadLocalConfigReadOnlyReadsSymlinkedLayers(t *testing.T) {
	env, root, cwd := localFixture(t)
	roWrite(t, filepath.Join(root, "conf", "soho", "regular-user.conf"), "max_workers=7\n")
	if err := os.Symlink("regular-user.conf", filepath.Join(root, "conf", "soho", "config")); err != nil {
		t.Fatal(err)
	}
	roWrite(t, filepath.Join(cwd, ".agents", "regular-project.conf"), "state_dir=base\n")
	if err := os.Symlink("regular-project.conf", filepath.Join(cwd, ".agents", "soho.conf")); err != nil {
		t.Fatal(err)
	}
	roWrite(t, filepath.Join(cwd, "base", "ws", "regular-session.conf"), "state_dir=redirected\n")
	if err := os.Symlink("regular-session.conf", filepath.Join(cwd, "base", "ws", "session.conf")); err != nil {
		t.Fatal(err)
	}

	before := treeEntries(t, root)
	ctx, err := core.LoadLocalConfigReadOnly(env, cwd)
	if err != nil {
		t.Fatalf("LoadLocalConfigReadOnly: %v", err)
	}
	after := treeEntries(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the load wrote to the fixture: before %v, after %v", before, after)
	}

	if got := ctx.Entries["multi_role"]; got.Value != "on" || got.Source != "defaults" {
		t.Fatalf("defaults layer: multi_role=%#v, want the embedded default", got)
	}
	if got := ctx.Entries["max_workers"]; got.Value != "7" || got.Source != "user" {
		t.Fatalf("symlinked user layer: max_workers=%#v", got)
	}
	if got := ctx.Entries["state_dir"]; got.Value != "redirected" || got.Source != "session" {
		t.Fatalf("symlinked session layer: state_dir=%#v, want the session redirect", got)
	}
	want := filepath.Join(cwd, "redirected", "ws")
	if got := core.StateDirPath(&ctx, env, cwd); got != want {
		t.Fatalf("StateDirPath=%q, want %q", got, want)
	}
}

// TestLoadLocalConfigReadOnlyReadsSymlinkedUserFile: the user layer's own
// symlink picks the state root it names, with the same result as a regular
// user file (the control).
func TestLoadLocalConfigReadOnlyReadsSymlinkedUserFile(t *testing.T) {
	env, _, cwd := localFixture(t)
	user := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
	roWrite(t, user, "state_dir=redirected\n")
	if got := localStateRoot(t, env, cwd); got != filepath.Join(cwd, "redirected") {
		t.Fatalf("regular user file: state root=%q", got)
	}
	if err := os.Remove(user); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("regular-user.conf", user); err != nil {
		t.Fatal(err)
	}
	roWrite(t, filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "regular-user.conf"), "state_dir=redirected\n")
	if got := localStateRoot(t, env, cwd); got != filepath.Join(cwd, "redirected") {
		t.Fatalf("symlinked user file: state root=%q, want the redirected root", got)
	}
}

func localStateRoot(t *testing.T, env platform.Env, cwd string) string {
	t.Helper()
	ctx, err := core.LoadLocalConfigReadOnly(env, cwd)
	if err != nil {
		t.Fatalf("LoadLocalConfigReadOnly: %v", err)
	}
	return filepath.Dir(core.StateDirPath(&ctx, env, cwd))
}

// TestLoadLocalConfigReadOnlyUnreadableLayerIsAnError: a missing layer is an
// ordinary empty layer, while a layer path that exists and cannot be read — a
// symlink to a missing target, a symlink to a directory, a directory — is an
// error naming that layer, never a silent skip to the lower layers.
func TestLoadLocalConfigReadOnlyUnreadableLayerIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		breakLayer func(t *testing.T, env platform.Env, cwd string)
		layer      string
	}{
		{"session symlink to a missing target", func(t *testing.T, env platform.Env, cwd string) {
			dir := filepath.Join(cwd, "base", "ws")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("missing-session.conf", filepath.Join(dir, "session.conf")); err != nil {
				t.Fatal(err)
			}
		}, "session"},
		{"session symlink to a directory", func(t *testing.T, env platform.Env, cwd string) {
			dir := filepath.Join(cwd, "base", "ws")
			if err := os.MkdirAll(filepath.Join(dir, "session.conf"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "session"},
		{"project symlink to a missing target", func(t *testing.T, env platform.Env, cwd string) {
			file := filepath.Join(cwd, ".agents", "soho.conf")
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("missing-project.conf", file); err != nil {
				t.Fatal(err)
			}
		}, "project"},
		{"project path is a directory", func(t *testing.T, env platform.Env, cwd string) {
			file := filepath.Join(cwd, ".agents", "soho.conf")
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(file, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, cwd := localFixture(t)
			roWrite(t, filepath.Join(cwd, ".agents", "soho.conf"), "state_dir=base\n")
			tc.breakLayer(t, env, cwd)
			_, err := core.LoadLocalConfigReadOnly(env, cwd)
			if err == nil {
				t.Fatal("an unreadable layer was skipped")
			}
			if !strings.Contains(err.Error(), tc.layer+" configuration layer") {
				t.Fatalf("error %q does not name the %s layer", err, tc.layer)
			}
		})
	}

	// The control: a missing session file is an ordinary empty layer.
	env, _, cwd := localFixture(t)
	roWrite(t, filepath.Join(cwd, ".agents", "soho.conf"), "state_dir=base\n")
	if _, err := core.LoadLocalConfigReadOnly(env, cwd); err != nil {
		t.Fatalf("a missing session file was an error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "base", "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A session file that exists with no entries is readable, not unreadable.
	roWrite(t, filepath.Join(cwd, "base", "ws", "session.conf"), "# empty\n")
	if _, err := core.LoadLocalConfigReadOnly(env, cwd); err != nil {
		t.Fatalf("a comment-only session file was an error: %v", err)
	}
}

// TestLoadLocalConfigReadOnlyKeepsStateEnvWins: SOHO_DIR and SOHO_STATE_DIR
// still win over every config layer.
func TestLoadLocalConfigReadOnlyKeepsStateEnvWins(t *testing.T) {
	env, root, cwd := localFixture(t)
	roWrite(t, filepath.Join(cwd, ".agents", "soho.conf"), "state_dir=base\n")
	for name, want := range map[string]string{
		"SOHO_DIR":       filepath.Join(root, "soho-dir"),
		"SOHO_STATE_DIR": filepath.Join(root, "state-env"),
	} {
		scoped := env.Clone()
		scoped[name] = want
		ctx, err := core.LoadLocalConfigReadOnly(scoped, cwd)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantDir := filepath.Join(want, "ws")
		if got := core.StateDirPath(&ctx, scoped, cwd); got != wantDir {
			t.Fatalf("%s: StateDirPath=%q, want %q", name, got, wantDir)
		}
	}
}
