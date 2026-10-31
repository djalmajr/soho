package hcbackend

import (
	"context"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// TestListLaunchFailureIsUnavailable: a roster query names no target, so an
// `hc ls` that exits 3 is the backend being unavailable, not a target not
// found; the hc cause is kept.
func TestListLaunchFailureIsUnavailable(t *testing.T) {
	env, _ := fakeHc(t, []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s9"}}`},
		{Argv: []string{"ls", "--label", "soho.session=s9", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such session"}}`},
	})
	b := &backend{env: env, client: hc.New(env)}
	rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
	if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
		t.Fatalf("err %+v; want FailureUnavailable", rep.Err)
	}
	if rep.Err.Cause == nil {
		t.Fatal("the hc cause was dropped")
	}
}

// TestTurnKeepsTheWholeUint64: the turn is the schema's uint64 and reaches
// the sequence byte for byte, in Get and in List, beyond float64's exact
// integers and up to the uint64 maximum; a negative or fractional turn is no
// sequence.
func TestTurnKeepsTheWholeUint64(t *testing.T) {
	for _, turn := range []string{"9007199254740993", "18446744073709551615", "0"} {
		t.Run("get "+turn, func(t *testing.T) {
			env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","status":{"state":"idle"},"turn":` + turn + `}`}})
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
			if rep.Err != nil || string(rep.Value.Sequence) != turn {
				t.Fatalf("sequence %q err %v; want %s", rep.Value.Sequence, rep.Err, turn)
			}
		})
		t.Run("list "+turn, func(t *testing.T) {
			env, _ := fakeHc(t, []fakecli.Rule{
				{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s9"}}`},
				{Argv: []string{"ls", "--label", "soho.session=s9", "--json"}, Stdout: `{"terminals":[{"address":"t7","state":"live","status":{"state":"idle"},"turn":` + turn + `}]}`},
			})
			b := &backend{env: env, client: hc.New(env)}
			rep := b.List(context.Background(), transport.Scope{}, transport.Call{})
			if rep.Err != nil || len(rep.Value) != 1 || string(rep.Value[0].Sequence) != turn {
				t.Fatalf("rep %+v; want one terminal with sequence %s", rep, turn)
			}
		})
	}
	for _, bad := range []string{"-1", "1.5", `"7"`} {
		t.Run("not a uint64 "+bad, func(t *testing.T) {
			env, _ := fakeHc(t, []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","status":{"state":"idle"},"turn":` + bad + `}`}})
			b := &backend{env: env, client: hc.New(env)}
			rep := b.Get(context.Background(), transport.Target{ID: "t7"}, transport.Call{})
			if rep.Value.Sequence != nil {
				t.Fatalf("sequence %q; want none for turn %s", rep.Value.Sequence, bad)
			}
		})
	}
}
