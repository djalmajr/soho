package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestEnvDropReuseSelectsLastRosterRow(t *testing.T) {
	for _, tc := range []struct {
		name, mode, newerLane, newerPolicy, wantPane string
		withDistinct                                 bool
	}{
		{name: "non-lane uses latest", mode: "off", wantPane: "t9"},
		{name: "unnamed lane uses latest", mode: "on", wantPane: "t9"},
		{name: "named lane uses latest", mode: "named", wantPane: "t9"},
		{name: "non-lane skips newer marked name", mode: "off", newerPolicy: envDropPrefix + "DROP_TEST", wantPane: "t8", withDistinct: true},
		{name: "lane skips newer marked name", mode: "on", newerPolicy: envDropPrefix + "DROP_TEST", wantPane: "t8", withDistinct: true},
		{name: "lane cannot use older lane membership", mode: "on", newerLane: "build", wantPane: "t8", withDistinct: true},
		{name: "lane keeps order of retained candidates", mode: "on", wantPane: "t8", withDistinct: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, state, env, ctx := hcReuseFixture(t, "3")
			env["USERPROFILE"], env["XDG_CONFIG_HOME"] = env["HOME"], env["HOME"]
			if tc.mode == "off" {
				env["SOHO_LANES"] = "off"
			}
			args := []string{"reviewer", "--reuse"}
			if tc.mode == "named" {
				args = append(args, "--name", "a")
			}
			original := fmt.Sprintf(hcReuseWorkerRow, cwd)
			rows := []string{"# roster fixture", original}
			if tc.withDistinct {
				rows = append(rows, "b\tt8\t"+strings.SplitN(original, "\t", 3)[2])
			}
			fields := strings.Split(original, "\t")
			fields[1] = "t9"
			if tc.newerLane != "" {
				fields[11] = tc.newerLane
			}
			if tc.newerPolicy != "" {
				fields = append(fields, "inherit", "", "", tc.newerPolicy)
			}
			rows = append(rows, strings.Join(fields, "\t"))
			before := strings.Join(rows, "\n") + "\n"
			roster := filepath.Join(state, "sX", "agents.tsv")
			if err := os.WriteFile(roster, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}
			conn := newFakeSpawnRPCConn()
			exit, out, stderr, calls := spawnHcExitWithConn(t, bin, args, ctx, env, cwd, conn)
			after, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			if exit != nil || !spawnJSONHasField(t, out.String(), `"reused": true`) ||
				!spawnJSONHasField(t, out.String(), `"pane_id": "`+tc.wantPane+`"`) || string(after) != before {
				t.Fatalf("expected reuse of %s with an unchanged roster: exit=%+v stdout=%q stderr=%q roster=%q", tc.wantPane, exit, out, stderr, after)
			}
			if conn.getConnectCount() != 0 {
				t.Fatal("reuse attempted terminal creation")
			}
			for _, call := range calls {
				if !slices.Contains([]string{"get", "ls"}, call.Argv[0]) {
					t.Fatalf("reuse performed a terminal action: %v", call.Argv)
				}
			}
		})
	}
}

func TestEnvDropReuseRejectsReplacedSelection(t *testing.T) {
	for _, scope := range []string{"non-lane", "lane"} {
		for _, policy := range []string{"unfiltered", "filtered"} {
			t.Run(scope+"/"+policy, func(t *testing.T) {
				cwd, bin, state, env, ctx := hcSpawnFixture(t, "writer", "opencode")
				env["OPENCODE_CONFIG_CONTENT"] = "{}"
				env["USERPROFILE"], env["XDG_CONFIG_HOME"] = env["HOME"], env["HOME"]
				env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "18000"
				lane := ""
				if scope == "lane" {
					lane = "build"
					env["SOHO_LANES"] = "on"
					env["SOHO_LANE_BUILD_ROLES"] = "writer,scouter"
					env["SOHO_LANE_BUILD_KIND"] = "opencode"
					env["SOHO_LANE_BUILD_PANES"] = "1"
				}
				sd := filepath.Join(state, "sX")
				if err := os.MkdirAll(sd, 0o700); err != nil {
					t.Fatal(err)
				}
				roster := filepath.Join(sd, "agents.tsv")
				const header = "# roster fixture\n"
				fields := []string{"worker", "t7", "opencode", "scouter", "opencode", "1", cwd, "now",
					opencodePrimaryModel, "full", "scouter", lane, "", "", "", "inherit"}
				original := header + strings.Join(fields, "\t") + "\n"
				if err := os.WriteFile(roster, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
				fields[1], fields[7] = "t9", "replacement"
				if policy == "filtered" {
					fields = append(fields, "", "", envDropPrefix+"DROP_TEST")
				}
				replacement := header + strings.Join(fields, "\t") + "\n"
				release := filepath.Join(t.TempDir(), "release-qualification")
				get := []string{"get", "t7", "--json"}
				rules := append(hcFixtureRules(),
					fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: opencodePrimaryRecord("t7")},
					fakecli.Rule{Argv: get, Stdout: opencodePrimaryRecord("t7"), WaitFile: release},
				)
				if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
					t.Fatal(err)
				}
				conn := newFakeSpawnRPCConn()
				done := make(chan struct{})
				var message, stdout, stderr string
				var exitCode int
				var calls []fakecli.Call
				t.Cleanup(func() {
					if err := os.WriteFile(release, nil, 0o600); err != nil {
						t.Error(err)
					}
					<-done
				})
				go func() {
					defer close(done)
					exit, out, errOut, observed := spawnHcExitWithConn(t, bin, []string{
						"writer", "--name", "worker", "--kind", "opencode", "--model", opencodePrimaryModel, "--approvals", "full", "--reuse",
					}, ctx, env, cwd, conn)
					if exit != nil {
						exitCode, message = exit.Code, exit.Msg
					}
					stderr, stdout, calls = errOut.String(), out.String(), observed
				}()
				dja659WaitForCallInFlight(t, filepath.Join(bin, "hc.calls.jsonl"), get)
				if err := os.WriteFile(roster, []byte(replacement), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(release, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				<-done
				after, err := os.ReadFile(roster)
				if err != nil {
					t.Fatal(err)
				}
				if exitCode != 4 || !strings.Contains(message, "roster changed while checking reusable worker") ||
					stdout != "" || string(after) != replacement || strings.Contains(stderr, "reusing idle") {
					t.Fatalf("replacement must survive without reuse: exit=%d message=%q stdout=%q stderr=%q roster=%q", exitCode, message, stdout, stderr, after)
				}
				if conn.getConnectCount() != 0 {
					t.Fatal("refusal attempted terminal creation")
				}
				for _, call := range calls {
					if !slices.Contains([]string{"get", "ls"}, call.Argv[0]) {
						t.Fatalf("refusal performed a terminal action: %v", call.Argv)
					}
				}
			})
		}
	}
}

func TestEnvDropRosterCodec(t *testing.T) {
	legacy := make([]string, envDropColumn)
	legacy[16], legacy[17] = "4444", "approval column"
	names, err := RosterEnvDrop(legacy)
	if names != nil || err != nil {
		t.Fatalf("legacy row acquired a policy: %v %v", names, err)
	}
	var sixteen []string
	for i := 0; i < 16; i++ {
		sixteen = append(sixteen, fmt.Sprintf("DROP_%d", i))
	}
	marker := envDropPrefix + strings.Join(sixteen, ",")
	names, err = RosterEnvDrop(append(legacy, marker))
	if !slices.Equal(names, sixteen) || err != nil {
		t.Fatalf("16 names parsed as %v %v", names, err)
	}
	for _, malformed := range []string{
		"env-drop:v2:A", envDropPrefix, envDropPrefix + "A,A", envDropPrefix + "A,a",
		marker + ",EXTRA", envDropPrefix + strings.Repeat("X", envDropMarkerSize),
	} {
		if names, err := RosterEnvDrop(append(legacy, malformed)); err == nil || names != nil {
			t.Fatal("invalid roster marker accepted")
		}
	}
}

func TestEnvDropRosterPreservedByWorkerUpdates(t *testing.T) {
	dir := t.TempDir()
	fields := []string{"worker", "t7", "pi", "implementer", "", "1", dir, "now", "", "auto", "implementer", "", "", "", "", "inherit", "4444", "", envDropPrefix + "ONE,TWO"}
	core.RosterAppend(dir, fields)
	core.RosterSetRole(dir, "worker", "reviewer")
	core.RosterSetModel(dir, "worker", "provider/model", "low")
	core.RosterReplacePane(dir, "t7", "t8")
	ok, err := core.RosterRenameIf(dir, "worker", "renamed", core.RosterLine(dir, "worker"))
	if !ok || err != nil {
		t.Fatalf("rename = %v, %v", ok, err)
	}
	row := strings.Split(core.RosterLine(dir, "renamed"), "\t")
	names, err := RosterEnvDrop(row)
	if !slices.Equal(names, []string{"ONE", "TWO"}) || err != nil || core.RosterE2EPort(row) != "4444" || row[1] != "t8" {
		t.Fatalf("rewritten row lost identity or policy: %q, %v", row, err)
	}
}
