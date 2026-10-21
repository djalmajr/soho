package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The receiver-side warnings of signed peer messages (DJA-196).

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

func (d *inboundDoctor) key(name string) string {
	d.t.Helper()
	path, err := peerauth.NewKey(d.conf, name)
	if err != nil {
		d.t.Fatal(err)
	}
	return path
}

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

func warnLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "warn") && (strings.Contains(line, "inbound") || strings.Contains(line, "peer key") || strings.Contains(line, "not protected")) {
			lines = append(lines, strings.TrimLeft(strings.TrimPrefix(line, "warn"), " "))
		}
	}
	return lines
}

func wantWarn(t *testing.T, out, want string) {
	t.Helper()
	for _, line := range warnLines(out) {
		if line == want {
			return
		}
	}
	t.Fatalf("no warning %q in:\n%s", want, strings.Join(warnLines(out), "\n"))
}

func TestDoctorInboundDefaultOutputIsUnchanged(t *testing.T) {
	d := newInboundDoctor(t)
	base := d.out()
	if got := warnLines(base); len(got) != 0 {
		t.Fatalf("default configuration warns about inbound: %v", got)
	}
	// inbound_auth=off, a pile of keys and a protected project: still the same.
	d.user("inbound_auth=off\n")
	for _, name := range []string{"mac", "local"} {
		d.key(name)
	}
	d.write(filepath.Join(d.conf, "peer-keys", "garbage.key"), "garbage", 0o644)
	d.protect()
	withOff := d.out()
	// The project settings add their own ok line; strip it for the compare by
	// building the control the same way.
	control := newInboundDoctor(t)
	control.protect()
	// Different temp roots: compare with the root replaced.
	norm := func(out, root string) string { return strings.ReplaceAll(out, root, "<root>") }
	if norm(withOff, d.root) != norm(control.out(), control.root) {
		t.Fatalf("inbound_auth=off with keys changed the doctor output:\n%s\n---\n%s", norm(withOff, d.root), norm(control.out(), control.root))
	}
	// The default output also reads the same twice.
	if norm(d.out(), d.root) != norm(withOff, d.root) {
		t.Fatal("doctor output is not stable")
	}
	if strings.Contains(norm(base, d.root), "inbound") {
		t.Fatal("the default output mentions inbound")
	}
}

func TestDoctorInboundIgnoredOutsideTheUserFile(t *testing.T) {
	d := newInboundDoctor(t)
	d.write(filepath.Join(d.cwd, ".agents", "soho.conf"), "inbound_auth=enforce\ninbound_allow=mac\n", 0o600)
	out := d.out()
	project := filepath.Join(d.cwd, ".agents", "soho.conf")
	user := filepath.Join(d.conf, "config")
	wantWarn(t, out, fmt.Sprintf("config: inbound_auth in the project file %s is ignored; only the user file %s sets it", project, user))
	wantWarn(t, out, fmt.Sprintf("config: inbound_allow in the project file %s is ignored; only the user file %s sets it", project, user))

	d = newInboundDoctor(t)
	d.env["SOHO_INBOUND_AUTH"], d.env["SOHO_INBOUND_ALLOW"] = "warn", "mac"
	out = d.out()
	user = filepath.Join(d.conf, "config")
	wantWarn(t, out, fmt.Sprintf("config: SOHO_INBOUND_AUTH is ignored; only the user file %s sets inbound_auth", user))
	wantWarn(t, out, fmt.Sprintf("config: SOHO_INBOUND_ALLOW is ignored; only the user file %s sets inbound_allow", user))
	// An ignored key does not turn the receiver on: no key checks follow.
	if strings.Contains(out, "peer key local") {
		t.Fatalf("an ignored inbound_auth switched the key checks on:\n%s", out)
	}

	// The session file (a state dir session.conf) is ignored too.
	d = newInboundDoctor(t)
	d.env["HERDR_WORKSPACE_ID"] = "ws"
	d.write(filepath.Join(d.root, "state", "ws", "session.conf"), "inbound_auth=off\n", 0o600)
	out = d.out()
	wantWarn(t, out, fmt.Sprintf("config: inbound_auth in the session file %s is ignored; only the user file %s sets it", filepath.Join(d.root, "state", "ws", "session.conf"), filepath.Join(d.conf, "config")))
}

func TestDoctorInboundInvalidModeAppliesAsEnforce(t *testing.T) {
	d := newInboundDoctor(t)
	d.user("inbound_auth=strict\n")
	out := d.out()
	wantWarn(t, out, "config: inbound_auth='strict' is not off|warn|enforce (it applies as enforce)")
	// And as enforce it runs the key checks.
	wantWarn(t, out, "peer key local is missing: peer messages to this machine's own panes go out unsigned and, with inbound_auth=enforce, are refused; create it with: soho peer-key new local")
}

func TestDoctorInboundKeyChecks(t *testing.T) {
	for _, mode := range []string{"warn", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			d := newInboundDoctor(t)
			d.user("inbound_auth=" + mode + "\ninbound_allow=mac|../evil|missing|bad|loose\n")
			d.key("local")
			d.key("mac")
			d.write(filepath.Join(d.conf, "peer-keys", "bad.key"), "not a key\n", 0o600)
			loose := d.key("loose")
			if runtime.GOOS != "windows" {
				if err := os.Chmod(loose, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out := d.out()
			keys := filepath.Join(d.conf, "peer-keys")
			wantWarn(t, out, "inbound_allow: label '../evil' is not a valid machine name (letters, digits, '.', '_' and '-', at most 64)")
			wantWarn(t, out, fmt.Sprintf("inbound_allow: no key file for 'missing' (%s); create it with: soho peer-key new missing", filepath.Join(keys, "missing.key")))
			wantWarn(t, out, fmt.Sprintf("peer key %s is invalid (it must hold 64 hex characters, 32 bytes); replace it with: soho peer-key new bad", filepath.Join(keys, "bad.key")))
			if runtime.GOOS != "windows" {
				wantWarn(t, out, fmt.Sprintf("peer key %s has mode 0644; chmod 600 it", loose))
				for _, line := range warnLines(out) {
					if strings.Contains(line, "mac.key") || strings.Contains(line, "local.key") {
						t.Fatalf("a good 0600 key was flagged: %s", line)
					}
				}
			} else {
				wantWarn(t, out, "peer key permissions are not checked on Windows; keep the peer-keys directory readable by your user only")
			}
			if strings.Contains(out, "peer key local is missing") {
				t.Fatal("local.key exists, yet it is reported missing")
			}
			// Nothing printed carries a key value.
			raw, _ := os.ReadFile(filepath.Join(keys, "mac.key"))
			if strings.Contains(out, strings.TrimSpace(string(raw))) {
				t.Fatal("the doctor printed a key")
			}
		})
	}
	t.Run("local.key missing", func(t *testing.T) {
		d := newInboundDoctor(t)
		d.user("inbound_auth=warn\n")
		wantWarn(t, d.out(), "peer key local is missing: peer messages to this machine's own panes go out unsigned and, with inbound_auth=enforce, are refused; create it with: soho peer-key new local")
	})
	t.Run("an invalid local.key", func(t *testing.T) {
		d := newInboundDoctor(t)
		d.user("inbound_auth=warn\n")
		d.write(filepath.Join(d.conf, "peer-keys", "local.key"), "short\n", 0o600)
		wantWarn(t, d.out(), fmt.Sprintf("peer key %s is invalid (it must hold 64 hex characters, 32 bytes); replace it with: soho peer-key new local", filepath.Join(d.conf, "peer-keys", "local.key")))
	})
}

func TestDoctorInboundProjectWithoutTheHook(t *testing.T) {
	settings := func(d *inboundDoctor) string { return filepath.Join(d.cwd, ".claude", "settings.json") }
	want := func(d *inboundDoctor) string {
		return fmt.Sprintf("%s has no 'soho hook reminder' or 'soho hook inbound' in UserPromptSubmit: the Claude Code panes of this project do not check peer messages (run 'soho setup')", settings(d))
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
			d.user("inbound_auth=enforce\n")
			d.key("local")
			if c.body != "" {
				d.write(settings(d), c.body, 0o600)
			}
			out := d.out()
			found := false
			for _, line := range warnLines(out) {
				found = found || line == want(d)
			}
			if found == c.protect {
				t.Fatalf("protected=%t but warning present=%t:\n%s", c.protect, found, out)
			}
		})
	}
	t.Run("with inbound_auth=off there is no such warning", func(t *testing.T) {
		d := newInboundDoctor(t)
		d.user("inbound_auth=off\n")
		if got := warnLines(d.out()); len(got) != 0 {
			t.Fatalf("warnings: %v", got)
		}
	})
}

func TestDoctorInboundEnforceNamesUnprotectedAgents(t *testing.T) {
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
	want := "warn   review (codex) is not protected: no 'soho hook inbound --codex' in UserPromptSubmit (run 'soho setup')\nwarn   plan (pi) is not protected: no inbound hook for pi yet\n"
	if out.String() != want {
		t.Fatalf("output = %q\nwant     %q", out.String(), want)
	}
	// Outside Herdr, or without herdr, it says nothing and does not crash.
	out.Reset()
	delete(env, "HERDR_ENV")
	doctorUnprotectedAgents(&ctx, env, root, s)
	if out.Len() != 0 {
		t.Fatalf("outside Herdr: %q", out.String())
	}
}

// A replay cache that cannot be read refuses every peer message until it is
// fixed: the doctor says so, with inbound_auth other than off.
func TestDoctorInboundCorruptReplayCache(t *testing.T) {
	good := "mac\t0f23aac6\t2026-10-19T12:00:00Z\t2026-10-19T12:00:01Z\n"
	cacheWarning := func(out string) string {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "peer replay cache") {
				return strings.TrimLeft(strings.TrimPrefix(line, "warn"), " ")
			}
		}
		return ""
	}
	for _, mode := range []string{"warn", "enforce"} {
		for name, tc := range map[string]struct {
			body string
			line int
		}{
			"too few fields":  {good + "oops\n", 2},
			"too many fields": {"a\tb\tc\td\te\n", 1},
			"unreadable time": {good + good + "mac\t1\t2\tnope\n", 3},
		} {
			t.Run(mode+", "+name, func(t *testing.T) {
				d := newInboundDoctor(t)
				d.user("inbound_auth=" + mode + "\ninbound_allow=mac\n")
				d.key("local")
				d.key("mac")
				d.protect()
				path := filepath.Join(d.conf, peerauth.SeenFile)
				d.write(path, tc.body, 0o600)
				want := fmt.Sprintf("peer replay cache %s is corrupt (line %d); messages are refused until it is fixed: delete it", path, tc.line)
				if got := cacheWarning(d.out()); got != want {
					t.Fatalf("warning = %q\nwant      %q", got, want)
				}
				if raw, err := os.ReadFile(path); err != nil || string(raw) != tc.body {
					t.Fatalf("the doctor changed the cache: %q (%v)", raw, err)
				}
			})
		}
	}
	t.Run("a good cache and an off policy say nothing", func(t *testing.T) {
		d := newInboundDoctor(t)
		d.user("inbound_auth=enforce\ninbound_allow=mac\n")
		d.key("local")
		d.key("mac")
		d.protect()
		d.write(filepath.Join(d.conf, peerauth.SeenFile), good, 0o600)
		if got := cacheWarning(d.out()); got != "" {
			t.Fatalf("warning for a good cache: %q", got)
		}
		d.user("inbound_auth=off\n")
		d.write(filepath.Join(d.conf, peerauth.SeenFile), "oops\n", 0o600)
		if got := cacheWarning(d.out()); got != "" {
			t.Fatalf("warning with inbound_auth=off: %q", got)
		}
	})
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
