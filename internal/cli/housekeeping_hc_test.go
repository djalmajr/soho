package cli

// DJA-604: the housekeeping commands (copies, gc, procs, friction, roster,
// collect) under transport=hc. Each test runs the real CLI entry with
// SOHO_TRANSPORT=hc against the fake-hc technique the release tests use
// (fakecli.Install on PATH plus SOHO_FAKECLI_CONFIG) and the legacy
// fixtures of the same commands, so the expected output is the legacy
// command's own golden. A fake herdr stays installed and fails the test when
// it is called: under hc the commands reach terminals only through hc, and
// only through its reads (get, ls and read), never kill, rm, run or send.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const hcHkCaller = `{"address":"p-worker","name":"worker","state":"live","labels":{"soho.session":"ws"}}`

const hcHkNotFound = `{"error":{"kind":"not_found","message":"no such terminal"}}`

func hcHkRecord(address, name, status string) string {
	return fmt.Sprintf(`{"address":%q,"name":%q,"state":"live","turn":5,"reported":true,"status":{"state":%q}}`, address, name, status)
}

// hcHkInstall switches a legacy fixture environment to transport=hc: the
// herdr markers go away, the caller is the terminal p-worker of session ws
// (the legacy fixtures' pane and workspace, so their roster rows and goldens
// carry over unchanged), and the fake hc answers the caller read and the
// rules the test names.
func hcHkInstall(t *testing.T, env platform.Env, fakeDir string, rules []fakecli.Rule) {
	t.Helper()
	for _, key := range []string{"HERDR_ENV", "HERDR_WORKSPACE_ID", "HERDR_PANE_ID", "HERDR_TAB_ID"} {
		delete(env, key)
	}
	env["SOHO_TRANSPORT"] = "hc"
	env["HC_TERMINAL_ID"] = "p-worker"
	env["SOHO_FAKECLI_CONFIG"] = fakeDir
	all := append([]fakecli.Rule{{Argv: []string{"get", "p-worker", "--json"}, ArgvPrefix: true, Stdout: hcHkCaller}}, rules...)
	if _, err := fakecli.Install(t, fakeDir, "hc", all); err != nil {
		t.Fatal(err)
	}
}

func hcHkCalls(t *testing.T, fakeDir string) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(fakeDir, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return calls
}

// hcHkAssertReads fails the test when herdr was called at all, or when hc
// was called with a verb outside the allowed reads.
func hcHkAssertReads(t *testing.T, fakeDir string, verbs ...string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fakeDir, "herdr.calls.jsonl")); err == nil {
		calls, _ := fakecli.ReadCalls(filepath.Join(fakeDir, "herdr.calls.jsonl"))
		t.Fatalf("herdr was called under transport=hc: %#v", calls)
	}
	for _, call := range hcHkCalls(t, fakeDir) {
		allowed := false
		for _, verb := range verbs {
			if len(call.Argv) > 0 && call.Argv[0] == verb {
				allowed = true
			}
		}
		if !allowed {
			t.Fatalf("hc call %#v is outside the allowed reads %v", call.Argv, verbs)
		}
	}
}

func hcHkRun(t *testing.T, env platform.Env, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{}, args...), env.Clone())
	}()
	return code, out.String(), errOut.String()
}

// hcHkCopies is the legacy copies fixture switched to hc, with the owners'
// terminals worker (idle) and worker2 (the record is gone).
func hcHkCopies(t *testing.T) *copiesFixture {
	t.Helper()
	f := newCopiesFixture(t, []fakecli.Rule{})
	hcHkInstall(t, f.env, f.env["SOHO_FAKECLI_CONFIG"], []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("p-worker", "worker", "idle")},
		{Argv: []string{"get", "worker2", "--json"}, ArgvPrefix: true, Code: 3, Stderr: hcHkNotFound},
	})
	return f
}

// TestHcCopiesThreeStates is TestCopiesShowsTheThreeStates on hc: the same
// registry prints the same four lines, the owner's state coming from hc get.
func TestHcCopiesThreeStates(t *testing.T) {
	f := hcHkCopies(t)
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"), copiesRosterRow("worker2", "p-worker2"))
	owned, orphan, goneDir := filepath.Join(f.tmp, "owned"), filepath.Join(f.tmp, "orphan"), filepath.Join(f.tmp, "gone-dir")
	missing := filepath.Join(f.tmp, "missing")
	for _, dir := range []string{owned, orphan, goneDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	age := core.FrictionISO(platform.Now().Add(-3 * time.Hour))
	f.writeCopies(t, []core.CopyRow{
		{Path: owned, Owner: "worker", Pane: "p-worker", Created: age, Source: "-", Origin: "add"},
		{Path: orphan, Owner: "ghost", Pane: "-", Created: age, Source: "-", Origin: "add"},
		{Path: goneDir, Owner: "worker2", Pane: "p-worker2", Created: age, Source: "-", Origin: "add"},
		{Path: missing, Owner: "worker", Pane: "p-worker", Created: age, Source: "-", Origin: "add"},
	})
	code, out, errOut := f.run(t, f.env, "copies")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	want := strings.Join([]string{
		owned + "  worker  3h  owned",
		orphan + "  ghost  3h  orphan",
		goneDir + "  worker2  3h  orphan",
		missing + "  worker  3h  missing",
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("stdout=%q; want\n%q", out, want)
	}
	hcHkAssertReads(t, f.env["SOHO_FAKECLI_CONFIG"], "get")
}

// TestHcCopiesAdd is TestCopiesAdd's registration on hc: the calling
// terminal's roster agent owns the line, --agent names another, and a caller
// outside any terminal's roster row owns it as "-".
func TestHcCopiesAdd(t *testing.T) {
	f := hcHkCopies(t)
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	fakeDir := f.env["SOHO_FAKECLI_CONFIG"]
	mk := func(name string) string {
		dir := filepath.Join(f.tmp, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	src := mk("handcopy")
	code, out, errOut := f.run(t, f.env, "copies", "add", src)
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q; want a silent success", code, out, errOut)
	}
	rows := f.copiesRows(t)
	if len(rows) != 1 || rows[0].Path != src || rows[0].Owner != "worker" || rows[0].Pane != "p-worker" || rows[0].Source != "-" || rows[0].Origin != "add" {
		t.Fatalf("registry rows=%#v; want the calling terminal's agent with origin add", rows)
	}
	src2 := mk("handcopy2")
	if code, out, errOut = f.run(t, f.env, "copies", "add", src2, "--agent", "worker"); code != 0 || errOut != "" {
		t.Fatalf("--agent code=%d out=%q err=%q", code, out, errOut)
	}
	if rows = f.copiesRows(t); len(rows) != 2 || rows[1].Owner != "worker" || rows[1].Pane != "p-worker" {
		t.Fatalf("registry rows=%#v; want the --agent line with the roster pane", rows)
	}
	// The listing shows both lines owned by the live worker.
	code, out, _ = f.run(t, f.env, "copies")
	if code != 0 || strings.Count(out, "  worker  ") != 2 || strings.Count(out, "  owned\n") != 2 {
		t.Fatalf("copies: code=%d out=%q; want both lines owned by worker", code, out)
	}
	// An --agent outside the roster exits 3 and registers nothing more.
	code, out, errOut = f.run(t, f.env, "copies", "add", mk("handcopy3"), "--agent", "ghost")
	if code != 3 || !strings.Contains(errOut, "agent 'ghost' is not in the roster") || len(f.copiesRows(t)) != 2 {
		t.Fatalf("ghost: code=%d out=%q err=%q rows=%#v", code, out, errOut, f.copiesRows(t))
	}
	// A refused path writes nothing.
	code, out, errOut = f.run(t, f.env, "copies", "add", f.home)
	if code != 2 || !strings.Contains(errOut, "copies add: refusing '"+f.home+"': is HOME or above it") || len(f.copiesRows(t)) != 2 {
		t.Fatalf("home: code=%d out=%q err=%q", code, out, errOut)
	}
	hcHkAssertReads(t, fakeDir, "get")
}

// TestHcCopiesListCannotReadOwner: an owner hc cannot answer for (the read
// fails, not a not-found) stays owned, as on herdr: gc must not drop a copy
// it cannot prove orphaned.
func TestHcCopiesListCannotReadOwner(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{})
	hcHkInstall(t, f.env, f.env["SOHO_FAKECLI_CONFIG"], []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"kind":"internal","message":"core unreachable"}}`},
	})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	dir := filepath.Join(f.tmp, "kept")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: dir, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour)), Source: "-", Origin: "add"}})
	code, out, errOut := f.run(t, f.env, "copies")
	if code != 0 || errOut != "" || out != dir+"  worker  3h  owned\n" {
		t.Fatalf("code=%d out=%q err=%q; an unreadable owner stays owned", code, out, errOut)
	}
}

// TestHcGcCopies is TestGc's dry run and --yes on hc: the same registry
// produces the same listing and the same removals; the owned copy, the young
// orphan and the unregistered leaf are kept, and no terminal is closed.
func TestHcGcCopies(t *testing.T) {
	setup := func(t *testing.T) (*copiesFixture, gcLayout) {
		f, l := gcFixture(t)
		hcHkInstall(t, f.env, f.env["SOHO_FAKECLI_CONFIG"], []fakecli.Rule{
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("p-worker", "worker", "idle")},
			{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[]}`},
		})
		return f, l
	}
	t.Run("dry run lists the candidates and changes nothing", func(t *testing.T) {
		f, l := setup(t)
		before, err := os.ReadFile(f.registryPath())
		if err != nil {
			t.Fatal(err)
		}
		code, out, errOut := f.run(t, f.env, "gc")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := strings.Join([]string{
			"resource pressure: not detected (disk 60% free, limit 15% / 20 GiB; swap 30% used, limit 80%)",
			"would remove " + l.oldOrphan + "  25 B  ghost  3h",
			"would drop the line of " + l.missing + " (already gone from disk)",
			"total: 25 B",
			"run 'soho gc --yes' to remove them",
			gcUnregisteredHeader,
			l.unreg + "  40 B  3h  no origin marker",
		}, "\n") + "\n"
		if out != want {
			t.Fatalf("stdout=%q; want\n%s", out, want)
		}
		after, err := os.ReadFile(f.registryPath())
		if err != nil || string(after) != string(before) {
			t.Fatalf("the dry run rewrote the registry:\nbefore %q\nafter  %q", before, after)
		}
		for _, path := range []string{filepath.Join(l.oldOrphan, "a.txt"), l.owned, l.unreg} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the dry run touched %s: %v", path, err)
			}
		}
		hcHkAssertReads(t, f.env["SOHO_FAKECLI_CONFIG"], "get", "ls")
	})
	t.Run("--yes removes only the old orphan and drops the missing line", func(t *testing.T) {
		f, l := setup(t)
		code, out, errOut := f.run(t, f.env, "gc", "--yes")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := strings.Join([]string{
			"resource pressure: not detected (disk 60% free, limit 15% / 20 GiB; swap 30% used, limit 80%)",
			"removed " + l.oldOrphan + "  25 B",
			"removed " + l.missing + "  0 B",
			gcUnregisteredHeader,
			l.unreg + "  40 B  3h  no origin marker",
			"freed: 25 B (logical size; see volume free below)", gcUnchangedVolume(l.oldOrphan),
		}, "\n") + "\n"
		if out != want {
			t.Fatalf("stdout=%q; want\n%s", out, want)
		}
		if _, err := os.Stat(l.oldOrphan); !os.IsNotExist(err) {
			t.Fatalf("the old orphan was not removed: %v", err)
		}
		for _, path := range []string{l.youngOrphan, l.owned, l.unreg} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s was touched: %v", path, err)
			}
		}
		rows := f.copiesRows(t)
		if len(rows) != 2 || rows[0].Path != l.youngOrphan || rows[1].Path != l.owned {
			t.Fatalf("registry rows=%#v; want the young orphan and the owned copy only", rows)
		}
		hcHkAssertReads(t, f.env["SOHO_FAKECLI_CONFIG"], "get", "ls")
	})
}

// TestHcGcUnrosteredCallerCopy is R360 F2: a live native caller outside the
// roster registers a copy (owner "-", its terminal recorded as the pane);
// at --older-than 0 gc offers or removes it only when a successful listing
// proves that terminal absent. While the terminal is live it is owned, and
// while the listing fails it is unverified: listed, never offered, never
// removed by --yes.
func TestHcGcUnrosteredCallerCopy(t *testing.T) {
	pressure := "resource pressure: not detected (disk 60% free, limit 15% / 20 GiB; swap 30% used, limit 80%)\n"
	for _, mode := range []string{"terminal-live", "terminal-unverifiable", "terminal-absent", "listing-record-without-address", "listing-record-with-a-numeric-address"} {
		t.Run(mode, func(t *testing.T) {
			oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
			platform.DiskFree = func(string) (int64, int64, bool) { return int64(60) << 30, int64(100) << 30, true }
			platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return int64(30) << 30, int64(100) << 30, true }
			t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })
			f := newCopiesFixture(t, []fakecli.Rule{})
			ls := fakecli.Rule{Argv: []string{"ls", "--json"}}
			switch mode {
			case "terminal-live":
				ls.Stdout = `{"terminals":[{"address":"p-worker","name":"worker","state":"live","status":{"state":"idle"}}]}`
			case "terminal-absent":
				ls.Stdout = `{"terminals":[{"address":"p-someone-else","state":"live","status":{"state":"idle"}}]}`
			case "terminal-unverifiable":
				ls.Code = 9
			case "listing-record-without-address":
				// Review R362: a record without identity proves no absence.
				ls.Stdout = `{"terminals":[{}]}`
			case "listing-record-with-a-numeric-address":
				ls.Stdout = `{"terminals":[{"address":7,"state":"live"}]}`
			}
			// A malformed listing is as unverifiable as a failed one.
			expect := mode
			if strings.HasPrefix(mode, "listing-record-") {
				expect = "terminal-unverifiable"
			}
			hcHkInstall(t, f.env, f.env["SOHO_FAKECLI_CONFIG"], []fakecli.Rule{ls})
			// No roster row: the caller registers as owner "-".
			dir := filepath.Join(f.tmp, "caller-copy")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if code, out, errOut := f.run(t, f.env, "copies", "add", dir); code != 0 || out != "" || errOut != "" {
				t.Fatalf("copies add: code=%d out=%q err=%q", code, out, errOut)
			}
			if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Owner != "-" || rows[0].Pane != "p-worker" {
				t.Fatalf("registry rows=%#v; want one unrostered line recording the caller terminal", rows)
			}
			wantState := map[string]string{"terminal-live": "owned", "terminal-unverifiable": stateUnverified, "terminal-absent": "orphan"}[expect]
			code, out, errOut := f.run(t, f.env, "copies")
			if code != 0 || errOut != "" || out != dir+"  -  0m  "+wantState+"\n" {
				t.Fatalf("copies: code=%d out=%q err=%q; want state %s", code, out, errOut, wantState)
			}
			code, out, errOut = f.run(t, f.env, "gc", "--older-than", "0")
			if code != 0 || errOut != "" {
				t.Fatalf("gc dry run: code=%d out=%q err=%q", code, out, errOut)
			}
			switch expect {
			case "terminal-live":
				if out != pressure+"nothing to remove\n" {
					t.Fatalf("gc dry run = %q; the live caller's copy is not offered", out)
				}
			case "terminal-unverifiable":
				if out != pressure+"nothing to remove\n"+gcUnverifiedHeader+"\ncopy "+dir+"  -  0m\n" {
					t.Fatalf("gc dry run = %q; the unverified copy is listed, never offered", out)
				}
			case "terminal-absent":
				if !strings.Contains(out, "would remove "+dir+"  0 B  -  0m\n") {
					t.Fatalf("gc dry run = %q; the copy of an absent terminal is offered", out)
				}
			}
			code, out, errOut = f.run(t, f.env, "gc", "--yes", "--older-than", "0")
			if code != 0 || errOut != "" {
				t.Fatalf("gc --yes: code=%d out=%q err=%q", code, out, errOut)
			}
			_, statErr := os.Stat(dir)
			switch expect {
			case "terminal-live", "terminal-unverifiable":
				if statErr != nil || len(f.copiesRows(t)) != 1 || strings.Contains(out, "removed ") {
					t.Fatalf("gc --yes = %q (stat %v); the copy and its line stay", out, statErr)
				}
			case "terminal-absent":
				if !os.IsNotExist(statErr) || len(f.copiesRows(t)) != 0 || !strings.Contains(out, "removed "+dir+"  ") {
					t.Fatalf("gc --yes = %q (stat %v); the copy of an absent terminal is removed", out, statErr)
				}
			}
			hcHkAssertReads(t, f.env["SOHO_FAKECLI_CONFIG"], "get", "ls")
		})
	}
}

// TestGcUnrosteredCopyKeepsLegacyRule pins the legacy transport: its
// copy classifier never read the recorded pane, and still does not.
func TestGcUnrosteredCopyKeepsLegacyRule(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{})
	dir := filepath.Join(f.tmp, "legacy-dash")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: dir, Owner: "-", Pane: "p-worker", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour)), Source: "-", Origin: "add"}})
	code, out, errOut := f.run(t, f.env, "copies")
	if code != 0 || errOut != "" || out != dir+"  -  3h  orphan\n" {
		t.Fatalf("copies: code=%d out=%q err=%q", code, out, errOut)
	}
}

// TestHcReleaseRemovesTheAgentsCopies is TestReleaseRemovesTheAgentsCopies on
// hc: the release removes the released agent's registered copies and only
// those.
func TestHcReleaseRemovesTheAgentsCopies(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{})
	hcHkInstall(t, f.env, f.env["SOHO_FAKECLI_CONFIG"], []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("p-worker", "worker", "idle")},
		{Argv: []string{"ls", "--label", "soho.session=ws", "--json"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
	})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"), copiesRosterRow("worker2", "p-worker2"))
	now := core.FrictionISO(platform.Now())
	copyA, copyB := filepath.Join(f.tmp, "copy-a"), filepath.Join(f.tmp, "copy-b")
	for _, dir := range []string{copyA, copyB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.writeCopies(t, []core.CopyRow{
		{Path: copyA, Owner: "worker", Pane: "p-worker", Created: now, Source: "-", Origin: "mutation-copy"},
		{Path: copyB, Owner: "worker2", Pane: "p-worker2", Created: now, Source: "-", Origin: "mutation-copy"},
	})
	addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
	code, out, errOut := f.run(t, f.env, "release", "worker")
	if code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(copyA); !os.IsNotExist(err) {
		t.Fatalf("the agent's copy was not removed: %v", err)
	}
	if _, err := os.Stat(copyB); err != nil {
		t.Fatalf("the other agent's copy was removed: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != copyB {
		t.Fatalf("registry rows=%#v; want only the other agent's line", rows)
	}
	if !strings.Contains(out, "removed copy "+copyA+"\n") || !strings.Contains(out, "released worker\n") {
		t.Fatalf("stdout=%q; want the removed copy line and released", out)
	}
}

// hcHkMutationCopy is the native mutation-copy fixture of the hc tests with
// the rules this file's tests need on top of the caller read.
func hcHkMutationCopy(t *testing.T, workerRule fakecli.Rule) (mutationCopyFixture, string, string) {
	t.Helper()
	f, sd, _ := mutationCopyHCFixture(t)
	// The fixture installed its hc fake with only the caller read; the
	// owner's terminal is answered by a second hc ahead of it on PATH.
	dir := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t-copy", "--json"}, Stdout: `{"address":"t-copy","state":"live","labels":{"soho.session":"native-session"}}`},
		workerRule,
		{Argv: []string{"ls", "--label", "soho.session=native-session", "--json"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
	}
	if _, err := fakecli.Install(t, dir, "hc", rules); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"] = dir + string(os.PathListSeparator) + f.env.Get("PATH")
	f.env["SOHO_FAKECLI_CONFIG"] = dir
	return f, sd, dir
}

// TestHcMutationCopyIsListedAndRemoved is the housekeeping acceptance on hc:
// a mutation-copy run is registered, soho copies lists it, and release
// <agent> (owner alive) or gc (owner's record gone) removes it, as on the
// legacy transport.
func TestHcMutationCopyIsListedAndRemoved(t *testing.T) {
	run := func(t *testing.T, f mutationCopyFixture, args ...string) (int, string, string) {
		t.Helper()
		return hcHkRun(t, f.env, args...)
	}
	t.Run("copies lists the copy as owned and release removes it", func(t *testing.T) {
		f, sd, dir := hcHkMutationCopy(t, fakecli.Rule{Argv: []string{"get", "native-worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("t-copy", "native-worker", "idle")})
		code, out, stderr := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--print-path")
		if code != 0 || stderr != "" {
			t.Fatalf("mutation-copy: exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		path := strings.TrimSuffix(out, "\n")
		code, listed, errOut := run(t, f, "copies")
		if code != 0 || errOut != "" {
			t.Fatalf("copies: code=%d out=%q err=%q", code, listed, errOut)
		}
		if !strings.HasPrefix(listed, path+"  native-worker  0m  owned\n") {
			t.Fatalf("copies = %q; want the copy listed as owned by native-worker", listed)
		}
		addReleaseReport(t, sd, "native-worker", false)
		code, out, errOut = run(t, f, "release", "native-worker")
		if code != 0 {
			t.Fatalf("release: code=%d out=%q err=%q", code, out, errOut)
		}
		if !strings.Contains(out, "removed copy "+path+"\n") {
			t.Fatalf("release stdout=%q; want the removed copy line", out)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("release left the copy at %s: %v", path, err)
		}
		if rows, err := core.ReadCopies(sd); err != nil || len(rows) != 0 {
			t.Fatalf("registry rows=%#v err=%v; want none", rows, err)
		}
		hcHkAssertReads(t, dir, "get", "ls")
	})
	t.Run("copies lists the copy as orphan and gc removes it", func(t *testing.T) {
		f, sd, dir := hcHkMutationCopy(t, fakecli.Rule{Argv: []string{"get", "native-worker", "--json"}, ArgvPrefix: true, Code: 3, Stderr: hcHkNotFound})
		code, out, stderr := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--print-path")
		if code != 0 || stderr != "" {
			t.Fatalf("mutation-copy: exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		path := strings.TrimSuffix(out, "\n")
		code, listed, errOut := run(t, f, "copies")
		if code != 0 || errOut != "" || !strings.HasPrefix(listed, path+"  native-worker  0m  orphan\n") {
			t.Fatalf("copies: code=%d out=%q err=%q; want the copy listed as orphan", code, listed, errOut)
		}
		// The registry line is minutes old, so --older-than 0 lets gc take it.
		code, out, errOut = run(t, f, "gc", "--yes", "--older-than", "0")
		if code != 0 || !strings.Contains(out, "removed "+path+"  ") {
			t.Fatalf("gc --yes: code=%d out=%q err=%q; want the copy removed", code, out, errOut)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("gc left the copy at %s: %v", path, err)
		}
		if rows, err := core.ReadCopies(sd); err != nil || len(rows) != 0 {
			t.Fatalf("registry rows=%#v err=%v; want none", rows, err)
		}
		hcHkAssertReads(t, dir, "get", "ls")
	})
}

// TestHcProcs is the procs registry on hc: add records the calling
// terminal's agent and pane, the listing shows the three states, and stop
// signals only the caller's own running process.
func TestHcProcs(t *testing.T) {
	f := newProcsFixture(t)
	hcHkInstall(t, f.env, f.fakeDir, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("p-worker", "worker", "idle")},
	})
	running := f.sleeper(t)
	if code, out, errOut := f.run(t, "procs", "add", strconv.Itoa(running)); code != 0 || out != "" || errOut != "" {
		t.Fatalf("procs add: code=%d out=%q err=%q", code, out, errOut)
	}
	rows := f.procsRows(t)
	if len(rows) != 1 || rows[0].Pid != running || rows[0].Owner != "worker" || rows[0].Pane != "p-worker" {
		t.Fatalf("registry rows = %+v; want the running process owned by worker on p-worker", rows)
	}
	gone := f.sleeperGone(t)
	reused := f.sleeper(t)
	f.writeProcs(t, append(rows,
		core.ProcRow{Pid: gone, Started: "Sun Oct  3 00:00:00 2026", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: reused, Started: "adulterated start time", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
	)...)
	code, out, errOut := f.run(t, "procs")
	if code != 0 || errOut != "" {
		t.Fatalf("procs: code=%d err=%q", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 ||
		!strings.HasPrefix(lines[0], strconv.Itoa(running)+"  "+filepath.Base(f.sleepExe)+"  worker  0m  running") ||
		!strings.HasPrefix(lines[1], strconv.Itoa(gone)+"  sleep  worker  0m  gone") ||
		!strings.HasPrefix(lines[2], strconv.Itoa(reused)+"  sleep  worker  0m  reused") {
		t.Fatalf("procs = %q; want running, gone and reused lines", out)
	}
	// The calling terminal stops its own running process.
	code, out, errOut = f.run(t, "procs", "stop", strconv.Itoa(running))
	if code != 0 || errOut != "" || !strings.HasPrefix(out, fmt.Sprintf("stopped process %d (", running)) {
		t.Fatalf("procs stop: code=%d out=%q err=%q", code, out, errOut)
	}
	if procAlive(t, f.env, running) {
		t.Fatal("procs stop left its own process running")
	}
	if !procAlive(t, f.env, reused) {
		t.Fatal("procs stop signalled another pid")
	}
	hcHkAssertReads(t, f.fakeDir, "get")
}

// TestHcGcProcesses is TestGcListAndStopOrphanProcesses on hc: the owned
// process is kept, the orphan listed by the dry run and stopped by --yes,
// the reused and gone lines dropped, and the owner's liveness read from hc.
func TestHcGcProcesses(t *testing.T) {
	f := newProcsFixture(t)
	hcHkInstall(t, f.env, f.fakeDir, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("p-worker", "worker", "idle")},
		{Argv: []string{"ls", "--json"}, Stdout: `{"terminals":[]}`},
	})
	owned, orphan, reused, gone := f.sleeper(t), f.sleeper(t), f.sleeper(t), f.sleeperGone(t)
	ownedStarted, ownedName, _ := platform.ProcInfo(owned, f.env)
	orphanStarted, orphanName, _ := platform.ProcInfo(orphan, f.env)
	f.writeProcs(t,
		core.ProcRow{Pid: owned, Started: ownedStarted, Name: ownedName, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: orphan, Started: orphanStarted, Name: orphanName, Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: reused, Started: "adulterated start time", Name: "sleep", Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now())},
		core.ProcRow{Pid: gone, Started: "Sun Oct  3 00:00:00 2026", Name: "sleep", Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now())},
	)
	code, out, errOut := f.run(t, "gc")
	want := "resource pressure: not detected (disk 60% free, limit 15% / 20 GiB; swap 30% used, limit 80%)\norphan processes:\n" +
		strconv.Itoa(orphan) + "  " + orphanName + "  ghost  0m\n" +
		"run 'soho gc --yes' to stop them\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("gc: code=%d err=%q out=%q; want %q", code, errOut, out, want)
	}
	for _, pid := range []int{owned, orphan, reused} {
		if !procAlive(t, f.env, pid) {
			t.Fatalf("the dry run stopped process %d", pid)
		}
	}
	code, out, errOut = f.run(t, "gc", "--yes")
	if code != 0 || errOut != "" ||
		!strings.Contains(out, "stopped process "+strconv.Itoa(orphan)+" ("+orphanName+")") ||
		!strings.Contains(out, "process "+strconv.Itoa(reused)+" was reused; not stopped") ||
		!strings.Contains(out, "process "+strconv.Itoa(gone)+" already gone") ||
		!strings.HasSuffix(out, "process lines handled: 3\n") {
		t.Fatalf("gc --yes: code=%d err=%q out=%q", code, errOut, out)
	}
	if procAlive(t, f.env, orphan) || !procAlive(t, f.env, owned) || !procAlive(t, f.env, reused) {
		t.Fatalf("orphan alive=%v owned alive=%v reused alive=%v; want the orphan stopped and the others kept",
			procAlive(t, f.env, orphan), procAlive(t, f.env, owned), procAlive(t, f.env, reused))
	}
	if rows := f.procsRows(t); len(rows) != 1 || rows[0].Pid != owned {
		t.Fatalf("registry rows = %+v; want only the owned line", rows)
	}
	hcHkAssertReads(t, f.fakeDir, "get", "ls")
}

// TestHcGcDashRowOrphanByTerminal is TestGcDashRowOrphanByPane on hc: a
// running "-" line is kept while its registered terminal is confirmed alive
// by the machine-wide terminal listing; a terminal that left the listing and
// one the listing cannot confirm keep the orphan behavior. The live terminal
// carries another session's label (or none), so only a machine-wide listing
// finds it.
func TestHcGcDashRowOrphanByTerminal(t *testing.T) {
	for _, mode := range []string{"terminal-alive", "terminal-alive-unlabeled", "terminal-gone", "terminal-unverifiable"} {
		t.Run(mode, func(t *testing.T) {
			f := newProcsFixture(t)
			lsRule := fakecli.Rule{Argv: []string{"ls", "--json"}}
			switch mode {
			case "terminal-alive":
				lsRule.Stdout = `{"terminals":[{"address":"p-orch","name":"orch","state":"live","status":{"state":"idle"},"labels":{"soho.session":"other-session"}}]}`
			case "terminal-alive-unlabeled":
				lsRule.Stdout = `{"terminals":[{"address":"p-orch","state":"live","status":{"state":"idle"}}]}`
			case "terminal-gone":
				lsRule.Stdout = `{"terminals":[{"address":"p-someone-else","state":"live","status":{"state":"idle"}}]}`
			case "terminal-unverifiable":
				lsRule.Code = 9
			}
			hcHkInstall(t, f.env, f.fakeDir, []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("p-worker", "worker", "idle")},
				lsRule,
			})
			row := procsStopRow(t, f, f.sleeper(t), "-")
			row.Pane = "p-orch"
			f.writeProcs(t, row)
			pressure := "resource pressure: not detected (disk 60% free, limit 15% / 20 GiB; swap 30% used, limit 80%)\n"
			alive := strings.HasPrefix(mode, "terminal-alive")
			unverified := mode == "terminal-unverifiable"
			kept := alive || unverified
			// An unverified row is kept and listed on its own; a live one is
			// not mentioned.
			unverifiedLine := gcUnverifiedHeader + "\n" + fmt.Sprintf("process %d  %s  -  ", row.Pid, row.Name)
			code, out, errOut := f.run(t, "gc")
			if code != 0 || errOut != "" {
				t.Fatalf("gc: code=%d err=%q out=%q", code, errOut, out)
			}
			if alive && out != pressure+"nothing to remove\n" {
				t.Fatalf("gc dry run = %q; the live-terminal dash row is not an orphan", out)
			}
			if unverified && (!strings.HasPrefix(out, pressure+"nothing to remove\n"+unverifiedLine) || strings.Contains(out, "orphan processes:") || strings.Contains(out, "would stop") || strings.Contains(out, "gc --yes")) {
				t.Fatalf("gc dry run = %q; the unverifiable-terminal dash row is listed as unverified, never offered", out)
			}
			if !kept && !strings.Contains(out, fmt.Sprintf("orphan processes:\n%d  %s  -  ", row.Pid, row.Name)) {
				t.Fatalf("gc dry run = %q; the dash row of an absent terminal is an orphan", out)
			}
			assertProcsStopAlive(t, f, row)
			code, out, errOut = f.run(t, "gc", "--yes")
			if code != 0 || errOut != "" {
				t.Fatalf("gc --yes: code=%d err=%q out=%q", code, errOut, out)
			}
			if alive {
				if out != pressure+"nothing to remove\n" || !procAlive(t, f.env, row.Pid) {
					t.Fatalf("gc --yes = %q; the live-terminal dash row is kept, nothing signalled", out)
				}
			} else if unverified {
				if !strings.HasPrefix(out, pressure+"nothing to remove\n"+unverifiedLine) || strings.Contains(out, "stopped process") || !procAlive(t, f.env, row.Pid) {
					t.Fatalf("gc --yes = %q; the unverifiable-terminal dash row is kept, nothing signalled", out)
				}
				if rows := f.procsRows(t); len(rows) != 1 || rows[0] != row {
					t.Fatalf("rows changed: %+v", rows)
				}
			} else if !strings.Contains(out, fmt.Sprintf("stopped process %d (%s)\n", row.Pid, row.Name)) || procAlive(t, f.env, row.Pid) {
				t.Fatalf("gc --yes = %q; the orphan dash row is stopped", out)
			}
			hcHkAssertReads(t, f.fakeDir, "get", "ls")
		})
	}
}

// hcHkProject is a bare hc-mode project (no legacy fixture) for the file
// commands: the caller terminal p-worker of session ws, a state dir, and a
// fake herdr that fails the test if called.
func hcHkProject(t *testing.T, rules []fakecli.Rule) (platform.Env, string, string) {
	t.Helper()
	env, _ := commandFixture(t)
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	hcHkInstall(t, env, fakeDir, rules)
	sd := filepath.Join(env.Get("SOHO_DIR"), "ws")
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	return env, fakeDir, sd
}

// TestHcFriction is the friction reader and writer on hc: the log of the
// session's state dir is read with the same filters, and add and fixed append
// to it; no terminal is touched beyond the caller read.
func TestHcFriction(t *testing.T) {
	env, fakeDir, sd := hcHkProject(t, nil)
	logPath := filepath.Join(sd, "friction.log")
	header := "friction log (" + logPath + "): timestamp, level, command, message\n"
	code, out, errOut := hcHkRun(t, env, "friction")
	if code != 0 || errOut != "" || out != "no friction recorded under "+logPath+"\n" {
		t.Fatalf("empty: code=%d out=%q err=%q", code, out, errOut)
	}
	if err := os.WriteFile(logPath, []byte(frictionFilterLog), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = hcHkRun(t, env, "friction")
	if code != 0 || errOut != "" || out != header+frictionFilterLog {
		t.Fatalf("whole log: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = hcHkRun(t, env, "friction", "--level", "error", "--command", "collect")
	wantLines := "2026-09-30T11:30:00\terror(exit 4)\tcollect\tverify failed for 'beta'\n" +
		"2026-09-30T11:31:00\terror(exit 16)\tcollect\tanother failure for 'beta'\n"
	if code != 0 || errOut != "" || out != header+wantLines {
		t.Fatalf("filtered: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = hcHkRun(t, env, "friction", "--summary", "--level", "note")
	if code != 0 || errOut != "" || out != "count  level  command\n2  ?  ?\n1  ?  x\n1  note  friction\n" {
		t.Fatalf("summary: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = hcHkRun(t, env, "friction", "add", "--key", "hc-note", "a note for the next run")
	if code != 0 || errOut != "" || out != "recorded\n" {
		t.Fatalf("add: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = hcHkRun(t, env, "friction", "fixed", "hc-note", "handled in the skill")
	if code != 0 || errOut != "" || out != "recorded\n" {
		t.Fatalf("fixed: code=%d out=%q err=%q", code, out, errOut)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	tail := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(tail) < 2 || !strings.HasSuffix(tail[len(tail)-2], "\tnote\tfriction\t[hc-note] a note for the next run") || !strings.HasSuffix(tail[len(tail)-1], "\tfixed\tfriction\thc-note handled in the skill") {
		t.Fatalf("log tail = %q; want the note and the fixed line", tail)
	}
	// A rejected invocation keeps its exit code and appends the error in
	// the existing session log, just like other friction-aware commands.
	before := string(raw)
	code, _, errOut = hcHkRun(t, env, "friction", "--level", "bogus")
	if code != 2 || !strings.Contains(errOut, "friction: --level expects warning, note or error (got 'bogus')") {
		t.Fatalf("bad level: code=%d err=%q", code, errOut)
	}
	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	added, ok := strings.CutPrefix(string(after), before)
	if !ok {
		t.Fatalf("existing log changed: %q", after)
	}
	fields := strings.Split(strings.TrimSuffix(added, "\n"), "\t")
	if len(fields) != 4 || fields[1] != "error(exit 2)" || fields[2] != "friction" || fields[3] != "friction: --level expects warning, note or error (got 'bogus')" || !strings.HasSuffix(added, "\n") {
		t.Fatalf("unexpected appended error: %q", added)
	}
	if _, err := time.Parse(time.RFC3339, fields[0]); err != nil {
		t.Fatalf("bad error timestamp %q: %v", fields[0], err)
	}
	hcHkAssertReads(t, fakeDir, "get")
}

// TestHcRoster is the roster on hc: the worker rows of the state dir with
// the live state hc reports, then the other live terminals of the session
// (the kind from the record's reporting app), and with --scope server the
// terminals of other sessions too. A finished record is not live, a terminal
// of another session never shows in the workspace scope, and the pane list
// that hc does not have is never read.
func TestHcRoster(t *testing.T) {
	const listing = `{"terminals":[` +
		`{"address":"t7","name":"worker","state":"live","reported":true,"status":{"state":"idle"},"labels":{"soho.session":"ws"}},` +
		`{"address":"t8","name":"retired","state":"finished","labels":{"soho.session":"ws"}},` +
		`{"address":"t11","name":"scout","state":"live","reported":true,"status":{"state":"working","app":"claude"},"labels":{"soho.session":"ws"}},` +
		`{"address":"t20","name":"foreign","state":"live","reported":true,"status":{"state":"idle","app":"codex"},"labels":{"soho.session":"sY"}},` +
		`{"address":"t21","state":"live","reported":true,"status":{"state":"blocked"}}` +
		`]}`
	build := func(t *testing.T, listRule fakecli.Rule) (platform.Env, string, string) {
		env, fakeDir, sd := hcHkProject(t, []fakecli.Rule{listRule})
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" +
			"worker\tt7\tcodex\timplementer\topenai\t1\t/work\tnow\tgpt-5\ttask\timplementer\t\n" +
			"gone-one\tt9\tgrok\treviewer\txai\t1\t/work\tnow\tgrok-4\ttask\treviewer\t\n"
		if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "task-worker"), []byte("build the thing\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return env, fakeDir, sd
	}
	row := func(fields ...string) string {
		return fmt.Sprintf("%-20s %-18s %-8s %-8s %-16s %-9s", fields[0], fields[1], fields[2], fields[3], fields[4], fields[5])
	}
	listRule := fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: listing}
	header := row("NAME", "ROLE", "KIND", "PANE", "TAB", "STATE") + fmt.Sprintf(" %-16s CWD TASK\n", "REPORT")
	workerLine := row("worker", "implementer", "codex", "t7", "-", "idle") + fmt.Sprintf(" %-16s /work build the thing\n", "none")
	goneLine := row("gone-one", "reviewer", "grok", "t9", "-", "gone") + fmt.Sprintf(" %-16s /work -\n", "none")
	t.Run("workspace scope", func(t *testing.T) {
		env, fakeDir, _ := build(t, listRule)
		code, out, errOut := hcHkRun(t, env, "roster")
		want := header + workerLine + goneLine +
			"\n# other live agents in workspace ws (not spawned by this skill)\n" +
			row("scout", "-", "claude", "t11", "-", "working") + "\n" +
			"\nlayout=split reuse_workers=on multi_role=on auto_approve=off\n"
		if code != 0 || errOut != "" || out != want {
			t.Fatalf("code=%d err=%q\nout=%q\nwant=%q", code, errOut, out, want)
		}
		hcHkAssertReads(t, fakeDir, "get", "ls")
		for _, call := range hcHkCalls(t, fakeDir) {
			if call.Argv[0] == "ls" && len(call.Argv) != 2 {
				t.Fatalf("roster listed with a label filter: %#v", call.Argv)
			}
		}
	})
	t.Run("server scope adds the other sessions", func(t *testing.T) {
		env, fakeDir, _ := build(t, listRule)
		code, out, errOut := hcHkRun(t, env, "roster", "--scope", "server")
		want := header + workerLine + goneLine +
			"\n# other live agents on this server (not spawned by this skill)\n" +
			"# workspace hc-t21\n" +
			row("-", "-", "null", "t21", "-", "blocked") + "\n" +
			"# workspace sY\n" +
			row("foreign", "-", "codex", "t20", "-", "idle") + "\n" +
			"# workspace ws\n" +
			row("scout", "-", "claude", "t11", "-", "working") + "\n" +
			"\nlayout=split reuse_workers=on multi_role=on auto_approve=off\n"
		if code != 0 || errOut != "" || out != want {
			t.Fatalf("code=%d err=%q\nout=%q\nwant=%q", code, errOut, out, want)
		}
		hcHkAssertReads(t, fakeDir, "get", "ls")
	})
	t.Run("json", func(t *testing.T) {
		env, _, _ := build(t, listRule)
		code, out, errOut := hcHkRun(t, env, "roster", "--json")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		var got struct {
			Workspace string `json:"workspace"`
			Scope     string `json:"scope"`
			Agents    []struct {
				Name, Role, Kind, Pane, Tab, State, Report, Cwd, Task string
			} `json:"agents"`
			Others []struct {
				Name, Kind, Pane, Tab, State, Workspace string
			} `json:"others"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		if got.Workspace != "ws" || got.Scope != "workspace" || len(got.Agents) != 2 || len(got.Others) != 1 {
			t.Fatalf("roster = %+v", got)
		}
		w, g, o := got.Agents[0], got.Agents[1], got.Others[0]
		if w.Name != "worker" || w.Pane != "t7" || w.State != "idle" || w.Kind != "codex" || w.Report != "none" || w.Task != "build the thing" || w.Tab != "" {
			t.Fatalf("worker = %+v", w)
		}
		if g.Name != "gone-one" || g.State != "gone" {
			t.Fatalf("gone-one = %+v", g)
		}
		if o.Name != "scout" || o.Kind != "claude" || o.Pane != "t11" || o.State != "working" || o.Workspace != "ws" {
			t.Fatalf("other = %+v", o)
		}
	})
	t.Run("a failed listing is a friction error with exit 4", func(t *testing.T) {
		env, fakeDir, _ := build(t, fakecli.Rule{Argv: []string{"ls", "--json"}, Code: 1, Stderr: `{"error":{"kind":"internal","message":"core unreachable"}}`})
		code, out, errOut := hcHkRun(t, env, "roster")
		if code != 4 || out != "" || !strings.HasPrefix(errOut, "soho: roster: cannot list the terminals (") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		hcHkAssertReads(t, fakeDir, "get", "ls")
	})
}

// TestHcCollect is collect on hc: the report file is printed when there is
// one; without a report a working worker is waited for (exit 4), an
// unavailable state exits 4, and otherwise the recent output is read through
// hc read (exit 6 after a successful read).
func TestHcCollect(t *testing.T) {
	const recent = `{"text":"line one\nline two\n","complete":true}`
	build := func(t *testing.T, rules []fakecli.Rule) (platform.Env, string, string) {
		env, fakeDir, sd := hcHkProject(t, rules)
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" +
			"worker\tt7\tcodex\timplementer\topenai\t1\t" + t.TempDir() + "\tnow\tgpt-5\ttask\timplementer\t\n"
		if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		return env, fakeDir, sd
	}
	readCalls := func(t *testing.T, fakeDir string) [][]string {
		t.Helper()
		var out [][]string
		for _, call := range hcHkCalls(t, fakeDir) {
			if call.Argv[0] == "read" {
				out = append(out, call.Argv)
			}
		}
		return out
	}
	t.Run("prints the report", func(t *testing.T) {
		env, fakeDir, sd := build(t, nil)
		report := filepath.Join(sd, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte("all done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(core.LastReportPath(sd, "worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := hcHkRun(t, env, "collect", "worker")
		if code != 0 || errOut != "" || out != "<!-- report: "+report+" -->\nall done\n" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		hcHkAssertReads(t, fakeDir, "get")
	})
	t.Run("verify reads the sha256 lines", func(t *testing.T) {
		env, fakeDir, sd := build(t, nil)
		file := filepath.Join(t.TempDir(), "artifact.txt")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(sd, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		// sha256 of "x".
		line := "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881  " + file + "\n"
		if err := os.WriteFile(report, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(core.LastReportPath(sd, "worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := hcHkRun(t, env, "collect", "worker", "--verify")
		if code != 0 || errOut != "" || out != "ok "+file+"\nverified 1: ok 1, changed 0, missing 0\n" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		hcHkAssertReads(t, fakeDir, "get")
	})
	t.Run("a working worker without a report is waited for", func(t *testing.T) {
		env, fakeDir, _ := build(t, []fakecli.Rule{{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("t7", "worker", "working")}})
		code, out, errOut := hcHkRun(t, env, "collect", "worker")
		if code != 4 || out != "" || !strings.Contains(errOut, "'worker' is working and has no report yet") || !strings.Contains(errOut, "soho wait worker") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if reads := readCalls(t, fakeDir); len(reads) != 0 {
			t.Fatalf("a worker still working was read: %#v", reads)
		}
	})
	t.Run("an unavailable state exits 4 and names hc get", func(t *testing.T) {
		env, fakeDir, _ := build(t, []fakecli.Rule{{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"kind":"internal","message":"core unreachable"}}`}})
		code, out, errOut := hcHkRun(t, env, "collect", "worker")
		if code != 4 || out != "" || !strings.Contains(errOut, "and hc get failed: ") || strings.Contains(errOut, "herdr") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if reads := readCalls(t, fakeDir); len(reads) != 0 {
			t.Fatalf("an unavailable worker was read: %#v", reads)
		}
	})
	t.Run("an idle worker without a report falls back to the recent output", func(t *testing.T) {
		env, fakeDir, _ := build(t, []fakecli.Rule{
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("t7", "worker", "idle")},
			{Argv: []string{"read", "worker", "--lines", "120", "--json"}, Stdout: recent},
		})
		code, out, errOut := hcHkRun(t, env, "collect", "worker")
		if code != 6 || out != "line one\nline two\n" || !strings.Contains(errOut, "falling back to recent terminal output") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if reads := readCalls(t, fakeDir); len(reads) != 1 {
			t.Fatalf("reads = %#v; want one read of 120 lines", reads)
		}
		hcHkAssertReads(t, fakeDir, "get", "read")
	})
	t.Run("an explicit --lines reads even a working worker", func(t *testing.T) {
		env, fakeDir, _ := build(t, []fakecli.Rule{
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("t7", "worker", "working")},
			{Argv: []string{"read", "worker", "--lines", "5", "--json"}, Stdout: recent},
		})
		code, out, _ := hcHkRun(t, env, "collect", "worker", "--lines", "5")
		if code != 6 || out != "line one\nline two\n" {
			t.Fatalf("code=%d out=%q", code, out)
		}
		reads := readCalls(t, fakeDir)
		if len(reads) != 1 || strings.Join(reads[0], " ") != "read worker --lines 5 --json" {
			t.Fatalf("reads = %#v; want one read of 5 lines", reads)
		}
	})
	t.Run("an agent with no roster row is read without a state read", func(t *testing.T) {
		env, fakeDir, _ := build(t, []fakecli.Rule{{Argv: []string{"read", "stranger", "--lines", "120", "--json"}, Stdout: recent}})
		code, out, _ := hcHkRun(t, env, "collect", "stranger")
		if code != 6 || out != "line one\nline two\n" {
			t.Fatalf("code=%d out=%q", code, out)
		}
		for _, call := range hcHkCalls(t, fakeDir) {
			if call.Argv[0] == "get" && call.Argv[1] == "stranger" {
				t.Fatalf("state read for an agent outside the roster: %#v", call.Argv)
			}
		}
	})
	t.Run("a failed read exits 4 with its cause", func(t *testing.T) {
		env, _, _ := build(t, []fakecli.Rule{
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("t7", "worker", "idle")},
			{Argv: []string{"read", "worker", "--lines", "120", "--json"}, Code: 3, Stderr: hcHkNotFound},
		})
		code, out, errOut := hcHkRun(t, env, "collect", "worker")
		if code != 4 || out != "" || !strings.Contains(errOut, "collect: could not read the recent output of 'worker': ") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run("a line count hc cannot take is refused before any read", func(t *testing.T) {
		for _, bad := range []string{"abc", "-3", "1.5", ""} {
			env, fakeDir, _ := build(t, []fakecli.Rule{{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcHkRecord("t7", "worker", "idle")}})
			code, out, errOut := hcHkRun(t, env, "collect", "worker", "--lines", bad)
			if code != 2 || out != "" || !strings.Contains(errOut, "collect: --lines expects a non-negative integer under transport=hc, got '"+bad+"'") {
				t.Fatalf("--lines %q: code=%d out=%q err=%q", bad, code, out, errOut)
			}
			if reads := readCalls(t, fakeDir); len(reads) != 0 {
				t.Fatalf("--lines %q reached hc read: %#v", bad, reads)
			}
		}
	})
}

// TestHcHousekeepingReadOnlyAndOutsideATerminal pins two boundaries on hc:
// under SOHO_NOWRITE=1 the read invocations run and the writing ones keep
// the generic refusal before any hc call, and outside an hc terminal the
// commands that need the session die with the hc message instead of a
// herdr one.
func TestHcHousekeepingReadOnlyAndOutsideATerminal(t *testing.T) {
	t.Run("nowrite", func(t *testing.T) {
		env, fakeDir, _ := hcHkProject(t, nil)
		env["SOHO_NOWRITE"] = "1"
		for _, args := range [][]string{{"copies"}, {"procs"}} {
			if code, out, errOut := hcHkRun(t, env, args...); code != 0 || errOut != "" || !strings.Contains(out, "no registered") {
				t.Fatalf("%v: code=%d out=%q err=%q", args, code, out, errOut)
			}
		}
		calls := len(hcHkCalls(t, fakeDir))
		for _, args := range [][]string{{"gc", "--yes"}, {"copies", "add", "/nowhere"}, {"friction", "add", "x"}, {"procs", "stop", "1"}} {
			code, out, errOut := hcHkRun(t, env, args...)
			if code != 2 || out != "" || !strings.Contains(errOut, "SOHO_NOWRITE=1 is read-only") {
				t.Fatalf("%v: code=%d out=%q err=%q; want the generic read-only refusal", args, code, out, errOut)
			}
		}
		// A refused invocation may read the caller's own session (hc get of
		// the caller) and nothing else.
		for _, call := range hcHkCalls(t, fakeDir)[calls:] {
			if strings.Join(call.Argv, " ") != "get p-worker --json" {
				t.Fatalf("a refused invocation reached hc beyond the caller read: %#v", call.Argv)
			}
		}
	})
	t.Run("outside a terminal", func(t *testing.T) {
		env, fakeDir, _ := hcHkProject(t, nil)
		delete(env, "HC_TERMINAL_ID")
		for _, args := range [][]string{{"roster"}, {"copies"}, {"gc"}, {"procs"}, {"friction"}, {"collect", "worker"}} {
			code, out, errOut := hcHkRun(t, env, args...)
			if code != 2 || out != "" || !strings.Contains(errOut, "transport=hc: not inside an hc terminal (HC_TERMINAL_ID is not set)") {
				t.Fatalf("%v: code=%d out=%q err=%q", args, code, out, errOut)
			}
		}
		hcHkAssertReads(t, fakeDir)
	})
}
