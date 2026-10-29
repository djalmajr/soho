package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DJA-567: the guard must refuse a copy that holds a link whose target
// contains the copy itself (the loop an explicit --link used to create), with
// a line naming the link; a link to a sibling of the copy stays accepted.
func TestMutationGuardLinkLoop(t *testing.T) {
	t.Run("refuses a planted link to the copy itself", func(t *testing.T) {
		s := newGuardFixture(t)
		if err := os.Symlink(s.copy, filepath.Join(s.copy, "deps")); err != nil {
			t.Skip("symlink creation unavailable")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail no-symlink-into-source: symlink deps -> "+s.copy) || stderr != "" {
			t.Fatalf("self: code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("refuses a planted link to an ancestor of the copy", func(t *testing.T) {
		s := newGuardFixture(t)
		outer := filepath.Join(s.root, "outer")
		nested := filepath.Join(outer, "copy")
		if err := os.MkdirAll(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		// The ancestor contains the copy and not the source.
		if err := os.Symlink(outer, filepath.Join(nested, "deps")); err != nil {
			t.Skip("symlink creation unavailable")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{nested, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail no-symlink-into-source: symlink deps -> "+outer) || stderr != "" {
			t.Fatalf("ancestor: code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("keeps a planted link to a sibling of the copy accepted", func(t *testing.T) {
		s := newGuardFixture(t)
		sibling := filepath.Join(s.root, "sibling")
		if err := os.Mkdir(sibling, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sibling, filepath.Join(s.copy, "deps")); err != nil {
			t.Skip("symlink creation unavailable")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		want := "ok copy-outside-source\nok copy-outside-repository\nok no-symlink-into-source\nok build-env\nok cargo-config\n"
		if code != 0 || out != want || stderr != "" {
			t.Fatalf("sibling: code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}
