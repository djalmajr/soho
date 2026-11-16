package doctor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// TestDoctorOrphanSharedSnapshot isolates the three checks sharing the doctor
// runtime. The whole-doctor budget below also counts unrelated checks.
func TestDoctorOrphanSharedSnapshot(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	state := filepath.Join(root, "state", "ws")
	for _, dir := range []string{bin, state, filepath.Join(root, "skill")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"agents.tsv":         "worker\tt2\tpi\timplementer\n",
		"released-panes.tsv": "orphan\tt3\tpi\tnow\nforeign\tt4\tpi\tnow\n",
	} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	all := `{"terminals":[{"address":"t2","name":"worker","state":"live","parent":"t1","labels":{"soho.session":"ws"}},{"address":"t3","name":"orphan","state":"live","status":{"state":"idle"},"labels":{"soho.session":"ws"}},{"address":"t4","name":"foreign","state":"live","labels":{"soho.session":"other"}}]}`
	session := `{"terminals":[{"address":"t2","name":"worker","state":"live"},{"address":"t3","name":"orphan","state":"live","status":{"state":"idle"}}]}`
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"}}`},
		{Argv: []string{"ls", "--json"}, Stdout: all},
		{Argv: []string{"ls", "--label", "soho.session=ws", "--json"}, Stdout: session},
	}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_TRANSPORT": "hc", "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "HC_TERMINAL_ID": "t1"}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	svc := backend.New(env)
	svc.Runtime = &listOnceRuntime{Transport: svc.Runtime}
	var out strings.Builder
	s := &Say{Out: &out}
	doctorOrphans(ctx, svc, env, root, s)
	doctorDoneOpen(ctx, svc, env, root, s)
	doctorNativeParentless(ctx, svc, env, root, s)
	want := "warn   orphan pane: 'orphan' (t3, idle) is not in the roster; close it with: soho release orphan --close\n"
	if out.String() != want {
		t.Errorf("warnings=%q, want %q", out.String(), want)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var lists [][]string
	for _, call := range calls {
		if len(call.Argv) > 0 && call.Argv[0] == "ls" {
			lists = append(lists, call.Argv)
		}
	}
	if !reflect.DeepEqual(lists, [][]string{{"ls", "--json"}}) {
		t.Errorf("listing calls=%q; want one unfiltered hc ls", lists)
	}
}

// TestNativeInboundRawIdentityCollision demonstrates that the snapshot's
// projection cannot distinguish two records the current inbound check does.
func TestNativeInboundRawIdentityCollision(t *testing.T) {
	var projections [][]transport.Terminal
	for _, paneField := range []string{`,"pane_id":"t2"`, ""} {
		root := t.TempDir()
		bin, state := filepath.Join(root, "bin"), filepath.Join(root, "state", "ws")
		for _, dir := range []string{bin, state, filepath.Join(root, "skill")} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("first\tt2\tpi\timplementer\n"), 0600); err != nil {
			t.Fatal(err)
		}
		listing := `{"terminals":[{"address":"t2","name":"first","state":"live","labels":{"soho.session":"ws"}` + paneField + `}]}`
		if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"}}`},
			{Argv: []string{"ls", "--json"}, Stdout: listing},
			{Argv: []string{"ls", "--label", "soho.session=ws", "--json"}, Stdout: listing},
		}); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_TRANSPORT": "hc", "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "HC_TERMINAL_ID": "t1"}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		listed := backend.New(env).Runtime.List(nil, transport.Scope{All: true}, transport.Call{})
		if listed.Err != nil || !listed.Valid || len(listed.Value) != 1 {
			t.Fatalf("listing=%+v", listed)
		}
		projections = append(projections, listed.Value)
		var out strings.Builder
		doctorUnprotectedAgents(ctx, env, root, &Say{Out: &out})
		want := ""
		if paneField != "" {
			want = "warn   first (pi) is not protected: no inbound hook for pi yet (run 'soho setup')\n"
		}
		if out.String() != want {
			t.Fatalf("pane field %q: warning=%q want=%q", paneField, out.String(), want)
		}
	}
	if !reflect.DeepEqual(projections[0], projections[1]) {
		t.Fatalf("expected identical terminal projections: %#v and %#v", projections[0], projections[1])
	}
}

// TestDoctorInboundNativeSnapshot preserves raw inbound identity rules and
// checks the whole-doctor listing budget.
func TestDoctorInboundNativeSnapshot(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		name := "healthy"
		if malformed {
			name = "malformed foreign record"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			bin, state := filepath.Join(root, "bin"), filepath.Join(root, "state", "ws")
			for _, dir := range []string{bin, state, filepath.Join(root, "skill")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			roster := "first\tt2\tpi\timplementer\nsecond\tt3\tpi\timplementer\nthird\tt4\tpi\timplementer\nany\t\tpi\timplementer\nforeign\tt5\tpi\timplementer\n"
			if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(roster), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "released-panes.tsv"), []byte("orphan\tt7\tpi\tnow\n"), 0600); err != nil {
				t.Fatal(err)
			}
			local := `{"address":"t2","pane_id":"t2","name":"first","state":"live","labels":{"soho.session":"ws"}},{"address":"t3","name":"second","state":"live","labels":{"soho.session":"ws"}},{"address":"t4","pane_id":"wrong","name":"third","state":"live","labels":{"soho.session":"ws"}},{"address":"t6","name":"any","state":"live","labels":{"soho.session":"ws"}},{"address":"t7","name":"orphan","state":"live","status":{"state":"idle"},"labels":{"soho.session":"ws"}}`
			foreign := `{"address":"t5","pane_id":"t5","name":"foreign","state":"live","labels":{"soho.session":"other"}}`
			if malformed {
				foreign = `{"address":false,"name":"foreign","state":"live","labels":{"soho.session":"other"}}`
			}
			all := `{"terminals":[` + local + `,` + foreign + `]}`
			session := `{"terminals":[` + local + `]}`
			if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
				{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"}}`},
				{Argv: []string{"ls", "--json"}, Stdout: all},
				{Argv: []string{"ls", "--label", "soho.session=ws", "--json"}, Stdout: session},
				{Argv: []string{"--version", "--json"}, Stdout: "hc 0.1.14\n"},
				{Argv: []string{"server", "status", "--json"}, Stdout: doctorHcStatusJSON},
				{Argv: []string{"connect", "--json"}, Stdout: doctorHcHelloOK},
			}); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_TRANSPORT": "hc", "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "HC_TERMINAL_ID": "t1"}
			var out strings.Builder
			DoctorCheck(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, root, &out)
			var got []string
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, " (pi) ") || strings.Contains(line, "orphan pane:") {
					got = append(got, line)
				}
			}
			want := []string{
				"warn   first (pi) is not protected: no inbound hook for pi yet (run 'soho setup')",
				"warn   any (pi) is not protected: no inbound hook for pi yet (run 'soho setup')",
			}
			if !malformed {
				want = append([]string{"warn   orphan pane: 'orphan' (t7, idle) is not in the roster; close it with: soho release orphan --close"}, want...)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("warnings=%q want=%q; doctor output:\n%s", got, want, out.String())
			}
			if !strings.Contains(out.String(), "first_run:") {
				t.Error("doctor stopped before first_run")
			}
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var lists [][]string
			for _, call := range calls {
				if len(call.Argv) > 0 && call.Argv[0] == "ls" {
					lists = append(lists, call.Argv)
				}
			}
			// Inbound retains a labeled raw listing until the follow-up adds
			// raw records to the shared terminal snapshot.
			wantCalls := [][]string{{"ls", "--json"}, {"ls", "--label", "soho.session=ws", "--json"}}
			if !reflect.DeepEqual(lists, wantCalls) {
				t.Errorf("listing calls=%q; want %q", lists, wantCalls)
			}
		})
	}
}

// TestDoctorOrphanListingBudget exercises the whole doctor, including the
// done-open and parentless checks, with both real transport adapters.
func TestDoctorOrphanListingBudget(t *testing.T) {
	for _, mode := range []string{"hc", "herdr"} {
		for _, fail := range []bool{false, true} {
			name := mode
			if fail {
				name += "/listing failure"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				state := filepath.Join(root, "state", "ws")
				for _, dir := range []string{bin, state, filepath.Join(root, "skill")} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				for file, content := range map[string]string{
					"agents.tsv":         "worker\tt2\tpi\timplementer\n",
					"released-panes.tsv": "zeta\tt3\tpi\tnow\nalpha\tt4\tpi\tnow\nworker\tt2\tpi\tnow\ncaller\tt1\tpi\tnow\nforeign\tt5\tpi\tnow\n",
				} {
					if err := os.WriteFile(filepath.Join(state, file), []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				nativeList := `{"terminals":[{"address":"t1","name":"caller","state":"live","labels":{"soho.session":"ws"}},{"address":"t2","name":"worker","state":"live","labels":{"soho.session":"ws"}},{"address":"t3","name":"zeta","state":"live","status":{"state":"idle"},"labels":{"soho.session":"ws"}},{"address":"t4","name":"alpha","state":"live","status":{"state":"working"},"labels":{"soho.session":"ws"}},{"address":"t5","name":"foreign","state":"live","status":{"state":"idle"},"labels":{"soho.session":"other"}}]}`
				sessionList := `{"terminals":[{"address":"t1","name":"caller","state":"live"},{"address":"t2","name":"worker","state":"live"},{"address":"t3","name":"zeta","state":"live","status":{"state":"idle"}},{"address":"t4","name":"alpha","state":"live","status":{"state":"working"}}]}`
				legacyList := `{"result":{"agents":[{"name":"caller","pane_id":"t1","agent_status":"idle"},{"name":"worker","pane_id":"t2","agent_status":"idle"},{"name":"zeta","pane_id":"t3","agent_status":"idle"},{"name":"alpha","pane_id":"t4","agent_status":"working"},{"name":"foreign","pane_id":"t5","agent_status":"idle"}]}}`
				code := 0
				if fail {
					code = 1
				}
				rules := []fakecli.Rule{
					{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
					{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
					{Argv: []string{"agent", "list"}, Stdout: legacyList, Code: code},
					{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"t1"},{"pane_id":"t2"},{"pane_id":"t3"},{"pane_id":"t4"}]}}`},
				}
				binary := "herdr"
				if mode == "hc" {
					binary = "hc"
					rules = []fakecli.Rule{
						{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"}}`},
						{Argv: []string{"ls", "--json"}, Stdout: nativeList, Code: code},
						{Argv: []string{"ls", "--label", "soho.session=ws", "--json"}, Stdout: sessionList, Code: code},
						{Argv: []string{"--version", "--json"}, Stdout: "hc 0.1.14\n"},
						{Argv: []string{"server", "status", "--json"}, Stdout: doctorHcStatusJSON},
						{Argv: []string{"connect", "--json"}, Stdout: doctorHcHelloOK},
					}
				}
				if _, err := fakecli.Install(t, bin, binary, rules); err != nil {
					t.Fatal(err)
				}
				env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_TRANSPORT": mode, "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "HERDR_ENV": "1", "HERDR_PANE_ID": "t1", "HERDR_WORKSPACE_ID": "ws", "SOHO_CALLER_CHECK": "off"}
				if mode == "hc" {
					env["HC_TERMINAL_ID"] = "t1"
				}
				var out strings.Builder
				DoctorCheck(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, root, &out)
				lines := []string{}
				for _, line := range strings.Split(out.String(), "\n") {
					if strings.Contains(line, "orphan pane:") {
						lines = append(lines, line)
					}
				}
				want := []string{}
				if !fail {
					want = []string{"warn   orphan pane: 'alpha' (t4, working) is not in the roster; close it with: soho release alpha --close", "warn   orphan pane: 'zeta' (t3, idle) is not in the roster; close it with: soho release zeta --close"}
				}
				if !reflect.DeepEqual(lines, want) {
					t.Errorf("orphan warnings=%q, want %q; output:\n%s", lines, want, out.String())
				}
				if !strings.Contains(out.String(), "first_run:") {
					t.Error("doctor stopped before first_run")
				}
				calls, err := fakecli.ReadCalls(filepath.Join(bin, binary+".calls.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				var lists [][]string
				for _, call := range calls {
					if len(call.Argv) > 0 && (call.Argv[0] == "ls" || len(call.Argv) > 1 && (call.Argv[0] == "agent" || call.Argv[0] == "pane") && call.Argv[1] == "list") {
						lists = append(lists, call.Argv)
					}
				}
				if mode == "hc" {
					// Inbound retains a labeled raw listing until the follow-up
					// adds raw records to the shared terminal snapshot.
					wantCalls := [][]string{{"ls", "--json"}, {"ls", "--label", "soho.session=ws", "--json"}}
					if !reflect.DeepEqual(lists, wantCalls) {
						t.Errorf("listing calls=%q; want %q", lists, wantCalls)
					}
				} else {
					wantCalls := [][]string{{"agent", "list"}, {"pane", "list", "--workspace", "ws"}, {"agent", "list"}, {"agent", "list"}}
					if fail {
						wantCalls = [][]string{{"agent", "list"}, {"agent", "list"}, {"agent", "list"}}
					}
					if !reflect.DeepEqual(lists, wantCalls) {
						t.Errorf("listing calls=%q; want %q", lists, wantCalls)
					}
				}
			})
		}
	}
}
