package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestCopiesAddNested(t *testing.T) {
	for _, name := range []string{"child and log", "only child", "two children", "unregistered child", "inside registered copy", "symlinked ancestor", "symlinked registry path", "same directory", "sibling prefix", "deep child only"} {
		t.Run(name, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
				t.Setenv(key, f.env.Get(key))
			}
			f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
			parent := filepath.Join(f.tmp, "evidence")
			child := filepath.Join(parent, "mutant")
			if name == "deep child only" {
				child = filepath.Join(parent, "nested", "mutant")
			}
			if err := os.MkdirAll(child, 0o700); err != nil {
				t.Fatal(err)
			}
			input, registered := parent, child
			wantWarning := true
			other, more := "", ""
			switch name {
			case "child and log":
				if err := os.WriteFile(filepath.Join(parent, "gate.log"), []byte("evidence\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				other = " (and other entries)"
			case "two children":
				other, more = " (and other entries)", " (+1 more)"
			case "unregistered child":
				registered, wantWarning = "", false
			case "inside registered copy":
				input, registered, wantWarning = child, parent, false
			case "same directory":
				registered, wantWarning = parent, false
			case "sibling prefix":
				registered, wantWarning = parent+"-sibling", false
				if err := os.Mkdir(registered, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlinked ancestor", "symlinked registry path":
				alias := filepath.Join(f.root, "alias")
				if err := os.Symlink(f.tmp, alias); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("symlink creation unavailable: %v", err)
					}
					t.Fatal(err)
				}
				if name == "symlinked ancestor" {
					input = filepath.Join(alias, "evidence")
				} else {
					registered = filepath.Join(alias, "evidence", "mutant")
				}
			}
			var initial []core.CopyRow
			if registered != "" {
				initial = append(initial, core.CopyRow{Path: registered, Owner: "other-worker", Pane: "other-pane", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "mutation-copy", Task: "-", Report: "-"})
			}
			if name == "two children" {
				second := filepath.Join(parent, "second")
				if err := os.Mkdir(second, 0o700); err != nil {
					t.Fatal(err)
				}
				initial = append(initial, core.CopyRow{Path: second, Owner: "-", Pane: "-", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add", Task: "-", Report: "-"})
			}
			f.writeCopies(t, initial)
			code, out, stderr := f.run(t, f.env, "copies", "add", input)
			want := ""
			if wantWarning {
				want = fmt.Sprintf("copies add: %s contains the registered copy %s%s%s; release removes all of %s, logs included. Register only the copy, or release with --keep-copies to keep it", parent, child, more, other, parent)
			}
			wantStderr := ""
			if want != "" {
				wantStderr = "soho: warning: " + want + "\n"
			}
			if code != 0 || out != "" || stderr != wantStderr {
				t.Fatalf("copies add %q: code=%d stdout=%q stderr=%q; want exit=0 stdout=empty stderr=%q", input, code, out, stderr, wantStderr)
			}
			resolved, err := filepath.EvalSymlinks(input)
			if err != nil {
				t.Fatal(err)
			}
			rows := f.copiesRows(t)
			wantCount := len(initial) + 1
			if name == "same directory" {
				wantCount = len(initial)
			}
			if len(rows) != wantCount {
				t.Fatalf("rows=%#v; want %d rows", rows, wantCount)
			}
			found := false
			for _, row := range rows {
				if row.Path == resolved {
					found = row.Owner == "worker" && row.Pane == "p-worker" && row.Origin == "add" && row.Source == "-"
				} else {
					kept := false
					for _, original := range initial {
						kept = kept || row == original
					}
					if !kept {
						t.Fatalf("existing registry row changed: %#v", row)
					}
				}
			}
			if !found {
				t.Fatalf("new registration missing or changed: %#v", rows)
			}
			log, err := os.ReadFile(filepath.Join(f.stateDir(t), "friction.log"))
			if wantWarning {
				if err != nil || strings.Count(string(log), "\twarning\tcopies\t"+want+"\n") != 1 || strings.Count(string(log), "\n") != 1 {
					t.Fatalf("friction log=%q err=%v; want exactly one copies warning %q", log, err, want)
				}
			} else if err != nil && !os.IsNotExist(err) || len(log) != 0 {
				t.Fatalf("unexpected friction log=%q err=%v", log, err)
			}
			if name == "child and log" {
				data, err := os.ReadFile(filepath.Join(parent, "gate.log"))
				if err != nil || string(data) != "evidence\n" {
					t.Fatalf("evidence changed: %q %v", data, err)
				}
			}
		})
	}
}

// TestCopiesAddNestedHelpers pins the strict containment (a spelling of the
// parent itself is never a child) and the depth-bounded entry check.
func TestCopiesAddNestedHelpers(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(parent, "a", "b", "copy")
	if err := os.MkdirAll(filepath.Join(child, "inside"), 0o777); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		child string
		want  bool
	}{
		{parent, false},
		{parent + string(filepath.Separator), false},
		{child, true},
		{parent + "x", false},
		{root, false},
	} {
		if got := strictlyInside(parent, c.child); got != c.want {
			t.Fatalf("strictlyInside(%q, %q) = %t, want %t", parent, c.child, got, c.want)
		}
	}
	// Only the path down to the copy, and files inside the copy: nothing else.
	if err := os.WriteFile(filepath.Join(child, "inside", "f"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	if hasOtherEntries(parent, child) {
		t.Fatal("entries inside the copy counted as other entries")
	}
	// A sibling anywhere on the path counts, and so does a wide directory.
	if err := os.WriteFile(filepath.Join(parent, "a", "log.txt"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	if !hasOtherEntries(parent, child) {
		t.Fatal("a file beside the path was not counted")
	}
	for i := 0; i < 2000; i++ {
		if err := os.WriteFile(filepath.Join(parent, fmt.Sprintf("f%04d", i)), nil, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if !hasOtherEntries(parent, child) {
		t.Fatal("a wide parent was not counted")
	}
}
