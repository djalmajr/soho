package peer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestDJA651CodexHomeResolution(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	for _, key := range []string{"CODEX_HOME", "HOME", "USERPROFILE"} {
		t.Run(key, func(t *testing.T) {
			home := t.TempDir()
			codex := home
			if key != "CODEX_HOME" {
				codex = filepath.Join(home, ".codex")
			}
			path := filepath.Join(codex, "sessions", "2026", "11", "05", "rollout-test-"+uuid+".jsonl")
			if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"`+uuid+`"}}`+"\n"), 0600); e != nil {
				t.Fatal(e)
			}
			r := newCodexReceipt(resolvedTarget{Machine: "local", Kind: "codex", TargetArg: "w:p"}, agentInfo{OK: true, Status: "working", PaneID: "w:p", Kind: "codex", SessionAgent: "codex", SessionKind: "id", SessionValue: uuid}, "[/soho:peer:01020304]", platform.Env{key: home})
			if r == nil {
				t.Fatal("configured/default synthetic Codex home did not arm")
			}
			r.Close()
		})
	}
}
