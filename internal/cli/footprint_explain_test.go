package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// explainFootprintHelperRoleEnv is the role of the re-executed test binary
// that stands in for a registered worker process (the explain footprint
// test registers its pid with its real identity).
const explainFootprintHelperRoleEnv = "SOHO_EXPLAIN_FOOTPRINT_HELPER_ROLE"

func TestExplainFootprintHelper(t *testing.T) {
	if os.Getenv(explainFootprintHelperRoleEnv) == "" {
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(1)
	}
	_ = w
	buf := make([]byte, 1)
	_, _ = r.Read(buf)
	os.Exit(1) // unreachable: the read blocks until a signal stops the process
}

func TestExplainFootprintTextAndGate(t *testing.T) {
	t.Run("the exact line with processes", func(t *testing.T) {
		fp := &core.Footprint{Copies: 2, CopiesBytes: 3072, StateBytes: 512, ScratchBytes: 128, Total: 3712, Running: 3, RSS: 1 << 30}
		want := "Footprint: 3.6 KB logical size (copies 3.0 KB in 2, state 512 B, scratch 128 B); 3 registered processes using 1.0 GB of memory."
		if got := explainFootprintText(fp); got != want {
			t.Fatalf("line=%q\nwant=%q", got, want)
		}
	})
	t.Run("the exact line with an unreadable RSS", func(t *testing.T) {
		fp := &core.Footprint{Copies: 2, CopiesBytes: 3072, StateBytes: 512, ScratchBytes: 128, Total: 3712, Running: 3, RSS: 1 << 30, Unreadable: 1}
		want := "Footprint: 3.6 KB logical size (copies 3.0 KB in 2, state 512 B, scratch 128 B); 3 registered processes using 1.0 GB of memory (1 unreadable)."
		if got := explainFootprintText(fp); got != want {
			t.Fatalf("line=%q\nwant=%q", got, want)
		}
	})
	t.Run("the exact line without a running process", func(t *testing.T) {
		fp := &core.Footprint{Copies: 1, CopiesBytes: 100, StateBytes: 200, ScratchBytes: 300, Total: 600}
		want := "Footprint: 600 B logical size (copies 100 B in 1, state 200 B, scratch 300 B); no registered process running."
		if got := explainFootprintText(fp); got != want {
			t.Fatalf("line=%q\nwant=%q", got, want)
		}
	})
	t.Run("singular counts keep the same text", func(t *testing.T) {
		fp := &core.Footprint{Copies: 1, CopiesBytes: 100, StateBytes: 200, ScratchBytes: 300, Total: 600, Running: 1, RSS: 4096}
		want := "Footprint: 600 B logical size (copies 100 B in 1, state 200 B, scratch 300 B); 1 registered processes using 4.0 KB of memory."
		if got := explainFootprintText(fp); got != want {
			t.Fatalf("line=%q\nwant=%q", got, want)
		}
	})
	t.Run("the gate", func(t *testing.T) {
		cases := []struct {
			name    string
			fp      *core.Footprint
			limit   int64
			limitOK bool
			visible bool
		}{
			{"a copy on disk shows the line", &core.Footprint{Copies: 1}, 0, false, true},
			{"a running process shows the line", &core.Footprint{Running: 1}, 0, false, true},
			{"the total reaching the limit shows the line", &core.Footprint{Total: 5 << 30}, 5, true, true},
			{"above the limit shows the line", &core.Footprint{Total: 5<<30 + 1}, 5, true, true},
			{"nothing registered and below the limit hides the line", &core.Footprint{Total: 5<<30 - 1}, 5, true, false},
			{"a zero limit never shows the line", &core.Footprint{Total: 10 << 30}, 0, true, false},
			{"an unreadable limit never shows the line", &core.Footprint{Total: 10 << 30}, 0, false, false},
		}
		for _, tc := range cases {
			if got := explainFootprintGate(tc.fp, tc.limit, tc.limitOK); got != tc.visible {
				t.Fatalf("%s: visible=%t, want %t", tc.name, got, tc.visible)
			}
		}
	})
}

// agentsTsvBuild is the minimal roster the explain running form needs (the
// same shape the tm6 explain parity test writes).
const agentsTsvBuild = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nbuild\tp1\tgrok\timplementer\txai\t1\t/work\tt1\tgrok-4.7\tfull\timplementer\tbuild\n"

// footprintLineWant builds the disk part of the exact explain line for a
// copy of copyBytes inside a state of stateBytes (scratch is 0 in the
// fixture): the disk part is deterministic, the RSS is read live.
func footprintLineWant(stateBytes, copyBytes int64) string {
	total := stateBytes + copyBytes
	return "Footprint: " + core.HumanSize(total) + " logical size (copies " + core.HumanSize(copyBytes) + " in 1, state " + core.HumanSize(stateBytes) + ", scratch 0 B); "
}

func TestExplainCommandFootprintLine(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	bin := env.Get("PATH")
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
	}); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, bin)
	// The tm6 PATH holds only the fake herdr; append the real one so the
	// in-process ps reads (liveness, RSS) resolve exactly as in production.
	if real := os.Getenv("PATH"); real != "" {
		env["PATH"] = bin + string(os.PathListSeparator) + real
	}
	sd := filepath.Join(env.Get("SOHO_DIR"), env.Get("HERDR_WORKSPACE_ID"))
	copyDir := filepath.Join(env.Get("SOHO_DIR"), "footprint-copy")
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	copyBytes := int64(3072)
	if err := os.WriteFile(filepath.Join(copyDir, "f.bin"), make([]byte, copyBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	copiesTsv := "path\towner\tpane\tcreated\tsource\torigin\n" + copyDir + "\tbuild\tp1\t2026-01-01T00:00:00Z\ts\tadd\n"

	t.Run("running form: the line sits after the lane lines and before the blank", func(t *testing.T) {
		tm6Write(t, filepath.Join(sd, "copies.tsv"), copiesTsv)
		tm6Write(t, filepath.Join(sd, "agents.tsv"), agentsTsvBuild)
		cmd := exec.Command(os.Args[0], "-test.run=^TestExplainFootprintHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), explainFootprintHelperRoleEnv+"=1")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pid := cmd.Process.Pid
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		var started string
		var live platform.ProcLiveness
		for i := 0; i < 200; i++ {
			started, _, live = platform.ReadProcFull(pid, env)
			if live == platform.ProcRunning {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if live != platform.ProcRunning {
			t.Fatalf("the helper child never read as running (liveness %v)", live)
		}
		if err := os.WriteFile(filepath.Join(sd, "procs.tsv"), []byte("pid\tstarted\tname\towner\tpane\tcreated\n"+strconv.Itoa(pid)+"\t"+started+"\tbuild\tbuild\tp1\t2026-01-01T00:00:00Z\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stateBytes := core.DirSize(sd)
		code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
		if code != 0 {
			t.Fatalf("explain exit=%d stderr=%q", code, stderr)
		}
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		laneIdx, footIdx := -1, -1
		for i, line := range lines {
			if line == "review: not started" { // the last lane line
				laneIdx = i
			}
			if strings.HasPrefix(line, "Footprint: ") {
				footIdx = i
			}
		}
		if laneIdx < 0 || footIdx < 0 || footIdx != laneIdx+1 {
			t.Fatalf("the footprint line is not right after the lane lines:\n%s", out)
		}
		if lines[footIdx+1] != "" {
			t.Fatalf("the line after the footprint line must be the blank line:\n%s", out)
		}
		// The disk part is exact (the state is measured with the same walk
		// the implementation uses); the RSS is read live, so its size is
		// dynamic and only the shape is asserted.
		wantDisk := footprintLineWant(stateBytes, copyBytes)
		if !strings.HasPrefix(lines[footIdx], wantDisk) {
			t.Fatalf("disk part=%q\nwant prefix=%q", lines[footIdx], wantDisk)
		}
		tail := strings.TrimPrefix(lines[footIdx], wantDisk)
		if !regexp.MustCompile(`^\d+ registered processes using [0-9.]+ (B|KB|MB|GB|TB) of memory\.$`).MatchString(tail) {
			t.Fatalf("process tail=%q", tail)
		}
	})

	t.Run("running form with copies only: the exact line", func(t *testing.T) {
		_ = os.Remove(filepath.Join(sd, "procs.tsv"))
		tm6Write(t, filepath.Join(sd, "copies.tsv"), copiesTsv)
		tm6Write(t, filepath.Join(sd, "agents.tsv"), agentsTsvBuild)
		stateBytes := core.DirSize(sd)
		code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
		if code != 0 {
			t.Fatalf("explain exit=%d stderr=%q", code, stderr)
		}
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "Footprint: ") {
				want := footprintLineWant(stateBytes, copyBytes) + "no registered process running."
				if line != want {
					t.Fatalf("line=%q\nwant=%q", line, want)
				}
				if lines[i+1] != "" {
					t.Fatalf("the line after the footprint line must be the blank line:\n%s", out)
				}
				return
			}
		}
		t.Fatalf("no footprint line:\n%s", out)
	})

	t.Run("idle form: a blank line between the paragraph and the line", func(t *testing.T) {
		// A header-only roster reads as zero rows (idle) and keeps the state
		// size stable: StateDir only creates the roster when it is absent.
		headerOnly := agentsTsvBuild
		if i := strings.IndexByte(agentsTsvBuild, '\n'); i >= 0 {
			headerOnly = agentsTsvBuild[:i+1]
		}
		tm6Write(t, filepath.Join(sd, "agents.tsv"), headerOnly)
		tm6Write(t, filepath.Join(sd, "copies.tsv"), copiesTsv)
		stateBytes := core.DirSize(sd)
		code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
		if code != 0 {
			t.Fatalf("explain exit=%d stderr=%q", code, stderr)
		}
		want := strings.Join(explainIdleParagraph(), "\n") + "\n\n" +
			footprintLineWant(stateBytes, copyBytes) + "no registered process running." + "\n"
		if out != want {
			t.Fatalf("out:\n%s\nwant:\n%s", out, want)
		}
	})

	t.Run("nothing registered and below the limit: byte-identical to today", func(t *testing.T) {
		_ = os.RemoveAll(sd)
		tm6Write(t, filepath.Join(sd, "agents.tsv"), agentsTsvBuild)
		code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
		if code != 0 {
			t.Fatalf("explain exit=%d stderr=%q", code, stderr)
		}
		want := "Panels: 4.\nbuild: implementer, grok, model grok-4.7, idle\nreview: not started\n\nRecommendation: 4 panels - two write code (research included) in parallel and one reviews. Uses more quota. 3 panels are lighter: one writes and one reviews. With 2 panels one writes and the review happens here.\n"
		if out != want {
			t.Fatalf("out:\n%s\nwant:\n%s", out, want)
		}
		if strings.Contains(out, "Footprint:") {
			t.Fatalf("the footprint line must stay out of a footprint-free explain:\n%s", out)
		}
	})

	t.Run("a queued copy counts and the queue stays (DJA-323)", func(t *testing.T) {
		_ = os.RemoveAll(sd)
		tm6Write(t, filepath.Join(sd, "agents.tsv"), agentsTsvBuild)
		queue, err := core.QueueCopy(env, env.Get("HERDR_WORKSPACE_ID"), sd, core.CopyRow{Path: copyDir, Owner: "build", Pane: "p1", Created: "2026-01-01T00:00:00Z", Source: "s", Origin: "mutation-copy"})
		if err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(queue)
		if err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
		if code != 0 {
			t.Fatalf("explain exit=%d stderr=%q", code, stderr)
		}
		if !strings.Contains(out, "(copies "+core.HumanSize(copyBytes)+" in 1, ") {
			t.Fatalf("the queued copy is not counted:\n%s", out)
		}
		after, err := os.ReadFile(queue)
		if err != nil || string(after) != string(before) {
			t.Fatalf("explain changed the queue: %q -> %q (%v)", before, after, err)
		}
		if _, err := os.Stat(filepath.Join(sd, "copies.tsv")); !os.IsNotExist(err) {
			t.Fatalf("explain wrote the registry: %v", err)
		}
	})
}
