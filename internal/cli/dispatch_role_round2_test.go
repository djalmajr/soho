package cli

import (
	"bytes"
	"encoding/json"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run372Command(t *testing.T, f *dispatchArrivalFixture, args ...string) (int, string, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := Run(args, f.env)
	return code, out.String(), stderr.String()
}
func TestDispatchRecordedRoleWait(t *testing.T) {
	for _, role := range []string{"implementer", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			f := roleDispatchFixture(t)
			f.env["SOHO_METRICS"] = "on"
			f.env["SOHO_WAIT_POLL_MS"] = "1"
			f.env["SOHO_REPORT_STABLE_MS"] = "0"
			out := probe372Run(t, f, "Type: review\nComplete the task.\n", role, false)
			report, _ := dispatchOutputField(t, out, "report")
			writeRoleFixture(t, report, "# Report\nCompleted.\n")
			code, out, msg := run372Command(t, f, "wait", "worker", "--timeout", "2000")
			if code != 0 || !strings.Contains(out, `"status":"done"`) || strings.Contains(out, `"verdict"`) {
				t.Fatalf("exit=%d out=%s stderr=%s", code, out, msg)
			}
			warning := strings.Contains(msg, "has no 'findings:")
			if warning != (role == "reviewer") {
				t.Fatalf("recorded role=%s policy stderr=%s", role, msg)
			}
			raw, e := os.ReadFile(filepath.Join(f.state, "ws", "metrics.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			var line map[string]any
			if e = json.Unmarshal(bytes.TrimSpace(raw), &line); e != nil {
				t.Fatal(e)
			}
			if line["role"] != role {
				t.Fatalf("metrics role=%v want=%s line=%s", line["role"], role, raw)
			}
			if !strings.Contains(mustRead(t, filepath.Join(f.state, "ws", "agents.tsv")), "\tscouter\t") {
				t.Fatal("roster changed")
			}
		})
	}
}
func TestDispatchRecordedRoleWaitTimeout(t *testing.T) {
	f := roleDispatchFixture(t)
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	f.env["SOHO_SETTLED_GRACE"] = "3600"
	writeRoleFixture(t, filepath.Join(f.root, "roles", "implementer.md"), "---\nname: Implementer\nmode: edit\ntimeout: 120\neffort: high\n---\nEdit.\n")
	writeRoleFixture(t, filepath.Join(f.root, "roles", "scouter.md"), "---\nname: Scouter\nmode: read-only\ntimeout: 600\neffort: high\n---\nRead only.\n")
	probe372Run(t, f, "Type: review\nContinue.\n", "implementer", false)
	code, out, msg := run372Command(t, f, "wait", "worker")
	if code != 9 || !strings.Contains(out, `"status":"timeout"`) || !strings.Contains(msg, "soho wait worker --timeout 240") || strings.Contains(msg, "--timeout 1200") {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, msg)
	}
}

// Ported from review-5-20261026T101809.probe.corrected_test.go.
func probe372Run(t *testing.T, f *dispatchArrivalFixture, body, role string, amend bool) string {
	t.Helper()
	writeRoleFixture(t, f.brief, body)
	args := []string{"worker", f.brief, "--no-wait"}
	if role != "" {
		args = append(args, "--role", role)
	}
	if amend {
		args = append(args, "--amend")
	}
	code, out, msg := f.run(t, args...)
	t.Logf("role=%q amend=%v code=%d out=%s stderr=%s", role, amend, code, out, msg)
	if code != 0 {
		t.Fatalf("dispatch failed: %d", code)
	}
	return out
}
func probe372Sends(t *testing.T, f *dispatchArrivalFixture) int {
	t.Helper()
	calls, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if os.IsNotExist(e) {
		return 0
	}
	if e != nil {
		t.Fatal(e)
	}
	return countArgvPrefix(calls, []string{"agent", "prompt"})
}
func TestIndependent372HistoryDifferentRole(t *testing.T) {
	f := roleDispatchFixture(t)
	probe372Run(t, f, "Type: review\nOriginal.\n", "scouter", false)
	probe372Run(t, f, "Type: review\nAmend X.\n", "scouter", true)
	probe372Run(t, f, "Type: review\nAmend Y.\n", "implementer", true)
	before := probe372Sends(t, f)
	out := probe372Run(t, f, "Type: review\nAmend X.\n", "", true)
	after := probe372Sends(t, f)
	t.Logf("prompt_count_before=%d after=%d", before, after)
	if after != before+1 || strings.Contains(out, "duplicate_of") {
		t.Fatal("amendment X was never sent under implementer but was deduplicated against scouter history")
	}
}
func TestDispatchHistorySameRoleControl(t *testing.T) {
	f := roleDispatchFixture(t)
	probe372Run(t, f, "Type: review\nOriginal.\n", "scouter", false)
	x := probe372Run(t, f, "Type: review\nAmend X.\n", "scouter", true)
	probe372Run(t, f, "Type: review\nAmend Y.\n", "scouter", true)
	before := probe372Sends(t, f)
	out := probe372Run(t, f, "Type: review\nAmend X.\n", "scouter", true)
	composed, _ := dispatchOutputField(t, x, "composed_prompt")
	duplicate, _ := dispatchOutputField(t, out, "duplicate_of")
	if probe372Sends(t, f) != before || duplicate != composed {
		t.Fatalf("same-role history was not deduplicated: %s", out)
	}
}
func TestDispatchHistoryLegacyRoleControl(t *testing.T) {
	f := roleDispatchFixture(t)
	probe372Run(t, f, "Type: review\nOriginal.\n", "scouter", false)
	x := probe372Run(t, f, "Type: review\nAmend X.\n", "scouter", true)
	probe372Run(t, f, "Type: review\nAmend Y.\n", "implementer", true)
	composed, _ := dispatchOutputField(t, x, "composed_prompt")
	path := dispatch.DispatchSidecar(composed)
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	v, e := jsonjs.Parse(b)
	if e != nil {
		t.Fatal(e)
	}
	obj := v.(*jsonjs.Object)
	obj.Delete("role")
	writeRoleFixture(t, path, jsonjs.Stringify(obj))
	before := probe372Sends(t, f)
	out := probe372Run(t, f, "Type: review\nAmend X.\n", "", true)
	if probe372Sends(t, f) != before || !strings.Contains(out, "duplicate_of") {
		t.Fatalf("legacy history did not match: %s", out)
	}
}

// Ported from review-5-20261026T101809.probe.extra_test.go, with explicit
// helper and no-side-effects assertions in addition to the original exit code.
func TestIndependent372FenceClosureWhitespace(t *testing.T) {
	for _, tc := range []struct{ name, close string }{{"spaces", "```  "}, {"tab", "```\t"}, {"crlf", "```\r"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := roleDispatchFixture(t)
			body := "```md\nExample only.\n" + tc.close + "\nType: backend\nEdit production.\n"
			writeRoleFixture(t, f.brief, body)
			code, out, msg := f.run(t, "worker", f.brief, "--no-wait")
			t.Logf("close=%q parsed_type=%q code=%d prompt_calls=%d stdout=%s stderr=%s", tc.close, dispatch.BriefType(body), code, probe372Sends(t, f), out, msg)
			if code != 2 || dispatch.BriefType(body) != "backend" {
				t.Fatalf("exit=%d type=%q", code, dispatch.BriefType(body))
			}
			assertRoleNoSend(t, f)
		})
	}
}
