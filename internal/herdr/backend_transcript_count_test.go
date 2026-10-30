package herdr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestBackendCountClaudeCompactBoundaries pins the legacy wrapper the
// dispatch compact phase reads its boundary baseline and rise through: the
// count of a file with boundaries and of one without, Err for an unreadable
// file and for a cancelled context, and no herdr call in any case.
func TestBackendCountClaudeCompactBoundaries(t *testing.T) {
	env := newFake(t, "herdr", nil)
	svc := New(env)
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	two := write("two.jsonl", `{"type":"user","content":"a"}`+"\n"+`{"type":"system","subtype":"compact_boundary"}`+"\n"+`{"type":"system","subtype":"compact_boundary","compact_metadata":{}}`+"\n")
	none := write("none.jsonl", `{"type":"user","content":"a"}`+"\n")

	if rep := svc.Legacy.CountClaudeCompactBoundaries(context.Background(), two); rep.Err != nil || !rep.Valid || rep.Value != 2 {
		t.Fatalf("two boundaries: %+v", rep)
	}
	if rep := svc.Legacy.CountClaudeCompactBoundaries(context.Background(), none); rep.Err != nil || !rep.Valid || rep.Value != 0 {
		t.Fatalf("no boundary: %+v", rep)
	}
	if rep := svc.Legacy.CountClaudeCompactBoundaries(context.Background(), filepath.Join(dir, "missing.jsonl")); rep.Err == nil || rep.Valid || rep.Value != 0 {
		t.Fatalf("unreadable file: %+v", rep)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if rep := svc.Legacy.CountClaudeCompactBoundaries(cancelled, two); rep.Err == nil || rep.Valid || rep.Value != 0 {
		t.Fatalf("cancelled context: %+v", rep)
	}
	if calls := backendCalls(t, env); len(calls) != 0 {
		t.Fatalf("a local count ran herdr: %+v", calls)
	}
}
