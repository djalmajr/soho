package hc

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestPlaceJSON pins the client's --json placement: only the options
// region (before the first "--") counts, and when it holds none the flag
// is inserted right before the first "--", or appended when there is no
// "--" at all (the no-"--" cases are unchanged from the old append).
func TestPlaceJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"no --, no --json: appended", []string{"get", "t7"}, []string{"get", "t7", "--json"}},
		{
			"no --, already --json: unchanged",
			[]string{"ls", "--json"},
			[]string{"ls", "--json"},
		},
		{
			"with --, no --json: inserted right before the --",
			[]string{"run", "--bg", "--name", "w", "--", "claude", "--model", "m"},
			[]string{"run", "--bg", "--name", "w", "--json", "--", "claude", "--model", "m"},
		},
		{
			"with --, --json only after it: a program argument does not suppress the flag",
			[]string{"run", "--bg", "--", "claude", "--json"},
			[]string{"run", "--bg", "--json", "--", "claude", "--json"},
		},
		{
			"with --, --json already in the options region: unchanged, once",
			[]string{"run", "--bg", "--json", "--name", "w", "--", "claude", "--json"},
			[]string{"run", "--bg", "--json", "--name", "w", "--", "claude", "--json"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := placeJSON(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("placeJSON(%#v) = %#v want %#v", tc.args, got, tc.want)
			}
		})
	}
}

// TestRunInputPlacesJSONBeforeTheProgramArgs goes through the package-level
// RunInput end to end (a fake hc on PATH): the client's --json lands in the
// options region, before the "--", while the program's own --json argument
// stays where the program put it.
func TestRunInputPlacesJSONBeforeTheProgramArgs(t *testing.T) {
	env, _ := newHCFake(t, []fakecli.Rule{
		{Argv: []string{"run", "--bg", "--name", "w", "--json", "--", "claude", "--json"}, Stdout: "{}"},
	})
	if _, err := RunInput(context.Background(), env, "line", "run", "--bg", "--name", "w", "--", "claude", "--json"); err != nil {
		t.Fatalf("err: %v", err)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "hc.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls=%d want 1: %#v", len(calls), calls)
	}
	want := []string{"run", "--bg", "--name", "w", "--json", "--", "claude", "--json"}
	if !reflect.DeepEqual(calls[0].Argv, want) {
		t.Fatalf("argv %#v want %#v", calls[0].Argv, want)
	}
}
