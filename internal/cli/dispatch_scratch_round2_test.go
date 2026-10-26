package cli

import (
	"fmt"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchScratchDuplicateVolatile(t *testing.T) {
	for _, kind := range []string{"pi", "codex"} {
		for _, waiting := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wait=%v", kind, waiting), func(t *testing.T) {
				f, first := durableScratchDispatch(t, kind, "full")
				report := durableField(t, first, "report")
				composed := durableField(t, first, "composed_prompt")
				if waiting {
					saved := dispatchWaitFor
					t.Cleanup(func() { dispatchWaitFor = saved })
					dispatchWaitFor = func([]string, string, *core.Config, platform.Env, float64, bool, string) int {
						fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O("agent", "worker", "status", "done", "report", report)))
						return 0
					}
				}
				args := []string{"worker", f.brief}
				if !waiting {
					args = append(args, "--no-wait")
				}
				code, out, msg := f.run(t, args...)
				v, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
				if err != nil {
					t.Fatalf("exit=%d output=%q stderr=%q err=%v", code, out, msg, err)
				}
				obj := v.(*jsonjs.Object)
				dup, _ := obj.Get("duplicate_of")
				volatile, present := obj.Get("report_volatile")
				status, _ := obj.Get("wait_status")
				wantStatus := "submitted"
				if waiting {
					wantStatus = "done"
				}
				if code != 0 || dup != composed || status != wantStatus || present != (kind == "codex") || (present && volatile != true) {
					t.Fatalf("duplicate code=%d output=%s stderr=%s", code, out, msg)
				}
				if got := countArgvPrefix(readArrivalCalls(t, f), []string{"agent", "prompt"}); got != 1 {
					t.Fatalf("duplicate sent %d prompts", got)
				}
				sd := filepath.Join(f.state, "ws")
				found, _, ok := memberTaskPaths(sd, report)
				plain, plainOK := memberComposedPath(sd, report)
				if !ok || !plainOK || found != composed || plain != composed {
					t.Fatalf("pending member paths composed=%s got=%s/%s", composed, found, plain)
				}
				if kind == "pi" {
					// A normal-state report can already exist without ever becoming a
					// composed brief: state/briefs wins over state/reports.
					if err := os.WriteFile(report, []byte("completed\n"), 0600); err != nil {
						t.Fatal(err)
					}
					found, _, ok = memberTaskPaths(sd, report)
					plain, plainOK = memberComposedPath(sd, report)
					if !ok || !plainOK || found != composed || plain != composed {
						t.Fatalf("report confused with composed brief: %s/%s", found, plain)
					}
				}
			})
		}
	}
}

func TestDispatchCursorWindowsNoScratchDurablePaths(t *testing.T) {
	// Twin of the intentional Cursor startup fixture; it remains Cursor.
	setDispatchPlatform(t, "win32")
	f, obj := durableScratchDispatch(t, "cursor", "full")
	if filepath.Dir(durableField(t, obj, "composed_prompt")) != filepath.Join(f.state, "ws", "briefs") || filepath.Dir(durableField(t, obj, "report")) != filepath.Join(f.state, "ws", "reports") {
		t.Fatalf("paths=%s", jsonjs.Stringify(obj))
	}
	if _, ok := obj.Get("report_volatile"); ok {
		t.Fatalf("cursor report is volatile: %s", jsonjs.Stringify(obj))
	}
	if _, err := os.Stat(filepath.Join(f.env["TMPDIR"], "soho", "ws", "reports")); !os.IsNotExist(err) {
		t.Fatalf("cursor created scratch: %v", err)
	}
}

func TestDispatchNonCodexDoesNotNarrowPrivateScratch(t *testing.T) {
	skipScratchOwnerOffUnix(t)
	f := newScratchDispatchFixture(t)
	path := filepath.Join(f.state, "ws", "agents.tsv")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(strings.Replace(string(data), "\tcodex\t", "\tclaude\t", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	level := f.levels()[0]
	if err = os.MkdirAll(level, 0777); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(level, 0777); err != nil {
		t.Fatal(err)
	}
	code, composed, report, msg := f.dispatch(t)
	if code != 0 || filepath.Dir(composed) != filepath.Join(f.state, "ws", "briefs") || filepath.Dir(report) != filepath.Join(f.state, "ws", "reports") || scratchModeOf(t, level) != 0777 {
		t.Fatalf("non-codex code=%d brief=%s report=%s stderr=%s", code, composed, report, msg)
	}
}
