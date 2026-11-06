//go:build linux

package platform

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProcExecutableLinuxSuffix(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	// A separate native helper provides a startup handshake without consulting
	// argv or environment. EOF on its private stdin makes it exit normally.
	program := "package main\nimport \"os\"\nfunc main(){os.Stdout.WriteString(\"ready\\n\");var b [1]byte;os.Stdin.Read(b[:])}\n"
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "helper")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", helper, source)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native helper: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name, filename            string
		unlink, hardlink, refused bool
	}{
		{"linked-codex", "codex", false, false, false},
		{"linked-literal", "codex (deleted)", false, false, false},
		{"unlinked-codex", "codex", true, false, false},
		{"unlinked-literal", "codex (deleted)", true, false, false},
		{"linked-embedded", "codex (deleted) worker", false, false, false},
		{"unlinked-embedded", "codex (deleted) worker", true, false, false},
		{"ambiguous-hardlinked-deleted", "codex", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			in, err := os.Open(helper)
			if err != nil {
				t.Fatal(err)
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
			if err != nil {
				_ = in.Close()
				t.Fatal(err)
			}
			_, copyErr := io.Copy(out, in)
			closeErr := out.Close()
			_ = in.Close()
			if copyErr != nil || closeErr != nil {
				t.Fatalf("copy helper: %v %v", copyErr, closeErr)
			}
			if tc.hardlink {
				if err := os.Link(path, filepath.Join(dir, "retained-link")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, path)
			cmd.Env = []string{"HOME=" + dir, "USERPROFILE=" + dir, "XDG_CONFIG_HOME=" + dir, "TMPDIR=" + dir, "PATH=" + dir}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				_ = stdin.Close()
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				_ = stdin.Close()
				t.Fatal(err)
			}
			waited := false
			t.Cleanup(func() {
				_ = stdin.Close()
				if !waited {
					if err := cmd.Wait(); err != nil {
						t.Errorf("owned child cleanup: %v", err)
					}
				}
			})
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || line != "ready\n" {
				t.Fatalf("child startup: %q %v", line, err)
			}
			if tc.unlink {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ProcExecutable(cmd.Process.Pid)
			if tc.refused {
				if err == nil || got != "" {
					t.Fatalf("ambiguous executable fabricated path: %q %v", got, err)
				}
			} else if err != nil || got != path {
				t.Fatalf("native %s path=%q err=%v want exact %q", tc.name, got, err, path)
			}
			raw, err := procExecutableRaw(cmd.Process.Pid)
			wantRaw := path
			if tc.unlink {
				wantRaw += " (deleted)"
			}
			if err != nil || raw != wantRaw {
				t.Fatalf("raw executable changed: %q %v want %q", raw, err, wantRaw)
			}
			_ = stdin.Close()
			err = cmd.Wait()
			waited = true
			if err != nil {
				t.Fatalf("owned child exit: %v", err)
			}
			if got, err := ProcExecutable(cmd.Process.Pid); err == nil || got != "" {
				t.Fatalf("dead process supplied path: %q %v", got, err)
			}
		})
	}
}
