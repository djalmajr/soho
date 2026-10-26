package wait

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestAudit538MirrorRefreshPreservesConcurrentEdit(t *testing.T) {
	ctx, env, sd, _, stem := privateMirror(t)
	env["SOHO_DIR"] = filepath.Dir(sd)
	env["SOHO_SKILL_DIR"] = t.TempDir()
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, map[string]string{stem + ".md": "completed report\n", stem + ".brief.md": "brief\n", filepath.Join(sd, "agents.tsv"): "worker\tp1\tcodex\timplementer\n", filepath.Join(sd, "last-report-worker"): stem + ".md\n"})
	mirror := filepath.Join(sd, "reports", filepath.Base(stem)+".md")
	saved := linkMirrorFile
	reached := false
	var created os.FileInfo
	linkMirrorFile = func(root *os.Root, tmp, dst string) error {
		if !reached && dst == filepath.Base(mirror) {
			reached = true
			if err := os.WriteFile(mirror, []byte("concurrent operator edit\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			created, err = os.Stat(mirror)
			if err != nil {
				t.Fatal(err)
			}
		}
		return saved(root, tmp, dst)
	}
	t.Cleanup(func() { linkMirrorFile = saved })
	var out strings.Builder
	beforeOut := platform.Stdout
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = beforeOut })
	msg := captureStderr(t)
	// Adapted from the refresh probe: create a competing operator destination
	// after the absence check, immediately before first-settle publication.
	code := CmdStatus([]string{"worker"}, ctx, env, filepath.Dir(filepath.Dir(sd)))
	body, err := os.ReadFile(mirror)
	after, statErr := os.Stat(mirror)
	if code != 0 || !reached || err != nil || statErr != nil || string(body) != "concurrent operator edit\n" || !os.SameFile(created, after) || !strings.Contains(out.String(), "\tdone\t") {
		t.Fatalf("boundary=%v status=%d mirror=%q err=%v stat=%v out=%s stderr=%s", reached, code, body, err, statErr, out.String(), msg.String())
	}
	entries, err := os.ReadDir(filepath.Dir(mirror))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".soho-mirror-") {
			t.Fatalf("temporary file remains: %s", entry.Name())
		}
	}
	t.Logf("native publication boundary reached=%v, operator inode and edit preserved", reached)
}
