package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestLintSameRepositoryPathWarnings(t *testing.T) {
	env := repoTestEnv(t)
	main := repoTestInit(t, env)
	worker := repoTestLinked(t, env, main)
	other := repoTestInit(t, env)
	nongit := t.TempDir()
	if err := os.Mkdir(filepath.Join(main, "messages"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tree string
		warn       bool
	}{
		{"different repository", other, false},
		{"same linked repository", worker, true},
		{"unknown repository", nongit, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brief := filepath.Join(t.TempDir(), "brief.md")
			if err := os.WriteFile(brief, []byte("Worktree: `"+tc.tree+"`\nRead `messages`.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			got := BriefLintFindings(brief, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, BriefLintOptions{WorkerCwd: tc.tree, OrchestratorCwd: main, PathsOnly: true})
			want := "brief " + brief + " line 2 cites 'messages', which the worker in " + tc.tree + " does not find at that relative path; a matching path exists at " + filepath.Join(main, "messages") + " under checkout " + main + ", outside the worker tree; use it only if this brief intends that file"
			if tc.warn && (len(got.UnseenPaths) != 1 || got.UnseenPaths[0] != want || len(got.Warnings) != 1 || got.Warnings[0] != want) {
				t.Fatalf("want exact warning %q, got unseen=%q warnings=%q", want, got.UnseenPaths, got.Warnings)
			}
			if !tc.warn && (len(got.UnseenPaths) != 0 || strings.Contains(strings.Join(got.Warnings, "\n"), "cites 'messages'")) {
				t.Fatalf("unexpected warnings: %+v", got)
			}
		})
	}
}
