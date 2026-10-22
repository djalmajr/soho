package core

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// TestLegacyLeftovers: LegacyLeftovers reports the former herdr-soho and
// herdr-agents names with the exact decision-8 messages, in the exact
// order (the former ones first, then the new leftovers), and nothing when
// there are no leftovers.
func TestLegacyLeftovers(t *testing.T) {
	run := func(t *testing.T, mut func(env platform.Env, root string)) []string {
		t.Helper()
		env, cwd := formerNamesEnv(t)
		if mut != nil {
			mut(env, cwd)
		}
		return LegacyLeftovers(env, cwd, platform.Current())
	}

	t.Run("no leftovers: nothing", func(t *testing.T) {
		got := run(t, nil)
		if len(got) != 0 {
			t.Fatalf("leftovers = %v, want none", got)
		}
	})

	t.Run("former env names only, alphabetical, empty values skipped", func(t *testing.T) {
		got := run(t, func(env platform.Env, root string) {
			env["HERDR_SOHO_BETA"] = "2"
			env["HERDR_SOHO_ALPHA"] = "1"
			env["HERDR_SOHO_EMPTY"] = ""
			env["SOHO_LAYOUT"] = "columns"
		})
		want := []string{
			"environment variable HERDR_SOHO_ALPHA is no longer read; rename it to SOHO_ALPHA",
			"environment variable HERDR_SOHO_BETA is no longer read; rename it to SOHO_BETA",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former user config dir with a config file: the dir warning replaces the file one", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		oldUser := LegacyUserConfigPath(platform.Current(), env)
		write(t, oldUser, "max_workers=7\n")
		got := LegacyLeftovers(env, cwd, platform.Current())
		oldDir := filepath.Dir(oldUser)
		sohoDir := filepath.Dir(platform.UserConfigPath(platform.Current(), env))
		want := []string{
			"former user config dir " + oldDir + " is still there: run 'soho setup' to move it to " + sohoDir,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former user config dir that is empty: nothing", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		if err := os.MkdirAll(filepath.Dir(LegacyUserConfigPath(platform.Current(), env)), 0o700); err != nil {
			t.Fatal(err)
		}
		got := LegacyLeftovers(env, cwd, platform.Current())
		if len(got) != 0 {
			t.Fatalf("leftovers = %v, want none (an empty former dir has no content)", got)
		}
	})

	t.Run("former project config file only", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		oldProject := filepath.Join(cwd, ".agents", "herdr-soho.conf")
		write(t, oldProject, "panes=3\n")
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{
			"former project config " + oldProject + " is no longer read: run 'soho setup' to move it to " + filepath.Join(cwd, ".agents", "soho.conf"),
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former state dir only", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		if err := os.MkdirAll(filepath.Join(cwd, ".herdr-soho"), 0o700); err != nil {
			t.Fatal(err)
		}
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{
			"former state dir " + filepath.Join(cwd, ".herdr-soho") + " is no longer used: run 'soho setup' to move it to " + filepath.Join(cwd, ".soho") + " once no worker is live",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former state dir with an empty current one: setup can still move it", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		for _, dir := range []string{".herdr-soho", ".soho"} {
			if err := os.MkdirAll(filepath.Join(cwd, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{
			"former state dir " + filepath.Join(cwd, ".herdr-soho") + " is no longer used: run 'soho setup' to move it to " + filepath.Join(cwd, ".soho") + " once no worker is live",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former state dir beside a current one in use: remove it, setup would keep it", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		if err := os.MkdirAll(filepath.Join(cwd, ".herdr-soho", "w9"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(cwd, ".soho", "w7"), 0o700); err != nil {
			t.Fatal(err)
		}
		got := LegacyLeftovers(env, cwd, platform.Current())
		old := filepath.Join(cwd, ".herdr-soho")
		want := []string{
			"former state dir " + old + " is no longer used and " + filepath.Join(cwd, ".soho") + " already exists: remove " + old + " once its reports are no longer needed",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former state dir the state dir is pinned to: in use, nothing", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		old := filepath.Join(cwd, ".herdr-soho")
		if err := os.MkdirAll(filepath.Join(old, "w9"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(cwd, ".soho", "w7"), 0o700); err != nil {
			t.Fatal(err)
		}
		env["SOHO_DIR"] = old
		got := LegacyLeftovers(env, cwd, platform.Current())
		if len(got) != 0 {
			t.Fatalf("leftovers = %v, want none (the pinned state dir is in use)", got)
		}
	})

	t.Run("former state dir the state dir is pinned inside: in use, nothing", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		old := filepath.Join(cwd, ".herdr-soho")
		if err := os.MkdirAll(filepath.Join(old, "active-state", "w9"), 0o700); err != nil {
			t.Fatal(err)
		}
		env["SOHO_DIR"] = filepath.Join(old, "active-state")
		got := LegacyLeftovers(env, cwd, platform.Current())
		if len(got) != 0 {
			t.Fatalf("leftovers = %v, want none (the pinned state dir lives inside the former one)", got)
		}
	})

	t.Run("former state dir a linked pin resolves inside: in use, nothing", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		old := filepath.Join(cwd, ".herdr-soho")
		if err := os.MkdirAll(filepath.Join(old, "active-state", "w9"), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(cwd, "state-link")
		if err := os.Symlink(filepath.Join(old, "active-state"), link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		for _, pin := range []string{link, filepath.Join(link, "not-yet")} {
			env["SOHO_DIR"] = pin
			got := LegacyLeftovers(env, cwd, platform.Current())
			if len(got) != 0 {
				t.Fatalf("SOHO_DIR=%s: leftovers = %v, want none (the pin resolves inside the former dir)", pin, got)
			}
		}
	})

	t.Run("former state dir that is itself a link to the pinned state: an alias, nothing", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		target := filepath.Join(cwd, "state")
		if err := os.MkdirAll(filepath.Join(target, "w9"), 0o700); err != nil {
			t.Fatal(err)
		}
		old := filepath.Join(cwd, ".herdr-soho")
		if err := os.Symlink(target, old); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		other := filepath.Join(cwd, "other-link")
		if err := os.Symlink(old, other); err != nil {
			t.Fatal(err)
		}
		// The pin names the target directly, or reaches it through the
		// former link: removing that link would cut the second one off.
		for _, pin := range []string{target, other} {
			env["SOHO_DIR"] = pin
			got := LegacyLeftovers(env, cwd, platform.Current())
			if len(got) != 0 {
				t.Fatalf("SOHO_DIR=%s: leftovers = %v, want none (the former dir is an alias of the state dir)", pin, got)
			}
		}
	})

	t.Run("former state dir beside a sibling pin that only shares its prefix: kept, as setup says", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		old := filepath.Join(cwd, ".herdr-soho")
		if err := os.MkdirAll(old, 0o700); err != nil {
			t.Fatal(err)
		}
		pinned := filepath.Join(cwd, ".herdr-soho-state")
		env["SOHO_DIR"] = pinned
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{
			"former state dir " + old + " is kept: the state dir is set to " + pinned,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former state dir with the state dir pinned elsewhere: kept, as setup says", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		old := filepath.Join(cwd, ".herdr-soho")
		if err := os.MkdirAll(old, 0o700); err != nil {
			t.Fatal(err)
		}
		pinned := filepath.Join(cwd, "state")
		env["SOHO_DIR"] = pinned
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{
			"former state dir " + old + " is kept: the state dir is set to " + pinned,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former state dir beside a linked current one: kept, no removal advice", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		old := filepath.Join(cwd, ".herdr-soho")
		if err := os.MkdirAll(filepath.Join(old, "w9"), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(cwd, "elsewhere")
		if err := os.MkdirAll(filepath.Join(target, "w7"), 0o700); err != nil {
			t.Fatal(err)
		}
		current := filepath.Join(cwd, ".soho")
		if err := os.Symlink(target, current); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{"former state dir " + old + " is kept: " + current + " already exists"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former herdr-agents user config dir with content", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		agentsDir := filepath.Join(filepath.Dir(filepath.Dir(LegacyUserConfigPath(platform.Current(), env))), "herdr-agents")
		write(t, filepath.Join(agentsDir, "config"), "max_workers=5\n")
		got := LegacyLeftovers(env, cwd, platform.Current())
		sohoDir := filepath.Dir(platform.UserConfigPath(platform.Current(), env))
		want := []string{
			"former user config dir " + agentsDir + " is still there: run 'soho setup' to move it to " + sohoDir,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former herdr-agents project config file", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		oldProject := filepath.Join(cwd, ".agents", "herdr-agents.conf")
		write(t, oldProject, "panes=3\n")
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{
			"former project config " + oldProject + " is no longer read: run 'soho setup' to move it to " + filepath.Join(cwd, ".agents", "soho.conf"),
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former herdr-agents state dir", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		if err := os.MkdirAll(filepath.Join(cwd, ".herdr-agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{
			"former state dir " + filepath.Join(cwd, ".herdr-agents") + " is no longer used: remove it once its reports are no longer needed",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("former herdr-soho binary on PATH", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		name := "herdr-soho"
		if runtime.GOOS == "windows" {
			name = "herdr-soho.exe"
		}
		binary := filepath.Join(env.Get("PATH"), name)
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		found, ok := platform.FindExecutable("herdr-soho", env, platform.Current())
		if !ok {
			t.Fatalf("FindExecutable did not find %s (the fixture must resolve it)", binary)
		}
		got := LegacyLeftovers(env, cwd, platform.Current())
		want := []string{
			"an old herdr-soho binary is still on PATH at " + found + ": remove it; soho replaces it",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})

	t.Run("everything at once, exact order", func(t *testing.T) {
		env, cwd := formerNamesEnv(t)
		env["HERDR_SOHO_ZETA"] = "1"
		env["HERDR_SOHO_ALPHA"] = "2"
		oldUser := LegacyUserConfigPath(platform.Current(), env)
		write(t, oldUser, "max_workers=7\n")
		oldProject := filepath.Join(cwd, ".agents", "herdr-soho.conf")
		write(t, oldProject, "panes=3\n")
		if err := os.MkdirAll(filepath.Join(cwd, ".herdr-soho"), 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(cwd, ".agents", "herdr-agents.conf"), "panes=2\n")
		if err := os.MkdirAll(filepath.Join(cwd, ".herdr-agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		name := "herdr-soho"
		if runtime.GOOS == "windows" {
			name = "herdr-soho.exe"
		}
		binary := filepath.Join(env.Get("PATH"), name)
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		found, ok := platform.FindExecutable("herdr-soho", env, platform.Current())
		if !ok {
			t.Fatalf("FindExecutable did not find %s (the fixture must resolve it)", binary)
		}
		got := LegacyLeftovers(env, cwd, platform.Current())
		oldUserDir := filepath.Dir(oldUser)
		sohoUserDir := filepath.Dir(platform.UserConfigPath(platform.Current(), env))
		want := []string{
			"environment variable HERDR_SOHO_ALPHA is no longer read; rename it to SOHO_ALPHA",
			"environment variable HERDR_SOHO_ZETA is no longer read; rename it to SOHO_ZETA",
			"former project config " + oldProject + " is no longer read: run 'soho setup' to move it to " + filepath.Join(cwd, ".agents", "soho.conf"),
			"former state dir " + filepath.Join(cwd, ".herdr-soho") + " is no longer used: run 'soho setup' to move it to " + filepath.Join(cwd, ".soho") + " once no worker is live",
			"former user config dir " + oldUserDir + " is still there: run 'soho setup' to move it to " + sohoUserDir,
			"former project config " + filepath.Join(cwd, ".agents", "herdr-agents.conf") + " is no longer read: run 'soho setup' to move it to " + filepath.Join(cwd, ".agents", "soho.conf"),
			"former state dir " + filepath.Join(cwd, ".herdr-agents") + " is no longer used: remove it once its reports are no longer needed",
			"an old herdr-soho binary is still on PATH at " + found + ": remove it; soho replaces it",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("leftovers = %v, want %v", got, want)
		}
	})
}
