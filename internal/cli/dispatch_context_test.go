package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchContextPercentReadsFooter(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   int
	}{
		{
			name:   "the real footer line reads its percent",
			screen: "   /Users/djalmajr/Developer/djalmajr/herdr-soho/.worktrees/s19       161.3K (62%)  ctrl+p commands",
			want:   62,
		},
		{
			name:   "a screen without the footer line has no reading",
			screen: "some output\nanother line\n",
			want:   -1,
		},
		{
			name:   "a marker line without a K (NN%) usage has no reading",
			screen: "   /Users/djalmajr/project  ctrl+p commands",
			want:   -1,
		},
		{
			name:   "a usage without the K token has no reading",
			screen: "   /Users/djalmajr/project  161.3 (62%)  ctrl+p commands",
			want:   -1,
		},
		{
			name:   "an integer K usage still reads",
			screen: "   /Users/djalmajr/project  34K (99%)  ctrl+p commands",
			want:   99,
		},
		{
			name:   "trailing blank lines still read",
			screen: "   /Users/djalmajr/project  12K (75%)  ctrl+p commands\n\n",
			want:   75,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchContextPercent(tc.screen); got != tc.want {
				t.Fatalf("dispatchContextPercent()=%d want %d", got, tc.want)
			}
		})
	}
}

func runContextWarnDispatch(t *testing.T, kind, screen string, amend bool, envExtra platform.Env, argvExtra []string) (int, string, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	roles := filepath.Join(root, "roles")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{roles, bin, filepath.Join(state, "ws")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roleFile := filepath.Join(roles, "implementer.md")
	if err := os.WriteFile(roleFile, []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte("# Brief — dispatch sample\n\nGoal: run it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tp1\t" + kind + "\timplementer\topenai\t0\t" + root + "\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		{Argv: []string{"pane", "title"}, ArgvPrefix: true, Code: 1},
	}
	if _, err := fakecli.Install(t, bin, "herdr", calls); err != nil {
		t.Fatal(err)
	}
	env := contextWarnDispatchEnv(t, bin, state, roles, root)
	for k, v := range envExtra {
		env[k] = v
	}
	// A race-instrumented fake child otherwise pauses for one second on
	// exit; the harness sets only GORACE and leaves the rest of the env as built.
	env["GORACE"] = fakecli.RaceOptions(env.List())
	if amend {
		if err := os.WriteFile(filepath.Join(state, "ws", "last-report-worker"), []byte(filepath.Join(state, "ws", "reports", "worker-old.md")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := cmdDispatch(append([]string{"worker", brief}, argvExtra...), ctx, env, root)
	return code, out.String(), stderr.String()
}

func TestDispatchContextWarn(t *testing.T) {
	footer62 := "   /Users/djalmajr/project       161.3K (62%)  ctrl+p commands"
	footer60 := "   /Users/djalmajr/project       150.0K (60%)  ctrl+p commands"
	footer40 := "   /Users/djalmajr/project       97.5K (40%)  ctrl+p commands"
	const wantWarn = "dispatch: 'worker' is at 62% of its context; a long context can end its turn empty (opencode): release --close it and spawn a fresh worker for a new task"

	t.Run("an opencode at the default threshold warns and the prompt still goes out", func(t *testing.T) {
		code, out, stderr := runContextWarnDispatch(t, "opencode", footer62, false, nil, []string{"--no-wait"})
		if code != 0 {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, out, stderr)
		}
		if !strings.Contains(stderr, wantWarn) {
			t.Fatalf("no context warning on stderr: %s", stderr)
		}
		if !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("the prompt was not sent: %s", out)
		}
	})

	t.Run("an opencode exactly at the threshold warns (the bound is >=)", func(t *testing.T) {
		code, out, stderr := runContextWarnDispatch(t, "opencode", footer60, false, nil, []string{"--no-wait"})
		if code != 0 {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, out, stderr)
		}
		if !strings.Contains(stderr, "dispatch: 'worker' is at 60% of its context") {
			t.Fatalf("no context warning at the threshold on stderr: %s", stderr)
		}
		if !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("the prompt was not sent: %s", out)
		}
	})

	t.Run("an opencode below the threshold does not warn", func(t *testing.T) {
		code, out, stderr := runContextWarnDispatch(t, "opencode", footer40, false, nil, []string{"--no-wait"})
		if code != 0 {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, out, stderr)
		}
		if strings.Contains(stderr, "of its context") {
			t.Fatalf("unexpected context warning: %s", stderr)
		}
		if !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("the prompt was not sent: %s", out)
		}
	})

	t.Run("context_warn_percent=0 turns the warning off", func(t *testing.T) {
		code, out, stderr := runContextWarnDispatch(t, "opencode", footer62, false, platform.Env{"SOHO_CONTEXT_WARN_PERCENT": "0"}, []string{"--no-wait"})
		if code != 0 {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, out, stderr)
		}
		if strings.Contains(stderr, "of its context") {
			t.Fatalf("unexpected context warning: %s", stderr)
		}
		if !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("the prompt was not sent: %s", out)
		}
	})

	t.Run("a kind other than opencode never warns", func(t *testing.T) {
		code, out, stderr := runContextWarnDispatch(t, "codex", footer62, false, nil, []string{"--no-wait"})
		if code != 0 {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, out, stderr)
		}
		if strings.Contains(stderr, "of its context") {
			t.Fatalf("unexpected context warning: %s", stderr)
		}
		if !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("the prompt was not sent: %s", out)
		}
	})

	t.Run("an amendment of a high-context opencode warns too", func(t *testing.T) {
		code, out, stderr := runContextWarnDispatch(t, "opencode", footer62, true, nil, []string{"--amend", "--no-wait"})
		if code != 0 {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, out, stderr)
		}
		if !strings.Contains(stderr, wantWarn) {
			t.Fatalf("no context warning on stderr: %s", stderr)
		}
		if !strings.Contains(out, `"wait_status":"submitted"`) {
			t.Fatalf("the amendment was not sent: %s", out)
		}
	})
}

func contextWarnDispatchEnv(t *testing.T, bin, state, roles, root string) platform.Env {
	t.Helper()
	return isolatedUserEnv(platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "HERDR_ENV": "1", "SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws", "SOHO_ROLES": roles, "SOHO_SKILL_DIR": testSkillDir(t), "SOHO_PROMPT_CHECK_SECONDS": "0", "SOHO_PROMPT_SETTLE_SECONDS": "0", "SOHO_BRIEF_LINT": "off", "TMPDIR": root})
}
