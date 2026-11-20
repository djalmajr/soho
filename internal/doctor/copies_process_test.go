package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// doctorCopyProcessFixture keeps the process scan and the copies registry in
// the test's own temp dirs: no real process is started or stopped. A nil
// rows slice leaves the registry absent; an empty slice writes an empty one.
func doctorCopyProcessFixture(t *testing.T, procs []platform.OrphanProc, scanErr error, rows []core.CopyRow) (platform.Env, string) {
	t.Helper()
	env, root := paneContextFixture(t, doctorFootprintRules())
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["TMPDIR"] = t.TempDir()
	old := doctorCopyProcessesScan
	doctorCopyProcessesScan = func() ([]platform.OrphanProc, error) { return procs, scanErr }
	t.Cleanup(func() { doctorCopyProcessesScan = old })
	if rows != nil {
		state := filepath.Join(root, "state", "ws")
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := core.WriteCopies(state, rows); err != nil {
			t.Fatal(err)
		}
	}
	return env, root
}

func doctorFreezeCopyProcessNow(t *testing.T) {
	t.Helper()
	fixed := time.Now().UTC()
	old := platform.Now
	platform.Now = func() time.Time { return fixed }
	t.Cleanup(func() { platform.Now = old })
}

func doctorCopyProcessRow(path string) core.CopyRow {
	return core.CopyRow{Path: path, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now()), Source: "-", Origin: "mutation-copy"}
}

func doctorCopyProcess(pid int, name, cwd string, age time.Duration) platform.OrphanProc {
	return platform.OrphanProc{Pid: pid, Name: name, Cwd: cwd, Started: platform.Now().Add(-age)}
}

func TestDoctorWarnsAboutOldProcessInsideRegisteredCopy(t *testing.T) {
	doctorFreezeCopyProcessNow(t)
	copyDir := t.TempDir()
	inside := filepath.Join(copyDir, "nested")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	old := doctorCopyProcess(400001, "go", inside, 45*time.Minute)
	env, root := doctorCopyProcessFixture(t, []platform.OrphanProc{old}, nil, []core.CopyRow{doctorCopyProcessRow(copyDir)})
	got := doctorCheckOutput(t, env, root)
	want := fmt.Sprintf("warn   process %d (go, 45m) has run inside copy %s for 45m; stop it with: kill %d", old.Pid, copyDir, old.Pid)
	if n := strings.Count(got, want); n != 1 {
		t.Fatalf("warning count=%d, want 1 (%q):\n%s", n, want, got)
	}
	if !strings.Contains(got, "first_run:") {
		t.Fatalf("the doctor did not finish:\n%s", got)
	}
}

func TestDoctorCopyProcessBoundaries(t *testing.T) {
	doctorFreezeCopyProcessNow(t)
	copyDir := t.TempDir()
	inside := filepath.Join(copyDir, "nested")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	sibling := filepath.Join(filepath.Dir(copyDir), filepath.Base(copyDir)+"-2")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		proc  platform.OrphanProc
		err   error
		rows  []core.CopyRow
		write bool
	}{
		{name: "young", proc: doctorCopyProcess(400101, "go", inside, 29*time.Minute), rows: []core.CopyRow{doctorCopyProcessRow(copyDir)}, write: true},
		{name: "exactly 30 minutes", proc: doctorCopyProcess(400102, "go", inside, 30*time.Minute), rows: []core.CopyRow{doctorCopyProcessRow(copyDir)}, write: true},
		{name: "outside copies", proc: doctorCopyProcess(400103, "go", other, 45*time.Minute), rows: []core.CopyRow{doctorCopyProcessRow(copyDir)}, write: true},
		{name: "unregistered directory", proc: doctorCopyProcess(400104, "go", sibling, 45*time.Minute), rows: []core.CopyRow{doctorCopyProcessRow(copyDir)}, write: true},
		{name: "unreadable cwd", proc: doctorCopyProcess(400105, "go", "", 45*time.Minute), rows: []core.CopyRow{doctorCopyProcessRow(copyDir)}, write: true},
		{name: "no registry", proc: doctorCopyProcess(400106, "go", inside, 45*time.Minute)},
		{name: "empty registry", proc: doctorCopyProcess(400107, "go", inside, 45*time.Minute), rows: []core.CopyRow{}, write: true},
		{name: "unreadable scan", proc: doctorCopyProcess(400108, "go", inside, 45*time.Minute), err: platform.ErrOrphansNotDetected, rows: []core.CopyRow{doctorCopyProcessRow(copyDir)}, write: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rows []core.CopyRow
			if tc.write {
				rows = tc.rows
			}
			env, root := doctorCopyProcessFixture(t, []platform.OrphanProc{tc.proc}, tc.err, rows)
			got := doctorCheckOutput(t, env, root)
			if strings.Contains(got, "has run inside copy ") {
				t.Fatalf("doctor warned:\n%s", got)
			}
			if !strings.Contains(got, "first_run:") {
				t.Fatalf("the doctor did not finish:\n%s", got)
			}
		})
	}
}

func TestDoctorCopyProcessSanitizesNameAndWarnsOncePerProcess(t *testing.T) {
	doctorFreezeCopyProcessNow(t)
	copyDir := t.TempDir()
	longName := "go\napi_key=secret " + strings.Repeat("x", 100)
	proc := doctorCopyProcess(400201, longName, copyDir, 98*time.Minute)
	env, root := doctorCopyProcessFixture(t, []platform.OrphanProc{proc}, nil, []core.CopyRow{
		doctorCopyProcessRow(copyDir),
		doctorCopyProcessRow(copyDir),
	})
	got := doctorCheckOutput(t, env, root)
	name := "go api_key=[redacted] " + strings.Repeat("x", 42)
	want := fmt.Sprintf("warn   process %d (%s, 1h) has run inside copy %s for 1h; stop it with: kill %d", proc.Pid, name, copyDir, proc.Pid)
	if n := strings.Count(got, want); n != 1 {
		t.Fatalf("warning count=%d, want 1 (%q):\n%s", n, want, got)
	}
	for _, leak := range []string{"\napi_key=secret", "secret", strings.Repeat("x", 51)} {
		if strings.Contains(got, leak) {
			t.Fatalf("doctor printed an unsanitized or uncut process name:\n%s", got)
		}
	}
}

func TestDoctorCopyProcessKeepsExistingWarningOrder(t *testing.T) {
	doctorFreezeCopyProcessNow(t)
	copyDir := t.TempDir()
	proc := doctorCopyProcess(400301, "go", copyDir, 45*time.Minute)
	env, root, ctx := copiesDirConfigFixture(t, "copies_dir="+copyDir+"\n", "")
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["TMPDIR"] = t.TempDir()
	old := doctorCopyProcessesScan
	doctorCopyProcessesScan = func() ([]platform.OrphanProc, error) { return []platform.OrphanProc{proc}, nil }
	t.Cleanup(func() { doctorCopyProcessesScan = old })
	state := filepath.Join(root, "state", "ws")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := core.WriteCopies(state, []core.CopyRow{doctorCopyProcessRow(copyDir)}); err != nil {
		t.Fatal(err)
	}
	got := doctorCheckLayers(t, &ctx, env, root)
	existing, added := strings.Index(got, copiesDirUserWarning), strings.Index(got, "has run inside copy ")
	if existing < 0 || added < existing {
		t.Fatalf("copy warning order changed: copies_dir=%d process=%d:\n%s", existing, added, got)
	}
	if n := strings.Count(got, copiesDirUserWarning); n != 1 {
		t.Fatalf("copies_dir warning count=%d, want 1:\n%s", n, got)
	}
}
