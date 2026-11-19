package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
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

// copies_dir is a machine setting: copies land outside the repository, so a
// value that only the project file sets dies with the checkout. The doctor
// says so once, and only then (SOH-448).
const copiesDirUserWarning = "warn   copies_dir is a machine setting; set it with --user so workers in other repositories use it too (soho config set copies_dir <path> --user)"

// copiesDirConfigFixture writes the project and user config files (an empty
// string leaves that file out) and returns the config loaded from them, so
// the test exercises the same layers `soho doctor` reads.
func copiesDirConfigFixture(t *testing.T, project, user string) (platform.Env, string, core.Config) {
	t.Helper()
	env, root := paneContextFixture(t, doctorFootprintRules())
	// The user file is the fixture's own: never the machine's.
	env["XDG_CONFIG_HOME"] = filepath.Join(root, "xdg")
	delete(env, "TMPDIR")
	delete(env, "SOHO_COPIES_DIR")
	if project != "" {
		dir := filepath.Join(root, ".agents")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "soho.conf"), []byte(project), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if user != "" {
		file := core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{})
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(user), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if file := core.ProjectConfigFileUsed(env, root); project != "" && !strings.HasSuffix(file, filepath.Join(".agents", "soho.conf")) {
		t.Fatalf("project layer reads %q, not the fixture's file", file)
	}
	if file := core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{}); user != "" {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("user layer reads %q: %v", file, err)
		}
	}
	return env, root, core.LoadConfig(env, root)
}

func doctorCheckLayers(t *testing.T, ctx *core.Config, env platform.Env, root string) string {
	t.Helper()
	var out strings.Builder
	DoctorCheck(ctx, env, root, &out)
	return out.String()
}

func TestDoctorCopiesDirProjectOnlyWarnsToUseUser(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "copies")
	env, root, ctx := copiesDirConfigFixture(t, "copies_dir="+dir+"\n", "")
	got := doctorCheckLayers(t, &ctx, env, root)
	if n := strings.Count(got, copiesDirUserWarning); n != 1 {
		t.Fatalf("warning count=%d, want 1: %s", n, got)
	}
}

func TestDoctorCopiesDirUserOnlyIsSilent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "copies")
	env, root, ctx := copiesDirConfigFixture(t, "", "copies_dir="+dir+"\n")
	if got := doctorCheckLayers(t, &ctx, env, root); strings.Contains(got, copiesDirUserWarning) {
		t.Fatalf("a user-file copies_dir warned: %s", got)
	}
}

// The user file already carries the machine-wide setting, so the project file
// repeating it needs no advice: this is the case the mutation turns red.
func TestDoctorCopiesDirInBothFilesIsSilent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "copies")
	env, root, ctx := copiesDirConfigFixture(t, "copies_dir="+dir+"\n", "copies_dir="+dir+"\n")
	if got := doctorCheckLayers(t, &ctx, env, root); strings.Contains(got, copiesDirUserWarning) {
		t.Fatalf("both files set copies_dir and the doctor warned: %s", got)
	}
}

func TestDoctorCopiesDirNeitherFileIsSilent(t *testing.T) {
	env, root, ctx := copiesDirConfigFixture(t, "", "")
	if got := doctorCheckLayers(t, &ctx, env, root); strings.Contains(got, copiesDirUserWarning) {
		t.Fatalf("no file sets copies_dir and the doctor warned: %s", got)
	}
}

// An empty value is not a setting: config resolves past it, so a project file
// carrying `copies_dir=` changes nothing and earns no advice.
func TestDoctorCopiesDirEmptyProjectValueIsSilent(t *testing.T) {
	env, root, ctx := copiesDirConfigFixture(t, "copies_dir=\n", "")
	if got := doctorCheckLayers(t, &ctx, env, root); strings.Contains(got, copiesDirUserWarning) {
		t.Fatalf("an empty project value warned: %s", got)
	}
}

// The same emptiness in the user file does not make that file the machine-wide
// setting: the effective value still comes from the project file, so the
// advice stands.
func TestDoctorCopiesDirEmptyUserValueStillWarns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "copies")
	env, root, ctx := copiesDirConfigFixture(t, "copies_dir="+dir+"\n", "copies_dir=\n")
	got := doctorCheckLayers(t, &ctx, env, root)
	if n := strings.Count(got, copiesDirUserWarning); n != 1 {
		t.Fatalf("warning count=%d, want 1: %s", n, got)
	}
}

// A session-layer value and no project file: the check reads file layers only,
// so a transient session setting never produces the advice.
func TestDoctorCopiesDirSessionLayerOnlyIsSilent(t *testing.T) {
	env, root, ctx := copiesDirConfigFixture(t, "", "")
	ctx.Layers = append(ctx.Layers, core.ConfigLayer{Source: "session", Entries: map[string]core.ConfigEntry{
		"copies_dir": {Value: filepath.Join(root, "copies"), Source: "session"},
	}, OriginalKeys: []string{"copies_dir"}})
	if got := doctorCheckLayers(t, &ctx, env, root); strings.Contains(got, copiesDirUserWarning) {
		t.Fatalf("a session-only copies_dir warned: %s", got)
	}
}

func TestDoctorCopiesDirEnvironmentSatisfiesTheAdvice(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "copies")
	env, root, ctx := copiesDirConfigFixture(t, "copies_dir="+dir+"\n", "")
	env["SOHO_COPIES_DIR"] = dir
	if got := doctorCheckLayers(t, &ctx, env, root); strings.Contains(got, copiesDirUserWarning) {
		t.Fatalf("SOHO_COPIES_DIR in the environment still warned: %s", got)
	}
}
