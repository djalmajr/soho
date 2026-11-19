package core

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func panelExit(t *testing.T, fn func()) (int, string) {
	t.Helper()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		fn()
	}()
	if recovered == nil {
		return 0, ""
	}
	exit, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("unexpected panic: %#v", recovered)
	}
	return exit.Code, exit.Msg
}

func panelDigest(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func panelTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, path)
			out[rel] = panelDigest(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func TestPanelConfigSetDryRunPrintsThePlanAndWritesNothing(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "# build lane\nlane.build.roles=designer,tasker\nlane.build.effort=high\nrole.reviewer.effort=medium\n")
	ctx := LoadConfig(env, repo)
	before := panelTree(t, filepath.Dir(filepath.Dir(file)))

	text, _ := capture(t, func() {
		CmdConfigSet([]string{"lane.build.effort", "xhigh", "--dry-run"}, &ctx, env, repo)
	})
	// The no-write hash is checked before the plan's text: a mutant that
	// performs the real write must fail on the changed file.
	for path, digest := range before {
		if after := panelDigest(t, filepath.Join(filepath.Dir(filepath.Dir(file)), path)); digest != after {
			t.Fatalf("dry-run changed %s", path)
		}
	}
	if after := panelTree(t, filepath.Dir(filepath.Dir(file))); len(before) != len(after) {
		t.Fatalf("dry-run changed the tree: before %v, after %v", before, after)
	}
	want := "would set lane.build.effort=xhigh in " + file + "\n"
	if text != want {
		t.Fatalf("dry-run text = %q, want %q", text, want)
	}

	out, _ := capture(t, func() {
		CmdConfigSet([]string{"role.reviewer.effort", "max", "--dry-run", "--json"}, &ctx, env, repo)
	})
	obj := panelParseObject(t, out)
	if keys := obj.Keys(); strings.Join(keys, ",") != "schema,key,value,scope,file,command,valid,previous,applies_to_roles" {
		t.Fatalf("dry-run JSON key order = %v", keys)
	}
	fields := map[string]any{"schema": float64(1), "key": "role.reviewer.effort", "value": "max", "scope": "project", "file": file,
		"command": "soho config set role.reviewer.effort max --project", "valid": true}
	for key, wantValue := range fields {
		if got, _ := obj.Get(key); got != wantValue {
			t.Fatalf("dry-run JSON %s = %#v, want %#v", key, got, wantValue)
		}
	}
	prev := panelObjectField(t, objValue(obj, "previous"), "previous")
	if got, _ := prev.Get("in_file"); got != "medium" {
		t.Fatalf("previous.in_file = %v, want medium", got)
	}
	if got, _ := prev.Get("effective"); got != "medium" {
		t.Fatalf("previous.effective = %v, want medium", got)
	}
	if got, _ := prev.Get("effective_source"); got != "project" {
		t.Fatalf("previous.effective_source = %v, want project", got)
	}
	if got := panelStringArray(t, objValue(obj, "applies_to_roles"), "applies_to_roles"); len(got) != 1 || got[0] != "reviewer" {
		t.Fatalf("applies_to_roles = %v, want [reviewer]", got)
	}
	if after := panelTree(t, filepath.Dir(filepath.Dir(file))); len(before) != len(after) {
		t.Fatalf("dry-run --json changed the tree: before %v, after %v", before, after)
	}
}

func TestPanelConfigSetDryRunLaneAppliesToRolesAndUserScope(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "lane.build.roles=designer,tasker\nlane.build.effort=high\n")
	ctx := LoadConfig(env, repo)
	out, _ := capture(t, func() {
		CmdConfigSet([]string{"lane.build.effort", "xhigh", "--user", "--dry-run", "--json"}, &ctx, env, repo)
	})
	obj := panelParseObject(t, out)
	if scope, _ := obj.Get("scope"); scope != "user" {
		t.Fatalf("scope = %v, want user", scope)
	}
	if got, _ := obj.Get("file"); got != ConfigFileFor("user", env, repo) {
		t.Fatalf("file = %v, want %v", got, ConfigFileFor("user", env, repo))
	}
	if command, _ := obj.Get("command"); command != "soho config set lane.build.effort xhigh --user" {
		t.Fatalf("command = %v", command)
	}
	if got := panelStringArray(t, objValue(obj, "applies_to_roles"), "applies_to_roles"); len(got) != 2 || got[0] != "designer" || got[1] != "tasker" {
		t.Fatalf("applies_to_roles = %v, want [designer tasker]", got)
	}
	prev := panelObjectField(t, objValue(obj, "previous"), "previous")
	if got, _ := prev.Get("in_file"); got != nil {
		t.Fatalf("previous.in_file = %v in the user file, want null", got)
	}

	out, _ = capture(t, func() {
		CmdConfigSet([]string{"panes", "4", "--dry-run", "--json"}, &ctx, env, repo)
	})
	obj = panelParseObject(t, out)
	if got := panelStringArray(t, objValue(obj, "applies_to_roles"), "applies_to_roles"); len(got) != 0 {
		t.Fatalf("applies_to_roles for a scalar = %v, want []", got)
	}
}

func TestPanelConfigSetDryRunInvalidMatchesSetMessage(t *testing.T) {
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	cases := []struct {
		name, argv, message string
	}{
		{"unknown key", "lane.build.nope 2", "config set: unknown key 'lane.build.nope'"},
		{"bad lane effort", "lane.build.effort turbo", "config set: invalid value 'turbo' for lane.build.effort"},
		{"bad role effort", "role.reviewer.effort turbo", "config set: invalid value 'turbo' for role.reviewer.effort"},
		{"empty value", "lane.build.effort ''", "config set: empty value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := strings.Fields(tc.argv)
			for i, arg := range argv {
				if arg == "''" {
					argv[i] = ""
				}
			}
			realCode, realMessage := panelExit(t, func() { CmdConfigSet(argv, &ctx, env, repo) })
			dryArgv := append(append([]string{}, argv...), "--dry-run", "--json")
			out, _ := capture(t, func() {
				dryCode, dryMessage := panelExit(t, func() { CmdConfigSet(dryArgv, &ctx, env, repo) })
				if dryCode != 2 || dryMessage != realMessage {
					t.Fatalf("dry-run error = (%d, %q), set = (%d, %q)", dryCode, dryMessage, realCode, realMessage)
				}
			})
			if realCode != 2 || realMessage != tc.message {
				t.Fatalf("set error = (%d, %q), want (2, %q)", realCode, realMessage, tc.message)
			}
			obj := panelParseObject(t, out)
			for key, want := range map[string]any{"schema": float64(1), "value": argv[1], "valid": false, "error": tc.message} {
				if got, _ := obj.Get(key); got != want {
					t.Fatalf("invalid JSON %s = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

func TestPanelConfigValueRoleEffortUsesTheLadder(t *testing.T) {
	env, repo := fixture(t)
	for _, effort := range EffortLadder {
		if !ConfigValueOk("role.reviewer.effort", effort, env, repo) {
			t.Fatalf("role.reviewer.effort=%q should be valid", effort)
		}
	}
	for _, value := range []string{"turbo", "", "HIGH", "high extra"} {
		if ConfigValueOk("role.reviewer.effort", value, env, repo) {
			t.Fatalf("role.reviewer.effort=%q should be invalid", value)
		}
	}
}

func TestPanelConfigUnsetKeepsEveryOtherLine(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	const original = "# lane settings\n\nlane.build.roles=designer,tasker  # trailing\nlane.build.effort=high\nrole.review.effort = medium\n# keep this comment\npanes=4\n"
	write(t, file, original)
	const want = "# lane settings\n\nlane.build.roles=designer,tasker  # trailing\nrole.review.effort = medium\n# keep this comment\npanes=4\n"

	out, _ := capture(t, func() { CmdConfigUnset([]string{"lane.build.effort"}, env, repo) })
	if wantOut := "unset lane.build.effort in " + file + "\n"; out != wantOut {
		t.Fatalf("unset output = %q, want %q", out, wantOut)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != want {
		t.Fatalf("unset file = %q err=%v, want %q", got, err, want)
	}

	// A key that only shares a prefix is never the target.
	out, _ = capture(t, func() { CmdConfigUnset([]string{"role.review.effort"}, env, repo) })
	if !strings.HasPrefix(out, "unset role.review.effort in ") {
		t.Fatalf("unset output = %q", out)
	}
	got, _ = os.ReadFile(file)
	if !strings.Contains(string(got), "panes=4\n") || strings.Contains(string(got), "role.review.effort") {
		t.Fatalf("prefix-key unset damaged the file: %q", got)
	}
}

func TestPanelConfigUnsetAbsentAndUnknown(t *testing.T) {
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "panes=4\n")
	out, _ := capture(t, func() { CmdConfigUnset([]string{"lane.build.effort"}, env, repo) })
	if want := "not set in " + file + "\n"; out != want {
		t.Fatalf("absent output = %q, want %q", out, want)
	}
	if got, _ := os.ReadFile(file); string(got) != "panes=4\n" {
		t.Fatalf("absent unset rewrote the file: %q", got)
	}
	code, message := panelExit(t, func() { CmdConfigUnset([]string{"lane.build.nope"}, env, repo) })
	if code != 2 || message != "config unset: unknown key 'lane.build.nope'" {
		t.Fatalf("unknown key = (%d, %q)", code, message)
	}
	if !ConfigKeyOk("role.reviewer.effort") {
		t.Fatal("role reviewer effort key should still be known")
	}
	code, message = panelExit(t, func() { CmdConfigSet([]string{"panes", "4", "--json"}, &ctx, env, repo) })
	if code != 2 || message != "config set: unknown option '--json'" {
		t.Fatalf("set --json without --dry-run = (%d, %q)", code, message)
	}
}

func TestPanelConfigUnsetUsesAtomicWriter(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "lane.build.effort=high\npanes=4\n")
	real := writeConfigAtomic
	calls := 0
	writeConfigAtomic = func(dest, content string) error {
		calls++
		if dest != file || strings.Contains(content, "lane.build.effort") {
			t.Fatalf("atomic write got (%s, %q)", dest, content)
		}
		return real(dest, content)
	}
	t.Cleanup(func() { writeConfigAtomic = real })
	_, _ = capture(t, func() { CmdConfigUnset([]string{"lane.build.effort"}, env, repo) })
	if calls != 1 {
		t.Fatalf("atomic writer called %d times, want 1", calls)
	}
}

func TestPanelConfigDryRunMissingProjectFileWritesNothing(t *testing.T) {
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	file := filepath.Join(repo, ".agents", "soho.conf")
	out, _ := capture(t, func() {
		CmdConfigSet([]string{"lane.build.effort", "xhigh", "--dry-run", "--json"}, &ctx, env, repo)
	})
	obj := panelParseObject(t, out)
	if got, _ := obj.Get("file"); got != file {
		t.Fatalf("missing file = %v, want %v", got, file)
	}
	prev := panelObjectField(t, objValue(obj, "previous"), "previous")
	if got, _ := prev.Get("in_file"); got != nil {
		t.Fatalf("previous.in_file = %v, want null", got)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("dry-run created %s (err=%v)", file, err)
	}
}

func TestPanelConfigUnsetHostileKeysAndPrefixKey(t *testing.T) {
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	for _, key := range []string{"lane.build.effort xhigh", "lane.build.effort=1", "lane.build.effort\npanes=4", "role.reviewer.effort;rm"} {
		code, message := panelExit(t, func() { CmdConfigUnset([]string{key}, env, repo) })
		setCode, _ := panelExit(t, func() { CmdConfigSet([]string{key, "xhigh", "--dry-run"}, &ctx, env, repo) })
		if code != 2 || setCode != 2 || message != "config unset: unknown key '"+key+"'" {
			t.Fatalf("hostile key %q: unset=(%d,%q) set=%d", key, code, message, setCode)
		}
	}
	file := filepath.Join(repo, ".agents", "soho.conf")
	const original = "role.review.effort=medium\nrole.reviewer.effort=high\n# tail\n"
	write(t, file, original)
	_, _ = capture(t, func() { CmdConfigUnset([]string{"role.review.effort"}, env, repo) })
	got, _ := os.ReadFile(file)
	if string(got) != "role.reviewer.effort=high\n# tail\n" {
		t.Fatalf("prefix-key unset = %q, want only role.review.effort removed", got)
	}
	for _, value := range []string{"high\nxhigh", "high\t", "high#comment", "xhigh=1"} {
		dryCode, dryMessage := panelExit(t, func() { CmdConfigSet([]string{"lane.build.effort", value, "--dry-run"}, &ctx, env, repo) })
		setCode, setMessage := panelExit(t, func() { CmdConfigSet([]string{"lane.build.effort", value}, &ctx, env, repo) })
		if dryCode != setCode || dryMessage != setMessage || dryCode != 2 {
			t.Fatalf("hostile value %q: dry=(%d,%q) set=(%d,%q)", value, dryCode, dryMessage, setCode, setMessage)
		}
	}
}
