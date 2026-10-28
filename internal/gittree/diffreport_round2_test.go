package gittree_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/cli"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func diffRound2Run(t *testing.T, f *diffFixture, stderr io.Writer, args ...string) (int, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out bytes.Buffer
	platform.Stdout, platform.Stderr = &out, stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	return cli.Run(args, f.env), out.String()
}

func TestDiffCleanTreeAddsNothing(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Completed `src/a.go`.\n", "implementer")
	before := diffGitFiles(t, filepath.Join(f.tree, ".git"))
	value, out, stderr := f.wait(t)
	if len(value) != 3 || stderr != "" {
		t.Fatalf("clean tree changed output: %v stderr=%q", value, stderr)
	}
	artifact := strings.TrimSuffix(f.report, ".md") + ".diff-stat.txt"
	if _, err := os.Lstat(artifact); !os.IsNotExist(err) {
		t.Fatalf("clean artifact exists: %v", err)
	}
	if _, err := os.Stat(f.sidecar + ".diff-checked"); err != nil {
		t.Fatal(err)
	}
	_, again, stderr := f.wait(t)
	if again != out || stderr != "" {
		t.Fatalf("repeat differs before=%s after=%s stderr=%q", out, again, stderr)
	}
	if after := diffGitFiles(t, filepath.Join(f.tree, ".git")); !reflect.DeepEqual(before, after) {
		t.Fatal("clean audit changed Git metadata")
	}
	t.Logf("clean first/repeat exit=0 byte-identical JSON=%s artifact absent; no cited warning", strings.TrimSpace(out))
}

func TestDiffCollectOnceThenWaitSilent(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Completed `src/a.go` and `src/b.go`.\n", "implementer")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
	diffWrite(t, filepath.Join(f.tree, "outside.txt"), "new\n")
	var stderr bytes.Buffer
	code, out := diffRound2Run(t, f, &stderr, "collect", "worker")
	if code != 0 || !strings.Contains(out, "Completed") || !strings.Contains(stderr.String(), "changed files outside its Owned files: outside.txt") || !strings.Contains(stderr.String(), "cites files the tree shows unchanged: src/a.go") {
		t.Fatalf("collect code=%d out=%s stderr=%s", code, out, stderr.String())
	}
	artifact := strings.TrimSuffix(f.report, ".md") + ".diff-stat.txt"
	if _, err := os.Stat(artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.sidecar + ".diff-checked"); err != nil {
		t.Fatal(err)
	}
	firstErr := stderr.String()
	stderr.Reset()
	code, again := diffRound2Run(t, f, &stderr, "collect", "worker")
	if code != 0 || out != again || stderr.Len() != 0 {
		t.Fatalf("repeat collect code=%d out=%s stderr=%s", code, again, stderr.String())
	}
	value, _, waitErr := f.wait(t)
	if value["diff_stat"] != nil || value["outside_owned"] != nil || value["cited_unchanged"] != nil || waitErr != "" {
		t.Fatalf("wait repeated audit: %v stderr=%s", value, waitErr)
	}
	t.Logf("collect exit=0 artifact=%s first warnings=%q; repeat collect and wait exit=0 silent", artifact, firstErr)
}

func TestDiffCollectReadOnlySkipsAudit(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Completed `src/a.go`.\n", "implementer")
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
	f.env["SOHO_NOWRITE"] = "1"
	var stderr bytes.Buffer
	code, out := diffRound2Run(t, f, &stderr, "collect", "worker")
	if code != 0 || !strings.Contains(out, "Completed") || stderr.Len() != 0 {
		t.Fatalf("readonly code=%d out=%s stderr=%s", code, out, stderr.String())
	}
	if _, err := os.Lstat(f.sidecar + ".diff-checked"); !os.IsNotExist(err) {
		t.Fatalf("readonly marker: %v", err)
	}
}

type diffDuplicateWriter struct {
	body    bytes.Buffer
	trigger func()
	fired   bool
}

func (w *diffDuplicateWriter) Write(b []byte) (int, error) {
	n, e := w.body.Write(b)
	if !w.fired && strings.Contains(w.body.String(), "already has this brief") {
		w.fired = true
		w.trigger()
	}
	return n, e
}

func TestDiffFreshAndDuplicateDispatchForward(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		name := "fresh"
		if duplicate {
			name = "duplicate"
		}
		t.Run(name, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Previous.\n", "implementer")
			diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "changed\n")
			diffWrite(t, filepath.Join(f.tree, "outside.txt"), "new\n")
			f.env["SOHO_PROMPT_CHECK_SECONDS"], f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0", "0"
			input := filepath.Join(filepath.Dir(f.state), "input.md")
			diffWrite(t, input, "# Goal\nDo it.\n# Owned files\n- src/*.go\n")
			bin := f.env["SOHO_FAKECLI_CONFIG"]
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
				{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
				{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true},
				{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
				{Argv: []string{"pane", "report-metadata"}, ArgvPrefix: true},
			}
			config, err := json.Marshal(struct {
				Log   string         `json:"log"`
				Rules []fakecli.Rule `json:"rules"`
			}{filepath.Join(bin, "herdr.calls.jsonl"), rules})
			if err != nil {
				t.Fatal(err)
			}
			diffWrite(t, filepath.Join(bin, "herdr.json"), string(config))
			args := []string{"dispatch", "worker", input, "--role", "implementer", "--timeout", "2s"}
			var stderr bytes.Buffer
			var output string
			var code int
			if duplicate {
				code, output = diffRound2Run(t, f, &stderr, append(append([]string{}, args...), "--no-wait")...)
				if code != 0 {
					t.Fatalf("initial dispatch code=%d out=%s err=%s", code, output, stderr.String())
				}
				var sent map[string]any
				if err := json.Unmarshal([]byte(output), &sent); err != nil {
					t.Fatal(err)
				}
				report, _ := sent["report"].(string)
				if _, err := os.Stat(report); !os.IsNotExist(err) {
					t.Fatalf("report already exists before duplicate: %v", err)
				}
				var writeErr error
				trigger := &diffDuplicateWriter{trigger: func() {
					writeErr = diffAfterCurrentDispatch(f, report, true)
				}}
				code, output = diffRound2Run(t, f, trigger, args...)
				if !trigger.fired || writeErr != nil {
					t.Fatalf("duplicate branch fired=%t report write=%v out=%s stderr=%s", trigger.fired, writeErr, output, trigger.body.String())
				}
				stderr.Reset()
				stderr.WriteString(trigger.body.String())
			} else {
				stop, done := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(done)
					for {
						select {
						case <-stop:
							return
						case <-time.After(time.Millisecond):
							b, e := os.ReadFile(filepath.Join(f.state, "last-report-worker"))
							if e == nil {
								report := strings.TrimSpace(string(b))
								if report != "" && report != f.report {
									if err := diffAfterCurrentDispatch(f, report, true); err != nil {
										return
									}
									return
								}
							}
						}
					}
				}()
				t.Cleanup(func() { close(stop); <-done })
				code, output = diffRound2Run(t, f, &stderr, args...)
			}
			var value map[string]any
			if err := json.Unmarshal([]byte(output), &value); err != nil {
				t.Fatalf("out=%s err=%v", output, err)
			}
			report, _ := value["report"].(string)
			wantStat := strings.TrimSuffix(report, ".md") + ".diff-stat.txt"
			if code != 0 || value["wait_status"] != "done" || value["diff_stat"] != wantStat || !reflect.DeepEqual(value["outside_owned"], []any{"outside.txt"}) || !reflect.DeepEqual(value["cited_unchanged"], []any{"src/a.go"}) {
				t.Fatalf("code=%d out=%s stderr=%s", code, output, stderr.String())
			}
			_, hasDuplicate := value["duplicate_of"]
			if hasDuplicate != duplicate {
				t.Fatalf("wrong branch: %s", output)
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(bin, "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			prompts := 0
			for _, call := range calls {
				if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
					prompts++
				}
			}
			if prompts != 1 {
				t.Fatalf("branch=%s prompts=%d want 1", name, prompts)
			}
			t.Logf("branch=%s exit=%d prompts=%d forwarded diff_stat=%v outside_owned=%v cited_unchanged=%v", name, code, prompts, value["diff_stat"], value["outside_owned"], value["cited_unchanged"])
		})
	}
}
