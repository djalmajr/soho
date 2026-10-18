package spawn

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }

func TestSpawnMapsConfiguredArgumentsAndWritesRoster(t *testing.T) {
	// Mutation captured: changing approval, model, effort, configured args order, or trailing argv changes the observable Herdr start argv.
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	state := filepath.Join(root, "state")
	roles := filepath.Join(root, "roles")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{cwd, state, roles, bin, filepath.Join(root, "skill", "roles")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: grok\neffort: high\napprovals: full\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "ready\n"},
		{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}`},
		{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH": bin, "HOME": root, "TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
		"SOHO_SKILL_DIR": filepath.Join(root, "skill"), "SOHO_ROLES": roles,
		"SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws", "HERDR_ENV": "1",
		"HERDR_PANE_ID": "p1", "SOHO_LANES": "off", "SOHO_LAYOUT": "split",
		"SOHO_REGRID": "off", "SOHO_ARGS_GROK": "--config-arg value",
		"SOHO_ROLE_WORKER_ARGS": "--role-arg", "SOHO_WAIT_POLL_MS": "1",
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	CmdSpawn([]string{"worker", "--approvals", "full", "--model", "grok-4.7", "--effort", "high", "--", "--tail", "two words"}, ctx, env, cwd)
	if strings.Contains(stderr.String(), "fakecli: no rule") {
		t.Fatalf("fake Herdr call failed: %s", stderr.String())
	}
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var start []string
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			start = call.Argv
			break
		}
	}
	want := []string{"agent", "start", "worker", "--kind", "grok", "--pane", "p2", "--timeout", "60000", "--", "--permission-mode", "bypassPermissions", "--always-approve", "--model", "grok-4.7", "--reasoning-effort", "high", "--config-arg", "value", "--role-arg", "--tail", "two words"}
	if strings.Join(start, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("agent start argv=%q, want %q", start, want)
	}
	if _, err := jsonjs.Parse([]byte(out.String())); err != nil {
		t.Fatalf("spawn JSON: %v; %q", err, out.String())
	}
	if !strings.Contains(out.String(), `"status": "ready"`) || !strings.Contains(out.String(), `"placement": "split"`) {
		t.Fatalf("unexpected spawn result: %s", out.String())
	}
	roster, err := os.ReadFile(filepath.Join(state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	row := strings.Split(strings.TrimSpace(string(roster)), "\n")
	// DJA-392 fatia 2 R2: the row's last column is the effective MCP
	// choice; the worker role has no mcp key, so it records inherit.
	if len(row) != 2 || !strings.HasSuffix(row[1], "\t--config-arg value --role-arg\thigh\tinherit") {
		t.Fatalf("roster=%q", roster)
	}
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "move" {
			t.Fatalf("regrid=off moved panes: %q", call.Argv)
		}
		if strings.Join(call.Argv, " ") == "pane list --workspace ws" {
			t.Fatalf("regrid=off ran the post-spawn grid: %q", call.Argv)
		}
	}
}

func TestSpawnTimeoutUsesJavaScriptNumberSemantics(t *testing.T) {
	// JS: "spawn: start and update wait deadlines coerce --timeout with Number()"
	// Mutation captured: digit-only parsing changes the deadline for invalid, spaced, exponent, and decimal inputs.
	cases := []struct {
		raw         string
		start, gone float64
	}{
		{raw: "xyz", start: 30_000, gone: 60_000},
		{raw: "0", start: 30_000, gone: 60_000},
		{raw: "-5", start: 29_995, gone: 60_000},
		{raw: "60000 ", start: 90_000, gone: 60_000},
		{raw: "1e3", start: 31_000, gone: 1_000},
		{raw: "12.5", start: 30_012.5, gone: 12.5},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			start, gone := startWrapperTimeoutMs(tc.raw), updateGoneWaitMs(tc.raw)
			t.Logf("%q start=%g gone=%g", tc.raw, start, gone)
			if start != tc.start || gone != tc.gone {
				t.Fatalf("start=%g gone=%g, want start=%g gone=%g", start, gone, tc.start, tc.gone)
			}
		})
	}
}

func TestFullApprovalsMapForEveryKind(t *testing.T) {
	// JS: "kindApprovalArgs: each kind maps ask, edits, and full to its own argv"
	// Mutation captured: dropping full approval args silently removes or changes configured permissions for a CLI.
	want := map[string][]string{
		"claude":   {"--permission-mode", "bypassPermissions", "--settings", `{"enableAllProjectMcpServers":true}`},
		"codex":    {"-s", "workspace-write", "-a", "never"},
		"grok":     {"--permission-mode", "bypassPermissions", "--always-approve"},
		"agy":      {"--dangerously-skip-permissions"},
		"gemini":   {"--dangerously-skip-permissions"},
		"cursor":   {"--trust", "--force", "--approve-mcps"},
		"pi":       nil,
		"opencode": {"--auto"},
	}
	for kind, expected := range want {
		if got := buildArgs(kind, "full", "", "", &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{}); strings.Join(got, "\x00") != strings.Join(expected, "\x00") {
			t.Errorf("%s full approvals argv=%q, want %q", kind, got, expected)
		}
	}
}

func TestExplicitKindDropsConfiguredModelAndEffortClampsToKindCeiling(t *testing.T) {
	// Mutation captured: --kind without --model must not keep a model spec from a lower kind layer.
	root := t.TempDir()
	roles := filepath.Join(root, "roles")
	if err := os.MkdirAll(roles, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: grok\nmodel: grok-4.7\neffort: max\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"SOHO_ROLES": roles, "SOHO_SKILL_DIR": root}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"role_worker_model": {Value: "configured-model", Source: "user"}}}
	resolved := core.ResolveRoleSettings("worker", ctx, env, root, core.RoleFlags{Kind: "claude"})
	if resolved.ModelSpec != "" {
		t.Fatalf("model spec survived kind override: %#v", resolved)
	}
	if got := clampSpawnEffort("max", "grok", "max"); got != "xhigh" {
		t.Fatalf("grok effort clamp=%q, want xhigh", got)
	}
}

func TestLowerLayerLaneModelIsDiscardedByHigherLaneKind(t *testing.T) {
	// JS: "resolveRoleSettings: a model from a layer below the lane kind is discarded"
	// Mutation captured: removing the lane kind layer gate forwards a stale model to the wrong CLI configuration.
	root := t.TempDir()
	repo, skill, home := filepath.Join(root, "repo"), filepath.Join(root, "skill"), filepath.Join(root, "home")
	for _, dir := range []string{repo, filepath.Join(skill, "roles"), home, filepath.Join(repo, ".agents")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, "config.defaults"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := "lane.build.roles=worker\nlane.build.kind=pi\n"
	if err := os.WriteFile(filepath.Join(repo, ".agents", "soho.conf"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(home, ".config", "soho")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte("lane.build.model=lower-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"HOME": home, "SOHO_SKILL_DIR": skill}
	ctx := core.LoadConfig(env, repo)
	resolved := core.ResolveRoleSettings("worker", &ctx, env, repo, core.RoleFlags{})
	if resolved.Kind != "pi" || resolved.ModelSpec != "" {
		t.Fatalf("lower-layer lane model survived: %#v", resolved)
	}
}

func TestChangedNativeArgsDeclineOtherwiseReusableWorker(t *testing.T) {
	// Mutation captured: deleting the native-args equality check reuses a live CLI process with stale arguments.
	root := t.TempDir()
	cwd, state, roles, skill, bin := filepath.Join(root, "repo"), filepath.Join(root, "state"), filepath.Join(root, "roles"), filepath.Join(root, "skill"), filepath.Join(root, "bin")
	for _, dir := range []string{cwd, state, roles, filepath.Join(skill, "roles"), bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(state, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	row := strings.Join([]string{"worker", "p1", "grok", "worker", "xai", "1", cwd, "T", "", "ask", "worker", "", "", "--old-arg"}, "\t") + "\n"
	if err := os.WriteFile(filepath.Join(ws, "agents.tsv"), []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws", "SOHO_ROLES": roles, "SOHO_SKILL_DIR": skill, "SOHO_ROLE_WORKER_ARGS": "--new-arg"}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	name, unavailable, blocked := FindReusable("worker", "grok", cwd, "", "", "ask", ctx, env, cwd)
	if name != "" || unavailable != "" || blocked {
		t.Fatalf("reused worker with stale native args: name=%q unavailable=%q blocked=%v", name, unavailable, blocked)
	}
}

func TestUniqueNameAddsSuffixForLiveAgent(t *testing.T) {
	// Mutation captured: returning a live agent name creates a second Herdr agent with the same identity.
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker"},{"name":"worker-2"}]}}`}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin}
	if got := UniqueName("worker", env); got != "worker-3" {
		t.Fatalf("UniqueName()=%q", got)
	}
}

func TestAgentNameTakenPreservesLiveAgentsFriction(t *testing.T) {
	// Mutation captured: returning a plain exit error drops the spawn failure from friction.log.
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{}}`}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		AgentNameTaken("worker", env)
	}()
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok || exitErr.Code != 4 || !exitErr.Friction || !strings.Contains(exitErr.Msg, "returned no agent list") {
		t.Fatalf("recovered=%#v", recovered)
	}
}

func TestBlockedTrustStartupKeepsPaneAndExitsSeven(t *testing.T) {
	// JS: "spawn: agent_not_ready → registered, JSON blocked_at_startup, screen, exit 7"
	// Mutation captured: treating an agent_not_ready trust dialog as ready or closing it hides the human approval prompt.
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	state := filepath.Join(root, "state")
	roles := filepath.Join(root, "roles")
	skill := filepath.Join(root, "skill")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{cwd, state, roles, filepath.Join(skill, "roles"), bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}, {Argv: []string{"agent", "start"}, ArgvPrefix: true, Stderr: "agent_not_ready: trust dialog\n", Code: 1}, {Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "Trust this workspace?\n"}}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "HOME": root, "TMPDIR": root, "SOHO_SKILL_DIR": skill, "SOHO_ROLES": roles, "SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws", "HERDR_ENV": "1", "SOHO_LANES": "off", "SOHO_NOWRITE": "0"}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := 0
	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					code = e.Code
					return
				}
				panic(value)
			}
		}()
		CmdSpawn([]string{"worker", "--pane", "p-existing", "--name", "worker"}, ctx, env, cwd)
	}()
	if code != 7 || !strings.Contains(out.String(), `"status": "blocked_at_startup"`) || !strings.Contains(out.String(), "Trust this workspace?") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
	roster, err := os.ReadFile(filepath.Join(state, "ws", "agents.tsv"))
	if err != nil || !strings.Contains(string(roster), "worker\tp-existing") {
		t.Fatalf("blocked worker roster=%q err=%v", roster, err)
	}
}

func TestResumeArgumentsAreRejectedBeforeOpeningPane(t *testing.T) {
	// Mutation captured: allowing Claude resume args sends a new worker into the orchestrator session.
	if got := ResumeArg("claude", []string{"--resume=latest"}); got != "--resume=latest" {
		t.Fatalf("ResumeArg()=%q", got)
	}
	if got := ResumeArg("codex", []string{"-c"}); got != "" {
		t.Fatalf("Codex args should not use the Claude resume denylist: %q", got)
	}
}

func TestConfigNativeArgsStayBoundToResolvedKind(t *testing.T) {
	// JS: "configNativeArgs: drops a scoped value when a kind flag switches the CLI"
	// Mutation captured: forwarding role args resolved for another CLI can resume or broaden the wrong process.
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"role_worker_args": {Value: "--continue"}, "role_worker_kind": {Value: "claude"}}}
	skillDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(skillDir, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"SOHO_SKILL_DIR": skillDir}
	var dropped string
	if got := ConfigNativeArgs("codex", "", "worker", ctx, env, t.TempDir(), func(key, own string) { dropped = key + ":" + own }); got != "" || dropped != "role.worker.args:claude" {
		t.Fatalf("args=%q dropped=%q", got, dropped)
	}
}
