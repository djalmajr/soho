package job

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const (
	asrID     = "job-1"
	asrWS     = "w1"
	asrBranch = "job/job-1"
)

// asrStartSession is the team the job start wrote to the store's session
// file: the pin copies every line in order and appends
// state_dir="<store root>" (quoted) as its last line. It has no
// state_dir line of its own: the starter's selection resolves the store
// root, and a session state_dir pointing elsewhere would move the
// starter's own root away from it.
const asrStartSession = `lane.job.roles=orchestrator
lane.build.roles=implementer,reviewer
lane.build.kind=pi
lane.build.model=model-x
panes=3
`

// asrPinnedBody is the pin's expected bytes for a store at storeRoot: the
// start's lines in order, plus the quoted state_dir naming the store
// root. The quotes survive the config parser (it strips one pair), so
// spaces at either end and quotes inside the path round-trip.
func asrPinnedBody(storeRoot string) string {
	return "lane.job.roles=orchestrator\n" +
		"lane.build.roles=implementer,reviewer\n" +
		"lane.build.kind=pi\n" +
		"lane.build.model=model-x\n" +
		"panes=3\n" +
		`state_dir="` + storeRoot + `"` +
		"\n"
}

// asrEndSession is the end-to-end team file: only the lanes' roles — the
// kind, model, effort and max_workers come from the starter's user
// configuration, which the step-1 snapshot must pin for the agents.
const asrEndSession = "lane.job.roles=orchestrator\n" +
	"lane.build.roles=implementer\n"

// asrStarterUser is the starter's user configuration (the launch
// selection): the resolved team values the snapshot must pin, and
// nothing else (no approvals: permission policy is never copied).
const asrStarterUser = "role.implementer.kind=pi\n" +
	"role.implementer.model=model-a\n" +
	"role.implementer.effort=low\n" +
	"max_workers=5\n"

// asrActorUser is the actors' own user configuration: a different value
// for every team key, and approvals the pin must not copy.
const asrActorUser = "role.implementer.kind=claude\n" +
	"role.implementer.model=model-b\n" +
	"role.implementer.effort=high\n" +
	"max_workers=1\n" +
	"approvals=full\n"

// asrSnapshottedStart is asrEndSession after the step-1 snapshot: the
// resolved starter values on the role keys, in teamPins' order (kind,
// model and effort per role, then max_workers).
const asrSnapshottedStart = asrEndSession +
	"role.implementer.kind=pi\n" +
	"role.implementer.model=model-a\n" +
	"role.implementer.effort=low\n" +
	"max_workers=5\n"

// asrLanePinnedStart records the role snapshot and the lane kind that
// the current job pinning code writes when the team's kinds agree.
const asrLanePinnedStart = asrEndSession +
	"role.implementer.kind=pi\n" +
	"role.implementer.model=model-a\n" +
	"role.implementer.effort=low\n" +
	"lane.build.kind=pi\n" +
	"max_workers=5\n"

// asrLaneActorUser is the actors' own user configuration for the
// lane-kind scenarios. The session's role pins take precedence over
// these lane defaults. The pin does not copy approvals.
const asrLaneActorUser = "lane.build.kind=pi\n" +
	"lane.build.model=model-b\n" +
	"lane.build.effort=high\n" +
	"approvals=full\n"

// asrTwoRoleEndSession is the two-role end-to-end team file: the build
// lane holds the implementer and the tasker alike.
const asrTwoRoleEndSession = "lane.job.roles=orchestrator\n" +
	"lane.build.roles=implementer,tasker\n"

// asrStarterTwoRoles is the starter's selection for the two-role team:
// each role its own kind, model and effort (the tasker's values differ
// from the implementer's, so the snapshot cannot hide them).
const asrStarterTwoRoles = "role.implementer.kind=pi\n" +
	"role.implementer.model=model-a\n" +
	"role.implementer.effort=low\n" +
	"role.tasker.kind=pi\n" +
	"role.tasker.model=model-c\n" +
	"role.tasker.effort=high\n" +
	"max_workers=5\n"

// asrStarterTwoKinds is asrStarterTwoRoles with the tasker's kind at
// another value: the lane's roles resolve two kinds in the starter's
// view, so the lane's kind cannot be anchored.
const asrStarterTwoKinds = "role.implementer.kind=pi\n" +
	"role.implementer.model=model-a\n" +
	"role.implementer.effort=low\n" +
	"role.tasker.kind=codex\n" +
	"role.tasker.model=model-c\n" +
	"role.tasker.effort=high\n" +
	"max_workers=5\n"

// asrTwoRolePinnedStart is asrTwoRoleEndSession after the step-1
// snapshot: the per-role pins in teamPins' order (the roles sorted,
// kind, model and effort each), then the pinned lane kind, then
// max_workers.
const asrTwoRolePinnedStart = asrTwoRoleEndSession +
	"role.implementer.kind=pi\n" +
	"role.implementer.model=model-a\n" +
	"role.implementer.effort=low\n" +
	"role.tasker.kind=pi\n" +
	"role.tasker.model=model-c\n" +
	"role.tasker.effort=high\n" +
	"lane.build.kind=pi\n" +
	"max_workers=5\n"

// asrTwoRoleUnpinnedStart is the two-role snapshot without a pinned lane
// kind (the lane's roles resolve two kinds in the starter's view, so
// none can be anchored): the role pins and max_workers only.
const asrTwoRoleUnpinnedStart = asrTwoRoleEndSession +
	"role.implementer.kind=pi\n" +
	"role.implementer.model=model-a\n" +
	"role.implementer.effort=low\n" +
	"role.tasker.kind=codex\n" +
	"role.tasker.model=model-c\n" +
	"role.tasker.effort=high\n" +
	"max_workers=5\n"

// asrStateDirResolved reads the pin's state_dir back through the config
// parser (the same pairs sessionStateDir checks) and returns the value.
func asrStateDirResolved(t *testing.T, file string) string {
	t.Helper()
	pairs, err := core.ReadConfigFilePairs(file)
	if err != nil {
		t.Fatalf("read back %s: %v", file, err)
	}
	for _, pair := range pairs {
		if pair.Key == "state_dir" {
			return pair.Value
		}
	}
	t.Fatalf("no state_dir pair in %s: %#v", file, pairs)
	return ""
}

// asrBase builds the hermetic actor environment and returns it with the
// XDG config dir: a PATH that resolves only the system git, its own HOME
// and XDG config dir, an empty global gitconfig, the fixture commit
// identity, and the fixture skill dir (the config layers read it instead
// of materializing a bundled skill).
func asrBase(t *testing.T, root string) (env platform.Env, conf string) {
	t.Helper()
	home := filepath.Join(root, "home")
	conf = filepath.Join(root, "conf")
	global := filepath.Join(root, "gitconfig")
	skill := filepath.Join(root, "skill")
	for _, dir := range []string{home, conf, skill} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, "config.defaults"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	env = platform.Env{
		"PATH":                prepareGitOnlyDir(t),
		"HOME":                home,
		"XDG_CONFIG_HOME":     conf,
		"GIT_CONFIG_GLOBAL":   global,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "fixture",
		"GIT_AUTHOR_EMAIL":    "fixture@example.org",
		"GIT_COMMITTER_NAME":  "fixture",
		"GIT_COMMITTER_EMAIL": "fixture@example.org",
		"SOHO_SKILL_DIR":      skill,
	}
	return env, conf
}

// asrWriteUserConfig sets one user configuration under base (an XDG
// config dir for the actors' environment, or the starter's launch
// selection base: base/soho/config).
func asrWriteUserConfig(t *testing.T, base, body string) {
	t.Helper()
	file := filepath.Join(base, "soho", "config")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// asrGit runs a real git with the fixture env, bounded by a 60 s
// deadline (the prepare fixture's shape).
func asrGit(t *testing.T, env platform.Env, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = time.Second
	cmd.Dir = dir
	cmd.Env = env.List()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
}

// asrGitOut is asrGit with the combined output (trimmed).
func asrGitOut(t *testing.T, env platform.Env, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = time.Second
	cmd.Dir = dir
	cmd.Env = env.List()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
	return strings.TrimSpace(string(out))
}

// asrCheckout seeds a fresh git checkout (init + one commit) under root
// and returns its top.
func asrCheckout(t *testing.T, env platform.Env, root, name string) string {
	t.Helper()
	co := filepath.Join(root, name)
	if err := os.MkdirAll(co, 0o700); err != nil {
		t.Fatal(err)
	}
	asrGit(t, env, co, "init", "-q")
	asrGit(t, env, co, "commit", "-q", "--allow-empty", "-m", "init")
	return co
}

// asrJob records a running job over a store at storeRoot whose directory
// is dir, and returns the store, the shared fake clock and a fresh fake
// team (the supervise fixture's shape).
func asrJob(t *testing.T, storeRoot, dir string) (*Store, *fakeClock, *fakeTeam) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 10, 9, 10, 0, 0, 0, time.FixedZone("-03:00", -3*3600))}
	store := Open(storeRoot)
	store.Clock = clock
	if _, err := store.Start(asrID, []byte(supTestBrief)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(asrID, StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(asrID, func(st *State) { st.Dir = dir; st.Branch = asrBranch; st.Base = "main" }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(asrID, func(st *State) { st.WorkspaceID = asrWS }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(asrID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	return store, clock, &fakeTeam{spawnName: "orch-1"}
}

// asrSupervisor wires a privately launched supervisor over the job with
// the actor environment and the starter's launch selection (the
// unexported fields the same-package tests set directly, the way
// UseLaunchSelection does: actorEnv is a clone of the environment
// handed in) and a recording friction sink.
func asrSupervisor(store *Store, clock *fakeClock, team teamOps, friction *[]string, actorEnv platform.Env, sel core.UserSelection) *Supervisor {
	sup := &Supervisor{
		Store:    store,
		ID:       asrID,
		Ops:      team,
		Clock:    clock,
		Friction: func(message string) { *friction = append(*friction, message) },
	}
	sup.PrivateLaunch = true
	sup.actorEnv = actorEnv.Clone()
	sup.launchSel = sel
	return sup
}

// asrWriteStartSession writes the start's session file into the store
// root, at <storeRoot>/<ws>/session.conf.
func asrWriteStartSession(t *testing.T, storeRoot, body string) {
	t.Helper()
	dir := filepath.Join(storeRoot, asrWS)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.conf"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// asrPinPath is the pin the default actor environment resolves: the
// workspace's session file under the checkout's default state directory.
func asrPinPath(co string) string {
	return filepath.Join(co, ".soho", asrWS, "session.conf")
}

// asrAbs canonicalizes the temp root: git reports real paths, so every
// expectation that names a path git resolves must be canonical.
func asrAbs(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonicalize %s: %v", path, err)
	}
	return real
}

// asrScenario builds one guard scenario: a canonical root, the actor
// env (its user configuration from actorUser), a real checkout the job
// runs in, the store at storeRel under the root (or at the checkout's
// default state dir when storeRel is empty), and the starter's launch
// selection (its user configuration from selBody, absent when selBody
// is empty). It returns the supervisor, the friction sink, the checkout
// and the store root. The start session is the caller's to write.
func asrScenario(t *testing.T, storeRel, actorUser, selBody string) (sup *Supervisor, friction *[]string, co, storeRoot string) {
	t.Helper()
	root := asrAbs(t, t.TempDir())
	env, conf := asrBase(t, root)
	co = asrCheckout(t, env, root, "co")
	if storeRel == "" {
		storeRoot = filepath.Join(co, ".soho")
	} else {
		storeRoot = filepath.Join(root, storeRel)
	}
	if actorUser != "" {
		asrWriteUserConfig(t, conf, actorUser)
	}
	var sel core.UserSelection
	if selBody != "" {
		sel = core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, selBody)
	}
	store, clock, team := asrJob(t, storeRoot, co)
	friction = new([]string)
	sup = asrSupervisor(store, clock, team, friction, env, sel)
	return sup, friction, co, storeRoot
}

// asrHashScenario is a job whose store root holds '#': the checkout's
// path is "<root>/re#po" and the starter's selection names the relative
// state_dir "store" (the store <checkout>/store), while the agents'
// environment resolves its own default <checkout>/.soho. No state
// locator variable is involved on either side.
func asrHashScenario(t *testing.T) (sup *Supervisor, friction *[]string, co, storeRoot string) {
	t.Helper()
	root := asrAbs(t, t.TempDir())
	env, _ := asrBase(t, root)
	co = asrCheckout(t, env, root, "re#po")
	storeRoot = filepath.Join(co, "store")
	sel := core.UserSelection{Base: filepath.Join(root, "starter")}
	asrWriteUserConfig(t, sel.Base, "state_dir=store\n")
	store, clock, team := asrJob(t, storeRoot, co)
	friction = new([]string)
	sup = asrSupervisor(store, clock, team, friction, env, sel)
	return sup, friction, co, storeRoot
}

// asrAssertRefused checks one refused pin: errActorSession, exactly one
// friction message with the expected text, and nothing readable at the
// pin path (a missing file, or a directory or other entry the pin must
// not have become a file at).
func asrAssertRefused(t *testing.T, err error, friction *[]string, pin, wantFriction string) {
	t.Helper()
	if err != errActorSession {
		t.Fatalf("pin error = %v, want errActorSession", err)
	}
	if len(*friction) != 1 || (*friction)[0] != wantFriction {
		t.Fatalf("friction = %v, want [%s]", *friction, wantFriction)
	}
	if _, readErr := os.ReadFile(pin); readErr == nil {
		t.Fatalf("a file was written at the pin path %s, want nothing written", pin)
	}
}

// asrExcludeCount counts line as one of the checkout's local git exclude
// file's lines (a missing file counts 0).
func asrExcludeCount(t *testing.T, co, line string) int {
	t.Helper()
	file := filepath.Join(co, ".git", "info", "exclude")
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", file, err)
	}
	n := 0
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) == line {
			n++
		}
	}
	return n
}

// TestActorSession pins (*Supervisor).pinActorSession against the
// current contract: the starter's selection (launchSel) must resolve the
// job store root; the step-1 snapshot pins the starter's resolved team
// (kind, model, effort per role on the lane or role key, plus
// max_workers, never approvals) into the start's session file even when
// the roots are the same; a workspace pin at the actors' own state root
// carries the session plus state_dir="<store root>" (quoted) and, inside
// the checkout, its git-local exclude line; and step 3 re-resolves the
// agents' view, refusing the pin when the team or the state root would
// differ. Every refusal is errActorSession with one friction message.
func TestActorSession(t *testing.T) {
	t.Run("different roots write the pin at the actor root and stay idempotent", func(t *testing.T) {
		// pinActorSession step 2: the actors' state root (the checkout's
		// default) differs from the store root, so the pin is written at
		// the actor root with the start's lines and the quoted
		// state_dir="<store root>". The spaces in the checkout and the
		// store root must survive the pin and its quoted line.
		root := asrAbs(t, t.TempDir())
		env, _ := asrBase(t, root)
		co := asrCheckout(t, env, root, "check out")
		storeRoot := filepath.Join(root, "st ore")
		sel := core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, "state_dir="+storeRoot+"\n")
		store, clock, team := asrJob(t, storeRoot, co)
		asrWriteStartSession(t, storeRoot, asrStartSession)
		var friction []string
		sup := asrSupervisor(store, clock, team, &friction, env, sel)
		if err := sup.pinActorSession(); err != nil {
			t.Fatalf("pin: %v (friction %v)", err, friction)
		}
		if len(friction) != 0 {
			t.Fatalf("friction = %v, want none", friction)
		}
		pin := asrPinPath(co)
		data, err := os.ReadFile(pin)
		if err != nil {
			t.Fatalf("the pin is missing at %s (a pin written to the store root instead of the actor root would land there): %v", pin, err)
		}
		want := asrPinnedBody(storeRoot)
		if string(data) != want {
			t.Fatalf("pin = %q, want %q (the start's lines in order, the quoted store root appended)", data, want)
		}
		// The pin is a private file: 0600 on the POSIX platforms.
		if runtime.GOOS != "windows" {
			info, err := os.Stat(pin)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("pin mode = %o, want 0600", info.Mode().Perm())
			}
		}
		// The parser reads the quoted line back as the store root: an
		// unquoted line would lose the root's spaces to the parser's
		// trim, and the write's read-back check would have refused it.
		if got := asrStateDirResolved(t, pin); got != storeRoot {
			t.Fatalf("parsed state_dir = %q, want %q", got, storeRoot)
		}
		// The step-1 snapshot wrote nothing here (the session already
		// decides kind and model, nothing resolves effort, the selection
		// sets no max_workers): the start's bytes are untouched.
		if start, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf")); err != nil || string(start) != asrStartSession {
			t.Fatalf("the start's session = %q (err %v), want its own bytes", start, err)
		}
		// A second pin overwrites byte for byte: the copy is
		// deterministic and idempotent, not a growing append.
		if err := sup.pinActorSession(); err != nil {
			t.Fatal(err)
		}
		again, err := os.ReadFile(pin)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("second pin = %q, want the first pin's bytes %q", again, data)
		}
	})

	t.Run("roots the quoted line round-trips are written, not refused", func(t *testing.T) {
		// sessionStateDirValue allows a tab, a space at the end and a
		// quote in the store root (only '#' and line breaks refuse): the
		// quoted state_dir line round-trips through the config parser, so
		// writeSessionFile writes the workspace pin for each and its
		// read-back check (the parsed state_dir is the store root) holds.
		// A refusal here would silently narrow the supported roots.
		for _, name := range []string{"bad\tstore", "bad store ", "badstore\"", "\"badstore"} {
			t.Run(strings.ReplaceAll(name, "\t", "TAB"), func(t *testing.T) {
				root := asrAbs(t, t.TempDir())
				storeRoot := filepath.Join(root, name)
				var friction []string
				sup := &Supervisor{Store: Open(storeRoot), Friction: func(m string) { friction = append(friction, m) }}
				actorRoot := filepath.Join(root, "agent")
				body := pinnedSessionBody(asrStartSession, storeRoot)
				if body != asrPinnedBody(storeRoot) {
					t.Fatalf("pin body = %q, want %q", body, asrPinnedBody(storeRoot))
				}
				if !sup.writeSessionFile(actorRoot, asrWS, body, true) {
					t.Fatalf("the pin was refused (friction %v)", friction)
				}
				pin := filepath.Join(actorRoot, asrWS, "session.conf")
				if got := asrStateDirResolved(t, pin); got != storeRoot {
					t.Fatalf("parsed state_dir = %q, want the round-tripped root %q", got, storeRoot)
				}
			})
		}
	})

	t.Run("the same root writes no workspace pin, only the snapshot lines", func(t *testing.T) {
		// The starter's selection resolves the checkout's default state
		// dir, which is the store root and the actors' root alike: no
		// workspace pin is written. Step 1 still snapshots the starter's
		// resolved team (the selection's role kind, model, effort and
		// max_workers, none of which the session decides yet) into the
		// start's own session file — and only those lines: no state_dir
		// line joins the store's own session, and a second pin adds
		// nothing (the session decides the keys now).
		root := asrAbs(t, t.TempDir())
		env, _ := asrBase(t, root)
		co := asrCheckout(t, env, root, "co")
		storeRoot := filepath.Join(co, ".soho")
		sel := core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, asrStarterUser)
		store, clock, team := asrJob(t, storeRoot, co)
		asrWriteStartSession(t, storeRoot, asrEndSession)
		var friction []string
		sup := asrSupervisor(store, clock, team, &friction, env, sel)
		if err := sup.pinActorSession(); err != nil {
			t.Fatalf("pin: %v (friction %v)", err, friction)
		}
		if len(friction) != 0 {
			t.Fatalf("friction = %v, want none", friction)
		}
		data, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != asrSnapshottedStart {
			t.Fatalf("the start's session = %q, want the start's lines plus the snapshot lines and no state_dir line %q", data, asrSnapshottedStart)
		}
		if err := sup.pinActorSession(); err != nil {
			t.Fatal(err)
		}
		again, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("second pin = %q, want the first pin's bytes %q (the snapshot is idempotent)", again, data)
		}
	})

	t.Run("private launch gates", func(t *testing.T) {
		// A supervisor without the private context, or without the actor
		// environment, must not pin: dropping the PrivateLaunch check or
		// the nil actorEnv check would write the pin in either case.
		for name, gate := range map[string]func(sup *Supervisor){
			"PrivateLaunch off": func(sup *Supervisor) { sup.PrivateLaunch = false },
			"nil actorEnv":      func(sup *Supervisor) { sup.actorEnv = nil },
		} {
			t.Run(name, func(t *testing.T) {
				sup, friction, co, _ := asrScenario(t, "store", "", asrStarterUser)
				gate(sup)
				if err := sup.pinActorSession(); err != nil {
					t.Fatalf("pin: %v", err)
				}
				if len(*friction) != 0 {
					t.Fatalf("friction = %v, want none", *friction)
				}
				if _, readErr := os.ReadFile(asrPinPath(co)); readErr == nil {
					t.Fatalf("a file was written at %s, want nothing written", asrPinPath(co))
				}
			})
		}
	})

	t.Run("a starter selection that does not resolve the store root is refused", func(t *testing.T) {
		// pinActorSession's first check: the starter's selection
		// (LoadConfigFor with launchSel) must resolve the store root.
		// A selection whose state_dir is absent (the default root) or
		// points elsewhere cannot be the starter's, and the pin must
		// fail before anything is written.
		sup, friction, co, storeRoot := asrScenario(t, "store", "", "")
		asrWriteStartSession(t, storeRoot, asrStartSession)
		err := sup.pinActorSession()
		asrAssertRefused(t, err, friction, asrPinPath(co),
			"job: the starter's selection does not resolve the job state root")
		if start, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf")); err != nil || string(start) != asrStartSession {
			t.Fatalf("the start's session = %q (err %v), want its own bytes (the refusal runs before the snapshot write)", start, err)
		}
	})

	t.Run("a store root a session line cannot name is refused", func(t *testing.T) {
		// sessionStateDirValue: a '#' anywhere in the store root cuts a
		// session line (a comment) even quoted. Reachable: the checkout's
		// path holds '#' and the starter's relative state_dir puts the
		// store under it, while the agents keep their default root — so
		// the workspace pin is needed and must be refused, not written
		// naming a root the agents would read back cut.
		sup, friction, co, storeRoot := asrHashScenario(t)
		asrWriteStartSession(t, storeRoot, asrStartSession)
		err := sup.pinActorSession()
		asrAssertRefused(t, err, friction, asrPinPath(co),
			"job: the job state root cannot be written in a session file (an absolute path without '#' or line breaks)")
		if start, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf")); err != nil || string(start) != asrStartSession {
			t.Fatalf("the start's session = %q (err %v), want its own bytes", start, err)
		}
	})

	t.Run("an actor root under a job worktree is refused", func(t *testing.T) {
		// pinExclude refuses a workspace session under the checkout's
		// .worktrees: a worktree has its own git root, which a
		// checkout's info/exclude line does not reach. The pin must be
		// refused and the exclude file must gain no line.
		root := asrAbs(t, t.TempDir())
		env, conf := asrBase(t, root)
		co := asrCheckout(t, env, root, "co")
		storeRoot := filepath.Join(root, "store")
		if err := os.MkdirAll(filepath.Join(co, ".worktrees"), 0o700); err != nil {
			t.Fatal(err)
		}
		asrWriteUserConfig(t, conf, "state_dir="+filepath.Join(co, ".worktrees", "other-state")+"\n")
		sel := core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, "state_dir="+storeRoot+"\n")
		store, clock, team := asrJob(t, storeRoot, co)
		asrWriteStartSession(t, storeRoot, asrStartSession)
		var friction []string
		sup := asrSupervisor(store, clock, team, &friction, env, sel)
		err := sup.pinActorSession()
		asrAssertRefused(t, err, &friction, filepath.Join(co, ".worktrees", "other-state", asrWS, "session.conf"),
			"job: the job workspace session would be inside a job worktree")
		if n := asrExcludeCount(t, co, "/.worktrees/other-state/"+asrWS+"/"); n != 0 {
			t.Fatalf("the exclude file holds the worktree line %d times, want 0 (the refusal runs before any exclude write)", n)
		}
	})

	t.Run("an actor root inside the checkout gets an exclude line and is written", func(t *testing.T) {
		// pinExclude: a workspace session elsewhere in the checkout is
		// written, with /<path from the checkout>/<ws>/ appended to the
		// checkout's .git/info/exclude so the pin never shows up as
		// untracked; a second pin must not append the line again.
		root := asrAbs(t, t.TempDir())
		env, conf := asrBase(t, root)
		co := asrCheckout(t, env, root, "co")
		storeRoot := filepath.Join(root, "store")
		asrWriteUserConfig(t, conf, "state_dir="+filepath.Join(co, "other")+"\n")
		sel := core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, "state_dir="+storeRoot+"\n")
		store, clock, team := asrJob(t, storeRoot, co)
		asrWriteStartSession(t, storeRoot, asrStartSession)
		var friction []string
		sup := asrSupervisor(store, clock, team, &friction, env, sel)
		if err := sup.pinActorSession(); err != nil {
			t.Fatalf("pin: %v (friction %v)", err, friction)
		}
		if len(friction) != 0 {
			t.Fatalf("friction = %v, want none", friction)
		}
		pin := filepath.Join(co, "other", asrWS, "session.conf")
		data, err := os.ReadFile(pin)
		if err != nil {
			t.Fatalf("the pin is missing at %s: %v", pin, err)
		}
		if want := asrPinnedBody(storeRoot); string(data) != want {
			t.Fatalf("pin = %q, want %q", data, want)
		}
		if n := asrExcludeCount(t, co, "/other/"+asrWS+"/"); n != 1 {
			t.Fatalf("the exclude line /%s/ appears %d times, want 1", asrWS, n)
		}
		if status := asrGitOut(t, env, co, "status", "--porcelain"); status != "" {
			t.Fatalf("git status --porcelain = %q, want empty (the pin is excluded from the checkout)", status)
		}
		if err := sup.pinActorSession(); err != nil {
			t.Fatal(err)
		}
		if n := asrExcludeCount(t, co, "/other/"+asrWS+"/"); n != 1 {
			t.Fatalf("after the second pin the exclude line appears %d times, want 1 (appendExclude must not duplicate it)", n)
		}
		again, err := os.ReadFile(pin)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("second pin = %q, want the first pin's bytes %q", again, data)
		}
	})

	t.Run("the workspace session path guards", func(t *testing.T) {
		// confinedDir.open refuses a link or another kind of file at the
		// state root's levels, at the workspace directory or at the
		// session file, before anything is created; confine refuses a
		// path whose nearest existing ancestor resolves to something
		// that is not a directory. Each must fail the pin and write
		// nothing.
		t.Run("a link at the workspace directory", func(t *testing.T) {
			root := asrAbs(t, t.TempDir())
			env, conf := asrBase(t, root)
			co := asrCheckout(t, env, root, "co")
			storeRoot := filepath.Join(root, "store")
			target := filepath.Join(root, "elsewhere")
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(co, "state"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(co, "state", asrWS)); err != nil {
				t.Fatalf("create the symlink fixture: %v", err)
			}
			asrWriteUserConfig(t, conf, "state_dir="+filepath.Join(co, "state")+"\n")
			sel := core.UserSelection{Base: filepath.Join(root, "starter")}
			asrWriteUserConfig(t, sel.Base, "state_dir="+storeRoot+"\n")
			store, clock, team := asrJob(t, storeRoot, co)
			asrWriteStartSession(t, storeRoot, asrStartSession)
			var friction []string
			sup := asrSupervisor(store, clock, team, &friction, env, sel)
			err := sup.pinActorSession()
			asrAssertRefused(t, err, &friction, filepath.Join(co, "state", asrWS, "session.conf"),
				"job: the job workspace session path holds a link or another kind of file")
			// The pin would have landed at the symlink's target (w1 is
			// the link): nothing may exist there either.
			if _, readErr := os.ReadFile(filepath.Join(target, "session.conf")); readErr == nil {
				t.Fatalf("the pin was written through the link into %s, want nothing written", target)
			}
		})
		t.Run("a directory at the session path", func(t *testing.T) {
			root := asrAbs(t, t.TempDir())
			env, conf := asrBase(t, root)
			co := asrCheckout(t, env, root, "co")
			storeRoot := filepath.Join(root, "store")
			if err := os.MkdirAll(filepath.Join(co, "state", asrWS, "session.conf"), 0o700); err != nil {
				t.Fatal(err)
			}
			asrWriteUserConfig(t, conf, "state_dir="+filepath.Join(co, "state")+"\n")
			sel := core.UserSelection{Base: filepath.Join(root, "starter")}
			asrWriteUserConfig(t, sel.Base, "state_dir="+storeRoot+"\n")
			store, clock, team := asrJob(t, storeRoot, co)
			asrWriteStartSession(t, storeRoot, asrStartSession)
			var friction []string
			sup := asrSupervisor(store, clock, team, &friction, env, sel)
			err := sup.pinActorSession()
			asrAssertRefused(t, err, &friction, filepath.Join(co, "state", asrWS, "session.conf"),
				"job: the job workspace session path holds a link or another kind of file")
			info, statErr := os.Lstat(filepath.Join(co, "state", asrWS, "session.conf"))
			if statErr != nil || !info.IsDir() {
				t.Fatalf("the session path = %v (%v), want the fixture's directory untouched", info, statErr)
			}
		})
		t.Run("an unresolvable path", func(t *testing.T) {
			root := asrAbs(t, t.TempDir())
			env, conf := asrBase(t, root)
			co := asrCheckout(t, env, root, "co")
			storeRoot := filepath.Join(root, "store")
			// A symlink whose target is a regular file: the nearest
			// existing ancestor of the state root is the file, which
			// confine refuses.
			if err := os.WriteFile(filepath.Join(co, "f"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(co, "f"), filepath.Join(co, "link")); err != nil {
				t.Fatalf("create the symlink fixture: %v", err)
			}
			asrWriteUserConfig(t, conf, "state_dir="+filepath.Join(co, "link", "state")+"\n")
			sel := core.UserSelection{Base: filepath.Join(root, "starter")}
			asrWriteUserConfig(t, sel.Base, "state_dir="+storeRoot+"\n")
			store, clock, team := asrJob(t, storeRoot, co)
			asrWriteStartSession(t, storeRoot, asrStartSession)
			var friction []string
			sup := asrSupervisor(store, clock, team, &friction, env, sel)
			err := sup.pinActorSession()
			asrAssertRefused(t, err, &friction, filepath.Join(co, "link", "state", asrWS, "session.conf"),
				"job: the job workspace session path cannot be resolved")
		})
	})

	t.Run("the start session file guards", func(t *testing.T) {
		// readPrivateFile on the start's session file refuses a missing
		// file, a symlink and an oversized file: each must fail the pin
		// and write nothing. The roots are the same, so the starter's
		// root check passes and the file guard is the refusal.
		t.Run("missing", func(t *testing.T) {
			sup, friction, co, _ := asrScenario(t, "same", "", asrStarterUser)
			err := sup.pinActorSession()
			asrAssertRefused(t, err, friction, asrPinPath(co), "job: the job session file is unreadable")
		})
		t.Run("symlinked", func(t *testing.T) {
			sup, friction, _, storeRoot := asrScenario(t, "same", "", asrStarterUser)
			target := filepath.Join(storeRoot, "elsewhere.conf")
			if err := os.WriteFile(target, []byte(asrStartSession), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(storeRoot, asrWS), 0o700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(storeRoot, asrWS, "session.conf")
			// The symlink guard is portable, so its coverage is too: a
			// platform or run that cannot create the symlink fails the
			// test instead of skipping it (the requireSymlink shape).
			if err := os.Symlink(target, link); err != nil {
				t.Fatalf("create the symlink fixture: %v", err)
			}
			err := sup.pinActorSession()
			if err != errActorSession {
				t.Fatalf("pin error = %v, want errActorSession", err)
			}
			if len(*friction) != 1 || (*friction)[0] != "job: the job session file is unreadable" {
				t.Fatalf("friction = %v, want [job: the job session file is unreadable]", *friction)
			}
			// The symlink stands untouched: the guard refused before any
			// open, so the link is neither followed nor replaced.
			info, statErr := os.Lstat(link)
			if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the start's session = %v (%v), want the fixture's symlink", info, statErr)
			}
			if data, readErr := os.ReadFile(target); readErr != nil || string(data) != asrStartSession {
				t.Fatalf("the symlink's target = %q (err %v), want its own bytes (no write through the link)", data, readErr)
			}
		})
		t.Run("oversized", func(t *testing.T) {
			sup, friction, _, storeRoot := asrScenario(t, "same", "", asrStarterUser)
			// 8193 lines of 8 bytes: 65544 > the 64 KiB bound.
			if err := os.MkdirAll(filepath.Join(storeRoot, asrWS), 0o700); err != nil {
				t.Fatal(err)
			}
			big := strings.Repeat("x=12345\n", 8193)
			if err := os.WriteFile(filepath.Join(storeRoot, asrWS, "session.conf"), []byte(big), 0o600); err != nil {
				t.Fatal(err)
			}
			err := sup.pinActorSession()
			if err != errActorSession {
				t.Fatalf("pin error = %v, want errActorSession", err)
			}
			if len(*friction) != 1 || (*friction)[0] != "job: the job session file is unreadable" {
				t.Fatalf("friction = %v, want [job: the job session file is unreadable]", *friction)
			}
			// The oversized file stands untouched: the size guard refused
			// before any write.
			if data, readErr := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf")); readErr != nil || string(data) != big {
				t.Fatalf("the start's session = %q (err %v), want the fixture's oversized bytes", data, readErr)
			}
		})
	})

	t.Run("the starter's role pins override the actor's group defaults", func(t *testing.T) {
		// The job pins the starter's kind, model and effort on role keys.
		// The actor still reads its own approvals.
		for _, tc := range []struct {
			name     string
			storeRel string
		}{
			{"custom root", "st ore"},
			{"same root", ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root := asrAbs(t, t.TempDir())
				env, conf := asrBase(t, root)
				co := asrCheckout(t, env, root, "check out")
				var storeRoot string
				if tc.storeRel == "" {
					storeRoot = filepath.Join(co, ".soho")
				} else {
					storeRoot = filepath.Join(root, tc.storeRel)
				}
				selBody := asrStarterUser
				if tc.storeRel != "" {
					selBody = "state_dir=" + storeRoot + "\n" + asrStarterUser
				}
				sel := core.UserSelection{Base: filepath.Join(root, "starter")}
				asrWriteUserConfig(t, sel.Base, selBody)
				asrWriteUserConfig(t, conf, asrLaneActorUser)
				store, clock, team := asrJob(t, storeRoot, co)
				asrWriteStartSession(t, storeRoot, asrEndSession)
				var friction []string
				sup := asrSupervisor(store, clock, team, &friction, env, sel)
				if err := sup.pinActorSession(); err != nil {
					t.Fatalf("pin: %v (friction %v)", err, friction)
				}
				if len(friction) != 0 {
					t.Fatalf("friction = %v, want none", friction)
				}
				// Step 1 pinned the role keys, the lane kind and
				// max_workers into the start's session file, in both
				// variants.
				data, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf"))
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != asrLanePinnedStart {
					t.Fatalf("the start's session = %q, want %q (the role pins, the pinned lane kind, then max_workers)", data, asrLanePinnedStart)
				}
				if tc.storeRel != "" {
					// The workspace pin at the actors' default root:
					// the store's session bytes plus the quoted
					// state_dir, which the parser reads back as the
					// store root.
					pin := asrPinPath(co)
					pinData, err := os.ReadFile(pin)
					if err != nil {
						t.Fatalf("the pin is missing at %s: %v", pin, err)
					}
					if want := asrLanePinnedStart + `state_dir="` + storeRoot + `"` + "\n"; string(pinData) != want {
						t.Fatalf("pin = %q, want %q", pinData, want)
					}
					if got := asrStateDirResolved(t, pin); got != storeRoot {
						t.Fatalf("parsed state_dir = %q, want %q", got, storeRoot)
					}
				}
				// The agents' own load: the starter's team through the
				// session, the job's state root, their own approvals.
				agentEnv := env.Clone()
				agentEnv["HERDR_WORKSPACE_ID"] = asrWS
				cfg := core.LoadConfig(agentEnv, co)
				if got := core.StateRootPath(&cfg, agentEnv, co); got != storeRoot {
					t.Fatalf("state root = %q, want the job store root %q", got, storeRoot)
				}
				rs := core.ResolveRoleSettings("implementer", &cfg, agentEnv, co, core.RoleFlags{})
				if rs.Kind != "pi" || rs.ModelSpec != "model-a" || rs.Effort != "low" {
					t.Fatalf("the agents resolve %+v, want the starter's kind pi / model model-a / effort low (the session's role pins must beat the agents' lane keys)", rs)
				}
				if got := core.Cfg(&cfg, "max_workers", "", agentEnv); got != "5" {
					t.Fatalf("max_workers = %q, want 5 (the starter's, from the session)", got)
				}
				// Approvals were never pinned: the agents keep their
				// own, from their user configuration.
				if rs.Approvals != "full" {
					t.Fatalf("approvals = %q (from %s), want the actors' own full (the pin must not copy approvals)", rs.Approvals, rs.ApprovalsFrom)
				}
				if e, ok := cfg.Entries["approvals"]; !ok || e.Source != "user" {
					t.Fatalf("approvals entry = %+v (ok %v), want the actors' user configuration", e, ok)
				}
			})
		}
	})

	t.Run("a lane kind the agents set at another value still gets the starter's team", func(t *testing.T) {
		// The agents' own user layer sets the build lane's kind to
		// another value (codex, not the starter's pi). The lane kind
		// outranks the session's role pins, so step 1 must pin the
		// lane's kind at the session layer (every team role of the lane
		// resolves the starter's kind there): the session-layer kind
		// raises the kind layer, the agents' own lane kind is overridden
		// and the starter's team resolves. Intentional expectation
		// change: the subtest in place of this one (agents that would
		// not resolve the job team refuse the pin) asserted the refusal
		// this gap causes — it pinned the defect.
		root := asrAbs(t, t.TempDir())
		env, conf := asrBase(t, root)
		co := asrCheckout(t, env, root, "co")
		storeRoot := filepath.Join(co, ".soho")
		sel := core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, asrStarterUser)
		asrWriteUserConfig(t, conf, "lane.build.kind=codex\n")
		store, clock, team := asrJob(t, storeRoot, co)
		asrWriteStartSession(t, storeRoot, asrEndSession)
		var friction []string
		sup := asrSupervisor(store, clock, team, &friction, env, sel)
		if err := sup.pinActorSession(); err != nil {
			t.Fatalf("pin: %v (friction %v)", err, friction)
		}
		if len(friction) != 0 {
			t.Fatalf("friction = %v, want none", friction)
		}
		data, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf"))
		if err != nil || string(data) != asrLanePinnedStart {
			t.Fatalf("the start's session = %q (err %v), want the role pins, the pinned lane kind and max_workers %q", data, err, asrLanePinnedStart)
		}
		// The agents' own load must resolve the starter's team: the
		// session's pinned lane kind beats the agents' own codex.
		agentEnv := env.Clone()
		agentEnv["HERDR_WORKSPACE_ID"] = asrWS
		cfg := core.LoadConfig(agentEnv, co)
		rs := core.ResolveRoleSettings("implementer", &cfg, agentEnv, co, core.RoleFlags{})
		if rs.Kind != "pi" || rs.ModelSpec != "model-a" || rs.Effort != "low" {
			t.Fatalf("the agents resolve %+v, want the starter's kind pi / model model-a / effort low (not the agents' own lane kind codex)", rs)
		}
	})

	t.Run("supported per-role selections in one lane reach the agents", func(t *testing.T) {
		// Two roles in one build lane: the starter's selection gives
		// each role its own kind, model and effort (the tasker's values
		// differ from the implementer's), and the agents' own user layer
		// sets the lane's kind, model and effort. Each role's session
		// pins take precedence over those defaults.
		root := asrAbs(t, t.TempDir())
		env, conf := asrBase(t, root)
		co := asrCheckout(t, env, root, "co")
		storeRoot := filepath.Join(co, ".soho")
		sel := core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, asrStarterTwoRoles)
		asrWriteUserConfig(t, conf, "lane.build.kind=pi\nlane.build.model=model-b\nlane.build.effort=medium\n")
		store, clock, team := asrJob(t, storeRoot, co)
		asrWriteStartSession(t, storeRoot, asrTwoRoleEndSession)
		var friction []string
		sup := asrSupervisor(store, clock, team, &friction, env, sel)
		if err := sup.pinActorSession(); err != nil {
			t.Fatalf("pin: %v (friction %v)", err, friction)
		}
		if len(friction) != 0 {
			t.Fatalf("friction = %v, want none", friction)
		}
		data, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf"))
		if err != nil || string(data) != asrTwoRolePinnedStart {
			t.Fatalf("the start's session = %q (err %v), want the per-role pins, the pinned lane kind and max_workers %q", data, err, asrTwoRolePinnedStart)
		}
		// The agents' own load: each role its own per-role pins, not
		// the lane's values or the other role's.
		agentEnv := env.Clone()
		agentEnv["HERDR_WORKSPACE_ID"] = asrWS
		cfg := core.LoadConfig(agentEnv, co)
		ri := core.ResolveRoleSettings("implementer", &cfg, agentEnv, co, core.RoleFlags{})
		if ri.Kind != "pi" || ri.ModelSpec != "model-a" || ri.Effort != "low" {
			t.Fatalf("the agents resolve the implementer %+v, want the starter's kind pi / model model-a / effort low", ri)
		}
		rt := core.ResolveRoleSettings("tasker", &cfg, agentEnv, co, core.RoleFlags{})
		if rt.Kind != "pi" || rt.ModelSpec != "model-c" || rt.Effort != "high" {
			t.Fatalf("the agents resolve the tasker %+v, want the starter's kind pi / model model-c / effort high", rt)
		}
	})

	t.Run("different starter kinds in one group reach the actors", func(t *testing.T) {
		// A group's default cannot override a role pin, even when the
		// roles differ and the job adds no group kind to the snapshot.
		root := asrAbs(t, t.TempDir())
		env, conf := asrBase(t, root)
		co := asrCheckout(t, env, root, "co")
		storeRoot := filepath.Join(co, ".soho")
		sel := core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, asrStarterTwoKinds)
		asrWriteUserConfig(t, conf, "lane.build.kind=pi\n")
		store, clock, team := asrJob(t, storeRoot, co)
		asrWriteStartSession(t, storeRoot, asrTwoRoleEndSession)
		var friction []string
		sup := asrSupervisor(store, clock, team, &friction, env, sel)
		if err := sup.pinActorSession(); err != nil || len(friction) != 0 {
			t.Fatalf("pin error=%v friction=%v", err, friction)
		}
		data, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf"))
		if err != nil || string(data) != asrTwoRoleUnpinnedStart {
			t.Fatalf("the start's session = %q (err %v), want the role pins and max_workers without a lane kind %q", data, err, asrTwoRoleUnpinnedStart)
		}
		agentEnv := env.Clone()
		agentEnv["HERDR_WORKSPACE_ID"] = asrWS
		cfg := core.LoadConfig(agentEnv, co)
		for _, want := range []struct{ role, kind, model, effort string }{
			{"implementer", "pi", "model-a", "low"}, {"tasker", "codex", "model-c", "high"},
		} {
			got := core.ResolveRoleSettings(want.role, &cfg, agentEnv, co, core.RoleFlags{})
			if got.Lane != "build" || got.Kind != want.kind || got.ModelSpec != want.model || got.Effort != want.effort {
				t.Fatalf("%s resolved %+v; want %+v in build", want.role, got, want)
			}
		}
	})

	t.Run("the pinned session carries the job team and store root to the actor", func(t *testing.T) {
		// End to end, the brief's actor case: the team file gives only
		// the lane's roles, the starter's user configuration gives the
		// implementer's kind, model, effort and max_workers, and the
		// actors' own configuration gives different values (and
		// approvals the pin never copies). After the pin, the agents'
		// own load must resolve the starter's team through the session,
		// the job's state root, and keep their own approvals.
		for _, tc := range []struct {
			name     string
			storeRel string
		}{
			{"custom root", "st ore"},
			{"same root", ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root := asrAbs(t, t.TempDir())
				env, conf := asrBase(t, root)
				co := asrCheckout(t, env, root, "check out")
				var storeRoot string
				if tc.storeRel == "" {
					storeRoot = filepath.Join(co, ".soho")
				} else {
					storeRoot = filepath.Join(root, tc.storeRel)
				}
				selBody := asrStarterUser
				if tc.storeRel != "" {
					selBody = "state_dir=" + storeRoot + "\n" + asrStarterUser
				}
				sel := core.UserSelection{Base: filepath.Join(root, "starter")}
				asrWriteUserConfig(t, sel.Base, selBody)
				asrWriteUserConfig(t, conf, asrActorUser)
				store, clock, team := asrJob(t, storeRoot, co)
				asrWriteStartSession(t, storeRoot, asrEndSession)
				var friction []string
				sup := asrSupervisor(store, clock, team, &friction, env, sel)
				if err := sup.pinActorSession(); err != nil {
					t.Fatalf("pin: %v (friction %v)", err, friction)
				}
				if len(friction) != 0 {
					t.Fatalf("friction = %v, want none", friction)
				}
				// Step 1 pinned the starter's resolved team into the
				// start's session file, in both variants.
				data, err := os.ReadFile(filepath.Join(storeRoot, asrWS, "session.conf"))
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != asrSnapshottedStart {
					t.Fatalf("the start's session = %q, want %q (the snapshot lines on the role keys, then max_workers)", data, asrSnapshottedStart)
				}
				agentEnv := env.Clone()
				agentEnv["HERDR_WORKSPACE_ID"] = asrWS
				cfg := core.LoadConfig(agentEnv, co)
				if tc.storeRel != "" {
					// The workspace pin at the actors' default root:
					// the start's lines (snapshot included) plus the
					// quoted state_dir, which the parser reads back as
					// the store root.
					pin := asrPinPath(co)
					data, err := os.ReadFile(pin)
					if err != nil {
						t.Fatalf("the pin is missing at %s: %v", pin, err)
					}
					if want := asrSnapshottedStart + `state_dir="` + storeRoot + `"` + "\n"; string(data) != want {
						t.Fatalf("pin = %q, want %q", data, want)
					}
					if got := asrStateDirResolved(t, pin); got != storeRoot {
						t.Fatalf("parsed state_dir = %q, want %q", got, storeRoot)
					}
				}
				for key, want := range map[string]string{
					"role_implementer_kind":   "pi",
					"role_implementer_model":  "model-a",
					"role_implementer_effort": "low",
					"max_workers":             "5",
				} {
					e, ok := cfg.Entries[key]
					if !ok || e.Value != want || e.Source != "session" {
						t.Fatalf("config entry %s = %+v (ok %v), want %q from the session (the pin must beat the actors' own user configuration)", key, e, ok, want)
					}
				}
				if tc.storeRel != "" {
					e := cfg.Entries["state_dir"]
					if e.Value != storeRoot || e.Source != "session" {
						t.Fatalf("state_dir entry = %+v, want %q from the session", e, storeRoot)
					}
				} else if _, ok := cfg.Entries["state_dir"]; ok {
					t.Fatalf("the same-root start's session gained a state_dir entry; the workspace pin must not be written there")
				}
				if got := core.StateRootPath(&cfg, agentEnv, co); got != storeRoot {
					t.Fatalf("state root = %q, want the job store root %q", got, storeRoot)
				}
				rs := core.ResolveRoleSettings("implementer", &cfg, agentEnv, co, core.RoleFlags{})
				if rs.Kind != "pi" || rs.ModelSpec != "model-a" || rs.Effort != "low" {
					t.Fatalf("resolved team = %+v, want the starter's kind pi / model model-a / effort low", rs)
				}
				if got := core.Cfg(&cfg, "max_workers", "", agentEnv); got != "5" {
					t.Fatalf("max_workers = %q, want 5 (the starter's, from the session)", got)
				}
				// Approvals were never pinned: the agents keep their
				// own.
				if rs.Approvals != "full" {
					t.Fatalf("approvals = %q (from %s), want the actors' own full (the pin must not copy approvals)", rs.Approvals, rs.ApprovalsFrom)
				}
				if e, ok := cfg.Entries["approvals"]; !ok || e.Source != "user" {
					t.Fatalf("approvals entry = %+v (ok %v), want the actors' user configuration", e, ok)
				}
			})
		}
	})

	t.Run("Supervisor.Run pins after the job lane and before the orchestrator spawn", func(t *testing.T) {
		// The real Run flow with the fake teamOps: the call order is
		// ensure-lane, spawn, dispatch, and the pin file must already
		// exist with the pinned body when the orchestrator is spawned.
		// A pin ordered before the job lane, or after the spawn, fails
		// here.
		root := asrAbs(t, t.TempDir())
		env, _ := asrBase(t, root)
		co := asrCheckout(t, env, root, "co")
		storeRoot := filepath.Join(root, "store")
		sel := core.UserSelection{Base: filepath.Join(root, "starter")}
		asrWriteUserConfig(t, sel.Base, "state_dir="+storeRoot+"\n")
		store, clock, _ := asrJob(t, storeRoot, co)
		asrWriteStartSession(t, storeRoot, asrStartSession)
		team := &asrOrderTeam{
			spawnName: "orch-1",
			pinPath:   asrPinPath(co),
			statusScript: []fakeStatus{
				{State: "working"},
				{State: "working"},
				{State: "done", Report: writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")},
			},
		}
		var friction []string
		sup := asrSupervisor(store, clock, team, &friction, env, sel)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		want := []string{"ensure", "spawn", "dispatch"}
		if !reflect.DeepEqual(team.order, want) {
			t.Fatalf("call order = %v, want %v", team.order, want)
		}
		if !team.pinAtSpawn {
			t.Fatalf("the pin file is absent when the orchestrator is spawned")
		}
		wantBody := asrPinnedBody(storeRoot)
		if !bytes.Equal(team.pinBodyAtSpawn, []byte(wantBody)) {
			t.Fatalf("pin body at spawn = %q, want %q", team.pinBodyAtSpawn, wantBody)
		}
		if len(friction) != 0 {
			t.Fatalf("friction = %v, want none", friction)
		}
	})

	t.Run("a pin the job cannot make fails the job before the orchestrator spawn", func(t *testing.T) {
		// The store root cannot be named in a session line ('#'): the
		// pin is refused and Run must fail the job with exit 2 before
		// the orchestrator is spawned — the agents would otherwise run
		// with a session that misses the job's team and state root.
		sup, frictionp, co, storeRoot := asrHashScenario(t)
		store := sup.Store
		asrWriteStartSession(t, storeRoot, asrStartSession)
		team := &asrOrderTeam{spawnName: "orch-1", pinPath: asrPinPath(co)}
		sup.Ops = team
		exit, err := sup.Run()
		friction := *frictionp
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitUsage {
			t.Fatalf("exit = %d, want %d (the refused pin fails the job)", exit, ExitUsage)
		}
		// The job lane was ensured, then the pin refused: no spawn, no
		// dispatch.
		if want := []string{"ensure"}; !reflect.DeepEqual(team.order, want) {
			t.Fatalf("call order = %v, want %v (the spawn must not run)", team.order, want)
		}
		if team.pinAtSpawn {
			t.Fatalf("the pin file exists, want nothing written")
		}
		if len(friction) != 1 || friction[0] != "job: the job state root cannot be written in a session file (an absolute path without '#' or line breaks)" {
			t.Fatalf("friction = %v, want the state root refusal", friction)
		}
		snap, err := store.Snapshot(asrID)
		if err != nil {
			t.Fatal(err)
		}
		if snap.State.Status != StatusFailed || snap.State.Motivo == nil || *snap.State.Motivo != "job: cannot pin the job workspace session" {
			t.Fatalf("state = %+v, want failed with the pin refusal motivo", snap.State)
		}
	})
}

// asrOrderTeam is the ordering probe teamOps: it records the call order
// and snapshots the pin file at the moment the orchestrator is spawned,
// so the pin is proven to run after EnsureJobLane and before the spawn.
type asrOrderTeam struct {
	order          []string
	spawnName      string
	statusScript   []fakeStatus
	pinPath        string
	pinAtSpawn     bool
	pinBodyAtSpawn []byte
}

var _ teamOps = (*asrOrderTeam)(nil)

func (o *asrOrderTeam) EnsureJobLane() error {
	o.order = append(o.order, "ensure")
	return nil
}

func (o *asrOrderTeam) SpawnOrchestrator() (string, error) {
	o.order = append(o.order, "spawn")
	data, err := os.ReadFile(o.pinPath)
	if err == nil {
		o.pinAtSpawn = true
		o.pinBodyAtSpawn = data
	}
	return o.spawnName, nil
}

func (o *asrOrderTeam) Dispatch(name, briefPath string, amend bool) error {
	o.order = append(o.order, "dispatch")
	return nil
}

func (o *asrOrderTeam) Send(name, path string) error { return nil }

func (o *asrOrderTeam) Status(name string) (string, string, error) {
	if len(o.statusScript) == 0 {
		panic("asrOrderTeam: no scripted status")
	}
	next := o.statusScript[0]
	o.statusScript = o.statusScript[1:]
	return next.State, next.Report, next.Err
}

func (o *asrOrderTeam) Release(name string) error { return nil }

func (o *asrOrderTeam) ReleaseTeam() error { return nil }

func (o *asrOrderTeam) GC() error { return nil }

func (o *asrOrderTeam) Roster() ([]rosterRow, error) {
	return nil, nil
}

func TestActorSessionRefusesRoleOverridesOfGroupValues(t *testing.T) {
	const group = asrEndSession + "lane.build.kind=pi\nlane.build.model=team-model\nlane.build.effort=low\nlane.build.approvals=ask\n"
	for _, tc := range []struct {
		name, actor string
		knobs       []string
	}{
		{"none", "", nil},
		{"matching", "role.implementer.kind=pi\nrole.implementer.model=team-model\nrole.implementer.effort=low\n", []string{"kind", "model", "effort"}},
		{"kind", "role.implementer.kind=codex\n", []string{"kind"}},
		{"model", "role.implementer.model=actor-model\n", []string{"model"}},
		{"effort", "role.implementer.effort=high\n", []string{"effort"}},
		{"approvals", "role.implementer.approvals=full\n", []string{"approvals"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, rootMode := range []string{"same", "custom"} {
				t.Run(rootMode, func(t *testing.T) {
					root := asrAbs(t, t.TempDir())
					env, actorConf := asrBase(t, root)
					co := asrCheckout(t, env, root, "co")
					storeRoot := filepath.Join(co, ".soho")
					if rootMode == "custom" {
						storeRoot = filepath.Join(root, "starter store")
					}
					sel := core.UserSelection{Base: filepath.Join(root, "starter")}
					asrWriteUserConfig(t, sel.Base, "state_dir="+storeRoot+"\nmax_workers=5\n")
					asrWriteUserConfig(t, actorConf, tc.actor+"approvals=full\n")
					store, clock, team := asrJob(t, storeRoot, co)
					var friction []string
					sup := asrSupervisor(store, clock, team, &friction, env, sel)
					sup.actorEnv["USERPROFILE"], sup.actorEnv["TMPDIR"] = sup.actorEnv["HOME"], t.TempDir()
					asrWriteStartSession(t, storeRoot, group)
					if rootMode == "custom" && len(tc.knobs) > 0 {
						// The destination's old session cannot hide a base role key.
						asrWriteStartSession(t, filepath.Join(co, ".soho"), tc.actor)
					}
					err := sup.pinActorSession()
					session := filepath.Join(storeRoot, asrWS, "session.conf")
					body, readErr := os.ReadFile(session)
					if len(tc.knobs) > 0 {
						var want []string
						for _, knob := range tc.knobs {
							want = append(want, "job: role.implementer."+knob+" in user config overrides the team's lane.build."+knob+"; remove it or set the override in the team")
						}
						if err != errActorSession || !reflect.DeepEqual(friction, want) || readErr != nil || string(body) != group {
							t.Fatalf("pin error=%v friction=%v session=%q read=%v, want unchanged session and %v", err, friction, body, readErr, want)
						}
						return
					}
					if err != nil || len(friction) != 0 {
						t.Fatalf("pin error=%v friction=%v", err, friction)
					}
					if want := group + "max_workers=5\n"; readErr != nil || string(body) != want {
						t.Fatalf("session=%q err=%v; want %q", body, readErr, want)
					}
					env = sup.actorEnv.Clone()
					env["HERDR_WORKSPACE_ID"] = asrWS
					ctx := core.LoadConfig(env, co)
					res := core.ResolveRoleSettings("implementer", &ctx, env, co, core.RoleFlags{})
					if res.Lane != "build" || res.Kind != "pi" || res.ModelSpec != "team-model" || res.Effort != "low" || res.Approvals != "ask" {
						t.Fatalf("actor resolved %+v", res)
					}
				})
			}
		})
	}
}

func TestActorSessionStillRefusesDivergentUnpinnedModel(t *testing.T) {
	sup, friction, _, storeRoot := asrScenario(t, "", "role.implementer.model=actor-model\n", "max_workers=5\n")
	sup.actorEnv["USERPROFILE"], sup.actorEnv["TMPDIR"] = sup.actorEnv["HOME"], t.TempDir()
	asrWriteStartSession(t, storeRoot, asrEndSession+"lane.build.kind=pi\nlane.build.effort=low\n")
	if err := sup.pinActorSession(); err != errActorSession || len(*friction) != 1 || (*friction)[0] != "job: the job's agents would not resolve the job team (role implementer model)" {
		t.Fatalf("pin error=%v friction=%v", err, *friction)
	}
}
