package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func mutationCopyHCFixture(t *testing.T) (mutationCopyFixture, string, string) {
	t.Helper()
	f := newMutationCopyFixture(t)
	oldNow := platform.Now
	platform.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	t.Cleanup(func() { platform.Now = oldNow })
	bin := filepath.Join(f.root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	mutationAbsoluteSymlink(t, git, filepath.Join(bin, filepath.Base(git)))
	// A fresh environment cannot inherit a legacy pane, registry hint or user config.
	f.env = platform.Env{"HOME": f.home, "USERPROFILE": f.home, "XDG_CONFIG_HOME": filepath.Join(f.root, "config"), "TMPDIR": f.tmp, "PATH": bin, "SOHO_DIR": filepath.Join(f.root, "state"), "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t-copy", "SOHO_FAKECLI_CONFIG": bin, "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull}
	caller := `{"address":"t-copy","state":"live","labels":{"soho.session":"native-session"}}`
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{{Argv: []string{"get", "t-copy", "--json"}, Stdout: caller}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	sd := filepath.Join(f.env["SOHO_DIR"], "native-session")
	writeConfigFile(t, filepath.Join(sd, "agents.tsv"), "# roster\n"+releaseRow("native-worker", "t-copy", "1", "implementer", false, f.source)+"\n")
	t.Chdir(f.source)
	return f, sd, bin
}

func TestMutationCopyHCNativeRun(t *testing.T) {
	for _, pathMode := range []bool{false, true} {
		t.Run(fmt.Sprint(pathMode), func(t *testing.T) {
			f, sd, bin := mutationCopyHCFixture(t)
			writeConfigFile(t, filepath.Join(f.source, ".agents", "soho.conf"), "mutation_copy_exclude=configured.txt\n")
			for name, data := range map[string]string{"tracked.txt": "modified\n", "new.txt": "new\n", "ignored.txt": "ignored\n", "configured.txt": "excluded\n", "explicit.txt": "excluded\n"} {
				writeConfigFile(t, filepath.Join(f.source, name), data)
			}
			before := fileSnapshot(t, f.source)
			args := []string{"--source", f.source, "--exclude", "explicit.txt"}
			if pathMode {
				args = append(args, "--print-path")
			}
			code, out, stderr := runMutationCopyCmd(t, f, f.source, nil, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			rows, err := core.ReadCopies(sd)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%#v err=%v", rows, err)
			}
			row := rows[0]
			if row.Owner != "native-worker" || row.Pane != "t-copy" || row.Source != f.source || row.Origin != "mutation-copy" || row.Task != "-" || row.Report != "-" {
				t.Fatalf("wrong native registration: %#v", row)
			}
			want := fmt.Sprintf(`{"copy":%q,"source":%q,"files":5,"excluded":2,"links":[]}`+"\n", row.Path, f.source)
			if pathMode {
				want = row.Path + "\n"
			}
			if out != want {
				t.Fatalf("stdout=%q want=%q", out, want)
			}
			for name, data := range map[string]string{".gitignore": "ignored.txt\n", "tracked.txt": "modified\n", "new.txt": "new\n", "sub/deep.txt": "deep\n", ".agents/soho.conf": "mutation_copy_exclude=configured.txt\n"} {
				got, err := os.ReadFile(filepath.Join(row.Path, name))
				if err != nil || string(got) != data {
					t.Fatalf("%s=%q err=%v", name, got, err)
				}
			}
			for _, name := range []string{".git", "ignored.txt", "configured.txt", "explicit.txt"} {
				if _, err := os.Lstat(filepath.Join(row.Path, name)); !os.IsNotExist(err) {
					t.Fatalf("copied %s: %v", name, err)
				}
			}
			marker, err := os.ReadFile(row.Path + ".soho-origin")
			wantMarker := "project=" + filepath.Join(f.source, ".git") + "\nsource=" + f.source + "\ncreated=" + row.Created + "\n"
			if err != nil || string(marker) != wantMarker {
				t.Fatalf("marker=%q want=%q err=%v", marker, wantMarker, err)
			}
			registry, err := os.ReadFile(core.CopiesFile(sd))
			if err != nil || strings.Count(string(registry), row.Path+"\t") != 1 {
				t.Fatalf("registry=%q err=%v", registry, err)
			}
			if !reflect.DeepEqual(before, fileSnapshot(t, f.source)) {
				t.Fatal("source bytes changed")
			}
			assertZeroCliCalls(t, filepath.Join(bin, "herdr.calls.jsonl"), "herdr")
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
			if err != nil || len(calls) == 0 {
				t.Fatalf("no native session lookup: %v", err)
			}
			for _, call := range calls {
				if !reflect.DeepEqual(call.Argv, []string{"get", "t-copy", "--json"}) {
					t.Fatalf("unexpected native call: %#v", call)
				}
			}
		})
	}
}

func TestMutationCopyHCRefusals(t *testing.T) {
	for _, name := range []string{"nowrite", "arguments", "source", "destination", "ancestor", "repository", "symlink", "link-loop", "guard", "owned-guard"} {
		t.Run(name, func(t *testing.T) {
			f, sd, bin := mutationCopyHCFixture(t)
			dest := filepath.Join(f.root, "refused")
			args := []string{"--source", f.source, "--dest", dest}
			codeWant := 2
			want := ""
			owned := false
			switch name {
			case "nowrite":
				f.env["SOHO_NOWRITE"] = "1"
			case "arguments":
				args = append(args, "--unknown")
				want = "soho: usage: mutation-copy [--source <dir>] [--dest <dir>] [--print-path] [--exclude <glob>]... [--link <relpath>=<target>]...\n"
			case "source":
				args[1] = f.home
				want = "mutation-copy: refusing source '" + f.home + "': HOME or above it\n"
			case "destination":
				writeConfigFile(t, filepath.Join(dest, "keep"), "caller bytes\n")
				owned = true
				want = "mutation-copy: refusing destination '" + dest + "': not empty\n"
			case "ancestor":
				dest = f.source
				args[3] = dest
				owned = true
				want = "mutation-copy: refusing destination '" + dest + "': destination is inside the source\n"
			case "repository":
				args[1] = filepath.Join(f.source, "sub")
				dest = filepath.Join(f.source, "sibling")
				args[3] = dest
				codeWant = 1
				want = "fail copy-outside-repository: the copy is inside the repository at " + f.source + "\n"
			case "symlink":
				mutationAbsoluteSymlink(t, f.source, filepath.Join(f.source, "escape"))
				want = "mutation-copy: refusing symlink 'escape': target contains the source\n"
			case "link-loop":
				args = append(args, "--link", "deps="+dest)
				want = "mutation-copy: refusing link 'deps=" + dest + "': target contains the copy\n"
			case "guard", "owned-guard":
				f.env["CARGO_TARGET_DIR"] = filepath.Join(f.source, "target")
				codeWant = 1
				want = "fail build-env: CARGO_TARGET_DIR points into the source tree\n"
				if name == "owned-guard" {
					if err := os.Mkdir(dest, 0700); err != nil {
						t.Fatal(err)
					}
					owned = true
				}
			}
			before := mutationCopyHCSnapshot(t, f.source)
			registryBefore := fileSnapshot(t, sd)
			code, out, stderr := runMutationCopyCmd(t, f, f.source, nil, args...)
			if name == "nowrite" {
				if !strings.Contains(stderr, "SOHO_NOWRITE=1 is read-only:") || !strings.Contains(stderr, "rejected: mutation-copy") {
					t.Fatalf("wrong NOWRITE: %q", stderr)
				}
			} else if stderr != want {
				t.Fatalf("stderr=%q want=%q", stderr, want)
			}
			if code != codeWant || out != "" {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", code, codeWant, out, stderr)
			}
			if !reflect.DeepEqual(registryBefore, fileSnapshot(t, sd)) {
				t.Fatal("failed copy registered")
			}
			if _, err := os.Stat(dest); owned && err != nil || !owned && !os.IsNotExist(err) {
				t.Fatalf("destination ownership violated: %v", err)
			}
			if after := mutationCopyHCSnapshot(t, f.source); !reflect.DeepEqual(before, after) {
				t.Fatalf("source bytes or links changed:\n%s", mutationCopyHCSnapshotDiff(f.source, before, after))
			}
			if name == "destination" {
				data, err := os.ReadFile(filepath.Join(dest, "keep"))
				if err != nil || string(data) != "caller bytes\n" {
					t.Fatal("caller bytes changed")
				}
			}
			if entries, err := os.ReadDir(f.tmp); err != nil || len(entries) != 0 {
				t.Fatalf("temporary files left: %v %v", entries, err)
			}
			assertZeroCliCalls(t, filepath.Join(bin, "herdr.calls.jsonl"), "herdr")
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			for _, call := range calls {
				if !reflect.DeepEqual(call.Argv, []string{"get", "t-copy", "--json"}) {
					t.Fatalf("refusal issued a non-read native call: %#v", call)
				}
			}
		})
	}
}

// Describe changed snapshot entries by path, kind and SHA-256 only, never by content.
func mutationCopyHCSnapshotDiff(root string, before, after map[string]string) string {
	describe := func(entry string, ok bool) string {
		if !ok {
			return "absent"
		}
		kind, body, _ := strings.Cut(entry, ":")
		return fmt.Sprintf("%s sha256=%x", kind, sha256.Sum256([]byte(body)))
	}
	seen := map[string]bool{}
	var paths []string
	for _, snap := range []map[string]string{before, after} {
		for path := range snap {
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	sort.Strings(paths)
	var lines []string
	for _, path := range paths {
		b, bok := before[path]
		a, aok := after[path]
		if bok == aok && b == a {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		lines = append(lines, fmt.Sprintf("changed %s: before %s; after %s", filepath.ToSlash(rel), describe(b, bok), describe(a, aok)))
	}
	return strings.Join(lines, "\n")
}

// Snapshot link text without following links, including directory links.
func mutationCopyHCSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			result[path] = "link:" + target
		case info.IsDir():
			result[path] = "directory"
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = "file:" + string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMutationCopyHCRegistrationFallback(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			f, sd, bin := mutationCopyHCFixture(t)
			cause := errors.New("disk full")
			if queued {
				cause = os.ErrPermission
			}
			refuseCopiesRegistry(t, cause)
			dest := filepath.Join(f.tmp, "soho-mutation-copy")
			code, out, stderr := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--dest", dest, "--print-path")
			want := "mutation-copy: copy not registered (disk full); remove it yourself when done\n"
			if queued {
				queue := core.PendingCopiesFile(f.env, "native-session")
				want = "mutation-copy: the state dir " + sd + " is not writable from here; the registration is queued for the orchestrator in " + queue + "\n"
				raw := readQueue(t, queue)
				if !strings.Contains(raw, "\n"+sd+"\t"+dest+"\tnative-worker\tt-copy\t") || !strings.HasSuffix(raw, "\t"+f.source+"\tmutation-copy\t-\t-\n") {
					t.Fatalf("queue=%q", raw)
				}
			}
			if code != 0 || out != dest+"\n" || stderr != want {
				t.Fatalf("exit=%d stdout=%q stderr=%q want=%q", code, out, stderr, want)
			}
			if _, err := os.Stat(filepath.Join(dest, "tracked.txt")); err != nil {
				t.Fatal(err)
			}
			assertZeroCliCalls(t, filepath.Join(bin, "herdr.calls.jsonl"), "herdr")
		})
	}
}
