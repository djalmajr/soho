package doctor

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestDoctorCopiesDirWorldWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits required")
	}
	env, root := paneContextFixture(t, doctorFootprintRules())
	tmp := t.TempDir()
	if err := os.Chmod(tmp, os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0700) })
	env["TMPDIR"] = tmp
	want := fmt.Sprintf("TMPDIR %s is world-writable (mode 1777): mutation copies there can fail a project's trusted-path checks; set copies_dir to a private directory, for example ~/.cache/soho/copies", tmp)
	got := doctorCheckOutput(t, env, root)
	if strings.Count(got, want) != 1 {
		t.Fatalf("warning missing or duplicated: %s", got)
	}
	env["SOHO_COPIES_DIR"] = t.TempDir()
	if got = doctorCheckOutput(t, env, root); strings.Contains(got, "is world-writable") {
		t.Fatalf("configured dir warned: %s", got)
	}
	delete(env, "SOHO_COPIES_DIR")
	if err := os.Chmod(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	if got = doctorCheckOutput(t, env, root); strings.Contains(got, "is world-writable") {
		t.Fatalf("private tmp warned: %s", got)
	}
}

// An unset TMPDIR falls back to the system default (/tmp on Linux, mode
// 1777): that is every Linux user's normal setup, so it stays quiet even
// when the default directory is world-writable.
func TestDoctorCopiesDirUnsetTMPDIRIsQuiet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits required")
	}
	env, root := paneContextFixture(t, doctorFootprintRules())
	shared := t.TempDir()
	if err := os.Chmod(shared, os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shared, 0700) })
	// The process default (os.TempDir) points at the world-writable
	// directory; the doctor's environment names no TMPDIR.
	t.Setenv("TMPDIR", shared)
	delete(env, "TMPDIR")
	if got := doctorCheckOutput(t, env, root); strings.Contains(got, "is world-writable") {
		t.Fatalf("an unset TMPDIR warned: %s", got)
	}
}
