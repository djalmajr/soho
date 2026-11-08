package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type mutationGuardHcFixture struct {
	root   string
	bin    string
	home   string
	state  string
	config string
	tmp    string
	repo   string
	copy   string
	env    platform.Env
}

func newMutationGuardHcFixture(t *testing.T) mutationGuardHcFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	state := filepath.Join(root, "state")
	config := filepath.Join(root, "config")
	tmp := filepath.Join(root, "tmp")
	repo := filepath.Join(root, "repo")
	copyDir := filepath.Join(root, "isolated_copy")

	for _, dir := range []string{bin, home, config, tmp, copyDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// Install trapping hc and herdr fakes with no rules: any execution
	// logs a call and exits with 127 ("fakecli: no rule for...").
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{}); err != nil {
		t.Fatal(err)
	}

	env := make(platform.Env)
	for _, entry := range testutil.CleanEnv(t) {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			env[k] = v
		}
	}
	env["HOME"] = home
	env["USERPROFILE"] = home
	env["SOHO_DIR"] = state
	env["XDG_CONFIG_HOME"] = config
	env["TMPDIR"] = tmp
	env["PATH"] = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	env["SOHO_FAKECLI_CONFIG"] = bin
	env["GIT_CONFIG_GLOBAL"] = os.DevNull
	env["GIT_CONFIG_SYSTEM"] = os.DevNull
	env["GIT_AUTHOR_NAME"] = "mutation-guard-hc fixture"
	env["GIT_AUTHOR_EMAIL"] = "fixture@example.invalid"
	env["GIT_COMMITTER_NAME"] = "mutation-guard-hc fixture"
	env["GIT_COMMITTER_EMAIL"] = "fixture@example.invalid"
	delete(env, "CARGO_TARGET_DIR")
	delete(env, "CARGO_BUILD_TARGET_DIR")
	delete(env, "SOHO_NOWRITE")
	delete(env, "SOHO_TRANSPORT")
	delete(env, "HC_TERMINAL_ID")

	// Source repository with tracked file
	gitRepoAll(t, mutationCopyFixture{env: env}, repo, "tracked.txt", "source-content\n")

	// Isolated non-Cargo copy directory
	if err := os.WriteFile(filepath.Join(copyDir, "plain.txt"), []byte("copy-content\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return mutationGuardHcFixture{
		root:   root,
		bin:    bin,
		home:   home,
		state:  state,
		config: config,
		tmp:    tmp,
		repo:   repo,
		copy:   copyDir,
		env:    env,
	}
}

func runMutationGuardHc(t *testing.T, args []string, env platform.Env) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &stdout, &stderr
	defer func() {
		platform.Stdout, platform.Stderr = oldOut, oldErr
	}()
	code := Run(args, env)
	return code, stdout.String(), stderr.String()
}

func assertZeroCliCalls(t *testing.T, logFile string, cliName string) {
	t.Helper()
	calls, err := fakecli.ReadCalls(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read %s calls: %v", cliName, err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 %s invocations, got %d: %#v", cliName, len(calls), calls)
	}
}

func fileSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := make(map[string]string)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return snap
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if info.IsDir() {
			snap[rel] = "dir"
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snap[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestMutationGuardHcBootstrap(t *testing.T) {
	t.Run("valid isolated non-cargo copy with synthetic hc identity present", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		beforeRepo := fileSnapshot(t, f.repo)
		beforeCopy := fileSnapshot(t, f.copy)

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.copy, "--source", f.repo}, env)

		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if errOut != "" {
			t.Fatalf("stderr = %q, want empty", errOut)
		}
		wantOut := "ok copy-outside-source\nok copy-outside-repository\nok no-symlink-into-source\nok build-env\nok cargo-config\n"
		if out != wantOut {
			t.Fatalf("stdout = %q, want %q", out, wantOut)
		}

		// Assert zero hc and zero herdr invocations
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
		assertZeroCliCalls(t, filepath.Join(f.bin, "herdr.calls.jsonl"), "herdr")

		// Assert absence of configured state root and cache
		if _, err := os.Stat(f.state); !os.IsNotExist(err) {
			t.Fatalf("configured state root %s must not be created", f.state)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".cache")); !os.IsNotExist(err) {
			t.Fatalf("cache directory %s must not be created", filepath.Join(f.home, ".cache"))
		}

		afterRepo := fileSnapshot(t, f.repo)
		afterCopy := fileSnapshot(t, f.copy)
		if !reflect.DeepEqual(beforeRepo, afterRepo) {
			t.Fatalf("source repository modified: before=%#v after=%#v", beforeRepo, afterRepo)
		}
		if !reflect.DeepEqual(beforeCopy, afterCopy) {
			t.Fatalf("copy directory modified: before=%#v after=%#v", beforeCopy, afterCopy)
		}
	})

	t.Run("valid isolated non-cargo copy with identity absent", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.copy, "--source", f.repo}, env)

		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if errOut != "" {
			t.Fatalf("stderr = %q, want empty", errOut)
		}
		wantOut := "ok copy-outside-source\nok copy-outside-repository\nok no-symlink-into-source\nok build-env\nok cargo-config\n"
		if out != wantOut {
			t.Fatalf("stdout = %q, want %q", out, wantOut)
		}

		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
		assertZeroCliCalls(t, filepath.Join(f.bin, "herdr.calls.jsonl"), "herdr")

		if _, err := os.Stat(f.state); !os.IsNotExist(err) {
			t.Fatalf("configured state root %s must not be created", f.state)
		}
	})

	t.Run("overlap equal", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.repo, "--source", f.repo}, env)
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout=%q stderr=%q)", code, out, errOut)
		}
		wantLine := "fail copy-outside-source: copy and source directories overlap"
		if !strings.Contains(out, wantLine) {
			t.Fatalf("stdout = %q, want it to contain %q", out, wantLine)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})

	t.Run("overlap nested", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		nested := filepath.Join(f.repo, "nested_copy")
		if err := os.MkdirAll(nested, 0o700); err != nil {
			t.Fatal(err)
		}

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", nested, "--source", f.repo}, env)
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout=%q stderr=%q)", code, out, errOut)
		}
		wantLine := "fail copy-outside-source: copy and source directories overlap"
		if !strings.Contains(out, wantLine) {
			t.Fatalf("stdout = %q, want it to contain %q", out, wantLine)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})

	t.Run("overlap ancestor", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		sub := filepath.Join(f.repo, "sub")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.repo, "--source", sub}, env)
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout=%q stderr=%q)", code, out, errOut)
		}
		wantLine := "fail copy-outside-source: copy and source directories overlap"
		if !strings.Contains(out, wantLine) {
			t.Fatalf("stdout = %q, want it to contain %q", out, wantLine)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})

	t.Run("sibling worktree inside repository", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		wtA := filepath.Join(f.repo, ".worktrees", "a")
		wtB := filepath.Join(f.repo, ".worktrees", "b")
		fixtureGit(t, mutationCopyFixture{env: env}, f.repo, "worktree", "add", "--detach", wtA)
		fixtureGit(t, mutationCopyFixture{env: env}, f.repo, "worktree", "add", "--detach", wtB)

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", wtB, "--source", wtA}, env)
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout=%q stderr=%q)", code, out, errOut)
		}
		wantPrefix := "fail copy-outside-repository: the copy is inside the repository at "
		if !strings.Contains(out, wantPrefix) {
			t.Fatalf("stdout = %q, want it to contain %q", out, wantPrefix)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})

	t.Run("symlink into source", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		symlinkPath := filepath.Join(f.copy, "link_to_source")
		if err := os.Symlink(filepath.Join(f.repo, "tracked.txt"), symlinkPath); err != nil {
			t.Fatal(err)
		}

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.copy, "--source", f.repo}, env)
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if !strings.Contains(out, "fail no-symlink-into-source:") {
			t.Fatalf("stdout = %q, want it to contain %q", out, "fail no-symlink-into-source:")
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})

	t.Run("build-env pointing into source", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"
		env["CARGO_TARGET_DIR"] = filepath.Join(f.repo, "target")

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.copy, "--source", f.repo}, env)
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if !strings.Contains(out, "fail build-env:") {
			t.Fatalf("stdout = %q, want it to contain %q", out, "fail build-env:")
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})

	t.Run("usage missing copy", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard"}, env)
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if out != "" {
			t.Fatalf("stdout = %q, want empty", out)
		}
		wantStderr := "soho: usage: mutation-guard <copy-dir> [--source <dir>] [--env NAME]...\n"
		if errOut != wantStderr {
			t.Fatalf("stderr = %q, want %q", errOut, wantStderr)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})

	t.Run("usage unknown option", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.copy, "--unknown"}, env)
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if out != "" {
			t.Fatalf("stdout = %q, want empty", out)
		}
		wantStderr := "soho: usage: mutation-guard <copy-dir> [--source <dir>] [--env NAME]...\n"
		if errOut != wantStderr {
			t.Fatalf("stderr = %q, want %q", errOut, wantStderr)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})

	t.Run("usage missing source value", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.copy, "--source"}, env)
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if out != "" {
			t.Fatalf("stdout = %q, want empty", out)
		}
		wantStderr := "soho: usage: mutation-guard <copy-dir> [--source <dir>] [--env NAME]...\n"
		if errOut != wantStderr {
			t.Fatalf("stderr = %q, want %q", errOut, wantStderr)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})
}

func TestMutationGuardNowrite(t *testing.T) {
	t.Run("nowrite with arguments rejects with exact generic diagnostic", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["SOHO_NOWRITE"] = "1"
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.copy, "--source", f.repo}, env)
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if out != "" {
			t.Fatalf("stdout = %q, want empty", out)
		}
		wantStderr := "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, --patterns, --json, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: mutation-guard (3 extra arguments not allowed) — unset SOHO_NOWRITE to write\n"
		if errOut != wantStderr {
			t.Fatalf("stderr = %q, want %q", errOut, wantStderr)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
		if _, err := os.Stat(f.state); !os.IsNotExist(err) {
			t.Fatalf("configured state root %s must not be created", f.state)
		}
	})

	t.Run("nowrite bare command rejects with exact generic diagnostic", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["SOHO_NOWRITE"] = "1"
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard"}, env)
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if out != "" {
			t.Fatalf("stdout = %q, want empty", out)
		}
		wantStderr := "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, --patterns, --json, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: mutation-guard — unset SOHO_NOWRITE to write\n"
		if errOut != wantStderr {
			t.Fatalf("stderr = %q, want %q", errOut, wantStderr)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})
}

func TestMutationGuardHcGuardRefusals(t *testing.T) {
	t.Run("unsupported commands retain exact hc refusal", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["SOHO_TRANSPORT"] = "hc"

		for _, cmd := range []string{"clean", "mutation-copy"} {
			code, out, errOut := runMutationGuardHc(t, []string{cmd}, env)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (stdout=%q stderr=%q)", code, out, errOut)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want empty", out)
			}
			wantStderr := "soho: " + cmd + " does not support transport=hc yet; set transport=herdr or see DJA-604\n"
			if errOut != wantStderr {
				t.Fatalf("stderr = %q, want %q", errOut, wantStderr)
			}
		}
	})
}

func TestMutationGuardHelp(t *testing.T) {
	const wantHelp = "  soho mutation-guard <copy-dir> [--source <dir>] [--env NAME]...\n"

	t.Run("first position --help with synthetic identity present", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", "--help"}, env)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if errOut != "" {
			t.Fatalf("stderr = %q, want empty", errOut)
		}
		if out != wantHelp {
			t.Fatalf("stdout = %q, want exact %q", out, wantHelp)
		}

		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
		assertZeroCliCalls(t, filepath.Join(f.bin, "herdr.calls.jsonl"), "herdr")
		if _, err := os.Stat(f.state); !os.IsNotExist(err) {
			t.Fatalf("configured state root %s must not be created", f.state)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".cache")); !os.IsNotExist(err) {
			t.Fatalf("cache directory %s must not be created", filepath.Join(f.home, ".cache"))
		}
	})

	t.Run("first position -h with synthetic identity present", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", "-h"}, env)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if errOut != "" {
			t.Fatalf("stderr = %q, want empty", errOut)
		}
		if out != wantHelp {
			t.Fatalf("stdout = %q, want exact %q", out, wantHelp)
		}

		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
		assertZeroCliCalls(t, filepath.Join(f.bin, "herdr.calls.jsonl"), "herdr")
		if _, err := os.Stat(f.state); !os.IsNotExist(err) {
			t.Fatalf("configured state root %s must not be created", f.state)
		}
	})

	t.Run("first position --help with identity absent", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", "--help"}, env)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if errOut != "" {
			t.Fatalf("stderr = %q, want empty", errOut)
		}
		if out != wantHelp {
			t.Fatalf("stdout = %q, want exact %q", out, wantHelp)
		}

		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
		assertZeroCliCalls(t, filepath.Join(f.bin, "herdr.calls.jsonl"), "herdr")
		if _, err := os.Stat(f.state); !os.IsNotExist(err) {
			t.Fatalf("configured state root %s must not be created", f.state)
		}
	})

	t.Run("first position -h with identity absent", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", "-h"}, env)
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if errOut != "" {
			t.Fatalf("stderr = %q, want empty", errOut)
		}
		if out != wantHelp {
			t.Fatalf("stdout = %q, want exact %q", out, wantHelp)
		}

		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
		assertZeroCliCalls(t, filepath.Join(f.bin, "herdr.calls.jsonl"), "herdr")
		if _, err := os.Stat(f.state); !os.IsNotExist(err) {
			t.Fatalf("configured state root %s must not be created", f.state)
		}
	})

	t.Run("help under NOWRITE must still refuse", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["SOHO_NOWRITE"] = "1"
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		wantStderr := "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, --patterns, --json, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: mutation-guard (1 extra argument not allowed) — unset SOHO_NOWRITE to write\n"

		for _, flag := range []string{"--help", "-h"} {
			code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", flag}, env)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 for %s (stdout=%q stderr=%q)", code, flag, out, errOut)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want empty for %s", out, flag)
			}
			if errOut != wantStderr {
				t.Fatalf("stderr = %q, want %q for %s", errOut, wantStderr, flag)
			}
			assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
		}
	})

	t.Run("later position --help is not help and produces usage error", func(t *testing.T) {
		f := newMutationGuardHcFixture(t)
		env := f.env.Clone()
		env["HC_TERMINAL_ID"] = "t1"
		env["SOHO_TRANSPORT"] = "hc"

		code, out, errOut := runMutationGuardHc(t, []string{"mutation-guard", f.copy, "--help"}, env)
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (stdout=%q stderr=%q)", code, out, errOut)
		}
		if out != "" {
			t.Fatalf("stdout = %q, want empty", out)
		}
		wantStderr := "soho: usage: mutation-guard <copy-dir> [--source <dir>] [--env NAME]...\n"
		if errOut != wantStderr {
			t.Fatalf("stderr = %q, want %q", errOut, wantStderr)
		}
		assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	})
}
