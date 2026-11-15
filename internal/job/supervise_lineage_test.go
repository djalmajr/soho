package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

// TestUseLaunchSelectionLineage: a bound supervisor hands the selection to
// its team commands only; the git sync and the Herdr adapter keep the
// environment as it was (no private name, the original generic selectors).
func TestUseLaunchSelectionLineage(t *testing.T) {
	root := t.TempDir()
	store := Open(root)
	if _, err := store.Start("job-1", []byte(supTestBrief)); err != nil {
		t.Fatal(err)
	}
	dir, err := Dir(root, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	editState(t, dir, func(s *State) {
		s.Status, s.Dir, s.Branch, s.Base, s.Modo = StatusRunning, worktree, "job/job-1", "main", "worktree"
	})
	env := platform.Env{"XDG_CONFIG_HOME": "/server/xdg", "HOME": "/server/home", "USERPROFILE": "/server/home", "APPDATA": "/server/appdata", "PATH": "/usr/bin"}
	sup, err := NewSupervisor(store, "job-1", env, "/bin/soho", nil)
	if err != nil {
		t.Fatal(err)
	}
	sel := core.UserSelection{Base: "/starter/config", Home: "/starter/home"}
	sup.UseLaunchSelection(sel)
	if !sup.PrivateLaunch {
		t.Fatal("PrivateLaunch is not set")
	}
	ops, ok := sup.Ops.(selfCLI)
	if !ok {
		t.Fatalf("Ops is %T", sup.Ops)
	}
	if ops.Env[launchConfigBaseVar] != sel.Base || ops.Env[launchHomeVar] != sel.Home {
		t.Fatalf("team command env misses the selection: %v", ops.Env)
	}
	check := func(what string, got platform.Env) {
		t.Helper()
		for _, name := range []string{launchConfigBaseVar, launchHomeVar} {
			if _, ok := got[name]; ok {
				t.Fatalf("%s env carries %s", what, name)
			}
		}
		for name, value := range env {
			if got[name] != value {
				t.Fatalf("%s env %s=%q, want the original %q", what, name, got[name], value)
			}
		}
	}
	for name, value := range env {
		if ops.Env[name] != value {
			t.Fatalf("team command env %s=%q, want the original %q", name, ops.Env[name], value)
		}
	}
	if sup.Git == nil {
		t.Fatal("no git sync for a branch job")
	}
	check("git sync", sup.Git.Env)
	herdr, ok := sup.Herdr.(*herdrCLI)
	if !ok {
		t.Fatalf("Herdr is %T", sup.Herdr)
	}
	check("herdr", herdr.Env)
	check("input", env)
}

// TestWakeHookEnvironmentIsTheServers: the wake command a bound supervisor
// runs is a real process whose environment has no private name and every
// generic selector exactly as the server's — the hook gets the environment
// cli.Run built without the names (WithoutLaunchLocators), and binding the
// supervisor to the selection does not touch it.
func TestWakeHookEnvironmentIsTheServers(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := Open(root)
	if _, err := store.Start("job-1", []byte(supTestBrief)); err != nil {
		t.Fatal(err)
	}
	dir, err := Dir(root, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	editState(t, dir, func(s *State) { s.Status, s.Dir, s.Modo = StatusRunning, t.TempDir(), "workspace" })
	dump := filepath.Join(t.TempDir(), "wake-env.txt")
	starterRoot, starterSep := nativeRoot(t), string(filepath.Separator)
	starterConfig := starterRoot + "starter" + starterSep + "config"
	starterHome := starterRoot + "starter" + starterSep + "home"
	server := platform.Env{
		"XDG_CONFIG_HOME": "/server/xdg", "HOME": "/server/home", "USERPROFILE": "/server/home", "APPDATA": "/server/appdata",
		"PATH": testutil.HostPath(), managedHelperVar: "envdump", "JOB_ENV_DUMP": dump,
		launchConfigBaseVar: starterConfig, launchHomeVar: starterHome,
	}
	sel := ReadLaunchLocators(server).Selection
	env := WithoutLaunchLocators(server)
	sup, err := NewSupervisor(store, "job-1", env, self, nil)
	if err != nil {
		t.Fatal(err)
	}
	sup.Wake = &WakeHook{Cmd: self, JobID: "job-1", Env: env, Timeout: 30 * time.Second, Delays: []time.Duration{}}
	sup.UseLaunchSelection(sel)
	if attempts := sup.Wake.Run(Event{Seq: 1, Tipo: "terminal", Refs: map[string]string{}}); attempts != 1 {
		t.Fatalf("wake attempts = %d", attempts)
	}
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("the wake command did not run: %v", err)
	}
	got := platform.Env{}
	for _, kv := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	for _, name := range []string{launchConfigBaseVar, launchHomeVar} {
		if _, ok := got[name]; ok {
			t.Fatalf("the wake command received %s", name)
		}
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "HOME", "USERPROFILE", "APPDATA"} {
		if got[name] != server[name] {
			t.Fatalf("wake %s=%q, want the server's %q", name, got[name], server[name])
		}
	}
	if got["SOHO_JOB_ID"] != "job-1" {
		t.Fatalf("the wake command is not the hook's: %v", got["SOHO_JOB_ID"])
	}
	if ops := sup.Ops.(selfCLI); ops.Env[launchConfigBaseVar] != starterConfig {
		t.Fatal("the team commands did not receive the selection")
	}
}
