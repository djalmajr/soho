package core_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/communication"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// policyFixture builds an isolated config stack (user under XDG_CONFIG_HOME,
// project under repo/.agents, session under SOHO_DIR, defaults from
// the skill checked out with this tree) and points the process at repo, so
// communication.Load resolves the same files core.LoadConfig read.
func policyFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	skill := skillDir(t) // before the chdir: the test binary's cwd is internal/core
	root := t.TempDir()
	// Canonicalize the existing temporary root before deriving any
	// fixture path (same short-alias vs canonical reason as the core
	// fixture): the product resolves project paths through git, which
	// reports the canonical form.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize the fixture root: %v", err)
	}
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	conf := filepath.Join(root, "conf")
	state := filepath.Join(root, "state")
	for _, dir := range []string{repo, home, conf, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)
	env := platform.Env{
		"HOME": home, "XDG_CONFIG_HOME": conf, "SOHO_DIR": state,
		"SOHO_SKILL_DIR": skill, "HERDR_WORKSPACE_ID": "ws",
		"PATH": testutil.HostPath(),
	}
	return env, repo
}

var policyPackageDir, policyPackageDirErr = os.Getwd()

func skillDir(t *testing.T) string {
	t.Helper()
	// Capture the runtime package directory before any fixture chdir. A
	// cross-compiled test binary must never rely on its build machine path.
	if policyPackageDirErr != nil {
		t.Fatal(policyPackageDirErr)
	}
	skill := filepath.Join(policyPackageDir, "..", "..", "skills", "soho")
	if _, err := os.Stat(filepath.Join(skill, "SKILL.md")); err != nil {
		t.Fatalf("skill directory %q not found: %v", skill, err)
	}
	return skill
}

func writePolicy(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func userConfig(env platform.Env) string {
	return platform.UserConfigPath(platform.Current(), env)
}

func projectConfig(cwd string) string {
	return filepath.Join(cwd, ".agents", "soho.conf")
}

func sessionConfig(env platform.Env) string {
	return filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
}

func ruleOf(t *testing.T, pol communication.Policy, name string) communication.Rule {
	t.Helper()
	for _, rule := range pol.Rules {
		if rule.Name == name {
			return rule
		}
	}
	t.Fatalf("rule %q not in policy (rules: %d)", name, len(pol.Rules))
	return communication.Rule{}
}

func TestWorkerMessagesConfigPrecedence(t *testing.T) {
	// defaults < user < project < session < env for the mode, and the rule
	// is replaced as a unit by the highest layer that names it.
	env, cwd := policyFixture(t)
	writePolicy(t, userConfig(env), "worker_messages=policy\nworker_messages.rules.review.from=role:user\nworker_messages.rules.review.to=role:project\nworker_messages.rules.review.types=review.ready\nworker_messages.rules.review.scope=assignment\n")
	writePolicy(t, projectConfig(cwd), "worker_messages.rules.review.from=role:project\nworker_messages.rules.review.to=role:user\nworker_messages.rules.review.types=review.finding\nworker_messages.rules.review.scope=assignment\n")
	writePolicy(t, sessionConfig(env), "worker_messages.rules.review.types=review.result\n")
	ctx := core.LoadConfig(env, cwd)
	if got := core.Cfg(&ctx, "worker_messages", "", env); got != "policy" {
		t.Fatalf("mode = %q, want policy (user layer)", got)
	}
	if got := core.CfgSource(&ctx, "worker_messages", env); got != "user" {
		t.Fatalf("mode source = %q, want user", got)
	}
	env["SOHO_WORKER_MESSAGES_RULES_REVIEW_FROM"] = "role:env"
	env["SOHO_WORKER_MESSAGES_RULES_REVIEW_TO"] = "role:env"
	env["SOHO_WORKER_MESSAGES_RULES_REVIEW_TYPES"] = "review.question"
	env["SOHO_WORKER_MESSAGES_RULES_REVIEW_SCOPE"] = "assignment"
	pol, err := communication.Load(&ctx, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if pol.Mode != communication.ModePolicy {
		t.Fatalf("mode = %q, want policy", pol.Mode)
	}
	rule := ruleOf(t, pol, "review")
	if rule.Source != "env" {
		t.Fatalf("rule source = %q, want env (the highest layer that names it)", rule.Source)
	}
	if rule.From != "role:env" || rule.Types != "review.question" {
		t.Fatalf("rule fields = %q/%q, want the whole env definition (no mixing with lower layers)", rule.From, rule.Types)
	}
	// The session layer defined only types for review: the env layer named
	// the rule first, so the session partial is not consulted at all. Drop
	// the env rule and the session layer (which alone is partial) must fail.
	delete(env, "SOHO_WORKER_MESSAGES_RULES_REVIEW_FROM")
	delete(env, "SOHO_WORKER_MESSAGES_RULES_REVIEW_TO")
	delete(env, "SOHO_WORKER_MESSAGES_RULES_REVIEW_TYPES")
	delete(env, "SOHO_WORKER_MESSAGES_RULES_REVIEW_SCOPE")
	if _, err := communication.Load(&ctx, env); err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("partial session layer must be a visible error, got %v", err)
	}
	// With the session layer gone, project (complete) wins over user. The
	// layers were read into ctx, so reload the config without the file.
	os.Remove(sessionConfig(env))
	ctx = core.LoadConfig(env, cwd)
	pol, err = communication.Load(&ctx, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rule = ruleOf(t, pol, "review")
	if rule.Source != "project" || rule.From != "role:project" || rule.Types != "review.finding" {
		t.Fatalf("rule = %+v, want the whole project definition over user", rule)
	}
}

func TestWorkerMessagesSessionUsesConfiguredStateDir(t *testing.T) {
	env, cwd := policyFixture(t)
	delete(env, "SOHO_DIR")
	custom := filepath.Join(t.TempDir(), "custom-state")
	writePolicy(t, userConfig(env), "state_dir="+custom+"\nworker_messages=policy\n")
	first := core.LoadConfig(env, cwd)
	session := core.SessionConfPath(&first, env, cwd)
	if !strings.HasPrefix(session, custom+string(filepath.Separator)) {
		t.Fatalf("session outside custom state: %s", session)
	}
	writePolicy(t, session, "layout=stack\nworker_messages=off\n")
	ctx := core.LoadConfig(env, cwd)
	pol, err := communication.Load(&ctx, env)
	if err != nil || pol.Mode != communication.ModeOff || core.Cfg(&ctx, "layout", "", env) != "stack" {
		t.Fatalf("configured session ignored: mode=%s err=%v sources=%v", pol.Mode, err, ctx.Sources)
	}
}

func TestWorkerMessagesReadErrorIsBoundToLoadedDirectory(t *testing.T) {
	env, cwd := policyFixture(t)
	other := filepath.Join(t.TempDir(), "other")
	file := projectConfig(other)
	if err := os.MkdirAll(file, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := core.LoadConfig(env, other)
	if _, err := communication.Load(&ctx, env); err == nil || !strings.Contains(err.Error(), projectConfig(other)) {
		t.Fatalf("loaded cwd %s (process cwd %s) read error was lost: %v", other, cwd, err)
	}
}

func TestWorkerMessagesLoadsCurrentWorkspaceSessionOnce(t *testing.T) {
	env, cwd := policyFixture(t)
	delete(env, "HERDR_WORKSPACE_ID")
	env["HERDR_ENV"] = "1"
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"workspace_id":"probe-ws"}}}`}}); err != nil {
		t.Fatal(err)
	}
	env["PATH"] = bin + string(os.PathListSeparator) + env["PATH"]
	env["SOHO_FAKECLI_CONFIG"] = bin
	writePolicy(t, filepath.Join(env["SOHO_DIR"], "probe-ws", "session.conf"), "layout=stack\n")
	ctx := core.LoadConfig(env, cwd)
	if core.Cfg(&ctx, "layout", "", env) != "stack" {
		t.Fatal("current workspace session was not read")
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("one config load queried the current workspace %d times", len(calls))
	}
}

func envPrefix(name, field string) string {
	return "SOHO_WORKER_MESSAGES_RULES_" + strings.ToUpper(name) + "_" + strings.ToUpper(field)
}

func TestWorkerMessagesConfigTombstone(t *testing.T) {
	// enabled=off alone in a higher layer deactivates the inherited rule;
	// the lower definition is neither used nor required to be complete.
	env, cwd := policyFixture(t)
	writePolicy(t, userConfig(env), "worker_messages=policy\nworker_messages.rules.review.from=role:implementer\nworker_messages.rules.review.to=role:reviewer\nworker_messages.rules.review.types=review.ready\nworker_messages.rules.review.scope=assignment\n")
	writePolicy(t, projectConfig(cwd), "worker_messages.rules.review.enabled=off\n")
	ctx := core.LoadConfig(env, cwd)
	pol, err := communication.Load(&ctx, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rule := ruleOf(t, pol, "review")
	if rule.Source != "project" || rule.Enabled {
		t.Fatalf("rule = %+v, want the project tombstone (Enabled=false)", rule)
	}
	req := communication.Request{
		From: communication.Participant{Role: "implementer"},
		To:   communication.Participant{Role: "reviewer"},
		Type: "review.ready", AssignmentID: "a1", Inbound: "auto", Assigned: true,
	}
	if err := pol.Authorize(req); err == nil {
		t.Fatal("tombstoned rule must refuse the message")
	}
	// The tombstone also wins from the environment (the highest layer).
	env[envPrefix("review", "enabled")] = "off"
	pol, err = communication.Load(&ctx, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rule := ruleOf(t, pol, "review"); rule.Source != "env" || rule.Enabled {
		t.Fatalf("rule = %+v, want the env tombstone", rule)
	}
}

func TestWorkerMessagesConfigOrigin(t *testing.T) {
	// Each layer's rule is exposed with its source and original name.
	env, cwd := policyFixture(t)
	writePolicy(t, userConfig(env), "worker_messages=policy\nworker_messages.rules.u_from.from=role:user\nworker_messages.rules.u_from.to=role:user\nworker_messages.rules.u_from.types=review.ready\nworker_messages.rules.u_from.scope=assignment\n")
	writePolicy(t, projectConfig(cwd), "worker_messages.rules.p_from.from=role:project\nworker_messages.rules.p_from.to=role:project\nworker_messages.rules.p_from.types=review.ready\nworker_messages.rules.p_from.scope=assignment\n")
	writePolicy(t, sessionConfig(env), "worker_messages.rules.s_from.from=role:session\nworker_messages.rules.s_from.to=role:session\nworker_messages.rules.s_from.types=review.ready\nworker_messages.rules.s_from.scope=assignment\n")
	ctx := core.LoadConfig(env, cwd)
	pol, err := communication.Load(&ctx, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{"u_from": "user", "p_from": "project", "s_from": "session"}
	for name, source := range want {
		rule := ruleOf(t, pol, name)
		if rule.Source != source {
			t.Errorf("rule %s source = %q, want %q", name, rule.Source, source)
		}
		if rule.Original != name {
			t.Errorf("rule %s original = %q, want the original name as written", name, rule.Original)
		}
	}
	// Layers were recorded as units by the source.
	sources := map[string]bool{}
	for _, layer := range ctx.Layers {
		sources[layer.Source] = true
	}
	for _, source := range []string{"defaults", "user", "project", "session"} {
		if !sources[source] {
			t.Errorf("layer %q not recorded in Config.Layers", source)
		}
	}
	if entry, ok := ctx.Layers[0].Entries["worker_messages"]; ok && entry.Source != "defaults" {
		t.Errorf("defaults layer entry source = %q", entry.Source)
	}
	if !hasEntry(ctx, "worker_messages_rules_u_from_from", "user") {
		t.Error("user layer must keep the rule entries it defined")
	}
}

func hasEntry(ctx core.Config, key, source string) bool {
	for _, layer := range ctx.Layers {
		if layer.Source != source {
			continue
		}
		if _, ok := layer.Entries[key]; ok {
			return true
		}
	}
	return false
}

func TestWorkerMessagesConfigInvalidMode(t *testing.T) {
	env, cwd := policyFixture(t)
	writePolicy(t, userConfig(env), "worker_messages=policy\nworker_messages.rules.review.from=role:implementer\nworker_messages.rules.review.to=role:reviewer\nworker_messages.rules.review.types=review.ready\nworker_messages.rules.review.scope=assignment\n")
	writePolicy(t, projectConfig(cwd), "worker_messages=banana\n")
	ctx := core.LoadConfig(env, cwd)
	if _, err := communication.Load(&ctx, env); err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("invalid mode must be a visible error, got %v", err)
	}
	// The env mode follows Cfg and beats the files.
	env["SOHO_WORKER_MESSAGES"] = "policy"
	pol, err := communication.Load(&ctx, env)
	if err != nil {
		t.Fatalf("Load with env mode: %v", err)
	}
	if pol.Mode != communication.ModePolicy {
		t.Fatalf("mode = %q, want policy from env", pol.Mode)
	}
	env["SOHO_WORKER_MESSAGES"] = "off"
	pol, err = communication.Load(&ctx, env)
	if err != nil {
		t.Fatalf("Load with env mode off: %v", err)
	}
	if pol.Mode != communication.ModeOff || len(pol.Rules) != 0 {
		t.Fatalf("mode off must not load rules: %+v", pol)
	}
}

func TestWorkerMessagesConfigOffSkipsRules(t *testing.T) {
	// With mode off the rules are not loaded at all: broken rule keys in a
	// readable file are not an error (nothing is authorized anyway).
	env, cwd := policyFixture(t)
	writePolicy(t, userConfig(env), "worker_messages.rules.broken.bogus=1\n")
	ctx := core.LoadConfig(env, cwd)
	// The mode default comes from the skill defaults layer.
	if got := core.CfgSource(&ctx, "worker_messages", env); got != "defaults" {
		t.Fatalf("mode default source = %q, want defaults", got)
	}
	pol, err := communication.Load(&ctx, env)
	if err != nil {
		t.Fatalf("mode off must not validate rules: %v", err)
	}
	if pol.Mode != communication.ModeOff || len(pol.Rules) != 0 {
		t.Fatalf("policy = %+v, want mode off with no rules", pol)
	}
}

func TestWorkerMessagesConfigUnreadableFile(t *testing.T) {
	// An existing config file that cannot be read is refused by the policy
	// (the shared loader drops it silently for the legacy keys).
	env, cwd := policyFixture(t)
	writePolicy(t, userConfig(env), "worker_messages=policy\nworker_messages.rules.review.from=role:implementer\nworker_messages.rules.review.to=role:reviewer\nworker_messages.rules.review.types=review.ready\nworker_messages.rules.review.scope=assignment\n")
	if err := os.MkdirAll(projectConfig(cwd), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := core.LoadConfig(env, cwd)
	// The legacy resolution silently kept the lower layer (compatibility).
	if got := core.Cfg(&ctx, "worker_messages", "", env); got != "policy" {
		t.Fatalf("legacy mode = %q, want the user value (silent drop kept)", got)
	}
	if _, err := communication.Load(&ctx, env); err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("an existing unreadable policy file must be a visible error, got %v", err)
	}
}

func TestWorkerMessagesConfigUnreadableUserAndSession(t *testing.T) {
	for _, layer := range []string{"user", "session"} {
		t.Run(layer, func(t *testing.T) {
			env, cwd := policyFixture(t)
			writePolicy(t, projectConfig(cwd), "worker_messages=policy\nworker_messages.rules.review.from=role:implementer\nworker_messages.rules.review.to=role:reviewer\nworker_messages.rules.review.types=review.ready\nworker_messages.rules.review.scope=assignment\n")
			file := userConfig(env)
			if layer == "session" {
				file = sessionConfig(env)
			}
			if err := os.MkdirAll(file, 0700); err != nil {
				t.Fatal(err)
			}
			ctx := core.LoadConfig(env, cwd)
			if got := core.Cfg(&ctx, "worker_messages", "", env); got != "policy" {
				t.Fatalf("traditional project value changed: %q", got)
			}
			if _, err := communication.Load(&ctx, env); err == nil || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), layer+" config") {
				t.Fatalf("unreadable %s file inherited permissions: %v", layer, err)
			}
		})
	}
}

func TestWorkerMessagesBrokenPreferredWorktreeConfig(t *testing.T) {
	env, main := policyFixture(t)
	linked := filepath.Join(filepath.Dir(main), "linked")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		cmd.Env = append(testutil.CleanEnv(t), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture")
	git("worktree", "add", "--detach", "-q", linked)
	writePolicy(t, projectConfig(main), "worker_messages=policy\nworker_messages.rules.review.from=role:implementer\nworker_messages.rules.review.to=role:reviewer\nworker_messages.rules.review.types=review.ready\nworker_messages.rules.review.scope=assignment\n")
	file := projectConfig(linked)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(linked, "missing.conf"), file); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege required for this case: %v", err)
		}
		t.Fatal(err)
	}
	ctx := core.LoadConfig(env, linked)
	if got := core.Cfg(&ctx, "worker_messages", "", env); got != "policy" {
		t.Fatalf("traditional main fallback changed: %q", got)
	}
	if _, err := communication.Load(&ctx, env); err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("broken preferred worktree config inherited permissions: %v", err)
	}
}

func TestWorkerMessagesTraditionalCfgUnchanged(t *testing.T) {
	// The traditional Cfg resolution ignores the new keys and keeps its
	// precedence (session > project, env > session) for the old keys.
	env, cwd := policyFixture(t)
	writePolicy(t, userConfig(env), "layout=columns\nworker_messages=policy\nworker_messages.rules.review.from=role:implementer\n")
	writePolicy(t, projectConfig(cwd), "layout=split\nworker_messages.rules.review.to=role:reviewer\n")
	writePolicy(t, sessionConfig(env), "layout=tab\nworker_messages=off\n")
	ctx := core.LoadConfig(env, cwd)
	if got := core.Cfg(&ctx, "layout", "", env); got != "tab" {
		t.Fatalf("layout = %q, want tab (session wins, as before)", got)
	}
	if got := core.CfgSource(&ctx, "layout", env); got != "session" {
		t.Fatalf("layout source = %q, want session", got)
	}
	env["SOHO_LAYOUT"] = "split"
	if got := core.Cfg(&ctx, "layout", "", env); got != "split" {
		t.Fatalf("layout with env = %q, want split (env wins, as before)", got)
	}
	// The new keys reach Cfg through the same layers.
	if got := core.Cfg(&ctx, "worker_messages", "", env); got != "off" || core.CfgSource(&ctx, "worker_messages", env) != "session" {
		t.Fatalf("worker_messages = %q/%q, want off/session", got, core.CfgSource(&ctx, "worker_messages", env))
	}
	// And LoadConfig keeps recording Sources exactly as before.
	wantSources := "defaults user project session"
	if got := strings.Join(ctx.Sources, " "); got != wantSources {
		t.Fatalf("sources = %q, want %q", got, wantSources)
	}
}

func TestWorkerMessagesConfigSetValidation(t *testing.T) {
	// config set knows the mode key and the rule field keys.
	if !core.ConfigKeyOk("worker_messages") {
		t.Error("worker_messages must be a known key")
	}
	for _, field := range []string{"from", "to", "types", "scope", "enabled"} {
		key := "worker_messages.rules.review_request." + field
		if !core.ConfigKeyOk(key) {
			t.Errorf("%s must be a known key", key)
		}
	}
	if !core.ConfigValueOk("worker_messages", "policy", nil, "") || !core.ConfigValueOk("worker_messages", "off", nil, "") {
		t.Error("worker_messages must accept off|policy")
	}
	if core.ConfigValueOk("worker_messages", "banana", nil, "") {
		t.Error("worker_messages must refuse other values")
	}
}

func TestWorkerMessagesConfigCommandExposesRules(t *testing.T) {
	// The config command shows the mode row and the rule keys with their
	// original names and the layer that holds them.
	env, cwd := policyFixture(t)
	writePolicy(t, userConfig(env), "worker_messages=policy\nworker_messages.rules.review_request.from=role:implementer\nworker_messages.rules.review_request.to=role:reviewer\nworker_messages.rules.review_request.types=review.ready,review.question\nworker_messages.rules.review_request.scope=assignment\n")
	ctx := core.LoadConfig(env, cwd)
	var out bytes.Buffer
	oldOut := platform.Stdout
	platform.Stdout = &out
	core.CmdConfig(&ctx, env, cwd)
	platform.Stdout = oldOut
	text := out.String()
	for _, want := range []string{
		"worker_messages    policy",
		"worker_messages.rules.review_request.from",
		"role:implementer",
		"worker_messages.rules.review_request.types",
		"review.ready,review.question",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config output missing %q:\n%s", want, text)
		}
	}
}
