package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	configBefore := map[string]string{
		"project": panelFileDigestOrAbsent(t, file),
		"user":    panelFileDigestOrAbsent(t, ConfigFileFor("user", env, repo)),
	}
	out, _ := capture(t, func() {
		CmdConfigSet([]string{"role.reviewer.effort", "max", "--dry-run", "--json"}, &ctx, env, repo)
	})
	obj := panelParseObject(t, out)
	if keys := obj.Keys(); strings.Join(keys, ",") != "schema,key,value,scope,file,command,valid,previous,applies_to_roles" {
		t.Fatalf("dry-run JSON key order = %v", keys)
	}
	fields := map[string]any{"schema": float64(1), "key": "role.reviewer.effort", "value": "max", "scope": "project", "file": file,
		"command": "soho config set role.reviewer.effort=max --project", "valid": true}
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
	// F13: the JSON branch is compared as a full digest of the config files,
	// not only as a file count, so an in-place rewrite cannot pass.
	for name, path := range map[string]string{"project": file, "user": ConfigFileFor("user", env, repo)} {
		if got := panelFileDigestOrAbsent(t, path); got != configBefore[name] {
			t.Fatalf("dry-run --json changed the %s config: %s -> %s", name, configBefore[name], got)
		}
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
	if command, _ := obj.Get("command"); command != "soho config set lane.build.effort=xhigh --user" {
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

func TestPanelConfigScopeFlagIsRefusedInsteadOfLastWins(t *testing.T) {
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	project := filepath.Join(repo, ".agents", "soho.conf")
	user := ConfigFileFor("user", env, repo)
	write(t, project, "role.tester.model=project-value\n")
	write(t, user, "role.tester.model=user-value\n")
	beforeProject, beforeUser := panelDigest(t, project), panelDigest(t, user)
	const want = "config set: only one scope option (--project or --user) may be given"
	for _, argv := range [][]string{
		{"role.tester.model=changed", "--user", "--project"},
		{"--project", "role.tester.model=changed", "--project"},
		{"--user", "role.tester.model=changed", "--user"},
	} {
		code, message := panelExit(t, func() { CmdConfigSet(argv, &ctx, env, repo) })
		if code != 2 || message != want {
			t.Fatalf("set %v = (%d, %q), want exit 2 with the scope refusal", argv, code, message)
		}
	}
	out, _ := capture(t, func() {
		code, message := panelExit(t, func() {
			CmdConfigSet([]string{"role.tester.model=changed", "--user", "--project", "--dry-run", "--json"}, &ctx, env, repo)
		})
		if code != 2 || message != want {
			t.Fatalf("preview = (%d, %q)", code, message)
		}
	})
	obj := panelParseObject(t, out)
	if valid, _ := obj.Get("valid"); valid != false {
		t.Fatalf("preview valid = %v, want false", valid)
	}
	if got, _ := obj.Get("error"); got != want {
		t.Fatalf("preview error = %v, want %q", got, want)
	}
	for _, argv := range [][]string{
		{"role.tester.model", "--user", "--project"},
		{"role.tester.model", "--project", "--project"},
	} {
		code, message := panelExit(t, func() { CmdConfigUnset(argv, env, repo) })
		if wantUnset := "config unset: only one scope option (--project or --user) may be given"; code != 2 || message != wantUnset {
			t.Fatalf("unset %v = (%d, %q), want exit 2 with the scope refusal", argv, code, message)
		}
	}
	if got := panelDigest(t, project); got != beforeProject {
		t.Fatalf("the scope conflict rewrote the project config")
	}
	if got := panelDigest(t, user); got != beforeUser {
		t.Fatalf("the scope conflict rewrote the user config")
	}
}

func TestPanelConfigPreviewCommandReplaysThroughTheShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the replay shell is /bin/sh")
	}
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	project := filepath.Join(repo, ".agents", "soho.conf")
	bin := t.TempDir()
	script := filepath.Join(bin, "soho")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value  string
		quoted bool
	}{
		{"-x", true},
		{"two words", true},
		{"a$HOME", true},
		{"--json", true},
		{"plain.value:_1", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			write(t, project, "role.tester.model=sentinel\n")
			out, _ := capture(t, func() {
				CmdConfigSet([]string{"role.tester.model=" + tc.value, "--project", "--dry-run", "--json"}, &ctx, env, repo)
			})
			command, _ := panelParseObject(t, out).Get("command")
			text, ok := command.(string)
			if !ok {
				t.Fatalf("command = %#v", command)
			}
			token := "role.tester.model=" + tc.value
			if tc.quoted != strings.Contains(text, "'"+token+"'") {
				t.Fatalf("command %q quoting for %q = %v, want %v", text, tc.value, !tc.quoted, tc.quoted)
			}
			cmd := exec.Command("/bin/sh", "-c", text)
			cmd.Env = []string{"PATH=" + bin}
			replayed, err := cmd.Output()
			if err != nil {
				t.Fatalf("/bin/sh -c %q: %v", text, err)
			}
			args := strings.Split(strings.TrimSuffix(string(replayed), "\n"), "\n")
			if len(args) != 4 || args[0] != "config" || args[1] != "set" || args[2] != token || args[3] != "--project" {
				t.Fatalf("/bin/sh -c %q tokenized to %q, want one key=value token and --project", text, args)
			}
			_, _ = capture(t, func() { CmdConfigSet(args[2:], &ctx, env, repo) })
			if got := FileKeyValue(project, "role.tester.model"); got != tc.value {
				t.Fatalf("replayed value = %q, want %q", got, tc.value)
			}
		})
	}
}

func TestPanelConfigPreviewPreservesEmptyAssignmentsAndTheListingProjection(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "role.tester.model=\nlane.build.roles=tester\n")
	ctx := LoadConfig(env, repo)
	out, _ := capture(t, func() {
		CmdConfigSet([]string{"role.tester.model=preview", "--project", "--dry-run", "--json"}, &ctx, env, repo)
	})
	prev := panelObjectField(t, objValue(panelParseObject(t, out), "previous"), "previous")
	if got, ok := prev.Get("in_file"); !ok || got != "" {
		t.Fatalf("present empty in_file = %#v (present=%v), want \"\"", got, ok)
	}
	out, _ = capture(t, func() {
		CmdConfigSet([]string{"role.tester.effort=high", "--project", "--dry-run", "--json"}, &ctx, env, repo)
	})
	prev = panelObjectField(t, objValue(panelParseObject(t, out), "previous"), "previous")
	if got, _ := prev.Get("in_file"); got != nil {
		t.Fatalf("absent in_file = %#v, want null", got)
	}

	listing, _ := capture(t, func() { CmdConfig(&ctx, env, repo) })
	wantValue, wantSource := "", ""
	for _, line := range strings.Split(listing, "\n") {
		if fields := strings.Fields(line); len(fields) == 3 && fields[0] == "max_workers" {
			wantValue, wantSource = fields[1], fields[2]
		}
	}
	if wantValue == "" || wantSource != "lanes" {
		t.Fatalf("listing max_workers row = (%q, %q), want the lane-derived value", wantValue, wantSource)
	}
	out, _ = capture(t, func() {
		CmdConfigSet([]string{"max_workers=3", "--project", "--dry-run", "--json"}, &ctx, env, repo)
	})
	prev = panelObjectField(t, objValue(panelParseObject(t, out), "previous"), "previous")
	if got, _ := prev.Get("effective"); got != wantValue {
		t.Fatalf("previous.effective = %#v, want the listing's %q", got, wantValue)
	}
	if got, _ := prev.Get("effective_source"); got != wantSource {
		t.Fatalf("previous.effective_source = %#v, want the listing's %q", got, wantSource)
	}
}

func TestPanelConfigUnsetKeepsEveryNonTargetByte(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, before, after string }{
		{"crlf and an unterminated last line", "# comment\r\nrole.tester.model=drop\r\n\r\nrole.tester.effort=high", "# comment\r\n\r\nrole.tester.effort=high"},
		{"a later crlf target", "panes=4\r\nrole.tester.model=drop\r\n# tail\r\n", "panes=4\r\n# tail\r\n"},
		{"lone lf endings", "# c\nrole.tester.model=drop\nrole.tester.effort=high\n", "# c\nrole.tester.effort=high\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(tc.before), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _ = capture(t, func() { CmdConfigUnset([]string{"role.tester.model", "--project"}, env, repo) })
			got, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(got, []byte(tc.after)) {
				t.Fatalf("unset bytes = %q err=%v, want %q", got, err, tc.after)
			}
		})
	}
}

func TestPanelConfigSetAndUnsetSerializeOnTheDestination(t *testing.T) {
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	file := filepath.Join(repo, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("# retained padding line for the read-modify-write window\n", 60000)
	oldOut := platform.Stdout
	platform.Stdout = io.Discard
	t.Cleanup(func() { platform.Stdout = oldOut })
	run := func(fn func()) (code int, message string) {
		defer func() {
			if recovered := recover(); recovered != nil {
				exit, ok := recovered.(*platform.ExitError)
				if !ok {
					panic(recovered)
				}
				code, message = exit.Code, exit.Msg
			}
		}()
		fn()
		return 0, ""
	}
	for round := 0; round < 16; round++ {
		setKey, setValue, goneKey := "role.tester.model", "", "role.tester.effort"
		if round%2 == 1 {
			setKey, goneKey = "role.tester.effort", "role.tester.model"
		}
		setValue = "round-" + string(rune('a'+round))
		if setKey == "role.tester.effort" {
			setValue = "high"
			if round%4 == 3 {
				setValue = "low"
			}
		}
		if err := os.WriteFile(file, []byte("role.tester.model=old\nrole.tester.effort=old\n"+pad), 0o600); err != nil {
			t.Fatal(err)
		}
		setArgs, unsetArgs := []string{setKey + "=" + setValue, "--project"}, []string{goneKey, "--project"}
		start := make(chan struct{})
		results := make(chan [2]any, 2)
		go func() {
			<-start
			code, message := run(func() { CmdConfigSet(setArgs, &ctx, env, repo) })
			results <- [2]any{code, message}
		}()
		go func() {
			<-start
			code, message := run(func() { CmdConfigUnset(unsetArgs, env, repo) })
			results <- [2]any{code, message}
		}()
		close(start)
		for i := 0; i < 2; i++ {
			if result := <-results; result[0] != 0 {
				t.Fatalf("round %d: %v failed: (%v, %v)", round, []any{setArgs, unsetArgs}, result[0], result[1])
			}
		}
		pairs, err := ReadConfigFilePairs(file)
		if err != nil {
			t.Fatalf("round %d: read back: %v", round, err)
		}
		got := map[string]string{}
		for _, pair := range pairs {
			got[pair.Key] = pair.Value
		}
		if got[NormalizeKey(setKey)] != setValue {
			t.Fatalf("round %d: the set was lost: %v = %q", round, setKey, got[NormalizeKey(setKey)])
		}
		if _, present := got[NormalizeKey(goneKey)]; present {
			t.Fatalf("round %d: the unset was lost: %s is still assigned", round, goneKey)
		}
	}
}

func TestPanelConfigUnsetRefusesTheProjectDestinationInsideTheSkill(t *testing.T) {
	env, repo := fixture(t)
	skill := filepath.Join(repo, "scratch-skill")
	if err := os.MkdirAll(filepath.Join(skill, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = skill
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	file := filepath.Join(skill, ".agents", "soho.conf")
	write(t, file, "role.tester.model=fixture\n")
	envSkill := env.Clone()
	envSkill["SOHO_SKILL_DIR"] = skill
	code, message := panelExit(t, func() { CmdConfigUnset([]string{"role.tester.model", "--project"}, envSkill, skill) })
	if code != 2 || !strings.HasPrefix(message, "config unset: the state dir") || !strings.Contains(message, "(nothing was written)") {
		t.Fatalf("project unset inside the skill = (%d, %q)", code, message)
	}
	if got, _ := os.ReadFile(file); string(got) != "role.tester.model=fixture\n" {
		t.Fatalf("project unset inside the skill wrote the file: %q", got)
	}
	// The user destination stays valid from inside the skill.
	userFile := ConfigFileFor("user", envSkill, skill)
	write(t, userFile, "role.tester.model=user-fixture\n")
	out, _ := capture(t, func() { CmdConfigUnset([]string{"role.tester.model", "--user"}, envSkill, skill) })
	if want := "unset role.tester.model in " + userFile + "\n"; out != want {
		t.Fatalf("user unset inside the skill = %q, want %q", out, want)
	}
}

func TestPanelConfigUnsetReportsReadFailures(t *testing.T) {
	env, repo := fixture(t)
	userFile := ConfigFileFor("user", env, repo)
	if _, err := os.Stat(userFile); !os.IsNotExist(err) {
		t.Fatalf("the fixture's user config should be absent")
	}
	out, _ := capture(t, func() {
		if code, message := panelExit(t, func() { CmdConfigUnset([]string{"role.tester.model", "--user"}, env, repo) }); code != 0 {
			t.Fatalf("missing file = (%d, %q)", code, message)
		}
	})
	if want := "not set in " + userFile + "\n"; out != want {
		t.Fatalf("missing file output = %q, want %q", out, want)
	}

	if err := os.MkdirAll(userFile, 0o700); err != nil {
		t.Fatal(err)
	}
	code, message := panelExit(t, func() { CmdConfigUnset([]string{"role.tester.model", "--user"}, env, repo) })
	if code != 4 || !strings.Contains(message, userFile) || !strings.Contains(message, "(file left untouched)") {
		t.Fatalf("directory destination = (%d, %q), want exit 4 (file left untouched)", code, message)
	}
	if err := os.Remove(userFile); err != nil {
		t.Fatal(err)
	}

	write(t, userFile, "role.tester.model=kept\n")
	if err := os.Chmod(userFile, 0o000); err != nil {
		t.Skipf("cannot make %s unreadable: %v", userFile, err)
	}
	t.Cleanup(func() { _ = os.Chmod(userFile, 0o600) })
	if _, err := os.ReadFile(userFile); err == nil {
		t.Skip("the process can still read a 0000 file (root?), so the permission case cannot fire")
	}
	code, message = panelExit(t, func() { CmdConfigUnset([]string{"role.tester.model", "--user"}, env, repo) })
	if code != 4 || !strings.Contains(message, "(file left untouched)") {
		t.Fatalf("unreadable destination = (%d, %q), want exit 4 (file left untouched)", code, message)
	}
}

func TestPanelConfigSetReservedTokensKeepTheBaseGrammar(t *testing.T) {
	env, repo := fixture(t)
	ctx := LoadConfig(env, repo)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "role.tester.model=sentinel\n")
	// One token after the key is the value, even when it is a preview flag.
	out, _ := capture(t, func() { CmdConfigSet([]string{"role.tester.model", "--dry-run"}, &ctx, env, repo) })
	if want := "set role.tester.model=--dry-run in " + file + "\n"; out != want {
		t.Fatalf("lone --dry-run token = %q, want a real set (%q)", out, want)
	}
	if got := FileKeyValue(file, "role.tester.model"); got != "--dry-run" {
		t.Fatalf("saved value = %q, want --dry-run", got)
	}
	// The inline assignment is the explicit preview escape for a reserved value.
	out, _ = capture(t, func() {
		CmdConfigSet([]string{"role.tester.model=--dry-run", "--dry-run", "--json"}, &ctx, env, repo)
	})
	preview := panelParseObject(t, out)
	if value, _ := preview.Get("value"); value != "--dry-run" {
		t.Fatalf("inline preview value = %v", value)
	}
	if got := FileKeyValue(file, "role.tester.model"); got != "--dry-run" {
		t.Fatalf("the inline preview wrote the file: %q", got)
	}
	// --json without --dry-run stays an unknown option: exit 2, stderr only.
	write(t, file, "panes=4\n")
	out, _ = capture(t, func() {
		code, message := panelExit(t, func() { CmdConfigSet([]string{"panes", "5", "--json"}, &ctx, env, repo) })
		if code != 2 || message != "config set: unknown option '--json'" {
			t.Fatalf("set --json without --dry-run = (%d, %q)", code, message)
		}
	})
	if out != "" {
		t.Fatalf("set --json without --dry-run printed %q on stdout, want nothing", out)
	}
	if got, _ := os.ReadFile(file); string(got) != "panes=4\n" {
		t.Fatalf("the rejected set wrote the file: %q", got)
	}
}

func TestPanelConfigEmptyInlineValueIsRefusedWithoutAWrite(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "role.tester.model=sentinel\n")
	ctx := LoadConfig(env, repo)
	const want = "usage: config set <key> <value> | <key>=<value> [--project|--user] (default --project; for this session only: soho session set)"
	before := panelDigest(t, file)
	checkSentinel := func(label string) {
		t.Helper()
		if got := panelDigest(t, file); got != before {
			t.Fatalf("%s changed the config: %s -> %s", label, before, got)
		}
	}

	// The text preview: the tokens after key= are options, the value is empty.
	out, _ := capture(t, func() {
		code, message := panelExit(t, func() { CmdConfigSet([]string{"role.tester.model=", "--project", "--dry-run"}, &ctx, env, repo) })
		if code != 2 || message != want {
			t.Fatalf("text preview = (%d, %q), want exit 2 with the empty-value refusal", code, message)
		}
	})
	if out != "" {
		t.Fatalf("text preview printed %q on stdout, want nothing", out)
	}
	checkSentinel("the text preview")

	// The JSON preview: valid:false with that error, still no write.
	out, _ = capture(t, func() {
		code, message := panelExit(t, func() {
			CmdConfigSet([]string{"role.tester.model=", "--project", "--dry-run", "--json"}, &ctx, env, repo)
		})
		if code != 2 || message != want {
			t.Fatalf("JSON preview = (%d, %q)", code, message)
		}
	})
	obj := panelParseObject(t, out)
	for key, wantValue := range map[string]any{"schema": float64(1), "key": "role.tester.model", "value": "", "valid": false, "error": want} {
		if got, _ := obj.Get(key); got != wantValue {
			t.Fatalf("JSON preview %s = %#v, want %#v", key, got, wantValue)
		}
	}
	checkSentinel("the JSON preview")

	// The real set refuses the same way: --project and --dry-run never become
	// the value.
	code, message := panelExit(t, func() { CmdConfigSet([]string{"role.tester.model=", "--project"}, &ctx, env, repo) })
	if code != 2 || message != want {
		t.Fatalf("set = (%d, %q), want exit 2 with the empty-value refusal", code, message)
	}
	checkSentinel("the refused set")
	if got := FileKeyValue(file, "role.tester.model"); got != "sentinel" {
		t.Fatalf("saved value = %q, want the sentinel", got)
	}

	// The control: an explicit empty second argument keeps the base refusal,
	// so the inline form's message stays tied to the key= spelling.
	code, message = panelExit(t, func() { CmdConfigSet([]string{"role.tester.model", ""}, &ctx, env, repo) })
	if code != 2 || message != "config set: empty value" {
		t.Fatalf("two-argument empty value = (%d, %q), want the base refusal", code, message)
	}
	checkSentinel("the two-argument refusal")
}

func TestPanelConfigResponseModeIsDecidedBeforeParseErrors(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "role.tester.model=sentinel\n")
	ctx := LoadConfig(env, repo)
	before := panelDigest(t, file)

	// No preview: --json never puts a JSON error on stdout, wherever it sits.
	for _, argv := range [][]string{
		{"role.tester.model=changed", "--project", "--json", "--bogus"},
		{"role.tester.model=changed", "--project", "--bogus", "--json"},
	} {
		out, _ := capture(t, func() {
			code, message := panelExit(t, func() { CmdConfigSet(argv, &ctx, env, repo) })
			if code != 2 || message != "config set: unknown option '--bogus'" {
				t.Fatalf("set %v = (%d, %q)", argv, code, message)
			}
		})
		if out != "" {
			t.Fatalf("set %v printed %q on stdout, want nothing (no preview)", argv, out)
		}
	}

	// A real preview renders the JSON error wherever its two flags sit.
	for _, argv := range [][]string{
		{"role.tester.model=changed", "--project", "--dry-run", "--bogus", "--json"},
		{"role.tester.model=changed", "--project", "--json", "--dry-run", "--bogus"},
		{"--json", "--dry-run", "--bogus", "role.tester.model=changed", "--project"},
	} {
		out, _ := capture(t, func() {
			code, message := panelExit(t, func() { CmdConfigSet(argv, &ctx, env, repo) })
			if code != 2 || message != "config set: unknown option '--bogus'" {
				t.Fatalf("preview %v = (%d, %q)", argv, code, message)
			}
		})
		obj := panelParseObject(t, out)
		if valid, _ := obj.Get("valid"); valid != false {
			t.Fatalf("preview %v valid = %#v, want false", argv, valid)
		}
		if got, _ := obj.Get("error"); got != "config set: unknown option '--bogus'" {
			t.Fatalf("preview %v error = %#v", argv, got)
		}
	}
	if got := panelDigest(t, file); got != before {
		t.Fatalf("a refused invocation changed the config: %s -> %s", before, got)
	}
}

func TestPanelConfigPreviewPresetLaneRolesUseTheListingRow(t *testing.T) {
	env, repo := fixture(t)
	file := filepath.Join(repo, ".agents", "soho.conf")
	write(t, file, "lanes=on\n")
	ctx := LoadConfig(env, repo)

	listing, _ := capture(t, func() { CmdConfig(&ctx, env, repo) })
	wantValue, wantSource := "", ""
	for _, line := range strings.Split(listing, "\n") {
		if fields := strings.Fields(line); len(fields) >= 3 && fields[0] == "lane.builders.roles" {
			wantValue, wantSource = fields[1], strings.Join(fields[2:], " ")
		}
	}
	const preset = "implementer,critical-implementer,designer,tasker,tester,scouter,researcher,documenter"
	if wantValue != preset || wantSource != "skill default" {
		t.Fatalf("listing lane.builders.roles row = (%q, %q), want the skill default's preset", wantValue, wantSource)
	}

	out, _ := capture(t, func() {
		CmdConfigSet([]string{"lane.builders.roles=tester", "--project", "--dry-run", "--json"}, &ctx, env, repo)
	})
	obj := panelParseObject(t, out)
	prev := panelObjectField(t, objValue(obj, "previous"), "previous")
	if got, _ := prev.Get("in_file"); got != nil {
		t.Fatalf("preset lane previous.in_file = %#v, want null", got)
	}
	if got, _ := prev.Get("effective"); got != wantValue {
		t.Fatalf("previous.effective = %#v, want the listing's %q", got, wantValue)
	}
	if got, _ := prev.Get("effective_source"); got != wantSource {
		t.Fatalf("previous.effective_source = %#v, want the listing's %q", got, wantSource)
	}
	applies := panelStringArray(t, objValue(obj, "applies_to_roles"), "applies_to_roles")
	if len(applies) != 8 || applies[0] != "implementer" || applies[7] != "documenter" {
		t.Fatalf("applies_to_roles = %v, want the preset membership", applies)
	}

	// A lane key spelled with underscores is the lane's own key too: the
	// configured value wins in the preview and the listing has no preset row
	// for that lane (review round 3, F2).
	write(t, file, "lanes=on\nlane_builders_roles=tester\n")
	ctx = LoadConfig(env, repo)
	out, _ = capture(t, func() {
		CmdConfigSet([]string{"lane.builders.roles=designer", "--project", "--dry-run", "--json"}, &ctx, env, repo)
	})
	prev = panelObjectField(t, objValue(panelParseObject(t, out), "previous"), "previous")
	if got, _ := prev.Get("effective"); got != "tester" {
		t.Fatalf("underscore lane key: previous.effective = %#v, want the configured tester", got)
	}
	if got, _ := prev.Get("effective_source"); got != "project" {
		t.Fatalf("underscore lane key: previous.effective_source = %#v, want project", got)
	}
	listing, _ = capture(t, func() { CmdConfig(&ctx, env, repo) })
	if strings.Contains(listing, preset) {
		t.Fatalf("the listing still synthesizes the preset row for a lane with its own key:\n%s", listing)
	}

	// A lane with a key of its own never gets the synthesized row.
	write(t, file, "lanes=on\nlane.builders.panes=2\n")
	ctx = LoadConfig(env, repo)
	out, _ = capture(t, func() {
		CmdConfigSet([]string{"lane.builders.roles=tester", "--project", "--dry-run", "--json"}, &ctx, env, repo)
	})
	prev = panelObjectField(t, objValue(panelParseObject(t, out), "previous"), "previous")
	if got, _ := prev.Get("effective"); got != "" {
		t.Fatalf("previous.effective = %#v, want empty for a lane with its own key", got)
	}
}

func TestPanelConfigSymlinkedDestinationsShareOneLock(t *testing.T) {
	env, repo := fixture(t)
	project := filepath.Join(repo, ".agents", "soho.conf")
	user := ConfigFileFor("user", env, repo)
	target := filepath.Join(repo, "shared-config")
	pad := strings.Repeat("# retained padding line for the read-modify-write window\n", 60000)
	if err := os.MkdirAll(filepath.Dir(project), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, project); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, user); err != nil {
		t.Fatal(err)
	}
	ctx := LoadConfig(env, repo)
	oldOut := platform.Stdout
	platform.Stdout = io.Discard
	t.Cleanup(func() { platform.Stdout = oldOut })
	run := func(fn func()) (code int, message string) {
		defer func() {
			if recovered := recover(); recovered != nil {
				exit, ok := recovered.(*platform.ExitError)
				if !ok {
					panic(recovered)
				}
				code, message = exit.Code, exit.Msg
			}
		}()
		fn()
		return 0, ""
	}
	pair := func(label string, a, b func(), want map[string]string, absent []string) {
		t.Helper()
		start := make(chan struct{})
		results := make(chan [2]any, 2)
		for _, fn := range []func(){a, b} {
			fn := fn
			go func() {
				<-start
				code, message := run(fn)
				results <- [2]any{code, message}
			}()
		}
		close(start)
		for i := 0; i < 2; i++ {
			if result := <-results; result[0] != 0 {
				t.Fatalf("%s: a command failed: (%v, %v)", label, result[0], result[1])
			}
		}
		pairs, err := ReadConfigFilePairs(target)
		if err != nil {
			t.Fatalf("%s: read back: %v", label, err)
		}
		got := map[string]string{}
		for _, p := range pairs {
			got[p.Key] = p.Value
		}
		for key, value := range want {
			if got[key] != value {
				t.Fatalf("%s: %s = %q, want %q (both changes must survive)", label, key, got[key], value)
			}
		}
		for _, key := range absent {
			if value, present := got[key]; present {
				t.Fatalf("%s: %s is still assigned (%q); the unset was lost", label, key, value)
			}
		}
		if !panelIsSymlink(project) || !panelIsSymlink(user) {
			t.Fatalf("%s: a config symlink was replaced: project=%v user=%v", label, panelIsSymlink(project), panelIsSymlink(user))
		}
	}
	for round := 0; round < 16; round++ {
		if err := os.WriteFile(target, []byte("role.tester.model=old\nrole.tester.effort=old\n"+pad), 0o600); err != nil {
			t.Fatal(err)
		}
		pair("set/unset on two names for one file", func() { CmdConfigSet([]string{"role.tester.model=changed", "--project"}, &ctx, env, repo) }, func() {
			CmdConfigUnset([]string{"role.tester.effort", "--user"}, env, repo)
		}, map[string]string{"role_tester_model": "changed"}, []string{"role_tester_effort"})
	}
	for round := 0; round < 16; round++ {
		if err := os.WriteFile(target, []byte("role.tester.model=old\nrole.tester.effort=old\n"+pad), 0o600); err != nil {
			t.Fatal(err)
		}
		pair("set/set on two names for one file", func() { CmdConfigSet([]string{"role.tester.model=changed", "--project"}, &ctx, env, repo) }, func() {
			CmdConfigSet([]string{"role.tester.effort=low", "--user"}, &ctx, env, repo)
		}, map[string]string{"role_tester_model": "changed", "role_tester_effort": "low"}, nil)
	}
}

// panelIsSymlink reports a symlink at path, for the controls that a rewrite
// through the resolved destination keeps the name the user's config uses.
func panelIsSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func panelFileDigestOrAbsent(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return "absent"
		}
		t.Fatalf("read %s: %v", file, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
