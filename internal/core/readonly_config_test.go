package core_test

// Focused regressions for the explicit read-only configuration seam
// (DJA-611): LoadReadOnlyConfig, StateRootPathForProject and the confined
// bounded layer reads. The external test package uses the committed
// inventory.ResolveMode as the real producer-facing consumer and the
// package's fakecli fakes (wired by core_test's TestMain) as the no-exec
// canaries. Fixtures pin HOME and USERPROFILE.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/inventory"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	skill "github.com/djalmajr/soho/skills/soho"
)

// roFixture returns a fresh fixture with HOME and USERPROFILE pinned at a
// fixture home and an empty bin directory for the fake CLI canaries.
func roFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME":         home,
		"USERPROFILE":  home,
		"APPDATA":      filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
		"PATH":         bin,
	}
	return env, root
}

func roWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func roRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// roInstallCanaries installs fake git/herdr/hc/soho executables on the
// fixture PATH: any exec routed through the passed env is recorded and
// can never pass quietly.
func roInstallCanaries(t *testing.T, bin string) {
	t.Helper()
	for _, name := range []string{"git", "herdr", "hc", "soho"} {
		if _, err := fakecli.Install(t, bin, name, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func roCanaryFired(t *testing.T, bin string) {
	t.Helper()
	for _, name := range []string{"git", "herdr", "hc", "soho"} {
		if _, err := os.Stat(filepath.Join(bin, name+".calls.jsonl")); err == nil {
			t.Fatalf("the read-only load executed %s: %s", name, filepath.Join(bin, name+".calls.jsonl"))
		}
	}
}

// roSnapshot hashes every fixture file (bytes plus size and kind metadata,
// symlink targets included) so a before/after comparison proves no write,
// creation or replacement.
func roSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "link:" + target
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:]) + " " + strconv.Itoa(len(data)) + " " + d.Type().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func roEnvUnchanged(t *testing.T, env, before platform.Env) {
	t.Helper()
	if !reflect.DeepEqual(env, before) {
		t.Fatal("the read-only load changed the passed environment")
	}
}

func roLayer(layers []core.ConfigLayer, source string) (core.ConfigLayer, bool) {
	for _, layer := range layers {
		if layer.Source == source {
			return layer, true
		}
	}
	return core.ConfigLayer{}, false
}

// roStaleMarkers poisons the caller context: stale Herdr/HC ids, a stale
// transport and poisoned Git routing variables. The loader must ignore
// all of them.
func roStaleMarkers(env platform.Env, root string) {
	env["HERDR_WORKSPACE_ID"] = "w-stale"
	env["HERDR_PANE_ID"] = "w-stale:p1"
	env["HERDR_TAB_ID"] = "tab-stale"
	env["HC_TERMINAL_ID"] = "hc:stale:1"
	env["SOHO_TRANSPORT"] = "hc"
	env["GIT_DIR"] = filepath.Join(root, "gitdir")
	env["GIT_WORK_TREE"] = root
	env["GIT_COMMON_DIR"] = filepath.Join(root, "gitdir")
}

// TestReadOnlyBootstrap: empty project root and session id load the
// defaults and the selected user only, use the embedded defaults when no
// skill source exists, create nothing, mutate nothing and execute nothing.
func TestReadOnlyBootstrapDefaultsUserOnly(t *testing.T) {
	env, root := roFixture(t)
	roInstallCanaries(t, filepath.Join(root, "bin"))
	roStaleMarkers(env, root)
	before := roEnvCopy(env)
	snap := roSnapshot(t, root)

	rc, err := core.LoadReadOnlyConfig(env, "", "", core.UserSelection{})
	if err != nil {
		t.Fatalf("bootstrap load: %v", err)
	}
	roEnvUnchanged(t, env, before)
	roCanaryFired(t, filepath.Join(root, "bin"))
	if got := roSnapshot(t, root); !reflect.DeepEqual(got, snap) {
		t.Fatalf("the bootstrap load created or changed fixture files: %v", diffMaps(got, snap))
	}

	if rc.ProjectRoot != "" || rc.BaseStateRoot != "" || rc.StateRoot != "" || rc.StateDir != "" {
		t.Fatalf("bootstrap must not compute project or state paths: %+v", rc)
	}
	layers := rc.Config.Layers
	if len(layers) != 2 {
		t.Fatalf("bootstrap layers = %d (%+v), want defaults and user only", len(layers), layers)
	}
	if layers[0].Source != "defaults" || layers[1].Source != "user" {
		t.Fatalf("bootstrap layer order = %q, %q; want defaults, user", layers[0].Source, layers[1].Source)
	}
	def := layers[0]
	if def.ReadError != nil {
		t.Fatalf("defaults layer read error: %v", def.ReadError)
	}
	if def.File != "" {
		t.Fatalf("the embedded defaults layer must carry no filesystem path, got %q", def.File)
	}
	want, err := skill.ReadConfigDefaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range core.ConfigTextPairs(want) {
		entry, ok := def.Entries[pair.Key]
		if !ok || entry.Value != pair.Value {
			t.Fatalf("defaults layer key %s = %+v, want %q from the embedded bytes", pair.Key, entry, pair.Value)
		}
	}
	if len(def.Entries) != len(core.ConfigTextPairs(want)) {
		t.Fatalf("defaults layer holds %d keys, the embedded bytes hold %d", len(def.Entries), len(core.ConfigTextPairs(want)))
	}
	user := layers[1]
	wantUserFile := platform.UserConfigPath(platform.Current(), env)
	if user.File != wantUserFile {
		t.Fatalf("user layer file = %q, want the pinned-HOME user config path %q", user.File, wantUserFile)
	}
	if user.ReadError != nil || len(user.Entries) != 0 {
		t.Fatalf("absent user config is an ordinary empty layer, got %+v", user)
	}
	if strings.Contains(strings.Join(rc.Config.Sources, ","), "user") {
		t.Fatalf("an absent user layer is not a read source: %v", rc.Config.Sources)
	}
	if !rc.Config.User.IsZero() {
		t.Fatalf("Config.User must retain the selection, got %+v", rc.Config.User)
	}
	// The absent skill cache is not materialized by the bootstrap.
	if _, err := os.Stat(cacheBaseOf(t, env)); !os.IsNotExist(err) {
		t.Fatalf("the bootstrap created the skill cache base")
	}
}

func roEnvCopy(env platform.Env) platform.Env {
	out := make(platform.Env, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func homeOf(t *testing.T, env platform.Env) string {
	t.Helper()
	if v := env["HOME"]; v != "" {
		return v
	}
	t.Fatal("the fixture must pin HOME")
	return ""
}

func userConfigFileOf(t *testing.T, env platform.Env) string {
	t.Helper()
	return platform.UserConfigPath(platform.Current(), env)
}

func cacheBaseOf(t *testing.T, env platform.Env) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		if cache := env["LOCALAPPDATA"]; cache != "" {
			return cache
		}
		return filepath.Join(homeOf(t, env), "AppData", "Local")
	}
	if cache := env["XDG_CACHE_HOME"]; cache != "" {
		return cache
	}
	return filepath.Join(homeOf(t, env), ".cache")
}

func diffMaps(a, b map[string]string) string {
	var sb strings.Builder
	for k, v := range a {
		if b[k] != v {
			sb.WriteString("+" + k + " " + v + "\n")
		}
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			sb.WriteString("-" + k + " " + v + "\n")
		}
	}
	return sb.String()
}

// TestReadOnlyForeignRootSession: a foreign known root and session load
// under stale, conflicting caller markers; the root is canonicalized
// without Git; the session file comes from the explicit id under the base
// state root; the committed consumer resolves the lower-only mode.
func TestReadOnlyForeignRootSession(t *testing.T) {
	env, root := roFixture(t)
	bin := filepath.Join(root, "bin")
	roInstallCanaries(t, bin)
	roStaleMarkers(env, root)

	foreign := filepath.Join(root, "foreign")
	roWrite(t, filepath.Join(foreign, ".agents", "soho.conf"), "inventory=show\ninventory_name=acme\n")
	roWrite(t, filepath.Join(foreign, ".soho", "w1", "session.conf"), "inventory=hidden\n")
	roWrite(t, userConfigFileOf(t, env), "max_workers=2\n")

	link := filepath.Join(root, "link-foreign")
	if err := os.Symlink(foreign, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(foreign)
	if err != nil {
		t.Fatal(err)
	}

	before := roEnvCopy(env)
	snap := roSnapshot(t, root)
	rc, err := core.LoadReadOnlyConfig(env, link, "w1", core.UserSelection{})
	if err != nil {
		t.Fatalf("foreign root/session load: %v", err)
	}
	roEnvUnchanged(t, env, before)
	roCanaryFired(t, bin)
	if got := roSnapshot(t, root); !reflect.DeepEqual(got, snap) {
		t.Fatalf("the load created or changed fixture files: %s", diffMaps(got, snap))
	}

	if rc.ProjectRoot != canonical {
		t.Fatalf("ProjectRoot = %q, want the canonical root %q", rc.ProjectRoot, canonical)
	}
	layers := rc.Config.Layers
	if len(layers) != 4 {
		t.Fatalf("layers = %d (%+v), want defaults, user, project, session", len(layers), layers)
	}
	if layers[0].Source != "defaults" || layers[1].Source != "user" || layers[2].Source != "project" || layers[3].Source != "session" {
		t.Fatalf("layer order = %v", []string{layers[0].Source, layers[1].Source, layers[2].Source, layers[3].Source})
	}
	project, _ := roLayer(layers, "project")
	if project.File != filepath.Join(canonical, ".agents", "soho.conf") {
		t.Fatalf("project layer file = %q, want that root's own .agents/soho.conf", project.File)
	}
	if project.ReadError != nil {
		t.Fatalf("project layer read error: %v", project.ReadError)
	}
	if v := project.Entries["inventory"].Value; v != "show" {
		t.Fatalf("project inventory = %q", v)
	}
	session, _ := roLayer(layers, "session")
	wantSession := filepath.Join(canonical, ".soho", "w1", "session.conf")
	if session.File != wantSession {
		t.Fatalf("session layer file = %q, want %q (the explicit id under the base state root, not the stale marker)", session.File, wantSession)
	}
	if session.ReadError != nil {
		t.Fatalf("session layer read error: %v", session.ReadError)
	}
	if v := session.Entries["inventory"].Value; v != "hidden" {
		t.Fatalf("session inventory = %q", v)
	}
	if v := rc.Config.Entries["inventory"].Value; v != "hidden" {
		t.Fatalf("merged inventory = %q, want the session layer's value", v)
	}
	if v := rc.Config.Entries["max_workers"].Value; v != "2" {
		t.Fatalf("user layer entry max_workers = %q, want the selected user file's value", v)
	}
	if rc.BaseStateRoot != filepath.Join(canonical, ".soho") {
		t.Fatalf("BaseStateRoot = %q, want the default .soho under the canonical root", rc.BaseStateRoot)
	}
	if rc.StateRoot != rc.BaseStateRoot {
		t.Fatalf("StateRoot = %q, want the base root (the session does not redirect)", rc.StateRoot)
	}
	if rc.StateDir != filepath.Join(rc.StateRoot, "w1") {
		t.Fatalf("StateDir = %q, want StateRoot/w1", rc.StateDir)
	}
	if mode := inventory.ResolveMode(&rc.Config, env); mode != inventory.ModeHidden {
		t.Fatalf("the committed consumer resolves %q, want hidden (project show lowered by the session)", mode)
	}
}

// TestReadOnlySessionRedirect: the base state root locates the session
// file; the session's state_dir redirects the effective root to an
// external absolute path; no second session file is ever read.
func TestReadOnlySessionRedirect(t *testing.T) {
	env, root := roFixture(t)
	bin := filepath.Join(root, "bin")
	roInstallCanaries(t, bin)
	roStaleMarkers(env, root)

	foreign := filepath.Join(root, "foreign")
	roWrite(t, filepath.Join(foreign, ".agents", "soho.conf"), "inventory=show\n")
	external := filepath.Join(root, "external")
	// The real session file redirects the effective state root outward.
	roWrite(t, filepath.Join(foreign, ".soho", "w1", "session.conf"), "state_dir="+external+"\n")
	// A decoy session under the redirected root: if the loader chased a
	// second session file it would read this and close the mode.
	roWrite(t, filepath.Join(external, "w1", "session.conf"), "inventory=off\nargs_decoy=decoy\n")

	canonical, err := filepath.EvalSymlinks(foreign)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := core.LoadReadOnlyConfig(env, foreign, "w1", core.UserSelection{})
	if err != nil {
		t.Fatalf("redirect load: %v", err)
	}
	roCanaryFired(t, bin)

	if rc.BaseStateRoot != filepath.Join(canonical, ".soho") {
		t.Fatalf("BaseStateRoot = %q, want the pre-session root under the canonical project", rc.BaseStateRoot)
	}
	if rc.StateRoot != external {
		t.Fatalf("StateRoot = %q, want the session's external state_dir %q", rc.StateRoot, external)
	}
	if rc.StateDir != filepath.Join(external, "w1") {
		t.Fatalf("StateDir = %q, want the redirected root plus the session", rc.StateDir)
	}
	session, _ := roLayer(rc.Config.Layers, "session")
	if session.File != filepath.Join(rc.BaseStateRoot, "w1", "session.conf") {
		t.Fatalf("session layer file = %q, want the base-state-root session file", session.File)
	}
	if _, ok := rc.Config.Entries["args_decoy"]; ok {
		t.Fatalf("the decoy session under the redirected root was read; the loader must read at most one session file")
	}
	if v := session.Entries["state_dir"].Value; v != external {
		t.Fatalf("session state_dir = %q", v)
	}
	if mode := inventory.ResolveMode(&rc.Config, env); mode != inventory.ModeShow {
		t.Fatalf("the committed consumer resolves %q, want show (the decoy would close it)", mode)
	}
}

// TestReadOnlyUserSelectionConventions: the selected user path on both
// conventions (an explicit base and the environment's home) and the
// platform path arithmetic the selection delegates to.
func TestReadOnlyUserSelectionConventions(t *testing.T) {
	env, root := roFixture(t)
	bin := filepath.Join(root, "bin")
	roInstallCanaries(t, bin)

	foreign := filepath.Join(root, "foreign")
	roWrite(t, filepath.Join(foreign, ".agents", "soho.conf"), "inventory=show\n")

	// (a) An explicit base selects <base>/soho/config on every platform.
	base := filepath.Join(root, "userbase")
	roWrite(t, filepath.Join(base, "soho", "config"), "inventory=hidden\n")
	sel := core.UserSelection{Base: base}
	rc, err := core.LoadReadOnlyConfig(env, foreign, "", sel)
	if err != nil {
		t.Fatalf("explicit-base load: %v", err)
	}
	user, _ := roLayer(rc.Config.Layers, "user")
	if user.File != filepath.Join(base, "soho", "config") {
		t.Fatalf("user layer file = %q, want the explicit base's file", user.File)
	}
	if v := user.Entries["inventory"].Value; v != "hidden" {
		t.Fatalf("explicit-base user layer inventory = %q", v)
	}
	if rc.Config.User.Base != base {
		t.Fatalf("Config.User = %+v, want the selection retained", rc.Config.User)
	}
	if mode := inventory.ResolveMode(&rc.Config, env); mode != inventory.ModeHidden {
		t.Fatalf("explicit-base consumer mode = %q, want hidden", mode)
	}

	// (b) Without a base the environment's home selects the host
	// convention's user file, exactly the one the normal loader reads.
	home := homeOf(t, env)
	homeUserFile := platform.UserConfigPath(platform.Current(), env)
	roWrite(t, homeUserFile, "inventory=off\n")
	rc, err = core.LoadReadOnlyConfig(env, foreign, "", core.UserSelection{Home: home})
	if err != nil {
		t.Fatalf("home-selection load: %v", err)
	}
	user, _ = roLayer(rc.Config.Layers, "user")
	if user.File != homeUserFile {
		t.Fatalf("home-selection user layer file = %q, want %q", user.File, homeUserFile)
	}
	if rc.Config.User.Home != home {
		t.Fatalf("Config.User = %+v, want the home selection retained", rc.Config.User)
	}
	if mode := inventory.ResolveMode(&rc.Config, env); mode != inventory.ModeOff {
		t.Fatalf("home-selection consumer mode = %q, want off", mode)
	}

	// (c) The selection delegates to the platform path arithmetic on both
	// conventions (pure, host-independent).
	winEnv := platform.Env{"APPDATA": `C:\Users\d\AppData\Roaming`}
	if got := platform.UserConfigPath("win32", winEnv); got != filepath.Join(`C:\Users\d\AppData\Roaming`, "soho", "config") {
		t.Fatalf("win32 APPDATA user path = %q", got)
	}
	winHome := platform.Env{"USERPROFILE": `C:\Users\d`}
	if got := platform.UserConfigPath("win32", winHome); got != filepath.Join(`C:\Users\d`, "AppData", "Roaming", "soho", "config") {
		t.Fatalf("win32 USERPROFILE user path = %q", got)
	}
	if got := platform.UserConfigPath("win32", platform.Env{"XDG_CONFIG_HOME": `/x/xdg`, "APPDATA": `C:\a`}); got != filepath.Join(`/x/xdg`, "soho", "config") {
		t.Fatalf("XDG must precede APPDATA on win32 = %q", got)
	}
	unixEnv := platform.Env{"HOME": "/h"}
	if got := platform.UserConfigPath("linux", unixEnv); got != filepath.Join("/h", ".config", "soho", "config") {
		t.Fatalf("unix HOME user path = %q", got)
	}
	if got := platform.UserConfigPath("darwin", platform.Env{"XDG_CONFIG_HOME": "/xdg", "HOME": "/h"}); got != filepath.Join("/xdg", "soho", "config") {
		t.Fatalf("unix XDG precedence = %q", got)
	}
}

// TestReadOnlyMissingFiles: absent user, project and session files are
// ordinary empty layers; the roots are still computed; the consumer closes.
func TestReadOnlyMissingFiles(t *testing.T) {
	env, root := roFixture(t)
	roInstallCanaries(t, filepath.Join(root, "bin"))

	foreign := filepath.Join(root, "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(foreign)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := core.LoadReadOnlyConfig(env, foreign, "w1", core.UserSelection{})
	if err != nil {
		t.Fatalf("missing-files load: %v", err)
	}
	for _, source := range []string{"user", "project", "session"} {
		layer, ok := roLayer(rc.Config.Layers, source)
		if !ok {
			t.Fatalf("the %s layer must be recorded even when the file is absent", source)
		}
		if layer.ReadError != nil {
			t.Fatalf("%s layer read error for an absent file: %v", source, layer.ReadError)
		}
		if len(layer.Entries) != 0 {
			t.Fatalf("%s layer holds entries for an absent file: %+v", source, layer.Entries)
		}
	}
	if _, ok := roLayer(rc.Config.Layers, "defaults"); !ok {
		t.Fatal("the defaults layer is missing")
	}
	if rc.BaseStateRoot != filepath.Join(canonical, ".soho") || rc.StateRoot != filepath.Join(canonical, ".soho") || rc.StateDir != filepath.Join(canonical, ".soho", "w1") {
		t.Fatalf("roots with absent layers = %q, %q, %q", rc.BaseStateRoot, rc.StateRoot, rc.StateDir)
	}
	if mode := inventory.ResolveMode(&rc.Config, env); mode != inventory.ModeOff {
		t.Fatalf("absent project file resolves %q, want off", mode)
	}
}

// TestReadOnlyRefusalsPreserveTargets: symlink, directory, FIFO and
// oversized layers are refused with the layer's ReadError, fail closed in
// the committed consumer and leave the outside targets byte-identical.
func TestReadOnlyRefusalsPreserveTargets(t *testing.T) {
	oversizedProject := "inventory=show\n" + strings.Repeat("x", (1<<20)-14)
	oversizedSession := "inventory=show\n" + strings.Repeat("y", (64<<10)-14)
	exactProject := "a=" + strings.Repeat("x", (1<<20)-2)
	exactSession := "b=" + strings.Repeat("y", (64<<10)-2)

	cases := []struct {
		name    string
		setup   func(t *testing.T, env platform.Env, root, foreign, outside string)
		kind    string // which layer the refusal hits
		wantKey string // the entry that must NOT appear
	}{
		{"project symlink escapes the root", func(t *testing.T, env platform.Env, root, foreign, outside string) {
			roWrite(t, outside, "inventory=show\n")
			os.MkdirAll(filepath.Join(foreign, ".agents"), 0o755)
			if err := os.Symlink(outside, filepath.Join(foreign, ".agents", "soho.conf")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}, "project", "inventory"},
		{"agents directory is a symlink", func(t *testing.T, env platform.Env, root, foreign, outside string) {
			roWrite(t, filepath.Join(root, "other-agents", "soho.conf"), "inventory=show\n")
			os.MkdirAll(foreign, 0o755)
			if err := os.Symlink(filepath.Join(root, "other-agents"), filepath.Join(foreign, ".agents")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}, "project", "inventory"},
		{"project config is a directory", func(t *testing.T, env platform.Env, root, foreign, outside string) {
			os.MkdirAll(filepath.Join(foreign, ".agents", "soho.conf"), 0o755)
		}, "project", "inventory"},
		{"project config is a fifo", func(t *testing.T, env platform.Env, root, foreign, outside string) {
			if err := os.MkdirAll(filepath.Join(foreign, ".agents"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := roMkfifo(filepath.Join(foreign, ".agents", "soho.conf")); err != nil {
				t.Skipf("no fifos on this system: %v", err)
			}
		}, "project", "inventory"},
		{"project config oversized", func(t *testing.T, env platform.Env, root, foreign, outside string) {
			roWrite(t, filepath.Join(foreign, ".agents", "soho.conf"), oversizedProject)
		}, "project", "inventory"},
		{"session config oversized", func(t *testing.T, env platform.Env, root, foreign, outside string) {
			roWrite(t, filepath.Join(foreign, ".agents", "soho.conf"), "inventory=show\n")
			roWrite(t, filepath.Join(foreign, ".soho", "w1", "session.conf"), oversizedSession)
		}, "session", "inventory"},
		{"user config oversized", func(t *testing.T, env platform.Env, root, foreign, outside string) {
			os.MkdirAll(foreign, 0o755)
			roWrite(t, userConfigFileOf(t, env), oversizedProject)
		}, "user", "inventory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, root := roFixture(t)
			roInstallCanaries(t, filepath.Join(root, "bin"))
			roStaleMarkers(env, root)
			foreign := filepath.Join(root, "foreign")
			outside := filepath.Join(root, "outside")
			roWrite(t, outside, "inventory=show\n")

			tc.setup(t, env, root, foreign, outside)
			beforeOutside := roRead(t, outside)

			rc, err := core.LoadReadOnlyConfig(env, foreign, "w1", core.UserSelection{})
			if err != nil {
				t.Fatalf("refusals are recorded as layer errors, not returned: %v", err)
			}
			roCanaryFired(t, filepath.Join(root, "bin"))
			if got := roRead(t, outside); got != beforeOutside {
				t.Fatalf("the outside target changed: %q -> %q", beforeOutside, got)
			}
			layer, ok := roLayer(rc.Config.Layers, tc.kind)
			if !ok {
				t.Fatalf("the %s layer is missing", tc.kind)
			}
			if layer.ReadError == nil {
				t.Fatalf("the %s layer must record a read error for %s", tc.kind, tc.name)
			}
			if v, present := layer.Entries[tc.wantKey]; present {
				t.Fatalf("the refused layer contributed an entry: %q", v)
			}
			if mode := inventory.ResolveMode(&rc.Config, env); mode != inventory.ModeOff {
				t.Fatalf("the committed consumer resolves %q, want off (fail closed)", mode)
			}
		})
	}

	// Boundary: a layer of exactly the bound still loads.
	t.Run("boundaries", func(t *testing.T) {
		env, root := roFixture(t)
		foreign := filepath.Join(root, "foreign")
		roWrite(t, filepath.Join(foreign, ".agents", "soho.conf"), exactProject)
		roWrite(t, filepath.Join(foreign, ".soho", "w1", "session.conf"), exactSession)
		rc, err := core.LoadReadOnlyConfig(env, foreign, "w1", core.UserSelection{})
		if err != nil {
			t.Fatal(err)
		}
		project, _ := roLayer(rc.Config.Layers, "project")
		if project.ReadError != nil {
			t.Fatalf("a project layer of exactly 1MiB must load, got %v", project.ReadError)
		}
		if len(project.Entries["a"].Value) != (1<<20)-2 {
			t.Fatalf("the exact-bound project value was truncated")
		}
		session, _ := roLayer(rc.Config.Layers, "session")
		if session.ReadError != nil {
			t.Fatalf("a session layer of exactly 64KiB must load, got %v", session.ReadError)
		}
		if len(session.Entries["b"].Value) != (64<<10)-2 {
			t.Fatalf("the exact-bound session value was truncated")
		}
	})
}

// TestReadOnlyDefaultsSourceRefusal: an explicitly selected existing
// skill source that cannot be safely read records the failure instead of
// fabricating the embedded source.
func TestReadOnlyDefaultsSourceRefusal(t *testing.T) {
	env, root := roFixture(t)
	roInstallCanaries(t, filepath.Join(root, "bin"))
	skillDir := filepath.Join(root, "skill")
	roWrite(t, filepath.Join(skillDir, "config.defaults"), "inventory=show\n"+strings.Repeat("z", (1<<20)-1))
	env["SOHO_SKILL_DIR"] = skillDir

	rc, err := core.LoadReadOnlyConfig(env, "", "", core.UserSelection{})
	if err != nil {
		t.Fatalf("defaults refusal must not be returned: %v", err)
	}
	def, _ := roLayer(rc.Config.Layers, "defaults")
	if def.ReadError == nil {
		t.Fatal("an oversized existing defaults source must record its read error")
	}
	if def.File != filepath.Join(skillDir, "config.defaults") {
		t.Fatalf("defaults layer file = %q, want the selected source's file", def.File)
	}
	if len(def.Entries) != 0 {
		t.Fatalf("the refused source contributed entries: %+v", def.Entries)
	}
}

// TestReadOnlyArgumentAndRootErrors: argument and root errors are returned
// before any I/O, without printing.
func TestReadOnlyArgumentAndRootErrors(t *testing.T) {
	env, root := roFixture(t)
	snap := roSnapshot(t, root)
	before := roEnvCopy(env)

	cases := []struct {
		name        string
		root, sessn string
		want        string
	}{
		{"session without root", "", "w1", "readonly config: a session id requires a project root"},
		{"relative root", "a/rel", "", "readonly config: the project root must be an absolute path"},
		{"missing root", filepath.Join(root, "absent"), "", "readonly config: the project root does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := core.LoadReadOnlyConfig(env, tc.root, tc.sessn, core.UserSelection{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	for _, id := range []string{"", ".", "..", "a/b", `a\b`, "CON", "nul.txt", "w.", "w ", "w:1", "w*"} {
		_, err := core.LoadReadOnlyConfig(env, root, id, core.UserSelection{})
		if id == "" && err != nil {
			t.Fatalf("the empty session id must not be refused: %v", err)
		}
		if id != "" && err == nil {
			t.Fatalf("session id %q was accepted; want a refusal before any I/O", id)
		}
	}
	if _, err := core.LoadReadOnlyConfig(env, root, "w-1_ok.9", core.UserSelection{}); err != nil {
		t.Fatalf("a valid session id is refused: %v", err)
	}

	// The argument and id refusals above must have touched nothing; the
	// root-kind cases below create their own fixture entries on purpose.
	roEnvUnchanged(t, env, before)
	if got := roSnapshot(t, root); !reflect.DeepEqual(got, snap) {
		t.Fatalf("the refused loads touched the fixture: %s", diffMaps(got, snap))
	}

	fileRoot := filepath.Join(root, "a-file")
	roWrite(t, fileRoot, "x\n")
	if _, err := core.LoadReadOnlyConfig(env, fileRoot, "", core.UserSelection{}); err == nil || !strings.Contains(err.Error(), "the project root is not a directory") {
		t.Fatalf("file root error = %v", err)
	}
	broken := filepath.Join(root, "broken-link")
	if err := os.Symlink(filepath.Join(root, "nowhere"), broken); err == nil {
		if _, err := core.LoadReadOnlyConfig(env, broken, "", core.UserSelection{}); err == nil || !strings.Contains(err.Error(), "the project root cannot be canonicalized") {
			t.Fatalf("broken-symlink root error = %v", err)
		}
	}
	roEnvUnchanged(t, env, before)
}

// TestReadOnlyResolveModeLowerOnly: the seam's layers feed the committed
// lower-only consumer over the full matrix of restrictive sources.
func TestReadOnlyResolveModeLowerOnly(t *testing.T) {
	projects := map[string]string{"show": "inventory=show\n", "hidden": "inventory=hidden\n", "invalid": "inventory=bogus\n", "absent": ""}
	restrictors := map[string]func(t *testing.T, env platform.Env, foreign string){
		"none": func(t *testing.T, env platform.Env, foreign string) {},
		"user hidden": func(t *testing.T, env platform.Env, foreign string) {
			roWrite(t, userConfigFileOf(t, env), "inventory=hidden\n")
		},
		"user off": func(t *testing.T, env platform.Env, foreign string) {
			roWrite(t, userConfigFileOf(t, env), "inventory=off\n")
		},
		"user bogus": func(t *testing.T, env platform.Env, foreign string) {
			roWrite(t, userConfigFileOf(t, env), "inventory=bogus\n")
		},
		"session off": func(t *testing.T, env platform.Env, foreign string) {
			roWrite(t, filepath.Join(foreign, ".soho", "w1", "session.conf"), "inventory=off\n")
		},
	}
	for project, projectText := range projects {
		for restr, apply := range restrictors {
			for _, envLower := range []string{"", "off", "hidden"} {
				name := project + "/" + restr + "/env=" + envLower
				t.Run(name, func(t *testing.T) {
					env, root := roFixture(t)
					if envLower != "" {
						env["SOHO_INVENTORY"] = envLower
					}
					foreign := filepath.Join(root, "foreign")
					os.MkdirAll(foreign, 0o755)
					if projectText != "" {
						roWrite(t, filepath.Join(foreign, ".agents", "soho.conf"), projectText)
					}
					apply(t, env, foreign)

					rc, err := core.LoadReadOnlyConfig(env, foreign, "w1", core.UserSelection{})
					if err != nil {
						t.Fatalf("load: %v", err)
					}
					mode := inventory.ResolveMode(&rc.Config, env)
					want := wantMode(project, restr, envLower)
					if mode != want {
						t.Fatalf("ResolveMode = %q, want %q", mode, want)
					}
				})
			}
		}
	}
	// Only the project file opens: a user show with no project stays off.
	t.Run("user show cannot open", func(t *testing.T) {
		env, root := roFixture(t)
		roWrite(t, userConfigFileOf(t, env), "inventory=show\n")
		foreign := filepath.Join(root, "foreign")
		os.MkdirAll(foreign, 0o755)
		rc, err := core.LoadReadOnlyConfig(env, foreign, "", core.UserSelection{})
		if err != nil {
			t.Fatal(err)
		}
		if mode := inventory.ResolveMode(&rc.Config, env); mode != inventory.ModeOff {
			t.Fatalf("a user show with no project resolves %q, want off", mode)
		}
	})
}

func wantMode(project, restr, envLower string) inventory.Mode {
	base := inventory.ModeOff
	if project == "show" {
		base = inventory.ModeShow
	}
	if project == "hidden" {
		base = inventory.ModeHidden
	}
	if project == "invalid" {
		return inventory.ModeOff
	}
	rank := func(m inventory.Mode) int {
		switch m {
		case inventory.ModeShow:
			return 2
		case inventory.ModeHidden:
			return 1
		default:
			return 0
		}
	}
	lower := func(m inventory.Mode) {
		if rank(m) < rank(base) {
			base = m
		}
	}
	switch restr {
	case "user hidden":
		lower(inventory.ModeHidden)
	case "user off":
		lower(inventory.ModeOff)
	case "user bogus":
		return inventory.ModeOff
	case "session off":
		lower(inventory.ModeOff)
	}
	switch envLower {
	case "off":
		lower(inventory.ModeOff)
	case "hidden":
		lower(inventory.ModeHidden)
	}
	return base
}

// TestReadOnlyNowriteReadonly: with SOHO_NOWRITE both unset and set the
// load leaves the fixture byte-identical and creates no state, lock,
// gitignore or cache artifact.
func TestReadOnlyNowriteReadonly(t *testing.T) {
	for _, nowrite := range []string{"", "1"} {
		t.Run("nowrite="+nowrite, func(t *testing.T) {
			env, root := roFixture(t)
			roInstallCanaries(t, filepath.Join(root, "bin"))
			roStaleMarkers(env, root)
			if nowrite != "" {
				env["SOHO_NOWRITE"] = nowrite
			}
			foreign := filepath.Join(root, "foreign")
			roWrite(t, filepath.Join(foreign, ".agents", "soho.conf"), "inventory=show\n")
			roWrite(t, filepath.Join(foreign, ".soho", "w1", "session.conf"), "inventory=hidden\n")

			absent := []string{
				cacheBaseOf(t, env),
				filepath.Join(foreign, ".gitignore"),
				filepath.Join(foreign, ".soho", "w1", "agents.lock"),
				filepath.Join(foreign, ".soho", "w1", "agents.tsv"),
				filepath.Join(foreign, ".soho", "w1", "briefs"),
			}
			snap := roSnapshot(t, root)
			rc, err := core.LoadReadOnlyConfig(env, foreign, "w1", core.UserSelection{})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if rc.Config.Entries["inventory"].Value != "hidden" {
				t.Fatalf("the load did not read the layers: %q", rc.Config.Entries["inventory"].Value)
			}
			if got := roSnapshot(t, root); !reflect.DeepEqual(got, snap) {
				t.Fatalf("the load changed fixture bytes or metadata: %s", diffMaps(got, snap))
			}
			for _, path := range absent {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("the load created %s", path)
				}
			}
			roCanaryFired(t, filepath.Join(root, "bin"))
		})
	}
}

// TestReadOnlyAbsentSkillCacheEmbedded: with no installed skill and no
// cache the defaults layer is the embedded bytes and nothing is created.
func TestReadOnlyAbsentSkillCacheEmbedded(t *testing.T) {
	env, _ := roFixture(t)
	_, err := core.LoadReadOnlyConfig(env, "", "", core.UserSelection{})
	if err != nil {
		t.Fatal(err)
	}
	cache := cacheBaseOf(t, env)
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("the load materialized the skill cache at %s", cache)
	}
}

// TestReadOnlyRaceReplacement: a concurrent replacement of the project
// config by a symlink to an outside file must never surface the outside
// content in a successful load and must never modify the outside target:
// each load either reads the original file's bytes or is refused.
func TestReadOnlyRaceReplacement(t *testing.T) {
	env, root := roFixture(t)
	roInstallCanaries(t, filepath.Join(root, "bin"))
	foreign := filepath.Join(root, "foreign")
	target := filepath.Join(foreign, ".agents", "soho.conf")
	outside := filepath.Join(root, "outside.conf")
	original := "inventory=show\n"
	roWrite(t, target, original)
	roWrite(t, outside, "inventory=hidden\n")
	if err := os.Symlink(outside, filepath.Join(root, "probe-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	os.Remove(filepath.Join(root, "probe-link"))

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(target)
			_ = os.Symlink(outside, target)
			_ = os.Remove(target)
			_ = os.WriteFile(target, []byte(original), 0o600)
		}
	}()

	for i := 0; i < 200; i++ {
		rc, err := core.LoadReadOnlyConfig(env, foreign, "", core.UserSelection{})
		if err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
		layer, _ := roLayer(rc.Config.Layers, "project")
		if layer.ReadError != nil {
			continue // a replacement inside the window is refused; the outside target still must be untouched
		}
		v := layer.Entries["inventory"].Value
		if v == "" {
			if len(layer.Entries) != 0 {
				t.Fatalf("load %d: an empty layer holds entries: %+v", i, layer.Entries)
			}
			continue // the file was absent mid-load: an ordinary empty layer
		}
		if v != "show" {
			t.Fatalf("load %d read the replacement content %q; only the original file's bytes may load", i, v)
		}
	}
	if got := roRead(t, outside); got != "inventory=hidden\n" {
		t.Fatalf("the outside target changed: %q", got)
	}
	roCanaryFired(t, filepath.Join(root, "bin"))
}

// TestStateRootPathForProjectDelegation: the exported helper is the pure
// root-relative arithmetic (SOHO_DIR first, then the state_dir setting)
// and StateRootPath delegates to it over the existing root resolution.
func TestStateRootPathForProjectDelegation(t *testing.T) {
	env, root := roFixture(t)
	foreign := filepath.Join(root, "foreign")
	os.MkdirAll(foreign, 0o755)
	// A non-Git directory: the existing StateProjectRoot falls back to the
	// directory itself, so the delegation equality is exact.
	empty := &core.Config{}
	if got := core.StateRootPath(empty, env, foreign); got != core.StateRootPathForProject(empty, env, foreign) {
		t.Fatalf("StateRootPath = %q, helper = %q; the delegation must be exact", got, core.StateRootPathForProject(empty, env, foreign))
	}

	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"state_dir": {Value: "custom", Source: "project"}}}
	if got := core.StateRootPathForProject(ctx, env, foreign); got != filepath.Join(foreign, "custom") {
		t.Fatalf("relative state_dir = %q, want the root's custom dir", got)
	}
	external := filepath.Join(root, "external-state")
	ctx = &core.Config{Entries: map[string]core.ConfigEntry{"state_dir": {Value: external, Source: "session"}}}
	if got := core.StateRootPathForProject(ctx, env, foreign); got != external {
		t.Fatalf("external absolute state_dir = %q, want it preserved", got)
	}
	if got := core.StateRootPathForProject(ctx, platform.Env{"SOHO_DIR": external}, foreign); got != external {
		t.Fatalf("SOHO_DIR must win over the state_dir setting")
	}
	stateOverride := filepath.Join(root, "sd")
	if got := core.StateRootPathForProject(ctx, platform.Env{"SOHO_STATE_DIR": stateOverride}, foreign); got != stateOverride {
		t.Fatalf("SOHO_STATE_DIR must win inside the setting: got %q want %q", got, stateOverride)
	}
	// A defaults-layer state_dir does not override the default.
	ctx = &core.Config{Entries: map[string]core.ConfigEntry{"state_dir": {Value: "ignored", Source: "defaults"}}}
	if got := core.StateRootPathForProject(ctx, env, foreign); got != filepath.Join(foreign, ".soho") {
		t.Fatalf("a defaults-only state_dir = %q, want the default .soho", got)
	}
}
