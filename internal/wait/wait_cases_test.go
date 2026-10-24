package wait

import (
	"bytes"
	"math"
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

func TestWaitJavaScriptPureCases(t *testing.T) {
	t.Run("waitRank: 4 > 11 > 14 > 15 > 7 > 6, everything else 0", func(t *testing.T) { // JS: "waitRank: 4 > 11 > 14 > 15 > 7 > 6, everything else 0"
		for _, tc := range []struct{ code, want int }{{4, 6}, {11, 5}, {14, 4}, {15, 3}, {7, 2}, {6, 1}, {0, 0}, {9, 0}} {
			if got := WaitRank(tc.code); got != tc.want {
				t.Errorf("WaitRank(%d) = %d, want %d", tc.code, got, tc.want)
			}
		}
		if !(WaitRank(4) > WaitRank(11) && WaitRank(11) > WaitRank(14) && WaitRank(14) > WaitRank(15) && WaitRank(15) > WaitRank(7) && WaitRank(7) > WaitRank(6) && WaitRank(0) == 0) {
			t.Fatal("wait return-code rank changed")
		}
	})
	t.Run("pollIntervalMs: only an integer from 1 to 3000 shortens the 3 s poll", func(t *testing.T) { // JS: "pollIntervalMs: only an integer from 1 to 3000 shortens the 3 s poll"
		for _, tc := range []struct {
			raw  string
			want int
		}{{"1", 1}, {"3000", 3000}, {"0", 3000}, {"3001", 3000}, {"1.5", 3000}, {" 10", 3000}, {"-1", 3000}} {
			if got := PollIntervalMs(platform.Env{"SOHO_WAIT_POLL_MS": tc.raw}); got != tc.want {
				t.Errorf("PollIntervalMs(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		}
	})
	t.Run("cksumField: the first field of the local `cksum` (algorithm 3)", func(t *testing.T) { // JS: "cksumField: the first field of the local `cksum` (algorithm 3)"
		if got := CksumField("hello\n"); got != 3015617425 {
			t.Fatalf("CksumField(hello) = %d", got)
		}
	})
	t.Run("activityAgeSeconds: a changed screen has no age, the equal one its age, and no marker has neither", func(t *testing.T) {
		sd := t.TempDir()
		waitDir := filepath.Join(sd, "wait")
		if err := os.MkdirAll(waitDir, 0o700); err != nil {
			t.Fatal(err)
		}
		write := func(name, value string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(waitDir, "agent."+name), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		screen := "Loading file 17%\n"
		write("stuck-hash", "1\n")
		write("probe-at", "100\n")
		if age, changed := ActivityAgeSeconds(sd, "agent", screen, 100); age != nil || !changed {
			t.Fatalf("changed screen = (%v, %v), want no age and changed", age, changed)
		}
		write("stuck-hash", "not-a-number\n")
		if age, changed := ActivityAgeSeconds(sd, "agent", screen, 100); age != nil || !changed {
			t.Fatalf("changed screen with an invalid probe date = (%v, %v), want no age and changed", age, changed)
		}
		write("stuck-hash", ""+stringInt(CksumField(NormalizeScreen(screen)))+"\n")
		write("activity-at", "45\n")
		if age, changed := ActivityAgeSeconds(sd, "agent", screen, 100); changed || age == nil || *age != 55 {
			t.Fatalf("unchanged screen = (%v, %v), want age 55 and no change", age, changed)
		}
		if age, changed := ActivityAgeSeconds(sd, "agent", "", 100); age != nil || changed {
			t.Fatalf("an empty (failed) read = (%v, %v), want nothing", age, changed)
		}
		if age, changed := ActivityAgeSeconds(sd, "missing", screen, 100); age != nil || changed {
			t.Fatalf("missing activity markers = (%v, %v), want nothing", age, changed)
		}
	})
}

func TestWaitJavaScriptReportProbeCases(t *testing.T) {
	t.Run("wait: report ready only on the second probe with the same size", func(t *testing.T) { // JS: "wait: report ready only on the second probe with the same size"
		// A finished worker is idle; working now deliberately defers completion.
		f := newQueuedProbeFixture(t, "idle", "5", "working\n", nil)
		report := filepath.Join(f.sd, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte("# Report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := f.probe(report); got != "pending" {
			t.Fatalf("first equal-size observation = %q, want pending", got)
		}
		if got := f.probe(report); got != "done" {
			t.Fatalf("second equal-size observation = %q, want done", got)
		}
	})
	t.Run("wait: settled with a still screen, reset by movement", func(t *testing.T) { // JS: "wait: settled with a still screen, reset by movement"
		f := newQueuedProbeFixture(t, "idle", "5", "waiting\n", nil)
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		if got := f.probe(""); got != "working" {
			t.Fatalf("first probe = %q", got)
		}
		if err := os.WriteFile(f.marker("screen"), []byte("bogus\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixed = fixed.Add(time.Minute)
		if got := f.probe(""); got != "working" {
			t.Fatalf("movement probe = %q, want a reset", got)
		}
		fixed = fixed.Add(time.Minute)
		if got := f.probe(""); got != "settled" {
			t.Fatalf("unchanged probe after grace = %q, want settled", got)
		}
	})
}

func TestWaitJavaScriptStateProbeCases(t *testing.T) {
	t.Run("wait: two blocked probes before reporting blocked", func(t *testing.T) { // JS: "wait: two blocked probes before reporting blocked"
		f := newQueuedProbeFixture(t, "blocked", "5", "Allow command?\nPress enter to confirm or esc to cancel\n", nil)
		if got := f.probe(""); got != "working" {
			t.Fatalf("first blocked observation = %q, want working", got)
		}
		if _, err := os.Stat(f.marker("blocked")); err != nil {
			t.Fatalf("blocked confirmation marker missing: %v", err)
		}
		if got := f.probe(""); got != "blocked" {
			t.Fatalf("second blocked observation = %q, want blocked", got)
		}
	})
	t.Run("wait: gone and unavailable with a cause", func(t *testing.T) { // JS: "wait: gone and unavailable with a cause"
		gone := newQueuedProbeFixture(t, "gone", "", "", nil)
		if got := gone.probe(""); got != "gone" {
			t.Fatalf("missing worker = %q, want gone", got)
		}
		unavailable := newQueuedProbeFixture(t, "unavailable", "", "", nil)
		if got := unavailable.probe(""); !strings.HasPrefix(got, "unavailable\t") || !strings.Contains(got, "permission_denied") {
			t.Fatalf("permission failure = %q, want unavailable with a cause", got)
		}
	})
	t.Run("wait: an auth failure returns provider-error on the first probe", func(t *testing.T) { // JS: "wait: an auth failure returns provider-error on the first probe"
		f := newQueuedProbeFixture(t, "idle", "5", "Error: 401 Unauthorized: Invalid API key\n", nil)
		if got := f.probe(""); got != "provider-error" {
			t.Fatalf("auth failure = %q, want immediate provider-error", got)
		}
	})
	t.Run("wait: quota wins over a provider stop on the same screen", func(t *testing.T) { // JS: "wait: quota wins over a provider stop on the same screen"
		// Mutation captured: prioritizing the Connection error on this screen returns provider-error (14), not quota (11).
		f := newQueuedProbeFixture(t, "idle", "5", "Individual quota reached\nError: Connection error.\n", nil)
		f.env["SOHO_DIR"] = filepath.Dir(f.sd)
		f.env["HERDR_WORKSPACE_ID"] = filepath.Base(f.sd)
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
		code := CmdWait([]string{"worker", "--timeout", "100"}, f.ctx, f.env, "")
		if code != 11 || !strings.Contains(errOut.String(), "warning: quota: agent 'worker' lane=build kind=claude model=? : Individual quota reached") {
			t.Fatalf("wait code=%d stderr=%q", code, errOut.String())
		}
		if !strings.Contains(out.String(), `"status":"quota"`) || !strings.Contains(out.String(), `"match":"Individual quota reached"`) {
			t.Fatalf("wait output=%q, want quota status and screen match", out.String())
		}
	})
	t.Run("wait: a working probe reads the screen once", func(t *testing.T) { // JS: "wait: a working probe reads the screen once"
		f := newQueuedProbeFixture(t, "working", "5", "Running tests 12%\n", nil)
		if got := f.probe(""); got != "working" {
			t.Fatalf("working state = %q", got)
		}
		reads := 0
		for _, call := range f.calls() {
			if strings.Contains(strings.Join(call.Argv, " "), "agent read worker") {
				reads++
			}
		}
		if reads != 1 {
			t.Fatalf("working probe read screen %d times, want exactly once", reads)
		}
	})
}

func TestWaitJavaScriptAutoApproveCases(t *testing.T) {
	t.Run("tryAutoApprove: off by default, no keypress", func(t *testing.T) { // JS: "tryAutoApprove: off by default, no keypress"
		f := newQueuedProbeFixture(t, "blocked", "5", "Allow command?\nPress enter to confirm\n", nil)
		if TryAutoApprove(f.sd, "worker", f.ctx, f.env) {
			t.Fatal("auto-approve ran without explicit config")
		}
		if hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatal("default-off auto-approve sent Enter")
		}
	})
	t.Run("tryAutoApprove: a counter that exists but is not a number fails closed", func(t *testing.T) { // JS: "tryAutoApprove: a counter that exists but is not a number fails closed"
		f := newQueuedProbeFixture(t, "blocked", "5", "Allow command?\nPress enter to confirm\n", nil)
		env := f.env.Clone()
		env["SOHO_AUTO_APPROVE"] = "on"
		for _, bad := range []string{"", "  \n", "x\n", "1.5\n"} {
			if err := os.WriteFile(f.marker("approvals"), []byte(bad), 0o600); err != nil {
				t.Fatal(err)
			}
			if TryAutoApprove(f.sd, "worker", f.ctx, env) {
				t.Fatalf("invalid approvals value %q was accepted", bad)
			}
			if hasCall(f.calls(), "agent send-keys worker enter") {
				t.Fatalf("invalid approvals value %q sent Enter", bad)
			}
			got, err := os.ReadFile(f.marker("approvals"))
			if err != nil || string(got) != bad {
				t.Fatalf("invalid counter was changed: %q, %v", got, err)
			}
		}
	})
}

func TestWaitJavaScriptRetryCases(t *testing.T) {
	t.Run("wait: an absent seq stays a dash when queued becomes not-received, so a foreign echo gets no Enter", func(t *testing.T) { // JS: "wait: an absent seq stays a dash when queued becomes not-received, so a foreign echo gets no Enter"
		prompt := "/tmp/worker-brief.md"
		f := newQueuedProbeFixture(t, "idle", "", "Read the file /tmp/another-brief.md\n", map[string]string{"queued": "1 - " + prompt + "\n"})
		if got := f.probe(""); got != "not-received" {
			t.Fatalf("probe=%q", got)
		}
		marker, err := os.ReadFile(f.marker("not-received"))
		if err != nil || !strings.Contains(string(marker), " - "+prompt) {
			t.Fatalf("marker=%q err=%v", marker, err)
		}
		if hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatal("foreign echo got Enter")
		}
	})
	t.Run("wait: quota after queued wins over not-received and clears queued markers", func(t *testing.T) { // JS: "wait: quota after queued wins over not-received and clears queued markers"
		f := newQueuedProbeFixture(t, "idle", "5", "quota exceeded for this account\n", map[string]string{
			"queued": "1 5 /tmp/worker-brief.md\n", "not-received": "1 5 /tmp/worker-brief.md\n", "enter-retry": "1 1234567890\n",
		})
		if got := f.probe(""); got != "quota" {
			t.Fatalf("probe=%q", got)
		}
		for _, name := range []string{"queued", "enter-retry"} {
			if _, err := os.Stat(f.marker(name)); !os.IsNotExist(err) {
				t.Errorf("%s remains: %v", name, err)
			}
		}
	})
	t.Run("wait: a fresh not-received marker waits one window before the first retry", func(t *testing.T) { // JS: "wait: a fresh not-received marker waits one window before the first retry"
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		prompt := "/tmp/worker-brief.md"
		// Adapted contract: the retry needs a recognized composer holding
		// the stored path — the claude box, not a plain history line.
		f := newQueuedProbeFixture(t, "idle", "5", claudeBoxScreen("", "❯ Read the file "+prompt+" in full and execute it.", "  [Opus 5.5] 67% [main*]\n"), map[string]string{"not-received": "2000000000 5 " + prompt + "\n"})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "15"
		if got := f.probe(""); got != "working" {
			t.Fatalf("fresh marker = %q, want wait", got)
		}
		if hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatal("first retry was sent before its window")
		}
		fixed = fixed.Add(15 * time.Second)
		if got := f.probe(""); got != "working" {
			t.Fatalf("aged marker = %q, want retry", got)
		}
		if !hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatal("retry was not sent after its window")
		}
	})
	t.Run("wait: three retries spent end the wait not-received", func(t *testing.T) { // JS: "wait: three retries spent end the wait not-received"
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		prompt := "/tmp/worker-brief.md"
		f := newQueuedProbeFixture(t, "idle", "5", "Read the file "+prompt+" in full and execute it.\n", map[string]string{
			"not-received": "1 5 " + prompt + "\n", "enter-retry": "3 1999999900\n",
		})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "15"
		if got := f.probe(""); got != "not-received" {
			t.Fatalf("spent retries = %q", got)
		}
		if hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatal("retry limit still sent Enter")
		}
	})
	t.Run("wait: a state change since the marker drops the marker and sends no Enter", func(t *testing.T) { // JS: "wait: a state change since the marker drops the marker and sends no Enter"
		prompt := "/tmp/worker-brief.md"
		f := newQueuedProbeFixture(t, "working", "6", "Read the file "+prompt+" in full and execute it.\n", map[string]string{"not-received": "1 5 " + prompt + "\n"})
		if got := f.probe(""); got != "working" {
			t.Fatalf("probe after seq change = %q", got)
		}
		if _, err := os.Stat(f.marker("not-received")); !os.IsNotExist(err) {
			t.Fatalf("stale marker remains: %v", err)
		}
		if hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatal("seq change sent Enter")
		}
	})
}

func TestWaitTM4RemainingRetryAndApprovalCases(t *testing.T) {
	t.Run("wait: an Enter retry while the prompt sits in the input box", func(t *testing.T) { // JS: "wait: an Enter retry while the prompt sits in the input box"
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		prompt := "/tmp/worker-brief.md"
		// Adapted contract: the retry needs a recognized composer holding
		// the stored path — the claude box, not a plain "> " marker line.
		f := newQueuedProbeFixture(t, "idle", "5", "Welcome to the worker\n"+claudeBoxScreen("", "❯ Read the file "+prompt+" in full and execute it.", "  [Opus 5.5] 67% [main*]\n"), map[string]string{"not-received": "1999998800 5 " + prompt + "\n"})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "600"
		enterCount := func() int {
			count := 0
			for _, call := range f.calls() {
				if strings.Join(call.Argv, " ") == "agent send-keys worker enter" {
					count++
				}
			}
			return count
		}
		if got := f.probe(""); got != "working" {
			t.Fatalf("probe = %q, want working after retry", got)
		}
		if count := enterCount(); count != 1 {
			t.Fatalf("expired prompt marker sent %d Enter retries, want one", count)
		}
		retry, err := os.ReadFile(f.marker("enter-retry"))
		if err != nil || string(retry) != "1 2000000000\n" {
			t.Fatalf("retry marker = %q, %v; want exact attempt count and timestamp", retry, err)
		}
		if got := f.probe(""); got != "working" {
			t.Fatalf("probe inside retry window = %q, want working", got)
		}
		if count := enterCount(); count != 1 {
			t.Fatalf("probe inside retry window sent %d Enter retries, want one total", count)
		}
		if err := os.WriteFile(f.marker("enter-retry"), []byte("malformed marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := f.probe(""); got != "not-received" {
			t.Fatalf("malformed retry marker state = %q, want not-received", got)
		}
		if count := enterCount(); count != 1 {
			t.Fatalf("malformed retry marker sent %d Enter retries, want one total", count)
		}
	})
	t.Run("tryAutoApprove: key per kind, counter and log, the cap, send failure", func(t *testing.T) { // JS: "tryAutoApprove: key per kind, counter and log, the cap, send failure"
		f := newQueuedProbeFixture(t, "blocked", "5", "Allow command?\nPress enter to confirm\n", nil)
		env := f.env.Clone()
		env["SOHO_AUTO_APPROVE"] = "on"
		if !TryAutoApprove(f.sd, "worker", f.ctx, env) {
			t.Fatal("claude dialog was not auto-approved")
		}
		if !hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatal("claude approval did not send Enter")
		}
		count, err := os.ReadFile(f.marker("approvals"))
		if err != nil || string(count) != "1\n" {
			t.Fatalf("approval count = %q, %v", count, err)
		}
		log, err := os.ReadFile(filepath.Join(f.sd, "wait", "worker.approvals.log"))
		if err != nil || !strings.Contains(string(log), "auto-approved dialog #1") {
			t.Fatalf("approval log = %q, %v", log, err)
		}

		capFixture := newQueuedProbeFixture(t, "blocked", "5", "Allow command?\nPress enter to confirm\n", map[string]string{"approvals": "1\n"})
		capEnv := capFixture.env.Clone()
		capEnv["SOHO_AUTO_APPROVE"] = "on"
		capEnv["SOHO_MAX_AUTO_APPROVALS"] = "1"
		if TryAutoApprove(capFixture.sd, "worker", capFixture.ctx, capEnv) || hasCall(capFixture.calls(), "agent send-keys worker enter") {
			t.Fatal("approval cap allowed another Enter")
		}

		failed := newQueuedProbeFixture(t, "blocked", "5", "Allow command?\nPress enter to confirm\n", nil)
		if err := os.WriteFile(filepath.Join(failed.sd, "agents.tsv"), []byte("# header\nworker\tp0a\tcodex\timplementer\t\t\t\t\t\t\t\t\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Install a separate fake so the default fixture remains untouched.
		failedBin := filepath.Join(t.TempDir(), "bin")
		if _, err := fakecli.Install(t, failedBin, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked","state_change_seq":5}}}`},
			{Argv: []string{"agent", "send-keys", "worker", "y"}, Code: 1, Stderr: "denied"},
		}); err != nil {
			t.Fatal(err)
		}
		failedEnv := failed.env.Clone()
		failedEnv["PATH"] = failedBin
		failedEnv["SOHO_FAKECLI_CONFIG"] = failedBin
		failedEnv["SOHO_AUTO_APPROVE"] = "on"
		if TryAutoApprove(failed.sd, "worker", failed.ctx, failedEnv) {
			t.Fatal("failed key send was reported as approved")
		}
		calls, err := fakecli.ReadCalls(filepath.Join(failedBin, "herdr.calls.jsonl"))
		if err != nil || !hasCall(calls, "agent send-keys worker y") {
			t.Fatalf("codex approval did not use y: calls=%#v err=%v", calls, err)
		}
		if _, err := os.Stat(failed.marker("approvals")); !os.IsNotExist(err) {
			t.Fatalf("failed send changed approval count: %v", err)
		}
	})
}

func TestWaitTM4RetryThenDoneCase(t *testing.T) {
	t.Run("wait: the retry Enter unblocks the worker and the wait settles done", func(t *testing.T) { // JS: "wait: the retry Enter unblocks the worker and the wait settles done"
		// Mutation captured: losing the retry Enter leaves the expired prompt unreceived and prevents the subsequent report from settling.
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		prompt := "/tmp/worker-brief.md"
		// Adapted contract: the retry needs a recognized composer holding
		// the stored path — the claude box, not a plain "> " marker line.
		f := newQueuedProbeFixture(t, "idle", "5", "Welcome to the worker\n"+claudeBoxScreen("", "❯ Read the file "+prompt+" in full and execute it.", "  [Opus 5.5] 67% [main*]\n"), map[string]string{"not-received": "1999998800 5 " + prompt + "\n"})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "600"
		if got := f.probe(""); got != "working" || !hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatalf("retry did not keep the probe working with one Enter: state=%q calls=%#v", got, f.calls())
		}
		report := filepath.Join(f.sd, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte("completed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := f.probe(report); got != "pending" {
			t.Fatalf("first report probe=%q", got)
		}
		if got := f.probe(report); got != "done" {
			t.Fatalf("settled report probe=%q", got)
		}
	})
}

func TestWaitTM4ApprovalDialogHistoryCases(t *testing.T) {
	newApprovalFixture := func(t *testing.T, screens []string) *queuedProbeFixture {
		t.Helper()
		f := newQueuedProbeFixture(t, "blocked", "5", "", map[string]string{"blocked": ""})
		bin := filepath.Join(t.TempDir(), "bin")
		rules := []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked","state_change_seq":5}}}`}}
		for i, screen := range screens {
			rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Call: i + 1, Stdout: screen})
		}
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}})
		if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
			t.Fatal(err)
		}
		f.bin = bin
		env := f.env.Clone()
		env["PATH"] = bin
		env["SOHO_FAKECLI_CONFIG"] = bin
		env["SOHO_AUTO_APPROVE"] = "on"
		f.env = env
		return f
	}
	t.Run("wait: the same auto-approve dialog is not sent a third time", func(t *testing.T) { // JS: "wait: the same auto-approve dialog is not sent a third time"
		f := newApprovalFixture(t, []string{"Allow command?\nPress enter to confirm\n", "Allow command?\nPress enter to confirm\n", "Allow command?\nPress enter to confirm\n"})
		states := []string{f.probe(""), f.probe(""), f.probe(""), f.probe(""), f.probe("")}
		if strings.Join(states, ",") != "working,working,working,working,blocked" {
			t.Fatalf("repeat dialog states = %v", states)
		}
		count := 0
		for _, call := range f.calls() {
			if strings.Join(call.Argv, " ") == "agent send-keys worker enter" {
				count++
			}
		}
		if count != 2 {
			t.Fatalf("same dialog sent Enter %d times, want two", count)
		}
	})
	t.Run("wait: rotating glyphs in the same dialog do not reset the repeat counter", func(t *testing.T) { // JS: "wait: rotating glyphs in the same dialog do not reset the repeat counter"
		first := "⠋ Allow command?\nPress enter to confirm\n"
		rotated := "⠙ Allow command?\nPress enter to confirm\n"
		if NormalizeApproveScreen(first) != NormalizeApproveScreen(rotated) {
			t.Fatalf("rotating glyphs changed the normalized dialog: %q != %q", NormalizeApproveScreen(first), NormalizeApproveScreen(rotated))
		}
		f := newApprovalFixture(t, []string{first, rotated, first})
		states := []string{f.probe(""), f.probe(""), f.probe(""), f.probe(""), f.probe("")}
		if strings.Join(states, ",") != "working,working,working,working,blocked" {
			t.Fatalf("rotating dialog states = %v", states)
		}
	})
	t.Run("wait: a different dialog resets the repeat counter; the ceiling still applies", func(t *testing.T) { // JS: "wait: a different dialog resets the repeat counter; the ceiling still applies"
		f := newApprovalFixture(t, []string{
			"Allow command A?\nPress enter to confirm\n",
			"Allow command A?\nPress enter to confirm\n",
			"Allow command B?\nPress enter to confirm\n",
			"Allow command B?\nPress enter to confirm\n",
			"Allow command B?\nPress enter to confirm\n",
		})
		states := []string{f.probe(""), f.probe(""), f.probe(""), f.probe(""), f.probe(""), f.probe(""), f.probe(""), f.probe(""), f.probe("")}
		if strings.Join(states, ",") != "working,working,working,working,working,working,working,working,blocked" {
			t.Fatalf("changed dialog states = %v", states)
		}
	})
}

func TestWaitTM4CheckpointRemainingCases(t *testing.T) {
	t.Run("wait: a move seen across a long gap between waits has no age (changed since the last probe, not active)", func(t *testing.T) {
		fixed := time.Unix(2_000_001_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		f := newQueuedProbeFixture(t, "working", "5", "Loading 20%\n", nil)
		oldScreen := "Starting the task\n"
		if err := os.WriteFile(f.marker("stuck-hash"), []byte(strconv.FormatUint(uint64(CksumField(NormalizeScreen(oldScreen))), 10)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.marker("probe-at"), []byte("2000000000\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// The recorded hash is the old screen's: the read-only age is
		// unknown (no age) and the screen is changed since that probe.
		age, changed := ActivityAgeSeconds(f.sd, "worker", "Loading 20%\n", fixed.Unix())
		if age != nil || !changed {
			t.Fatalf("activity = (%v, %v), want no age and a changed screen", age, changed)
		}
		if got := f.probe(""); got != "working" {
			t.Fatalf("changed screen state = %q", got)
		}
		activity, err := os.ReadFile(f.marker("activity-at"))
		if err != nil || string(activity) != "2000000000\n" {
			t.Fatalf("activity-at = %q, %v; want the prior probe time", activity, err)
		}
	})
	t.Run("wait: with stuck_warn_minutes=0 the hash still follows the screen (no endless fresh change)", func(t *testing.T) { // JS: "wait: with stuck_warn_minutes=0 the hash still follows the screen (no endless fresh change)"
		f := newQueuedProbeFixture(t, "working", "5", "Busy 30%\n", nil)
		f.env["SOHO_STUCK_WARN_MINUTES"] = "0"
		if err := os.WriteFile(f.marker("stuck-hash"), []byte(strconv.FormatUint(uint64(CksumField(NormalizeScreen("Busy 10%\n"))), 10)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if got := f.probe(""); got != "working" {
				t.Fatalf("working state = %q", got)
			}
		}
		want := strconv.FormatUint(uint64(CksumField(NormalizeScreen("Busy 30%\n"))), 10) + "\n"
		if got, err := os.ReadFile(f.marker("stuck-hash")); err != nil || string(got) != want {
			t.Fatalf("screen hash = %q, %v; want %q", got, err, want)
		}
	})
	t.Run("wait: a non-numeric timeout drops the --timeout suggestion (checkpoint and warn)", func(t *testing.T) { // JS: "wait: a non-numeric timeout drops the --timeout suggestion (checkpoint and warn)"
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		run := func(t *testing.T, active bool) (string, string, int) {
			t.Helper()
			f := newQueuedProbeFixture(t, "working", "5", "Busy on the task 30%\n", nil)
			screen := "Busy on the task 30%\n"
			if err := os.WriteFile(f.marker("stuck-hash"), []byte(strconv.FormatUint(uint64(CksumField(NormalizeScreen(screen))), 10)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if active {
				if err := os.WriteFile(f.marker("activity-at"), []byte("1999999995\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			oldOut, oldErr := platform.Stdout, platform.Stderr
			platform.Stdout, platform.Stderr = &stdout, &stderr
			code := WaitFor([]string{"worker"}, f.sd, &core.Config{Entries: map[string]core.ConfigEntry{}}, f.env, math.NaN(), false, "")
			platform.Stdout, platform.Stderr = oldOut, oldErr
			return stdout.String(), stderr.String(), code
		}
		out, stderr, code := run(t, true)
		if code != 9 || !strings.Contains(out, `"checkpoint":true`) || !strings.Contains(stderr, "wait again: soho wait worker\n") || strings.Contains(stderr, "--timeout") {
			t.Fatalf("active timeout: code=%d out=%q stderr=%q", code, out, stderr)
		}
		out, stderr, code = run(t, false)
		if code != 9 || !strings.Contains(out, `"checkpoint":false`) || strings.Contains(stderr, "soho wait worker") || strings.Contains(stderr, "--timeout") {
			t.Fatalf("inactive timeout: code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

func stringInt(n uint32) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var out [10]byte
	i := len(out)
	for n > 0 {
		i--
		out[i] = digits[n%10]
		n /= 10
	}
	return string(out[i:])
}
