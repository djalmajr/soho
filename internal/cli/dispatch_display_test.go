package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchDisplayWarning(t *testing.T) {
	for _, tc := range []struct {
		name, kind, typ, args, config, display string
		warn                                   bool
	}{
		{"native", "codex", "ui", `-c shell_environment_policy.include_only=["PATH"]`, ``, "secret-display-value", true},
		{"config", "codex", "ui", ``, `[shell_environment_policy]
include_only = ["PATH"]`, "secret-display-value", true},
		{"glob", "codex", "ui", `-c shell_environment_policy.include_only=["WAYLAND_*"]`, ``, "secret-display-value", false},
		{"exact", "codex", "ui", `-c shell_environment_policy.include_only=["WAYLAND_DISPLAY"]`, ``, "secret-display-value", false},
		{"none", "codex", "ui", `-c shell_environment_policy.include_only=["PATH"]`, ``, "", false},
		{"backend", "codex", "backend", `-c shell_environment_policy.include_only=["PATH"]`, ``, "secret-display-value", false},
		{"claude", "claude", "ui", `-c shell_environment_policy.include_only=["PATH"]`, ``, "secret-display-value", false},
		{"pi", "pi", "ui", `-c shell_environment_policy.include_only=["PATH"]`, ``, "secret-display-value", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			f.env["WAYLAND_DISPLAY"] = tc.display
			delete(f.env, "DISPLAY")
			delete(f.env, "HYPRLAND_INSTANCE_SIGNATURE")
			if err := os.WriteFile(f.brief, []byte("Type: "+tc.typ+"\n\n# Goal\n\nDispatch.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(f.state, "ws", "agents.tsv")
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(b), "\n")
			row := strings.Split(lines[1], "\t")
			row[2], row[13] = tc.kind, tc.args
			lines[1] = strings.Join(row, "\t")
			if err = os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				dir := filepath.Join(f.root, "home", ".codex")
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, "config.toml"), []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var output string
			for i := 0; i < 2; i++ {
				code, out, msg := f.run(t, "worker", f.brief, "--no-wait", "--resend")
				if code != 0 {
					t.Fatalf("exit=%d out=%s stderr=%s", code, out, msg)
				}
				output += out + msg
			}
			want := 0
			if tc.warn {
				want = 1
			}
			warning := "soho: warning: dispatch: 'worker' (codex) does not receive WAYLAND_DISPLAY (shell_environment_policy.include_only): the graphical proof of this ui brief stays with you, or add them to include_only"
			if strings.Count(output, warning) != want || strings.Contains(output, "secret-display-value") {
				t.Fatalf("warning count=%d want=%d output=%s", strings.Count(output, warning), want, output)
			}
		})
	}
}
