package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorNativeParentlessRosterIdentity(t *testing.T) {
	listing := `{"terminals":[{"address":"t7","name":"unrelated","state":"live"},{"address":"t8","name":"second","state":"live"},{"address":"t9","state":"live","parent":"other"},{"address":"t10","state":"live"}]}`
	env, bin, sd := hcHkProject(t, []fakecli.Rule{{Argv: []string{"ls", "--json"}, Stdout: listing}})
	cwd := t.TempDir()
	row := func(name, pane string) string {
		return name + "\t" + pane + "\tcodex\timplementer\topenai\t1\t" + cwd + "\n"
	}
	roster := row("first", "t7") + row("second", "t8") + row("parented", "t9") + row("stale", "t10") + row("stale", "t11")
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := hcHkRun(t, env, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit=%d stdout=%q", code, out)
	}
	for _, want := range []string{"native worker 'first' (terminal t7)", "native worker 'second' (terminal t8)"} {
		if strings.Count(out, want) != 1 {
			t.Fatalf("missing or repeated %q: %q", want, out)
		}
	}
	if strings.Count(out, "native worker '") != 2 {
		t.Fatalf("unexpected warnings: %q", out)
	}
	ls := 0
	for _, call := range hcHkCalls(t, bin) {
		if len(call.Argv) == 2 && call.Argv[0] == "ls" && call.Argv[1] == "--json" {
			ls++
		}
	}
	if ls != 1 {
		t.Fatalf("unfiltered hc ls calls=%d want=1", ls)
	}
}

func TestDoctorNativeParentlessWorkers(t *testing.T) {
	const warning = "warn   native worker 'worker' (terminal t7) has no parent terminal: its exit note reaches nobody and it outlives the orchestrator unseen; release it (soho release worker --close) and spawn it again from the orchestrator's terminal\n"
	cases := []struct {
		name, transport, listing string
		code                     int
		want                     bool
	}{
		{"parentless", "hc", `{"terminals":[{"address":"t7","state":"live"}]}`, 0, true},
		{"parented", "hc", `{"terminals":[{"address":"t7","state":"live","parent":"t-orch"}]}`, 0, false},
		{"absent", "hc", `{"terminals":[{"address":"t8","state":"live"}]}`, 0, false},
		{"finished", "hc", `{"terminals":[{"address":"t7","state":"finished"}]}`, 0, false},
		{"unknown lifecycle", "hc", `{"terminals":[{"address":"t7"}]}`, 0, false},
		{"empty parent", "hc", `{"terminals":[{"address":"t7","state":"live","parent":""}]}`, 0, true},
		{"another terminal's malformed parent does not hide a parentless worker", "hc", `{"terminals":[{"address":"t7","state":"live"},{"address":"t8","state":"live","parent":false}]}`, 0, true},
		{"a malformed parent is unknown, not missing", "hc", `{"terminals":[{"address":"t7","state":"live","parent":false}]}`, 0, false},
		{"listing failure", "hc", "", 1, false},
		{"legacy", "herdr", `{"terminals":[{"address":"t7","state":"live"}]}`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: tc.listing, Code: tc.code}
			env, bin, sd := hcHkProject(t, []fakecli.Rule{rule})
			env["SOHO_TRANSPORT"] = tc.transport
			roster := "# name\tpane\nworker\tt7\tcodex\timplementer\topenai\t1\t" + t.TempDir() + "\n"
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, _ := hcHkRun(t, env, "doctor")
			if code != 0 {
				t.Fatalf("doctor exit=%d, want 0; stdout=%q", code, out)
			}
			count := strings.Count(out, warning)
			if (tc.want && count != 1) || (!tc.want && count != 0) || strings.Count(out, "native worker '") != count {
				t.Fatalf("warning count=%d want=%t; stdout=%q", count, tc.want, out)
			}
			ls := 0
			for _, call := range hcHkCalls(t, bin) {
				if len(call.Argv) == 2 && call.Argv[0] == "ls" && call.Argv[1] == "--json" {
					ls++
				}
			}
			wantLS := 1
			if tc.transport == "herdr" {
				wantLS = 0
			}
			if ls != wantLS {
				t.Fatalf("unfiltered hc ls calls=%d want=%d", ls, wantLS)
			}
		})
	}
}
