package cli

import (
	"strings"
	"testing"
)

func TestCommandHelpFirstPositionOnly(t *testing.T) {
	env, cwd := commandFixture(t)

	const spawnHelp = "  soho spawn <role> [--name N] [--kind K] [--direction right|down]\n" +
		"                      [--ratio F] [--cwd DIR] [--pane ID] [--timeout MS]\n" +
		"                      [--effort low|medium|high|xhigh|max] [--model M]\n" +
		"                      [--approvals ask|edits|full] [--mcp inherit|none] [--reuse|--fresh]\n" +
		"                      [--tab-label TEXT] [--force] [-- <native agent args>]\n"
	for _, flag := range []string{"--help", "-h"} {
		t.Run("spawn "+flag, func(t *testing.T) {
			code, out, errOut := runIn(t, []string{"spawn", flag}, env, cwd)
			if code != 0 || out != spawnHelp || errOut != "" {
				t.Fatalf("code=%d out=%q err=%q; want the spawn usage lines on stdout", code, out, errOut)
			}
		})
	}

	t.Run("feedback --help", func(t *testing.T) {
		const want = "  soho feedback send <report.md> \"<one-line summary>\"\n" +
			"                                             # feedback=local: file the report in feedback_dir; one line to feedback_to when set\n"
		code, out, errOut := runIn(t, []string{"feedback", "--help"}, env, cwd)
		if code != 0 || out != want || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q; want the feedback usage lines on stdout", code, out, errOut)
		}
	})

	t.Run("a command cited together with another prints the line it appears on", func(t *testing.T) {
		const want = "  soho roles [--json] | kinds\n"
		for _, command := range []string{"roles", "kinds"} {
			code, out, errOut := runIn(t, []string{command, "--help"}, env, cwd)
			if code != 0 || out != want || errOut != "" {
				t.Fatalf("%s --help: code=%d out=%q err=%q", command, code, out, errOut)
			}
		}
	})

	t.Run("send --help prints the send lines", func(t *testing.T) {
		const want = "  soho send <ref|name> <message…> | --file <path> [--now] [--timeout MS]\n" +
			"                      [--assignment ID --type review.question]\n" +
			"                                             # peer message to an agent of any kind (a reference, or a name on the local server); waits for a busy target to settle by default; the target project's inbound=off refuses (exit 18)\n"
		code, out, errOut := runIn(t, []string{"send", "--help"}, env, cwd)
		if code != 0 || out != want || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q; want the send usage lines on stdout", code, out, errOut)
		}
	})

	t.Run("a command without continuation lines prints only its own line", func(t *testing.T) {
		const want = "  soho mutation-guard <copy-dir> [--source <dir>] [--env NAME]...\n"
		code, out, errOut := runIn(t, []string{"mutation-guard", "--help"}, env, cwd)
		if code != 0 || out != want || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q; want the mutation-guard usage line on stdout", code, out, errOut)
		}
	})

	t.Run("friction --help stops at the next soho line", func(t *testing.T) {
		const want = "  soho friction [--since D] [--level L] [--command C] [--agent A] [--summary]  # errors/warnings of this workspace; options AND together\n"
		code, out, errOut := runIn(t, []string{"friction", "--help"}, env, cwd)
		if code != 0 || out != want || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q; want the first friction usage line on stdout", code, out, errOut)
		}
	})

	t.Run("--help after another first argument is not help", func(t *testing.T) {
		code, out, errOut := runIn(t, []string{"send", "x", "--help"}, env, cwd)
		if code != 2 || out != "" || !strings.Contains(errOut, "send: unknown option '--help'") {
			t.Fatalf("code=%d out=%q err=%q; want send's own option error, not the help", code, out, errOut)
		}
	})

	t.Run("--help on an unknown command keeps the unknown command error", func(t *testing.T) {
		code, out, errOut := runIn(t, []string{"bogus", "--help"}, env, cwd)
		if code != 2 || out != "" || !strings.Contains(errOut, "unknown command 'bogus'") {
			t.Fatalf("code=%d out=%q err=%q; want the unknown command error", code, out, errOut)
		}
	})
}

func TestCollaborationHelpIncludesWholeWorkflow(t *testing.T) {
	env, cwd := commandFixture(t)
	code, out, stderr := runIn(t, []string{"collaborate", "--help"}, env, cwd)
	if code != 0 || stderr != "" {
		t.Fatalf("help: %d %s", code, stderr)
	}
	for _, word := range []string{"start <author>", "status <assignment>", "event <assignment>", "stop <assignment>", "finalize <assignment>", "--revision", "--verdict"} {
		if !strings.Contains(out, word) {
			t.Fatalf("missing help %s: %s", word, out)
		}
	}
}
