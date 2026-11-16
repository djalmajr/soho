package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorDoneOpen(t *testing.T) {
	fixed := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	oldNow := platform.Now
	platform.Now = func() time.Time { return fixed }
	t.Cleanup(func() { platform.Now = oldNow })
	for _, mode := range []string{"herdr", "hc"} {
		for _, tc := range []struct {
			name                                                                 string
			age                                                                  time.Duration
			pending, noReport, listingFailure, gone, caller, working, statusOnly bool
			want                                                                 bool
		}{
			{name: "done31", age: 31 * time.Minute, want: true},
			{name: "statusDone31", age: 31 * time.Minute, statusOnly: true, want: true},
			{name: "done29", age: 29 * time.Minute},
			{name: "exact30", age: 30 * time.Minute},
			{name: "newDispatch", age: 31 * time.Minute, pending: true},
			{name: "notReceived", age: 31 * time.Minute, noReport: true},
			{name: "quota", age: 31 * time.Minute, noReport: true},
			{name: "terminalGone", age: 31 * time.Minute, gone: true},
			{name: "listingFailure", age: 31 * time.Minute, listingFailure: true},
			{name: "orchestrator", age: 31 * time.Minute, caller: true},
			{name: "draftWorking", age: 31 * time.Minute, working: true, statusOnly: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				sd := filepath.Join(root, "state", "ws")
				for _, dir := range []string{bin, filepath.Join(sd, "wait"), filepath.Join(sd, "reports")} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				pane, caller := "p2", "p1"
				if mode == "hc" {
					pane, caller = "t2", "t1"
				}
				if tc.caller {
					pane = caller
				}
				write := func(path, body string) {
					t.Helper()
					if err := os.WriteFile(path, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				write(filepath.Join(sd, "agents.tsv"), "worker\t"+pane+"\tpi\timplementer\n")
				report := filepath.Join(sd, "reports", "worker.md")
				write(report, "report\n")
				at := fixed.Add(-tc.age)
				if err := os.Chtimes(report, at, at); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(report)
				if err != nil {
					t.Fatal(err)
				}
				pointer := report
				if tc.pending || tc.noReport {
					pointer = filepath.Join(sd, "reports", "new.md")
				}
				write(filepath.Join(sd, "last-report-worker"), pointer+"\n")
				if !tc.statusOnly {
					write(filepath.Join(sd, "wait", "worker.report-settled"), fmt.Sprintf("%d\t%d\t%q\n", info.Size(), info.ModTime().UnixNano(), report))
				}
				if tc.name == "notReceived" {
					write(filepath.Join(sd, "wait", "worker.not-received"), "0\n")
				}
				if tc.name == "quota" {
					write(filepath.Join(sd, "wait", "worker.quota"), "quota\n")
				}
				state := "idle"
				if tc.working {
					state = "working"
				}
				legacyList := `{"result":{"agents":[{"name":"worker","pane_id":"` + pane + `","agent_status":"` + state + `"}]}}`
				hcList := `{"terminals":[{"address":"` + pane + `","name":"worker","state":"live","status":{"state":"` + state + `"}}]}`
				if tc.gone {
					legacyList = `{"result":{"agents":[]}}`
					hcList = `{"terminals":[]}`
				}
				listCode := 0
				if tc.listingFailure {
					listCode = 1
				}
				legacyRules := []fakecli.Rule{
					{Argv: []string{"agent", "list"}, Stdout: legacyList, Code: listCode},
					{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + state + `"}}}`},
				}
				hcRules := []fakecli.Rule{
					{Argv: []string{"get", caller, "--json"}, Stdout: `{"address":"` + caller + `","state":"live","labels":{"soho.session":"ws"}}`},
					{Argv: []string{"ls", "--json"}, Stdout: hcList, Code: listCode},
					{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"` + pane + `","name":"worker","state":"live","status":{"state":"` + state + `"}}`},
				}
				name, rules := "herdr", legacyRules
				if mode == "hc" {
					name, rules = "hc", hcRules
				}
				if _, err := fakecli.Install(t, bin, name, rules); err != nil {
					t.Fatal(err)
				}
				env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_TRANSPORT": mode, "SOHO_SKILL_DIR": filepath.Join(root, "skill"), "HERDR_ENV": "1", "HERDR_PANE_ID": caller, "HERDR_WORKSPACE_ID": "ws"}
				if mode == "hc" {
					env["HC_TERMINAL_ID"] = caller
				}
				ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
				var out strings.Builder
				doctorDoneOpen(ctx, backend.New(env), env, root, &Say{Out: &out})
				want := "warn   worker 'worker' finished its task 31 min ago and is still open: release it (soho release worker --close), or keep it with a reason you state to the user\n"
				if tc.want && out.String() != want {
					t.Fatalf("got %q want %q", out.String(), want)
				}
				if !tc.want && out.Len() != 0 {
					t.Fatalf("unexpected warning %q", out.String())
				}
				calls, err := fakecli.ReadCalls(filepath.Join(bin, name+".calls.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				lists := 0
				for _, call := range calls {
					if len(call.Argv) > 0 && (call.Argv[0] == "ls" || (len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "list")) {
						lists++
					}
				}
				if lists != 1 {
					t.Fatalf("list calls=%d, want 1: %v", lists, calls)
				}
			})
		}
	}
}
