package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

type parentlessRuntime struct {
	transport.Transport
	reply transport.Reply[[]transport.Terminal]
	calls int
	scope transport.Scope
}

func (r *parentlessRuntime) List(_ context.Context, scope transport.Scope, _ transport.Call) transport.Reply[[]transport.Terminal] {
	r.calls++
	r.scope = scope
	return r.reply
}

func TestDoctorParentlessRejectsFailedListingAndLegacyTransport(t *testing.T) {
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{{Argv: []string{"get", "t-orch", "--json"}, Stdout: `{"address":"t-orch","state":"live","labels":{"soho.session":"ws"}}`}}); err != nil {
		t.Fatal(err)
	}
	env, root := doctorHcEnv(t, bin)
	env["SOHO_TRANSPORT"] = "hc"
	env["HC_TERMINAL_ID"] = "t-orch"
	sd := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tt7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	term := transport.Terminal{PaneID: "t7", NativeState: "live", ParentKnown: true}
	for _, tc := range []struct {
		name        string
		legacy      bool
		reply       transport.Reply[[]transport.Terminal]
		wantCalls   int
		wantWarning bool
	}{
		{"valid control", false, transport.Reply[[]transport.Terminal]{Valid: true, Value: []transport.Terminal{term}}, 1, true},
		{"failure with partial value", false, transport.Reply[[]transport.Terminal]{Valid: true, Err: &transport.Failure{Kind: transport.FailureUnavailable}, Value: []transport.Terminal{term}}, 1, false},
		{"invalid with partial value", false, transport.Reply[[]transport.Terminal]{Value: []transport.Terminal{term}}, 1, false},
		{"legacy guard", true, transport.Reply[[]transport.Terminal]{Valid: true, Value: []transport.Terminal{term}}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := backend.New(env)
			r := &parentlessRuntime{Transport: svc.Runtime, reply: tc.reply}
			svc.Runtime = r
			cfg := doctorHcCtx("hc")
			testEnv := env.Clone()
			if tc.legacy {
				testEnv["SOHO_TRANSPORT"] = "herdr"
				testEnv["HERDR_ENV"] = "1"
				testEnv["HERDR_WORKSPACE_ID"] = "ws"
				cfg = doctorHcCtx("herdr")
			}
			var out strings.Builder
			doctorNativeParentless(cfg, svc, testEnv, root, &Say{Out: &out})
			if r.calls != tc.wantCalls || (r.calls > 0 && !r.scope.All) {
				t.Fatalf("list calls=%d scope=%+v want=%d all=true", r.calls, r.scope, tc.wantCalls)
			}
			if got := strings.Contains(out.String(), "native worker 'worker'"); got != tc.wantWarning {
				t.Fatalf("warning=%t want=%t output=%q", got, tc.wantWarning, out.String())
			}
		})
	}
}
