package kinds

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func stubPiCoreEnvLaunch(t *testing.T, launch func(context.Context, platform.Env, ...string) platform.RunResult) {
	t.Helper()
	old := piCoreEnvLaunch
	piCoreEnvLaunch = launch
	t.Cleanup(func() { piCoreEnvLaunch = old })
}

func TestDoctorPiBarePrintenv(t *testing.T) {
	calls := 0
	stubPiCoreEnvLaunch(t, func(_ context.Context, _ platform.Env, _ ...string) platform.RunResult {
		calls++
		return platform.RunResult{NotFound: true}
	})
	for _, tc := range []struct {
		name, key, transport, login string
		want                        bool
	}{
		{"bare", "!printenv OPENCODE_API_KEY", "hc", "off", true},
		{"spaces", "!  printenv   OPENCODE_API_KEY  ", "hc", "off", true},
		{"login on", "!printenv OPENCODE_API_KEY", "hc", "on", false},
		{"legacy", "!printenv OPENCODE_API_KEY", "herdr", "off", false},
		{"zsh", "!/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv OPENCODE_API_KEY'", "hc", "off", false},
		{"suffix command", "!printenv OPENCODE_API_KEY; echo no", "hc", "off", false},
		{"argument", "!printenv OPENCODE_API_KEY OTHER", "hc", "off", false},
		{"substitution", "!printenv $(touch bad)", "hc", "off", false},
		{"embedded substitution", "!printenv KEY$(touch bad)", "hc", "off", false},
		{"semicolon", "!printenv KEY;OTHER", "hc", "off", false},
		{"space in name", "!printenv KEY OTHER", "hc", "off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, ".pi", "agent", "models.json")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(map[string]any{"providers": map[string]any{"vendor": map[string]any{"apiKey": tc.key, "models": []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}}, "override": map[string]any{"apiKey": tc.key}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": filepath.Join(root, "xdg"), "SOHO_TRANSPORT": tc.transport, "SOHO_LOGIN_SHELL": tc.login}
			before := calls
			_, warnings := OwnProviderDoctorLines(&core.Config{}, env, root)
			wantCalls := 0
			if tc.want {
				wantCalls = 1
			}
			if calls-before != wantCalls {
				t.Fatalf("probe calls = %d, want %d", calls-before, wantCalls)
			}
			var matched []string
			for _, w := range warnings {
				if strings.Contains(w, "Core environment") {
					matched = append(matched, w)
				}
			}
			if tc.want {
				if len(matched) != 2 {
					t.Fatalf("want warning per provider, got %q", warnings)
				}
				for _, w := range matched {
					for _, s := range []string{"OPENCODE_API_KEY", "login_shell=on", "/usr/bin/env -u ZDOTDIR /bin/zsh -c"} {
						if !strings.Contains(w, s) {
							t.Errorf("missing %q in %q", s, w)
						}
					}
					if strings.Contains(w, tc.key) {
						t.Errorf("apiKey leaked: %q", w)
					}
				}
			} else if len(matched) != 0 {
				t.Fatalf("unexpected warnings: %q", warnings)
			}
		})
	}
}

func TestDoctorPiCoreEnvProbe(t *testing.T) {
	status := func(n int) *int { return &n }
	for _, tc := range []struct {
		name    string
		result  platform.RunResult
		message string
	}{
		{"present", platform.RunResult{Status: status(0)}, ""},
		{"absent", platform.RunResult{Status: status(3)}, "KEY is not set in the Core environment, so its workers cannot read the key; set login_shell=on or use a key command such as !/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv KEY' to load the profile"},
		{"other exit", platform.RunResult{Status: status(1)}, "worker inherits only the Core environment; KEY may be missing there. Set login_shell=on or use a key command such as !/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv KEY' to load the profile"},
		{"hc missing", platform.RunResult{NotFound: true}, "worker inherits only the Core environment; KEY may be missing there. Set login_shell=on or use a key command such as !/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv KEY' to load the profile"},
		{"core down", platform.RunResult{Status: status(2)}, "worker inherits only the Core environment; KEY may be missing there. Set login_shell=on or use a key command such as !/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv KEY' to load the profile"},
		{"timeout", platform.RunResult{TimedOut: true, Error: "ETIMEDOUT"}, "worker inherits only the Core environment; KEY may be missing there. Set login_shell=on or use a key command such as !/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv KEY' to load the profile"},
		{"error with status", platform.RunResult{Status: status(3), Error: "ETIMEDOUT"}, "worker inherits only the Core environment; KEY may be missing there. Set login_shell=on or use a key command such as !/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv KEY' to load the profile"},
		{"no status", platform.RunResult{Error: "failed"}, "worker inherits only the Core environment; KEY may be missing there. Set login_shell=on or use a key command such as !/usr/bin/env -u ZDOTDIR /bin/zsh -c 'printenv KEY' to load the profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".pi", "agent")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"one":{"apiKey":"!printenv KEY"},"two":{"apiKey":"!printenv KEY"}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			stubPiCoreEnvLaunch(t, func(ctx context.Context, _ platform.Env, args ...string) platform.RunResult {
				calls++
				want := []string{"exec", "--timeout", "5", "sh", "-c", `test -n "${KEY+x}" && exit 0 || exit 3`}
				if !reflect.DeepEqual(args, want) {
					t.Errorf("argv = %#v; want %#v", args, want)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
					t.Error("probe lacks a live, at most 5-second client deadline")
				}
				r := tc.result
				r.Stdout = "fake-secret-value"
				r.Stderr = "fake-secret-value"
				return r
			})
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": filepath.Join(root, "xdg"), "SOHO_TRANSPORT": "hc", "SOHO_LOGIN_SHELL": "off"}
			_, warnings := OwnProviderDoctorLines(&core.Config{}, env, root)
			if calls != 1 {
				t.Fatalf("calls = %d; want one for shared variable", calls)
			}
			if tc.message == "" {
				if len(warnings) != 0 {
					t.Fatalf("present key warned: %q", warnings)
				}
				return
			}
			if len(warnings) != 2 {
				t.Fatalf("warnings = %q; want one per provider", warnings)
			}
			for i, name := range []string{"one", "two"} {
				if warnings[i] != "pi provider '"+name+"': "+tc.message {
					t.Errorf("wrong warning for %s: %q", name, warnings[i])
				}
				if strings.Contains(warnings[i], "fake-secret-value") {
					t.Error("probe output leaked")
				}
			}
		})
	}
}

func TestDoctorPiCoreEnvDeadlineOverridesExit(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".pi", "agent")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"one":{"apiKey":"!printenv KEY"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	stubPiCoreEnvLaunch(t, func(ctx context.Context, _ platform.Env, _ ...string) platform.RunResult {
		<-ctx.Done()
		status := 3
		return platform.RunResult{Status: &status}
	})
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": filepath.Join(root, "xdg"), "SOHO_TRANSPORT": "hc", "SOHO_LOGIN_SHELL": "off"}
	_, warnings := OwnProviderDoctorLines(&core.Config{}, env, root)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "KEY may be missing there") {
		t.Fatalf("deadline with exit 3 must be unknown; warnings = %q", warnings)
	}
}

func TestDoctorPiCoreEnvProbePerRunAndVariable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".pi", "agent")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"one":{"apiKey":"!printenv KEY"},"two":{"apiKey":"!printenv KEY"},"three":{"apiKey":"!printenv _KEY2"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var scripts []string
	stubPiCoreEnvLaunch(t, func(_ context.Context, _ platform.Env, args ...string) platform.RunResult {
		script := args[len(args)-1]
		scripts = append(scripts, script)
		status := 0
		if strings.Contains(script, "_KEY2") {
			status = 3
		}
		return platform.RunResult{Status: &status}
	})
	env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": filepath.Join(root, "xdg"), "SOHO_TRANSPORT": "hc", "SOHO_LOGIN_SHELL": "off"}
	for run := 0; run < 2; run++ {
		_, warnings := OwnProviderDoctorLines(&core.Config{}, env, root)
		if len(warnings) != 1 || !strings.Contains(warnings[0], "pi provider 'three': _KEY2 is not set") {
			t.Fatalf("run %d warnings = %q", run, warnings)
		}
	}
	want := []string{`test -n "${KEY+x}" && exit 0 || exit 3`, `test -n "${_KEY2+x}" && exit 0 || exit 3`, `test -n "${KEY+x}" && exit 0 || exit 3`, `test -n "${_KEY2+x}" && exit 0 || exit 3`}
	if !reflect.DeepEqual(scripts, want) {
		t.Fatalf("scripts = %#v; want %#v", scripts, want)
	}
}
