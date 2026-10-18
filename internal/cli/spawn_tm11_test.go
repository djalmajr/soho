//go:build !windows

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type tm11SpawnStep struct {
	Args []string `json:"args"`
	Err  string   `json:"err"`
	Log  *string  `json:"log"`
	Out  string   `json:"out"`
	RC   int      `json:"rc"`
}

type tm11SpawnGolden struct {
	Files map[string]*string `json:"files"`
	Steps []tm11SpawnStep    `json:"steps"`
}

func TestTM11SpawnCommandParity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "legacy", "parity-spawn.json"))
	if err != nil {
		t.Fatal(err)
	}
	var goldens map[string]tm11SpawnGolden
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatal(err)
	}
	t.Run(`JS: "parity spawn: planner is 12, a sub-orchestrator outside a lane is 3"`, func(t *testing.T) {
		golden := tm11RequireSpawnGolden(t, goldens, "errors")
		f := newTM11SpawnFixture(t, nil)
		f.runSteps(t, golden)
	})
	t.Run(`JS: "parity spawn: agent_not_ready registers, prints JSON + screen, exits 7"`, func(t *testing.T) {
		golden := tm11RequireSpawnGolden(t, goldens, "notready")
		rules := tm11SpawnRules(
			fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stderr: "agent_not_ready: login prompt\n", Code: 1},
			fakecli.Rule{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
			fakecli.Rule{Argv: []string{"pane", "list", "--workspace", "ws"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[{"pane_id":"p-new","tab_id":"t-herd"}]}}`},
			fakecli.Rule{Argv: []string{"tab", "get", "t-herd"}, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"}}}`},
			fakecli.Rule{Argv: []string{"agent", "read", "build"}, ArgvPrefix: true, Stdout: "plain screen\n"},
		)
		f := newTM11SpawnFixture(t, rules)
		f.runSteps(t, golden)
	})
	t.Run(`JS: "parity spawn: --pane places the worker in a given pane"`, func(t *testing.T) {
		golden := tm11RequireSpawnGolden(t, goldens, "given-pane")
		rules := tm11SpawnRules(
			fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
			fakecli.Rule{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			fakecli.Rule{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "plain screen\n"},
		)
		f := newTM11SpawnFixture(t, rules)
		f.runSteps(t, golden)
	})
	t.Run(`JS: "parity spawn: agent_pane_busy twice, then success (15x1s retry budget)"`, func(t *testing.T) {
		golden := tm11RequireSpawnGolden(t, goldens, "busy2")
		rules := tm11SpawnRules(
			fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Call: 1, Stderr: `{"error":{"code":"agent_pane_busy","message":"shell not ready"}}`, Code: 1},
			fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Call: 2, Stderr: `{"error":{"code":"agent_pane_busy","message":"shell not ready"}}`, Code: 1},
			fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Call: 3, Stdout: `{"result":{"started":true}}`},
			fakecli.Rule{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
			fakecli.Rule{Argv: []string{"pane", "list", "--workspace", "ws"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[{"pane_id":"p-new","tab_id":"t-herd"}]}}`},
			fakecli.Rule{Argv: []string{"tab", "get", "t-herd"}, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"}}}`},
			fakecli.Rule{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			fakecli.Rule{Argv: []string{"agent", "read", "build"}, ArgvPrefix: true, Stdout: "plain screen\n"},
		)
		f := newTM11SpawnFixture(t, rules)
		f.runSteps(t, golden)
	})
	t.Run(`JS: "parity spawn: a full caller tab overflows the worker into a herd tab"`, func(t *testing.T) {
		golden := tm11RequireSpawnGolden(t, goldens, "overflow")
		layout := `{"result":{"layout":{"area":{"x":0,"y":0,"width":107,"height":57},"panes":[{"pane_id":"c","tab_id":"t-main","focused":false,"rect":{"x":0,"y":0,"width":53,"height":57}},{"pane_id":"p-w1","tab_id":"t-main","focused":false,"rect":{"x":53,"y":0,"width":54,"height":57}},{"pane_id":"p-w2","tab_id":"t-main","focused":false,"rect":{"x":0,"y":28,"width":53,"height":29}},{"pane_id":"p-w3","tab_id":"t-main","focused":false,"rect":{"x":53,"y":28,"width":54,"height":29}}]}}}`
		panes := `{"result":{"panes":[{"pane_id":"c","tab_id":"t-main"},{"pane_id":"p-w1","tab_id":"t-main"},{"pane_id":"p-w2","tab_id":"t-main"},{"pane_id":"p-w3","tab_id":"t-main"}]}}`
		live := `{"result":{"agents":[{"name":"w1","pane_id":"w1","agent_status":"idle"},{"name":"w2","pane_id":"w2","agent_status":"idle"},{"name":"w3","pane_id":"w3","agent_status":"idle"}]}}`
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "list"}, Stdout: live},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: panes},
			{Argv: []string{"pane", "list"}, Stdout: panes},
			{Argv: []string{"pane", "layout", "--current"}, Stdout: layout},
			{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
			{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
			{Argv: []string{"tab", "get", "t-herd"}, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
			{Argv: []string{"pane", "move"}, ArgvPrefix: true, Stdout: `{"result":{}}`},
			{Argv: []string{"agent", "rename"}, ArgvPrefix: true, Stdout: `{"result":{}}`},
			{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "plain screen\n"},
		}
		f := newTM11SpawnFixture(t, rules)
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" +
			"w1\tp-w1\tgrok\timplementer\txai\t1\t" + f.cwd + "\tnow\tgrok-4.7\tfull\timplementer\tbuild\n" +
			"w2\tp-w2\tgrok\tscouter\txai\t1\t" + f.cwd + "\tnow\tgrok-4.7\tfull\tscouter\texplore\n" +
			"w3\tp-w3\tgrok\treviewer\txai\t1\t" + f.cwd + "\tnow\tgrok-4.7\tfull\treviewer\treview\n"
		if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.bin, "panes.json"), []byte(panes), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.bin, "layout.json"), []byte(layout), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["HERDR_PANE_ID"] = "c"
		f.env["HERDR_TAB_ID"] = "t-main"
		f.env["SOHO_LANES"] = "off"
		f.env["SOHO_LAYOUT"] = "split"
		f.env["SOHO_SPLIT_MAX_PANES"] = "3"
		f.env["SOHO_MAX_WORKERS"] = "4"
		f.runSteps(t, golden)
	})
	t.Run(`JS: "parity spawn: fresh worker, lane reuse, kind mismatch, lane-kind reuse"`, func(t *testing.T) {
		golden := tm11RequireSpawnGolden(t, goldens, "kind")
		f := newTM11SpawnFixture(t, nil)
		f.runStepsPrepared(t, golden, func(i int) {
			switch i {
			case 0, 2:
				tm11SpawnReset(t, f)
			case 1:
				tm11SpawnSeed(t, f, [][4]string{{"explore", "scouter", "explore", "grok"}}, `{"result":{"agents":[{"name":"explore","pane_id":"p-explore","agent_status":"idle"}]}}`)
				_ = os.Remove(filepath.Join(f.state, "herd-tab"))
			case 3:
				if err := os.WriteFile(filepath.Join(f.bin, "live.json"), []byte(`{"result":{"agents":[{"name":"build","pane_id":"p-build","agent_status":"idle"}]}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case 4, 5:
				kind := "agy"
				if i == 5 {
					kind = "grok"
				}
				tm11SpawnSeed(t, f, [][4]string{{"build", "designer", "build", kind}}, `{"result":{"agents":[{"name":"build","pane_id":"p-build","agent_status":"idle"}]}}`)
				_ = tm11SpawnWriteConfig(t, f, "", "lane.build.kind = \"grok\"\n")
			}
			tm11SpawnInstallStateRules(t, f, "idle")
		})
	})
	t.Run(`JS: "parity spawn: busy 10, gone recreated, worker cap 8, locked 5, lane kind, lanes=off reuse"`, func(t *testing.T) {
		golden := tm11RequireSpawnGolden(t, goldens, "lane-states")
		f := newTM11SpawnFixture(t, nil)
		f.runStepsPreparedChecked(t, golden, func(i int) {
			status := "idle"
			live := `{"result":{"agents":[]}}`
			switch i {
			case 0:
				tm11SpawnReset(t, f)
				tm11SpawnSeed(t, f, [][4]string{{"build", "implementer", "build", "grok"}, {"build-2", "designer", "build", "grok"}}, `{"result":{"agents":[{"name":"build","pane_id":"p-build","agent_status":"working"},{"name":"build-2","pane_id":"p-build2","agent_status":"working"}]}}`)
				status = "working"
			case 1:
				tm11SpawnSeed(t, f, [][4]string{{"build", "implementer", "build", "grok"}}, live)
				status = "gone-until-start"
			case 2:
				rows := [][4]string{{"explore", "scouter", "explore", "grok"}, {"review", "reviewer", "review", "codex"}, {"extra", "researcher", "extra", "grok"}}
				tm11SpawnSeed(t, f, rows, `{"result":{"agents":[{"name":"explore","pane_id":"p-explore"},{"name":"review","pane_id":"p-review"},{"name":"extra","pane_id":"p-extra"}]}}`)
			case 3:
				tm11SpawnSeed(t, f, [][4]string{{"mix", "implementer", "mix", "grok"}}, `{"result":{"agents":[{"name":"mix","pane_id":"p-mix","agent_status":"idle"}]}}`)
				_ = tm11SpawnWriteConfig(t, f, "", "lane.mix.roles = \"implementer,reviewer\"\n")
			case 4:
				tm11SpawnReset(t, f)
				_ = tm11SpawnWriteConfig(t, f, "", "lane.build.kind = \"codex\"\n")
			case 5:
				_ = os.Remove(filepath.Join(f.cwd, ".agents", "soho.conf"))
				roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + "implementer\tp-impl\tgrok\timplementer\txai\t1\t" + f.cwd + "\tnow\n"
				if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte(roster), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.bin, "live.json"), []byte(`{"result":{"agents":[{"name":"implementer","pane_id":"p-impl","agent_status":"idle"}]}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if i == 5 {
				f.env["SOHO_LANES"] = "off"
			}
			tm11SpawnInstallStateRules(t, f, status)
		})
	})
	t.Run(`JS: "parity spawn: config layers (user vs project vs env, kind/model/effort precedence)"`, func(t *testing.T) {
		golden := tm11RequireSpawnGolden(t, goldens, "layers")
		f := newTM11SpawnFixture(t, nil)
		f.runStepsPreparedChecked(t, golden, func(i int) {
			delete(f.env, "SOHO_LANE_BUILD_KIND")
			user, project := "", ""
			switch i {
			case 0:
				tm11SpawnReset(t, f)
				user = "lane.explore.kind = \"grok\"\nlane.explore.model = \"grok-4.7\"\nlane.explore.model.codex.worker = \"gpt-6-luna\"\n"
				project = "lane.explore.kind = \"codex\"\n"
			case 1:
				tm11SpawnReset(t, f)
				project = "lane.explore.kind = \"codex\"\nlane.explore.model = \"grok-4.7\"\nlane.explore.model.codex.worker = \"gpt-6-luna\"\n"
			case 2:
				tm11SpawnReset(t, f)
				user = "lane.build.model = \"gpt-6-luna\"\nlane.build.model.pi.worker = \"my-provider/my-model\"\n"
				project = "lane.build.kind = \"pi\"\n"
				f.env["SOHO_LANE_BUILD_KIND"] = "pi"
			case 3:
				tm11SpawnReset(t, f)
				user = "lane.build.kind = \"codex\"\nlane.build.effort = \"high\"\n"
				project = "lane.build.kind = \"pi\"\nlane.build.effort.pi = \"max\"\n"
			case 4:
				tm11SpawnReset(t, f)
				user = "lane.build.effort = \"high\"\n"
				project = "lane.build.effort.pi = \"max\"\nlane.build.model.pi.worker = \"my-provider/my-model\"\n"
			case 5:
				if err := os.WriteFile(filepath.Join(f.bin, "live.json"), []byte(`{"result":{"agents":[{"name":"build","pane_id":"p-build","agent_status":"idle"}]}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				user = "lane.build.effort = \"high\"\n"
				project = "lane.build.effort.pi = \"max\"\nlane.build.model.pi.worker = \"my-provider/my-model\"\n"
			case 6:
				tm11SpawnReset(t, f)
				user = "lane.build.model = \"grok-4.7\"\n"
				project = "role.implementer.kind = \"codex\"\nrole.implementer.model.codex.worker = \"gpt-6-luna\"\n"
			case 7:
				tm11SpawnReset(t, f)
				user = "lane.build.model = \"grok-4.7\"\n"
			}
			if i != 5 {
				tm11SpawnWriteConfig(t, f, user, project)
			}
			live := `{"result":{"agents":[]}}`
			status := "idle"
			if i == 5 {
				live = `{"result":{"agents":[{"name":"build","pane_id":"p-build","agent_status":"idle"}]}}`
			}
			if i == 6 {
				_ = os.MkdirAll(filepath.Join(f.cwd, ".agents"), 0o700)
			}
			tm11SpawnInstallStateRules(t, f, status, live)
		})
	})
}

func tm11RequireSpawnGolden(t *testing.T, goldens map[string]tm11SpawnGolden, name string) tm11SpawnGolden {
	t.Helper()
	golden, ok := goldens[name]
	if !ok {
		t.Fatalf("parity-spawn golden %q missing", name)
	}
	return golden
}

type tm11SpawnFixture struct {
	env      platform.Env
	cwd      string
	state    string
	bin      string
	herdrLog string
}

func newTM11SpawnFixture(t *testing.T, herdrRules []fakecli.Rule) *tm11SpawnFixture {
	t.Helper()
	env, cwd := commandFixture(t)
	root := filepath.Dir(cwd)
	state := filepath.Join(root, "state")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(filepath.Join(state, "ws", "briefs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "ws", "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "ws", "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	herdrRules = append(herdrRules, fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}, fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`}, fakecli.Rule{Argv: []string{"pane", "list", "--workspace", "ws"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`})
	if _, err := fakecli.Install(t, bin, "herdr", herdrRules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "grok", []fakecli.Rule{{Argv: []string{"models"}, Stdout: "grok-4.7\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "agy", []fakecli.Rule{{Argv: []string{"models"}, Stdout: "gemini-2.5 (latest)\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "codex", []fakecli.Rule{{Argv: []string{"models"}, Stdout: "gpt-6-luna (latest)\n"}, {AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "pi", []fakecli.Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	clean := envFrom(testutil.CleanEnv(t))
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "SOHO_SKILL_DIR", "HERDR_WORKSPACE_ID"} {
		clean[key] = env.Get(key)
	}
	clean["PATH"] = bin
	clean["TMPDIR"] = t.TempDir()
	clean["USERPROFILE"] = env.Get("HOME")
	clean["HERDR_ENV"] = "1"
	clean["SOHO_DIR"] = state
	clean["HERDR_WORKSPACE_ID"] = "ws"
	clean["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "0"
	clean["SOHO_PRESSURE_SWAP_PERCENT"] = "0"
	clean["SOHO_LAYOUT"] = "tab"
	clean["SOHO_REGRID"] = "off"
	clean["SOHO_WAIT_POLL_MS"] = "1"
	clean = withFakeCLI(clean, bin)
	return &tm11SpawnFixture{env: clean, cwd: cwd, state: filepath.Join(state, "ws"), bin: bin, herdrLog: filepath.Join(bin, "herdr.log")}
}

func tm11SpawnRules(extra ...fakecli.Rule) []fakecli.Rule {
	return extra
}

func (f *tm11SpawnFixture) runSteps(t *testing.T, golden tm11SpawnGolden) {
	t.Helper()
	f.runStepsPrepared(t, golden, nil)
}

func (f *tm11SpawnFixture) runStepsPreparedChecked(t *testing.T, golden tm11SpawnGolden, prepare func(int)) {
	t.Helper()
	f.runStepsPrepared(t, golden, prepare)
}

func (f *tm11SpawnFixture) runStepsPrepared(t *testing.T, golden tm11SpawnGolden, prepare func(int)) {
	t.Helper()
	for i, want := range golden.Steps {
		if prepare != nil {
			prepare(i)
		}
		if err := f.clearHerdrCalls(); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.runStep(t, want.Args)
		actualOut := strings.ReplaceAll(tm11NormalizeReuseSpawn(out, want.Out), filepath.Dir(f.cwd), "<ROOT>")
		actualErr := strings.ReplaceAll(normalizeParityError(stderr), filepath.Dir(f.cwd), "<ROOT>")
		callLog, err := f.herdrCallLog()
		if err != nil {
			t.Fatal(err)
		}
		actualLog := strings.ReplaceAll(callLog, filepath.Dir(f.cwd), "<ROOT>")
		// DJA-392 intentional contract adaptation: roles with mcp=none add
		// the kind's MCP-off flag after the effort args; the frozen bytes
		// are unchanged.
		wantOut, adErr := testutil.RewriteMcpSpawnJSON(want.Out)
		if adErr != nil {
			t.Fatalf("DJA-392 spawn JSON adaptation: %s", adErr)
		}
		wantErr, adErr := testutil.RewriteMcpErr(want.Err, wantOut)
		if adErr != nil {
			t.Fatalf("DJA-392 spawn stderr adaptation: %s", adErr)
		}
		wantLog := ""
		if want.Log != nil {
			if wantLog, adErr = testutil.RewriteMcpAgentStartLog(*want.Log); adErr != nil {
				t.Fatalf("DJA-392 spawn log adaptation: %s", adErr)
			}
		}
		if code != want.RC || actualOut != wantOut || actualErr != wantErr || (want.Log != nil && actualLog != wantLog) {
			t.Fatalf("step %d args=%v\ncode=%d want=%d\nstdout=%q want=%q\nstderr=%q want=%q\nlog=%q want=%q", i, want.Args, code, want.RC, actualOut, wantOut, actualErr, wantErr, actualLog, wantLog)
		}
	}
	for name, rel := range map[string]string{"herdTab": "herd-tab", "roster": "agents.tsv"} {
		want := golden.Files[name]
		path := filepath.Join(f.state, rel)
		actual, err := os.ReadFile(path)
		if want == nil {
			if !os.IsNotExist(err) {
				t.Fatalf("%s should be absent, err=%v", name, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		value := strings.ReplaceAll(string(actual), filepath.Dir(f.cwd), "<ROOT>")
		value = normTM11SpawnRoster(value)
		// DJA-392 fatia 2 R2: the spawn records the effective MCP choice as
		// the roster's last field; adapt the frozen rows the scenario's
		// spawns wrote (the frozen bytes stay unchanged).
		if name == "roster" {
			adapted, adErr := testutil.RewriteMcpRoster(*want, testutil.McpSpawnedAgents(stepOutsOf(golden)...))
			if adErr != nil {
				t.Fatalf("DJA-392 R2 roster adaptation: %s", adErr)
			}
			*want = adapted
		}
		if value != *want {
			t.Fatalf("%s=%q want=%q", name, value, *want)
		}
	}
}

// stepOutsOf returns each step's stdout, in order, for the oracle's
// spawn-agent extraction (DJA-392 fatia 2 R2 roster adaptation).
func stepOutsOf(golden tm11SpawnGolden) []string {
	outs := make([]string, 0, len(golden.Steps))
	for _, step := range golden.Steps {
		outs = append(outs, step.Out)
	}
	return outs
}

// tm11SpawnReuseGoKeys: keys the Go spawn-reuse JSON gained after the JS
// parity golden was frozen — the Go side carries more information, the
// frozen JS reference does not.
var tm11SpawnReuseGoKeys = []string{"model", "effort", "agent_args"}

// tm11NormalizeReuseSpawn drops those keys from the Go stdout before the
// byte comparison with the frozen golden: only for a spawn-reuse JSON (the
// only spawn JSON that carries "reused": true) and only the keys the
// golden does not carry. Every other step, and every other key, compares
// byte for byte.
func tm11NormalizeReuseSpawn(out, want string) string {
	v, err := jsonjs.Parse([]byte(out))
	if err != nil {
		return out
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return out
	}
	if reused, _ := obj.Get("reused"); reused != true {
		return out
	}
	gv, err := jsonjs.Parse([]byte(want))
	if err != nil {
		return out
	}
	gobj, ok := gv.(*jsonjs.Object)
	if !ok {
		return out
	}
	for _, key := range tm11SpawnReuseGoKeys {
		if _, present := gobj.Get(key); present {
			continue
		}
		obj.Delete(key)
	}
	return jsonjs.StringifyIndent(obj, 2) + "\n"
}

func (f *tm11SpawnFixture) runStep(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	return runIn(t, args, f.env, f.cwd)
}

func (f *tm11SpawnFixture) clearHerdrCalls() error {
	path := filepath.Join(f.bin, "herdr.calls.jsonl")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (f *tm11SpawnFixture) herdrCallLog() (string, error) {
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var log strings.Builder
	for _, call := range calls {
		log.WriteString(strings.Join(call.Argv, " "))
		log.WriteByte('\n')
	}
	return log.String(), nil
}

func tm11SpawnReset(t *testing.T, f *tm11SpawnFixture) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(f.state, "herd-tab"))
}

func tm11SpawnSeed(t *testing.T, f *tm11SpawnFixture, rows [][4]string, live string) {
	t.Helper()
	var roster strings.Builder
	roster.WriteString("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n")
	for _, row := range rows {
		fmt.Fprintf(&roster, "%s\tp-%s\t%s\t%s\txai\t1\t%s\tnow\tgrok-4.7\tfull\t%s\t%s\n", row[0], row[0], row[3], row[1], f.cwd, row[1], row[2])
	}
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte(roster.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "live.json"), []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tm11SpawnWriteConfig(t *testing.T, f *tm11SpawnFixture, user, project string) error {
	t.Helper()
	// user layer: the file the product reads ($XDG_CONFIG_HOME/soho/config)
	userPath := filepath.Join(f.env["XDG_CONFIG_HOME"], "soho", "config")
	projectPath := filepath.Join(f.cwd, ".agents", "soho.conf")
	if user == "" {
		_ = os.Remove(userPath)
	} else {
		if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(userPath, []byte(user), 0o600); err != nil {
			return err
		}
	}
	if project == "" {
		_ = os.Remove(projectPath)
	} else {
		if err := os.MkdirAll(filepath.Dir(projectPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(projectPath, []byte(project), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func tm11SpawnInstallStateRules(t *testing.T, f *tm11SpawnFixture, status string, liveOverride ...string) {
	t.Helper()
	live := `{"result":{"agents":[]}}`
	if len(liveOverride) > 0 {
		live = liveOverride[0]
	} else if b, err := os.ReadFile(filepath.Join(f.bin, "live.json")); err == nil {
		live = string(b)
	}
	get := fakecli.Rule{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}
	switch status {
	case "working", "blocked":
		get.Stdout = `{"result":{"agent":{"agent_status":"` + status + `"}}}`
	case "gone":
		get.Stdout, get.Stderr, get.Code = "", `{"error":{"code":"agent_not_found","message":"gone"}}`, 1
	case "gone-until-start":
		get = fakecli.Rule{Argv: []string{"agent", "get"}, ArgvPrefix: true, Call: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`, Code: 1}
	case "missing-status":
		get.Stdout = `{"result":{"agent":{"name":"build"}}}`
	}
	rules := []fakecli.Rule{
		get,
		{Argv: []string{"agent", "list"}, Stdout: live},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
		{Argv: []string{"tab", "get", "t-herd"}, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "plain screen\n"},
		{Argv: []string{"agent", "rename"}, ArgvPrefix: true, Stdout: `{"result":{}}`},
	}
	if status == "gone-until-start" {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get"}, ArgvPrefix: true, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`})
	}
	config := struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{Log: filepath.Join(f.bin, "herdr.calls.jsonl"), Rules: append(rules, fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}, fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`})}
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.bin, "herdr.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func normTM11SpawnRoster(value string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) > 7 {
			fields[7] = "T"
		}
		lines[i] = strings.Join(fields, "\t")
	}
	return strings.Join(lines, "\n")
}
