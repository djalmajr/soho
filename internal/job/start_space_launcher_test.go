package job

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/text"
)

// The launcher is argv[0] of the supervisor pane run. An unsafe path must be
// refused before the server probe, the checkout, the job directory or any
// workspace call, with the same exit code the pane-run boundary would give.
// The refusal must name the exact resolved path, with only control characters
// escaped, for every hostile spelling below.
func TestStartRefusesSpacedLauncherBeforeAnyCallOrWrite(t *testing.T) {
	for _, tc := range []struct {
		name, component string
		skipWindows     bool
	}{
		{"ordinary space", "soho with space", false},
		{"repeated spaces", "soho  with  space", false},
		{"unicode", "espaço livre", false},
		{"over 200 characters", strings.Repeat("p", 145) + " long", false},
		{"backtick", "soho`with`tick", false},
		{"tab", "soho\twith tab", true},
		{"newline", "soho\nwith line", true},
		{"carriage return", "soho\rwith return", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.skipWindows {
				t.Skip("Windows filenames cannot contain this control character")
			}
			f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
			stateRoot := filepath.Join(t.TempDir(), "state")
			var friction []string
			s := startStarter(t, f, &fakeHerdr{Reachable: true}, stateRoot, &friction)
			dir := filepath.Join(t.TempDir(), tc.component)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(dir, "soho")
			s.SelfExe = launcher

			_, store, err := s.Start(startRequest("main", briefA))
			want := "job: the soho executable path contains characters herdr pane run refuses (" + text.QuoteControls(launcher) + "); install soho in a directory without spaces or set SOHO_BIN to one"
			wantExit(t, err, ExitHerdr, want)
			if store != nil {
				t.Fatalf("store = %v, want nil (nothing written)", store)
			}
			fake := s.Herdr.(*fakeHerdr)
			if fake.ListCalls != 0 || len(fake.CreateCalls) != 0 || len(fake.RunCalls) != 0 || len(fake.CloseIDs) != 0 {
				t.Fatalf("Herdr was called: list=%d create=%d run=%d close=%d", fake.ListCalls, len(fake.CreateCalls), len(fake.RunCalls), len(fake.CloseIDs))
			}
			for _, path := range []string{stateRoot, f.checkout()} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("a start side effect exists at %q: %v", path, statErr)
				}
			}
			if len(friction) != 0 {
				t.Fatalf("friction = %v, want none", friction)
			}
		})
	}
}

// A safe launcher is not refused: the same start reaches the server probe,
// so the refusals above are caused by the path and not by the fixture.
func TestStartSafeLauncherReachesTheServerProbe(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	stateRoot := filepath.Join(t.TempDir(), "state")
	var friction []string
	c := startStarter(t, f, &fakeHerdr{}, stateRoot, &friction)
	c.SelfExe = "/opt/bin/soho"
	_, store, err := c.Start(startRequest("main", briefA))
	wantExit(t, err, ExitHerdr, "job: no reachable Herdr server")
	if store != nil {
		t.Fatalf("control store = %v, want nil", store)
	}
	if got := c.Herdr.(*fakeHerdr).ListCalls; got != 1 {
		t.Fatalf("control ListCalls = %d, want 1", got)
	}
}

// The preflight shares the pane-run argument check: a launcher the pane
// boundary refuses is refused before the workspace, and a safe one is not.
func TestPaneRunArgSafeSharedAdmission(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		safe bool
	}{
		{"/opt/bin/soho", true},
		{`C:\Users\USER~1\soho.exe`, true},
		{"/opt/bin with space/soho", false},
		{"w9:p1; rm -rf /", false},
		{"", false},
	} {
		if got := PaneRunArgSafe(tc.arg); got != tc.safe {
			t.Errorf("PaneRunArgSafe(%q) = %t, want %t", tc.arg, got, tc.safe)
		}
	}
}
