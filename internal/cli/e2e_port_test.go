package cli

import (
	"crypto/sha256"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestE2EPortDispatchPrompt(t *testing.T) {
	for _, port := range []string{"", "47000"} {
		t.Run("port="+port, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			pinComposedStamp(t)
			if port != "" {
				roster := filepath.Join(f.state, "ws", "agents.tsv")
				body, err := os.ReadFile(roster)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
				fields := strings.Split(lines[1], "\t")
				for len(fields) < 16 {
					fields = append(fields, "")
				}
				fields = append(fields, port)
				lines[1] = strings.Join(fields, "\t")
				if err := os.WriteFile(roster, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			if code != 0 {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
			}
			composed, ok := dispatchOutputField(t, out, "composed_prompt")
			if !ok {
				t.Fatal(out)
			}
			body, err := os.ReadFile(composed)
			if err != nil {
				t.Fatal(err)
			}
			line := "Use E2E_PORT=47000 (already exported in your shell) for any local server or e2e run; never the project's default port."
			if port != "" {
				if strings.Count(string(body), line) != 1 || !strings.HasSuffix(string(body), line+"\n") {
					t.Fatalf("prompt=%q", body)
				}
			} else if strings.Contains(string(body), "E2E_PORT") {
				t.Fatalf("off prompt=%q", body)
			}
			normalized := strings.ReplaceAll(string(body), f.root, "$ROOT")
			normalized = strings.ReplaceAll(normalized, testSkillDir(t), "$SKILL")
			normalized = strings.ReplaceAll(normalized, platform.LauncherPath(f.env), "$LAUNCHER")
			t.Logf("port=%q normalized-prompt-sha256=%x", port, sha256.Sum256([]byte(normalized)))
		})
	}
}

func TestE2EPortExplain(t *testing.T) {
	env, cwd := commandFixture(t)
	sd := core.StateDir(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, cwd)
	core.RosterAppend(sd, []string{"worker", "p", "grok", "implementer", "xai", "1", cwd, "now", "model", "ask", "implementer", "", "", "", "high", "inherit", "47000"})
	env["PATH"] = ""
	env["SOHO_LANES"] = "off"
	code, out, stderr := runIn(t, []string{"explain"}, env, cwd)
	if code != 0 || !strings.Contains(out, "worker: E2E_PORT=47000\n") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	t.Logf("explain port line verified: worker: E2E_PORT=47000")
}

func TestE2EPortReleaseThenSpawnNative(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:47000")
	if err != nil {
		if os.IsPermission(err) || errors.Is(err, syscall.EPERM) {
			t.Skipf("[env] localhost listen refused: %v; rerun on host", err)
		}
		t.Skipf("[env] fixture port unavailable: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	env, cwd := commandFixture(t)
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = "p1"
	env["SOHO_LANES"] = "off"
	env["SOHO_REGRID"] = "off"
	env["SOHO_E2E_PORT_BASE"] = "47000"
	env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	// A fake Herdr process exercises public release and public spawn. Only the
	// localhost availability probe is native; no service is started.
	bin := t.TempDir()
	roles := t.TempDir()
	if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: grok\nmodel: grok-4.7\neffort: high\napprovals: ask\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env["SOHO_ROLES"] = roles
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "working\n"},
		{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p-new"}}}`},
		{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}`},
		{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "report-metadata"}, ArgvPrefix: true},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, bin)
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	sd := core.StateDir(ctx, env, cwd)
	core.RosterAppend(sd, []string{"old", "p-old", "grok", "worker", "xai", "1", cwd, "now", "grok-4.7", "ask", "worker", "", "", "", "high", "inherit", "47000"})
	code, out, stderr := runIn(t, []string{"release", "old", "--force", "--keep-copies", "--keep-procs"}, env, cwd)
	if code != 0 || core.RosterLine(sd, "old") != "" {
		t.Fatalf("release code=%d out=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = runIn(t, []string{"spawn", "worker", "--name", "new", "--fresh", "--direction", "right"}, env, cwd)
	if code != 0 || !strings.Contains(out, `"e2e_port": "47000"`) {
		t.Fatalf("spawn code=%d out=%s stderr=%s", code, out, stderr)
	}
	fields := strings.Split(core.RosterLine(sd, "new"), "\t")
	if core.RosterE2EPort(fields) != strconv.Itoa(47000) {
		t.Fatalf("new row=%v", fields)
	}
}
