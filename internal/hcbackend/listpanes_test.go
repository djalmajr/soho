package hcbackend

// DJA-604 2b-4: ListPanes under transport=hc. The soho session is the
// workspace; the views carry the herdr-shaped pane object (pane_id the
// terminal address) the way OrphansOf reads it.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// TestBackendListPanes pins the session listing, the herdr-shaped projection
// and the failure mapping of ListPanes.
func TestBackendListPanes(t *testing.T) {
	const twoTerminals = `{"terminals":[` +
		`{"address":"t7","name":"w7","state":"live","status":{"state":"working"},"turn":5},` +
		`{"address":"t8","name":"w8","state":"live","status":{"state":"idle"},"turn":1}` +
		`],"hidden_finished":0}`

	t.Run("session terminals: one view each, pane_id the address", func(t *testing.T) {
		env, calls := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s9"}}`},
			{Argv: []string{"ls", "--label", "soho.session=s9", "--json"}, Stdout: twoTerminals},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.ListPanes(context.Background(), transport.Scope{}, transport.Call{})
		if rep.Err != nil {
			t.Fatalf("err: %v", rep.Err)
		}
		if !rep.Valid || len(rep.Value) != 2 {
			t.Fatalf("rep: %+v", rep)
		}
		want := []struct {
			id, paneID string
			raw        string
		}{
			{"t7", "t7", `{"pane_id":"t7"}`},
			{"t8", "t8", `{"pane_id":"t8"}`},
		}
		for i, w := range want {
			got := rep.Value[i]
			if got.ID != w.id || got.PaneID != w.paneID || string(got.Raw) != w.raw {
				t.Fatalf("view %d: %+v want %v", i, got, w)
			}
			var pane map[string]any
			if err := json.Unmarshal(got.Raw, &pane); err != nil {
				t.Fatalf("raw %q is not a JSON object: %v", got.Raw, err)
			}
			if pane["pane_id"] != w.paneID {
				t.Fatalf("raw pane_id = %v, want %s", pane["pane_id"], w.paneID)
			}
		}
		wantArgv(t, calls(), [][]string{
			{"get", "t1", "--json"},
			{"ls", "--label", "soho.session=s9", "--json"},
		})
	})

	t.Run("empty session: a valid empty listing", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s9"}}`},
			{Argv: []string{"ls", "--label", "soho.session=s9", "--json"}, Stdout: `{"terminals":[]}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.ListPanes(context.Background(), transport.Scope{}, transport.Call{})
		if rep.Err != nil || !rep.Valid || len(rep.Value) != 0 {
			t.Fatalf("rep: %+v, want a valid empty listing", rep)
		}
	})

	t.Run("failed listing: unavailable, never not-found", func(t *testing.T) {
		env, _ := fakeHc(t, []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"s9"}}`},
			{Argv: []string{"ls", "--label", "soho.session=s9", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such label"}}`},
		})
		b := &backend{env: env, client: hc.New(env)}
		rep := b.ListPanes(context.Background(), transport.Scope{}, transport.Call{})
		if rep.Err == nil || rep.Err.Kind != transport.FailureUnavailable {
			t.Fatalf("err: %+v, want FailureUnavailable", rep.Err)
		}
	})
}
