package cli

// SOH-292: mutation-copy, copies add and procs add register their resource on
// native Core even when the worker cannot identify its own terminal
// (HC_TERMINAL_ID dropped, for example by a Codex include_only). Each case
// runs the real CLI entry against the same fixtures the hc housekeeping tests
// use, with the state dir named by SOHO_REGISTRY_DIR as spawn forwards it.
// A registration whose terminal cannot be identified on native Core records
// the pane "unverified", which gc never removes or stops.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// registrantTarget is one registration command on its fixture: the roster
// holds the agent "worker" on pane "p-worker" (replaceable), env is the
// native worker environment without HC_TERMINAL_ID, and run executes the
// command once and returns the stdout the command must print.
type registrantTarget struct {
	cmd         string
	env         platform.Env
	origin      string
	writeRoster func(t *testing.T, rows ...string)
	run         func(t *testing.T, env platform.Env) (code int, out, errOut, wantOut string)
	row         func(t *testing.T) (owner, pane, origin string)
	// orchEnv is the orchestrator's environment (the terminal identified,
	// no SOHO_AGENT) that reads and garbage-collects what the worker
	// registered; runCmd runs one soho command in the fixture's project with
	// it; kept reports whether the last registered resource still exists.
	orchEnv platform.Env
	runCmd  func(t *testing.T, env platform.Env, argv ...string) (int, string, string)
	kept    func(t *testing.T) bool
	// registered is the path or pid of the last registered resource, as it
	// appears in the first column of a `soho copies` line.
	registered func() string
	// listing makes the fake hc answer the machine-wide `ls` with a
	// successful listing that holds no terminal named like the row's pane:
	// a listing that proves nothing absent only by the literal's own rule.
	listing func(t *testing.T)
}

// registrantReinstallHc rewrites the fake hc of dir with rules plus a
// successful machine-wide listing without the row's pane. The fake's shim
// refuses to be installed twice, so the test removes only the shim it created.
func registrantReinstallHc(t *testing.T, dir string, rules ...fakecli.Rule) {
	t.Helper()
	shim := filepath.Join(dir, "hc")
	if runtime.GOOS == "windows" {
		shim += ".exe"
	}
	if err := os.Remove(shim); err != nil {
		t.Fatal(err)
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: registrantOtherTerminals})
	if _, err := fakecli.Install(t, dir, "hc", rules); err != nil {
		t.Fatal(err)
	}
}

const registrantOtherTerminals = `{"terminals":[{"address":"p-someone-else","name":"someone","state":"live","status":{"state":"idle"}}]}`

const registrantAgentPane = "p-worker"

func registrantTargets() map[string]func(t *testing.T) registrantTarget {
	return map[string]func(t *testing.T) registrantTarget{
		"mutation-copy": func(t *testing.T) registrantTarget {
			f, sd, bin := mutationCopyHCFixture(t)
			env := f.env.Clone()
			delete(env, "HC_TERMINAL_ID")
			env["SOHO_REGISTRY_DIR"] = sd
			n := 0
			last := ""
			target := registrantTarget{cmd: "mutation-copy", env: env, origin: "mutation-copy"}
			target.orchEnv = f.env.Clone()
			target.orchEnv["HC_TERMINAL_ID"] = "t-copy"
			target.listing = func(t *testing.T) {
				caller := `{"address":"t-copy","state":"live","labels":{"soho.session":"native-session"}}`
				registrantReinstallHc(t, bin, fakecli.Rule{Argv: []string{"get", "t-copy", "--json"}, Stdout: caller})
			}
			target.runCmd = func(t *testing.T, env platform.Env, argv ...string) (int, string, string) {
				return hcHkRun(t, env, argv...)
			}
			target.kept = func(t *testing.T) bool { _, err := os.Stat(last); return err == nil }
			target.registered = func() string { return last }
			target.writeRoster = func(t *testing.T, rows ...string) {
				writeConfigFile(t, filepath.Join(sd, "agents.tsv"), "# roster\n"+strings.Join(rows, "\n")+"\n")
			}
			target.writeRoster(t, releaseRow("worker", registrantAgentPane, "1", "implementer", false, f.source))
			target.run = func(t *testing.T, env platform.Env) (int, string, string, string) {
				n++
				dest := filepath.Join(f.root, "registered-"+strconv.Itoa(n))
				last = dest
				f.env = env
				code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--dest", dest, "--print-path")
				return code, out, errOut, dest + "\n"
			}
			target.row = func(t *testing.T) (string, string, string) {
				rows, err := core.ReadCopies(sd)
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows=%#v err=%v", rows, err)
				}
				return rows[0].Owner, rows[0].Pane, rows[0].Origin
			}
			return target
		},
		"copies add": func(t *testing.T) registrantTarget {
			f := hcHkCopies(t)
			sd := f.stateDir(t)
			env := f.env.Clone()
			delete(env, "HC_TERMINAL_ID")
			env["SOHO_REGISTRY_DIR"] = sd
			n := 0
			last := ""
			target := registrantTarget{cmd: "copies add", env: env, origin: "add"}
			target.orchEnv = f.env.Clone()
			target.listing = func(t *testing.T) {
				registrantReinstallHc(t, f.env["SOHO_FAKECLI_CONFIG"],
					fakecli.Rule{Argv: []string{"get", "p-worker", "--json"}, ArgvPrefix: true, Stdout: hcHkCaller},
					fakecli.Rule{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("p-worker", "worker", "idle")})
			}
			target.runCmd = f.run
			target.kept = func(t *testing.T) bool { _, err := os.Stat(last); return err == nil }
			target.registered = func() string { return last }
			target.writeRoster = func(t *testing.T, rows ...string) { f.writeRoster(t, rows...) }
			target.writeRoster(t, copiesRosterRow("worker", registrantAgentPane))
			target.run = func(t *testing.T, env platform.Env) (int, string, string, string) {
				n++
				dir := filepath.Join(f.tmp, "handcopy-"+strconv.Itoa(n))
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				last = dir
				code, out, errOut := f.run(t, env, "copies", "add", dir)
				return code, out, errOut, ""
			}
			target.row = func(t *testing.T) (string, string, string) {
				rows := f.copiesRows(t)
				if len(rows) != 1 {
					t.Fatalf("rows=%#v", rows)
				}
				return rows[0].Owner, rows[0].Pane, rows[0].Origin
			}
			return target
		},
		"procs add": func(t *testing.T) registrantTarget {
			f := newProcsFixture(t)
			hcHkInstall(t, f.env, f.fakeDir, nil)
			env := f.env.Clone()
			delete(env, "HC_TERMINAL_ID")
			env["SOHO_REGISTRY_DIR"] = f.state
			lastPid := 0
			target := registrantTarget{cmd: "procs add", env: env, origin: ""}
			target.orchEnv = f.env.Clone()
			target.listing = func(t *testing.T) {
				registrantReinstallHc(t, f.fakeDir, fakecli.Rule{Argv: []string{"get", "p-worker", "--json"}, ArgvPrefix: true, Stdout: hcHkCaller})
			}
			target.runCmd = f.runWithEnv
			target.kept = func(t *testing.T) bool { return procAlive(t, f.env, lastPid) }
			target.registered = func() string { return strconv.Itoa(lastPid) }
			target.writeRoster = func(t *testing.T, rows ...string) {
				writeConfigFile(t, filepath.Join(f.state, "agents.tsv"), "# header\n"+strings.Join(rows, "\n")+"\n")
			}
			target.writeRoster(t, releaseRow("worker", registrantAgentPane, "1", "implementer", false, "/tmp/work"))
			target.run = func(t *testing.T, env platform.Env) (int, string, string, string) {
				pid := f.sleeper(t)
				lastPid = pid
				f.writeProcs(t)
				code, out, errOut := f.runWithEnv(t, env, "procs", "add", strconv.Itoa(pid))
				return code, out, errOut, ""
			}
			target.row = func(t *testing.T) (string, string, string) {
				rows := f.procsRows(t)
				if len(rows) != 1 {
					t.Fatalf("rows=%#v", rows)
				}
				return rows[0].Owner, rows[0].Pane, ""
			}
			return target
		},
	}
}

// registrantExpect runs the command once and checks its exit, stdout, row and
// stderr. line is "" (stderr empty), "unverified" (one ownerless line saying
// gc keeps the row) or "orphan" (one ownerless line saying gc treats it as an
// orphan).
func registrantExpect(t *testing.T, target registrantTarget, env platform.Env, owner, pane, line string) {
	t.Helper()
	code, out, errOut, wantOut := target.run(t, env)
	if code != 0 || out != wantOut {
		t.Fatalf("%s: code=%d out=%q (want %q) err=%q", target.cmd, code, out, wantOut, errOut)
	}
	gotOwner, gotPane, origin := target.row(t)
	if gotOwner != owner || gotPane != pane || origin != target.origin {
		t.Fatalf("%s: row owner=%q pane=%q origin=%q; want owner=%q pane=%q origin=%q", target.cmd, gotOwner, gotPane, origin, owner, pane, target.origin)
	}
	if line == "" {
		if errOut != "" {
			t.Fatalf("%s: stderr=%q; want it empty", target.cmd, errOut)
		}
		return
	}
	if strings.Count(mutationCopyAdviceFree(errOut), "\n") != 1 || !strings.HasPrefix(errOut, target.cmd+": registered without an owner (this terminal could not be identified") {
		t.Fatalf("%s: stderr=%q; want exactly one ownerless line", target.cmd, errOut)
	}
	verdict := map[string]string{"unverified": "gc keeps it because its owner cannot be verified", "orphan": "gc treats it as an orphan"}[line]
	removal := "stop the process yourself"
	if strings.HasSuffix(target.cmd, "copy") || target.cmd == "copies add" {
		removal = "delete the directory yourself"
	}
	for _, want := range []string{verdict, removal, "`soho gc --yes` then drops the"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("%s: stderr=%q lacks %q", target.cmd, errOut, want)
		}
	}
	if strings.Contains(errOut, "soho release") || strings.Contains(errOut, "copies rm") || strings.Contains(errOut, "procs rm") {
		t.Fatalf("%s: stderr names a subcommand that does not exist: %q", target.cmd, errOut)
	}
}

// SOHO_AGENT naming a roster row binds the registration to that agent and to
// the pane of its roster row; the stdout and exit code are the command's own.
func TestRegistrationWithoutCallerIdentityUsesSohoAgent(t *testing.T) {
	for name, make := range registrantTargets() {
		t.Run(name, func(t *testing.T) {
			target := make(t)
			env := target.env.Clone()
			env["SOHO_AGENT"] = "worker"
			registrantExpect(t, target, env, "worker", registrantAgentPane, "")
		})
	}
}

// Without SOHO_AGENT, or with a value that is not a live roster row, the row
// is ownerless (owner "-", pane "unverified") with one stderr line; the command still
// exits 0 with its usual stdout. SOHO_AGENT never creates a roster row and
// never becomes a path.
func TestRegistrationWithoutCallerIdentityOrAgentIsOwnerless(t *testing.T) {
	values := map[string]*string{"unset": nil}
	for label, value := range map[string]string{"unknown": "ghost", "empty": "", "whitespace": "  \t", "path-like": "../../etc/passwd", "padded": " worker ", "case": "Worker"} {
		v := value
		values[label] = &v
	}
	for name, make := range registrantTargets() {
		for label, value := range values {
			t.Run(name+"/"+label, func(t *testing.T) {
				target := make(t)
				env := target.env.Clone()
				if value != nil {
					env["SOHO_AGENT"] = *value
				}
				before := rosterSnapshot(t, target)
				registrantExpect(t, target, env, "-", registrantUnverifiedPane, "unverified")
				if after := rosterSnapshot(t, target); after != before {
					t.Fatalf("the roster changed: %q -> %q", before, after)
				}
			})
		}
	}
}

func rosterSnapshot(t *testing.T, target registrantTarget) string {
	t.Helper()
	dir := target.env.Get("SOHO_REGISTRY_DIR")
	data, err := os.ReadFile(filepath.Join(dir, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A header-only roster has no row to bind to.
func TestRegistrationWithHeaderOnlyRosterIsOwnerless(t *testing.T) {
	for name, make := range registrantTargets() {
		t.Run(name, func(t *testing.T) {
			target := make(t)
			target.writeRoster(t)
			env := target.env.Clone()
			env["SOHO_AGENT"] = "worker"
			registrantExpect(t, target, env, "-", registrantUnverifiedPane, "unverified")
		})
	}
}

// The binding is to the live roster row at registration time: a name whose
// row moved to another pane takes the new pane, and a name whose row left
// the roster binds to nothing.
func TestRegistrationBindsToTheCurrentRosterRow(t *testing.T) {
	for name, make := range registrantTargets() {
		t.Run(name+"/moved", func(t *testing.T) {
			target := make(t)
			target.writeRoster(t, releaseRow("worker", "p-reused", "1", "implementer", false, "/tmp/work"))
			env := target.env.Clone()
			env["SOHO_AGENT"] = "worker"
			registrantExpect(t, target, env, "worker", "p-reused", "")
		})
		t.Run(name+"/released", func(t *testing.T) {
			target := make(t)
			target.writeRoster(t, releaseRow("other", "p-other", "1", "implementer", false, "/tmp/work"))
			env := target.env.Clone()
			env["SOHO_AGENT"] = "worker"
			registrantExpect(t, target, env, "-", registrantUnverifiedPane, "unverified")
		})
	}
}

// A caller lookup that dies is not fatal: the registration continues with no
// pane and falls to SOHO_AGENT, or to the ownerless row. A panic that is not
// a command exit still propagates.
func TestRegistrationSurvivesADyingCallerLookup(t *testing.T) {
	old := registrantCaller
	t.Cleanup(func() { registrantCaller = old })
	registrantCaller = func(platform.Env) (string, bool) {
		platform.Die("herdr pane current failed (exit 1)", 1)
		return "", false
	}
	for name, make := range registrantTargets() {
		t.Run(name+"/agent", func(t *testing.T) {
			target := make(t)
			env := target.env.Clone()
			env["SOHO_AGENT"] = "worker"
			registrantExpect(t, target, env, "worker", registrantAgentPane, "")
		})
		t.Run(name+"/ownerless", func(t *testing.T) {
			target := make(t)
			registrantExpect(t, target, target.env.Clone(), "-", registrantUnverifiedPane, "unverified")
		})
	}
	t.Run("other panics propagate", func(t *testing.T) {
		registrantCaller = func(platform.Env) (string, bool) { panic("boom") }
		defer func() {
			if recover() == nil {
				t.Fatal("a panic that is not a command exit was swallowed")
			}
		}()
		registrantPane(platform.Env{"SOHO_TRANSPORT": "hc"})
	})
}

// Order (a): the roster agent of the caller's own pane wins over SOHO_AGENT,
// which is only the fallback. Here the terminal is identified and rostered.
func TestRegistrationCallerPaneWinsOverSohoAgent(t *testing.T) {
	old := registrantCaller
	t.Cleanup(func() { registrantCaller = old })
	registrantCaller = func(platform.Env) (string, bool) { return "p-caller", true }
	for name, make := range registrantTargets() {
		t.Run(name, func(t *testing.T) {
			target := make(t)
			target.writeRoster(t,
				releaseRow("worker", registrantAgentPane, "1", "implementer", false, "/tmp/work"),
				releaseRow("caller", "p-caller", "1", "implementer", false, "/tmp/work"))
			env := target.env.Clone()
			env["SOHO_AGENT"] = "worker"
			registrantExpect(t, target, env, "caller", "p-caller", "")
		})
	}
}

// A terminal that is identified but not in the roster keeps today's row
// (owner "-" on its terminal) and prints no ownerless line, because the
// recorded terminal still ties the row to something gc can check. SOHO_AGENT
// does not take over: it stands in only for a missing terminal (review R369:
// a worker's child terminal inherits it, and the worker's release or gone
// row must not remove the child's copy).
func TestRegistrationIdentifiedUnrosteredTerminal(t *testing.T) {
	old := registrantCaller
	t.Cleanup(func() { registrantCaller = old })
	registrantCaller = func(platform.Env) (string, bool) { return "p-unrostered", true }
	for name, make := range registrantTargets() {
		t.Run(name+"/no-agent", func(t *testing.T) {
			target := make(t)
			registrantExpect(t, target, target.env.Clone(), "-", "p-unrostered", "")
		})
		t.Run(name+"/agent", func(t *testing.T) {
			target := make(t)
			env := target.env.Clone()
			env["SOHO_AGENT"] = "worker"
			registrantExpect(t, target, env, "-", "p-unrostered", "")
		})
	}
}

// The legacy transport keeps its behavior: no caller pane gives an ownerless
// row and no stderr line (the line is a native Core message).
func TestRegistrationLegacyOwnerlessPrintsNothing(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{})
	f.writeRoster(t, copiesRosterRow("worker", registrantAgentPane))
	env := f.env.Clone()
	for _, key := range []string{"HERDR_PANE_ID", "HERDR_TAB_ID", "SOHO_TRANSPORT", "HC_TERMINAL_ID"} {
		delete(env, key)
	}
	env["HERDR_ENV"] = "1"
	env["SOHO_REGISTRY_DIR"] = f.stateDir(t)
	dir := filepath.Join(f.tmp, "legacycopy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.run(t, env, "copies", "add", dir)
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q; want a silent success", code, out, errOut)
	}
	rows := f.copiesRows(t)
	if len(rows) != 1 || rows[0].Owner != "-" || rows[0].Pane != "-" {
		t.Fatalf("rows=%#v; want the ownerless row", rows)
	}
}

// A lookup that succeeds with an empty pane keeps today's row (owner "-",
// pane "-"): gc then reads it as an orphan, and the stderr line says so
// instead of claiming gc keeps it. SOHO_AGENT naming a roster row still wins.
func TestRegistrationSucceededLookupWithEmptyPaneKeepsDash(t *testing.T) {
	old := registrantCaller
	t.Cleanup(func() { registrantCaller = old })
	registrantCaller = func(platform.Env) (string, bool) { return "", true }
	for name, make := range registrantTargets() {
		t.Run(name+"/ownerless", func(t *testing.T) {
			target := make(t)
			registrantExpect(t, target, target.env.Clone(), "-", "-", "orphan")
		})
		t.Run(name+"/agent", func(t *testing.T) {
			target := make(t)
			env := target.env.Clone()
			env["SOHO_AGENT"] = "worker"
			registrantExpect(t, target, env, "worker", registrantAgentPane, "")
		})
	}
}

// An ownerless hc row (pane "unverified") is listed as unverified, and
// `gc --yes --older-than 0` neither removes the copy nor stops the process;
// the row stays until the operator deletes or stops the resource, after which
// `gc --yes` drops the missing or gone line. The control is the
// "-"/"-" row of a succeeded lookup, which gc does remove or stop.
func TestRegistrationUnverifiedRowIsNeverRemovedByGc(t *testing.T) {
	oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
	platform.DiskFree = func(string) (int64, int64, bool) { return int64(60) << 30, int64(100) << 30, true }
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return int64(30) << 30, int64(100) << 30, true }
	t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })
	for name, make := range registrantTargets() {
		t.Run(name+"/unverified", func(t *testing.T) {
			target := make(t)
			registrantExpect(t, target, target.env.Clone(), "-", registrantUnverifiedPane, "unverified")
			target.listing(t)
			orch := target.orchEnv.Clone()
			delete(orch, "SOHO_AGENT")
			listed := "copies"
			if target.cmd == "procs add" {
				listed = "procs"
			}
			code, out, errOut := target.runCmd(t, orch, listed)
			// `procs` prints no state column; `copies` does.
			if code != 0 || errOut != "" || !strings.Contains(out, target.registered()) || strings.Contains(out, "orphan") || (listed == "copies" && !strings.Contains(out, "  "+stateUnverified)) {
				t.Fatalf("%s: code=%d out=%q err=%q; want the row listed as %s", listed, code, out, errOut, stateUnverified)
			}
			if code, out, errOut = target.runCmd(t, orch, "gc", "--older-than", "0"); code != 0 || errOut != "" || !strings.Contains(out, gcUnverifiedHeader) || !strings.Contains(out, target.registered()) {
				t.Fatalf("gc dry run: code=%d out=%q err=%q; want the row under the unverified header", code, out, errOut)
			}
			for _, args := range [][]string{{"gc", "--older-than", "0"}, {"gc", "--yes", "--older-than", "0"}} {
				code, out, errOut = target.runCmd(t, orch, args...)
				if code != 0 || errOut != "" || strings.Contains(out, "removed ") || strings.Contains(out, "stopped process") || strings.Contains(out, "would remove") || strings.Contains(out, "orphan processes") {
					t.Fatalf("%v: code=%d out=%q err=%q; the unverified row must not be offered or touched", args, code, out, errOut)
				}
				if !target.kept(t) {
					t.Fatalf("%v: the registered resource is gone", args)
				}
			}
			if _, _, origin := target.row(t); origin != target.origin {
				t.Fatalf("the row changed: origin=%q", origin)
			}
		})
		t.Run(name+"/dash-control", func(t *testing.T) {
			old := registrantCaller
			t.Cleanup(func() { registrantCaller = old })
			registrantCaller = func(platform.Env) (string, bool) { return "", true }
			target := make(t)
			registrantExpect(t, target, target.env.Clone(), "-", "-", "orphan")
			target.listing(t)
			orch := target.orchEnv.Clone()
			delete(orch, "SOHO_AGENT")
			code, out, errOut := target.runCmd(t, orch, "gc", "--yes", "--older-than", "0")
			if code != 0 || errOut != "" || (!strings.Contains(out, "removed ") && !strings.Contains(out, "stopped process")) {
				t.Fatalf("gc --yes: code=%d out=%q err=%q; the dash row is an orphan", code, out, errOut)
			}
			if target.kept(t) {
				t.Fatal("the dash row's resource survived gc --yes")
			}
		})
	}
}

// Legacy transport: a dying caller lookup behaves as before the change, for
// each of the three commands. copies add and procs add die with the lookup's
// exit and register nothing; mutation-copy keeps its own recover (exit 0,
// stdout intact, "copy not registered").
func TestRegistrationLegacyDyingLookupKeepsPreviousBehavior(t *testing.T) {
	old := registrantCaller
	t.Cleanup(func() { registrantCaller = old })
	registrantCaller = func(platform.Env) (string, bool) {
		platform.Die("herdr pane current failed (exit 1)", 1)
		return "", false
	}
	t.Run("copies add", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{})
		f.writeRoster(t, copiesRosterRow("worker", registrantAgentPane))
		env := f.env.Clone()
		for _, key := range []string{"HERDR_PANE_ID", "HERDR_TAB_ID", "SOHO_TRANSPORT", "HC_TERMINAL_ID"} {
			delete(env, key)
		}
		env["HERDR_ENV"] = "1"
		env["SOHO_REGISTRY_DIR"] = f.stateDir(t)
		env["SOHO_AGENT"] = "worker"
		dir := filepath.Join(f.tmp, "legacycopy")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := f.run(t, env, "copies", "add", dir)
		if code != 1 || out != "" || !strings.Contains(errOut, "herdr pane current failed (exit 1)") {
			t.Fatalf("code=%d out=%q err=%q; want the lookup's exit 1", code, out, errOut)
		}
		if rows := f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("rows=%#v; nothing may be registered", rows)
		}
	})
	t.Run("procs add", func(t *testing.T) {
		f := newProcsFixture(t)
		env := f.env.Clone()
		env["SOHO_REGISTRY_DIR"] = f.state
		env["SOHO_AGENT"] = "worker"
		pid := f.sleeper(t)
		f.writeProcs(t)
		code, out, errOut := f.runWithEnv(t, env, "procs", "add", strconv.Itoa(pid))
		if code != 1 || out != "" || !strings.Contains(errOut, "herdr pane current failed (exit 1)") {
			t.Fatalf("code=%d out=%q err=%q; want the lookup's exit 1", code, out, errOut)
		}
		if rows := f.procsRows(t); len(rows) != 0 {
			t.Fatalf("rows=%#v; nothing may be registered", rows)
		}
	})
	t.Run("mutation-copy", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		registry := filepath.Join(f.root, "registry")
		if err := os.MkdirAll(registry, 0o700); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(f.root, "legacy-copy")
		code, out, errOut := runMutationCopyCmd(t, f, f.source, platform.Env{"SOHO_REGISTRY_DIR": registry, "SOHO_AGENT": "worker"}, "--source", f.source, "--dest", dest, "--print-path")
		if code != 0 || out != dest+"\n" || errOut != "mutation-copy: copy not registered (herdr pane current failed (exit 1)); remove it yourself when done\n" {
			t.Fatalf("code=%d out=%q err=%q; want the copy printed and 'copy not registered'", code, out, errOut)
		}
		if rows, err := core.ReadCopies(registry); err != nil || len(rows) != 0 {
			t.Fatalf("rows=%#v err=%v; nothing may be registered", rows, err)
		}
	})
}
