package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTM8FormerCLI pins the post-removal contract of the launcher
// commands toward the former herdr-soho artifacts: none of them is read,
// written or migrated; doctor names them as leftovers instead.
func TestTM8FormerCLI(t *testing.T) {
	t.Run(`config: the former user file is not read, not shown and not created`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		legacy := filepath.Join(env.Get("XDG_CONFIG_HOME"), "herdr-soho", "config")
		current := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
		tm8File(t, legacy, "# keep\nherd_label=crew\n")
		code, out, errOut := runIn(t, []string{"config"}, env, repo)
		if code != 0 || errOut != "" || strings.Contains(out, "crew") || strings.Contains(out, legacy) {
			t.Fatalf("config code=%d out=%q err=%q; want no trace of the former user file", code, out, errOut)
		}
		if _, err := os.Stat(current); !os.IsNotExist(err) {
			t.Fatalf("read created current config: %v", err)
		}
	})
	t.Run(`config: the former project file is not read and not shown`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		legacy := filepath.Join(repo, ".agents", "herdr-soho.conf")
		tm8File(t, legacy, "max_workers=5\n")
		code, out, errOut := runIn(t, []string{"config"}, env, repo)
		if code != 0 || errOut != "" || strings.Contains(out, legacy) {
			t.Fatalf("config code=%d out=%q err=%q; want no trace of the former project file", code, out, errOut)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "max_workers") && strings.Contains(line, " 5") {
				t.Fatalf("config read the former project file: %s", line)
			}
		}
	})
	t.Run(`config: the former env name is not read`, func(t *testing.T) { // JS: "config: the former env name is not read"
		env, repo := tm8CommandFixture(t)
		env["HERDR_SOHO_LAYOUT"] = "columns"
		code, out, errOut := runIn(t, []string{"config"}, env, repo)
		if code != 0 || errOut != "" {
			t.Fatalf("config code=%d out=%q err=%q; the former env name must not reach the layout", code, out, errOut)
		}
		// The value check is on the layout line: the declared-columns key
		// pane_columns is listed by config and legitimately contains the
		// word, so the full output may no longer be scanned for it.
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "layout ") && strings.Contains(line, "columns") {
				t.Fatalf("config read the former env name: %s", line)
			}
		}
	})
	t.Run(`config set --user: the former user file is not copied and is left intact`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		legacy := filepath.Join(env.Get("XDG_CONFIG_HOME"), "herdr-soho", "config")
		current := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
		old := []byte("# keep this comment\nherd_label=crew\n")
		tm8File(t, legacy, string(old))
		if err := os.Chmod(legacy, 0o640); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := runIn(t, []string{"config", "set", "layout", "tab", "--user"}, env, repo)
		got, readErr := os.ReadFile(current)
		still, oldErr := os.ReadFile(legacy)
		if code != 0 || strings.Contains(errOut, "copied") || readErr != nil || string(got) != "layout=tab\n" || oldErr != nil || string(still) != string(old) {
			t.Fatalf("set code=%d err=%q current=%q/%v legacy=%q/%v; want the plain write with the former file untouched", code, errOut, got, readErr, still, oldErr)
		}
	})
	t.Run(`doctor --fix: a former-only project is not read and not migrated`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		oldPath, newPath := filepath.Join(repo, ".agents", "herdr-soho.conf"), filepath.Join(repo, ".agents", "soho.conf")
		tm8File(t, oldPath, "panes=3\nherd_label=crew\n")
		code, _, errOut := runIn(t, []string{"doctor", "--fix"}, env, repo)
		legacy, oldErr := os.ReadFile(oldPath)
		newData, _ := os.ReadFile(newPath)
		if code != 2 || strings.Contains(errOut, "copied") || !strings.Contains(errOut, "panes is not set") || oldErr != nil || string(legacy) != "panes=3\nherd_label=crew\n" || strings.Contains(string(newData), "panes=3") {
			t.Fatalf("doctor fix code=%d new=%q legacy=%q/%v err=%q; the former file must stay unread and intact", code, newData, legacy, oldErr, errOut)
		}
	})
	t.Run(`setup: the former project file is moved to the soho name before it is read`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		old := filepath.Join(repo, ".agents", "herdr-soho.conf")
		newPath := filepath.Join(repo, ".agents", "soho.conf")
		tm8File(t, old, "lane.build.kind=codex\n")
		code, out, errOut := runIn(t, []string{"setup", "--no-hooks"}, env, repo)
		if code != 0 || !strings.Contains(out, "migrate: moved "+old+" to "+newPath) {
			t.Fatalf("setup with former team code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Lstat(old); !os.IsNotExist(err) {
			t.Fatalf("the former project file is still there: %v", err)
		}
		newData, _ := os.ReadFile(newPath)
		if !strings.Contains(string(newData), "lane.build.kind=codex") {
			t.Fatalf("the moved project config lost its team choice: %q", newData)
		}
	})
	t.Run(`config set: a symlinked former config is not read through the link`, func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require elevated privileges on Windows")
		}
		env, repo := tm8CommandFixture(t)
		legacy := filepath.Join(env.Get("XDG_CONFIG_HOME"), "herdr-soho", "config")
		current := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
		target := filepath.Join(filepath.Dir(legacy), "target")
		tm8File(t, target, "herd_label=crew\n")
		if err := os.Symlink(target, legacy); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := runIn(t, []string{"config", "set", "layout", "tab", "--user"}, env, repo)
		link, linkErr := os.Readlink(legacy)
		data, readErr := os.ReadFile(current)
		if code != 0 || strings.Contains(errOut, "copied") || linkErr != nil || link != target || readErr != nil || string(data) != "layout=tab\n" {
			t.Fatalf("set code=%d err=%q link=%q/%v data=%q/%v; want the plain write without reading the former file", code, errOut, link, linkErr, data, readErr)
		}
	})
	t.Run(`config: the default state dir is the soho directory`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		if err := os.MkdirAll(filepath.Join(repo, ".herdr-soho"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runIn(t, []string{"config"}, env, repo)
		if code != 0 || errOut != "" || !strings.Contains(out, "state_dir") || strings.Contains(out, ".herdr-soho") {
			t.Fatalf("config code=%d out=%q err=%q; the former state dir must not appear", code, out, errOut)
		}
	})
	t.Run(`doctor: without any former artifact nothing about them is printed`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		code, out, errOut := runIn(t, []string{"doctor"}, env, repo)
		for _, phrase := range []string{"is no longer read", "is no longer used"} {
			if strings.Contains(out+errOut, phrase) {
				t.Fatalf("unexpected %q in doctor output: %q %q", phrase, out, errOut)
			}
		}
		if code != 0 {
			t.Fatalf("doctor code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run(`doctor: the former env variable is named as a leftover`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		env["HERDR_SOHO_LAYOUT"] = "columns"
		code, out, errOut := runIn(t, []string{"doctor"}, env, repo)
		want := "environment variable HERDR_SOHO_LAYOUT is no longer read; rename it to SOHO_LAYOUT"
		if code != 0 || errOut != "" || !strings.Contains(out, want) {
			t.Fatalf("doctor code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run(`doctor: the former user config dir is named as a leftover with the move destination`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		oldDir := filepath.Join(env.Get("XDG_CONFIG_HOME"), "herdr-soho")
		newDir := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho")
		tm8File(t, filepath.Join(oldDir, "config"), "herd_label=crew\n")
		code, out, errOut := runIn(t, []string{"doctor"}, env, repo)
		if code != 0 || errOut != "" || !strings.Contains(out, "former user config dir "+oldDir+" is still there: run 'soho setup' to move it to "+newDir) {
			t.Fatalf("doctor code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run(`doctor: the former project config is named as a leftover with the move destination`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		oldPath, newPath := filepath.Join(repo, ".agents", "herdr-soho.conf"), filepath.Join(repo, ".agents", "soho.conf")
		tm8File(t, oldPath, "max_workers=5\n")
		code, out, errOut := runIn(t, []string{"doctor"}, env, repo)
		if code != 0 || errOut != "" || !strings.Contains(out, "former project config "+oldPath+" is no longer read: run 'soho setup' to move it to "+newPath) {
			t.Fatalf("doctor code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run(`doctor: the former state dir is named as a leftover when nothing pins the state dir`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		delete(env, "SOHO_DIR")
		oldPath := filepath.Join(repo, ".herdr-soho")
		if err := os.MkdirAll(oldPath, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runIn(t, []string{"doctor"}, env, repo)
		if code != 0 || errOut != "" || !strings.Contains(out, "former state dir "+oldPath+" is no longer used: run 'soho setup' to move it to "+filepath.Join(repo, ".soho")+" once no worker is live") {
			t.Fatalf("doctor code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run(`setup --plan: the before side does not seed from the former project file`, func(t *testing.T) {
		env, repo := tm8CommandFixture(t)
		tm8File(t, filepath.Join(repo, ".agents", "herdr-soho.conf"), "lane.build.roles=scouter,implementer\n")
		code, out, errOut := runIn(t, []string{"setup", "--plan", "--panes", "4"}, env, repo)
		if code != 0 || strings.Contains(out, "scouter,implementer") || strings.Contains(errOut, "copied") {
			t.Fatalf("setup plan code=%d out=%q err=%q; the former file must not seed the plan", code, out, errOut)
		}
		if _, err := os.Stat(filepath.Join(repo, ".agents", "soho.conf")); !os.IsNotExist(err) {
			t.Fatalf("setup plan wrote new config: %v", err)
		}
	})
}
