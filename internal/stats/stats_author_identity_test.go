package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAudit539RoundsUseWorktreeIdentity(t *testing.T) {
	f := newAmendLostFixture(t)
	tree := filepath.Join(f.root, "slice")
	for i, declared := range []string{tree, tree + string(filepath.Separator)} {
		stamp := fmt.Sprintf("20261001T10000%d", i)
		f.brief("reviewer", stamp, "You are running as the `reviewer` role\nWorktree: "+declared+"\n")
		f.report("reviewer", stamp, "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n")
		if err := os.WriteFile(filepath.Join(f.briefs, "reviewer-"+stamp+".dispatch.json"), []byte(`{"version":1,"submission":"accepted","kind":"codex","model":"reviewer","effort":"","author_model":"author","author_type":"backend"}`), 0600); err != nil {
			t.Fatal(err)
		}
		out, rc := f.run(t, "--by", "author-model", "--json")
		if rc != 0 {
			t.Fatal(rc)
		}
		var doc struct {
			Groups map[string]struct {
				Reviews int     `json:"reviews"`
				Rounds  float64 `json:"rounds_per_slice"`
			}
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatal(err)
		}
		t.Logf("after review %d: %s", i+1, out)
		if row := doc.Groups["author"]; row.Reviews != i+1 || row.Rounds != float64(i+1) {
			t.Fatalf("same-worktree rounds=%v reviews=%d want %d", row.Rounds, row.Reviews, i+1)
		}
	}
}

func TestStatsAuthorWorktreeAliases(t *testing.T) {
	for _, mode := range []string{"clean-dot", "symlink", "windows-case"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "windows-case" && runtime.GOOS != "windows" {
				t.Skip("requires native Windows case folding")
			}
			f := newAmendLostFixture(t)
			tree := filepath.Join(f.root, "slice")
			alias := tree + string(filepath.Separator) + "."
			if mode == "symlink" {
				if err := os.Mkdir(tree, 0700); err != nil {
					t.Fatal(err)
				}
				alias = filepath.Join(f.root, "linked-slice")
				if err := os.Symlink(tree, alias); err != nil {
					t.Fatalf("symlink fixture: %v", err)
				}
			}
			if mode == "windows-case" {
				alias = strings.ToUpper(tree)
			}
			for i, declared := range []string{tree, alias, tree + "-other"} {
				stamp := fmt.Sprintf("20261001T10000%d", i)
				f.brief("reviewer", stamp, "You are running as the `reviewer` role\nWorktree: "+declared+"\n")
				f.report("reviewer", stamp, "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n")
				if err := os.WriteFile(filepath.Join(f.briefs, "reviewer-"+stamp+".dispatch.json"), []byte(`{"version":1,"submission":"accepted","kind":"codex","model":"reviewer","effort":"","author_model":"author","author_type":"backend"}`), 0600); err != nil {
					t.Fatal(err)
				}
				for _, by := range []string{"author-model", "type"} {
					out, rc := f.run(t, "--by", by, "--json")
					if rc != 0 {
						t.Fatal(rc)
					}
					var doc struct {
						Groups map[string]struct {
							Reviews int     `json:"reviews"`
							Rounds  float64 `json:"rounds_per_slice"`
						}
					}
					if err := json.Unmarshal([]byte(out), &doc); err != nil {
						t.Fatal(err)
					}
					key := "author"
					if by == "type" {
						key = "backend"
					}
					want := []float64{1, 2, 1.5}[i]
					if row := doc.Groups[key]; row.Reviews != i+1 || row.Rounds != want {
						t.Fatalf("mode=%s by=%s output=%s want reviews=%d rounds=%v", mode, by, out, i+1, want)
					}
					t.Logf("mode=%s by=%s review=%d rounds=%v", mode, by, i+1, want)
				}
			}
		})
	}
}
