//go:build unix

package wait

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// queuedSourceFixture exercises the public status consumer with the same
// terminal reads as ordinary queued handling. Input paths authorize no reads.
func queuedSourceFixture(t *testing.T, prompt func(sd, tmp string) string) sourceViewFixture {
	t.Helper()
	f := newStatusViewFixtureFull(t, func(string) string {
		return "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	}, func(cwd string) []fakecli.Rule {
		base := filepath.Dir(cwd)
		input := "Read the file " + prompt(filepath.Join(base, "state", "ws"), filepath.Join(base, "tmp"))
		return []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":7}}}`},
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker","pane_id":"p1"}]}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "Ready"},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Ready"},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: input},
		}
	})
	src, err := core.OpenSessionSource(f.state, "ws", f.env["TMPDIR"])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	reportFilesWrite(t, filepath.Join(f.sd, "wait", "worker.queued"), "1 7\n")
	return sourceViewFixture{statusViewFixture: f, src: src}
}

func queuedSourceView(t *testing.T, f sourceViewFixture, sd string) AgentStatusView {
	t.Helper()
	envBefore := copyEnv(f.env)
	view, out, errOut := silentInspect(t, AgentStatusRequest{StateDir: sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd, Config: f.ctx, Env: f.env, Services: backend.New(f.env), Source: f.src})
	if out != "" || errOut != "" {
		t.Fatalf("stdout=%q stderr=%q", out, errOut)
	}
	assertEnvUnmutated(t, envBefore, f.env)
	if b, err := os.ReadFile(f.friction); err != nil || string(b) != "configured line\n" {
		t.Fatalf("friction changed: %q %v", b, err)
	}
	assertNoSessionDiscovery(t, f.callLog(t))
	for _, call := range f.callLog(t) {
		if !strings.HasPrefix(call, "agent\x00get\x00") && !strings.HasPrefix(call, "agent\x00list") && !strings.HasPrefix(call, "agent\x00read\x00") {
			t.Fatalf("unexpected effect: %q", call)
		}
	}
	return view
}

func queuedFIFOView(t *testing.T, f sourceViewFixture, sd, path string) AgentStatusView {
	t.Helper()
	got := inspectFIFOPromptly(t, path, func() AgentStatusView {
		return InspectAgentStatus(AgentStatusRequest{StateDir: sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd, Config: f.ctx, Env: f.env, Services: backend.New(f.env), Source: f.src})
	})
	assertNoSessionDiscovery(t, f.callLog(t))
	return got
}

func TestStatusViewSourceQueuedForeignStateIgnored(t *testing.T) {
	decoy := t.TempDir()
	prompt := filepath.Join(decoy, "briefs", "foreign.md")
	f := queuedSourceFixture(t, func(string, string) string { return prompt })
	before := queuedSourceView(t, f, decoy)
	if before.State != "no-report-yet" {
		t.Fatalf("control=%+v", before)
	}
	reportFilesWrite(t, filepath.Join(decoy, "last-report-worker"), filepath.Join(decoy, "reports", "foreign.md")+"\n")
	reportFilesWrite(t, filepath.Join(decoy, "briefs", "foreign.md"), "outside body must not be read\n")
	snapshot := f.fixtureBefore(t)
	after := queuedSourceView(t, f, decoy)
	if after != before {
		t.Fatalf("foreign metadata changed view: before=%+v after=%+v", before, after)
	}
	assertTreeUnchanged(t, "source queued", snapshot, f.fixtureBefore(t))
	// A raw fallback read would block here even though the retained source
	// still has no pointer. The outside FIFO must never be opened.
	foreignPointer := filepath.Join(decoy, "last-report-worker")
	if err := os.Remove(foreignPointer); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(foreignPointer, 0600); err != nil {
		t.Fatal(err)
	}
	if got := queuedFIFOView(t, f, decoy, foreignPointer); got != before {
		t.Fatalf("outside FIFO affected source view: before=%+v after=%+v", before, got)
	}
}

func TestStatusViewSourceQueuedControls(t *testing.T) {
	for _, kind := range []string{"embedded", "state-brief", "scratch-brief", "report-directory-brief", "missing-candidates-default", "missing-pointer", "large-candidate-metadata-only"} {
		t.Run(kind, func(t *testing.T) {
			embedded := filepath.Join(t.TempDir(), "embedded with spaces.md")
			prompt := func(sd, tmp string) string {
				switch kind {
				case "embedded":
					return embedded
				case "scratch-brief":
					return filepath.Join(tmp, "soho", "ws", "reports", "task.brief.md")
				case "report-directory-brief":
					return filepath.Join(sd, "reports", "task.brief.md")
				default:
					return filepath.Join(sd, "briefs", "task.md")
				}
			}
			f := queuedSourceFixture(t, prompt)
			report := filepath.Join(f.sd, "reports", "task.md")
			if kind == "scratch-brief" {
				report = filepath.Join(f.tmp, "soho", "ws", "reports", "task.md")
			}
			if kind != "missing-pointer" && kind != "embedded" {
				reportFilesWrite(t, filepath.Join(f.sd, "last-report-worker"), report+"\n")
			}
			switch kind {
			case "embedded":
				reportFilesWrite(t, filepath.Join(f.sd, "wait", "worker.queued"), "1 7 "+embedded+"\n")
			case "state-brief", "scratch-brief", "report-directory-brief":
				reportFilesWrite(t, prompt(f.sd, f.tmp), "brief body stays unread\n")
			case "large-candidate-metadata-only":
				path := prompt(f.sd, f.tmp)
				reportFilesWrite(t, path, "")
				if err := os.Truncate(path, 8<<20); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
			}
			// Readable regular controls use a full no-write snapshot. The unreadable
			// large-body control is checked by metadata without opening it in QA.
			var before map[string]snapEntry
			if kind != "large-candidate-metadata-only" {
				before = f.fixtureBefore(t)
			}
			got := queuedSourceView(t, f, f.sd)
			expected := "not-received"
			if kind == "missing-pointer" {
				expected = "no-report-yet"
			}
			if got.State != expected || got.ReportReady || got.TerminalState != "idle" {
				t.Fatalf("kind=%s view=%+v want=%s/idle/no report", kind, got, expected)
			}
			if plain := f.inspect(t, "ws", "worker"); plain != got {
				t.Fatalf("ordinary/source compatibility: ordinary=%+v source=%+v", plain, got)
			}
			if kind != "large-candidate-metadata-only" {
				assertTreeUnchanged(t, "queued controls", before, f.fixtureBefore(t))
			} else {
				info, err := os.Lstat(prompt(f.sd, f.tmp))
				if err != nil || info.Size() != 8<<20 || info.Mode().Perm() != 0 {
					t.Fatalf("body metadata changed: %v %v", info, err)
				}
			}
		})
	}
}

func TestStatusViewSourceQueuedRetainedFallback(t *testing.T) {
	f := queuedSourceFixture(t, func(sd, tmp string) string { return filepath.Join(sd, "briefs", "task.md") })
	reportFilesWrite(t, filepath.Join(f.sd, "last-report-worker"), filepath.Join(f.sd, "reports", "task.md")+"\n")
	reportFilesWrite(t, filepath.Join(f.sd, "briefs", "task.md"), "retained body\n")
	before := queuedSourceView(t, f, f.sd)
	if before.State != "not-received" {
		t.Fatalf("control=%+v", before)
	}
	if err := os.Rename(f.sd, f.sd+"-retained"); err != nil {
		t.Fatal(err)
	}
	reportFilesWrite(t, filepath.Join(f.sd, "last-report-worker"), filepath.Join(f.sd, "reports", "decoy.md")+"\n")
	reportFilesWrite(t, filepath.Join(f.sd, "briefs", "decoy.md"), "decoy body\n")
	snapshot := f.fixtureBefore(t)
	got := queuedSourceView(t, f, f.sd)
	if got != before {
		t.Fatalf("replaced raw directory changed retained view: before=%+v after=%+v", before, got)
	}
	assertTreeUnchanged(t, "retained fallback", snapshot, f.fixtureBefore(t))
}

func TestStatusViewSourceQueuedRefusedEvidence(t *testing.T) {
	for _, location := range []string{"pointer", "state-candidate", "scratch-candidate", "report-directory-candidate"} {
		for _, kind := range []string{"link", "fifo", "directory", "oversize-pointer"} {
			if kind == "oversize-pointer" && location != "pointer" {
				continue
			}
			t.Run(location+"/"+kind, func(t *testing.T) {
				scratch := location == "scratch-candidate"
				f := queuedSourceFixture(t, func(sd, tmp string) string {
					if scratch {
						return filepath.Join(tmp, "soho", "ws", "reports", "task.brief.md")
					}
					if location == "report-directory-candidate" {
						return filepath.Join(sd, "reports", "task.brief.md")
					}
					return filepath.Join(sd, "briefs", "task.md")
				})
				report := filepath.Join(f.sd, "reports", "task.md")
				if scratch {
					report = filepath.Join(f.tmp, "soho", "ws", "reports", "task.md")
				}
				pointer := filepath.Join(f.sd, "last-report-worker")
				reportFilesWrite(t, pointer, report+"\n")
				target := pointer
				switch location {
				case "state-candidate":
					target = filepath.Join(f.sd, "briefs", "task.md")
				case "scratch-candidate", "report-directory-candidate":
					target = filepath.Join(filepath.Dir(report), "task.brief.md")
				}
				reportFilesWrite(t, target, "plain positive control\n")
				if location == "pointer" {
					reportFilesWrite(t, pointer, report+"\n")
				}
				control := queuedSourceView(t, f, f.sd)
				if control.State != "not-received" {
					t.Fatalf("control=%+v", control)
				}
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "outside")
				reportFilesWrite(t, outside, "outside canary body\n")
				switch kind {
				case "link":
					if err := os.Symlink(outside, target); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := syscall.Mkfifo(target, 0600); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				case "oversize-pointer":
					reportFilesWrite(t, target, strings.Repeat("x", (64<<10)+1))
				}
				before, err := os.Lstat(target)
				if err != nil {
					t.Fatal(err)
				}
				var got AgentStatusView
				if kind == "fifo" {
					got = queuedFIFOView(t, f, f.sd, target)
				} else {
					got = queuedSourceView(t, f, f.sd)
				}
				if got.State != "unknown" || got.ReportReady {
					t.Errorf("refused required evidence view=%+v", got)
				}
				after, err := os.Lstat(target)
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
					t.Fatalf("candidate changed: before=%v after=%v err=%v", before, after, err)
				}
				if b, err := os.ReadFile(outside); err != nil || string(b) != "outside canary body\n" {
					t.Fatalf("outside changed: %q %v", b, err)
				}
			})
		}
	}
}
