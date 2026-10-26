package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchParityPortedCases(t *testing.T) {
	t.Run(`JS: "parity dispatch: the pane title (test-quota.sh)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"role":"implementer"`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		metadataTitle := false
		for _, call := range calls {
			args := strings.Join(call.Argv, " ")
			if strings.Contains(args, "pane report-metadata") && strings.Contains(args, "implementer:") {
				metadataTitle = true
			}
		}
		if !metadataTitle {
			t.Fatalf("pane title was not sent with role and task title: %#v", calls)
		}
	})
	t.Run(`JS: "parity dispatch: review dispatch accepts a matching family and retains strict lint (test-multi-role.sh)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0",
			fakecli.Rule{Argv: []string{"agent", "get", "rev"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			fakecli.Rule{Argv: []string{"agent", "prompt", "rev"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		)
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		role := filepath.Join(f.root, "roles", "reviewer.md")
		if err := os.WriteFile(role, []byte("---\nname: Reviewer\nmode: review\n---\nReview.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" +
			"rev\tw0test:p0b\tcodex\treviewer\topenai\t0\t\tnow\tgpt-5\ttask\treviewer\t\n" +
			"author\tw0test:p0a\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		writeRoleFixture(t, f.brief, strings.Replace(mustRead(t, f.brief), "# Owned files\n\nnone\n", "# Owned files\n\n", 1))
		code, out, errText := f.run(t, "rev", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) || strings.Contains(errText, "family") {
			t.Fatalf("review dispatch code=%d out=%q stderr=%q", code, out, errText)
		}
		// The reviewer is submitted even with a matching implementer in the roster.

		badBrief := filepath.Join(f.root, "bad.md")
		if err := os.WriteFile(badBrief, []byte("# Goal\nDo it.\n# Owned files\n\n# Forbidden\nNo commit or push.\n# Report\ndone.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_BRIEF_LINT"] = "strict"
		code, out, errText = f.run(t, "rev", badBrief, "--resend", "--no-wait")
		if code != 2 || out != "" || !strings.Contains(errText, "is missing sections: [Expected result]") {
			t.Fatalf("strict lint code=%d out=%q stderr=%q", code, out, errText)
		}
	})
	t.Run(`JS: "parity dispatch: the role comes from column 4 (test-multi-role.sh)"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"role":"implementer"`) {
			t.Fatalf("code=%d out=%s", code, out)
		}
		composed, _ := dispatchOutputField(t, out, "composed_prompt")
		prompt := mustRead(t, composed)
		if !strings.Contains(prompt, "You are running as the `implementer` role") {
			t.Fatalf("composed prompt did not use roster column 4: %s", prompt)
		}
	})
	t.Run(`JS: "dispatch: an untitled brief is titled by its file name"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		if err := os.WriteFile(f.brief, []byte("# Goal\n\nRun it.\n# Expected result\n\nDone.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\nDone.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out)
		}
		if got := mustRead(t, filepath.Join(f.state, "ws", "task-worker")); got != "implementer: brief\n" {
			t.Fatalf("task title=%q", got)
		}
	})
	t.Run(`JS: "dispatch: a prompt failure prints the error JSON and exits 4"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0",
			fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Code: 7, Stderr: "provider refused"},
		)
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 4 || !strings.Contains(out, `"error"`) || !strings.Contains(out, "provider refused") {
			t.Fatalf("code=%d out=%s", code, out)
		}
		composed, ok := dispatchOutputField(t, out, "composed_prompt")
		if !ok {
			t.Fatalf("error JSON omitted composed prompt: %s", out)
		}
		value := mustRead(t, dispatch.DispatchSidecar(composed))
		if !strings.Contains(value, `"submission":"failed"`) {
			t.Fatalf("failed prompt sidecar=%s", value)
		}
	})
	t.Run(`JS: "dispatch: the attempt sidecar records the roster metadata and the accepted submission"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out)
		}
		composed, ok := dispatchOutputField(t, out, "composed_prompt")
		if !ok {
			t.Fatalf("success JSON omitted composed prompt: %s", out)
		}
		value := mustRead(t, dispatch.DispatchSidecar(composed))
		for _, field := range []string{`"submission":"accepted"`, `"kind":"codex"`, `"model":"gpt-5"`, `"effort":"high"`} {
			if !strings.Contains(value, field) {
				t.Errorf("sidecar missing %s: %s", field, value)
			}
		}
	})
	t.Run(`JS: "dispatch: a brief naming a different report path — the composed prompt keeps the generated path authoritative"`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		brief := "# Goal\n\nRun it.\n# Expected result\n\nDone.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\nWrite your report as Markdown to `/tmp/user-selected.md`.\n"
		if err := os.WriteFile(f.brief, []byte(brief), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%s", code, out)
		}
		composed, ok := dispatchOutputField(t, out, "composed_prompt")
		if !ok {
			t.Fatalf("success JSON omitted composed prompt: %s", out)
		}
		prompt := mustRead(t, composed)
		generated, _ := dispatchOutputField(t, out, "report")
		if !strings.Contains(prompt, "Write your report as Markdown to `"+generated+"`") {
			t.Fatalf("report path not authoritative: %s", prompt)
		}
	})
}

func dispatchOutputField(t *testing.T, out, key string) (string, bool) {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &fields); err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	value, ok := fields[key].(string)
	return value, ok
}

func TestDispatchInvalidNativeBinaryPrecedesCompactAndState(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_BIN"] = filepath.Join(t.TempDir(), "missing-native-binary")
	before := snapshotTree(t, f.state)
	code, out, stderr := f.run(t, "worker", f.brief, "--compact", "--no-wait")
	if code != 2 || out != "" || !strings.Contains(stderr, "SOHO_BIN") {
		t.Fatalf("invalid binary: code=%d out=%q stderr=%q", code, out, stderr)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("invalid binary interacted with the worker before refusal: %#v", calls)
	}
	after := snapshotTree(t, f.state)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid binary mutated task state: before=%v after=%v", before, after)
	}
}

func TestRunInvalidNativeBinaryPrecedesSpawn(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_BIN"] = filepath.Join(t.TempDir(), "missing-native-binary")
	before := snapshotTree(t, f.state)
	code, out, stderr := runIn(t, []string{"run", "implementer", f.brief, "--no-wait"}, f.env, f.root)
	if code != 2 || out != "" || !strings.Contains(stderr, "SOHO_BIN") {
		t.Fatalf("invalid binary: code=%d out=%q stderr=%q", code, out, stderr)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("invalid binary opened or interacted with a worker: %#v", calls)
	}
	if after := snapshotTree(t, f.state); !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid binary mutated task state: before=%v after=%v", before, after)
	}
}
