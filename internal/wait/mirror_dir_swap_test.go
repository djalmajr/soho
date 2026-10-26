package wait

import (
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudit538DirectorySwapDoesNotFollowLink(t *testing.T) {
	for _, outside := range []bool{false, true} {
		name := "internal-target"
		if outside {
			name = "outside-target"
		}
		t.Run(name, func(t *testing.T) {
			ctx, env, sd, _, stem := privateMirror(t)
			env["SOHO_DIR"] = filepath.Dir(sd)
			env["SOHO_SKILL_DIR"] = t.TempDir()
			if e := os.MkdirAll(filepath.Join(sd, "reports"), 0700); e != nil {
				t.Fatal(e)
			}
			target := filepath.Join(sd, "operator-directory")
			if outside {
				target = t.TempDir()
			} else if e := os.Mkdir(target, 0700); e != nil {
				t.Fatal(e)
			}
			writeFiles(t, map[string]string{stem + ".md": "completed report\n", stem + ".brief.md": "brief\n", filepath.Join(sd, "agents.tsv"): "worker\tp1\tcodex\timplementer\n", filepath.Join(sd, "last-report-worker"): stem + ".md\n", filepath.Join(target, "sentinel"): "keep\n"})
			reached := false
			old := mirrorBeforeOpenDir
			t.Cleanup(func() { mirrorBeforeOpenDir = old })
			mirrorBeforeOpenDir = func(root *os.Root, name string) {
				if reached || name != "reports" {
					return
				}
				reached = true
				if e := root.Rename("reports", "reports-checked"); e != nil {
					t.Fatal(e)
				}
				link := target
				if !outside {
					link = "operator-directory"
				}
				if e := root.Symlink(link, "reports"); e != nil {
					t.Fatal(e)
				}
			}
			var out strings.Builder
			saved := platform.Stdout
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = saved })
			msg := captureStderr(t)
			code := CmdStatus([]string{"worker"}, ctx, env, filepath.Dir(filepath.Dir(sd)))
			foreign := filepath.Join(target, filepath.Base(stem)+".md")
			b, e := os.ReadFile(foreign)
			sentinel, se := os.ReadFile(filepath.Join(target, "sentinel"))
			t.Logf("outside=%v checked_to_open_boundary_reached=%v status=%d unexpected_destination=%s bytes=%q error=%v output=%s stderr=%s", outside, reached, code, foreign, b, e, out.String(), msg.String())
			if !reached || code != 0 || se != nil || string(sentinel) != "keep\n" {
				t.Fatal("fixture or status contract failed")
			}
			if !os.IsNotExist(e) {
				t.Fatal("mirror followed a directory link installed after Lstat")
			}
		})
	}
}
