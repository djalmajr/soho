package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// skillWriteFixture builds a non-git tree whose skill dir is a plain
// directory (not a git repository): with cwd inside the skill the project
// root is the skill itself and the state root is <skill>/.soho. PATH
// holds only the fake herdr (no git, no real herdr), so root resolution
// never leaves the fixture and any herdr call is recorded in the call log.
type skillWriteFixture struct {
	root, skill, bin, xdg string
	env                   platform.Env
	callsFile             string
}

func newSkillWriteFixture(t *testing.T) *skillWriteFixture {
	t.Helper()
	root := t.TempDir()
	// runIn passes cwd through os.Getwd, which resolves the /tmp symlink on
	// macOS; resolve it here so the fixture paths match the command's.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	skill := filepath.Join(root, "skill")
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	xdg := filepath.Join(root, "xdg")
	for _, d := range []string{skill, home, bin, xdg} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# herdr-soho\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": xdg,
		"PATH": bin, "SOHO_FAKECLI_CONFIG": bin,
		"HERDR_WORKSPACE_ID": "ws",
		"SOHO_SKILL_DIR":     skill,
	}
	return &skillWriteFixture{
		root: root, skill: skill, bin: bin, xdg: xdg, env: env,
		callsFile: filepath.Join(bin, "herdr.calls.jsonl"),
	}
}

func (f *skillWriteFixture) skillEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.skill)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func (f *skillWriteFixture) skillUntouched(t *testing.T, before []string) {
	t.Helper()
	after := f.skillEntries(t)
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("skill dir changed: before=%v after=%v", before, after)
	}
}

func (f *skillWriteFixture) herdrCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(f.callsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read herdr call log: %v", err)
	}
	return calls
}

func (f *skillWriteFixture) noHerdrCalls(t *testing.T) {
	t.Helper()
	if calls := f.herdrCalls(t); len(calls) != 0 {
		argvs := make([]string, 0, len(calls))
		for _, c := range calls {
			argvs = append(argvs, strings.Join(c.Argv, " "))
		}
		t.Fatalf("herdr was called: %v", argvs)
	}
}

func (f *skillWriteFixture) refusal(t *testing.T, command, path string) string {
	t.Helper()
	return "soho: " + command + ": the state dir '" + path + "' would be inside the soho skill ('" + f.skill + "'); run soho from the project's directory (nothing was written)\n"
}

// stateRefusal runs the command from inside the skill and asserts the exit
// 2 refusal: the exact phrase, nothing created in the skill, no herdr call.
func (f *skillWriteFixture) stateRefusal(t *testing.T, args []string, command, path string) int {
	t.Helper()
	before := f.skillEntries(t)
	code, out, errOut := runIn(t, args, f.env, f.skill)
	want := f.refusal(t, command, path)
	if code != 2 || out != "" || errOut != want {
		t.Fatalf("code=%d want=2 stdout=%q stderr=%q want=%q", code, out, errOut, want)
	}
	f.skillUntouched(t, before)
	f.noHerdrCalls(t)
	return code
}

func TestSessionSetRefusesInsideTheSkill(t *testing.T) {
	f := newSkillWriteFixture(t)
	f.stateRefusal(t, []string{"session", "set", "lane.build.kind", "grok"}, "session set", filepath.Join(f.skill, ".soho"))

	// Control: from outside the skill the command still writes.
	proj := filepath.Join(f.root, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runIn(t, []string{"session", "set", "lane.build.kind", "grok"}, f.env, proj)
	if code != 0 || errOut != "" || !strings.Contains(out, "set lane.build.kind=grok in ") {
		t.Fatalf("control code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	sessionFile := filepath.Join(proj, ".soho", "ws", "session.conf")
	data, err := os.ReadFile(sessionFile)
	if err != nil || string(data) != "lane.build.kind=grok\n" {
		t.Fatalf("control session.conf=%q err=%v", data, err)
	}
}

func TestSessionClearRefusesInsideTheSkill(t *testing.T) {
	f := newSkillWriteFixture(t)
	// A session file that already sits inside the skill: the refusal must
	// come before it is read or removed.
	seeded := filepath.Join(f.skill, ".soho", "ws", "session.conf")
	if err := os.MkdirAll(filepath.Dir(seeded), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seeded, []byte("lane.build.kind=grok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.stateRefusal(t, []string{"session", "clear", "lane.build.kind"}, "session clear", filepath.Join(f.skill, ".soho"))
	data, err := os.ReadFile(seeded)
	if err != nil || string(data) != "lane.build.kind=grok\n" {
		t.Fatalf("session file touched before the refusal: %q err=%v", data, err)
	}

	// Control: from outside the skill the clear still works.
	proj := filepath.Join(f.root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".soho", "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	projFile := filepath.Join(proj, ".soho", "ws", "session.conf")
	if err := os.WriteFile(projFile, []byte("lane.build.kind=grok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runIn(t, []string{"session", "clear"}, f.env, proj)
	if code != 0 || errOut != "" || !strings.Contains(out, "session cleared: "+projFile) {
		t.Fatalf("control code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if _, err := os.Stat(projFile); !os.IsNotExist(err) {
		t.Fatalf("control session file not cleared: %v", err)
	}
}

func TestConfigSetProjectRefusesInsideTheSkill(t *testing.T) {
	f := newSkillWriteFixture(t)
	f.stateRefusal(t, []string{"config", "set", "max_workers", "5"}, "config set", f.skill)

	// Control: from outside the skill the project file is still written.
	proj := filepath.Join(f.root, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runIn(t, []string{"config", "set", "max_workers", "5"}, f.env, proj)
	if code != 0 || errOut != "" || !strings.Contains(out, "set max_workers=5 in ") {
		t.Fatalf("control code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	projFile := filepath.Join(proj, ".agents", "soho.conf")
	data, err := os.ReadFile(projFile)
	if err != nil || string(data) != "max_workers=5\n" {
		t.Fatalf("control project config=%q err=%v", data, err)
	}
}

func TestConfigSetUserStillWorksInsideTheSkill(t *testing.T) {
	f := newSkillWriteFixture(t)
	before := f.skillEntries(t)
	code, out, errOut := runIn(t, []string{"config", "set", "--user", "max_workers", "5"}, f.env, f.skill)
	userFile := filepath.Join(f.xdg, "soho", "config")
	if code != 0 || errOut != "" || !strings.Contains(out, "set max_workers=5 in "+userFile) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	data, err := os.ReadFile(userFile)
	if err != nil || string(data) != "max_workers=5\n" {
		t.Fatalf("user config=%q err=%v", data, err)
	}
	f.skillUntouched(t, before)
	f.noHerdrCalls(t)
}

func TestSetupRefusesInsideTheSkill(t *testing.T) {
	f := newSkillWriteFixture(t)
	f.stateRefusal(t, []string{"setup"}, "setup", f.skill)

	// Control: from outside the skill the block is still written.
	proj := filepath.Join(f.root, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runIn(t, []string{"setup"}, f.env, proj)
	if code != 0 {
		t.Fatalf("control code=%d stderr=%q", code, errOut)
	}
	agents := filepath.Join(proj, "AGENTS.md")
	data, err := os.ReadFile(agents)
	if err != nil || !strings.Contains(string(data), "<!-- soho:start -->") {
		t.Fatalf("control AGENTS.md=%q err=%v", data, err)
	}
}

func TestDoctorFixRefusesInsideTheSkill(t *testing.T) {
	f := newSkillWriteFixture(t)
	f.stateRefusal(t, []string{"doctor", "--fix", "--panes", "3"}, "doctor --fix", f.skill)

	// Control: from outside the skill the preset is still written.
	proj := filepath.Join(f.root, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runIn(t, []string{"doctor", "--fix", "--panes", "3"}, f.env, proj)
	if code != 0 {
		t.Fatalf("control code=%d stderr=%q", code, errOut)
	}
	projFile := filepath.Join(proj, ".agents", "soho.conf")
	data, err := os.ReadFile(projFile)
	if err != nil || !strings.Contains(string(data), "panes=3") {
		t.Fatalf("control project config=%q err=%v", data, err)
	}
}

func TestSessionSetWithAbsoluteStateDirStillWorksInsideTheSkill(t *testing.T) {
	f := newSkillWriteFixture(t)
	before := f.skillEntries(t)
	state := filepath.Join(f.root, "state")
	env := f.env.Clone()
	env["SOHO_DIR"] = state
	code, out, errOut := runIn(t, []string{"session", "set", "lane.build.kind", "grok"}, env, f.skill)
	if code != 0 || errOut != "" || !strings.Contains(out, "set lane.build.kind=grok in ") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	data, err := os.ReadFile(filepath.Join(state, "ws", "session.conf"))
	if err != nil || string(data) != "lane.build.kind=grok\n" {
		t.Fatalf("session.conf=%q err=%v", data, err)
	}
	f.skillUntouched(t, before)
	f.noHerdrCalls(t)
}

func TestSkillAsGitCheckoutRefusesSessionSetWithoutGitignore(t *testing.T) {
	// The skill as a git checkout: root resolution reaches the skill repo,
	// so StateRoot would append <skill>/.gitignore. The refusal must come
	// first and leave the .gitignore alone.
	f := newSkillWriteFixture(t)
	init := exec.Command("git", "init", "-q")
	init.Dir = f.skill
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
	env := f.env.Clone()
	env["PATH"] = f.bin + string(os.PathListSeparator) + testutil.HostPath() // git for root resolution, fake herdr first
	code, out, errOut := runIn(t, []string{"session", "set", "lane.build.kind", "grok"}, env, f.skill)
	want := f.refusal(t, "session set", filepath.Join(f.skill, ".soho"))
	if code != 2 || out != "" || errOut != want {
		t.Fatalf("code=%d stdout=%q stderr=%q want=%q", code, out, errOut, want)
	}
	if _, err := os.Stat(filepath.Join(f.skill, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore created inside the skill checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.skill, ".soho")); !os.IsNotExist(err) {
		t.Fatalf("state dir created inside the skill checkout: %v", err)
	}
	f.noHerdrCalls(t)
}
