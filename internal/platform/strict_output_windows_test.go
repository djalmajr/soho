//go:build windows

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Exercise the positive-timeout command-file branch, including its Job-backed
// native process, rather than the generic exec.Cmd capture or conversion alone.
func TestStrictOutputUTF8WindowsCommandFiles(t *testing.T) {
	for _, extension := range []string{".cmd", ".bat"} {
		for _, mode := range []string{"pipes", "merged", "files"} {
			for _, strict := range []bool{false, true} {
				for _, code := range []int{0, 7} {
					for _, tc := range []struct {
						name, out, err, legacyOut, legacyErr string
						invalid                              bool
					}{
						{"unicode", "é中🙂\r\n", "Ω\x00", "é中🙂\r\n", "Ω\x00", false},
						{"literal_replacement", "{\"metadata\":\"�\"}", "�-diagnostic", "{\"metadata\":\"�\"}", "�-diagnostic", false},
						{"bom", "\xef\xbb\xbf{}", "\xef\xbb\xbferr", "\xef\xbb\xbf{}", "\xef\xbb\xbferr", false},
						{"invalid_stdout", "{\"private\":\"\xff\"}", "{\"ready\":true}", "{\"private\":\"�\"}", "{\"ready\":true}", true},
						{"invalid_stderr", "{\"ready\":true}", "private-\xff", "{\"ready\":true}", "private-�", true},
						{"truncated", "\xe2\x82", "control", "�", "control", true},
						{"surrogate", "control", "\xed\xa0\x80", "control", "���", true},
					} {
						t.Run(fmt.Sprintf("%s/%s/strict=%v/code=%d/%s", extension, mode, strict, code, tc.name), func(t *testing.T) {
							root, tmp := t.TempDir(), t.TempDir()
							env, wrapper := strictWindowsFixture(t, root, tmp, extension, []fakecli.Rule{{Argv: []string{"output"}, StdoutBytes: []byte(tc.out), StderrBytes: []byte(tc.err), Code: code, Delay: 10}})
							started := 0
							got := RunCli("wrapper", []string{"output"}, RunOptions{Platform: "win32", Env: env, Cwd: root, Input: "stdin payload", TimeoutMs: 15000, StrictOutputUTF8: strict, MergeOutput: mode == "merged", OutputFiles: mode == "files", OnStart: func(pid int) { started = pid }})
							if started <= 0 || !strings.EqualFold(got.Resolved, wrapper) || got.Status == nil || *got.Status != code || got.Signal != "" || got.NotFound || got.TimedOut {
								t.Fatalf("native classification: started=%d resolved=%q status=%s signal=%q missing=%v timeout=%v error=%q target=windows/%s mode=%s", started, got.Resolved, treeKillStatus(got), got.Signal, got.NotFound, got.TimedOut, got.Error, extension, mode)
							}
							wantOut, wantErr, wantError := tc.legacyOut, tc.legacyErr, ""
							if strict {
								wantOut, wantErr = tc.out, tc.err
							}
							if mode == "merged" {
								wantOut, wantErr = wantOut+wantErr, ""
							}
							if strict && tc.invalid {
								wantOut, wantErr, wantError = "", "", "EILSEQ"
							}
							if got.Stdout != wantOut || got.Stderr != wantErr || got.Error != wantError {
								t.Fatalf("output/privacy mismatch: stdout bytes=%d stderr bytes=%d error=%q", len(got.Stdout), len(got.Stderr), got.Error)
							}
							calls, err := fakecli.ReadCalls(filepath.Join(root, "fixture.calls.jsonl"))
							if err != nil || len(calls) != 1 {
								t.Fatalf("wrapper calls=%d error=%v", len(calls), err)
							}
							if strings.Join(calls[0].Argv, "\x00") != "output" || calls[0].Stdin != "stdin payload" || !strings.EqualFold(calls[0].Cwd, root) {
								t.Fatal("wrapper argv, stdin or cwd changed")
							}
							strictWindowsCaptureClean(t, tmp)
						})
					}
				}
			}
		}
	}
}

func strictWindowsFixture(t *testing.T, root, tmp, extension string, rules []fakecli.Rule) (Env, string) {
	t.Helper()
	exe, err := fakecli.InstallWithOptions(t, root, "fixture", rules, fakecli.InstallOptions{CaptureStdin: true})
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(root, "wrapper"+extension)
	// Same native cmd.exe wrapper pattern as TestRunCliWindows; the payload
	// bytes come from the re-executed Go fake, never from cmd.exe's echo.
	if err := os.WriteFile(wrapper, []byte("@echo off\r\n\"%FAKECLI%\" %*\r\nexit /b %errorlevel%\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": tmp, "TEMP": tmp, "TMP": tmp, "PATH": root, "PATHEXT": ".CMD;.BAT;.EXE;.COM", "FAKECLI": exe, "SOHO_FAKECLI_CONFIG": filepath.Join(root, "fixture.json"), "GORACE": fakecli.RaceOptions(nil)}
	// Windows needs its native system directory, but no ambient PATH/config.
	for _, key := range []string{"SystemRoot", "WINDIR", "COMSPEC"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	if env.Get("COMSPEC") == "" {
		env["COMSPEC"] = filepath.Join(systemDirectory(env), "cmd.exe")
	}
	return env, wrapper
}

func TestStrictOutputUTF8WindowsCommandPathDelimiters(t *testing.T) {
	for _, extension := range []string{".cmd", ".bat"} {
		t.Run(extension, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "path=with space&meta")
			tmp := t.TempDir()
			args := []string{"output", "a=b", "space here", "x& echo INJECTED", "x| echo INJECTED", "x>injected", "50%", `quote"inside`}
			env, wrapper := strictWindowsFixture(t, root, tmp, extension, []fakecli.Rule{{Argv: args, Stdout: "exact output", Code: 7}})
			got := RunCli("wrapper", args, RunOptions{Platform: "win32", Env: env, Cwd: root, TimeoutMs: 15000, StrictOutputUTF8: true})
			if got.Status == nil || *got.Status != 7 || got.Stdout != "exact output" || got.Stderr != "" || got.Error != "" || got.Signal != "" || got.NotFound || got.TimedOut || !strings.EqualFold(got.Resolved, wrapper) {
				t.Fatalf("command path or hostile argv failed: status=%s error=%q signal=%q missing=%v timeout=%v output lengths=%d/%d", treeKillStatus(got), got.Error, got.Signal, got.NotFound, got.TimedOut, len(got.Stdout), len(got.Stderr))
			}
			calls, err := fakecli.ReadCalls(filepath.Join(root, "fixture.calls.jsonl"))
			if err != nil || len(calls) != 1 || strings.Join(calls[0].Argv, "\x00") != strings.Join(args, "\x00") {
				t.Fatalf("command-file argv changed: calls=%v error=%v", calls, err)
			}
			if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
				t.Fatalf("argument caused a redirection: %v", err)
			}
			strictWindowsCaptureClean(t, tmp)
		})
	}
}

func strictWindowsCaptureClean(t *testing.T, tmp string) {
	t.Helper()
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Fatalf("capture directory not empty: entries=%v error=%v", entries, err)
	}
}

// Cancellation happens after the fixture has emitted its bytes: OnStart is
// invoked while the native command is suspended, so pre-cancellation is not
// substituted for the output/error interaction under test.
func TestStrictOutputUTF8WindowsStoppedOutput(t *testing.T) {
	for _, timedOut := range []bool{false, true} {
		for _, mode := range []string{"pipes", "merged", "files"} {
			t.Run(fmt.Sprintf("%s/timeout=%v", mode, timedOut), func(t *testing.T) {
				root, tmp := t.TempDir(), t.TempDir()
				env, _ := strictWindowsFixture(t, root, tmp, ".cmd", []fakecli.Rule{{Argv: []string{"output"}, StdoutBytes: []byte("private-\xff"), Stderr: "misleading ready"}})
				// A native batch pause keeps the root alive after the Go child exits;
				// reading a fixture-created marker avoids a timing-only assertion.
				wrapper := filepath.Join(root, "hold.cmd")
				content := "@echo off\r\n\"%FAKECLI%\" %*\r\necho ready>\"%MARKER%\"\r\n:hold\r\ngoto hold\r\n"
				if err := os.WriteFile(wrapper, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(root, "emitted")
				env["MARKER"] = marker
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				observer := make(chan bool, 1)
				go func() {
					deadline := time.Now().Add(10 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(marker); err == nil {
							observer <- true
							if !timedOut {
								cancel()
							}
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
					observer <- false
					cancel()
				}()
				timeoutMs, wantError := 15000, "EILSEQ"
				if timedOut {
					timeoutMs, wantError = 5000, "ETIMEDOUT"
				}
				pid := 0
				got := RunCli("hold", []string{"output"}, RunOptions{Platform: "win32", Env: env, Context: ctx, TimeoutMs: timeoutMs, StrictOutputUTF8: true, MergeOutput: mode == "merged", OutputFiles: mode == "files", OnStart: func(id int) { pid = id }})
				observed := <-observer
				if !observed || got.Status != nil || got.Signal != "SIGTERM" || got.TimedOut != timedOut || got.NotFound || got.Error != wantError || got.Stdout != "" || got.Stderr != "" {
					t.Fatalf("cancelled output: observed=%v status=%s signal=%q timeout=%v missing=%v error=%q output lengths=%d/%d", observed, treeKillStatus(got), got.Signal, got.TimedOut, got.NotFound, got.Error, len(got.Stdout), len(got.Stderr))
				}
				if pid <= 0 {
					t.Fatal("native process was not started")
				}
				if active, err := processIsActive(uint32(pid)); err != nil || active {
					t.Fatalf("native process cleanup: pid=%d active=%v error=%v", pid, active, err)
				}
				strictWindowsCaptureClean(t, tmp)
			})
		}
	}
}

// Complement the real wrapper with the capture contract used on resume error:
// the existing fixed error wins and native metadata remains unchanged.
func TestStrictOutputUTF8WindowsCaptureEarlierError(t *testing.T) {
	for _, mode := range []string{"pipes", "merged", "files"} {
		for _, prior := range []string{"EINVAL", "ETIMEDOUT"} {
			t.Run(mode+"/"+prior, func(t *testing.T) {
				status := 7
				got := RunResult{Status: &status, Signal: "SIGTERM", TimedOut: true, NotFound: true, Error: prior}
				out, errOut := bytes.NewBufferString("private-\xff"), bytes.NewBufferString("private-stderr")
				var outFile, errFile *os.File
				if mode != "pipes" {
					var err error
					outFile, err = os.CreateTemp(t.TempDir(), "capture")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = outFile.Close() })
					if _, err := outFile.Write(out.Bytes()); err != nil {
						t.Fatal(err)
					}
					if mode == "files" {
						errFile, err = os.CreateTemp(t.TempDir(), "capture")
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = errFile.Close() })
						if _, err := errFile.Write(errOut.Bytes()); err != nil {
							t.Fatal(err)
						}
					}
				}
				captureWindowsOutput(&got, mode != "pipes", mode == "merged", true, outFile, errFile, out, errOut, nil, nil)
				if got.Status != &status || *got.Status != 7 || got.Signal != "SIGTERM" || !got.TimedOut || !got.NotFound || got.Error != prior || got.Stdout != "" || got.Stderr != "" {
					t.Fatal("capture changed prior classification or retained private output")
				}
			})
		}
	}
}
