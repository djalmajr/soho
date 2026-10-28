package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// F9 retirement negative contract (cli): the frozen dispatch.test.mjs and
// test-status.sh cases that a wrong invocation or an already-ready report
// must handle without side effects. The exact current contract messages are
// asserted whole, and the no-effect side is asserted on the Herdr call log,
// the state marker and the output.
func TestRetirementNegativeContracts(t *testing.T) {
	t.Run("run: a missing value flag and an unknown option exit 2 with the exact usage message and start nothing", func(t *testing.T) { // JS: "run --kind -> rc 2 exact stderr, empty stdout, no agent start" (scripts/test/dispatch.test.mjs:2025,2288)
		for _, flag := range []string{"--kind", "--name", "--timeout"} {
			h := newTM3bHarness(t, "brief body")
			prepareRunHarness(t, &h)
			code, out, stderr := runForTest(t, h, flag)
			want := "soho: run: " + flag + " expects a value\n"
			if code != 2 || out != "" || stderr != want {
				t.Fatalf("flag=%s code=%d stdout=%q stderr=%q; want code=2, empty stdout, %q", flag, code, out, stderr, want)
			}
			assertRunStartAndPromptNever(t, h)
		}
		h := newTM3bHarness(t, "brief body")
		prepareRunHarness(t, &h)
		code, out, stderr := runForTest(t, h, "--amend")
		if code != 2 || out != "" || stderr != "soho: run: unknown option --amend\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		assertRunStartAndPromptNever(t, h)
	})
	t.Run("ownedPaths refuses to treat exclusion headings as ownership", func(t *testing.T) { // JS: "lint and ownership: Portuguese section prefixes reject word continuations" (scripts/test/dispatch.test.mjs:446-459)
		cases := []struct {
			body string
			want []string
		}{
			{"## Arquivos proibidos\n\n- src/secret.ts\n\n## Arquivos — donos\n\n- src/mine.ts\n", []string{"src/mine.ts"}},
			{"## Escopo fora\n\n- src/secret.ts\n\n## Arquivos:\n\n- src/mine.ts\n", []string{"src/mine.ts"}},
			{"## Arquivos\n\n- src/a.ts\n", []string{"src/a.ts"}}, // control: a real ownership heading is still owned
		}
		for _, tc := range cases {
			if got := ownedPaths(tc.body, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ownedPaths(%q) = %v; want %v", tc.body, got, tc.want)
			}
		}
	})
	t.Run("dispatch: a completed partial report settles done with the partial flag and exactly one warning", func(t *testing.T) { // JS: "dispatch wait: completed report marks 2 partial items -> rc 0, partial flag, exact single warning" (scripts/test/dispatch.test.mjs:2358-2441)
		cases := []struct {
			name        string
			body        string
			wantPartial bool
		}{
			{"partial report", "# Report\n\n- [partial] item A: waiting on X\n- [partial] item B: waiting on Y\n", true},
			{"clean report", "# Report\n\ndone.\n", false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := newTM3bHarness(t, "brief body")
				// The dispatch flow probes the existing roster agent before
				// prompting: the fake must report it idle (a working agent
				// is refused, not prompted).
				rules := []fakecli.Rule{
					{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
					{Argv: []string{"agent", "get", "build"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`},
					{Argv: []string{"agent", "read", "build"}, ArgvPrefix: true, Stdout: "ready\n"},
					{Argv: []string{"agent", "prompt", "build"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
					{AnyArgs: true, Stdout: `{"result":{"ok":true}}`},
				}
				logPath := filepath.Join(h.root, "bin", "herdr.calls.jsonl")
				config, err := json.Marshal(struct {
					Log   string         `json:"log"`
					Rules []fakecli.Rule `json:"rules"`
				}{Log: logPath, Rules: rules})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(h.root, "bin", "herdr.json"), config, 0o600); err != nil {
					t.Fatal(err)
				}
				h.env["HERDR_ENV"] = "1"
				h.env["SOHO_WAIT_POLL_MS"] = "1"
				paths := make(chan string, 1)
				installDispatchReportWriter(t, logPath, tc.body, paths)
				oldOut, oldErr := platform.Stdout, platform.Stderr
				var out, stderr strings.Builder
				platform.Stdout, platform.Stderr = &out, &stderr
				code := func() (rc int) {
					defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
					defer func() {
						if value := recover(); value != nil {
							t.Fatalf("dispatch panicked: %v", value)
						}
					}()
					return Run([]string{"dispatch", "build", h.brief, "--timeout", "5000"}, h.env)
				}()
				outStr, errStr := out.String(), stderr.String()
				var report string
				select {
				case report = <-paths:
				case <-time.After(10 * time.Second):
					calls, _ := fakecli.ReadCalls(logPath)
					t.Fatalf("the report was never written (code=%d out=%q stderr=%q); calls=%#v", code, outStr, errStr, calls)
				}
				if code != 0 {
					t.Fatalf("code=%d stderr=%q; want code=0", code, errStr)
				}
				raw := strings.TrimSpace(outStr)
				i := strings.Index(raw, `{"wait_status":`)
				if i < 0 {
					t.Fatalf("no wait JSON in stdout: %q stderr=%q", outStr, errStr)
				}
				line := raw[i:]
				if j := strings.IndexByte(line, '\n'); j >= 0 {
					line = line[:j]
				}
				var got map[string]any
				if err := json.Unmarshal([]byte(line), &got); err != nil {
					t.Fatalf("wait JSON: %v: %s", err, line)
				}
				if got["wait_status"] != "done" || got["agent"] != "build" || got["report"] != report || got["report_exists"] != true {
					t.Fatalf("wait JSON = %s; want wait_status=done agent=build report=%s report_exists=true", line, report)
				}
				if tc.wantPartial {
					if got["partial"] != float64(2) || got["auto_approved"] != float64(0) {
						t.Fatalf("partial=%v auto_approved=%v; want 2 and 0", got["partial"], got["auto_approved"])
					}
					// The frozen relative key order: the partial flag sits between
					// report_exists and auto_approved.
					pos := -1
					for _, key := range []string{"wait_status", "agent", "role", "kind", "composed_prompt", "report", "task_report", "report_exists", "partial", "auto_approved"} {
						p := strings.Index(line, `"`+key+`":`)
						if p < 0 || p <= pos {
							t.Fatalf("key %q out of order or missing in %s", key, line)
						}
						pos = p
					}
					warning := "soho: warning: report of 'build' marks 2 item(s) partial: a partial item is not a pass; read them before commit, push or release\n"
					if strings.Count(errStr, warning) != 1 {
						t.Fatalf("stderr carries %d exact partial warnings; want exactly 1: %q", strings.Count(errStr, warning), errStr)
					}
					if strings.Count(errStr, "item(s) partial") != 1 {
						t.Fatalf("stderr carries more than one partial notice: %q", errStr)
					}
				} else {
					if strings.Contains(line, `"partial":`) || strings.Contains(errStr, "item(s) partial") {
						t.Fatalf("clean report leaked the partial flag: %s stderr=%q", line, errStr)
					}
				}
			})
		}
	})
	t.Run("status: an authoritative ready report settles done when the agent get is denied", func(t *testing.T) {
		rows := "worker\tw0test:p0a\tcodex\timplementer\topenai\t0\t/d\tnow\tgpt-5\ttask\timplementer\tbuild\n"
		// The denied query makes the pane unavailable; the ready report
		// keeps its authority without adding a warning.
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: "PermissionDenied"}})
		reportPath := filepath.Join(f.state, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reportPath, []byte("report body"), 0o600); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(f.state, "last-report-worker")
		if err := os.WriteFile(marker, []byte(reportPath+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.run(t, "worker")
		if code != 0 || !strings.Contains(out, "worker\tdone\t"+reportPath+"\t") || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q; want code=0, done line with the report, no get failure", code, out, stderr)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, []string{"agent", "get", "worker"}) {
			t.Fatalf("want exactly one denied pane query, got %#v", calls)
		}
		raw, err := os.ReadFile(marker)
		if err != nil || string(raw) != reportPath+"\n" {
			t.Fatalf("the ready-report state rewrote the marker: %q err=%v", raw, err)
		}
	})
}

func assertRunStartAndPromptNever(t *testing.T, h tm3bHarness) {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(h.root, "bin", "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, call := range calls {
		if len(call.Argv) > 1 && (call.Argv[1] == "start" || call.Argv[1] == "prompt") {
			t.Fatalf("the usage error started or prompted an agent: %#v", call)
		}
	}
}

// installDispatchReportWriter is the native form of the frozen JS fixture:
// the fake herdr writes the report on `agent prompt` (like a worker). The
// generated report path is parsed from the dispatched prompt so the fixture
// writes exactly the report selected by dispatch, including suffixes.
func installDispatchReportWriter(t *testing.T, log, body string, paths chan<- string) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			calls, _ := fakecli.ReadCalls(log)
			for _, call := range calls {
				if len(call.Argv) < 4 || call.Argv[0] != "agent" || call.Argv[1] != "prompt" {
					continue
				}
				text := call.Argv[len(call.Argv)-1]
				const marker = "write your report to "
				_, rest, ok := strings.Cut(text, marker)
				if !ok {
					continue
				}
				path, _, ok := strings.Cut(rest, " and reply with exactly that path")
				if !ok {
					continue
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err == nil {
					paths <- path
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("the dispatched prompt never reached the fake herdr")
	}()
}
