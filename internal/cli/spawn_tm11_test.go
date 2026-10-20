//go:build !windows

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

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
