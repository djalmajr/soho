package peer_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func dja651ScreenSecurityFixture(t *testing.T) (f *fixture, id, prompt, screen string) {
	t.Helper()
	id, prompt = stalledID(t)
	screen = "PRIVATE-DJA651-DIAGNOSTIC\n" + codexPreScreen(t)
	p := dja651PromptRule(prompt)
	p.Code, p.Stderr = 1, stalledPromptErr
	f = newFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "working", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: screen},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "old history without receipt\n"},
		p,
	})
	dja651Isolate(f)
	return f, id, prompt, screen
}

func dja651ScreenSecuritySend(t *testing.T, f *fixture, id, prompt, screen string, saved bool) string {
	t.Helper()
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
	path := filepath.Join(dja651StateDir(f), "wait", "send-"+id+".screen")
	want := "soho: send: local/w0test:p0a did not confirm taking the message (agent_prompt_stalled: stalled); delivery is uncertain: no proof within the 0.001s window; read its pane for #" + id + " or a reply before sending again"
	if saved {
		want += "; screen saved to " + path
	}
	if code != 15 || out != "" || stderr != want+"\n" {
		t.Errorf("stalled consumer diagnostic: code=%d out=%q stderr=%q; want exit15, empty stdout and %q", code, out, stderr, want+"\n")
	}
	dja651Log(t, dja651StateDir(f), "stalled", id, "local/-", "local/w0test:p0a")
	dja651Screen(t, dja651StateDir(f), id, screen, out, stderr, saved)
	dja651Submission(t, f, prompt, 0)
	return path
}

func TestDJA651ScreenSecurityUnixModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix 0600/0700 contract; Windows inherited ACLs require separate native measurement")
	}
	for _, existing := range []bool{false, true} {
		name := "new_wait_directory"
		if existing {
			name = "existing_broad_wait_directory"
		}
		t.Run(name, func(t *testing.T) {
			f, id, prompt, screen := dja651ScreenSecurityFixture(t)
			waitDir := filepath.Join(dja651StateDir(f), "wait")
			marker := filepath.Join(waitDir, "unrelated-marker")
			const markerBytes = "existing shared marker\n"
			if existing {
				if err := os.MkdirAll(waitDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, []byte(markerBytes), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(waitDir, 0o777); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(waitDir)
				if err != nil || info.Mode().Perm() != 0o777 {
					t.Fatalf("broad-directory fixture was not established: info=%v err=%v", info, err)
				}
			}
			path := dja651ScreenSecuritySend(t, f, id, prompt, screen, true)
			// Permission evidence comes from the consumer's actual artifact,
			// after its bytes/path and existing delivery contract are checked.
			artifact, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !artifact.Mode().IsRegular() || artifact.Mode().Perm() != 0o600 {
				t.Errorf("saved screen mode=%v; want regular owner-only 0600", artifact.Mode())
			}
			dir, err := os.Lstat(waitDir)
			if err != nil {
				t.Fatal(err)
			}
			if !dir.IsDir() || dir.Mode().Perm() != 0o700 {
				t.Errorf("diagnostic directory mode=%v; want private 0700", dir.Mode())
			}
			if existing {
				if raw, err := os.ReadFile(marker); err != nil || string(raw) != markerBytes {
					t.Errorf("tightening the owned directory changed unrelated marker bytes: %q %v", raw, err)
				}
			}
		})
	}
}

func TestDJA651ScreenSecuritySymlinkWaitDirectory(t *testing.T) {
	f, id, prompt, screen := dja651ScreenSecurityFixture(t)
	sd := dja651StateDir(f)
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	// This sibling TempDir is owned exclusively by this test; no user path
	// or session directory is the escape target.
	outside := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(outside, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(outside, "outside-sentinel")
	const markerBytes = "outside fixture must remain untouched\n"
	if err := os.WriteFile(marker, []byte(markerBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeDir, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	beforeMarker, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	waitDir := filepath.Join(sd, "wait")
	if err := os.Symlink(outside, waitDir); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("native Windows symlink creation unavailable: %v; inherited ACL measurement remains separate", err)
		}
		t.Fatal(err)
	}
	dja651ScreenSecuritySend(t, f, id, prompt, screen, false)
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 || entries[0].Name() != "outside-sentinel" {
		t.Errorf("symlinked wait directory allowed writes outside the owned state tree: entries=%v err=%v", entries, err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != markerBytes {
		t.Errorf("outside sentinel bytes changed: %q %v", raw, err)
	}
	afterDir, err := os.Stat(outside)
	if err != nil || afterDir.Mode() != beforeDir.Mode() {
		t.Errorf("outside directory permissions changed: before=%v after=%v err=%v", beforeDir.Mode(), afterDir, err)
	}
	afterMarker, err := os.Stat(marker)
	if err != nil || afterMarker.Mode() != beforeMarker.Mode() {
		t.Errorf("outside sentinel permissions changed: before=%v after=%v err=%v", beforeMarker.Mode(), afterMarker, err)
	}
	if target, err := os.Readlink(waitDir); err != nil || target != outside {
		t.Errorf("unsafe wait symlink was changed: target=%q err=%v", target, err)
	}
}

func TestDJA651ScreenSecurityNOWRITE(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new_wait_directory"
		if existing {
			name = "existing_wait_directory"
		}
		t.Run(name, func(t *testing.T) {
			f, _, _, _ := dja651ScreenSecurityFixture(t)
			sd := dja651StateDir(f)
			if err := os.MkdirAll(sd, 0o700); err != nil {
				t.Fatal(err)
			}
			waitDir := filepath.Join(sd, "wait")
			var before os.FileInfo
			if existing {
				if err := os.Mkdir(waitDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" {
					if err := os.Chmod(waitDir, 0o777); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				before, err = os.Stat(waitDir)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(waitDir, "unrelated-marker"), []byte("keep\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f.env["SOHO_NOWRITE"] = "1"
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "hello"})
			if code != 2 || out != "" || !strings.Contains(stderr, "SOHO_NOWRITE=1 is read-only") || strings.Contains(stderr, ".screen") {
				t.Errorf("NOWRITE refusal changed: code=%d out=%q stderr=%q", code, out, stderr)
			}
			for _, path := range []string{filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), filepath.Join(sd, peer.PeerLogFile)} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("NOWRITE caused a CLI call or delivery log: %s: %v", path, err)
				}
			}
			if !existing {
				if _, err := os.Stat(waitDir); !os.IsNotExist(err) {
					t.Errorf("NOWRITE created the diagnostic directory: %v", err)
				}
				return
			}
			after, err := os.Stat(waitDir)
			if err != nil || after.Mode() != before.Mode() {
				t.Errorf("NOWRITE changed diagnostic-directory permissions: before=%v after=%v err=%v", before.Mode(), after, err)
			}
			if entries, err := os.ReadDir(waitDir); err != nil || len(entries) != 1 || entries[0].Name() != "unrelated-marker" {
				t.Errorf("NOWRITE changed diagnostic entries: %v %v", entries, err)
			}
			if raw, err := os.ReadFile(filepath.Join(waitDir, "unrelated-marker")); err != nil || string(raw) != "keep\n" {
				t.Errorf("NOWRITE changed marker bytes: %q %v", raw, err)
			}
		})
	}
}
