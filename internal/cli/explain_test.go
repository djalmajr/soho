package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestExplainTextAndRecommendation(t *testing.T) {
	t.Run("explainRecommendation: the 2, 3 and 4 panel texts and the lanes-off note", func(t *testing.T) { // JS: "explainRecommendation: the 2, 3 and 4 panel texts and the lanes-off note"
		// Mutation captured: replacing the 2-panel recommendation or omitting the lanes-off note changes the asserted output.
		env := platform.Env{"SOHO_SKILL_DIR": testSkillDir(t), "HERDR_WORKSPACE_ID": "ws"}
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeExplain := func(conf string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte(conf), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want := []string{
			"Recommendation: 2 panels - one writes code (research included) and the review happens here, from another model family. The lightest choice. 3 panels add a reviewer panel.",
			"Recommendation: 3 panels - one writes code (research included) and one reviews. Lighter on quota. 4 panels add a second writer; with 2 panels the review happens here.",
			"Recommendation: 4 panels - two write code (research included) in parallel and one reviews. Uses more quota. 3 panels are lighter: one writes and one reviews. With 2 panels one writes and the review happens here.",
		}
		for i, panes := range []string{"2", "3", "4"} {
			conf := "panes=" + panes + "\nlane.build.kind=grok\n"
			if panes != "2" {
				conf += "lane.review.kind=codex\n"
			}
			if panes == "4" {
				conf += "lane.review.model=gpt-5\n"
			}
			writeExplain(conf)
			ctx := core.LoadConfig(env, repo)
			rec := explainRecommendation(&ctx, env)
			if len(rec) == 0 || rec[0] != want[i] || !containsLocal(rec, "Chosen for build: grok.") {
				t.Fatalf("panes=%s recommendation=%#v", panes, rec)
			}
			if panes == "3" && !containsLocal(rec, "Chosen for review: codex.") {
				t.Fatalf("3-panel recommendation=%#v", rec)
			}
			if panes == "4" && !containsLocal(rec, "Chosen for review: codex, model gpt-5.") {
				t.Fatalf("4-panel recommendation=%#v", rec)
			}
		}
		writeExplain("lanes=off\n")
		ctx := core.LoadConfig(env, repo)
		rec := explainRecommendation(&ctx, env)
		if !containsLocal(rec, "Each agent keeps its own assistant instead of sharing one panel.") {
			t.Fatalf("lanes-off recommendation=%#v", rec)
		}
	})
	t.Run("explainPrintRunning: the panel count, the preset order and the idle paragraph", func(t *testing.T) { // JS: "explainPrintRunning: the panel count, the preset order and the idle paragraph"
		env := platform.Env{"SOHO_SKILL_DIR": testSkillDir(t), "HERDR_WORKSPACE_ID": "ws"}
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte("panes=4\nlane.build.kind=grok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := core.LoadConfig(env, repo)
		rows := []explainRow{
			{lane: "build", role: "implementer", kind: "grok", model: "grok-4.7", activity: "working"},
			{lane: "review", role: "reviewer", kind: "codex", model: "gpt-5", activity: "idle"},
		}
		lines := explainPrintRunning(rows, "", &ctx, env, "")
		for _, line := range []string{"Panels: 4.", "build: implementer, grok, model grok-4.7, working", "review: reviewer, codex, model gpt-5, idle"} {
			if !containsLocal(lines, line) {
				t.Fatalf("running explanation missing %q: %#v", line, lines)
			}
		}
		if got := strings.Join(explainIdleParagraph(), "\n"); !strings.Contains(got, "Nothing is running yet.") || !strings.Contains(got, "Four panels are recommended:") {
			t.Fatalf("idle text changed: %s", got)
		}
	})
}

func TestExplainCommandRosterActivity(t *testing.T) {
	t.Run("parity: explain idle, roster, waiting-for-report and quota (test-friendly.sh)", func(t *testing.T) { // JS: "parity: explain idle, roster, waiting-for-report and quota (test-friendly.sh)"
		// Mutation captured: forcing explain activities to idle hides waiting-for-report and out-of-quota rows.
		env, cwd := tm6CLIEnv(t)
		bin := env.Get("PATH")
		if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
			{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "review"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "review", "--source", "visible", "--lines", "20"}},
			{Argv: []string{"agent", "get", "queued"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "queued", "--source", "visible", "--lines", "20"}},
			{Argv: []string{"agent", "get", "capped"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "capped", "--source", "visible", "--lines", "20"}, Stdout: "Individual quota reached\n"},
		}); err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, bin)
		sd := filepath.Join(env.Get("SOHO_DIR"), env.Get("HERDR_WORKSPACE_ID"))
		check := func(want ...string) {
			t.Helper()
			code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
			}
			for _, text := range want {
				if !strings.Contains(out, text) {
					t.Fatalf("explain output missing %q: %s", text, out)
				}
			}
		}
		check("Nothing is running yet.", "never commit", "Four panels are recommended")
		tm6Write(t, filepath.Join(sd, "agents.tsv"), "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"+
			"build\tp1\tgrok\timplementer\txai\t1\t/work\tt1\tgrok-4.7\tfull\timplementer\tbuild\n"+
			"review\tp2\tcodex\treviewer\topenai\t1\t/work\tt2\tgpt-5\ttask\treviewer\treview\n")
		check("build: implementer, grok, model grok-4.7, working", "review: reviewer, codex, model gpt-5, idle", "Panels: 4.", "Recommendation: 4 panels")
		tm6Write(t, filepath.Join(sd, "agents.tsv"), "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nqueued\tp1\tgrok\timplementer\txai\t1\t/work\tt1\tgrok-4.7\tfull\timplementer\tbuild\n")
		tm6Write(t, filepath.Join(sd, "last-report-queued"), filepath.Join(sd, "reports", "queued.md")+"\n")
		check("build: implementer, grok, model grok-4.7, waiting for report")
		tm6Write(t, filepath.Join(sd, "agents.tsv"), "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"+
			"capped\tp2\tcodex\treviewer\topenai\t1\t/work\tt2\tgpt-5\ttask\treviewer\treview\n")
		_ = os.Remove(filepath.Join(sd, "last-report-capped"))
		check("review: reviewer, codex, model gpt-5, out of quota")
	})
	t.Run("parity: explain --json dies 2 (test-friendly.sh)", func(t *testing.T) { // JS: "parity: explain --json dies 2 (test-friendly.sh)"
		root := t.TempDir()
		env := platform.Env{"SOHO_SKILL_DIR": testSkillDir(t), "SOHO_DIR": filepath.Join(root, "state"), "HERDR_WORKSPACE_ID": "ws", "HOME": root}
		code, out, stderr := runIn(t, []string{"explain", "--json"}, env, root)
		if code != 2 || out != "" || !strings.Contains(stderr, "explain: takes no arguments") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
}

func containsLocal(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
