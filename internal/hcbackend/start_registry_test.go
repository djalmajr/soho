package hcbackend

// SOH-292: Start hands RegistryDir and Agent to the client's dedicated
// fields; the request env gains exactly SOHO_REGISTRY_DIR and SOHO_AGENT when
// they are set, and a value the client refuses refuses the start before any
// terminal.run request is written.

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

func TestStartForwardsRegistryDirAndAgent(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "state")
	for _, tc := range []struct {
		name        string
		registryDir string
		agent       string
		want        map[string]any
	}{
		{"unset", "", "", map[string]any{"SOHO_TRANSPORT": "hc"}},
		{"both", abs, "w7", map[string]any{"SOHO_TRANSPORT": "hc", "SOHO_REGISTRY_DIR": abs, "SOHO_AGENT": "w7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, conn, _ := startEnv(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle}})
			b := newBackend(env, conn)
			rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000", RegistryDir: tc.registryDir, Agent: tc.agent}, transport.Call{})
			if rep.Err != nil {
				t.Fatalf("rep: %+v", rep)
			}
			_, params := parseTerminalRunReq(t, conn.getWritten())
			if !reflect.DeepEqual(params["env"], tc.want) {
				t.Fatalf("env=%#v want %#v", params["env"], tc.want)
			}
		})
	}
}

func TestStartRefusesAnInvalidRegistryDirOrAgent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start transport.Start
	}{
		{"relative registry dir", transport.Start{RegistryDir: "state"}},
		{"registry dir with LF", transport.Start{RegistryDir: "/state\nx"}},
		{"agent with NUL", transport.Start{Agent: "w\x007"}},
		{"agent not a terminal name", transport.Start{Agent: "W 7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, conn, _ := startEnv(t, nil)
			b := newBackend(env, conn)
			start := tc.start
			start.Name, start.Kind, start.Timeout = "w7", "claude", "3000"
			rep := b.Start(context.Background(), start, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable || rep.Value != "" || rep.Valid {
				t.Fatalf("rep: %+v want a refusal with no address", rep)
			}
			if conn.getConnectCount() != 0 || strings.Contains(string(conn.getWritten()), "terminal.run") {
				t.Fatalf("connectCount=%d: the refusal must come before any terminal.run", conn.getConnectCount())
			}
		})
	}
}
