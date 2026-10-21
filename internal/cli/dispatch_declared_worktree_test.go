package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchDeclaredWorktree(t *testing.T) {
	for _, tc := range []struct {
		name, mine, peer string
		otherCwd         bool
		wantShared       bool
		warnOnly         bool
		lexicalCwd       bool
	}{
		{name: "different-declarations", mine: "a", peer: "b"},
		{name: "same-declarations", mine: "a", peer: "a", wantShared: true},
		{name: "only-new-declares", mine: "a"},
		{name: "only-peer-declares", peer: "a"},
		{name: "no-declarations", wantShared: true},
		{name: "same-declarations-different-roster-cwd", mine: "a", peer: "a", otherCwd: true, wantShared: true},
		{name: "new-declaration-matches-peer-cwd", mine: "root", wantShared: true},
		{name: "peer-declaration-matches-worker-cwd", peer: "root", wantShared: true},
		{name: "no-declarations-lexical-cwd-variant", lexicalCwd: true, warnOnly: true},
		{name: "normalized-declarations", mine: "a", peer: "a-dot", wantShared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mineBody := strings.Replace(tm3bFullBrief(), "internal/a.go", "- internal/a.go", 1)
			h := newTM3bHarness(t, mineBody)
			declaration := func(tree string) string {
				if tree == "" {
					return ""
				}
				path := h.root
				if tree != "root" {
					path = filepath.Join(h.root, ".worktrees", tree)
				}
				if tree == "a-dot" {
					path = filepath.Join(h.root, ".worktrees", "a") + string(os.PathSeparator) + "."
				}
				return "Worktree: `" + path + "` (branch example)\n"
			}
			if err := os.WriteFile(h.brief, []byte(declaration(tc.mine)+mineBody), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, sub := range []string{"briefs", "reports"} {
				if err := os.MkdirAll(filepath.Join(h.ws, sub), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			pending := "# Brief\n" + declaration(tc.peer) + "# Owned files\n- internal/a.go\n# Report contract\n"
			if err := os.WriteFile(filepath.Join(h.ws, "briefs", "peer.md"), []byte(pending), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(h.ws, "last-report-peer"), []byte(filepath.Join(h.ws, "reports", "peer.md")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			peerCwd := h.root
			if tc.otherCwd {
				peerCwd = filepath.Join(h.root, "other-roster-cwd")
			}
			if tc.lexicalCwd {
				peerCwd += string(os.PathSeparator) + "."
			}
			rosterPath := filepath.Join(h.ws, "agents.tsv")
			roster, err := os.ReadFile(rosterPath)
			if err != nil {
				t.Fatal(err)
			}
			peer := "peer\tw0test:p0b\tcodex\timplementer\topenai\t0\t" + peerCwd + "\tnow\tgpt-5\ttask\timplementer\n"
			if err := os.WriteFile(rosterPath, append(roster, []byte(peer)...), 0o600); err != nil {
				t.Fatal(err)
			}
			// Reuse the overlap harness, but list the peer as live so the real
			// dispatch composition also exercises sharedTreeEditor.
			bin := t.TempDir()
			if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
				{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"peer","pane_id":"w0test:p0b"}]}}`},
				{AnyArgs: true, Stdout: `{"result":{"agents":[],"sent":true}}`},
			}); err != nil {
				t.Fatal(err)
			}
			h.env["PATH"] = bin
			h.env["SOHO_FAKECLI_CONFIG"] = filepath.Join(bin, "herdr.json")
			code, out, stderr := h.run(t, "build", h.brief, "--no-wait")
			var result struct {
				Status string `json:"wait_status"`
				Prompt string `json:"composed_prompt"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil || code != 0 || result.Status != "submitted" {
				t.Fatalf("dispatch code=%d result=%+v err=%v stderr=%q out=%q", code, result, err, stderr, out)
			}
			warning := "brief " + h.brief + " owns files that 'peer' is still editing: internal/a.go"
			if want := tc.wantShared || tc.warnOnly; strings.Contains(stderr, warning) != want {
				t.Errorf("overlap warning=%t, want %t; stderr=%q", strings.Contains(stderr, warning), want, stderr)
			}
			prompt, err := os.ReadFile(result.Prompt)
			if err != nil {
				t.Fatal(err)
			}
			const note = "- Another worker edits this same tree now: run the global checks the brief asks for, but report failures in files you do not own as outside your slice (name the files), not as [partial] items of yours.\n"
			if got := strings.Contains(string(prompt), note); got != tc.wantShared {
				t.Errorf("shared-tree note=%t, want %t; composed=%q", got, tc.wantShared, result.Prompt)
			}
		})
	}
}
