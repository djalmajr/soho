package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// retryReleaseTransport exercises Run without a listener or external network.
type retryReleaseTransport struct{}

func (retryReleaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	switch req.URL.Path {
	case "/download/v1.2.3/soho_windows_amd64.exe":
		body = fakeBinary
	case "/download/v1.2.3/SHA256SUMS":
		body = sha256Hex([]byte(fakeBinary)) + "  soho_windows_amd64.exe\n"
	default:
		return nil, fmt.Errorf("unexpected release request: %s", req.URL.Path)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

func TestInstallRenameRetry(t *testing.T) {
	for _, stage := range []string{"move", "install", "undo"} {
		for _, mode := range []string{"transient", "exhausted", "other"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
					t.Setenv(key, dir)
				}
				destination := filepath.Join(dir, "soho.exe")
				aside := destination + ".old"
				writeFiles(t, dir, map[string]string{"soho.exe": "previous binary"})
				sharing := errors.New("simulated sharing violation")
				other := errors.New("simulated non-sharing failure")
				savedClassify, savedSleep := retryRenameError, sleepRenameRetry
				t.Cleanup(func() { retryRenameError, sleepRenameRetry = savedClassify, savedSleep })
				retryRenameError = func(err error) bool { return errors.Is(err, sharing) }
				var sleeps []time.Duration
				sleepRenameRetry = func(d time.Duration) { sleeps = append(sleeps, d) }
				attempts, direct := 0, 0
				moved := false
				last := error(nil)
				useReplaceSeams(t, true, func(src, dst string) error {
					if dst == destination && src != aside && !moved {
						direct++
						// Even a sharing violation must not retry the direct rename.
						return sharing
					}
					current := "install"
					if src == destination {
						current = "move"
					} else if src == aside {
						current = "undo"
					}
					if stage == "undo" && current == "install" {
						return other
					}
					if current == stage {
						attempts++
						if mode != "transient" || attempts <= 3 {
							cause := sharing
							if mode == "other" {
								cause = other
							}
							last = &os.LinkError{Op: "rename", Old: src, New: dst, Err: fmt.Errorf("attempt %d: %w", attempts, cause)}
							return last
						}
					}
					if err := os.Rename(src, dst); err != nil {
						return err
					}
					if current == "move" {
						moved = true
					}
					return nil
				}, nil)
				res, err := Run(context.Background(), Options{OS: "windows", Architecture: "amd64", Version: "v1.2.3", ReleaseBase: "https://release.invalid", InstallDir: dir, Client: &http.Client{Transport: retryReleaseTransport{}}})
				wantAttempts := 4
				wantSleeps := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
				if mode == "exhausted" {
					wantAttempts = 10
					wantSleeps = append(wantSleeps, 400*time.Millisecond, 400*time.Millisecond, 400*time.Millisecond, 400*time.Millisecond, 400*time.Millisecond, 400*time.Millisecond)
				} else if mode == "other" {
					wantAttempts, wantSleeps = 1, nil
				}
				if direct != 1 || attempts != wantAttempts || !reflect.DeepEqual(sleeps, wantSleeps) {
					t.Fatalf("direct=%d attempts=%d sleeps=%v; want 1, %d, %v; install err=%v", direct, attempts, sleeps, wantAttempts, wantSleeps, err)
				}
				var total time.Duration
				for _, d := range sleeps {
					total += d
				}
				if total > 3*time.Second {
					t.Fatalf("retry waiting=%v exceeds 3s", total)
				}
				if mode == "transient" && stage != "undo" {
					if err != nil || res.Previous != aside || res.PreviousMarker != aside+asideMarkerSuffix {
						t.Fatalf("result=%+v err=%v", res, err)
					}
					assertFile(t, destination, fakeBinary)
					assertFile(t, aside, "previous binary")
					assertFile(t, aside+asideMarkerSuffix, markerFor("previous binary"))
					assertDirExact(t, dir, "soho.exe", "soho.exe.old", "soho.exe.old.soho-install")
					return
				}
				if err == nil {
					t.Fatal("expected replacement failure")
				}
				if mode != "transient" && !strings.Contains(err.Error(), last.Error()) {
					t.Fatalf("error lost last rename failure: %v; want %v", err, last)
				}
				if stage == "undo" && mode != "transient" {
					if !strings.Contains(err.Error(), "the previous binary is at "+aside) {
						t.Fatalf("missing stranded backup diagnostic: %v", err)
					}
					assertFile(t, aside, "previous binary")
					assertDirExact(t, dir, "soho.exe.old")
				} else {
					assertFile(t, destination, "previous binary")
					assertDirExact(t, dir, "soho.exe")
				}
			})
		}
	}
}

func TestTransientRenameClassification(t *testing.T) {
	for _, code := range []syscall.Errno{5, 32, 33, 2} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("errno=%d/wrapped=%t", code, wrapped), func(t *testing.T) {
				var err error = code
				if wrapped {
					err = &os.LinkError{Op: "rename", Old: "old", New: "new", Err: fmt.Errorf("wrapped: %w", code)}
				}
				want := runtime.GOOS == "windows" && (code == 32 || code == 33)
				if got := isTransientRenameError(err); got != want {
					t.Fatalf("classification=%t want=%t on %s: %v", got, want, runtime.GOOS, err)
				}
			})
		}
	}
	if isTransientRenameError(nil) || isTransientRenameError(errors.New("sharing violation")) {
		t.Fatal("nil or message-only error classified as transient")
	}
}
