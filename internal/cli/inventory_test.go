package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func inventoryCLIEnv(t *testing.T, host string, rules []fakecli.Rule) (platform.Env, string, string) {
	t.Helper()
	root, bin := t.TempDir(), t.TempDir()
	home, cwd := filepath.Join(root, "home"), filepath.Join(root, "caller")
	for _, p := range []string{home, cwd, filepath.Join(cwd, ".agents", "soho.conf"), filepath.Join(root, "state", "private-session", "session.conf")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	for _, name := range []string{"herdr", "hc", "git"} {
		var selected []fakecli.Rule
		if name == host {
			selected = rules
		}
		if _, err := fakecli.InstallWithOptions(t, bin, name, selected, fakecli.InstallOptions{CaptureEnv: []string{"_SOHO_LAUNCH_CONFIG_BASE", "_SOHO_LAUNCH_HOME"}}); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"), "PATH": bin,
		"SOHO_FAKECLI_CONFIG": bin, "SOHO_TRANSPORT": host, "SOHO_DIR": filepath.Join(root, "state"), "TMPDIR": filepath.Join(root, "scratch"),
		"HERDR_ENV": "1", "HERDR_PANE_ID": "stale-private-pane", "HERDR_WORKSPACE_ID": "private-session", "HC_TERMINAL_ID": "stale-private-terminal",
		"_SOHO_LAUNCH_CONFIG_BASE": "invalid-private-locator\n", "_SOHO_LAUNCH_HOME": "invalid-private-home\n",
	}
	return env, root, bin
}

func inventoryCLIRun(t *testing.T, env platform.Env, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(args, env)
	return code, out.String(), stderr.String()
}

func inventoryCLITree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Observe current metadata rather than the directory enumeration's
		// cached record, which can retain a pre-child-creation mtime on Windows.
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" %x", sha256.Sum256(b))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
			value += " " + target
		}
		snap[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestInventoryCLITreeCanary(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	at := time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, at, at); err != nil {
		t.Fatal(err)
	}
	before := inventoryCLITree(t, root)
	if len(before) != 2 || before["file"] == "" || !reflect.DeepEqual(before, inventoryCLITree(t, root)) {
		t.Fatalf("unchanged snapshot is incomplete or unstable: %v", before)
	}
	// Equal length and restored mtime leave the bytes/hash as the discriminator.
	if err := os.WriteFile(file, []byte("after!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, at, at); err != nil {
		t.Fatal(err)
	}
	afterBytes := inventoryCLITree(t, root)
	if before["file"] == afterBytes["file"] {
		t.Fatal("same-length byte modification was not observed")
	}
	// Change only the regular-file timestamp, without touching bytes or length.
	later := at.Add(24 * time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	afterTime := inventoryCLITree(t, root)
	if afterBytes["file"] == afterTime["file"] {
		t.Fatal("regular-file mtime change was not observed")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "after!" {
		t.Fatalf("mtime control changed bytes: %q %v", data, err)
	}
	added := filepath.Join(root, "added")
	if err := os.WriteFile(added, []byte("entry"), 0o600); err != nil {
		t.Fatal(err)
	}
	afterCreate := inventoryCLITree(t, root)
	if len(afterCreate) != len(afterTime)+1 || afterCreate["added"] == "" {
		t.Fatal("entry creation was not observed")
	}
	if err := os.Remove(added); err != nil {
		t.Fatal(err)
	}
	afterDelete := inventoryCLITree(t, root)
	if _, exists := afterDelete["added"]; exists || len(afterDelete) != len(afterTime) {
		t.Fatal("entry deletion was not observed")
	}
}

func inventoryCLICalls(t *testing.T, bin, name string) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, name+".calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func assertInventoryCLICalls(t *testing.T, bin, host string, want [][]string) {
	t.Helper()
	for _, name := range []string{"herdr", "hc", "git"} {
		calls := inventoryCLICalls(t, bin, name)
		var argv [][]string
		for _, call := range calls {
			argv = append(argv, call.Argv)
			for _, value := range call.Env {
				if value != "" {
					t.Fatalf("private launch locator reached %s", name)
				}
			}
		}
		if name == host {
			if !reflect.DeepEqual(argv, want) {
				t.Fatalf("%s argv=%v want=%v", name, argv, want)
			}
		} else if len(calls) != 0 {
			t.Fatalf("unexpected %s calls=%v", name, argv)
		}
	}
}

func decodeInventoryCLI(t *testing.T, out string) map[string]json.RawMessage {
	t.Helper()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("missing final newline: %q", out)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var doc map[string]json.RawMessage
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("extra stdout after document: %v", err)
	}
	if string(doc["schema"]) != "1" {
		t.Fatalf("schema=%s", doc["schema"])
	}
	var version string
	var generated int64
	if err := json.Unmarshal(doc["soho_version"], &version); err != nil || version == "" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	if err := json.Unmarshal(doc["generated_ms"], &generated); err != nil || generated < 0 {
		t.Fatalf("generated=%d err=%v", generated, err)
	}
	var counts map[string]int
	if err := json.Unmarshal(doc["hidden"], &counts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(counts, map[string]int{"working": 0, "needs_you": 0, "done": 0, "idle": 0, "unknown": 0}) {
		t.Fatalf("hidden=%v", counts)
	}
	return doc
}

func TestInventoryCLIEmptyAndNowrite(t *testing.T) {
	for _, host := range []string{"herdr", "hc"} {
		for _, nowrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/nowrite=%v", host, nowrite), func(t *testing.T) {
				argv, body := []string{"agent", "list"}, `{"result":{"agents":[]}}`
				if host == "hc" {
					argv, body = []string{"ls", "--json"}, `{"terminals":[]}`
				}
				env, root, bin := inventoryCLIEnv(t, host, []fakecli.Rule{{Argv: argv, Stdout: body}})
				if nowrite {
					env["SOHO_NOWRITE"] = "1"
				}
				before, envBefore := inventoryCLITree(t, root), env.Clone()
				code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
				if code != 0 || stderr != "" {
					t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
				}
				doc := decodeInventoryCLI(t, out)
				if string(doc["projects"]) != "[]" {
					t.Fatalf("projects=%s", doc["projects"])
				}
				assertInventoryCLICalls(t, bin, host, [][]string{argv})
				if after := inventoryCLITree(t, root); !reflect.DeepEqual(before, after) {
					t.Fatalf("inventory changed fixture tree: before=%v after=%v", before, after)
				}
				if !reflect.DeepEqual(envBefore, env) {
					t.Fatal("inventory changed invocation environment")
				}
			})
		}
	}
}

func TestInventoryCLIUsageAndHelpBeforeIO(t *testing.T) {
	for _, host := range []string{"herdr", "hc"} {
		for _, nowrite := range []bool{false, true} {
			for _, args := range [][]string{{"inventory"}, {"inventory", "--json", "extra"}, {"inventory", "--json", "--json"}, {"inventory", "--JSON"}, {"inventory", "--help", "--json"}, {"inventory", "-h", "extra"}, {"inventory", "--help"}, {"inventory", "-h"}} {
				t.Run(fmt.Sprintf("%s/%v/%v", host, nowrite, args), func(t *testing.T) {
					env, root, bin := inventoryCLIEnv(t, host, nil)
					if nowrite {
						env["SOHO_NOWRITE"] = "1"
					}
					before := inventoryCLITree(t, root)
					code, out, stderr := inventoryCLIRun(t, env, args...)
					help := len(args) == 2 && (args[1] == "--help" || args[1] == "-h")
					if help {
						if code != 0 || out != "  soho inventory --json              # read-only local agent inventory\n" || stderr != "" {
							t.Fatalf("help exit=%d out=%q stderr=%q", code, out, stderr)
						}
					} else if code != 2 || out != "" || stderr != "soho: usage: soho inventory --json\n" {
						t.Fatalf("usage exit=%d out=%q stderr=%q", code, out, stderr)
					}
					assertInventoryCLICalls(t, bin, host, nil)
					if after := inventoryCLITree(t, root); !reflect.DeepEqual(before, after) {
						t.Fatalf("usage/help changed fixture tree: before=%v after=%v", before, after)
					}
				})
			}
		}
	}
	if !knownCommands["inventory"] || !hcCommandAllowed("inventory") || !nowriteReadInvocation([]string{"inventory", "--json"}) || nowriteReadInvocation([]string{"inventory", "--json", "extra"}) {
		t.Fatal("inventory command shape registration is inconsistent")
	}
}

func TestInventoryCLIFailureIsGenericAndAtomic(t *testing.T) {
	for _, host := range []string{"herdr", "hc"} {
		for _, invalid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/invalid=%v", host, invalid), func(t *testing.T) {
				argv := []string{"agent", "list"}
				if host == "hc" {
					argv = []string{"ls", "--json"}
				}
				rule := fakecli.Rule{Argv: argv, Code: 7, Stderr: "private cwd /private/project state report argv --token"}
				if invalid {
					rule.Code = 0
					rule.Stdout = "{private invalid listing"
				}
				env, root, bin := inventoryCLIEnv(t, host, []fakecli.Rule{rule})
				before := inventoryCLITree(t, root)
				code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
				if code != 4 || out != "" || stderr != "soho: inventory: unavailable\n" {
					t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
				}
				assertInventoryCLICalls(t, bin, host, [][]string{argv})
				if !reflect.DeepEqual(before, inventoryCLITree(t, root)) {
					t.Fatal("failure changed fixture tree")
				}
			})
		}
	}
}

func TestInventoryCLIOptedInAgentNullFields(t *testing.T) {
	env, repo := commandFixture(t)
	state := env["SOHO_DIR"]
	if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte("inventory=show\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0700); err != nil {
		t.Fatal(err)
	}
	seed, err := json.Marshal(map[string]any{"schema": 1, "project_root": repo, "session_id": "ws", "terminal": "p1", "name": "owner", "kind": "claude", "started_ms": 42, "task": ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ws", "orchestrator.json"), seed, 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	rules := []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + herdrAgentEntry("owner", "p1", "claude", "ws", repo) + `]}}`}, {Argv: []string{"agent", "get", "owner"}, Code: 7}}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withHostGit(t, withFakeCLI(env, bin), bin)
	env["SOHO_TRANSPORT"] = "herdr"
	delete(env, "HERDR_WORKSPACE_ID")
	before := inventoryCLITree(t, filepath.Dir(repo))
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	doc := decodeInventoryCLI(t, out)
	var projects []struct {
		Sessions []struct {
			Agents []map[string]json.RawMessage `json:"agents"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(doc["projects"], &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || len(projects[0].Sessions) != 1 || len(projects[0].Sessions[0].Agents) != 1 {
		t.Fatalf("expected one live registered agent: %s", doc["projects"])
	}
	a := projects[0].Sessions[0].Agents[0]
	for key, want := range map[string]string{"terminal": `"p1"`, "name": `"owner"`, "role": `"orchestrator"`, "kind": `"claude"`, "state": `"unknown"`, "model": "null", "effort": "null", "reason": "null", "task": "null", "since_ms": "null", "report_ready": "false", "started_ms": "42"} {
		if string(a[key]) != want {
			t.Fatalf("agent %s=%s want=%s", key, a[key], want)
		}
	}
	if !reflect.DeepEqual(before, inventoryCLITree(t, filepath.Dir(repo))) {
		t.Fatal("opted-in inventory changed project/home/state tree")
	}
	calls := inventoryCLICalls(t, bin, "herdr")
	var argv [][]string
	for _, c := range calls {
		argv = append(argv, c.Argv)
	}
	if !reflect.DeepEqual(argv, [][]string{{"agent", "list"}, {"agent", "get", "owner"}}) {
		t.Fatalf("unexpected caller/workspace operations: %v", argv)
	}
}

func TestInventoryCLIRegisteredTeamSample(t *testing.T) {
	env, repo := commandFixture(t)
	state := env["SOHO_DIR"]
	sess := filepath.Join(state, "ws")
	if err := os.MkdirAll(filepath.Join(repo, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte("inventory=show\ninventory_name=CLI sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sess, 0o700); err != nil {
		t.Fatal(err)
	}
	store := sessionmeta.Store{StateBase: state, SessionID: "ws"}
	if _, err := store.Register(sessionmeta.Record{Schema: 1, ProjectRoot: repo, SessionID: "ws", Terminal: "p1", Name: "orchestrator", Kind: "claude", StartedMS: 1793372400000, Task: "Coordinate sample"}); err != nil {
		t.Fatal(err)
	}
	fields := make([]string, 16)
	fields[0], fields[1], fields[2], fields[3] = "build", "p2", "claude", "implementer"
	fields[4], fields[5], fields[6] = "claude", "1", repo
	// The writer's local stamp represents this fixed, independently expected epoch.
	fields[7] = time.UnixMilli(1793448000000).In(time.Local).Format("20060102T150405")
	if err := os.WriteFile(filepath.Join(sess, "agents.tsv"), []byte(strings.Join(fields, "\t")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess, "task-build"), []byte("implementer: Build sample ✓\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[` + herdrAgentEntry("build", "p2", "claude", "ws", repo) + `,` + herdrAgentEntry("orchestrator", "p1", "claude", "ws", repo) + `]}}`},
		{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":7}}}`},
		{Argv: []string{"agent", "read", "build", "--source", "visible"}, Stdout: "typing\n"},
		{Argv: []string{"agent", "get", "orchestrator"}, Stdout: `{"result":{"agent":{"agent_status":"done","state_change_seq":7}}}`},
		{Argv: []string{"agent", "read", "orchestrator", "--source", "visible", "--lines", "20"}, Stdout: "waiting\n"},
		{Argv: []string{"agent", "read", "orchestrator", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "waiting\n"},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withHostGit(t, withFakeCLI(env, bin), bin)
	env["SOHO_TRANSPORT"] = "herdr"
	delete(env, "HERDR_WORKSPACE_ID")
	before := inventoryCLITree(t, filepath.Dir(repo))
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	doc := decodeInventoryCLI(t, out)
	var projects []struct {
		Name     string `json:"name"`
		Sessions []struct {
			ID     string                       `json:"id"`
			Agents []map[string]json.RawMessage `json:"agents"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(doc["projects"], &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "CLI sample" || len(projects[0].Sessions) != 1 || projects[0].Sessions[0].ID != "ws" || len(projects[0].Sessions[0].Agents) != 2 {
		t.Fatalf("team=%s", doc["projects"])
	}
	agents := projects[0].Sessions[0].Agents
	for i, want := range []map[string]string{
		{"terminal": `"p1"`, "name": `"orchestrator"`, "role": `"orchestrator"`, "kind": `"claude"`, "state": `"done"`, "started_ms": "1793372400000", "task": `"Coordinate sample"`},
		{"terminal": `"p2"`, "name": `"build"`, "role": `"implementer"`, "kind": `"claude"`, "state": `"working"`, "started_ms": "1793448000000", "task": `"Build sample"`},
	} {
		for key, value := range want {
			if string(agents[i][key]) != value {
				t.Fatalf("agent[%d].%s=%s want=%s", i, key, agents[i][key], value)
			}
		}
		for _, key := range []string{"model", "effort", "reason", "since_ms"} {
			if string(agents[i][key]) != "null" {
				t.Fatalf("agent[%d].%s=%s want=null", i, key, agents[i][key])
			}
		}
		if string(agents[i]["report_ready"]) != "false" {
			t.Fatalf("agent[%d].report_ready=%s", i, agents[i]["report_ready"])
		}
	}
	for _, path := range []string{repo, filepath.Dir(repo), env["HOME"], state, env["TMPDIR"]} {
		if path == "" {
			continue
		}
		encoded, _ := json.Marshal(path)
		if strings.Contains(out, path) || strings.Contains(out, string(encoded[1:len(encoded)-1])) {
			t.Fatal("CLI inventory exposed a fixture path")
		}
	}
	if !reflect.DeepEqual(before, inventoryCLITree(t, filepath.Dir(repo))) {
		t.Fatal("team inventory changed project/home/state")
	}
	calls := inventoryCLICalls(t, bin, "herdr")
	var argv [][]string
	for _, call := range calls {
		argv = append(argv, call.Argv)
	}
	if !reflect.DeepEqual(argv, [][]string{{"agent", "list"}, {"agent", "get", "build"}, {"agent", "read", "build", "--source", "visible"}, {"agent", "get", "orchestrator"}, {"agent", "read", "orchestrator", "--source", "visible", "--lines", "20"}, {"agent", "read", "orchestrator", "--source", "recent-unwrapped", "--lines", "40"}}) {
		t.Fatalf("unexpected team/caller calls: %v", argv)
	}
	// Optional evidence output is test-only: copy the captured CLI bytes verbatim.
	if path := os.Getenv("SOHO_TEST_INVENTORY_SAMPLE"); path != "" {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.WriteString(out)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("save exact CLI stdout: write=%v close=%v", writeErr, closeErr)
		}
	}
}
