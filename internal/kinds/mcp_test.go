package kinds

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func fakePiHelpEnv(t *testing.T, stdout string, code int) platform.Env {
	t.Helper()
	return fakePiHelpStreams(t, stdout, "", code)
}

func fakePiHelpStreams(t *testing.T, stdout, stderr string, code int) platform.Env {
	t.Helper()
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "pi", []fakecli.Rule{
		{Argv: []string{"--help"}, Stdout: stdout, Stderr: stderr, Code: code},
		{AnyArgs: true},
	}); err != nil {
		t.Fatal(err)
	}
	return platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "TMPDIR": t.TempDir()}
}

// probePi resolves the absolute executable the way doctor does, so tests
// exercise the exact production entry point rather than a basename adapter.
func probePi(t *testing.T, env platform.Env, cwd string) McpCapability {
	t.Helper()
	program, ok := platform.FindExecutable("pi", env, platform.Current())
	if !ok {
		return McpCapabilityInconclusive
	}
	if absolute, err := filepath.Abs(program); err == nil {
		program = absolute
	}
	return PiNoMcpCapabilityProgram(env, cwd, program)
}

func TestPiNoMcpCapability(t *testing.T) {
	cases := []struct {
		name string
		help string
		code int
		want McpCapability
	}{
		{"long option line", "Usage: pi [options]\n  --no-mcp      Disable all MCP servers\n", 0, McpCapabilitySupported},
		{"short alias line", "  -n, --no-mcp   Disable MCP servers\n", 0, McpCapabilitySupported},
		{"no option in successful help", "Usage: pi [options]\n  --model <id>   Choose a model\n", 0, McpCapabilityUnsupported},
		{"prose mention is inconclusive", "Use --no-mcp to disable servers in a new shell.\n", 0, McpCapabilityInconclusive},
		{"failed help is inconclusive", "", 1, McpCapabilityInconclusive},
		{"error-shaped line is inconclusive", "--no-mcp is unsupported by this wrapper\n", 0, McpCapabilityInconclusive},
		{"indented diagnostic is inconclusive", "wrapper ready\n  --no-mcp is unsupported here\n", 0, McpCapabilityInconclusive},
		{"generic usage is inconclusive", "Usage: tool [options]\n  --model <id>  Choose a model\n", 0, McpCapabilityInconclusive},
		{"prefix-sharing wrapper is inconclusive", "Usage: pip [options]\n  --model <id>  Choose a model\n", 0, McpCapabilityInconclusive},
		{"empty options block is inconclusive", "Usage: pi [options]\nOptions:\n", 0, McpCapabilityInconclusive},
		{"example invocation is not option evidence", "Usage: pi [options]\n  pi --model sonnet\n", 0, McpCapabilityInconclusive},
		{"example with the flag stays inconclusive", "Usage: pi [options]\n  pi --no-mcp\n", 0, McpCapabilityInconclusive},
		{"unpunctuated short alias is not a declaration", "  -n --no-mcp   Disable MCP servers\n", 0, McpCapabilityInconclusive},
		{"unrelated success is inconclusive", "wrapper ready\n", 0, McpCapabilityInconclusive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := probePi(t, fakePiHelpEnv(t, tc.help, tc.code), t.TempDir())
			if got != tc.want {
				t.Fatalf("capability=%v, want %v", got, tc.want)
			}
		})
	}
	t.Run("an absolute program is probed as named", func(t *testing.T) {
		env := fakePiHelpStreams(t, "Usage: pi [options]\n  --model <id>  Choose a model\n", "", 0)
		program, ok := platform.FindExecutable("pi", env, platform.Current())
		if !ok {
			t.Fatal("fake pi was not found")
		}
		if got := PiNoMcpCapabilityProgram(env, t.TempDir(), program); got != McpCapabilityUnsupported {
			t.Fatalf("capability=%v, want unsupported", got)
		}
	})
	t.Run("missing executable is inconclusive", func(t *testing.T) {
		got := probePi(t, platform.Env{"PATH": t.TempDir(), "TMPDIR": t.TempDir()}, t.TempDir())
		if got != McpCapabilityInconclusive {
			t.Fatalf("capability=%v, want inconclusive", got)
		}
	})
	for _, tc := range []struct {
		name, stdout, stderr string
		want                 McpCapability
	}{
		{"authentic help on stderr", "wrapper banner\n", "Usage: pi [options]\n  --no-mcp      Disable all MCP servers\n", McpCapabilitySupported},
		{"help split across streams", "Usage: pi [options]\n", "  --no-mcp      Disable all MCP servers\n", McpCapabilitySupported},
		{"stderr help without the option", "wrapper banner\n", "Usage: pi [options]\n  --model <id>  Choose a model\n", McpCapabilityUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := probePi(t, fakePiHelpStreams(t, tc.stdout, tc.stderr, 0), t.TempDir())
			if got != tc.want {
				t.Fatalf("capability=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestKindMcpArgsPerProcessTranslation(t *testing.T) {
	t.Run("mcp=none maps to the kind's static per-process flag", func(t *testing.T) {
		// codex is not here: its MCP-off args are dynamic (one -c pair per
		// enabled server, built by spawn from `codex mcp list --json`), so
		// the static translation carries nothing for it.
		cases := []struct{ kind, want string }{
			{"claude", "--strict-mcp-config"},
			{"pi", "--no-mcp"},
		}
		for _, tc := range cases {
			args, ok := KindMcpArgs(tc.kind, "none")
			if !ok {
				t.Fatalf("kind %s: the per-process flag was refused", tc.kind)
			}
			if got := strings.Join(args, " "); got != tc.want {
				t.Fatalf("kind %s args=%q, want %q", tc.kind, got, tc.want)
			}
		}
	})

	t.Run("codex has no static flag: the per-server args are built from the live listing", func(t *testing.T) {
		args, ok := KindMcpArgs("codex", "none")
		if ok || len(args) > 0 {
			t.Fatalf("codex static args=%v ok=%v, want nothing (the per-server flags come from the listing)", args, ok)
		}
		if !KindMcpOff("codex") {
			t.Fatal("codex must stay yes in the MCP column: it has a mechanism, a dynamic one")
		}
	})

	t.Run("the kinds without a per-process flag report unsupported", func(t *testing.T) {
		for _, kind := range []string{"grok", "agy", "cursor", "opencode", "gemini"} {
			args, ok := KindMcpArgs(kind, "none")
			if ok || len(args) > 0 {
				t.Fatalf("kind %s: unsupported flag %q reported as supported", kind, args)
			}
		}
	})

	t.Run("inherit (and an absent value) changes nothing, even for supported kinds", func(t *testing.T) {
		for _, mcp := range []string{"", "inherit"} {
			for _, kind := range []string{"claude", "codex", "pi"} {
				args, ok := KindMcpArgs(kind, mcp)
				if !ok || len(args) > 0 {
					t.Fatalf("kind %s mcp=%q: args=%v ok=%v, want nothing", kind, mcp, args, ok)
				}
			}
		}
	})
}

func TestKindMcpOffColumnInKindsTable(t *testing.T) {
	// An empty PATH: every CLI is uninstalled, so INSTALLED is a constant
	// "no" and the only thing under test is the MCP column.
	env := platform.Env{"PATH": "/nonexistent"}
	oldOut := platform.Stdout
	var out strings.Builder
	platform.Stdout = &out
	defer func() { platform.Stdout = oldOut }()
	CmdKinds(env, "darwin")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(KnownKinds)+1 {
		t.Fatalf("table=%q", lines)
	}
	header := strings.Split(lines[0], " ")
	if header[len(header)-1] != "MCP" {
		t.Fatalf("header=%q, want the MCP column last", lines[0])
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		kind := fields[0]
		mcp := fields[len(fields)-1]
		want := "no"
		if KindMcpOff(kind) {
			want = "yes"
		}
		if mcp != want {
			t.Fatalf("row %q MCP=%q, want %q", kind, mcp, want)
		}
	}
	// The supported set is exactly claude, codex and pi.
	if !KindMcpOff("claude") || !KindMcpOff("codex") || !KindMcpOff("pi") || KindMcpOff("grok") {
		t.Fatalf("KindMcpOff disagrees with the flag translation")
	}
}
