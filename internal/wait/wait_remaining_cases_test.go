package wait

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestWaitRemainingQueuedAndRetryCases(t *testing.T) {
	t.Run("wait: queued prompt path is recognized from the recent unwrapped composer", func(t *testing.T) { // Adapted contract: the queued retry needs a recognized composer holding the stored path (the claude box), not a plain history line.
		prompt := "/tmp/worker-brief.md"
		f := newQueuedProbeFixture(t, "idle", "5", claudeBoxScreen("", "❯ Read the file "+prompt+" in full and execute it.", "  [Opus 5.5] 67% [main*]\n"), map[string]string{"queued": "1 5 " + prompt + "\n"})
		if got := f.probe(""); got != "working" {
			t.Fatalf("queued prompt probe = %q, want working while retry is sent", got)
		}
		if !hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatalf("queued prompt not recognized from recent output: %#v", f.calls())
		}
	})
	t.Run("wait: a queued working probe still records stuck-screen activity", func(t *testing.T) { // JS: "wait: a queued working probe still records stuck-screen activity"
		f := newQueuedProbeFixture(t, "working", "5", "same screen\n", map[string]string{"queued": "1 5 /tmp/worker-brief.md\n"})
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		if got := f.probe(""); got != "working" {
			t.Fatalf("probe=%q", got)
		}
		if got, err := os.ReadFile(f.marker("probe-at")); err != nil || strings.TrimSpace(string(got)) != "2000000000" {
			t.Fatalf("probe-at=%q err=%v", got, err)
		}
	})
	t.Run("wait: a transient unavailable probe preserves queued input for the next wait", func(t *testing.T) { // JS: "wait: a transient unavailable probe preserves queued input for the next wait"
		queued, retry := "1 5 /tmp/worker-brief.md\n", "1 1234567890\n"
		f := newQueuedProbeFixture(t, "unavailable", "", "", map[string]string{"queued": queued, "enter-retry": retry})
		if got := f.probe(""); !strings.HasPrefix(got, "unavailable\t") {
			t.Fatalf("probe=%q", got)
		}
		for name, want := range map[string]string{"queued": queued, "enter-retry": retry} {
			got, err := os.ReadFile(f.marker(name))
			if err != nil || string(got) != want {
				t.Errorf("%s=%q err=%v, want preserved %q", name, got, err, want)
			}
		}
	})
	t.Run("wait: a late arrival clears the not-received marker and the retry counter", func(t *testing.T) { // JS: "wait: a late arrival clears the not-received marker and the retry counter"
		f := newQueuedProbeFixture(t, "idle", "6", "running\n", map[string]string{
			"not-received": "1 5 /tmp/worker-brief.md\n", "enter-retry": "2 1999999990\n",
		})
		if got := f.probe(""); got != "working" {
			t.Fatalf("arrival probe=%q", got)
		}
		for _, name := range []string{"not-received", "enter-retry"} {
			if _, err := os.Stat(f.marker(name)); !os.IsNotExist(err) {
				t.Errorf("%s marker remains after arrival: %v", name, err)
			}
		}
	})
	t.Run("wait: a blocked worker clears the not-received marker and keeps the blocked logic", func(t *testing.T) { // JS: "wait: a blocked worker clears the not-received marker and keeps the blocked logic"
		f := newQueuedProbeFixture(t, "blocked", "5", "Allow command?\nPress enter to confirm or esc to cancel\n", map[string]string{
			"blocked": "\n", "not-received": "1 5 /tmp/worker-brief.md\n", "enter-retry": "1 1234567890\n",
		})
		if got := f.probe(""); got != "blocked" {
			t.Fatalf("blocked probe=%q", got)
		}
		for _, name := range []string{"not-received", "enter-retry"} {
			if _, err := os.Stat(f.marker(name)); !os.IsNotExist(err) {
				t.Errorf("%s marker remains: %v", name, err)
			}
		}
	})
	t.Run("wait: the same seq and an epoch-only marker without a stored path keep the marker without keys", func(t *testing.T) { // Adapted contract: a missing stored path keeps the delivery uncertain without keys.
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		prompt := "/tmp/worker-brief.md"
		// Adapted contract: the marker carries no stored path (epoch only,
		// no last-report to resolve one), so the identity/path is missing:
		// the delivery keeps uncertain without keys — even on a recognized
		// composer holding a different prompt — and the marker stays.
		f := newQueuedProbeFixture(t, "idle", "5", claudeBoxScreen("", "❯ Read the file "+prompt+" in full and execute it.", "  [Opus 5.5] 67% [main*]\n"), map[string]string{
			"not-received": "1999999900\n", "enter-retry": "1 1999999900\n",
		})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "15"
		if got := f.probe(""); got != "not-received" {
			t.Fatalf("pathless marker probe=%q, want not-received without keys", got)
		}
		if hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatalf("epoch-only marker sent a key: %#v", f.calls())
		}
		if _, err := os.Stat(f.marker("not-received")); err != nil {
			t.Fatalf("retry marker removed: %v", err)
		}
	})
	t.Run("wait: a not-received agent whose screen no longer holds the prompt ends not-received", func(t *testing.T) { // JS: "wait: a not-received agent whose screen no longer holds the prompt ends not-received"
		f := newQueuedProbeFixture(t, "idle", "5", "ordinary idle screen\n", map[string]string{"not-received": "1 5 /tmp/worker-brief.md\n"})
		if got := f.probe(""); got != "not-received" {
			t.Fatalf("probe=%q", got)
		}
		if hasCall(f.calls(), "agent send-keys worker enter") {
			t.Fatal("prompt absent from input but Enter was sent")
		}
	})
}

func TestWaitRemainingMirrorAndStuckSinceCases(t *testing.T) {
	newMirror := func(t *testing.T) (*core.Config, platform.Env, string, string, string) {
		t.Helper()
		root := t.TempDir()
		state := filepath.Join(root, "state", "ws")
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		env := platform.Env{"TMPDIR": root, "HERDR_WORKSPACE_ID": "ws"}
		tmpReportDir := filepath.Join(root, "soho", core.WorkspaceID(ctx, env, ""), "reports")
		if err := os.MkdirAll(tmpReportDir, 0o700); err != nil {
			t.Fatal(err)
		}
		return ctx, env, state, tmpReportDir, filepath.Join(state, "reports")
	}
	t.Run("wait: a done report under the $TMPDIR routing is mirrored into the state dir", func(t *testing.T) { // JS: "wait: a done report under the $TMPDIR routing is mirrored into the state dir"
		// Mutation captured: dropping the TMPDIR report mirror leaves the completed report unavailable to later offline commands.
		ctx, env, state, srcDir, dstDir := newMirror(t)
		if err := os.MkdirAll(dstDir, 0o700); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(srcDir, "worker-20260929T000000.md")
		for path, body := range map[string]string{src: "completed\n", filepath.Join(srcDir, "worker-20260929T000000.brief.md"): "brief\n"} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		mirrorReport(state, "worker", src, ctx, env, "")
		if data, err := os.ReadFile(filepath.Join(dstDir, filepath.Base(src))); err != nil || string(data) != "completed\n" {
			t.Fatalf("mirror=%q err=%v", data, err)
		}
	})
	t.Run("wait: a mirror once written is never overwritten", func(t *testing.T) {
		// DJA-538 round 4: a settled mirror remains the original snapshot.
		ctx, env, state, srcDir, dstDir := newMirror(t)
		if err := os.MkdirAll(dstDir, 0o700); err != nil {
			t.Fatal(err)
		}
		src, dst := filepath.Join(srcDir, "worker-report.md"), filepath.Join(dstDir, "worker-report.md")
		for path, body := range map[string]string{src: "preserved report\n", filepath.Join(srcDir, "worker-report.brief.md"): "brief\n"} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		mirrorReport(state, "worker", src, ctx, env, "")
		if err := os.WriteFile(src, []byte("new report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stderr strings.Builder
		oldErr := platform.Stderr
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		mirrorReport(state, "worker", src, ctx, env, "")
		if data, err := os.ReadFile(dst); err != nil || string(data) != "preserved report\n" || !strings.Contains(stderr.String(), "kept "+dst) {
			t.Fatalf("state copy=%q stderr=%q err=%v", data, stderr.String(), err)
		}
	})
	t.Run("wait: the mirror copies the dispatch sidecar (identical kept, different stands)", func(t *testing.T) { // JS: "wait: the mirror copies the dispatch sidecar (identical kept, different stands)"
		// Mutation captured: replacing the sidecar policy either drops accepted provenance or overwrites a conflicting state sidecar.
		ctx, env, state, srcDir, dstDir := newMirror(t)
		if err := os.MkdirAll(dstDir, 0o700); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(srcDir, "worker-20260929T000000.md")
		brief := filepath.Join(srcDir, "worker-20260929T000000.brief.md")
		for path, body := range map[string]string{report: "report\n", brief: "brief\n"} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		side := filepath.Join(srcDir, "worker-20260929T000000.dispatch.json")
		if err := os.WriteFile(side, []byte("{\"accepted\":true}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		dstSide := filepath.Join(state, "briefs", filepath.Base(side))
		if err := os.MkdirAll(filepath.Dir(dstSide), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dstSide, []byte("{\"accepted\":true}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stderr strings.Builder
		oldErr := platform.Stderr
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		mirrorReport(state, "worker", report, ctx, env, "")
		if data, err := os.ReadFile(dstSide); err != nil || string(data) != "{\"accepted\":true}\n" || stderr.Len() != 0 {
			t.Fatalf("identical sidecar=%q stderr=%q err=%v", data, stderr.String(), err)
		}
		if err := os.WriteFile(dstSide, []byte("local sidecar\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stderr.Reset()
		mirrorReport(state, "worker", report, ctx, env, "")
		if data, err := os.ReadFile(dstSide); err != nil || string(data) != "local sidecar\n" || !strings.Contains(stderr.String(), "kept "+dstSide) {
			t.Fatalf("sidecar=%q stderr=%q err=%v", data, stderr.String(), err)
		}
	})
	t.Run("wait: mirroring an old pair without a sidecar is silent", func(t *testing.T) { // JS: "wait: mirroring an old pair without a sidecar is silent"
		// Mutation captured: legacy pairs without dispatch metadata must not produce a missing-sidecar warning.
		ctx, env, state, srcDir, dstDir := newMirror(t)
		if err := os.MkdirAll(dstDir, 0o700); err != nil {
			t.Fatal(err)
		}
		brief := filepath.Join(srcDir, "worker-report.brief.md")
		report := filepath.Join(srcDir, "worker-report.md")
		for path, body := range map[string]string{brief: "brief\n", report: "report\n"} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var stderr strings.Builder
		oldErr := platform.Stderr
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		mirrorReport(state, "worker", report, ctx, env, "")
		if stderr.Len() != 0 {
			t.Fatalf("legacy mirror warned: %q", stderr.String())
		}
		if data, err := os.ReadFile(filepath.Join(dstDir, filepath.Base(report))); err != nil || string(data) != "report\n" {
			t.Fatalf("report=%q err=%v", data, err)
		}
	})
	t.Run("wait: a missing, empty or non-numeric stuck-since is now (rewritten), never epoch 0", func(t *testing.T) { // JS: "wait: a missing, empty or non-numeric stuck-since is now (rewritten), never epoch 0"
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		for _, marker := range []string{"missing", "", "not-a-number\n"} {
			f := newQueuedProbeFixture(t, "working", "5", "Busy on the task\n", nil)
			if err := os.WriteFile(f.marker("stuck-hash"), []byte(stringInt(CksumField(NormalizeScreen("Busy on the task\n")))+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if marker != "missing" {
				if err := os.WriteFile(f.marker("stuck-since"), []byte(marker), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := f.probe(""); got != "working" {
				t.Fatalf("marker=%q state=%q", marker, got)
			}
			if got, err := os.ReadFile(f.marker("stuck-since")); err != nil || string(got) != "2000000000\n" {
				t.Fatalf("marker=%q stuck-since=%q err=%v", marker, got, err)
			}
		}
	})
}

func TestWaitRemainingCapacityCases(t *testing.T) {
	newCapacity := func(t *testing.T) (*queuedProbeFixture, string) {
		t.Helper()
		f := newQueuedProbeFixture(t, "idle", "5", `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`+"\n", nil)
		bin := filepath.Join(t.TempDir(), "bin")
		if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
			{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		}); err != nil {
			t.Fatal(err)
		}
		f.bin = bin
		f.env["PATH"] = bin
		f.env["SOHO_FAKECLI_CONFIG"] = bin
		f.env["SOHO_PROVIDER_RETRIES"] = "1"
		f.env["SOHO_PROVIDER_RETRY_DELAY"] = "0"
		return f, bin
	}
	t.Run("wait: capacity sends the continue prompt and the report settles done", func(t *testing.T) { // JS: "wait: capacity sends the continue prompt and the report settles done"
		// Mutation captured: omitting the capacity continuation leaves the provider error terminal and prevents a later report from settling.
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		f, _ := newCapacity(t)
		report := filepath.Join(f.sd, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if got := f.probe(""); got != "working" {
				t.Fatalf("capacity probe %d = %q", i+1, got)
			}
		}
		calls := f.calls()
		prompt := "The model provider was at capacity and your last request failed. Continue the task from where you stopped; do not redo finished steps. When finished, write your report to " + report + " and reply with only that path."
		found := false
		for _, call := range calls {
			if len(call.Argv) == 4 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" && call.Argv[2] == "worker" && call.Argv[3] == prompt {
				found = true
			}
		}
		if !found {
			t.Fatalf("continue prompt missing: %#v", calls)
		}
		if err := os.WriteFile(report, []byte("finished\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := f.probe(report); got != "pending" {
			t.Fatalf("first report probe=%q", got)
		}
		if got := f.probe(report); got != "done" {
			t.Fatalf("stable report probe=%q", got)
		}
	})
	t.Run("wait: capacity exhausted at provider_retries=1 exits 14 with the retries", func(t *testing.T) { // JS: "wait: capacity exhausted at provider_retries=1 exits 14 with the retries"
		// Mutation captured: ignoring provider_retries sends an extra continuation instead of returning the exhausted capacity state.
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		f, _ := newCapacity(t)
		for i := 0; i < 3; i++ {
			if got := f.probe(""); got != "working" {
				t.Fatalf("capacity probe %d = %q", i+1, got)
			}
		}
		for i := 0; i < 1; i++ {
			if got := f.probe(""); got != "working" {
				t.Fatalf("capacity recovery probe %d = %q", i+1, got)
			}
		}
		if got := f.probe(""); got != "capacity" {
			t.Fatalf("exhausted probe=%q", got)
		}
		if got, err := os.ReadFile(f.marker("capacity-retries")); err != nil || string(got) != "1\n" {
			t.Fatalf("retries=%q err=%v", got, err)
		}
		prompts := 0
		for _, call := range f.calls() {
			if len(call.Argv) >= 3 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" && call.Argv[2] == "worker" {
				prompts++
			}
		}
		if prompts != 1 {
			t.Fatalf("continue count=%d", prompts)
		}
	})
}

func TestWaitRemainingExitRankCases(t *testing.T) {
	t.Run("wait: the rank order 11 > 14 > 7 with quota, provider-error and blocked", func(t *testing.T) { // JS: "wait: the rank order 11 > 14 > 7 with quota, provider-error and blocked"
		// Mutation captured: demoting provider-error below blocked changes the multi-agent exit code.
		if !(WaitRank(11) > WaitRank(14) && WaitRank(14) > WaitRank(7)) {
			t.Fatalf("rank quota=%d provider=%d blocked=%d", WaitRank(11), WaitRank(14), WaitRank(7))
		}
	})
	t.Run("wait: the rank order 4 > 11 > 14 > 15 > 7 with a not-received agent", func(t *testing.T) { // JS: "wait: the rank order 4 > 11 > 14 > 15 > 7 with a not-received agent"
		// Mutation captured: changing any adjacent exit precedence returns the wrong multi-agent wait code.
		if !(WaitRank(4) > WaitRank(11) && WaitRank(11) > WaitRank(14) && WaitRank(14) > WaitRank(15) && WaitRank(15) > WaitRank(7)) {
			t.Fatalf("rank unavailable=%d quota=%d provider=%d not-received=%d blocked=%d", WaitRank(4), WaitRank(11), WaitRank(14), WaitRank(15), WaitRank(7))
		}
	})
}

func TestWaitStuckScreenWarningOnceCase(t *testing.T) {
	t.Run("wait: a still screen (counters aside) warns once after stuck_warn_minutes", func(t *testing.T) { // JS: "wait: a still screen (counters aside) warns once after stuck_warn_minutes"
		// Mutation captured: warning on every probe duplicates friction, while skipping the threshold leaves the operator without a stuck-screen notice.
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		screen := "Running tests 42% ◐ 3.1s\n"
		f := newQueuedProbeFixture(t, "working", "5", screen, nil)
		f.env["SOHO_STUCK_WARN_MINUTES"] = "1"
		if err := os.WriteFile(f.marker("stuck-hash"), []byte(stringInt(CksumField(NormalizeScreen(screen)))+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.marker("stuck-since"), []byte("1999999930\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stderr strings.Builder
		oldErr := platform.Stderr
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		for i := 0; i < 2; i++ {
			if got := f.probe(""); got != "working" {
				t.Fatalf("probe %d state=%q", i+1, got)
			}
		}
		warning := "agent 'worker' has shown the same screen (apart from counters) for 1 min while working"
		if strings.Count(stderr.String(), warning) != 1 {
			t.Fatalf("warning output=%q", stderr.String())
		}
		if _, err := os.Stat(f.marker("stuck-warned")); err != nil {
			t.Fatalf("stuck-warned marker: %v", err)
		}
	})
}

func TestWaitRemainingProviderCases(t *testing.T) {
	t.Run("wait: provider-error only on the second equal probe", func(t *testing.T) { // JS: "wait: provider-error only on the second equal probe"
		f := newQueuedProbeFixture(t, "idle", "5", "Error: Connection error.\n", nil)
		if got := f.probe(""); got != "working" {
			t.Fatalf("first provider probe=%q, want confirmation", got)
		}
		if got := f.probe(""); got != "provider-error" {
			t.Fatalf("second equal provider probe=%q", got)
		}
	})
	t.Run("wait: a probe without the stop clears the provider double-confirm", func(t *testing.T) { // JS: "wait: a probe without the stop clears the provider double-confirm"
		f := newQueuedProbeFixture(t, "idle", "5", "Error: Connection error.\n", nil)
		if got := f.probe(""); got != "working" {
			t.Fatalf("first provider probe=%q", got)
		}
		writeQueuedProbeRules(t, f, "ordinary output\n")
		if got := f.probe(""); got != "working" {
			t.Fatalf("ordinary probe=%q", got)
		}
		if _, err := os.Stat(f.marker("provider")); !os.IsNotExist(err) {
			t.Fatalf("provider confirmation survived a normal probe: %v", err)
		}
	})
	t.Run("wait: a quota probe between two provider probes clears the provider record", func(t *testing.T) { // JS: "wait: a quota probe between two provider probes clears the provider record"
		f := newQueuedProbeFixture(t, "idle", "5", "Error: Connection error.\n", nil)
		if got := f.probe(""); got != "working" {
			t.Fatalf("first provider probe=%q", got)
		}
		writeQueuedProbeRules(t, f, "quota exceeded for this account\n")
		if got := f.probe(""); got != "quota" {
			t.Fatalf("quota probe=%q", got)
		}
		if _, err := os.Stat(f.marker("provider")); !os.IsNotExist(err) {
			t.Fatalf("provider marker survived quota: %v", err)
		}
	})
	t.Run("wait: a non-auth unexpected status still needs two probes", func(t *testing.T) { // JS: "wait: a non-auth unexpected status still needs two probes"
		f := newQueuedProbeFixture(t, "idle", "5", "Error: 502 Bad Gateway\n", nil)
		if got := f.probe(""); got != "working" {
			t.Fatalf("first unexpected status=%q", got)
		}
		if got := f.probe(""); got != "provider-error" {
			t.Fatalf("second unexpected status=%q", got)
		}
	})
	t.Run("wait: bare unexpected statuses without a reason phrase", func(t *testing.T) { // JS: "wait: bare unexpected statuses without a reason phrase"
		f := newQueuedProbeFixture(t, "idle", "5", "Error: unexpected status 503\n", nil)
		if got := f.probe(""); got != "working" {
			t.Fatalf("bare 503 first probe=%q", got)
		}
		if got := f.probe(""); got != "provider-error" {
			t.Fatalf("bare 503 second probe=%q", got)
		}
		if got, err := os.ReadFile(f.marker("provider-cause")); err != nil || string(got) != "Error: unexpected status 503\n" {
			t.Fatalf("provider cause=%q err=%v", got, err)
		}
		writeQueuedProbeRules(t, f, "Error: unexpected status 401\n")
		if got := f.probe(""); got != "provider-error" {
			t.Fatalf("bare 401 should be terminal, got %q", got)
		}
	})
}

func TestWaitRemainingTaskCases(t *testing.T) {
	t.Run("briefTask: H1, the Brief prefixes, contract sections, no H1, CRLF", func(t *testing.T) { // JS: "briefTask: H1, the Brief prefixes, contract sections, no H1, CRLF"
		for _, tc := range []struct {
			name, body, want string
		}{
			{"brief heading", "\r\n# Brief — handle retries\r\n# Later\r\n", "handle retries"},
			{"contract heading", "# Goal\r\n# Real task\r\n", "task"},
			{"no heading", "body only\r\n", "task"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				file := filepath.Join(t.TempDir(), "task.md")
				if err := os.WriteFile(file, []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
				if got := core.BriefTask(file); got != tc.want {
					t.Fatalf("BriefTask=%q, want %q", got, tc.want)
				}
			})
		}
	})
	t.Run("markTaskDone: adds the check mark once, via paneTitle", func(t *testing.T) { // JS: "markTaskDone: adds the check mark once, via paneTitle"
		base := t.TempDir()
		sd := filepath.Join(base, "state")
		if err := os.MkdirAll(sd, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tpane-1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sd, "task-worker"), []byte("Task title\n\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(base, "bin")
		if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"pane", "report-metadata", "pane-1", "--source", "soho", "--title", "Task title ✓"}}}); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "HERDR_SOCKET_PATH": filepath.Join(base, "none.sock")}
		if err := core.MarkTaskDone(sd, "worker", env); err != nil {
			t.Fatal(err)
		}
		if err := core.MarkTaskDone(sd, "worker", env); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(sd, "task-worker"))
		if err != nil || string(got) != "Task title ✓\n" {
			t.Fatalf("task=%q err=%v", got, err)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		if err != nil || len(calls) != 1 || strings.Join(calls[0].Argv, " ") != "pane report-metadata pane-1 --source soho --title Task title ✓" {
			t.Fatalf("calls=%+v err=%v", calls, err)
		}
	})
}

func writeQueuedProbeRules(t *testing.T, f *queuedProbeFixture, screen string) {
	t.Helper()
	type fakeScript struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}
	rules := []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`}}
	for _, source := range [][]string{
		{"agent", "read", "worker", "--source", "visible", "--lines", "20"},
		{"agent", "read", "worker", "--source", "visible", "--lines", "40"},
		{"agent", "read", "worker", "--source", "visible"},
		{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"},
	} {
		rules = append(rules, fakecli.Rule{Argv: source, Stdout: screen})
	}
	data, err := json.Marshal(fakeScript{Log: f.bin + "/herdr.calls.jsonl", Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.bin+"/herdr.json", data, 0o600); err != nil {
		t.Fatal(err)
	}
}
