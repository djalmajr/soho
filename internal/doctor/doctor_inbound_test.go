package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The receiver side of unsigned peer messages (DJA-610): the deprecated
// signed-peer settings, the leftover seen-state file, and every agent without
// a UserPromptSubmit hook.

type inboundDoctor struct {
	t    *testing.T
	env  platform.Env
	root string
	cwd  string
	conf string // the user config dir
}

func newInboundDoctor(t *testing.T) *inboundDoctor {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &inboundDoctor{t: t, root: root, cwd: filepath.Join(root, "proj"), conf: filepath.Join(root, "xdg", "soho")}
	for _, dir := range []string{d.cwd, filepath.Join(root, "bin"), filepath.Join(root, "skill"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d.env = platform.Env{
		"HOME": filepath.Join(root, "home"), "USERPROFILE": filepath.Join(root, "home"), "APPDATA": filepath.Join(root, "xdg"),
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg"), "PATH": filepath.Join(root, "bin"),
		"SOHO_DIR": filepath.Join(root, "state"), "SOHO_LANES": "off", "SOHO_SKILL_DIR": filepath.Join(root, "skill"),
	}
	return d
}

func (d *inboundDoctor) write(path, body string, mode os.FileMode) {
	d.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		d.t.Fatal(err)
	}
}

func (d *inboundDoctor) user(body string) { d.write(filepath.Join(d.conf, "config"), body, 0o600) }

func (d *inboundDoctor) protect() {
	d.write(filepath.Join(d.cwd, ".claude", "settings.json"), `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook reminder"}]}]}}`, 0o600)
}

func (d *inboundDoctor) out() string {
	d.t.Helper()
	ctx := core.LoadConfig(d.env, d.cwd)
	var out strings.Builder
	DoctorCheck(&ctx, d.env, d.cwd, &out)
	return out.String()
}

func inboundWarnLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "warn") && (strings.Contains(line, "peer") || strings.Contains(line, "inbound")) {
			lines = append(lines, strings.TrimLeft(strings.TrimPrefix(line, "warn"), " "))
		}
	}
	return lines
}

func wantWarn(t *testing.T, out, want string) {
	t.Helper()
	for _, line := range inboundWarnLines(out) {
		if line == want {
			return
		}
	}
	t.Fatalf("no warning %q in:\n%s", want, strings.Join(inboundWarnLines(out), "\n"))
}

// A bare project (no settings, no hooks, no deprecated config) warns that its
// Claude Code panes do not check peer messages: the hook is the protection.
func TestDoctorInboundBareProjectWarnsAboutTheHook(t *testing.T) {
	d := newInboundDoctor(t)
	out := d.out()
	wantWarn(t, out, fmt.Sprintf("%s and %s have no 'soho hook reminder' or 'soho hook inbound' in UserPromptSubmit: the Claude Code panes of this project do not check peer messages (run 'soho setup')",
		filepath.Join(d.cwd, ".claude", "settings.json"), filepath.Join(d.cwd, ".claude", "settings.local.json")))
	if got := len(inboundWarnLines(out)); got != 1 {
		t.Fatalf("one peer warning, got %d:\n%s", got, out)
	}
	// The output is stable across two runs.
	if d.out() != out {
		t.Fatal("doctor output is not stable")
	}
	// With the hook installed and no deprecated config, nothing peer-related
	// warns.
	d.protect()
	if got := inboundWarnLines(d.out()); len(got) != 0 {
		t.Fatalf("a protected project without deprecated config warns: %v", got)
	}
}

// The signed peer settings of the previous design are retired: any file that
// still sets one gets the retired warning (the family_check style), and the
// doctor reads nothing else for it (no key checks follow).
func TestDoctorInboundRetiredConfigWarnings(t *testing.T) {
	d := newInboundDoctor(t)
	d.protect()
	project := filepath.Join(d.cwd, ".agents", "soho.conf")
	d.write(project, "inbound_auth=enforce\ninbound_allow=mac\n", 0o600)
	out := d.out()
	wantWarn(t, out, fmt.Sprintf("config: inbound_auth is retired (project); delete the inbound_auth line from %s", project))
	wantWarn(t, out, fmt.Sprintf("config: inbound_allow is retired (project); delete the inbound_allow line from %s", project))

	d = newInboundDoctor(t)
	d.protect()
	user := core.EffectiveUserConfigFile(platform.Current(), d.env, core.UserSelection{})
	d.user("inbound_auth=off\n")
	out = d.out()
	wantWarn(t, out, "config: inbound_auth is retired (user); delete the inbound_auth line from "+user)
	// A retired value reads nothing else: no key or policy side effects.
	if strings.Contains(out, "peer key") || strings.Contains(out, "peer-keys") {
		t.Fatalf("a retired setting switched checks on:\n%s", out)
	}

	// The session file (a state dir session.conf) is retired too.
	d = newInboundDoctor(t)
	d.protect()
	d.env["HERDR_WORKSPACE_ID"] = "ws"
	session := filepath.Join(d.root, "state", "ws", "session.conf")
	d.write(session, "inbound_auth=off\n", 0o600)
	out = d.out()
	wantWarn(t, out, fmt.Sprintf("config: inbound_auth is retired (session); delete the inbound_auth line from %s", session))
}

// The leftovers of the previous design warn one by one; a clean user dir
// prints none of them. Nothing is deleted automatically.
func TestDoctorInboundLeftoverWarnings(t *testing.T) {
	d := newInboundDoctor(t)
	d.protect()
	keysDir := filepath.Join(d.conf, "peer-keys")
	seen := filepath.Join(d.conf, "peer-inbound-seen.tsv")
	d.write(filepath.Join(keysDir, "mac.key"), "0123456789abcdef0123456789abcdef\n", 0o600)
	body := "mac\t0f23aac6\t2026-10-19T12:00:00Z\t2026-10-19T12:00:01Z\n"
	d.write(seen, body, 0o600)
	out := d.out()
	wantWarn(t, out, "peer keys in "+keysDir+" are no longer used; delete the directory")
	wantWarn(t, out, "peer replay cache "+seen+" is no longer used; delete it")
	// The doctor does not touch the leftovers.
	if raw, err := os.ReadFile(seen); err != nil || string(raw) != body {
		t.Fatalf("the doctor changed the cache: %q (%v)", raw, err)
	}
	if _, err := os.Stat(filepath.Join(keysDir, "mac.key")); err != nil {
		t.Fatalf("the doctor changed the keys dir: %v", err)
	}

	// Remove one leftover at a time: each warning goes with its file.
	if err := os.Remove(seen); err != nil {
		t.Fatal(err)
	}
	if got := inboundWarnLines(d.out()); len(got) != 1 || !strings.Contains(got[0], "peer keys in") {
		t.Fatalf("without the cache: %v", got)
	}
	if err := os.RemoveAll(keysDir); err != nil {
		t.Fatal(err)
	}
	if got := inboundWarnLines(d.out()); len(got) != 0 {
		t.Fatalf("a clean user dir prints none of them: %v", got)
	}
}

func TestDoctorInboundProjectWithoutTheHook(t *testing.T) {
	settings := func(d *inboundDoctor) string { return filepath.Join(d.cwd, ".claude", "settings.json") }
	want := func(d *inboundDoctor) string {
		return fmt.Sprintf("%s and %s have no 'soho hook reminder' or 'soho hook inbound' in UserPromptSubmit: the Claude Code panes of this project do not check peer messages (run 'soho setup')", settings(d), filepath.Join(d.cwd, ".claude", "settings.local.json"))
	}
	cases := map[string]struct {
		body    string
		protect bool
	}{
		"no settings file":         {"", false},
		"settings without hooks":   {`{}`, false},
		"only a SessionStart hook": {`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"soho hook doctor"}]}]}}`, false},
		"a foreign prompt hook":    {`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`, false},
		"broken json":              {`{`, false},
		"hook reminder":            {`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook reminder"}]}]}}`, true},
		"hook inbound":             {`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound"}]}]}}`, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := newInboundDoctor(t)
			if c.body != "" {
				d.write(settings(d), c.body, 0o600)
			}
			out := d.out()
			found := false
			for _, line := range inboundWarnLines(out) {
				found = found || line == want(d)
			}
			if found == c.protect {
				t.Fatalf("protected=%t but warning present=%t:\n%s", c.protect, found, out)
			}
		})
	}
}

func TestDoctorInboundNamesUnprotectedAgents(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	state := filepath.Join(root, "state")
	for _, dir := range []string{bin, filepath.Join(state, "ws")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	agents := `{"result":{"agents":[{"name":"build","pane_id":"p1"},{"name":"review","pane_id":"p2"},{"name":"plan","pane_id":"p3"},{"name":"gone-elsewhere","pane_id":"other"}]}}`
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: agents}}); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\n" +
		"build\tp1\tclaude\timplementer\n" +
		"review\tp2\tcodex\treviewer\n" +
		"plan\tp3\tpi\tplanner\n" +
		"gone\tp9\tcursor\tscouter\n" +
		"gone-elsewhere\tp4\tgrok\tscouter\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": bin, "HERDR_ENV": "1", "HERDR_WORKSPACE_ID": "ws", "SOHO_DIR": state, "SOHO_FAKECLI_CONFIG": bin, "HOME": root}
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	var out strings.Builder
	s := &Say{Out: &out}
	doctorUnprotectedAgents(&ctx, env, root, s)
	want := "warn   review (codex) is not protected: no 'soho hook inbound --codex' in UserPromptSubmit (run 'soho setup')\nwarn   plan (pi) is not protected: no inbound hook for pi yet (run 'soho setup')\n"
	if out.String() != want {
		t.Fatalf("output = %q\nwant     %q", out.String(), want)
	}
	// A managed extension from an older release is named as out of date,
	// not as a missing hook; the current text clears the warning.
	extension := filepath.Join(root, ".pi", "extensions", "soho-inbound.ts")
	if err := os.MkdirAll(filepath.Dir(extension), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extension, []byte(setuptext.PiInboundMarker+"\n// an older release's text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	doctorUnprotectedAgents(&ctx, env, root, s)
	staleWant := "warn   review (codex) is not protected: no 'soho hook inbound --codex' in UserPromptSubmit (run 'soho setup')\nwarn   plan (pi) may not be protected: .pi/extensions/soho-inbound.ts differs from this soho's inbound extension (run 'soho setup')\n"
	if out.String() != staleWant {
		t.Fatalf("stale output = %q\nwant           %q", out.String(), staleWant)
	}
	if err := os.WriteFile(extension, []byte(setuptext.PiInboundExtension()), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	doctorUnprotectedAgents(&ctx, env, root, s)
	if want := "warn   review (codex) is not protected: no 'soho hook inbound --codex' in UserPromptSubmit (run 'soho setup')\n"; out.String() != want {
		t.Fatalf("current output = %q\nwant             %q", out.String(), want)
	}
	if err := os.Remove(extension); err != nil {
		t.Fatal(err)
	}
	// Outside Herdr, or without herdr, it says nothing and does not crash.
	out.Reset()
	delete(env, "HERDR_ENV")
	doctorUnprotectedAgents(&ctx, env, root, s)
	if out.Len() != 0 {
		t.Fatalf("outside Herdr: %q", out.String())
	}
}

func TestDoctorCodexInboundHook(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		protected bool
	}{
		{"missing", "", false}, {"empty", `{"hooks":{}}`, false}, {"wrong event", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]}]}}`, false},
		{"without flag", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound"}]}]}}`, false},
		{"wrong type", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"prompt","command":"soho hook inbound --codex"}]}]}}`, false},
		{"installed", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]}]}}`, true},
		{"async true", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex","async":true}]}]}}`, false},
		{"async false", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex","async":false}]}]}}`, true},
		{"async null", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex","async":null}]}]}}`, false},
		{"async string", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex","async":"false"}]}]}}`, false},
		{"async number", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex","async":0}]}]}}`, false},
		{"async array", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex","async":[]}]}]}}`, false},
		{"async object", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex","async":{}}]}]}}`, false},
		{"numeric-command", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":7}]}]}}`, false},
		{"bad-row-before-protected", `{"hooks":{"UserPromptSubmit":[7,{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]}]}}`, false},
		{"bad-row-after-protected", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]},7]}}`, false},
		{"bad-command-before-protected", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":7},{"type":"command","command":"soho hook inbound --codex"}]}]}}`, false},
		{"bad-command-after-protected", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook inbound --codex"},{"command":7}]}]}}`, false},
		{"bad-command-true", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":true},{"type":"command","command":"soho hook inbound --codex"}]}]}}`, false},
		{"bad-command-array", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":[]},{"type":"command","command":"soho hook inbound --codex"}]}]}}`, false},
		{"bad-command-object", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"command":{}},{"type":"command","command":"soho hook inbound --codex"}]}]}}`, false},
		{"null-row-before-protected", `{"hooks":{"UserPromptSubmit":[null,{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]}]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newInboundDoctor(t)
			d.env["HERDR_ENV"], d.env["HERDR_WORKSPACE_ID"], d.env["SOHO_FAKECLI_CONFIG"] = "1", "ws", d.env["PATH"]
			d.write(filepath.Join(d.env["SOHO_DIR"], "ws", "agents.tsv"), "review\tp2\tcodex\treviewer\n", 0600)
			if _, e := fakecli.Install(t, d.env["PATH"], "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"review","pane_id":"p2"}]}}`}}); e != nil {
				t.Fatal(e)
			}
			if tc.raw != "" {
				d.write(filepath.Join(d.cwd, ".codex", "hooks.json"), tc.raw, 0600)
			}
			ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
			var out strings.Builder
			doctorUnprotectedAgents(&ctx, d.env, d.cwd, &Say{Out: &out})
			has := strings.Contains(out.String(), "review (codex) is not protected")
			if has == tc.protected {
				t.Fatalf("protected=%v warnings=%s", tc.protected, out.String())
			}
			if has && !strings.HasSuffix(strings.TrimSpace(out.String()), "(run 'soho setup')") {
				t.Fatalf("no setup advice: %s", out.String())
			}
		})
	}
}

func TestDoctorPiInboundExtension(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		protected     bool
	}{
		{"missing", "", false},
		{"installed", setuptext.PiInboundExtension(), true},
		{"marked-different", setuptext.PiInboundMarker + "\nold content\n", false},
		{"unmarked", "export default function () {}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newInboundDoctor(t)
			d.env["HERDR_ENV"], d.env["HERDR_WORKSPACE_ID"], d.env["SOHO_FAKECLI_CONFIG"] = "1", "ws", d.env["PATH"]
			d.write(filepath.Join(d.env["SOHO_DIR"], "ws", "agents.tsv"), "review\tp2\tpi\treviewer\n", 0600)
			if _, err := fakecli.Install(t, d.env["PATH"], "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"review","pane_id":"p2"}]}}`}}); err != nil {
				t.Fatal(err)
			}
			if tc.name != "missing" {
				d.write(filepath.Join(d.cwd, ".pi", "extensions", "soho-inbound.ts"), tc.content, 0600)
			}
			var out strings.Builder
			ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
			doctorUnprotectedAgents(&ctx, d.env, d.cwd, &Say{Out: &out})
			warned := strings.Contains(out.String(), "review (pi) ")
			if warned == tc.protected || (warned && !strings.HasSuffix(strings.TrimSpace(out.String()), "(run 'soho setup')")) {
				t.Fatalf("protected=%v output=%s", tc.protected, out.String())
			}
		})
	}
}
