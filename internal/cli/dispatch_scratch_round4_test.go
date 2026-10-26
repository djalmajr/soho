package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudit538MirrorParentLink(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, matching := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing=%v/matching-record=%v", existing, matching), func(t *testing.T) {
				f, obj := durableScratchDispatch(t, "codex", "full")
				report := durableField(t, obj, "report")
				stateReports := filepath.Join(f.state, "ws", "reports")
				foreign := t.TempDir()
				victim := filepath.Join(foreign, filepath.Base(report))
				sentinel := []byte("foreign preserved report\n")
				// Move only this fixture's empty directory to plant the adversarial link.
				if err := os.Rename(stateReports, stateReports+"-fixture-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, stateReports); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				if err := os.WriteFile(filepath.Join(foreign, "sentinel"), sentinel, 0600); err != nil {
					t.Fatal(err)
				}
				if existing {
					if err := os.WriteFile(victim, sentinel, 0600); err != nil {
						t.Fatal(err)
					}
				}
				record := []byte("invalid digest\n")
				if matching {
					record = []byte(fmt.Sprintf("%x\n", sha256.Sum256(sentinel)))
				}
				if err := os.WriteFile(victim+".mirror.sha256", record, 0600); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadDir(foreign)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(report, []byte("scratch replacement\n"), 0600); err != nil {
					t.Fatal(err)
				}
				code, out, msg := runIn(t, []string{"status", "worker"}, f.env, f.root)
				after, err := os.ReadDir(foreign)
				if err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(filepath.Join(foreign, "sentinel"))
				if code != 0 || !strings.Contains(out, "\tdone\t") || err != nil || string(body) != string(sentinel) || len(before) != len(after) {
					t.Fatalf("status=%d out=%s stderr=%s sentinel=%q before=%d after=%d err=%v", code, out, msg, body, len(before), len(after), err)
				}
				body, err = os.ReadFile(victim)
				if existing {
					if err != nil || string(body) != string(sentinel) {
						t.Fatalf("outside report changed: %q %v", body, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("outside report created: %q %v", body, err)
				}
			})
		}
	}
}

func TestScratchDurableMirrorCreatedOnceAtSettle(t *testing.T) {
	for _, command := range []string{"status", "wait"} {
		t.Run(command, func(t *testing.T) {
			f, obj := durableScratchDispatch(t, "codex", "full")
			report := durableField(t, obj, "report")
			mirror := filepath.Join(f.state, "ws", "reports", filepath.Base(report))
			if _, err := os.Lstat(mirror); !os.IsNotExist(err) {
				t.Fatalf("mirror exists before settle: %v", err)
			}
			if err := os.WriteFile(report, []byte("completed report\n"), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{command, "worker"}
			if command == "wait" {
				args = append(args, "--timeout", "1s")
			}
			code, out, msg := runIn(t, args, f.env, f.root)
			body, err := os.ReadFile(mirror)
			if code != 0 || err != nil || string(body) != "completed report\n" {
				t.Fatalf("settle=%d out=%s stderr=%s mirror=%q err=%v", code, out, msg, body, err)
			}
			before, err := os.Stat(mirror)
			if err != nil {
				t.Fatal(err)
			}
			code, out, msg = runIn(t, args, f.env, f.root)
			after, err := os.Stat(mirror)
			if code != 0 || err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("repeat settle changed mirror: code=%d out=%s stderr=%s err=%v", code, out, msg, err)
			}
			entries, err := os.ReadDir(filepath.Dir(mirror))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), ".mirror.sha256") || strings.HasPrefix(entry.Name(), ".soho-mirror-") {
					t.Fatalf("publication artifact remains: %s", entry.Name())
				}
			}
		})
	}
}
