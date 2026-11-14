package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const reopenPrivate = "SYNTHETIC_PRIVATE_REOPEN_R44"
const reopenCapability = `{"running":true,"capabilities":{"native_agent_launch_v1":true}}`

func configuredReopenFixture(t *testing.T, capability string) *releaseFixture {
	t.Helper()
	f := newReopenFixture(t, "idle")
	f.env["PATH"] = f.fakeDir
	f.env["SOHO_LANES"] = "off"
	f.env["SOHO_ROLE_IMPLEMENTER_APPROVALS"] = "ask"
	f.env["SOHO_LAYOUT"] = "tab"
	f.env["SOHO_OPENCODE_TUI_PROFILE"] = "v2"
	f.env["OPENCODE_CONFIG_CONTENT"] = `{"synthetic":"` + reopenPrivate + `","large":9007199254740993}`
	f.env["OPENCODE_CONFIG_DIR"] = filepath.Join(f.cwd, "space 'quoted' 雪")
	if _, err := fakecli.Install(t, f.fakeDir, "opencode", nil); err != nil {
		t.Fatal(err)
	}
	row := []string{"worker", "p-worker", "opencode", "implementer", "opencode", "1", f.cwd, "now", "provider/model#variant", "ask", "implementer", "", "", "--auto", "high", "inherit"}
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+strings.Join(row, "\t")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	addReleaseReport(t, f.state, "worker", false)
	for p, b := range map[string]string{taskreport.TaskReportPointerPath(f.state, "worker"): "original pointer\n", filepath.Join(f.state, "task-worker"): "original marker\n"} {
		if err := os.WriteFile(p, []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rules := append([]fakecli.Rule{
		{Argv: []string{"status", "server", "--json"}, Stdout: capability},
		{Argv: []string{"agent", "start-configured"}, ArgvPrefix: true, Stdout: `{"id":"configured-start","result":{"type":"agent_configured_started","agent":{"name":"worker","agent":"opencode","pane_id":"p-new","terminal_id":"actual-terminal","agent_status":"idle","interactive_ready":true,"launch_profile":"opencode-primary-v2"}}}`},
	}, reopenRules(t, f)...)
	config := struct {
		Log     string         `json:"log"`
		Capture bool           `json:"capture_stdin"`
		Rules   []fakecli.Rule `json:"rules"`
	}{filepath.Join(f.fakeDir, "herdr.calls.jsonl"), true, rules}
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.configFile, b, 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func reopenSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[p] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Observe cmdReopen's actual thrown code/message independently of output.
func configuredReopenRun(t *testing.T, f *releaseFixture, args ...string) (code int, message, out, stderr string, calls []fakecli.Call) {
	t.Helper()
	ctx := core.LoadConfig(f.env, f.cwd)
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var stdout, errout bytes.Buffer
	platform.Stdout, platform.Stderr = &stdout, &errout
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	func() {
		defer func() {
			if r := recover(); r != nil {
				if e, ok := r.(*platform.ExitError); ok {
					code, message = e.Code, e.Msg
				} else {
					panic(r)
				}
			}
		}()
		code = cmdReopen(args, &ctx, f.env, f.cwd)
	}()
	calls, err := fakecli.ReadCallsForConfig(f.configFile)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return code, message, stdout.String(), errout.String(), calls
}

func TestConfiguredReopenAdmissionBeforeRelease(t *testing.T) {
	for _, tc := range []struct {
		name, capability, key, value, message string
		code                                  int
	}{
		{"absent", `{"running":true}`, "", "", "spawn: configured launch preflight refused", 4},
		{"false", `{"running":true,"capabilities":{"native_agent_launch_v1":false}}`, "", "", "spawn: configured launch preflight refused", 4},
		{"malformed-native-zero", `null`, "", "", "spawn: configured launch preflight refused", 4},
		{"invalid-capability-type", `{"running":true,"capabilities":{"native_agent_launch_v1":"true"}}`, "", "", "spawn: configured launch preflight refused", 4},
		{"inline-json", reopenCapability, "OPENCODE_CONFIG_CONTENT", `{"private":"` + reopenPrivate, "launch profile: inline configuration is not a single non-null JSON object", 2},
		{"selector", reopenCapability, "SOHO_OPENCODE_TUI_PROFILE", reopenPrivate, "launch profile: OpenCode TUI profile is invalid", 2},
		{"configured-args", reopenCapability, "SOHO_ARGS_OPENCODE", "--model " + reopenPrivate, "native launch profile is invalid", 2},
		{"native-args", reopenCapability, "row-native", "--model " + reopenPrivate, "native launch profile is invalid", 2},
		{"model", reopenCapability, "row-model", reopenPrivate, "launch profile: model reference is malformed", 2},
	} {
		for _, force := range []bool{false, true} {
			name := tc.name
			if force {
				name += "/force"
			}
			t.Run(name, func(t *testing.T) {
				f := configuredReopenFixture(t, tc.capability)
				if strings.HasPrefix(tc.key, "row-") {
					fields := strings.Split(core.RosterLine(f.state, "worker"), "\t")
					i := 13
					if tc.key == "row-model" {
						i = 8
					}
					fields[i] = tc.value
					if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+strings.Join(fields, "\t")+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if tc.key != "" {
					f.env[tc.key] = tc.value
				}
				before := reopenSnapshot(t, f.state)
				args := []string{"worker"}
				if force {
					args = append(args, "--force")
				}
				code, msg, out, stderr, calls := configuredReopenRun(t, f, args...)
				if code != tc.code || msg != tc.message || out != "" {
					t.Errorf("exit=%d message=%q out=%q stderr=%q", code, msg, out, stderr)
				}
				if !reflect.DeepEqual(before, reopenSnapshot(t, f.state)) {
					t.Error("old worker/report/pointer/marker state changed before admission")
				}
				wantCalls := 0
				if tc.code == 4 {
					wantCalls = 1
				}
				if len(calls) != wantCalls {
					t.Errorf("capability observations=%d want=%d", len(calls), wantCalls)
				}
				for _, c := range calls {
					if !reflect.DeepEqual(c.Argv, []string{"status", "server", "--json"}) || c.Stdin != "" {
						t.Errorf("effect before admission: %+v", c)
					}
					if strings.Contains(strings.Join(c.Argv, " "), reopenPrivate) {
						t.Error("private argv")
					}
				}
				if strings.Contains(out+stderr+msg, reopenPrivate) {
					t.Error("private output")
				}
			})
		}
	}
}

func TestConfiguredReopenPublicAdmissionPreservesState(t *testing.T) {
	for _, tc := range []struct {
		name, capability, key, value, message string
		code                                  int
	}{
		{"missing-capability", `{ "running":true }`, "", "", "spawn: configured launch preflight refused", 4},
		{"inline-json", reopenCapability, "OPENCODE_CONFIG_CONTENT", `{"private":"` + reopenPrivate, "launch profile: inline configuration is not a single non-null JSON object", 2},
		{"native-args", reopenCapability, "SOHO_ARGS_OPENCODE", "--model " + reopenPrivate, "native launch profile is invalid", 2},
	} {
		for _, force := range []bool{false, true} {
			name := tc.name + "/ordinary"
			if force {
				name = tc.name + "/force"
			}
			t.Run(name, func(t *testing.T) {
				f := configuredReopenFixture(t, tc.capability)
				if tc.key != "" {
					f.env[tc.key] = tc.value
				}
				before := reopenSnapshot(t, f.state)
				args := []string{"worker"}
				if force {
					args = append(args, "--force")
				}
				r := f.reopen(t, args...)
				if r.code != tc.code || r.stdout != "" || r.stderr != "soho: "+tc.message+"\n" {
					t.Errorf("exit=%d stdout=%q stderr=%q", r.code, r.stdout, r.stderr)
				}
				if strings.Contains(r.stdout+r.stderr, reopenPrivate) {
					t.Error("private output")
				}
				if !reflect.DeepEqual(before, reopenSnapshot(t, f.state)) {
					t.Error("public Run changed old worker/report/pointers/state (including friction.log)")
				}
				if _, err := os.Stat(filepath.Join(f.state, "friction.log")); !os.IsNotExist(err) {
					t.Errorf("admission created friction.log: %v", err)
				}
				wantCalls := 0
				if tc.code == 4 {
					wantCalls = 1
				}
				if len(r.calls) != wantCalls {
					t.Errorf("calls=%+v want count=%d", r.calls, wantCalls)
				}
				for _, c := range r.calls {
					if !reflect.DeepEqual(c.Argv, []string{"status", "server", "--json"}) || c.Stdin != "" {
						t.Errorf("forbidden native effect: %+v", c)
					}
				}
			})
		}
	}
}

func TestConfiguredReopenNormalSpawnFrictionUnchanged(t *testing.T) {
	f := configuredReopenFixture(t, reopenCapability)
	f.env["OPENCODE_CONFIG_CONTENT"] = `{"private":"` + reopenPrivate
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := Run([]string{"spawn", "implementer", "--kind", "opencode", "--name", "replacement", "--model", "provider/model", "--fresh", "--force"}, f.env)
	const message = "launch profile: inline configuration is not a single non-null JSON object"
	if code != 2 || out.Len() != 0 || stderr.String() != "soho: "+message+"\n" {
		t.Fatalf("normal spawn exit=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
	log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
	if err != nil || strings.Count(string(log), "\n") != 1 || !strings.Contains(string(log), "\terror(exit 2)\tspawn\t"+message) || strings.Contains(string(log), reopenPrivate) {
		t.Fatalf("normal spawn friction=%q err=%v", log, err)
	}
}

func TestConfiguredReopenPositive(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "ordinary"
		if force {
			name = "force"
		}
		t.Run(name, func(t *testing.T) {
			f := configuredReopenFixture(t, reopenCapability)
			args := []string{"worker"}
			if force {
				args = append(args, "--force")
			}
			code, msg, out, stderr, calls := configuredReopenRun(t, f, args...)
			if code != 0 || msg != "" || !strings.Contains(out, `"status": "ready"`) || !strings.Contains(out, `"launch_profile": "opencode-primary-v2"`) {
				t.Fatalf("exit=%d message=%q out=%q stderr=%q", code, msg, out, stderr)
			}
			closed, started, probes := -1, 0, 0
			for i, c := range calls {
				a := strings.Join(c.Argv, " ")
				if a == "status server --json" {
					probes++
				}
				if a == "pane close p-worker" {
					if probes != 1 || closed >= 0 {
						t.Error("close without prior single admission")
					}
					closed = i
				}
				if strings.HasPrefix(a, "agent start-configured ") {
					started++
					if closed < 0 || i <= closed || probes != 3 {
						t.Errorf("configured start order: %+v", calls)
					}
					assertReopenConfiguredFrame(t, c.Stdin, f.env.Get("OPENCODE_CONFIG_DIR"))
				} else if c.Stdin != "" {
					t.Errorf("unexpected stdin on %q", a)
				}
				if strings.HasPrefix(a, "agent start ") || strings.HasPrefix(a, "pane send-") || strings.HasPrefix(a, "agent rename ") {
					t.Errorf("legacy start/clear/input/rename: %q", a)
				}
				if strings.Contains(a, reopenPrivate) || strings.Contains(a, "OPENCODE_CONFIG") {
					t.Error("private argv")
				}
			}
			if closed < 0 || started != 1 {
				t.Errorf("close=%d starts=%d", closed, started)
			}
			row := core.RosterLine(f.state, "worker")
			if !strings.HasPrefix(row, "worker\tp-new\topencode\timplementer\t") || strings.Contains(out+stderr+row, reopenPrivate) {
				t.Error("replacement roster/privacy")
			}
		})
	}
}

func assertReopenConfiguredFrame(t *testing.T, frame, dir string) {
	t.Helper()
	const prefix = "HERDR_CONFIGURED_START_V1\n"
	if !strings.HasPrefix(frame, prefix) {
		t.Fatal("missing private frame")
	}
	var v struct {
		Method string
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
	if v.Method != "agent.start_configured" || v.Params.Name != "worker" || v.Params.Kind != "opencode" || v.Params.Pane != "p-new" || v.Params.Timeout != 60000 || !reflect.DeepEqual(v.Params.Args, []string{"--standalone", "--auto"}) {
		t.Fatalf("wrong frame metadata/args: %+v", v.Params.Args)
	}
	overrides := map[string]string{}
	previous := ""
	for _, p := range v.Params.Env {
		if p[0] <= previous {
			t.Error("unsorted overrides")
		}
		previous = p[0]
		overrides[p[0]] = p[1]
	}
	if len(overrides) != 3 || overrides["OPENCODE_CONFIG_DIR"] != dir || overrides["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
		t.Error("wrong overrides")
	}
	decoder := json.NewDecoder(strings.NewReader(overrides["OPENCODE_CONFIG_CONTENT"]))
	decoder.UseNumber()
	var cfg map[string]any
	if err := decoder.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	agents, _ := cfg["agents"].(map[string]any)
	build, _ := agents["build"].(map[string]any)
	if cfg["model"] != "provider/model#variant" || cfg["default_agent"] != "build" || build["model"] != cfg["model"] || cfg["synthetic"] != reopenPrivate || cfg["large"] != json.Number("9007199254740993") {
		t.Error("wrong exact model/config")
	}
}

func TestConfiguredReopenPublicHCGuardUnchanged(t *testing.T) {
	f := configuredReopenFixture(t, reopenCapability)
	f.env["SOHO_TRANSPORT"] = "hc"
	before := reopenSnapshot(t, f.state)
	r := f.reopen(t, "worker", "--force")
	if r.code != 2 || r.stdout != "" || r.stderr != "soho: reopen does not support transport=hc yet; set transport=herdr or see DJA-604\n" || len(r.calls) != 0 || !reflect.DeepEqual(before, reopenSnapshot(t, f.state)) {
		t.Fatalf("hc guard: exit=%d stdout=%q stderr=%q calls=%+v", r.code, r.stdout, r.stderr, r.calls)
	}
}
