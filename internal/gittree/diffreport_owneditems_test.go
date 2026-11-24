package gittree_test

// SOH-410 round 4: every path token in an item is owned, but the audit
// matcher is the base matcher: a bare name is the file at the tree root.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestDiffReportOwnedItemBareNamesRootOnly(t *testing.T) {
	const item = "- `pane-view.tsx` e `editor.tsx` (só para remover o código antigo)\n"
	f := newDiffFixtureFiles(t, "# Owned files\n"+item, "Completed.\n", "implementer", map[string]string{
		"pane-view.tsx":                "original\n",
		"editor.tsx":                   "original\n",
		"src/components/pane-view.tsx": "original\n",
		"src/editor/editor.tsx":        "original\n",
	})
	for _, name := range []string{"pane-view.tsx", "editor.tsx", "src/components/pane-view.tsx", "src/editor/editor.tsx"} {
		if err := os.WriteFile(filepath.Join(f.tree, filepath.FromSlash(name)), []byte("changed\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	value, _, stderr := f.wait(t)
	want := []string{"src/components/pane-view.tsx", "src/editor/editor.tsx"}
	got := ownedOutside(value)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outside_owned=%q want %q stderr=%q", got, want, stderr)
	}
	if !strings.Contains(stderr, "outside its Owned files:") {
		t.Fatalf("missing outside warning: stderr=%q", stderr)
	}
}

// R586 F1 and F3: qualified root spellings normalize as on the base and
// keep glob matching; inherited sidecar fields are read verbatim.
func TestDiffReportOwnedItemQualifiedRootFile(t *testing.T) {
	files := map[string]string{"config.ts": "original\n", "src/config.ts": "original\n"}
	for _, tc := range []struct {
		name, brief string
		absolute    bool
		inherited   bool
		wantOutside []string
	}{
		{name: "relative qualified glob", brief: "# Owned files\n- `./*.ts`\n", wantOutside: []string{"src/config.ts"}},
		{name: "relative qualified root", brief: "# Owned files\n- `./config.ts`\n", wantOutside: []string{"src/config.ts"}},
		{name: "absolute qualified root", brief: "# Owned files\n- `ROOTPLACEHOLDER/config.ts`\n", absolute: true, wantOutside: []string{"src/config.ts"}},
		{name: "bare name", brief: "# Owned files\n- `config.ts`\n", wantOutside: []string{"src/config.ts"}},
		{name: "inherited normalized root field", brief: "# Owned files\n- inherit: previous brief\n", inherited: true, wantOutside: []string{"src/config.ts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiffFixtureFiles(t, tc.brief, "Completed.\n", "implementer", files)
			briefPath := filepath.Join(f.state, "briefs", "worker-fixture.md")
			if tc.absolute {
				info, err := os.Stat(briefPath)
				if err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(briefPath)
				if err != nil {
					t.Fatal(err)
				}
				replaced := strings.Replace(string(body), "ROOTPLACEHOLDER", f.tree, 1)
				if replaced == string(body) {
					t.Fatalf("brief %q has no tree placeholder", body)
				}
				if err := os.WriteFile(briefPath, []byte(replaced), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(briefPath, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if tc.inherited {
				body, err := os.ReadFile(f.sidecar)
				if err != nil {
					t.Fatal(err)
				}
				var sidecar map[string]any
				if err := json.Unmarshal(body, &sidecar); err != nil {
					t.Fatal(err)
				}
				sidecar["git_previous_owned"] = []string{"config.ts"}
				body, err = json.Marshal(sidecar)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.sidecar, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"config.ts", "src/config.ts"} {
				if err := os.WriteFile(filepath.Join(f.tree, filepath.FromSlash(name)), []byte("changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			value, _, stderr := f.wait(t)
			got, want := ownedOutside(value), append([]string{}, tc.wantOutside...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("outside_owned=%q want %q stderr=%q", got, want, stderr)
			}
			if len(want) > 0 && !strings.Contains(stderr, "outside its Owned files:") {
				t.Fatalf("missing outside warning: stderr=%q", stderr)
			}
		})
	}
}

func ownedOutside(value map[string]any) []string {
	raw, ok := value["outside_owned"].([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, item := range raw {
		out = append(out, item.(string))
	}
	sort.Strings(out)
	return out
}

// The two distinct on-disk names are only available where the filesystem
// does not fold a trailing dot into the ordinary path spelling.
func TestDiffReportOwnedItemQuotedTrailingDotOnDisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Win32 normalizes a trailing dot in ordinary paths, so sample.ts. and sample.ts cannot name two distinct files")
	}
	f := newDiffFixtureFiles(t, "# Owned files\n- `sample.ts.`\n", "Completed.\n", "implementer", map[string]string{
		"sample.ts.": "original\n",
		"sample.ts":  "original\n",
	})
	for _, name := range []string{"sample.ts.", "sample.ts"} {
		if err := os.WriteFile(filepath.Join(f.tree, name), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(f.tree)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.Name()] = true
	}
	if !seen["sample.ts."] || !seen["sample.ts"] {
		t.Fatalf("filesystem does not keep both names: %v", seen)
	}
	value, _, stderr := f.wait(t)
	if got, want := ownedOutside(value), []string{"sample.ts"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outside_owned=%q want %q stderr=%q", got, want, stderr)
	}
}

func TestDiffReportOwnedItemLiteralMatching(t *testing.T) {
	cases := []struct {
		name, brief string
		files       map[string]string
		changed     []string
		wantOutside []string
	}{
		{
			name:        "bare name is a root file, not a basename match",
			brief:       "# Owned files\n- `notes.md`\n",
			files:       map[string]string{"notes.md": "original\n", "src/[draft]/notes.md": "original\n"},
			changed:     []string{"notes.md", "src/[draft]/notes.md"},
			wantOutside: []string{"src/[draft]/notes.md"},
		},
		{
			name:        "balanced parentheses belong to the path",
			brief:       "# Owned files\n- src/(setup)\n",
			files:       map[string]string{"src/(setup)": "original\n", "src/other": "original\n"},
			changed:     []string{"src/(setup)", "src/other"},
			wantOutside: []string{"src/other"},
		},
		{
			name:    "orchestrator item excludes every plain path",
			brief:   "# Owned files\n- unrelated.go\n# Orchestrator files\n- src/a.ts and src/b.ts\n",
			files:   map[string]string{"src/a.ts": "a\n", "src/b.ts": "b\n"},
			changed: []string{"src/a.ts", "src/b.ts"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiffFixtureFiles(t, tc.brief, "Completed.\n", "implementer", tc.files)
			for _, name := range tc.changed {
				if err := os.WriteFile(filepath.Join(f.tree, filepath.FromSlash(name)), []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			value, _, stderr := f.wait(t)
			if len(tc.wantOutside) == 0 && stderr != "" {
				t.Fatalf("stderr=%q", stderr)
			}
			if len(tc.wantOutside) > 0 && !strings.Contains(stderr, "outside its Owned files:") {
				t.Fatalf("missing outside warning: stderr=%q", stderr)
			}
			got, want := ownedOutside(value), append([]string{}, tc.wantOutside...)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("outside_owned=%q want %q", got, want)
			}
		})
	}
}
