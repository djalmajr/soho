package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// TestNativeDoctorHookPrintsNoPaneAdvice: the SessionStart doctor hook of a
// native Core session injects no pane-count or split_max_panes advice, and
// the legacy hook still does (the control: the same project and files).
func TestNativeDoctorHookPrintsNoPaneAdvice(t *testing.T) {
	for _, tc := range []struct {
		name      string
		native    bool
		wantPanes bool
	}{
		{"legacy transport keeps the advice", false, true},
		{"native transport prints none", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := commandFixture(t)
			if tc.native {
				env, _ = hcFixture(t, env, repo, "t1")
			} else {
				env["HERDR_ENV"], env["PATH"] = "1", t.TempDir()
			}
			conf := filepath.Join(repo, ".agents", "soho.conf")
			if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
				t.Fatal(err)
			}
			// panes=9 is not a valid count; split_max_panes=9 is above 1 + max_workers.
			if err := os.WriteFile(conf, []byte("panes=9\nmax_workers=3\nsplit_max_panes=9\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := runIn(t, []string{"hook", "doctor"}, env, repo)
			if code != 0 || stderr != "" {
				t.Fatalf("hook doctor: code=%d stderr=%q out=%s", code, stderr, out)
			}
			if out == "" {
				t.Fatal("the hook printed no warnings at all: the doctor did not run")
			}
			advice := strings.Contains(out, "panes='9' is not 2, 3 or 4") && strings.Contains(out, "split_max_panes=9 is greater than 1 + max_workers")
			if advice != tc.wantPanes {
				t.Fatalf("pane advice present=%v, want %v:\n%s", advice, tc.wantPanes, out)
			}
			if !tc.wantPanes && (strings.Contains(out, "panes=") || strings.Contains(out, "split_max_panes")) {
				t.Fatalf("native hook mentions panes:\n%s", out)
			}
		})
	}
}

// TestFirstRunFalseWithTeamChosenInUserFile: init and doctor report
// first_run=false when only the user's own file chooses the team (no project
// choice, no roster row), and true when nothing does.
func TestFirstRunFalseWithTeamChosenInUserFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		user string
		want string
	}{
		{"user file chooses a lane kind", "lane.build.kind=pi\n", "false"},
		{"user file chooses nothing", "max_workers=4\n", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := commandFixture(t)
			user := core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{})
			if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(user, []byte(tc.user), 0o600); err != nil {
				t.Fatal(err)
			}
			env, _ = hcFixture(t, env, repo, "t1")
			var out, stderr strings.Builder
			runAndCapture(t, env, []string{"init"}, &out, &stderr)
			if want := `"first_run": ` + tc.want; !strings.Contains(out.String(), want) {
				t.Fatalf("init lacks %s:\n%s\nstderr:\n%s", want, out.String(), stderr.String())
			}
			code, docOut, docErr := runIn(t, []string{"doctor"}, env, repo)
			if code != 0 || !strings.Contains(docOut, "first_run: "+tc.want+"\n") {
				t.Fatalf("doctor lacks first_run: %s: code=%d err=%q\n%s", tc.want, code, docErr, docOut)
			}
		})
	}
}
