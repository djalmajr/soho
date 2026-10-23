package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/job"
	"github.com/djalmajr/soho/internal/platform"
)

// Control replies and delivery events use separate counters: compare the
// public CLI replies with the events produced by the real supervisor path.
func TestJobControlSeqDelivery(t *testing.T) {
	var seqs []int
	f, _, _ := partialPublicFix(t, false, func(f *jobFix, co string) {
		f.env["TMPDIR"] = t.TempDir()
		body := filepath.Join(co, "control-body.md")
		if err := os.WriteFile(body, []byte("dispatcher feedback"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i, sub := range []string{"amend", "amend", "send"} {
			code, out, errOut := f.run(t, sub, "--id", "job-1", body)
			var reply struct {
				Seq int `json:"seq"`
			}
			if err := json.Unmarshal([]byte(out), &reply); err != nil || code != 0 || errOut != "" || reply.Seq != i+1 {
				t.Fatalf("%s reply code=%d out=%q stderr=%q decode=%v want numeric seq=%d", sub, code, out, errOut, err, i+1)
			}
			seqs = append(seqs, reply.Seq)
		}
	})
	code, out, errOut := f.run(t, "events", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("events code=%d out=%q stderr=%q", code, out, errOut)
	}
	var received []job.Event
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var event job.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Tipo == "amend_received" {
			received = append(received, event)
		}
	}
	if len(received) != 3 {
		t.Fatalf("delivery events=%+v; output=%s", received, out)
	}
	for i, event := range received {
		wantSummary := "amendment delivered"
		if i == 2 {
			wantSummary = "note delivered"
		}
		if event.Refs["seq_ref"] != strconv.Itoa(seqs[i]) || event.Resumo != wantSummary {
			t.Fatalf("request seq=%d delivery=%+v want summary=%q", seqs[i], event, wantSummary)
		}
	}
}

func TestJobStatusControlSeqAbsent(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	f.startJob(t, co)
	f.env["TMPDIR"] = t.TempDir()
	code, out, errOut := f.run(t, "status", "--id", "job-1")
	if code != 0 || errOut != "" || out != jobControlStatusLine {
		t.Fatalf("status changed: code=%d out=%q stderr=%q", code, out, errOut)
	}
}

// The done+partial public-path contract: for a done job that carries a
// partial outcome of its own worker collaboration — and for the complete
// control — the three public surfaces agree. The report.json the
// supervisor wrote (the fields stand from the terminal transition on),
// the actual `job status --id` line, and the actual `job wait --id` line
// with its exit code. The supervisor runs through the production wiring
// (job.NewSupervisor over the self CLI): the team commands run against
// the fake herdr-soho executable (this test binary, re-exec'd and armed
// through the HERDR_FAKE environment value) and the workspace close
// against the fake herdr executable (the same binary, linked into the
// fixture PATH and armed the same way).

func init() {
	// The fakes arm through the environment: every subprocess the
	// supervisor runs (the self CLI re-exec of this test binary and the
	// linked herdr executable) stands with HERDR_FAKE=1 in its env, and
	// nothing else does. The herdr commands open with workspace; every
	// other command is a herdr-soho team command.
	if os.Getenv("HERDR_FAKE") != "1" {
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "workspace" || os.Args[1] == "pane") {
		os.Exit(partialPublicFakeHerdr(os.Args[1:]))
	}
	os.Exit(partialPublicFakeHerdrSoho(os.Args[1:]))
}

// partialPublicFakeHerdrSoho answers the team commands the supervisor's
// self CLI runs: the session sets and the config, the spawn of the job
// orchestrator, the dispatches, the orchestrator status (done, with the
// report path from the environment), the releases, the roster and the gc.
func partialPublicFakeHerdrSoho(args []string) int {
	if len(args) == 0 {
		return 1
	}
	switch args[0] {
	case "session":
		return 0
	case "config":
		fmt.Println("max_workers 4 defaults")
		return 0
	case "spawn":
		fmt.Println(`{"name":"orch-1","pane":"w1:p1"}`)
		return 0
	case "dispatch", "send":
		return 0
	case "status":
		if len(args) < 2 || args[1] != "orch-1" {
			return 1
		}
		if path := os.Getenv("FAKE_STATUS_REPORT"); path != "" {
			fmt.Printf("orch-1\tdone\t%s\n", path)
			return 0
		}
		return 1
	case "release":
		return 0
	case "roster":
		fmt.Print("NAME ROLE KIND PANE\n" +
			"orch-1 job-orchestrator openai w1:p1\n" +
			"impl-1 worker openai w1:p2\n" +
			"rev-1 worker anthropic w1:p3\n")
		return 0
	case "gc":
		return 0
	}
	return 1
}

// partialPublicFakeHerdr answers the herdr commands the supervisor runs
// over the workspace: the close and the list.
func partialPublicFakeHerdr(args []string) int {
	if len(args) >= 2 && args[0] == "workspace" && (args[1] == "close" || args[1] == "list") {
		return 0
	}
	if len(args) >= 2 && args[0] == "pane" && args[1] == "list" {
		// The close watch's pane list: no pane outside the job.
		fmt.Println(`{"result":{"panes":[]}}`)
		return 0
	}
	return 1
}

// partialPublicClock is the store clock of the fixture: the sleep advances
// the monotonic reading without pausing, so the collaboration's grace runs
// on virtual time.
type partialPublicClock struct {
	now  time.Time
	mono int64
}

func (c *partialPublicClock) Now() time.Time        { return c.now }
func (c *partialPublicClock) Monotonic() int64      { return c.mono }
func (c *partialPublicClock) Sleep(d time.Duration) { c.mono += int64(d) }

// partialPublicFix wires one done job of the fixture into the supervisor
// run: the state walks to running with the recorded worktree and
// workspace, the fake executables stand in the fixture PATH, and the
// supervisor runs to its terminal outcome. withCollab adds the worker
// collaboration the capture must end partial. The fixture is returned:
// the CLI surfaces run on the same env, over the same repos root.
func partialPublicFix(t *testing.T, withCollab bool, beforeRun ...func(*jobFix, string)) (*jobFix, string, string) {
	t.Helper()
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	store := f.store(t, co)
	id := ""
	now := time.Now()
	store.Clock = &partialPublicClock{now: now}
	if _, err := store.Start("job-1", []byte(jobTestBrief)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", "preparing"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record("job-1", func(st *job.State) {
		st.Dir = co
		st.WorkspaceID = "w1"
		st.Modo = "workspace"
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", "running"); err != nil {
		t.Fatal(err)
	}
	if withCollab {
		wsDir := filepath.Join(co, ".soho", "w1")
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, "code.go"), []byte("package main\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		cstore := collaboration.Store{StateDir: wsDir}
		created, err := cstore.Create(collaboration.Assignment{
			Workspace: "w1",
			Root:      co,
			BriefHash: collaboration.Hash([]byte(jobTestBrief)),
			Paths:     []string{"code.go"},
			Author:    collaboration.Participant{Name: "impl-1", Pane: "w1:p2", Family: "openai", Started: now.Format(time.RFC3339), Session: "s-impl-1", Cwd: cwd},
			Reviewer:  collaboration.Participant{Name: "rev-1", Pane: "w1:p3", Family: "anthropic", Started: now.Format(time.RFC3339), Session: "s-rev-1", Cwd: cwd},
		})
		if err != nil {
			t.Fatal(err)
		}
		id = created.ID
	}
	// The orchestrator's done report (no item partials: the partial fact
	// is the collaboration's, not the items').
	orchReport := filepath.Join(co, "orchestrator-report.md")
	if err := os.WriteFile(orchReport, []byte("# report\n\n- [done] item\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake executables: the herdr-soho self CLI is this test binary
	// re-exec'd with its flag; the herdr adapter is the same binary
	// linked into the fixture PATH, armed through the environment.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	herdrBin := filepath.Join(f.root, "bin", "herdr")
	if runtime.GOOS == "windows" {
		// Windows finds a program on PATH only with its extension, and a
		// symbolic link needs a privilege the runner may lack: an
		// independent byte copy named herdr.exe.
		data, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(herdrBin+".exe", data, 0o755); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Symlink(exe, herdrBin); err != nil {
		t.Fatal(err)
	}
	env := make(platform.Env, len(f.env)+2)
	for k, v := range f.env {
		env[k] = v
	}
	env["FAKE_STATUS_REPORT"] = orchReport
	env["HERDR_FAKE"] = "1"
	for _, before := range beforeRun {
		before(f, co)
	}
	sup, err := job.NewSupervisor(store, "job-1", env, exe, func(m string) { t.Fatalf("friction: %s", m) })
	if err != nil {
		t.Fatal(err)
	}
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("supervisor Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("supervisor exit = %d, want 0", exit)
	}
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")
	return f, jobDir, id
}

// partialPublicAgree parses the three public surfaces and returns their
// partial flag and ids: the report.json the supervisor wrote, the actual
// job status line, and the actual job wait line (with its exit code).
func partialPublicAgree(t *testing.T, f *jobFix, jobDir string) (partial bool, statusIDs, waitIDs []string, reportIDs []string, waitExit int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatalf("read the report the supervisor wrote: %v", err)
	}
	var rep struct {
		Status                string   `json:"status"`
		Partial               bool     `json:"partial"`
		PartialCollaborations []string `json:"partial_collaborations"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("unmarshal the report: %v\n%s", err, raw)
	}
	if rep.Status != "done" {
		t.Fatalf("report status = %q, want done", rep.Status)
	}
	partial = rep.Partial
	reportIDs = rep.PartialCollaborations
	code, out, errOut := f.run(t, "status", "--id", "job-1")
	if code != 0 || errOut != "" {
		t.Fatalf("job status code=%d out=%q err=%q", code, out, errOut)
	}
	statusIDs = partialPublicLine(t, out, "status")
	code, out, errOut = f.run(t, "wait", "--id", "job-1", "--timeout", "1000")
	waitExit = code
	if code != 0 || errOut != "" {
		t.Fatalf("job wait code=%d out=%q err=%q", code, out, errOut)
	}
	waitIDs = partialPublicLine(t, out, "wait")
	return partial, statusIDs, waitIDs, reportIDs, waitExit
}

// partialPublicLine is the partial fact of one actual CLI JSON line: the
// line carries the fields with the same values as the report.
func partialPublicLine(t *testing.T, out, what string) []string {
	t.Helper()
	trimmed := strings.TrimSpace(out)
	var line struct {
		Status                string   `json:"status"`
		Partial               bool     `json:"partial"`
		PartialCollaborations []string `json:"partial_collaborations"`
	}
	if err := json.Unmarshal([]byte(trimmed), &line); err != nil {
		t.Fatalf("unmarshal the %s line: %v\n%s", what, err, out)
	}
	if line.Status != "done" {
		t.Fatalf("%s line status = %q, want done", what, line.Status)
	}
	if !line.Partial && line.PartialCollaborations == nil {
		t.Fatalf("%s line lacks the partial fields: %s", what, trimmed)
	}
	return line.PartialCollaborations
}

// The done-with-partial case: the supervisor ends the job's own
// collaboration partial, and the report, the status line and the wait
// line carry the same partial fact.
func TestJobPartialDoneCarriesThePartialFact(t *testing.T) {
	f, jobDir, id := partialPublicFix(t, true)
	// The report the supervisor wrote stands with the exact values, from
	// the terminal transition on.
	partial, statusIDs, waitIDs, reportIDs, waitExit := partialPublicAgree(t, f, jobDir)
	if !partial {
		t.Fatal("the report stands partial=false with a partial collaboration")
	}
	for _, ids := range [][]string{statusIDs, waitIDs, reportIDs} {
		if len(ids) != 1 || ids[0] != id {
			t.Fatalf("a surface carries %v, want the single %v", ids, []string{id})
		}
	}
	if waitExit != 0 {
		t.Fatalf("wait exit = %d, want 0", waitExit)
	}
}

// The complete control: the same done job without a collaboration
// carries the empty values on all three surfaces.
func TestJobPartialControlCarriesTheEmptyFact(t *testing.T) {
	f, jobDir, id := partialPublicFix(t, false)
	if id != "" {
		t.Fatal("the control fixture must have no collaboration")
	}
	partial, statusIDs, waitIDs, reportIDs, waitExit := partialPublicAgree(t, f, jobDir)
	if partial {
		t.Fatal("the report stands partial=true without a collaboration")
	}
	for _, surface := range []struct {
		name string
		ids  []string
	}{{"status", statusIDs}, {"wait", waitIDs}, {"report", reportIDs}} {
		if surface.ids == nil || len(surface.ids) != 0 {
			t.Fatalf("the %s surface carries %v, want the empty array", surface.name, surface.ids)
		}
	}
	if waitExit != 0 {
		t.Fatalf("wait exit = %d, want 0", waitExit)
	}
}
