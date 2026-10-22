package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func Test_dispatch_hints_cwd_unknown_option(t *testing.T) {
	// JS: "dispatch: --cwd exits 2 pointing at spawn; another unknown option keeps today's message"
	t.Run("--cwd exits 2 with the spawn pointer", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		code, out, stderr := f.run(t, "worker", f.brief, "--cwd", "/tmp/work")
		want := "soho: dispatch: unknown option --cwd (the worker's directory is set when it is opened: spawn <role> --cwd <dir>, then dispatch to it)\n"
		if code != 2 || strings.TrimSpace(out) != "" || stderr != want {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("--cwd before the agent exits 2 with the spawn pointer", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		code, out, stderr := f.run(t, "--cwd", "/tmp/work", "worker", f.brief)
		want := "soho: dispatch: unknown option --cwd (the worker's directory is set when it is opened: spawn <role> --cwd <dir>, then dispatch to it)\n"
		if code != 2 || strings.TrimSpace(out) != "" || stderr != want {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("--foo keeps today's message", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		code, out, stderr := f.run(t, "worker", f.brief, "--foo")
		if code != 2 || strings.TrimSpace(out) != "" || stderr != "soho: dispatch: unknown option --foo\n" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

func Test_dispatch_hints_glob_overlap(t *testing.T) {
	// JS: "dispatch: a leading-star glob crosses only what it matches (the D14 false positive is gone)"
	t.Run("pathsCross: a leading-star glob does not cross plain files it does not match", func(t *testing.T) {
		if pathsCross("*.workers.test.ts", "src/a.ts") || pathsCross("*.workers.test.ts", "src/b.test.ts") {
			t.Fatal("the leading-star glob crossed files it does not match")
		}
		if pathsCross("src/a.ts", "*.workers.test.ts") || pathsCross("src/b.test.ts", "*.workers.test.ts") {
			t.Fatal("the leading-star glob crossed a plain file from the other side")
		}
		if !pathsCross("src/*.ts", "src/a.ts") || !pathsCross("src/a.ts", "src/*.ts") {
			t.Fatal("a glob that matches the file stopped crossing it")
		}
		if pathsCross("*.workers.test.ts", "src/*.spec.ts") {
			t.Fatal("globs with incompatible extensions crossed")
		}
		if !pathsCross("*.workers.test.ts", "*.test.ts") {
			t.Fatal("globs that could match the same file stopped crossing")
		}
	})
	// JS: "dispatch: the owned-files overlap warn is silent for a leading-star glob and fires for a matching glob"
	t.Run("the owned-files overlap warn follows the corrected crossing", func(t *testing.T) {
		overlap := func(t *testing.T, mine, theirs string) string {
			t.Helper()
			h := newTM3bHarness(t, strings.Replace(tm3bFullBrief(), "internal/a.go", mine, 1))
			for _, dir := range []string{filepath.Join(h.ws, "briefs"), filepath.Join(h.ws, "reports")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			pending := filepath.Join(h.ws, "briefs", "peer.md")
			if err := os.WriteFile(pending, []byte("# Brief\n# Owned files\n"+theirs+"\n# Report contract\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(h.ws, "reports", "peer.md")
			if err := os.WriteFile(filepath.Join(h.ws, "last-report-peer"), []byte(report+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			roster := "peer\tw0test:p0b\tcodex\timplementer\topenai\t0\t" + h.root + "\tnow\tgpt-5\ttask\timplementer\n"
			if err := os.WriteFile(filepath.Join(h.ws, "agents.tsv"), []byte(roster), 0o600); err != nil {
				t.Fatal(err)
			}
			oldErr := platform.Stderr
			var stderr bytes.Buffer
			platform.Stderr = &stderr
			t.Cleanup(func() { platform.Stderr = oldErr })
			warnOwnedOverlap(h.brief, "implementer", "build", h.ws, h.ctx, h.env, h.root, h.root)
			return stderr.String()
		}
		t.Run("a leading-star glob against plain files it does not match does not warn (the D14 case)", func(t *testing.T) {
			if got := overlap(t, "- *.workers.test.ts", "- src/a.ts\n- src/b.test.ts"); strings.Contains(got, "owns files") {
				t.Fatalf("false overlap warning: %q", got)
			}
		})
		t.Run("a glob that matches a file of the other brief still warns", func(t *testing.T) {
			got := overlap(t, "- src/*.ts", "- src/a.ts\n- src/b.test.ts")
			if !strings.Contains(got, "owns files that 'peer' is still editing: src/*.ts") {
				t.Fatalf("matching-glob warning missing: %q", got)
			}
		})
	})
}
