package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnUnsandboxedApprovals(t *testing.T) {
	for _, source := range []string{"flag", "lane", "role", "global"} {
		t.Run(source, func(t *testing.T) {
			env, bin, state := laneKindFlagEnv(t)
			env["HOME"], env["USERPROFILE"], env["XDG_CONFIG_HOME"], env["TMPDIR"] = t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			args := []string{"spawn", "scouter", "--kind", "codex"}
			if source == "lane" {
				env["SOHO_LANE_BUILD_KIND"] = "codex"
				env["SOHO_LANE_BUILD_APPROVALS"] = "unsandboxed"
			} else {
				env["SOHO_LANES"] = "off"
				switch source {
				case "flag":
					args = append(args, "--approvals", "unsandboxed")
				case "role":
					env["SOHO_ROLE_SCOUTER_APPROVALS"] = "unsandboxed"
				case "global":
					env["SOHO_APPROVALS"] = "unsandboxed"
				}
			}
			r := runLaneKindFlag(t, env, bin, state, args...)
			if r.code != 0 || !strings.Contains(r.stdout, `"approvals": "unsandboxed"`) {
				t.Fatalf("code=%d out=%s stderr=%s", r.code, r.stdout, r.stderr)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			started := false
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
					started = true
					if !strings.Contains(strings.Join(call.Argv, " "), "-- -s danger-full-access -a never") {
						t.Fatalf("argv=%q", call.Argv)
					}
				}
			}
			if !started {
				t.Fatal("no agent start")
			}
			found := false
			for _, row := range strings.Split(r.roster, "\n") {
				cols := strings.Split(row, "\t")
				if len(cols) > 9 && cols[2] == "codex" {
					found = true
					if cols[9] != "unsandboxed" {
						t.Fatalf("roster=%q", row)
					}
				}
			}
			if !found {
				t.Fatalf("no codex roster row: %q", r.roster)
			}
		})
	}
}

func TestConfigUnsandboxedApprovals(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	for _, key := range []string{"approvals", "role.scouter.approvals", "lane.build.approvals"} {
		for _, value := range []string{"ask", "edits", "full", "unsandboxed", "UNKNOWN"} {
			code, out, stderr := runIn(t, []string{"config", "set", key, value}, env, cwd)
			want := 0
			if value == "UNKNOWN" {
				want = 2
			}
			if code != want {
				t.Fatalf("%s=%s code=%d want=%d out=%q stderr=%q", key, value, code, want, out, stderr)
			}
		}
	}
}

func TestDoctorUnsandboxedApprovals(t *testing.T) {
	for _, value := range []string{"ask", "edits", "full", "unsandboxed"} {
		t.Run(value, func(t *testing.T) {
			env, cwd := tm6CLIEnv(t)
			keys := []string{"approvals", "role.scouter.approvals", "lane.build.approvals"}
			conf := ""
			for _, key := range keys {
				conf += key + "=" + value + "\n"
			}
			tm6Write(t, filepath.Join(cwd, ".agents", "soho.conf"), conf)
			code, out, stderr := runIn(t, []string{"doctor"}, env, cwd)
			if code != 0 {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			for _, key := range keys {
				want := "warn   config: " + key + "=unsandboxed; workers run without a sandbox by the owner's choice"
				if strings.Contains(out, want) != (value == "unsandboxed") {
					t.Fatalf("key=%s value=%s out=%s", key, value, out)
				}
			}
			// The effective environment value must replace the file's value.
			for _, key := range []string{"SOHO_APPROVALS", "SOHO_ROLE_SCOUTER_APPROVALS", "SOHO_LANE_BUILD_APPROVALS"} {
				if value == "unsandboxed" {
					env[key] = "full"
				} else {
					env[key] = "unsandboxed"
				}
			}
			_, out, _ = runIn(t, []string{"doctor"}, env, cwd)
			if strings.Contains(out, "workers run without a sandbox by the owner's choice") != (value != "unsandboxed") {
				t.Fatalf("override value=%s out=%s", value, out)
			}
		})
	}
}

func TestDispatchUnsandboxedRosterApprovals(t *testing.T) {
	for _, mode := range []string{"full", "unsandboxed"} {
		for _, amend := range []bool{false, true} {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			f.env["HOME"], f.env["USERPROFILE"], f.env["XDG_CONFIG_HOME"] = f.root, f.root, f.root
			roster := filepath.Join(f.state, "ws", "agents.tsv")
			raw := mustRead(t, roster)
			tm6Write(t, roster, strings.Replace(raw, "\ttask\timplementer", "\t"+mode+"\timplementer", 1))
			args := []string{"worker", f.brief, "--no-wait"}
			if amend {
				if code, out, stderr := f.run(t, args...); code != 0 {
					t.Fatalf("initial dispatch code=%d out=%s stderr=%s", code, out, stderr)
				}
				args = append(args, "--amend")
			}
			code, out, stderr := f.run(t, args...)
			if code != 0 {
				t.Fatalf("mode=%s amend=%v code=%d out=%s stderr=%s", mode, amend, code, out, stderr)
			}
			composed, ok := dispatchOutputField(t, out, "composed_prompt")
			if !ok {
				t.Fatalf("no prompt: %s", out)
			}
			body, err := os.ReadFile(composed)
			if err != nil {
				t.Fatal(err)
			}
			for _, note := range []string{"Your sandbox cannot write under .git", "Your sandbox has no network"} {
				if strings.Contains(string(body), note) != (mode == "full") {
					t.Fatalf("mode=%s amend=%v note=%q prompt=%s", mode, amend, note, body)
				}
			}
		}
	}
}
