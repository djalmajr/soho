package wait

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWaitSettledCwdGoneCause(t *testing.T) {
	for _, name := range []string{"removed", "present", "file", "empty", "permission"} {
		t.Run(name, func(t *testing.T) {
			f := newSettledFixture(t, "grok", "idle prompt\n")
			cwd := filepath.Join(t.TempDir(), "worker tree ; literal")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			if name == "removed" || name == "file" {
				if err := os.Remove(cwd); err != nil {
					t.Fatal(err)
				}
			}
			if name == "file" {
				if err := os.WriteFile(cwd, []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "empty" {
				cwd = ""
			}
			if name == "permission" {
				old := WorkerCwdStat
				WorkerCwdStat = func(path string) (os.FileInfo, error) {
					if path == cwd {
						return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrPermission}
					}
					return os.Stat(path)
				}
				t.Cleanup(func() { WorkerCwdStat = old })
			}
			roster := filepath.Join(f.sd, "agents.tsv")
			raw, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			cols := strings.Split(lines[1], "\t")
			for len(cols) < 7 {
				cols = append(cols, "")
			}
			cols[6] = cwd
			lines[1] = strings.Join(cols, "\t")
			if err = os.WriteFile(roster, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			code, out := f.waitForRun(t, 10000)
			line := settledLine(t, out)
			gone := name == "removed" || name == "file"
			if code != 6 {
				t.Fatalf("exit=%d output=%s", code, out)
			}
			if gone {
				want := "; its working directory " + cwd + " no longer exists"
				if line["cause"] != want {
					t.Fatalf("cause=%v want=%s output=%s", line["cause"], want, out)
				}
			} else if _, exists := line["cause"]; exists {
				t.Fatalf("control cause changed: %s", out)
			}
			t.Logf("case=%s exit=%d JSON=%s", name, code, out)
		})
	}
}
