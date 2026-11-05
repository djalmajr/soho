package platform

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestProcExecutableNative(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		if path, err := ProcExecutable(os.Getpid()); err == nil || path != "" {
			t.Fatalf("unsupported lookup = %q, %v", path, err)
		}
		return
	}
	path, err := ProcExecutable(os.Getpid())
	if err != nil || path == "" {
		t.Fatalf("native self lookup = %q, %v", path, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(self)
	if err != nil {
		t.Fatal(err)
	}
	if path != want {
		t.Fatalf("native path = %q, want exact resolved %q", path, want)
	}
}

func TestProcExecutableValidation(t *testing.T) {
	calls := 0
	restore := OverrideProcExecutableRunner(func(int) (string, error) { calls++; return "/fixture", nil })
	defer restore()
	pids := []int{-1, 0}
	if strconv.IntSize == 64 {
		tooLarge := int64(math.MaxInt32) + 1
		pids = append(pids, int(tooLarge))
	}
	for _, pid := range pids {
		if path, err := ProcExecutable(pid); err == nil || path != "" {
			t.Fatalf("invalid pid %d accepted: %q, %v", pid, path, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid PID invoked runner %d times", calls)
	}
	for _, pid := range []int{1, math.MaxInt32} {
		if path, err := ProcExecutable(pid); err != nil || path != "/fixture" {
			t.Fatalf("valid PID boundary %d: %q, %v", pid, path, err)
		}
	}
	sentinel := errors.New("unreadable executable")
	for _, tc := range []struct {
		name, path string
		err        error
		ok         bool
	}{
		{"read-error", "/must-not-match", sentinel, false},
		{"empty", "", nil, false},
		{"over-cap", strings.Repeat("x", ProcExecPathMax+1), nil, false},
		{"boundary", strings.Repeat("x", ProcExecPathMax), nil, true},
		{"embedded", "/a (deleted)/b", nil, true},
		{"suffix", "/a (deleted)", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			undo := OverrideProcExecutableRunner(func(int) (string, error) { return tc.path, tc.err })
			defer undo()
			path, err := ProcExecutable(1)
			if tc.ok {
				if err != nil || path != tc.path {
					t.Fatalf("valid result changed: len=%d err=%v", len(path), err)
				}
				return
			}
			if err == nil || path != "" {
				t.Fatalf("%s result accepted: len=%d err=%v", tc.name, len(path), err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("read cause lost: %v", err)
			}
		})
	}
	if path, err := ProcExecutable(1); err != nil || path != "/fixture" {
		t.Fatalf("nested restoration: %q, %v", path, err)
	}
}

func TestProcExecutableIndependentOfArguments(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("no native argument reader")
	}
	sentinel := errors.New("argument reader unavailable")
	old := procExecReadArguments
	procExecReadArguments = func(uintptr) ([]string, error) { return nil, sentinel }
	defer func() { procExecReadArguments = old }()
	if path, err := ProcExecutable(os.Getpid()); err != nil || path == "" {
		t.Fatalf("executable depended on argv: %q, %v", path, err)
	}
	if path, argv, err := ProcExec(os.Getpid()); !errors.Is(err, sentinel) || path != "" || argv != nil {
		t.Fatalf("ProcExec argument failure changed: %q, %v, %v", path, argv, err)
	}
	restore := OverrideProcExecutableRunner(func(int) (string, error) { return "/override", nil })
	defer restore()
	if path, err := ProcExecutable(os.Getpid()); path != "/override" || err != nil {
		t.Fatalf("override: %q, %v", path, err)
	}
	if _, _, err := ProcExec(os.Getpid()); !errors.Is(err, sentinel) {
		t.Fatalf("override affected ProcExec: %v", err)
	}
	procExecReadArguments = func(uintptr) ([]string, error) { return []string{"fixture", "argument with spaces"}, nil }
	path, argv, err := ProcExec(os.Getpid())
	if err != nil || path == "" || path == "/override" || len(argv) != 2 || argv[0] != "fixture" || argv[1] != "argument with spaces" {
		t.Fatalf("ProcExec success changed: %q, %v, %v", path, argv, err)
	}
	restore()
	if path, err := ProcExecutable(os.Getpid()); path == "/override" || path == "" || err != nil {
		t.Fatalf("native restoration: %q, %v", path, err)
	}
}
