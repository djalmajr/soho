package doctor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorCopiesPressureListing(t *testing.T) {
	env, root := paneContextFixture(t, doctorFootprintRules())
	parent := t.TempDir()
	tmp := t.TempDir()
	env["TMPDIR"] = tmp
	env["SOHO_COPIES_DIR"] = parent
	env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "15"
	env["SOHO_PRESSURE_DISK_FREE_MIN_GB"] = "0"
	oldDisk, oldDevice := platform.DiskFree, platform.DiskDevice
	t.Cleanup(func() { platform.DiskFree, platform.DiskDevice = oldDisk, oldDevice })
	platform.DiskDevice = func(path string) (string, bool) {
		if strings.HasPrefix(path, parent) || strings.HasPrefix(path, tmp) {
			return "copy", true
		}
		return "state", true
	}
	platform.DiskFree = func(path string) (int64, int64, bool) {
		if path == parent {
			return 1, 100, true
		}
		return 50, 100, true
	}
	env["HERDR_WORKSPACE_ID"] = "ws"
	project, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, env["PATH"], "git", []fakecli.Rule{{AnyArgs: true, Stdout: project + "\n"}}); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 6; i++ {
		p := filepath.Join(parent, fmt.Sprintf("soho-mutation-%d", i))
		payload := p
		if i == 6 {
			payload = filepath.Join(p, "group", "worktree")
		}
		if err := os.MkdirAll(payload, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(payload, "data"), []byte(strings.Repeat("x", i)), 0600); err != nil {
			t.Fatal(err)
		}
		markerProject := project
		if i != 6 {
			markerProject = "other"
		}
		if err := os.WriteFile(p+".soho-origin", []byte("project="+markerProject+"\nsource=/source\ncreated=now\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	otherRootCopy := filepath.Join(tmp, "soho-mutation-largest")
	if err := os.Mkdir(otherRootCopy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherRootCopy, "data"), []byte("1234567"), 0600); err != nil {
		t.Fatal(err)
	}
	registered := filepath.Join(parent, "soho-mutation-registered")
	if err := os.Mkdir(registered, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.CopiesFile(state), []byte(registered+"\towner\tpane\tcreated\tsource\torigin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := doctorCheckOutput(t, env, root)
	if !strings.Contains(out, "disk "+parent+" (mutation copies) has 1% free") {
		t.Fatalf("missing warning: %s", out)
	}
	last := strings.Index(out, otherRootCopy)
	if last < 0 {
		t.Fatalf("missing largest copy in TMPDIR: %s", out)
	}
	for i := 6; i >= 3; i-- {
		path := filepath.Join(parent, fmt.Sprintf("soho-mutation-%d", i))
		idx := strings.Index(out, path)
		if idx < 0 || idx <= last {
			t.Fatalf("missing or unordered %s: %s", path, out)
		}
		last = idx
	}
	if strings.Contains(out, registered) || strings.Contains(out, filepath.Join(parent, "soho-mutation-1")) || strings.Contains(out, filepath.Join(parent, "soho-mutation-2")) || !strings.Contains(out, "6 B  "+filepath.Join(parent, "soho-mutation-6")+"  this project") || !strings.Contains(out, "other project") {
		t.Fatalf("incorrect entries: %s", out)
	}
	if strings.Contains(out, filepath.Join("group", "worktree")) {
		t.Fatalf("doctor listed the nested worktree instead of the lifecycle root: %s", out)
	}
}

func TestDoctorCopiesPressureSizeDeadline(t *testing.T) {
	env, root := paneContextFixture(t, doctorFootprintRules())
	parent := t.TempDir()
	env["TMPDIR"] = parent
	env["SOHO_COPIES_DIR"] = parent
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "15"
	env["SOHO_PRESSURE_DISK_FREE_MIN_GB"] = "0"
	for _, name := range []string{"soho-mutation-a", "soho-mutation-b"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	oldDisk, oldDevice, oldSize, oldTimeout := platform.DiskFree, platform.DiskDevice, doctorCopiesSize, doctorCopiesSizeTimeout
	t.Cleanup(func() {
		platform.DiskFree, platform.DiskDevice, doctorCopiesSize, doctorCopiesSizeTimeout = oldDisk, oldDevice, oldSize, oldTimeout
	})
	platform.DiskFree = func(path string) (int64, int64, bool) {
		if path == parent {
			return 1, 100, true
		}
		return 50, 100, true
	}
	platform.DiskDevice = func(string) (string, bool) { return "copy", true }
	doctorCopiesSizeTimeout = 10 * time.Millisecond
	doctorCopiesSize = func(ctx context.Context, _ string) (int64, bool) { <-ctx.Done(); return 0, false }
	out := doctorCheckOutput(t, env, root)
	if strings.Count(out, "size unknown") != 2 {
		t.Fatalf("deadline should leave both sizes unknown: %s", out)
	}
}

func TestDoctorCopiesPressurePendingRegistration(t *testing.T) {
	env, cwd := paneContextFixture(t, doctorFootprintRules())
	parent := t.TempDir()
	env["TMPDIR"], env["SOHO_COPIES_DIR"], env["HERDR_WORKSPACE_ID"] = parent, parent, "ws"
	env["SOHO_PRESSURE_DISK_FREE_MIN_GB"] = "0"
	queued := filepath.Join(parent, "soho-mutation-queued")
	control := filepath.Join(parent, "soho-mutation-control")
	for _, p := range []string{queued, control} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(env["SOHO_DIR"], "ws")
	if _, err := core.QueueCopy(env, "ws", state, core.CopyRow{Path: queued, Owner: "live", Pane: "pane", Created: "now", Source: "source", Origin: "mutation-copy"}); err != nil {
		t.Fatal(err)
	}
	if len(core.PendingCopiesFor(state, "ws", env)) != 1 {
		t.Fatal("queue fixture invalid")
	}
	oldDisk, oldDevice := platform.DiskFree, platform.DiskDevice
	t.Cleanup(func() { platform.DiskFree, platform.DiskDevice = oldDisk, oldDevice })
	platform.DiskFree = func(string) (int64, int64, bool) { return 1, 100, true }
	platform.DiskDevice = func(string) (string, bool) { return "copy", true }
	cfg := core.LoadConfig(env, cwd)
	var out bytes.Buffer
	doctorCopiesPressure(&cfg, env, cwd, &Say{Out: &out})
	if strings.Contains(out.String(), queued) || !strings.Contains(out.String(), control) {
		t.Fatalf("incorrect listing: %s", out.String())
	}
	if len(core.PendingCopiesFor(state, "ws", env)) != 1 {
		t.Fatal("doctor consumed queue")
	}
}

func TestDoctorCopiesPressureUnreadableSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod fixture requires Unix permissions")
	}
	env, cwd := paneContextFixture(t, doctorFootprintRules())
	parent := t.TempDir()
	env["TMPDIR"], env["SOHO_COPIES_DIR"], env["HERDR_WORKSPACE_ID"] = parent, parent, "ws"
	env["SOHO_PRESSURE_DISK_FREE_MIN_GB"] = "0"
	unknown := filepath.Join(parent, "soho-mutation-unknown")
	known := filepath.Join(parent, "soho-mutation-known")
	child := filepath.Join(unknown, "private")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(known, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "data"), []byte("hidden bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(known, "data"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(child, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(child, 0700) })
	if _, err := os.ReadDir(child); err == nil {
		t.Skip("fixture is readable with current privileges")
	}
	oldDisk, oldDevice := platform.DiskFree, platform.DiskDevice
	t.Cleanup(func() { platform.DiskFree, platform.DiskDevice = oldDisk, oldDevice })
	platform.DiskFree = func(string) (int64, int64, bool) { return 1, 100, true }
	platform.DiskDevice = func(string) (string, bool) { return "copy", true }
	cfg := core.LoadConfig(env, cwd)
	var out bytes.Buffer
	doctorCopiesPressure(&cfg, env, cwd, &Say{Out: &out})
	a, b := strings.Index(out.String(), "1 B  "+known), strings.Index(out.String(), "size unknown  "+unknown)
	if a < 0 || b <= a {
		t.Fatalf("incorrect sizes or order: %s", out.String())
	}
}
