package core

// F2 auxiliary confined reads: the bounded directory enumeration
// (ReadDir), the approved-location file reads (ReadAuxFile) and the
// protected mirror observation (StateReportMirror) behave on the
// inspection path as the briefs require — plain directory entries only,
// a capped enumeration that never truncates, approved locations refused
// without a read, absence as os.ErrNotExist, unsafe present entries as
// a refusal (never absence), and the mirror's owner rule.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuxReadDir(t *testing.T) {
	// (a) The absent component is os.ErrNotExist.
	base, _ := sessionFixture(t, "s1")
	s := openSource(t, base, "s1", "")
	if _, err := s.ReadDir("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent ReadDir = %v, want os.ErrNotExist", err)
	}
	// (b) Only the plain directory entries come back: a regular file,
	// a link to a directory and a nested non-directory are never
	// listed.
	sessionDir := filepath.Join(base, "s1")
	if err := os.MkdirAll(filepath.Join(sessionDir, "collaboration", "c-abcdef1234567890"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, "collaboration", "plain-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "collaboration", "regular"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(sessionDir, "collaboration", "linked")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	names, err := s.ReadDir("collaboration")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	want := map[string]bool{"c-abcdef1234567890": true, "plain-dir": true}
	if len(names) != len(want) {
		t.Fatalf("ReadDir names = %v, want exactly %v", names, want)
	}
	for _, name := range names {
		if !want[name] {
			t.Fatalf("ReadDir names = %v, includes the non-plain entry %q", names, name)
		}
	}
	// (c) A regular file is not an enumeration root.
	if err := os.WriteFile(filepath.Join(sessionDir, "regular-entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDir("regular-entry"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadDir over a regular file = %v, want a refusal that is not absence", err)
	}
	// (d) The enumeration is capped: a directory over the 4096-entry
	// cap refuses, and is never truncated.
	big := filepath.Join(sessionDir, "big")
	if err := os.MkdirAll(big, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4097; i++ {
		if err := os.WriteFile(filepath.Join(big, filepath.Base(strings.Repeat("e", 1)+itoaName(i))), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if names, err := s.ReadDir("big"); err == nil {
		t.Fatalf("ReadDir over the cap returned %d names, want a refusal", len(names))
	} else if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadDir over the cap collapsed to absence: %v", err)
	}
}

// itoaName renders an entry index as a fixed-width name without an
// extra import.
func itoaName(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0000"
	}
	var b [4]byte
	for pos := 3; pos >= 0; pos-- {
		b[pos] = digits[i%10]
		i /= 10
	}
	return string(b[:])
}

func TestAuxReadFile(t *testing.T) {
	base, sessionDir := sessionFixture(t, "s1")
	scratch := t.TempDir()
	s := openSource(t, base, "s1", scratch)
	// (a) Approved state locations: the root level, briefs, reports.
	if err := os.WriteFile(filepath.Join(sessionDir, "last-report-worker"), []byte("state-root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, "briefs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "briefs", "report.md"), []byte("brief\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "reports", "worker-final.md"), []byte("mirror\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(sessionDir, "last-report-worker"):         "state-root\n",
		filepath.Join(sessionDir, "briefs", "report.md"):        "brief\n",
		filepath.Join(sessionDir, "reports", "worker-final.md"): "mirror\n",
	} {
		got, err := s.ReadAuxFile(path, 64<<10)
		if err != nil || got != want {
			t.Fatalf("ReadAuxFile(%s) = %q, %v; want %q", path, got, err, want)
		}
	}
	// (b) The approved scratch locations, through the retained scratch
	// root.
	scratchSession := filepath.Join(scratch, "soho", "s1")
	if err := os.MkdirAll(filepath.Join(scratchSession, "briefs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratchSession, "composed.md"), []byte("scratch-root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratchSession, "briefs", "brief.md"), []byte("scratch-brief\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(scratchSession, "composed.md"):        "scratch-root\n",
		filepath.Join(scratchSession, "briefs", "brief.md"): "scratch-brief\n",
	} {
		got, err := s.ReadAuxFile(path, 64<<10)
		if err != nil || got != want {
			t.Fatalf("ReadAuxFile(%s) = %q, %v; want %q", path, got, err, want)
		}
	}
	// (c) A location outside the approved ones is refused without a
	// read (the wait dir, the collaboration dir, and a parent escape).
	for _, path := range []string{
		filepath.Join(sessionDir, "wait", "worker.queued"),
		filepath.Join(sessionDir, "collaboration", "c-abcdef1234567890", "assignment.json"),
		filepath.Join(base, "s2", "last-report-worker"),
	} {
		if _, err := s.ReadAuxFile(path, 64<<10); err == nil {
			t.Fatalf("ReadAuxFile accepted the unapproved location %s", path)
		} else if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ReadAuxFile(%s) collapsed to absence: %v", path, err)
		}
	}
	// (d) Absence is os.ErrNotExist.
	if _, err := s.ReadAuxFile(filepath.Join(sessionDir, "briefs", "absent.md"), 64<<10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent ReadAuxFile = %v, want os.ErrNotExist", err)
	}
	// (e) A present file over the cap refuses.
	if err := os.WriteFile(filepath.Join(sessionDir, "briefs", "big.md"), []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadAuxFile(filepath.Join(sessionDir, "briefs", "big.md"), 1024); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversize ReadAuxFile = %v, want a refusal that is not absence", err)
	}
	// (f) A link to the approved content is refused (the confined read
	// requires a plain regular file).
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "real.md"), []byte("real\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "real.md"), filepath.Join(sessionDir, "briefs", "linked.md")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	if _, err := s.ReadAuxFile(filepath.Join(sessionDir, "briefs", "linked.md"), 64<<10); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("linked ReadAuxFile = %v, want a refusal that is not absence", err)
	}
}

func TestAuxStateReportMirror(t *testing.T) {
	report := filepath.Join(t.TempDir(), "scratch", "reports", "worker-final.md")
	// (a) The valid mirror stands: the state reports path is returned.
	base, sessionDir := sessionFixture(t, "s1")
	s := openSource(t, base, "s1", "")
	if err := os.MkdirAll(filepath.Join(sessionDir, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "reports", "worker-final.md"), []byte("mirror\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.StateReportMirror(report, "worker-final.md"); got != s.StateReportsPath("worker-final.md") {
		t.Fatalf("mirror = %q, want the state reports path %q", got, s.StateReportsPath("worker-final.md"))
	}
	// (b) An empty mirror does not stand: the report's own path.
	base2, sessionDir2 := sessionFixture(t, "s1")
	s2 := openSource(t, base2, "s1", "")
	if err := os.MkdirAll(filepath.Join(sessionDir2, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir2, "reports", "worker-final.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s2.StateReportMirror(report, "worker-final.md"); got != report {
		t.Fatalf("empty mirror = %q, want the report's own path", got)
	}
	// (c) A linked mirror does not stand.
	base3, sessionDir3 := sessionFixture(t, "s1")
	s3 := openSource(t, base3, "s1", "")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "real.md"), []byte("real\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir3, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "real.md"), filepath.Join(sessionDir3, "reports", "worker-final.md")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	if got := s3.StateReportMirror(report, "worker-final.md"); got != report {
		t.Fatalf("linked mirror = %q, want the report's own path", got)
	}
	// (d) No mirror at all: the report's own path.
	base4, _ := sessionFixture(t, "s1")
	s4 := openSource(t, base4, "s1", "")
	if got := s4.StateReportMirror(report, "worker-final.md"); got != report {
		t.Fatalf("absent mirror = %q, want the report's own path", got)
	}
}
