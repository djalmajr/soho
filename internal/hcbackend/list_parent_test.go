package hcbackend

import (
	"context"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

func TestListParentField(t *testing.T) {
	for _, all := range []bool{false, true} {
		name := "session"
		argv := []string{"ls", "--label", "soho.session=s9", "--json"}
		if all {
			name = "all"
			argv = []string{"ls", "--json"}
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name, parent, want string
				bad                bool
			}{
				{"omitted", "", "", false},
				{"empty", `,"parent":""`, "", false},
				{"set", `,"parent":"t-orch"`, "t-orch", false},
				{"null", `,"parent":null`, "", true},
				{"number", `,"parent":42`, "", true},
				{"object", `,"parent":{}`, "", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body := `{"terminals":[{"address":"t7","state":"live"},{"address":"t8","state":"live"` + tc.parent + `}]}`
					env, _ := fakeHc(t, []fakecli.Rule{
						{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s9"}}`},
						{Argv: argv, Stdout: body},
					})
					b := &backend{env: env, client: hc.New(env)}
					rep := b.List(context.Background(), transport.Scope{All: all}, transport.Call{})
					// A malformed parent never fails the listing; it is unknown.
					if rep.Err != nil || !rep.Valid || len(rep.Value) != 2 || rep.Value[0].Parent != "" || !rep.Value[0].ParentKnown || rep.Value[1].Parent != tc.want || rep.Value[1].ParentKnown == tc.bad {
						t.Fatalf("listing=%+v want parent %q", rep, tc.want)
					}
				})
			}
		})
	}
}
