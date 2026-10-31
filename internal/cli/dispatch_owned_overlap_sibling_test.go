package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// DJA-636: the owned-files overlap warning must not flag sibling folders.
// The skedly false positive reported the new brief's folder
// .soho/w6S/qa/namebreak/r2/ (missing on disk) as an overlap with the busy
// brief's sibling folder .soho/w6S/qa/shared-disabledreason/ plus four files.
// Siblings never cross: neither folder contains the other.

const dja636BusyOwned = "- .soho/w6S/qa/shared-disabledreason/\n" +
	"- src/ui/components/ui/button.tsx\n" +
	"- src/ui/components/ui/dropdown-menu.tsx\n" +
	"- src/ui/components/ui/button.test.tsx\n" +
	"- src/ui/components/ui/dropdown-menu.test.tsx\n"

// dja636WarnFixture runs the reported shape through warnOwnedOverlap, the
// real warning path, creating extraDirs (relative to the fixture root) first.
// It returns the fixture root (so an absolute owned entry can be built
// against the right base) and the captured stderr.
func dja636WarnFixture(t *testing.T, mine, theirs string, extraDirs ...string) (string, string) {
	t.Helper()
	h := newTM3bHarness(t, strings.Replace(tm3bFullBrief(), "internal/a.go", mine, 1))
	for _, dir := range []string{filepath.Join(h.ws, "briefs"), filepath.Join(h.ws, "reports")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range extraDirs {
		if err := os.MkdirAll(filepath.Join(h.root, dir), 0o700); err != nil {
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
	var stderr strings.Builder
	platform.Stderr = &stderr
	t.Cleanup(func() { platform.Stderr = oldErr })
	warnOwnedOverlap(h.brief, "implementer", "build", h.ws, h.ctx, h.env, h.root, h.root)
	return h.root, stderr.String()
}

// dja636DispatchFixture runs the reported shape through the full cmdDispatch
// path (the real dispatch) and returns (code, stderr).
func dja636DispatchFixture(t *testing.T, mine, theirs string) (int, string) {
	t.Helper()
	h := newTM3bHarness(t, strings.Replace(tm3bFullBrief(), "internal/a.go", mine, 1))
	for _, dir := range []string{filepath.Join(h.ws, "briefs"), filepath.Join(h.ws, "reports")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.ws, "briefs", "peer.md"), []byte("# Brief\n# Owned files\n"+theirs+"\n# Report contract\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws, "last-report-peer"), []byte(filepath.Join(h.ws, "reports", "peer.md")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rosterPath := filepath.Join(h.ws, "agents.tsv")
	roster, err := os.ReadFile(rosterPath)
	if err != nil {
		t.Fatal(err)
	}
	peer := "peer\tw0test:p0b\tcodex\timplementer\topenai\t0\t" + h.root + "\tnow\tgpt-5\ttask\timplementer\t\t\t\t\n"
	if err := os.WriteFile(rosterPath, append(roster, []byte(peer)...), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := h.run(t, "build", h.brief, "--no-wait")
	return code, stderr
}

func TestDJA636SiblingFoldersDoNotCross(t *testing.T) {
	// JS: "pathsCross: two sibling folders under the same parent never cross"
	t.Run("pathsCross: two sibling folders under the same parent never cross", func(t *testing.T) {
		for _, pair := range [][2]string{
			{".soho/w6S/qa/namebreak/r2/", ".soho/w6S/qa/shared-disabledreason/"},
			{".soho/w6S/qa/shared-disabledreason/", ".soho/w6S/qa/namebreak/r2/"},
			{".soho/w6S/qa/namebreak/r2", ".soho/w6S/qa/shared-disabledreason"},
			{".soho/w6S/qa/namebreak/r2/", ".soho/w6S/qa/shared-disabledreason"},
		} {
			if pathsCross(pair[0], pair[1]) {
				t.Errorf("pathsCross(%q, %q)=true; siblings never cross", pair[0], pair[1])
			}
		}
	})
	// JS: "dispatch: the reported sibling shape through the warn path stays silent"
	t.Run("dispatch: the reported sibling shape through the warn path stays silent", func(t *testing.T) {
		// The r2 folder does not exist on disk yet (missing on disk is the
		// reported condition); the sibling folder exists. Neither fact may
		// change the comparison.
		_, got := dja636WarnFixture(t, "- .soho/w6S/qa/namebreak/r2/", dja636BusyOwned, ".soho/w6S/qa/shared-disabledreason")
		if strings.Contains(got, "owns files") {
			t.Fatalf("sibling folders crossed: %q", got)
		}
	})
	// JS: "dispatch: the reported shape with both folders present stays silent"
	t.Run("dispatch: the reported shape with both folders present stays silent", func(t *testing.T) {
		_, got := dja636WarnFixture(t, "- .soho/w6S/qa/namebreak/r2/", dja636BusyOwned, ".soho/w6S/qa/namebreak/r2", ".soho/w6S/qa/shared-disabledreason")
		if strings.Contains(got, "owns files") {
			t.Fatalf("sibling folders crossed: %q", got)
		}
	})
	// JS: "dispatch: the reported shape with an absolute owned folder stays silent"
	t.Run("dispatch: the reported shape with an absolute owned folder stays silent", func(t *testing.T) {
		// The skedly message showed the hit as an absolute path, so the new
		// brief listed the folder in absolute form. Owned paths are compared
		// as strings and never resolved on disk, so any absolute base
		// exercises the case; siblings still never cross, whatever base the
		// entries use.
		abs := "/projects/skedly/.soho/w6S/qa"
		absMine := "- `" + abs + "/namebreak/r2/`\n"
		_, got := dja636WarnFixture(t, absMine, dja636BusyOwned)
		if strings.Contains(got, "owns files") {
			t.Fatalf("absolute sibling folder crossed: %q", got)
		}
		// And with both sides absolute (the busy brief's entries rebased to
		// the same project root).
		absTheirs := strings.ReplaceAll(dja636BusyOwned, ".soho/", abs+"/")
		_, got = dja636WarnFixture(t, absMine, absTheirs)
		if strings.Contains(got, "owns files") {
			t.Fatalf("all-absolute siblings crossed: %q", got)
		}
	})
	// JS: "dispatch: the reported shape through the full dispatch stays silent"
	t.Run("dispatch: the reported shape through the full dispatch stays silent", func(t *testing.T) {
		code, stderr := dja636DispatchFixture(t, "- .soho/w6S/qa/namebreak/r2/", dja636BusyOwned)
		if code != 0 {
			t.Fatalf("dispatch code=%d stderr=%s", code, stderr)
		}
		if strings.Contains(stderr, "owns files") {
			t.Fatalf("sibling folders crossed through the full dispatch: %q", stderr)
		}
	})
	// JS: "dispatch: the fixture still warns on a true overlap"
	t.Run("dispatch: the fixture still warns on a true overlap", func(t *testing.T) {
		// Positive control: the same file in both briefs is a documented
		// crossing, so the fixture above cannot be silent by accident.
		_, got := dja636WarnFixture(t, "- src/ui/components/ui/button.tsx", "- src/ui/components/ui/button.tsx\n")
		if !strings.Contains(got, "owns files that 'peer' is still editing: src/ui/components/ui/button.tsx") {
			t.Fatalf("true overlap did not warn: %q", got)
		}
		_, got = dja636WarnFixture(t, "- .soho/w6S/qa/namebreak/r2/", "- .soho/w6S/qa/namebreak/r2/\n")
		if !strings.Contains(got, "owns files that 'peer' is still editing: .soho/w6S/qa/namebreak/r2/") {
			t.Fatalf("equal folders did not warn: %q", got)
		}
	})
}
