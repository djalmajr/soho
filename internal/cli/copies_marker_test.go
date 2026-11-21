package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

func TestMutationCopyDestDiscoveryWarning(t *testing.T) {
	for _, tc := range []struct {
		name           string
		parent         string
		base           string
		pathMode       bool
		warn           bool
		tempRootBreaks bool
	}{
		{name: "prefixed outside roots", parent: "nested", base: "soho-mutation-x", warn: true},
		{name: "nonprefixed under root", base: "review copy", pathMode: true, warn: true},
		{name: "prefixed under temp root", base: "soho-mutation-x", pathMode: true},
		{name: "prefixed under configured root", parent: "configured", base: "soho-mutation-x"},
		{name: "line breaks in temp root", parent: "configured", base: "review copy", tempRootBreaks: true, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.tempRootBreaks && runtime.GOOS == "windows" {
				t.Skip("Windows file names cannot contain CR or LF")
			}
			f := newCopiesFixture(t, nil)
			f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
			f.cwd = f.worktree
			f.env["SOHO_COPIES_DIR"] = "~/configured copies"
			configured := filepath.Join(f.home, "configured copies")
			shownTmp := f.tmp
			if tc.tempRootBreaks {
				f.env["TMPDIR"] = filepath.Join(f.root, "temp\r\nroot")
				if err := os.Mkdir(f.env["TMPDIR"], 0o700); err != nil {
					t.Fatal(err)
				}
				shownTmp = filepath.Join(f.root, "temp\\r\\nroot")
			}
			parent := f.tmp
			switch tc.parent {
			case "nested":
				parent = filepath.Join(f.tmp, "outside roots")
			case "configured":
				parent = configured
			}
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(parent, tc.base)
			marker := dest + ".soho-origin"
			if tc.parent == "nested" {
				if err := os.WriteFile(marker, []byte("unrelated sibling contents\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			args := []string{"mutation-copy", "--dest", dest}
			wantOut := fmt.Sprintf("{\"copy\":%q,\"source\":%q,\"files\":0,\"excluded\":0,\"links\":[]}\n", dest, f.worktree)
			if tc.pathMode {
				args = append(args, "--print-path")
				wantOut = dest + "\n"
			}
			wantWarning := ""
			if tc.warn {
				wantWarning = "mutation-copy: gc does not find --dest copies if their registry row is lost (it looks for soho-mutation-* directly under " +
					shownTmp + ", " + configured + "); release or remove " + dest + " yourself, or drop --dest to let soho place it\n"
			}
			code, out, stderr := f.run(t, f.env, args...)
			if code != 0 || out != wantOut || stderr != wantWarning {
				t.Fatalf("argv=%q exit=%d stdout=%q stderr=%q; want exit 0 stdout=%q stderr=%q", args, code, out, stderr, wantOut, wantWarning)
			}
			if tc.parent == "nested" {
				if data, err := os.ReadFile(marker); err != nil || string(data) != "unrelated sibling contents\n" {
					t.Fatalf("existing sibling marker changed: bytes=%q err=%v", data, err)
				}
			} else if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Fatalf("explicit destination should have no new marker: %v", err)
			}
			if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != dest || rows[0].Owner != "worker" {
				t.Fatalf("registry rows=%+v; want worker's copy %q", rows, dest)
			}
		})
	}
}

func TestMutationCopyDestWarningUsesCallerConfig(t *testing.T) {
	for _, mode := range []string{"caller root", "source-only root", "shared environment root", "automatic placement"} {
		t.Run(mode, func(t *testing.T) {
			f := gcEmptyFixture(t)
			f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
			source := filepath.Join(f.root, "other source")
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			f.git(t, source, "init", "-q")
			f.git(t, source, "commit", "-q", "--allow-empty", "-m", "fixture")
			callerRoot := filepath.Join(f.root, "caller copies")
			sourceRoot := filepath.Join(f.root, "source copies")
			for repo, root := range map[string]string{f.repo: callerRoot, source: sourceRoot} {
				writeConfigFile(t, filepath.Join(repo, ".agents", "soho.conf"), "copies_dir="+root+"\n")
			}

			switch mode {
			case "automatic placement":
				code, out, stderr := f.run(t, f.env, "mutation-copy", "--source", source, "--print-path")
				dest := strings.TrimSuffix(out, "\n")
				if code != 0 || stderr != "" || filepath.Dir(dest) != sourceRoot ||
					!strings.HasPrefix(filepath.Base(dest), "soho-mutation-") {
					t.Fatalf("automatic placement: exit=%d stdout=%q stderr=%q; want a copy under %q", code, out, stderr, sourceRoot)
				}
				if got, want := markerProject(t, dest), gitCommonDirOf(t, source, f.env); got != want {
					t.Fatalf("automatic marker project=%q; want %q", got, want)
				}
			case "caller root":
				dest := filepath.Join(callerRoot, "soho-mutation-x")
				checkExplicitCopyDiscovery(t, f, source, dest, dest, f.tmp, callerRoot)
			case "source-only root":
				dest := filepath.Join(sourceRoot, "soho-mutation-x")
				checkExplicitCopyDiscovery(t, f, source, dest, "", f.tmp, callerRoot)
			case "shared environment root":
				root := filepath.Join(f.root, "environment copies")
				f.env["SOHO_COPIES_DIR"] = root
				dest := filepath.Join(root, "soho-mutation-x")
				checkExplicitCopyDiscovery(t, f, source, dest, dest, f.tmp, root)
			}
		})
	}
}

func TestMutationCopyDestWarningUsesEntryName(t *testing.T) {
	for _, tc := range []struct {
		diskName, alias, listed string
	}{
		{diskName: "SOHO-MUTATION-x", alias: "soho-mutation-x"},
		{diskName: "soho-mutation-y", alias: "SOHO-MUTATION-y", listed: "soho-mutation-y"},
	} {
		t.Run(tc.diskName, func(t *testing.T) {
			f := gcEmptyFixture(t)
			f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
			onDisk := filepath.Join(f.tmp, tc.diskName)
			if err := os.Mkdir(onDisk, 0o700); err != nil {
				t.Fatal(err)
			}
			request := filepath.Join(f.tmp, tc.alias)
			diskInfo, err := os.Lstat(onDisk)
			if err != nil {
				t.Fatal(err)
			}
			aliasInfo, err := os.Lstat(request)
			if os.IsNotExist(err) {
				t.Skipf("filesystem does not resolve %q as %q", request, onDisk)
			}
			if err != nil || !os.SameFile(diskInfo, aliasInfo) {
				t.Fatalf("case alias %q does not identify %q: %v", request, onDisk, err)
			}
			var scanPath string
			if tc.listed != "" {
				scanPath = filepath.Join(f.tmp, tc.listed)
			}
			checkExplicitCopyDiscovery(t, f, f.repo, request, scanPath, f.tmp)
		})
	}
}

func TestMutationCopyDestWarningRootAndCopyAliases(t *testing.T) {
	for _, tc := range []struct {
		name, diskName, aliasName, listedName  string
		targetOutside, aliasOutside, useTarget bool
	}{
		{name: "root alias", diskName: "soho-mutation-x", listedName: "soho-mutation-x"},
		{name: "nonprefixed symlink to prefixed entry", diskName: "soho-mutation-x", aliasName: "review copy", listedName: "soho-mutation-x"},
		{name: "prefixed symlink outside root", diskName: "review copy", aliasName: "soho-mutation-x", aliasOutside: true},
		{name: "prefixed symlink in root", diskName: "review copy", aliasName: "soho-mutation-x", listedName: "soho-mutation-x"},
		{name: "prefixed symlink to outside target", diskName: "review copy", aliasName: "soho-mutation-x", listedName: "soho-mutation-x", targetOutside: true},
		{name: "prefixed symlink not supplied as dest", diskName: "review copy", aliasName: "soho-mutation-x", listedName: "soho-mutation-x", useTarget: true},
		{name: "nonprefixed symlink in root", diskName: "review copy", aliasName: "review alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := gcEmptyFixture(t)
			f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
			targetParent := f.tmp
			if tc.targetOutside {
				targetParent = f.root
			}
			real := filepath.Join(targetParent, tc.diskName)
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			root := f.tmp
			request := real
			if tc.aliasName == "" {
				root = filepath.Join(f.root, "root alias")
				if err := os.Symlink(f.tmp, root); err != nil {
					t.Skipf("root symlink unavailable: %v", err)
				}
				f.env["TMPDIR"] = root
			} else {
				aliasParent := f.tmp
				if tc.aliasOutside {
					aliasParent = f.root
				}
				alias := filepath.Join(aliasParent, tc.aliasName)
				if err := os.Symlink(real, alias); err != nil {
					t.Skipf("destination symlink unavailable: %v", err)
				}
				if !tc.useTarget {
					request = alias
				}
			}
			if tc.name == "nonprefixed symlink in root" {
				dangling := filepath.Join(root, "soho-mutation-dangling")
				if err := os.Symlink(filepath.Join(f.root, "absent"), dangling); err != nil {
					t.Fatal(err)
				}
			}
			var scanPath string
			if tc.listedName != "" {
				scanPath = filepath.Join(root, tc.listedName)
			}
			checkExplicitCopyDiscovery(t, f, f.repo, request, scanPath, root)
		})
	}
}

func TestMutationCopyDestWarningUnreadableScanRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX mode bits")
	}
	f := gcEmptyFixture(t)
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	dest := filepath.Join(f.tmp, "soho-mutation-x")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.tmp, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(f.tmp, 0o700); err != nil {
			t.Errorf("restore permissions on %q: %v", f.tmp, err)
		}
	})
	if _, err := os.ReadDir(f.tmp); !os.IsPermission(err) {
		t.Skipf("mode 0300 did not block listing: %v", err)
	}
	checkExplicitCopyDiscovery(t, f, f.repo, dest, "", f.tmp)
}

func checkExplicitCopyDiscovery(t *testing.T, f *copiesFixture, source, request, scanPath string, roots ...string) {
	t.Helper()
	code, out, stderr := f.run(t, f.env, "mutation-copy", "--source", source, "--dest", request, "--print-path")
	resolved, err := filepath.EvalSymlinks(request)
	if err != nil || code != 0 || out != resolved+"\n" {
		t.Fatalf("request=%q exit=%d stdout=%q stderr=%q; resolved=%q err=%v", request, code, out, stderr, resolved, err)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Owner != "worker" || rows[0].Path != resolved || rows[0].Source != source {
		t.Fatalf("registry rows=%+v; want worker's copy %q of %q", rows, resolved, source)
	}
	if _, err := os.Lstat(resolved + ".soho-origin"); !os.IsNotExist(err) {
		t.Fatalf("explicit destination marker: %v; want absent", err)
	}
	// Remove only the fixture row to exercise the real GC listing.
	f.writeCopies(t, nil)
	old := platform.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(resolved, old, old); err != nil {
		t.Fatal(err)
	}
	gcCode, gcOut, gcErr := f.run(t, f.env, "gc", "--verbose")
	if gcCode != 0 || gcErr != "" {
		t.Fatalf("gc exit=%d stdout=%q stderr=%q", gcCode, gcOut, gcErr)
	}
	wantWarning := ""
	if scanPath == "" {
		if strings.Contains(gcOut, gcUnregisteredHeader) {
			t.Fatalf("gc discovered %q after registry loss: %s", resolved, gcOut)
		}
		wantWarning = "mutation-copy: gc does not find --dest copies if their registry row is lost (it looks for soho-mutation-* directly under " +
			strings.Join(roots, ", ") + "); release or remove " + resolved + " yourself, or drop --dest to let soho place it\n"
	} else if !strings.Contains(gcOut, "\n"+scanPath+"  ") || !strings.Contains(gcOut, "no origin marker\n") {
		t.Fatalf("gc missed %q after registry loss: %s", scanPath, gcOut)
	}
	if stderr != wantWarning {
		t.Fatalf("request=%q scanPath=%q mutation-copy stderr=%q; want %q; gc stdout=%q", request, scanPath, stderr, wantWarning, gcOut)
	}
}
