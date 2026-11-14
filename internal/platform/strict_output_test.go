package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestStrictOutputUTF8(t *testing.T) {
	for _, mode := range []string{"pipes", "merged", "files"} {
		for _, strict := range []bool{false, true} {
			for _, code := range []int{0, 7} {
				for _, tc := range []struct {
					name, out, err, legacyOut, legacyErr string
					invalid                              bool
				}{
					{"unicode", "é中🙂\r\n", "Ω\x00", "é中🙂\r\n", "Ω\x00", false},
					{"literal replacement", "private-�", "�-diagnostic", "private-�", "�-diagnostic", false},
					{"bom", "\xef\xbb\xbf{}", "\xef\xbb\xbferr", "\xef\xbb\xbf{}", "\xef\xbb\xbferr", false},
					{"invalid stdout", "private-\xff", "private-stderr", "private-�", "private-stderr", true},
					{"invalid stderr", "private-stdout", "private-\xff", "private-stdout", "private-�", true},
					{"truncated", "\xe2\x82", "control", "�", "control", true},
					{"surrogate", "control", "\xed\xa0\x80", "control", "���", true},
				} {
					t.Run(fmt.Sprintf("%s/strict=%v/code=%d/%s", mode, strict, code, tc.name), func(t *testing.T) {
						root, tmp := t.TempDir(), t.TempDir()
						_, err := fakecli.Install(t, root, "fixture", []fakecli.Rule{{Argv: []string{"output"}, StdoutBytes: []byte(tc.out), StderrBytes: []byte(tc.err), Code: code}})
						if err != nil {
							t.Fatal(err)
						}
						env := Env{}
						for _, entry := range fakecli.Env([]string{"HOME=" + root, "USERPROFILE=" + root, "XDG_CONFIG_HOME=" + root, "TMPDIR=" + tmp}, root) {
							k, v, _ := strings.Cut(entry, "=")
							env[k] = v
						}
						got := RunCli("fixture", []string{"output"}, RunOptions{Env: env, StrictOutputUTF8: strict, MergeOutput: mode == "merged", OutputFiles: mode == "files"})
						if got.Status == nil || *got.Status != code || got.Signal != "" || got.NotFound || got.TimedOut {
							t.Fatalf("native classification: status=%v signal=%q missing=%v timeout=%v", got.Status, got.Signal, got.NotFound, got.TimedOut)
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
							t.Fatal("output bytes or fixed error differ from literal expectation")
						}
						if paths, err := filepath.Glob(filepath.Join(tmp, ".soho-out-*")); err != nil || len(paths) != 0 {
							t.Fatalf("temporary capture cleanup: %v %v", paths, err)
						}
						if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
							t.Fatalf("capture directory not empty: %v %v", entries, err)
						}
					})
				}
			}
		}
	}
}

func TestStrictOutputUTF8EarlierError(t *testing.T) {
	// The conversion must not replace native classification or an earlier timeout.
	status := 7
	r := RunResult{Status: &status, Signal: "SIGTERM", TimedOut: true, NotFound: true, Error: "ETIMEDOUT", Stdout: "private", Stderr: "private"}
	setRunOutput(&r, []byte{0xff}, []byte("private"), true)
	if r.Status != &status || *r.Status != 7 || r.Signal != "SIGTERM" || !r.TimedOut || !r.NotFound || r.Error != "ETIMEDOUT" || r.Stdout != "" || r.Stderr != "" {
		t.Fatal("strict conversion changed classification or retained output")
	}
}
