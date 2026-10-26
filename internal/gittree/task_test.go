package gittree

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.Main(); os.Exit(m.Run()) }

func audit338Fixture(t *testing.T, rules ...fakecli.Rule) (string, platform.Env, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "task.dispatch.json")
	bin := filepath.Join(root, "bin")
	old, next := strings.Repeat("a", 40), strings.Repeat("b", 40)
	if err := core.WriteScratchFile(path, jsonjs.Stringify(jsonjs.O("git_head", old, "git_branch", "main", "git_tree", root))); err != nil {
		t.Fatal(err)
	}
	if len(rules) == 0 {
		rules = []fakecli.Rule{
			{Argv: []string{"-C", root, "rev-parse", "HEAD"}, Stdout: next},
			{Argv: []string{"-C", root, "symbolic-ref", "-q", "--short", "HEAD"}, Stdout: "main"},
			{AnyArgs: true},
		}
	}
	if _, err := fakecli.Install(t, bin, "git", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, item := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if k, v, ok := strings.Cut(item, "="); ok {
			env[k] = v
		}
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		env[key] = root
	}
	env["PATH"] = bin
	var stderr bytes.Buffer
	saved := platform.Stderr
	platform.Stderr = &stderr
	t.Cleanup(func() { platform.Stderr = saved })
	return path, env, &stderr
}

func audit338Calls(t *testing.T, path string) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(path), "bin", "git.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return len(calls)
}

func TestAudit338EightConcurrentChecks(t *testing.T) {
	path, env, stderr := audit338Fixture(t)
	start := make(chan struct{})
	results := make(chan *jsonjs.Object, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- Check(path, "worker", env, filepath.Join(filepath.Dir(path), "friction.log"), "collect")
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	moved := 0
	for result := range results {
		if result != nil {
			moved++
		}
	}
	if count := strings.Count(stderr.String(), "left a local commit"); count != 1 || moved != 1 || audit338Calls(t, path) != 3 {
		t.Fatalf("warnings=%d movements=%d git_calls=%d stderr=%s", count, moved, audit338Calls(t, path), stderr.String())
	}
	if checked, _ := read(path).Get("git_checked"); checked != true {
		t.Fatal("missing readable checked record")
	}
	info, err := os.Stat(path + ".git-checked")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("marker mode=%v", info.Mode())
	}
	if Check(path, "worker", env, "", "collect") != nil || strings.Count(stderr.String(), "left a local commit") != 1 || audit338Calls(t, path) != 3 {
		t.Fatal("repeat check was not silent")
	}
	t.Logf("8 concurrent checks: warnings=1 movements=1 git_calls=3 marker=%s", path+".git-checked")
}

func TestAudit338ExistingMarkerSilent(t *testing.T) {
	for _, kind := range []string{"file", "link", "directory", "denied"} {
		t.Run(kind, func(t *testing.T) {
			path, env, stderr := audit338Fixture(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			marker := path + ".git-checked"
			target := filepath.Join(filepath.Dir(path), "target")
			switch kind {
			case "file":
				err = os.WriteFile(marker, []byte("keep marker"), 0600)
			case "link":
				if runtime.GOOS == "windows" {
					t.Skip("symlink requires developer mode")
				}
				if err = os.WriteFile(target, []byte("keep target"), 0600); err == nil {
					err = os.Symlink(target, marker)
				}
			case "directory":
				err = os.Mkdir(marker, 0700)
			case "denied":
				if runtime.GOOS == "windows" {
					t.Skip("Unix directory permissions")
				}
				err = os.Chmod(filepath.Dir(path), 0500)
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0700) })
			}
			if err != nil {
				t.Fatal(err)
			}
			if Check(path, "worker", env, "", "collect") != nil || stderr.Len() != 0 || audit338Calls(t, path) != 0 {
				t.Fatalf("not silent: %s calls=%d", stderr.String(), audit338Calls(t, path))
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("sidecar changed: %v", err)
			}
			if kind == "link" {
				body, err := os.ReadFile(target)
				if err != nil || string(body) != "keep target" {
					t.Fatalf("link target=%q err=%v", body, err)
				}
				if info, err := os.Lstat(marker); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("marker link replaced")
				}
			}
			if kind == "file" {
				body, err := os.ReadFile(marker)
				if err != nil || string(body) != "keep marker" {
					t.Fatal("marker changed")
				}
			}
		})
	}
}

func TestAudit338LegacyAndEmptySidecar(t *testing.T) {
	path, env, stderr := audit338Fixture(t)
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", path} {
		if Check(p, "worker", env, "", "collect") != nil {
			t.Fatal("legacy check returned movement")
		}
	}
	if stderr.Len() != 0 || audit338Calls(t, path) != 0 {
		t.Fatal("legacy check queried git or warned")
	}
	if _, err := os.Lstat(path + ".git-checked"); !os.IsNotExist(err) {
		t.Fatalf("legacy marker created: %v", err)
	}
}

func TestAudit338FailedGitKeepsPermanentMarker(t *testing.T) {
	path, env, stderr := audit338Fixture(t, fakecli.Rule{AnyArgs: true, Code: 1})
	for i := 0; i < 2; i++ {
		if Check(path, "worker", env, "", "collect") != nil {
			t.Fatal("failed git returned movement")
		}
	}
	if stderr.Len() != 0 || audit338Calls(t, path) != 1 {
		t.Fatalf("failed git retried: calls=%d stderr=%s", audit338Calls(t, path), stderr.String())
	}
	if _, err := os.Stat(path + ".git-checked"); err != nil {
		t.Fatal(err)
	}
	if checked, _ := read(path).Get("git_checked"); checked == true {
		t.Fatal("failed git wrote success record")
	}
}

// Only Check writes git_checked, so a sidecar that carries it records a
// comparison already made, here or in the scratch sidecar wait mirrored it
// from: no git call, no warning, no marker.
func TestCheckSkipsARecordedComparison(t *testing.T) {
	path, env, stderr := audit338Fixture(t)
	o := read(path)
	o.Set("git_checked", true)
	if err := core.WriteScratchFile(path, jsonjs.Stringify(o)); err != nil {
		t.Fatal(err)
	}
	if Check(path, "worker", env, "", "collect") != nil || stderr.Len() != 0 || audit338Calls(t, path) != 0 {
		t.Fatalf("a recorded comparison ran again: calls=%d stderr=%s", audit338Calls(t, path), stderr.String())
	}
	if _, err := os.Stat(path + ".git-checked"); !os.IsNotExist(err) {
		t.Fatalf("marker created for a recorded comparison: %v", err)
	}
}
