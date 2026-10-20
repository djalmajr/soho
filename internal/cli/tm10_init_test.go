package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestTM10InitContracts(t *testing.T) {
	t.Run(`JS: "init: first_run true — doctor on stderr only, the JSON context on stdout, no rename"`, func(t *testing.T) {
		runSetupTM5InitContract(t, false)
	})
	t.Run(`JS: "init: first_run false with a project config that makes the team choice"`, func(t *testing.T) {
		runSetupTM5InitContract(t, true)
	})
	t.Run(`JS: "init: first_run false with a roster row and no config (comments and max_workers do not count)"`, func(t *testing.T) {
		seed := func(repo, state string, roster string) {
			conf := filepath.Join(repo, ".agents", "soho.conf")
			if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(conf, []byte("# comment\nmax_workers=3\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if roster != "" {
				dir := filepath.Join(state, "ws")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(roster), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		row := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nbuild\tp1\tgrok\timplementer\txai\t1\t/tmp/work\tnow\tgrok-4.7\ttask\timplementer\tbuild\n"
		out, _, _, _, code := runTM10Init(t, `{"result":{"agent":{"name":"orchestrator"}}}`, "", 0, 0, func(repo, state string) { seed(repo, state, row) })
		if code != 0 || !strings.Contains(out, `"first_run": false`) {
			t.Fatalf("roster first-run code=%d out=%q", code, out)
		}
		out, _, _, _, code = runTM10Init(t, `{"result":{"agent":{"name":"orchestrator"}}}`, "", 0, 0, func(repo, state string) {
			seed(repo, state, "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n")
		})
		if code != 0 || !strings.Contains(out, `"first_run": true`) {
			t.Fatalf("header-only first-run code=%d out=%q", code, out)
		}
	})
	t.Run(`JS: "init: when orchestrator is live the rename takes the unique suffix"`, TestInitRenamesTakenOrchestratorNameThroughFakeHerdr)
	t.Run(`JS: "init: an orchestrator-N prefix is left alone (no rename)"`, func(t *testing.T) {
		out, stderr, calls, _, _ := runTM10Init(t, `{"result":{"agent":{"name":"orchestrator-2"}}}`, `{"result":{"pane":{"title":"existing","workspace_id":"ws"}}}`, 0, 0)
		if !strings.Contains(out, `"orchestrator": "orchestrator-2"`) || stderr == "" {
			t.Fatalf("stdout=%q stderr=%q", out, stderr)
		}
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "rename" {
				t.Fatalf("unexpected rename: %#v", calls)
			}
		}
	})
	t.Run(`JS: "init: renames the caller to orchestrator, and the second run is idempotent"`, func(t *testing.T) {
		env, repo := commandFixture(t)
		env["HERDR_ENV"], env["HERDR_PANE_ID"], env["HERDR_TAB_ID"] = "1", "p1", "t1"
		fakeDir := t.TempDir()
		rules := []fakecli.Rule{
			{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"}, {Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
			{Argv: []string{"agent", "get", "p1"}, Call: 1, Stdout: `{"result":{"agent":{"name":"worker-old"}}}`},
			{Argv: []string{"agent", "get", "p1"}, Call: 2, Stdout: `{"result":{"agent":{"name":"orchestrator"}}}`},
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"agent", "rename", "p1", "orchestrator"}}, {Argv: []string{"pane", "get", "p1"}, Stdout: `{"result":{"pane":{"title":"current","workspace_id":"ws"}}}`}, {AnyArgs: true},
		}
		if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, fakeDir)
		old, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(repo); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })
		for i := 0; i < 2; i++ {
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			code := Run([]string{"init"}, env)
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if code != 0 || !strings.Contains(out.String(), `"orchestrator": "orchestrator"`) {
				t.Fatalf("run=%d code=%d out=%q err=%q", i, code, out.String(), stderr.String())
			}
		}
		calls, err := fakecli.ReadCalls(filepath.Join(fakeDir, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		renames := 0
		for _, call := range calls {
			if strings.Join(call.Argv, " ") == "agent rename p1 orchestrator" {
				renames++
			}
		}
		if renames != 1 {
			t.Fatalf("rename calls=%d %#v", renames, calls)
		}
	})
	t.Run(`JS: "init: a living command — without HERDR_ENV it refuses with the bash message (rc 2)"`, func(t *testing.T) {
		env, _ := commandFixture(t)
		delete(env, "HERDR_ENV")
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"init"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "not running inside Herdr") {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
	})
	t.Run("JS: \"init: a title-less pane gets `orchestrator: <basename>` exactly once\"", func(t *testing.T) {
		out, _, calls, _, code := runTM10Init(t, `{"result":{"agent":{"name":"orchestrator"}}}`, `{"result":{"pane":{"title":"","workspace_id":"ws"}}}`, 0, 0)
		if code != 0 {
			t.Fatalf("init exit=%d", code)
		}
		if !strings.Contains(out, `"title": "orchestrator: repo"`) {
			t.Fatalf("stdout=%q", out)
		}
		count := 0
		for _, call := range calls {
			if strings.Join(call.Argv, " ") == "pane report-metadata p1 --source soho --title orchestrator: repo" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("metadata call count=%d calls=%#v", count, calls)
		}
	})
	t.Run(`JS: "init: an existing title is left alone (no report-metadata)"`, func(t *testing.T) {
		out, _, calls, _, code := runTM10Init(t, `{"result":{"agent":{"name":"orchestrator"}}}`, `{"result":{"pane":{"title":"orchestrator: existing","workspace_id":"ws"}}}`, 0, 0)
		if code != 0 {
			t.Fatalf("init exit=%d", code)
		}
		if !strings.Contains(out, `"title": "orchestrator: existing"`) {
			t.Fatalf("stdout=%q", out)
		}
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[0] == "pane" && call.Argv[1] == "report-metadata" {
				t.Fatalf("metadata unexpectedly called: %#v", calls)
			}
		}
	})
	t.Run(`JS: "init: a pane get failure warns, reports title "" and still exits 0"`, func(t *testing.T) {
		out, stderr, _, state, code := runTM10Init(t, `{"result":{"agent":{"name":"orchestrator"}}}`, "", 1, 0)
		if code != 0 {
			t.Fatalf("init exit=%d", code)
		}
		assertTM10InitWarning(t, out, stderr, state, `"title": ""`, "init: HERDR_PANE_ID 'p1' is not a live pane; pane title left as is and pane_id left empty")
	})
	t.Run(`JS: "init: a report-metadata failure warns and reports title """`, func(t *testing.T) {
		out, stderr, _, state, code := runTM10Init(t, `{"result":{"agent":{"name":"orchestrator"}}}`, `{"result":{"pane":{"title":"","workspace_id":"ws"}}}`, 0, 1)
		if code != 0 {
			t.Fatalf("init exit=%d", code)
		}
		assertTM10InitWarning(t, out, stderr, state, `"title": ""`, "init: herdr pane report-metadata failed; pane title left as is")
	})
}

func runTM10Init(t *testing.T, agent, pane string, paneGetCode, metadataCode int, seeds ...func(repo, state string)) (string, string, []fakecli.Call, string, int) {
	t.Helper()
	env, repo := commandFixture(t)
	env["HERDR_ENV"], env["HERDR_PANE_ID"], env["HERDR_TAB_ID"] = "1", "p1", "t1"
	fakeDir := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"agent", "get", "p1"}, Stdout: agent},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "rename", "p1", "orchestrator"}},
		{Argv: []string{"pane", "get", "p1"}, Stdout: pane, Code: paneGetCode},
		{Argv: []string{"pane", "report-metadata", "p1", "--source", "soho", "--title", "orchestrator: repo"}, Code: metadataCode},
		{AnyArgs: true},
	}
	_, err := fakecli.Install(t, fakeDir, "herdr", rules)
	if err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	state := filepath.Join(env.Get("SOHO_DIR"), "ws")
	for _, seed := range seeds {
		seed(repo, state)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run([]string{"init"}, env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(fakeDir, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), stderr.String(), calls, state, code
}

func assertTM10InitWarning(t *testing.T, out, stderr, state, title, warning string) {
	t.Helper()
	if !strings.Contains(out, title) || !strings.Contains(stderr, warning) {
		t.Fatalf("stdout=%q stderr=%q", out, stderr)
	}
	data, err := os.ReadFile(filepath.Join(state, "friction.log"))
	if err != nil || !strings.Contains(string(data), warning) {
		t.Fatalf("friction=%q err=%v", data, err)
	}
}
