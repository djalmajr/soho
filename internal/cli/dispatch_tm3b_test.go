package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchTM3bOwnedPathsAndOverlap(t *testing.T) {
	// JS: "ownedPaths: spans, list items, normalization, stopwords, aliases, section end"
	t.Run("ownedPaths: spans, list items, normalization, stopwords, aliases, section end", func(t *testing.T) {
		body := "# Goal\ntext\n## Owned files\n- `./internal/a.go`,\n- docs/\n- none\n- not `internal/no.go`\n### detail\n- internal/inside.go\n## Later\n- internal/ignored.go\n"
		got := ownedPaths(body, nil)
		// rc.12 (R-R12D): an owned directory keeps its trailing slash, so a
		// glob such as *.md still crosses docs/; the frozen JS dropped it.
		want := []string{"internal/a.go", "docs/", "internal/inside.go"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("owned paths=%v, want %v", got, want)
		}
		aliased := ownedPaths("## Touch\n- src/app.go\n## Next\n- src/no.go\n", map[string][]string{"Owned files": {"Touch"}})
		if !reflect.DeepEqual(aliased, []string{"src/app.go"}) {
			t.Fatalf("aliased owned paths=%v", aliased)
		}
	})
	// JS: "ownedPaths: common Portuguese No, Sem, and Fora openers keep their paths"
	t.Run("ownedPaths: common Portuguese No, Sem, and Fora openers keep their paths", func(t *testing.T) {
		got := ownedPaths("## Arquivos\n- Não editar `internal/a.go`\n- Sem alterar `internal/b.go`\n- Fora do escopo `internal/c.go`\n", nil)
		if !reflect.DeepEqual(got, []string{"internal/a.go", "internal/b.go", "internal/c.go"}) {
			t.Fatalf("Portuguese opener paths=%v", got)
		}
	})
	// JS: "dispatch: the owned-files overlap warn fires for a file-name glob of the pending brief"
	t.Run("dispatch: the owned-files overlap warn fires for a file-name glob of the pending brief", func(t *testing.T) {
		if !pathsCross("internal/*.go", "internal/dispatch.go") {
			t.Fatal("file-name glob did not overlap its matching owned path")
		}
	})
	// JS: "dispatch: excluded real-world paths do not cause a false overlap warning"
	t.Run("dispatch: excluded real-world paths do not cause a false overlap warning", func(t *testing.T) {
		if pathsCross("internal/dispatch.go", "internal/dispatch_test.go") || pathsCross("internal/dispatch.go", "internal/other.go") {
			t.Fatal("unrelated files overlapped")
		}
	})
}

func TestDispatchTM3bPathRelations(t *testing.T) {
	// JS: "pathsCross: equal, directory containment either way, glob prefixes"
	t.Run("pathsCross: equal, directory containment either way, glob prefixes", func(t *testing.T) {
		for _, pair := range [][2]string{{"a/b", "a/b"}, {"a", "a/b"}, {"a/b", "a"}, {"a/*", "a/b"}, {"a/b", "a/*"}, {"a/**/x", "a/x"}} {
			if !pathsCross(pair[0], pair[1]) {
				t.Errorf("pathsCross(%q, %q)=false", pair[0], pair[1])
			}
		}
	})
	// JS: "pathsCross: a file-name glob matches the path and the directory relation holds"
	t.Run("pathsCross: a file-name glob matches the path and the directory relation holds", func(t *testing.T) {
		if !pathsCross("internal/*_test.go", "internal/dispatch_test.go") || !pathsCross("internal/foo*.go", "internal/foo/bar.go") {
			t.Fatal("glob match or prefix directory relation was lost")
		}
	})
}

func TestDispatchTM3bBriefSections(t *testing.T) {
	// JS: "pendingBriefPath: the state and the $TMPDIR routing"
	t.Run("pendingBriefPath: the state and the $TMPDIR routing", func(t *testing.T) {
		if got := pendingBriefPath("/state/reports/build.md"); got != "" {
			t.Fatalf("invented pending brief path %q for absent file", got)
		}
	})
	// JS: "composedBriefSection: the # Brief block, absent in an amendment"
	t.Run("composedBriefSection: the # Brief block, absent in an amendment", func(t *testing.T) {
		got := composedBriefSection("# Role\nrole\n# Brief\nbody\n# Report contract\ncontract\n")
		if got != "# Brief\nbody" || composedBriefSection("# Amendment to your current brief\nbody\n") != "" {
			t.Fatalf("brief extraction=%q", got)
		}
	})
	// JS: "pendingBriefSection: an amendment walks back to the agent's newest base brief"
	t.Run("pendingBriefSection: an amendment walks back to the agent's newest base brief", func(t *testing.T) {
		if got := pendingBriefSection("/missing/build-20260929T000000.brief.md"); got != "" {
			t.Fatalf("missing pending source returned %q", got)
		}
	})
	// JS: "dispatch: the overlap check uses the base brief while an amendment is pending"
	t.Run("dispatch: the overlap check uses the base brief while an amendment is pending", func(t *testing.T) {
		base := composedBriefSection("# Brief\n## Owned files\n- internal/base.go\n# Report contract\n")
		if !strings.Contains(base, "internal/base.go") || strings.Contains(composedBriefSection("# Amendment to your current brief\n- internal/amend.go"), "internal/amend.go") {
			t.Fatalf("base brief section selection=%q", base)
		}
	})
}

func TestDispatchTM3bLintAndRunArgumentCases(t *testing.T) {
	// JS: "lint: each missing section carries its own reason, alone and combined"
	t.Run("lint: each missing section carries its own reason, alone and combined", func(t *testing.T) {
		missing := dispatch.BriefMissingSections("# Goal\nDo it.\n", false, nil)
		got := dispatch.MissingSectionsReasons(missing)
		for _, part := range []string{"workers without owned files collide", "nothing says when the slice is done"} {
			if !strings.Contains(got, part) {
				t.Errorf("missing reason %s from %q", part, got)
			}
		}
	})
	// JS: "lint: an alias heading covering the section passes, a mid-title one does not"
	t.Run("lint: an alias heading covering the section passes, a mid-title one does not", func(t *testing.T) {
		aliases, _ := dispatch.ParseBriefLintAliases("Owned files=Touch")
		full := "# Goal\nDo.\n# Expected result\nDone.\n# Touch\n- internal/a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
		if got := dispatch.BriefMissingSections(full, false, aliases); strings.Contains(got, "[Owned files]") {
			t.Fatalf("valid alias remained missing: %q", got)
		}
		if got := dispatch.BriefMissingSections(strings.Replace(full, "# Touch", "# Not Touching", 1), false, aliases); !strings.Contains(got, "[Owned files]") {
			t.Fatalf("mid-title unexpectedly matched: %q", got)
		}
	})
	// JS: "aliases: the parser applies the valid items and warns once per malformed one"
	t.Run("aliases: the parser applies the valid items and warns once per malformed one", func(t *testing.T) {
		aliases, warnings := dispatch.ParseBriefLintAliases("Owned files=Touch|Scope,Bad,Goal=Target")
		if !reflect.DeepEqual(aliases["Owned files"], []string{"Touch", "Scope"}) || len(warnings) != 1 {
			t.Fatalf("aliases=%v warnings=%v", aliases, warnings)
		}
	})
	// JS: "lint: the read-only rule still applies with aliases"
	t.Run("lint: the read-only rule still applies with aliases", func(t *testing.T) {
		aliases, _ := dispatch.ParseBriefLintAliases("Owned files=Touch")
		full := "# Goal\nDo.\n# Expected result\nDone.\n# Touch\n- internal/a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
		if got := dispatch.BriefMissingSections(full, true, aliases); got != "" {
			t.Fatalf("read-only alias brief has missing sections: %q", got)
		}
	})
	// JS: "emptyCodeLines: exactly two backticks, outside fences, once per line"
	t.Run("emptyCodeLines: exactly two backticks, outside fences, once per line", func(t *testing.T) {
		got := dispatch.EmptyCodeLines("`x`\n``\nabc `` xyz\n```\n````\n```go\n``\n~~~\n``\n~~~\n")
		if !reflect.DeepEqual(got, []int{2, 3}) {
			t.Fatalf("empty-code lines=%v", got)
		}
	})
	// JS: "lint: the empty-code warn caps at three lines plus one summary"
	t.Run("lint: the empty-code warn caps at three lines plus one summary", func(t *testing.T) {
		got := dispatch.EmptyCodeLines("``\n``\n``\n``\n``\n")
		if len(got) != 5 {
			t.Fatalf("empty-code detector dropped findings before warning cap: %v", got)
		}
	})
	// JS: "run: --amend is not a run option (exit 2, nothing spawned)"
	t.Run("run: --amend is not a run option (exit 2, nothing spawned)", func(t *testing.T) {
		defer func() {
			value := recover()
			exit, ok := value.(*platform.ExitError)
			if !ok || exit.Code != 2 || exit.Msg != "run: unknown option --amend" {
				t.Fatalf("panic=%#v", value)
			}
		}()
		splitRunArgs([]string{"--amend"})
	})
}

func TestDispatchTM3bAuthScreenCases(t *testing.T) {
	const auth = "Error: 401 Unauthorized: Incorrect API key provided"
	t.Run("dispatch --no-wait: a newly visible auth failure exits 14 with the redacted cause (R11/D58)", func(t *testing.T) {
		// Mutation captured: dropping noWaitObservation's provider-error return changes rc 14 to submitted rc 0.
		f := newDispatchArrivalFixture(t, "blocked", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Call: 2, Stdout: auth + " token=sk-live-AbC123dEf456\n"})
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		value, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
		if err != nil {
			t.Fatalf("code=%d stderr=%q dispatch JSON=%q: %v", code, stderr, out, err)
		}
		obj := value.(*jsonjs.Object)
		cause, _ := obj.Get("cause")
		if code != 14 || dispatchOutputStatus(t, out) != "provider-error" || cause != auth+" token=[redacted]" || !reflect.DeepEqual(obj.Keys(), []string{"wait_status", "agent", "role", "kind", "composed_prompt", "report", "task_report", "report_exists", "auto_approved", "lane", "model", "cause"}) || strings.Contains(stderr, "sk-live-AbC123dEf456") {
			t.Fatalf("code=%d status=%s cause=%v keys=%v stderr=%q out=%s", code, dispatchOutputStatus(t, out), cause, obj.Keys(), stderr, out)
		}
		sidecar, _ := dispatchOutputField(t, out, "composed_prompt")
		data, err := os.ReadFile(strings.TrimSuffix(sidecar, ".md") + ".dispatch.json")
		if err != nil || !strings.Contains(string(data), `"submission":"accepted"`) || strings.Contains(string(data), "arrival") {
			t.Fatalf("accepted attempt sidecar=%q err=%v", data, err)
		}
	})
	t.Run("dispatch --no-wait: a retained auth screen predating the prompt stays submitted (R11/D58)", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "blocked", 1, 2, auth+"\n", "0")
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" || strings.Contains(out, `"cause"`) {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
	})
	t.Run("dispatch --no-wait: a report written by this prompt wins over a new auth screen (R11/D58)", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "blocked", 1, 2, auth+"\n", "0", fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Delay: 40})
		stop, finished := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(finished)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
				for _, call := range calls {
					if len(call.Argv) < 4 || call.Argv[0] != "agent" || call.Argv[1] != "prompt" {
						continue
					}
					const marker = "write your report to "
					_, rest, ok := strings.Cut(call.Argv[len(call.Argv)-1], marker)
					if !ok {
						return
					}
					report, _, _ := strings.Cut(rest, " and reply with exactly that path")
					_ = os.WriteFile(report, []byte("# Report\n\ndone.\n"), 0o600)
					return
				}
				select {
				case <-stop:
					return
				case <-ticker.C:
				}
			}
		}()
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		close(stop)
		<-finished
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" || !strings.Contains(out, `"report_exists":true`) || strings.Contains(out, `"cause"`) {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
	})
	t.Run("dispatch --no-wait: quota wins over auth and returns its own JSON fields (R11/D58)", func(t *testing.T) {
		quota := "Individual quota reached\ntry again in 2 hours\n" + auth
		f := newDispatchArrivalFixture(t, "blocked", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Call: 1, Stdout: quota})
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		value, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
		if err != nil {
			t.Fatalf("code=%d stderr=%q JSON=%q: %v", code, stderr, out, err)
		}
		obj := value.(*jsonjs.Object)
		if code != 11 || dispatchOutputStatus(t, out) != "quota" || !reflect.DeepEqual(obj.Keys(), []string{"wait_status", "agent", "role", "kind", "composed_prompt", "report", "task_report", "report_exists", "auto_approved", "lane", "model", "match", "renewal"}) {
			t.Fatalf("code=%d keys=%v out=%s stderr=%s", code, obj.Keys(), out, stderr)
		}
	})
	t.Run("dispatch --no-wait: a current decision question and transient failure remain submitted (R11/D58)", func(t *testing.T) {
		for _, tc := range []struct{ title, visible, recent string }{
			{"question", "Allow access? [y/N]\nEnter to submit answer, esc to cancel\n" + auth, auth},
			{"transient", "Welcome to the worker\n", "Error: Connection error.\n"},
		} {
			t.Run(tc.title, func(t *testing.T) {
				f := newDispatchArrivalFixture(t, "blocked", 1, 2, "", "0",
					fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Call: 1, Stdout: tc.visible},
					fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Call: 2, Stdout: tc.recent})
				code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
				if code != 0 || dispatchOutputStatus(t, out) != "submitted" || strings.Contains(out, `"cause"`) || strings.Contains(out, `"match"`) {
					t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
				}
			})
		}
	})
	t.Run("dispatch --no-wait: a prompt that never arrives ends not-received despite the auth screen (R11/D58)", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "working", 1, 1, auth, "0")
		f.env["SOHO_WAIT_POLL_MS"] = "1"
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		value, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
		if err != nil {
			t.Fatalf("code=%d stderr=%q JSON=%q: %v", code, stderr, out, err)
		}
		obj := value.(*jsonjs.Object)
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !reflect.DeepEqual(obj.Keys(), []string{"wait_status", "agent", "role", "kind", "composed_prompt", "report", "task_report", "report_exists"}) {
			t.Fatalf("code=%d keys=%v out=%s stderr=%s", code, obj.Keys(), out, stderr)
		}
	})
}

func TestDispatchTM3bWaitResultContract(t *testing.T) {
	capture := func(t *testing.T, last *jsonjs.Object, status string, amend bool) (int, string) {
		t.Helper()
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
		report := filepath.Join(t.TempDir(), "report.md")
		code := emitDispatchResult("build", "implementer", "codex", "/tmp/brief.md", report, "/tmp/task.md", status, amend, last, 0, t.TempDir(), false, false)
		_ = stderr
		return code, out.String()
	}
	// JS: "dispatch: the JSON output is one line with wait_status first (final, error, not-received)"
	t.Run("dispatch: the JSON output is one line with wait_status first (final, error, not-received)", func(t *testing.T) {
		for _, status := range []string{"done", "provider-error", "not-received"} {
			_, out := capture(t, nil, status, false)
			if strings.Count(strings.TrimSpace(out), "\n") != 0 || !strings.HasPrefix(strings.TrimSpace(out), `{"wait_status":"`+status+`"`) {
				t.Errorf("status %s JSON=%q", status, out)
			}
		}
	})
	// JS: "dispatch: a done report with partial items gets the partial key after report_exists"
	t.Run("dispatch: a done report with partial items gets the partial key after report_exists", func(t *testing.T) {
		last := jsonjs.O("status", "done", "partial", 2)
		_, out := capture(t, last, "submitted", false)
		if !strings.Contains(out, `"report_exists":false,"partial":2,"auto_approved":0`) {
			t.Fatalf("partial result order/value=%s", out)
		}
	})
	// JS: "dispatch: a clean done report keeps the key set of today"
	t.Run("dispatch: a clean done report keeps the key set of today", func(t *testing.T) {
		_, out := capture(t, jsonjs.O("status", "done"), "submitted", false)
		if strings.Contains(out, `"partial"`) || !strings.Contains(out, `"wait_status":"done"`) {
			t.Fatalf("clean result keys=%s", out)
		}
	})
	// JS: "dispatch --amend: the amendment report with partial items gets amend then partial"
	t.Run("dispatch --amend: the amendment report with partial items gets amend then partial", func(t *testing.T) {
		_, out := capture(t, jsonjs.O("status", "done", "partial", 1), "submitted", true)
		if !strings.Contains(out, `"report_exists":false,"amend":true,"partial":1`) {
			t.Fatalf("amend/partial result order=%s", out)
		}
	})
	// JS: "dispatch: a blocked worker ends 7 and the final JSON carries the wait dialog"
	t.Run("dispatch: a blocked worker ends 7 and the final JSON carries the wait dialog", func(t *testing.T) {
		code, out := capture(t, jsonjs.O("status", "blocked", "dialog", "question", "question", "Continue?"), "submitted", false)
		if code != 7 || !strings.Contains(out, `"dialog":"question"`) || !strings.Contains(out, `"question":"Continue?"`) {
			t.Fatalf("blocked code=%d output=%s", code, out)
		}
	})
	// JS: "dispatch: a done review report lands verdict, findings and severity before partial"
	t.Run("dispatch: a done review report lands verdict, findings and severity before partial", func(t *testing.T) {
		_, out := capture(t, jsonjs.O("status", "done", "verdict", "pass", "findings", []any{}, "severity", "none", "partial", 1), "submitted", false)
		want := `"report_exists":false,"verdict":"pass","findings":[],"severity":"none","partial":1`
		if !strings.Contains(out, want) {
			t.Fatalf("review fields order=%s", out)
		}
	})
	t.Run("dispatch: a done line with verdict_effective pass lands in the final JSON after the review fields", func(t *testing.T) {
		last := jsonjs.O("status", "done", "verdict", "pass", "findings", 0, "severity", jsonjs.O("P0", 0, "P1", 0, "P2", 0, "P3", 0), "verdict_effective", "pass")
		_, out := capture(t, last, "submitted", false)
		if !strings.Contains(out, `"report_exists":false,"verdict":"pass","findings":0,"severity":{"P0":0,"P1":0,"P2":0,"P3":0},"verdict_effective":"pass","auto_approved":0`) {
			t.Fatalf("verdict_effective order=%s", out)
		}
	})
	t.Run("dispatch: a done line with a fail verdict and verdict_effective fail lands in the final JSON", func(t *testing.T) {
		last := jsonjs.O("status", "done", "verdict", "fail", "findings", 1, "severity", jsonjs.O("P0", 1, "P1", 0, "P2", 0, "P3", 0), "verdict_effective", "fail")
		_, out := capture(t, last, "submitted", false)
		if !strings.Contains(out, `"verdict":"fail"`) || !strings.Contains(out, `"verdict_effective":"fail"`) {
			t.Fatalf("fail result=%s", out)
		}
	})
	t.Run("dispatch: a done line with partial and verdict_effective fail lands in the final JSON after partial", func(t *testing.T) {
		// Mutation captured: dropping verdict_effective from the copied keys
		// loses the field on this line.
		last := jsonjs.O("status", "done", "partial", 1, "verdict_effective", "fail")
		_, out := capture(t, last, "submitted", false)
		if !strings.Contains(out, `"partial":1,"verdict_effective":"fail"`) {
			t.Fatalf("partial result order=%s", out)
		}
	})
	t.Run("dispatch: a done line without review fields carries no verdict_effective", func(t *testing.T) {
		_, out := capture(t, jsonjs.O("status", "done"), "submitted", false)
		if strings.Contains(out, `"verdict_effective"`) {
			t.Fatalf("clean result keys=%s", out)
		}
	})
}

func TestDispatchTM3bSandboxAndSharedTreeCases(t *testing.T) {
	// JS: "sandbox notes: the codex arg combinations, in the brief and the amendment"
	t.Run("sandbox notes: the codex arg combinations, in the brief and the amendment", func(t *testing.T) {
		if got := dispatch.SandboxNotes("codex", ""); len(got) != 2 {
			t.Fatalf("default notes=%v", got)
		}
		if got := dispatch.SandboxNotes("codex", "danger-full-access network_access=true"); len(got) != 0 {
			t.Fatalf("bypass notes=%v", got)
		}
	})
	// JS: "sandbox notes: the real network token and the bypass flag lift their limits"
	t.Run("sandbox notes: the real network token and the bypass flag lift their limits", func(t *testing.T) {
		if got := dispatch.SandboxNotes("codex", "network_access=true"); len(got) != 1 || !strings.Contains(got[0], "cannot write under .git") {
			t.Fatalf("network token notes=%v", got)
		}
		if got := dispatch.SandboxNotes("codex", "--dangerously-bypass-approvals-and-sandbox"); len(got) != 0 {
			t.Fatalf("bypass flag notes=%v", got)
		}
	})
	// JS: "dispatch: the composed prompt of a codex worker carries the sandbox notes"
	t.Run("dispatch: the composed prompt of a codex worker carries the sandbox notes", func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "implementer.md")
		if err := os.WriteFile(role, []byte("---\nname: Implementer\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := dispatch.ComposePrompt(role, "implementer", "build", "brief", "/tmp/report.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{"SOHO_SKILL_DIR": t.TempDir()}, "codex", "", "", false)
		if !strings.Contains(got, "tests that start a local server fail") {
			t.Fatalf("composed prompt omitted sandbox note: %s", got)
		}
	})
	// JS: "dispatch: a live same-cwd edit agent adds the shared-tree line (D25)"
	t.Run("dispatch: a live same-cwd edit agent adds the shared-tree line (D25)", func(t *testing.T) {
		rows := []string{"worker\tp1\tcodex\timplementer\topenai\t0\t/work\tnow\tgpt-5\ttask\timplementer"}
		live := []any{jsonjs.O("name", "worker", "pane_id", "p1")}
		if !sharedTreeEditor(rows, live, "build", "/work", platform.Env{}, "/work") {
			t.Fatal("live same-cwd edit agent was not recognized")
		}
	})
}

func TestDispatchTM3bPendingBaseAndAmendExclusion(t *testing.T) {
	// JS: "pendingBriefSection: an amendment walks back to the agent's newest base brief"
	t.Run("pendingBriefSection: an amendment walks back to the agent's newest base brief", func(t *testing.T) {
		dir := t.TempDir()
		base := filepath.Join(dir, "build-20260928T120000.md")
		amend := filepath.Join(dir, "build-20260928T120001.md")
		if err := os.WriteFile(base, []byte("# Brief\nbase work\n# Report contract\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(amend, []byte("# Amendment to your current brief\nnew work\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := pendingBriefSection(amend); got != "# Brief\nbase work" {
			t.Fatalf("base section=%q", got)
		}
	})
	// JS: "dispatch: --amend does not run the owned-files overlap check"
	t.Run("dispatch: --amend does not run the owned-files overlap check", func(t *testing.T) {
		// Amendments are deltas; their own text is not a base owned-files section.
		if got := composedBriefSection("# Amendment to your current brief\n## Owned files\n- internal/new.go\n"); got != "" {
			t.Fatalf("amendment was interpreted as a composed base brief: %q", got)
		}
	})
}

type tm3bHarness struct {
	root, state, ws, role, brief string
	env                          platform.Env
	ctx                          *core.Config
}

func newTM3bHarness(t *testing.T, briefText string) tm3bHarness {
	t.Helper()
	root := t.TempDir()
	return newTM3bHarnessAt(t, root, briefText)
}

func newTM3bHarnessAt(t *testing.T, root, briefText string) tm3bHarness {
	t.Helper()
	state := filepath.Join(root, "state")
	ws := filepath.Join(state, "ws")
	roles := filepath.Join(root, ".agents", "herdr-roles")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{ws, roles, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	role := filepath.Join(roles, "implementer.md")
	if err := os.WriteFile(role, []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte(briefText), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"build\tw0test:p0a\tcodex\timplementer\topenai\t0\t" + root + "\tnow\tgpt-5\ttask\timplementer\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(ws, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{AnyArgs: true, Stdout: `{"result":{"agents":[],"sent":true}}`}})
	if err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, item := range testutil.CleanEnv(t) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	env["PATH"] = bin
	// The user's own config (~/.config/soho/config) must not reach the
	// harness: a context_window.grok there changes the spawn's herdr calls.
	env["HOME"] = filepath.Join(root, "home")
	env["USERPROFILE"] = filepath.Join(root, "home")
	env["XDG_CONFIG_HOME"] = filepath.Join(root, "xdg")
	env["SOHO_FAKECLI_CONFIG"] = filepath.Join(bin, "herdr.json")
	env["SOHO_DIR"] = state
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["SOHO_SKILL_DIR"] = testSkillDir(t)
	env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	env["SOHO_BRIEF_LINT"] = "off"
	env["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing", "herdr.sock")
	// A race-instrumented fake child otherwise pauses for one second on
	// exit; the harness sets only GORACE and leaves the rest of the env as built.
	env["GORACE"] = fakecli.RaceOptions(env.List())
	return tm3bHarness{root: root, state: state, ws: ws, role: role, brief: brief, env: env, ctx: &core.Config{Entries: map[string]core.ConfigEntry{}}}
}

func (h tm3bHarness) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := cmdDispatch(args, h.ctx, h.env, h.root)
	return code, out.String(), stderr.String()
}

func tm3bFullBrief() string {
	return "# Brief — fix the config\n\n# Goal\nDo the work.\n\n# Expected result\nA result.\n\n# Owned files\ninternal/a.go\n\n# Forbidden\nNo commit/push.\n\n# Report\nWrite a report.\n"
}

func TestDispatchTM3bAmendCommandCases(t *testing.T) {
	// JS: "dispatch --amend: new report, wait markers cleared, title keeps the task without the check mark, no lint"
	t.Run("dispatch --amend: new report, wait markers cleared, title keeps the task without the check mark, no lint", func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		if code, _, _ := h.run(t, "build", h.brief, "--no-wait"); code != 0 {
			t.Fatalf("first dispatch code=%d", code)
		}
		if err := os.WriteFile(filepath.Join(h.ws, "task-build"), []byte("implementer: fix the config ✓\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.ws, "wait", "build.question"), []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		amend := filepath.Join(h.root, "amend.md")
		if err := os.WriteFile(amend, []byte("# Amend — use the right flag\nDo X.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := h.run(t, "build", amend, "--amend", "--no-wait")
		if code != 0 {
			t.Fatalf("amend code=%d stderr=%s", code, stderr)
		}
		v, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
		if err != nil {
			t.Fatal(err)
		}
		obj := v.(*jsonjs.Object)
		for key, want := range map[string]any{"amend": true, "wait_status": "submitted", "report_exists": false} {
			got, _ := obj.Get(key)
			if got != want {
				t.Errorf("%s=%v want %v", key, got, want)
			}
		}
		if got, _ := os.ReadFile(filepath.Join(h.ws, "task-build")); string(got) != "implementer: fix the config\n" {
			t.Fatalf("task=%q", got)
		}
		if _, err := os.Stat(filepath.Join(h.ws, "wait", "build.question")); !os.IsNotExist(err) {
			t.Fatalf("old wait marker remains: %v", err)
		}
		if strings.Contains(stderr, "missing sections") {
			t.Fatalf("amend linted as a full brief: %s", stderr)
		}
	})
	// JS: "dispatch --amend: without a task file the title comes from the amendment file"
	t.Run("dispatch --amend: without a task file the title comes from the amendment file", func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		if err := os.WriteFile(filepath.Join(h.ws, "last-report-build"), []byte("/previous/report.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		amend := filepath.Join(h.root, "amend.md")
		if err := os.WriteFile(amend, []byte("# Amend — retry with the right flag\nDo it.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _, errText := h.run(t, "build", amend, "--amend", "--no-wait"); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errText)
		}
		if got, _ := os.ReadFile(filepath.Join(h.ws, "task-build")); string(got) != "implementer: Amend — retry with the right flag\n" {
			t.Fatalf("task=%q", got)
		}
	})
	// JS: "dispatch --amend: without an earlier dispatch it exits 2 (exact text)"
	t.Run("dispatch --amend: without an earlier dispatch it exits 2 (exact text)", func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		defer func() {
			v := recover()
			x, ok := v.(*platform.ExitError)
			if !ok || x.Code != 2 || !strings.Contains(x.Msg, "nothing to amend") {
				t.Fatalf("panic=%#v", v)
			}
		}()
		h.run(t, "build", h.brief, "--amend", "--no-wait")
	})
	t.Run("dispatch --amend accepts --role on an open task", func(t *testing.T) {
		h := newTM3bHarness(t, strings.Replace(tm3bFullBrief(), "# Owned files\ninternal/a.go\n\n", "", 1))
		writeRoleFixture(t, filepath.Join(filepath.Dir(h.role), "reviewer.md"), "---\nname: Reviewer\nmode: read-only\n---\nReview.\n")
		if code, out, msg := h.run(t, "build", h.brief, "--no-wait"); code != 0 {
			t.Fatalf("initial exit=%d out=%s stderr=%s", code, out, msg)
		}
		code, out, msg := h.run(t, "build", h.brief, "--amend", "--role", "reviewer", "--no-wait")
		if code != 0 || !strings.Contains(out, `"role":"reviewer"`) || strings.Contains(out, "duplicate_of") {
			t.Fatalf("amend exit=%d out=%s stderr=%s", code, out, msg)
		}
	})
}

func TestDispatchTM3bStrictLintOverlapAndMarkerCases(t *testing.T) {
	// JS: "dispatch: a strict lint dies 2 without creating the state dirs"
	t.Run("dispatch: a strict lint dies 2 without creating the state dirs", func(t *testing.T) {
		briefText := strings.Replace(tm3bFullBrief(), "# Owned files\ninternal/a.go\n\n", "", 1)
		h := newTM3bHarness(t, briefText)
		h.env["SOHO_BRIEF_LINT"] = "strict"
		var recovered any
		func() { defer func() { recovered = recover() }(); h.run(t, "build", h.brief, "--no-wait") }()
		exit, ok := recovered.(*platform.ExitError)
		if !ok || exit.Code != 2 || !strings.Contains(exit.Msg, "Owned files") {
			t.Fatalf("strict lint panic=%#v", recovered)
		}
		for _, name := range []string{"briefs", "reports", "wait"} {
			if _, err := os.Stat(filepath.Join(h.ws, name)); !os.IsNotExist(err) {
				t.Errorf("state directory %s exists before strict lint rejection: %v", name, err)
			}
		}
	})
	// JS: "dispatch: the owned-files overlap warn (edit, read-only, the cap of 5, settled reports)"
	t.Run("dispatch: the owned-files overlap warn (edit, read-only, the cap of 5, settled reports)", func(t *testing.T) {
		briefWithMany := strings.Replace(tm3bFullBrief(), "internal/a.go", "- internal/a.go\n- internal/b.go\n- internal/c.go\n- internal/d.go\n- internal/e.go\n- internal/f.go", 1)
		h := newTM3bHarness(t, briefWithMany)
		if err := os.MkdirAll(filepath.Join(h.ws, "briefs"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(h.ws, "reports"), 0o700); err != nil {
			t.Fatal(err)
		}
		pending := filepath.Join(h.ws, "briefs", "peer.md")
		owned := []string{"internal/a.go", "internal/b.go", "internal/c.go", "internal/d.go", "internal/e.go", "internal/f.go"}
		body := "# Brief\n# Owned files\n"
		for _, p := range owned {
			body += "- " + p + "\n"
		}
		body += "# Report contract\n"
		if err := os.WriteFile(pending, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(h.ws, "reports", "peer.md")
		if err := os.WriteFile(filepath.Join(h.ws, "last-report-peer"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "peer\tw0test:p0b\tcodex\timplementer\topenai\t0\t" + h.root + "\tnow\tgpt-5\ttask\timplementer\n"
		if err := os.WriteFile(filepath.Join(h.ws, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		oldErr := platform.Stderr
		var stderr bytes.Buffer
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		warnOwnedOverlap(h.brief, "implementer", "build", h.ws, h.ctx, h.env, h.root, h.root)
		if !strings.Contains(stderr.String(), "internal/a.go, internal/b.go, internal/c.go, internal/d.go, internal/e.go") || strings.Contains(stderr.String(), "internal/f.go") {
			t.Fatalf("overlap warning cap/output=%q", stderr.String())
		}
	})
	t.Run("dispatch: --amend does not run the owned-files overlap check", func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		if code, _, _ := h.run(t, "build", h.brief, "--no-wait"); code != 0 {
			t.Fatalf("initial dispatch code=%d", code)
		}
		pending := filepath.Join(h.ws, "briefs", "other-pending.md")
		if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pending, []byte("# Brief\n## Owned files\n- internal/a.go\n# Report contract\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		otherReport := filepath.Join(h.ws, "reports", "other-pending.md")
		if err := os.WriteFile(filepath.Join(h.ws, "last-report-other"), []byte(otherReport+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rosterPath := filepath.Join(h.ws, "agents.tsv")
		roster, err := os.ReadFile(rosterPath)
		if err != nil {
			t.Fatal(err)
		}
		rows := strings.Split(strings.TrimSpace(string(roster)), "\n")
		var workerDir string
		for _, row := range rows {
			cols := strings.Split(row, "\t")
			if len(cols) > 6 && cols[0] == "build" {
				workerDir = cols[6]
			}
		}
		other := "other\tw0test:p0b\tcodex\timplementer\topenai\t0\t" + workerDir + "\tnow\tgpt-5\ttask\timplementer\n"
		if err := os.WriteFile(rosterPath, append(roster, []byte(other)...), 0o600); err != nil {
			t.Fatal(err)
		}

		amend := filepath.Join(h.root, "amend.md")
		if err := os.WriteFile(amend, []byte("# Amend\n## Owned files\n- internal/a.go\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var positive bytes.Buffer
		oldErr := platform.Stderr
		platform.Stderr = &positive
		warnOwnedOverlap(amend, "implementer", "build", h.ws, h.ctx, h.env, h.root, workerDir)
		platform.Stderr = oldErr
		if !strings.Contains(positive.String(), "is still editing") {
			t.Fatalf("overlap subject is not an effective positive control: %q", positive.String())
		}

		code, _, stderr := h.run(t, "build", amend, "--amend", "--no-wait")
		if code != 0 || strings.Contains(stderr, "owns files") || strings.Contains(stderr, "is still editing") {
			t.Fatalf("amend overlap check code=%d stderr=%s", code, stderr)
		}
	})
	// JS: "dispatch: the owned-files overlap warn across the $TMPDIR routing"
	t.Run("dispatch --no-wait: the owned-files overlap warning resolves the pending brief in $TMPDIR", func(t *testing.T) {
		tmp := t.TempDir()
		briefText := strings.Replace(tm3bFullBrief(), "internal/a.go", "- scripts/x.mjs", 1)
		h := newTM3bHarness(t, briefText)
		h.env["TMPDIR"] = tmp
		reports := filepath.Join(tmp, "soho", "ws", "reports")
		pending := filepath.Join(reports, "other.brief.md")
		if err := os.MkdirAll(reports, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pending, []byte("# Brief\n## Owned files\n- scripts/x.mjs\n# Report contract\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		otherReport := filepath.Join(reports, "other.md")
		if err := os.WriteFile(filepath.Join(h.ws, "last-report-other"), []byte(otherReport+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		workerCwd := t.TempDir()
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
			"other\tw0test:p0b\tcodex\timplementer\topenai\t0\t" + workerCwd + "\tnow\tgpt-5\ttask\timplementer\t\t\t\t\n" +
			"build\tw0test:p0a\tcodex\timplementer\topenai\t0\t" + workerCwd + "\tnow\tgpt-5\ttask\timplementer\t\t\t\t\n"
		if err := os.WriteFile(filepath.Join(h.ws, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := h.run(t, "build", h.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) || !strings.Contains(stderr, "owns files that 'other' is still editing: scripts/x.mjs") {
			t.Fatalf("dispatch code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	// JS: "dispatch: the owned-files overlap warn only fires inside the same tree"
	t.Run("dispatch: the owned-files overlap warn only fires inside the same tree", func(t *testing.T) {
		tmp := t.TempDir()
		briefText := strings.Replace(tm3bFullBrief(), "internal/a.go", "- scripts/x.mjs", 1)
		h := newTM3bHarness(t, briefText)
		h.env["TMPDIR"] = tmp
		reports := filepath.Join(tmp, "soho", "ws", "reports")
		if err := os.MkdirAll(reports, 0o700); err != nil {
			t.Fatal(err)
		}
		body := "# Brief\n## Owned files\n- scripts/x.mjs\n# Report contract\n"
		for _, name := range []string{"other.brief.md", "same.brief.md"} {
			if err := os.WriteFile(filepath.Join(reports, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"other", "same"} {
			if err := os.WriteFile(filepath.Join(h.ws, "last-report-"+name), []byte(filepath.Join(reports, name+".md")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		otherCwd, workerCwd := t.TempDir(), t.TempDir()
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
			"other\tw0test:p0b\tcodex\timplementer\topenai\t0\t" + otherCwd + "\tnow\tgpt-5\ttask\timplementer\t\t\t\t\n" +
			"same\tw0test:p0c\tcodex\timplementer\topenai\t0\t" + workerCwd + "\tnow\tgpt-5\ttask\timplementer\t\t\t\t\n" +
			"build\tw0test:p0a\tcodex\timplementer\topenai\t0\t" + workerCwd + "\tnow\tgpt-5\ttask\timplementer\t\t\t\t\n"
		if err := os.WriteFile(filepath.Join(h.ws, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := h.run(t, "build", h.brief, "--no-wait")
		if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("dispatch code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(stderr, "owns files that 'same' is still editing: scripts/x.mjs") {
			t.Fatalf("same-tree overlap warning missing: %s", stderr)
		}
		if strings.Contains(stderr, "owns files that 'other' is still editing") {
			t.Fatalf("different-tree overlap warning fired: %s", stderr)
		}
	})
	// JS: "dispatch: a new dispatch clears the approve-screen marker"
	t.Run("dispatch: a new dispatch clears the approve-screen marker", func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		if err := os.MkdirAll(filepath.Join(h.ws, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(h.ws, "wait", "build.approve-screen")
		if err := os.WriteFile(marker, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _, stderr := h.run(t, "build", h.brief, "--no-wait"); code != 0 {
			t.Fatalf("dispatch code=%d stderr=%s", code, stderr)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("approve marker remains: %v", err)
		}
	})
	// JS: "dispatch: a mid-wait amendment that re-points the report settles on it (D39)"
	t.Run("dispatch: a mid-wait amendment that re-points the report settles on it (D39)", func(t *testing.T) {
		// Mutation captured: dropping settled_report or qualifying report_exists against report instead of the settled path breaks this decoded JSON contract.
		settled := filepath.Join(t.TempDir(), "amended-report.md")
		if err := os.WriteFile(settled, []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		last := jsonjs.O("status", "done", "report", settled)
		_, out := func() (int, string) {
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var b, e bytes.Buffer
			platform.Stdout, platform.Stderr = &b, &e
			defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
			code := emitDispatchResult("build", "implementer", "codex", "/tmp/brief.md", "/tmp/old-report.md", "/tmp/task.md", "submitted", true, last, 0, t.TempDir(), false, false)
			return code, b.String()
		}()
		value, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
		if err != nil {
			t.Fatalf("dispatch result JSON %q: %v", out, err)
		}
		obj := value.(*jsonjs.Object)
		got, _ := obj.Get("settled_report")
		exists, _ := obj.Get("report_exists")
		if got != settled || exists != true {
			t.Fatalf("settled report was not selected: got=%v exists=%v output=%s", got, exists, out)
		}
		windowsSettled := `C:\work\amended-report.md`
		_, out = func() (int, string) {
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var b, e bytes.Buffer
			platform.Stdout, platform.Stderr = &b, &e
			defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
			last := jsonjs.O("status", "done", "report", windowsSettled)
			code := emitDispatchResult("build", "implementer", "codex", `C:\work\brief.md`, `C:\work\old-report.md`, `C:\work\task.md`, "submitted", true, last, 0, t.TempDir(), false, false)
			return code, b.String()
		}()
		value, err = jsonjs.Parse([]byte(strings.TrimSpace(out)))
		if err != nil {
			t.Fatalf("Windows-shaped dispatch JSON %q: %v", out, err)
		}
		obj = value.(*jsonjs.Object)
		got, _ = obj.Get("settled_report")
		exists, _ = obj.Get("report_exists")
		if got != windowsSettled || exists != false {
			t.Fatalf("Windows-shaped settled path was not preserved: got=%v exists=%v output=%s", got, exists, out)
		}
	})
	// JS: "run: the last stdout line is the one-line dispatch JSON"
	t.Run("run: the last stdout line is the one-line dispatch JSON", func(t *testing.T) {
		_, out := func() (int, string) {
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var b, e bytes.Buffer
			platform.Stdout, platform.Stderr = &b, &e
			defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
			code := emitDispatchResult("build", "implementer", "codex", "/tmp/brief.md", filepath.Join(t.TempDir(), "report.md"), "/tmp/task.md", "submitted", false, nil, 0, t.TempDir(), false, false)
			return code, b.String()
		}()
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 1 || !strings.HasPrefix(lines[len(lines)-1], `{"wait_status":"submitted"`) {
			t.Fatalf("stdout=%q", out)
		}
	})
}

func TestDispatchAttemptSidecarFailureWarning(t *testing.T) { // Mutation captured: using the prompt-submission warning for a sidecar write failure tells operators a prompt failed when it was never sent.
	h := newTM3bHarness(t, tm3bFullBrief())
	fixedNow := platform.Now
	platform.Now = func() time.Time { return time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { platform.Now = fixedNow })
	sidecar := filepath.Join(h.ws, "briefs", "build-20260929T100000.dispatch.json")
	if err := os.MkdirAll(sidecar, 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := h.run(t, "build", h.brief, "--no-wait")
	if code != 4 || !strings.Contains(out, `"wait_status":"error"`) || !strings.Contains(out, "couldn't write the attempt sidecar") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !strings.Contains(stderr, "could not write the attempt sidecar "+sidecar+":") || strings.Contains(stderr, "prompt submission failed") {
		t.Fatalf("sidecar failure warning=%q", stderr)
	}
	callsPath := filepath.Join(h.root, "bin", "herdr.calls.jsonl")
	if calls, err := fakecli.ReadCalls(callsPath); err == nil {
		if got := countDispatchCalls(calls, "prompt"); got != 0 {
			t.Fatalf("prompt calls=%d; sidecar failed before submission", got)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestDispatchLiveAgentsFailureMatchesJavaScriptCapture(t *testing.T) { // Mutation captured: removing the shared-tree liveAgents catch aborts dispatch instead of skipping only the advisory note.
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		root = filepath.Join(root, `Users\operator\U`)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	h := newTM3bHarnessAt(t, root, tm3bFullBrief())
	listResponse, err := json.Marshal(map[string]any{"result": map[string]any{"agents": []any{}, "sent": true}})
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{
		Log: filepath.Join(h.root, "bin", "herdr.calls.jsonl"),
		Rules: []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stderr: "live-list-failed\n", Code: 3},
			{AnyArgs: true, Stdout: string(listResponse) + "\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && !strings.Contains(string(config), `\\U`) {
		t.Fatalf("fakecli config did not JSON-escape the simulated Windows path: %s", config)
	}
	if err := os.WriteFile(filepath.Join(h.root, "bin", "herdr.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := h.run(t, "build", h.brief, "--no-wait")
	if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) || !strings.Contains(stderr, "live-list-failed\n") {
		calls, _ := fakecli.ReadCalls(filepath.Join(h.root, "bin", "herdr.calls.jsonl"))
		t.Fatalf("code=%d out=%q stderr=%q calls=%#v", code, out, stderr, calls)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(h.root, "bin", "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	listCall, promptCall := -1, -1
	for i, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "list" {
			listCall = i
		}
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
			promptCall = i
		}
	}
	if listCall < 0 || promptCall <= listCall {
		t.Fatalf("agent list / prompt ordering=%d/%d calls=%#v", listCall, promptCall, calls)
	}
}
