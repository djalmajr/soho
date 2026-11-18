package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnContinueAdmission(t *testing.T) {
	cases := []struct {
		name, kind, config string
		args               []string
		want               string
	}{
		{"pi config flag", "pi", "", []string{"--", "-c", "shell_environment_policy.include_only=[x]"}, "-c"},
		{"claude fresh", "claude", "", []string{"--fresh", "--", "--continue"}, "--continue"},
		{"pi long config", "pi", "", []string{"--", "--config", "foo=bar"}, "--config"},
		{"configured", "pi", "-c k=v", nil, "-c"},
		{"equals short", "pi", "", []string{"--fresh", "--", "-c=foo=bar"}, "-c=foo=bar"},
		{"equals long", "pi", "", []string{"--fresh", "--", "--continue=true"}, "--continue=true"},
		{"codex", "codex", "", []string{"--fresh", "--", "-c", "foo=bar"}, ""},
		{"plain continue", "pi", "", []string{"--", "-c"}, ""},
		{"plain word", "pi", "", []string{"--", "-c", "word"}, ""},
		{"flag next", "pi", "", []string{"--", "-c", "--model"}, ""},
		{"empty", "pi", "", nil, ""},
		{"invalid key", "pi", "", []string{"--", "-c", "bad/key=value"}, ""},
		// R567: after the worker's own "--" the tokens are prompt text.
		{"literal prompt continue", "pi", "", []string{"--fresh", "--", "--", "--continue"}, ""},
		{"literal prompt config", "pi", "", []string{"--fresh", "--", "--", "-c", "topic=sorting"}, ""},
		{"before worker separator", "pi", "", []string{"--fresh", "--", "--continue", "--", "text"}, "--continue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFixture(t, freshSpawnRules())
			configureSpawnFixture(t, &f)
			f.env["SOHO_ARGS_PI"] = tc.config
			args := append([]string{"worker", "--kind", tc.kind, "--name", "worker", "--pane", "p1"}, tc.args...)
			code, msg, _, _, calls := runCmdSpawnWithExitMessage(t, f, args)
			if tc.want == "" {
				if code != 0 {
					t.Fatalf("unexpected exit=%d message=%s", code, msg)
				}
				return
			}
			if code != 2 || !strings.Contains(msg, tc.kind) || !strings.Contains(msg, tc.want) || !strings.Contains(msg, "--continue") {
				t.Fatalf("code=%d message=%q", code, msg)
			}
			if len(calls) != 0 {
				t.Fatalf("transport calls before refusal: %+v", calls)
			}
			entries, err := os.ReadDir(filepath.Join(f.state, "ws"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("state changed: %v %v", entries, err)
			}
		})
	}
}

func TestSpawnContinueHCAndResolvedKind(t *testing.T) {
	for _, transport := range []string{"hc", "herdr"} {
		t.Run(transport, func(t *testing.T) {
			f := newSpawnFixture(t, freshSpawnRules())
			configureSpawnFixture(t, &f)
			f.env["SOHO_TRANSPORT"] = transport
			if _, err := fakecli.Install(t, f.bin, "hc", nil); err != nil {
				t.Fatal(err)
			}
			// The fixture role is grok; an explicit pi kind must win.
			code, msg, _, _, calls := runCmdSpawnWithExitMessage(t, f, []string{"worker", "--kind", "pi", "--fresh", "--", "--continue"})
			if code != 2 || !strings.Contains(msg, "pi") || len(calls) != 0 {
				t.Fatalf("code=%d message=%q calls=%+v", code, msg, calls)
			}
			hcCalls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(hcCalls) != 0 {
				t.Fatalf("HC calls before refusal: %+v", hcCalls)
			}
			entries, err := os.ReadDir(filepath.Join(f.state, "ws"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("state changed: %v %v", entries, err)
			}
		})
	}
}

func TestSpawnContinueSanitizesKind(t *testing.T) {
	for _, args := range [][]string{{"--fresh", "--", "--continue"}, {"--", "-c", "key=value"}} {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		argv := append([]string{"worker", "--kind", "pi\nmarker"}, args...)
		code, msg, _, _, calls := runCmdSpawnWithExitMessage(t, f, argv)
		if code != 2 || strings.ContainsAny(msg, "\r\n") || !strings.Contains(msg, "marker") || len(calls) != 0 {
			t.Fatalf("argv=%q exit=%d message=%q calls=%+v", argv, code, msg, calls)
		}
	}
}

func TestSpawnPressureWarningBeforeParseError(t *testing.T) {
	code, _, stderr, message, calls, _ := pressureSpawn(t, []string{"worker", "--kind", "pi", "--typo"}, pressureGB, 100*pressureGB, true, 0, 0, false)
	if code != 2 || !strings.Contains(stderr, pressureWarnLine) || message != "spawn: unknown option --typo" || len(calls) != 0 {
		t.Fatalf("exit=%d stderr=%q message=%q calls=%+v", code, stderr, message, calls)
	}
}
