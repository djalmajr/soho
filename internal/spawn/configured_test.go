package spawn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const spawnCapability = `{"running":true,"capabilities":{"native_agent_launch_v1":true}}`
const spawnPrivate = "SYNTHETIC_PRIVATE_CONFIG_R28"

const spawnReuseReady = `{"result":{"type":"agent_info","agent":{"name":"old","agent":"opencode","pane_id":"p-old","terminal_id":"native-terminal","agent_status":"idle","interactive_ready":true,"launch_profile":"opencode-primary-v2"}}}`

func TestConfiguredSpawnReuseConsumer(t *testing.T) {
	for _, mode := range []struct {
		label, requestedName string
	}{
		{"ordinary", ""},
		{"lane-same-name", "old"},
		{"lane-distinct-name", "new"},
	} {
		for _, tc := range []struct {
			name, body string
			qualified  bool
		}{
			{"legacy", strings.Replace(spawnReuseReady, `,"launch_profile":"opencode-primary-v2"`, "", 1), false},
			{"qualified", spawnReuseReady, true},
			{"wrong-profile", strings.Replace(spawnReuseReady, "opencode-primary-v2", "legacy", 1), false},
			{"wrong-pane", strings.Replace(spawnReuseReady, "p-old", "p-other", 1), false},
			{"wrong-name", strings.Replace(spawnReuseReady, `"old"`, `"other"`, 1), false},
			{"wrong-kind", strings.Replace(spawnReuseReady, `"opencode"`, `"codex"`, 1), false},
			{"empty-terminal", strings.Replace(spawnReuseReady, `"native-terminal"`, `""`, 1), false},
			{"not-ready", strings.Replace(spawnReuseReady, `"interactive_ready":true`, `"interactive_ready":false`, 1), false},
			{"pending", strings.Replace(spawnReuseReady, `"interactive_ready":true`, `"interactive_ready":true,"launch_pending":true`, 1), false},
			{"malformed-native-zero", `{}`, false},
		} {
			distinctName := mode.requestedName == "new"
			if distinctName && !tc.qualified {
				continue
			}
			t.Run(mode.label+"/"+tc.name, func(t *testing.T) {
				body, qualified := tc.body, tc.qualified
				f := configuredFixture(t, []fakecli.Rule{
					{Argv: []string{"status", "server", "--json"}, Stdout: spawnCapability},
					idleHerdrRule("old"),
					{Argv: []string{"agent", "get", "p-old"}, Stdout: body},
					{Argv: []string{"agent", "rename", "p-old", "new"}, Stdout: `{"result":{}}`},
				})
				args := append(configuredArgs(""), "--effort", "high")
				row := []string{"old", "p-old", "opencode", "scouter", "opencode", "1", f.cwd, "now", "provider/model#variant", "ask", "scouter", "", "", "", "high"}
				if mode.requestedName != "" {
					f.env["SOHO_LANES"] = "on"
					for k, v := range map[string]string{"lane_build_roles": "worker", "lane_build_kind": "opencode", "lane_build_panes": "1"} {
						f.ctx.Entries[k] = core.ConfigEntry{Value: v, Source: "project"}
						f.ctx.Order = append(f.ctx.Order, k)
					}
					row[11] = "build"
					args = append(args, "--name", mode.requestedName)
				}
				f.roster(t, strings.Join(row, "\t"))
				sd := filepath.Join(f.state, "ws")
				report := filepath.Join(sd, "original-report.md")
				before := map[string][]byte{}
				for p, b := range map[string]string{report: "original report\n", core.LastReportPath(sd, "old"): report + "\n", taskreport.TaskReportPointerPath(sd, "old"): report + "\n", filepath.Join(sd, "task-old"): "original task\n"} {
					if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				for _, p := range []string{filepath.Join(sd, "agents.tsv"), report, core.LastReportPath(sd, "old"), taskreport.TaskReportPointerPath(sd, "old"), filepath.Join(sd, "task-old")} {
					b, err := os.ReadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					before[p] = b
				}
				// CmdSpawn states a refusal through the thrown ExitError that the
				// outer CLI handler prints, so one call keeps that message beside
				// the platform output actually written by this consumer.
				code, message, out, stderr, calls := runCmdSpawnWithExitMessage(t, f, args)
				switch {
				case distinctName:
					if code != 10 || message != "" || !strings.Contains(stderr, "an explicit new name requires a new worker") ||
						!strings.Contains(stderr, "lane 'build' has no room for a new worker (1 of 1)") {
						t.Errorf("exit=%d message=%q out=%q stderr=%q calls=%+v", code, message, out, stderr, calls)
					}
					obj := parseOneSpawnJSONLine(t, out)
					spawnOneLineField(t, obj, "status", "busy")
					spawnOneLineField(t, obj, "lane", "build")
					spawnOneLineField(t, obj, "name", "old")
				case !qualified:
					if code != 4 || message != "spawn: reusable configured worker profile is unverified" || out != "" {
						t.Errorf("exit=%d message=%q out=%q stderr=%q calls=%+v", code, message, out, stderr, calls)
					}
				default:
					var got struct {
						Name     string
						Pane     string `json:"pane_id"`
						Reused   bool
						Status   string
						Profile  string `json:"launch_profile"`
						Previous string `json:"previous_role"`
					}
					if err := json.Unmarshal([]byte(out), &got); err != nil {
						t.Fatal(err)
					}
					if code != 0 || message != "" || got.Name != "old" || got.Pane != "p-old" || !got.Reused || got.Status != "ready" || got.Profile != kinds.OpenCodePrimaryProfile || got.Previous != "scouter" {
						t.Errorf("exit=%d message=%q out=%q stderr=%q", code, message, out, stderr)
					}
				}
				wantReuse := qualified && !distinctName
				for p, want := range before {
					if wantReuse && p == filepath.Join(sd, "agents.tsv") {
						continue
					}
					got, err := os.ReadFile(p)
					if err != nil || string(got) != string(want) {
						t.Errorf("selected-worker state changed: %s got=%q err=%v", p, got, err)
					}
				}
				if core.RosterLine(sd, "new") != "" {
					t.Error("published renamed worker")
				}
				if wantReuse {
					row[3], row[10] = "worker", "scouter,worker"
					if got := core.RosterRows(sd); !reflect.DeepEqual(got, []string{strings.Join(row, "\t")}) {
						t.Errorf("configured reuse should only update the role and its history: roster=%q", got)
					}
				}
				observed, renamed := 0, 0
				for _, c := range calls {
					a := strings.Join(c.Argv, " ")
					if a == "agent get p-old" {
						observed++
					}
					if strings.HasPrefix(a, "agent rename ") {
						renamed++
					}
					if c.Stdin != "" || strings.HasPrefix(a, "agent start") || strings.HasPrefix(a, "agent send-") || strings.HasPrefix(a, "agent prompt") || strings.HasPrefix(a, "pane send-") || strings.HasPrefix(a, "pane close") || strings.HasPrefix(a, "tab create") || strings.HasPrefix(a, "pane split") {
						t.Errorf("reuse effect: %+v", c)
					}
				}
				wantObservations := 1
				if distinctName {
					wantObservations = 0
				}
				if observed != wantObservations || renamed != 0 {
					t.Errorf("observations=%d renames=%d calls=%+v", observed, renamed, calls)
				}
				for _, public := range []string{out, message, stderr, core.RosterLine(sd, "old"), core.RosterLine(sd, "new")} {
					if strings.Contains(public, spawnPrivate) || strings.Contains(public, "OPENCODE_CONFIG") {
						t.Error("private config leaked")
					}
				}
			})
		}
	}
}

func configuredFixture(t *testing.T, rules []fakecli.Rule) spawnFixture {
	t.Helper()
	f := newSpawnFixture(t, append(rules, freshSpawnRules()...))
	f.env["TMPDIR"] = f.root
	f.env["SOHO_LAYOUT"] = "tab"
	f.env["SOHO_REGRID"] = "off"
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	f.env["SOHO_OPENCODE_TUI_PROFILE"] = "v2"
	f.env["OPENCODE_CONFIG_CONTENT"] = `{"synthetic":"` + spawnPrivate + `","unicode":"雪�","large":9007199254740993}`
	f.env["OPENCODE_CONFIG_DIR"] = filepath.Join(f.root, "space 'quoted' 雪")
	if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: opencode\napprovals: ask\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, f.bin, "opencode", nil); err != nil {
		t.Fatal(err)
	}
	// Enable the existing real subprocess recorder, including private stdin.
	p := filepath.Join(f.bin, "herdr.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var script map[string]any
	if err := json.Unmarshal(b, &script); err != nil {
		t.Fatal(err)
	}
	script["capture_stdin"] = true
	b, err = json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func configuredArgs(pane string) []string {
	a := []string{"worker", "--model", "provider/model#variant"}
	if pane != "" {
		a = append(a, "--pane", pane)
	}
	return a
}

func configuredExit(fn func()) (code int, message string) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*platform.ExitError); ok {
				code, message = e.Code, e.Msg
			} else {
				panic(r)
			}
		}
	}()
	fn()
	return
}

// runCmdSpawnWithExitMessage runs CmdSpawn once and keeps both the platform
// output it writes and the ExitError code/message it throws, which the outer CLI
// handler prints for the caller.
func runCmdSpawnWithExitMessage(t *testing.T, f spawnFixture, args []string) (code int, message, stdout, stderr string, calls []fakecli.Call) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut strings.Builder
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code, message, calls = runCmdSpawnExpectError(t, f, args)
	return code, message, out.String(), errOut.String(), calls
}

func TestConfiguredSpawnPreflightBeforeEffects(t *testing.T) {
	for _, cap := range []struct {
		name, body string
		code       int
	}{
		{"old", `{}`, 0}, {"absent", `{"running":true}`, 0},
		{"false", `{"running":true,"capabilities":{"native_agent_launch_v1":false}}`, 0},
		{"native-error", spawnCapability, 7}, {"malformed-native-zero", `null`, 0},
	} {
		for _, pane := range []string{"", "p-target"} {
			t.Run(cap.name+"/"+pane, func(t *testing.T) {
				f := configuredFixture(t, []fakecli.Rule{{Argv: []string{"status", "server", "--json"}, Stdout: cap.body, Code: cap.code}})
				f.roster(t, spawnRosterRow("old", "worker", "old-model", "ask", ""))
				sd := filepath.Join(f.state, "ws")
				paths := []string{filepath.Join(sd, "agents.tsv"), core.LastReportPath(sd, "worker"), taskreport.TaskReportPointerPath(sd, "worker"), filepath.Join(sd, "task-worker")}
				before := map[string]string{}
				for i, p := range paths {
					if i > 0 {
						if err := os.WriteFile(p, []byte("original bytes\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					b, err := os.ReadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					before[p] = string(b)
				}
				code, out, stderr, calls := runCmdSpawn(t, f, configuredArgs(pane))
				if code != 4 || out != "" || strings.Contains(stderr, spawnPrivate) {
					t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
				}
				if len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, []string{"status", "server", "--json"}) || calls[0].Stdin != "" {
					t.Fatalf("effects before refusal: %+v", calls)
				}
				for p, want := range before {
					b, err := os.ReadFile(p)
					if err != nil || string(b) != want {
						t.Fatalf("changed %s: %q %v", p, b, err)
					}
				}
			})
		}
	}
}

func TestConfiguredSpawnInvalidBeforeCapability(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		tail             []string
	}{
		{name: "selector", key: "SOHO_OPENCODE_TUI_PROFILE", value: spawnPrivate},
		{name: "model", tail: []string{"--model", "malformed"}},
		{name: "config", key: "OPENCODE_CONFIG_CONTENT", value: `{"synthetic":`},
		{name: "directory", key: "OPENCODE_CONFIG_DIR", value: "bad\x00dir"},
		{name: "configured-args", key: "SOHO_ARGS_OPENCODE", value: "--model private"},
		{name: "trailing-args", tail: []string{"--", "-m", "private"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := configuredFixture(t, nil)
			if tc.key != "" {
				f.env[tc.key] = tc.value
			}
			args := append(configuredArgs("p-target"), tc.tail...)
			code, out, stderr, calls := runCmdSpawn(t, f, args)
			if code != 2 || out != "" || len(calls) != 0 || strings.Contains(stderr, spawnPrivate) {
				t.Fatalf("code=%d output=%q stderr=%q calls=%+v", code, out, stderr, calls)
			}
		})
	}
}

func TestConfiguredSpawnConsumer(t *testing.T) {
	for _, placement := range []string{"given", "created", "adopted"} {
		for _, outcome := range []string{"ready", "revoked", "malformed", "busy", "notready", "failed"} {
			t.Run(placement+"/"+outcome, func(t *testing.T) {
				pane := "p-target"
				if placement == "created" {
					pane = "p-new"
				}
				if placement == "adopted" {
					pane = "p-root"
				}
				body := fmt.Sprintf(`{"id":"configured-start","result":{"type":"agent_configured_started","agent":{"name":"worker","agent":"opencode","pane_id":%q,"terminal_id":"actual-terminal","agent_status":"idle","interactive_ready":true,"launch_profile":"opencode-primary-v2"}}}`, pane)
				startCode := 0
				switch outcome {
				case "malformed":
					body = `{"id":"configured-start","result":{}}`
				case "busy", "notready":
					wireCode := "agent_pane_busy"
					if outcome == "notready" {
						wireCode = "agent_not_ready"
					}
					body = fmt.Sprintf(`{"id":"configured-start","error":{"code":%q,"message":%q}}`, wireCode, spawnPrivate)
					startCode = 7
				case "failed":
					body = spawnPrivate
					startCode = 7
				}
				rules := []fakecli.Rule{}
				if outcome == "revoked" {
					rules = append(rules, fakecli.Rule{Argv: []string{"status", "server", "--json"}, Call: 2, Stdout: `{"running":true}`})
				}
				rules = append(rules, fakecli.Rule{Argv: []string{"status", "server", "--json"}, Stdout: spawnCapability}, fakecli.Rule{Argv: []string{"agent", "start-configured"}, ArgvPrefix: true, Stdout: body, Code: startCode})
				f := configuredFixture(t, rules)
				if placement == "adopted" {
					if err := os.WriteFile(filepath.Join(f.state, "ws", "herd-tab"), []byte("t-herd\therd\tmanual\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				argPane := ""
				if placement == "given" {
					argPane = pane
				}
				code, out, stderr, calls := runCmdSpawn(t, f, configuredArgs(argPane))
				wantCode := 4
				if outcome == "ready" {
					wantCode = 0
				}
				if outcome == "notready" {
					wantCode = 7
				}
				if code != wantCode {
					t.Fatalf("code=%d want=%d out=%q stderr=%q calls=%+v", code, wantCode, out, stderr, calls)
				}
				probes, starts, closes, placementIndex, secondProbe := 0, 0, 0, -1, -1
				for i, c := range calls {
					a := strings.Join(c.Argv, " ")
					if a == "status server --json" {
						probes++
						if probes == 2 {
							secondProbe = i
						}
						if c.Stdin != "" {
							t.Fatal("probe carried stdin")
						}
						continue
					}
					if strings.HasPrefix(a, "tab create ") || strings.HasPrefix(a, "pane split ") {
						placementIndex = i
					}
					if strings.HasPrefix(a, "pane close ") {
						closes++
						if !reflect.DeepEqual(c.Argv, []string{"pane", "close", pane}) {
							t.Fatalf("wrong close: %q", c.Argv)
						}
					}
					if strings.HasPrefix(a, "agent start ") || strings.HasPrefix(a, "pane send-text ") {
						t.Fatalf("legacy effect: %q", c.Argv)
					}
					if strings.Contains(a, spawnPrivate) || strings.Contains(a, "OPENCODE_") || strings.Contains(a, "provider/model") || strings.Contains(a, " -m ") {
						t.Fatalf("private argv: %q", c.Argv)
					}
					if strings.HasPrefix(a, "agent start-configured ") {
						starts++
						want := []string{"agent", "start-configured", "worker", "--kind", "opencode", "--pane", pane, "--timeout", "60000"}
						if !reflect.DeepEqual(c.Argv, want) || i != secondProbe+1 {
							t.Fatalf("start order/metadata: %q", c.Argv)
						}
						assertConfiguredFrame(t, c.Stdin, pane, f.env["OPENCODE_CONFIG_DIR"])
					}
				}
				wantStarts := 1
				if outcome == "revoked" {
					wantStarts = 0
				}
				wantCloses := 0
				if outcome == "revoked" && placement == "created" {
					wantCloses = 1
				}
				if probes != 2 || starts != wantStarts || closes != wantCloses || len(calls) == 0 || strings.Join(calls[0].Argv, " ") != "status server --json" {
					t.Fatalf("probes=%d starts=%d closes=%d calls=%+v", probes, starts, closes, calls)
				}
				if placement == "created" && (placementIndex <= 0 || secondProbe <= placementIndex) {
					t.Fatal("capability/placement order")
				}
				roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				managed := outcome == "ready" || outcome == "notready"
				if strings.Contains(string(roster), "worker\t"+pane+"\topencode") != managed {
					t.Fatalf("managed=%v roster=%q", managed, roster)
				}
				for _, public := range []string{out, stderr, string(roster)} {
					if strings.Contains(public, spawnPrivate) || strings.Contains(public, "OPENCODE_CONFIG") || strings.Contains(public, "agents.build") {
						t.Fatalf("private public output: %q", public)
					}
				}
				if outcome == "ready" && (!spawnJSONHasField(t, out, `"status": "ready"`) || !spawnJSONHasField(t, out, `"launch_profile": "`+kinds.OpenCodePrimaryProfile+`"`)) {
					t.Fatalf("ready result: %q", out)
				}
				if !managed && out != "" {
					t.Fatalf("failure emitted success: %q", out)
				}
			})
		}
	}
}

func assertConfiguredFrame(t *testing.T, frame, pane, dir string) {
	t.Helper()
	const prefix = "HERDR_CONFIGURED_START_V1\n"
	if !strings.HasPrefix(frame, prefix) {
		t.Fatal("missing private frame")
	}
	var v struct {
		Method string `json:"method"`
		Params struct {
			Name, Kind string
			Pane       string `json:"pane_id"`
			Timeout    uint64 `json:"timeout_ms"`
			Args       []string
			Env        [][2]string
		}
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, prefix)), &v); err != nil {
		t.Fatal(err)
	}
	if v.Method != "agent.start_configured" || v.Params.Name != "worker" || v.Params.Kind != "opencode" || v.Params.Pane != pane || v.Params.Timeout != 60000 || !reflect.DeepEqual(v.Params.Args, []string{"--standalone"}) {
		t.Fatalf("frame metadata/args: %+v", v)
	}
	env := map[string]string{}
	previous := ""
	for _, p := range v.Params.Env {
		if p[0] <= previous {
			t.Fatal("overrides not strictly sorted")
		}
		previous = p[0]
		env[p[0]] = p[1]
	}
	if len(env) != 3 || env["OPENCODE_DISABLE_AUTOUPDATE"] != "1" || env["OPENCODE_CONFIG_DIR"] != dir {
		t.Fatalf("overrides=%v", env)
	}
	d := json.NewDecoder(strings.NewReader(env["OPENCODE_CONFIG_CONTENT"]))
	d.UseNumber()
	var cfg map[string]any
	if err := d.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["model"] != "provider/model#variant" || cfg["default_agent"] != "build" || cfg["synthetic"] != spawnPrivate || cfg["unicode"] != "雪�" || cfg["large"] != json.Number("9007199254740993") {
		t.Fatalf("private config policy: %v", cfg)
	}
}

type configuredPreflightRuntime struct {
	fakeUncertainTerminalRuntime
	pre  transport.Reply[bool]
	seen []transport.Start
}

type configuredReuseRuntime struct {
	fakeUncertainTerminalRuntime
	reply transport.Reply[bool]
	seen  []transport.Start
	call  transport.Call
}

func (f *configuredReuseRuntime) PreflightConfiguredReuse(_ context.Context, s transport.Start, c transport.Call) transport.Reply[bool] {
	f.seen = append(f.seen, s)
	f.call = c
	return f.reply
}

func TestConfiguredSpawnReuseReply(t *testing.T) {
	z := 0
	o := spawnOptions{name: "requested-new-name", pane: "", timeout: "60000", launchProfile: kinds.LaunchProfile{Name: kinds.OpenCodePrimaryProfile, EnvOverrides: map[string]string{"explicit": "frozen"}}}
	line := "actual-old-name\tactual-pane\topencode"
	args := []string{"--standalone"}
	for _, reply := range []transport.Reply[bool]{{Value: true, Process: transport.ProcessResult{Status: &z}}, {Valid: true}, {Valid: true, Value: true, Err: &transport.Failure{Kind: transport.FailureProtocol}}, {Valid: true, Value: true}} {
		rt := &configuredReuseRuntime{reply: reply}
		profile := ""
		code, msg := configuredExit(func() { profile = gateConfiguredReuse(rt, line, o, "opencode", args, platform.Env{}) })
		allow := reply.Valid && reply.Value && reply.Err == nil
		if (code == 0) != allow || (!allow && (code != 4 || msg != "spawn: reusable configured worker profile is unverified")) || (profile == kinds.OpenCodePrimaryProfile) != allow {
			t.Fatalf("exit=%d msg=%q profile=%q", code, msg, profile)
		}
		want := transport.Start{Name: "actual-old-name", PaneID: "actual-pane", Kind: "opencode", Timeout: "60000", Args: args, LaunchProfile: o.launchProfile.Name, EnvOverrides: o.launchProfile.EnvOverrides}
		if len(rt.seen) != 1 || !reflect.DeepEqual(rt.seen[0], want) || rt.call != configuredSpawnCall(o) || len(rt.startCalls) != 0 {
			t.Fatalf("seen=%+v call=%+v", rt.seen, rt.call)
		}
	}
	code, msg := configuredExit(func() {
		gateConfiguredReuse(&fakeUncertainTerminalRuntime{}, line, o, "opencode", args, platform.Env{})
	})
	if code != 4 || msg != "spawn: reusable configured worker profile is unverified" {
		t.Fatalf("missing interface exit=%d msg=%q", code, msg)
	}
	for _, mode := range []string{"hc", "legacy"} {
		rt := &configuredReuseRuntime{}
		env := platform.Env{}
		copy := o
		if mode == "hc" {
			env["SOHO_TRANSPORT"] = "hc"
		} else {
			copy.launchProfile = kinds.LaunchProfile{}
		}
		if got := gateConfiguredReuse(rt, line, copy, "opencode", args, env); got != "" || len(rt.seen) != 0 {
			t.Fatal("legacy/HC invoked TUI qualification")
		}
	}
}

func (f *configuredPreflightRuntime) PreflightConfiguredStart(_ context.Context, s transport.Start, _ transport.Call) transport.Reply[bool] {
	f.seen = append(f.seen, s)
	return f.pre
}

func TestConfiguredSpawnPreflightReply(t *testing.T) {
	zero := 0
	for _, tc := range []struct {
		name  string
		r     transport.Reply[bool]
		allow bool
	}{
		{"native-zero-invalid", transport.Reply[bool]{Value: true, Process: transport.ProcessResult{Status: &zero}}, false},
		{"false", transport.Reply[bool]{Valid: true}, false},
		{"error", transport.Reply[bool]{Value: true, Valid: true, Err: &transport.Failure{Kind: transport.FailureUnavailable}}, false},
		{"accepted", transport.Reply[bool]{Value: true, Valid: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &configuredPreflightRuntime{pre: tc.r}
			o := spawnOptions{timeout: "60000", launchProfile: kinds.LaunchProfile{Name: kinds.OpenCodePrimaryProfile}}
			code, msg := configuredExit(func() { preflightConfiguredSpawn(rt, o, "opencode", []string{"--standalone"}) })
			if (code == 0) != tc.allow || (!tc.allow && (code != 4 || msg != "spawn: configured launch preflight refused")) || len(rt.seen) != 1 || rt.seen[0].Name != "" || rt.seen[0].PaneID != "" {
				t.Fatalf("code=%d msg=%q seen=%+v", code, msg, rt.seen)
			}
		})
	}
	code, msg := configuredExit(func() { preflightConfiguredSpawn(&fakeUncertainTerminalRuntime{}, spawnOptions{}, "opencode", nil) })
	if code != 4 || msg != "spawn: configured launch preflight refused" {
		t.Fatalf("missing interface code=%d msg=%q", code, msg)
	}
}

func TestConfiguredSpawnStartAdverseReplies(t *testing.T) {
	zero := 0
	for _, tc := range []struct {
		name string
		r    transport.Reply[string]
		safe bool
	}{
		{"native-zero-invalid", transport.Reply[string]{Process: transport.ProcessResult{Status: &zero}}, false},
		{"timeout", transport.Reply[string]{Process: transport.ProcessResult{Status: &zero, TimedOut: true}, Err: &transport.Failure{Kind: transport.FailureRunOutcomeUnknown}}, false},
		{"unknown", transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureRunOutcomeUnknown}}, false},
		{"notfound", transport.Reply[string]{Process: transport.ProcessResult{NotFound: true}, Err: &transport.Failure{Kind: transport.FailureCommand}}, true},
		{"protocol", transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureProtocol}}, true},
	} {
		for _, owned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/owned=%v", tc.name, owned), func(t *testing.T) {
				rt := &fakeUncertainTerminalRuntime{reply: tc.r}
				tr := &fakeUncertainTransport{}
				o := spawnOptions{name: "worker", pane: "actual-pane", timeout: "60000", launchProfile: kinds.LaunchProfile{Name: kinds.OpenCodePrimaryProfile, EnvOverrides: map[string]string{"private": "value"}}}
				code, _ := configuredExit(func() {
					startConfiguredSpawn(transport.Services{Terminal: rt, Runtime: tr}, o, "opencode", owned, []string{"--standalone"})
				})
				wantClose := 0
				if owned && tc.safe {
					wantClose = 1
				}
				if code != 4 || len(rt.startCalls) != 1 || len(tr.closeCalls) != wantClose {
					t.Fatalf("exit=%d start=%+v close=%+v", code, rt.startCalls, tr.closeCalls)
				}
				if wantClose == 1 && tr.closeCalls[0].ID != "actual-pane" {
					t.Fatal("wrong close target")
				}
				if !reflect.DeepEqual(rt.startCalls[0], configuredSpawnRequest(o, "opencode", []string{"--standalone"})) {
					t.Fatal("request changed")
				}
			})
		}
	}
}

func TestConfiguredSpawnProfileRouting(t *testing.T) {
	ctx := &core.Config{}
	for _, tc := range []struct {
		kind, mode, selector, model string
		named                       bool
		bad                         bool
	}{
		{"grok", "", "invalid", "", false, false}, {"opencode", "", "", "bad", false, false},
		{"opencode", "", "legacy", "bad", false, false}, {"opencode", "hc", "invalid", "provider/model", true, false},
		{"opencode", "", "v2", "provider/model", true, false}, {"opencode", "herdr", "v2", "provider/model", true, false},
		{"opencode", "unknown", "v2", "provider/model", false, true},
	} {
		t.Run(tc.kind+"/"+tc.mode+"/"+tc.selector, func(t *testing.T) {
			env := platform.Env{"SOHO_TRANSPORT": tc.mode, "SOHO_OPENCODE_TUI_PROFILE": tc.selector}
			p, err := resolveSpawnLaunchProfile(tc.kind, tc.model, "", ctx, env)
			if (err != nil) != tc.bad || (p.Name != "") != tc.named {
				t.Fatalf("profile=%+v err=%v", p, err)
			}
			if tc.kind != "opencode" && !reflect.DeepEqual(p, kinds.LaunchProfile{}) {
				t.Fatal("other kind read selector")
			}
		})
	}
}

type configuredCloseTransport struct {
	fakeUncertainTransport
	reply transport.Reply[bool]
}

func (f *configuredCloseTransport) Close(_ context.Context, target transport.Target, _ transport.Call) transport.Reply[bool] {
	f.closeCalls = append(f.closeCalls, target)
	return f.reply
}

func TestConfiguredSpawnCloseRequiresValidReply(t *testing.T) {
	for _, reply := range []transport.Reply[bool]{
		{Value: true},
		{Valid: true},
		{Valid: true, Value: true, Err: &transport.Failure{Kind: transport.FailureUnavailable}},
		{Valid: true, Value: true},
	} {
		tr := &configuredCloseTransport{reply: reply}
		rt := &fakeUncertainTerminalRuntime{reply: transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnsupported}}}
		code, msg := configuredExit(func() {
			startConfiguredSpawn(transport.Services{Terminal: rt, Runtime: tr}, spawnOptions{name: "worker", pane: "owned-pane"}, "opencode", true, nil)
		})
		want := "spawn: configured launch refused; pane close unconfirmed"
		if reply.Valid && reply.Err == nil && reply.Value {
			want = "spawn: configured launch refused; owned pane closed"
		}
		if code != 4 || msg != want || len(tr.closeCalls) != 1 || tr.closeCalls[0].ID != "owned-pane" {
			t.Fatalf("exit=%d msg=%q close=%+v", code, msg, tr.closeCalls)
		}
	}
}

func TestConfiguredSpawnLegacyConsumer(t *testing.T) {
	for _, selector := range []string{"", "legacy"} {
		t.Run(selector, func(t *testing.T) {
			f := configuredFixture(t, []fakecli.Rule{{Argv: []string{"pane", "send-text"}, ArgvPrefix: true}})
			f.env["SOHO_OPENCODE_TUI_PROFILE"] = selector
			code, out, stderr, calls := runCmdSpawn(t, f, configuredArgs("p-target"))
			if code != 0 || !spawnJSONHasField(t, out, `"status": "ready"`) {
				t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
			}
			starts := 0
			for _, c := range calls {
				if reflect.DeepEqual(c.Argv, []string{"status", "server", "--json"}) || (len(c.Argv) > 1 && c.Argv[1] == "start-configured") {
					t.Fatalf("legacy used configured protocol: %q", c.Argv)
				}
				if len(c.Argv) > 1 && c.Argv[0] == "agent" && c.Argv[1] == "start" {
					starts++
					want := []string{"agent", "start", "worker", "--kind", "opencode", "--pane", "p-target", "--timeout", "60000", "--", "-m", "provider/model#variant"}
					if !reflect.DeepEqual(c.Argv, want) || c.Stdin != "" {
						t.Fatalf("legacy args/input: %+v", c)
					}
				}
			}
			if starts != 1 {
				t.Fatalf("starts=%d", starts)
			}
		})
	}
}
