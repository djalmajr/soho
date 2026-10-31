package core

// Focused tests for the confined known-session metadata source: the
// acquisition bounds (trusted base alias, safe components, absent
// session), the confined read/observe guarantees (caps, links,
// nonregular, traversal) and the approved scratch root.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionFixture builds an existing state base with one session dir.
func sessionFixture(t *testing.T, session string) (base, sessionDir string) {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "PATH"} {
		t.Setenv(key, home)
	}
	base = t.TempDir()
	sessionDir = filepath.Join(base, session)
	if err := os.MkdirAll(filepath.Join(sessionDir, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	return base, sessionDir
}

// openSource opens a source over the fixture and defers its close.
func openSource(t *testing.T, base, session, scratch string) *SessionSource {
	t.Helper()
	s, err := OpenSessionSource(base, session, scratch)
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestSessionSourceAcquisition: the base must exist (a symlink alias is
// canonicalized), the session must be a safe plain directory, and an
// unsafe session id or a missing base is an acquisition error.
func TestSessionSourceAcquisition(t *testing.T) {
	if _, err := OpenSessionSource(filepath.Join(t.TempDir(), "absent"), "s1", ""); err == nil {
		t.Fatalf("the missing base was accepted")
	}
	baseFile := t.TempDir()
	file := filepath.Join(baseFile, "base")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSessionSource(file, "s1", ""); err == nil {
		t.Fatalf("the non-directory base was accepted")
	}
	if _, err := OpenSessionSource(t.TempDir(), "../s1", ""); err == nil {
		t.Fatalf("the unsafe session id was accepted")
	}
	base, sd := sessionFixture(t, "s1")
	// A trusted alias of the base is canonicalized and works.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	s, err := OpenSessionSource(alias, "s1", "")
	if err != nil {
		t.Fatalf("the symlink base alias was refused: %v", err)
	}
	// Path strings keep the configured base's form (the ordinary
	// readers' forms); the opened root is the canonical directory, which
	// the fixture's session dir proves by reading its absence.
	if s.StateDir() != filepath.Join(alias, "s1") {
		t.Fatalf("StateDir = %q, want the configured form %q", s.StateDir(), filepath.Join(alias, "s1"))
	}
	if _, err := s.ReadFile(1024, "no-such-file"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the alias source read = %v, want the canonical dir's absence", err)
	}
	s.Close()
	// A session that is a file, or a link, is refused.
	if err := os.WriteFile(filepath.Join(base, "f1"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSessionSource(base, "f1", ""); err == nil {
		t.Fatalf("the file session was accepted")
	}
	if err := os.Symlink(sd, filepath.Join(base, "l1")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	if _, err := OpenSessionSource(base, "l1", ""); err == nil {
		t.Fatalf("the linked session was accepted")
	}
}

// TestSessionSourceAbsentSession: an existing base without the session
// dir is a valid empty source: reads and observations report absence,
// the roster is empty and nothing is created.
func TestSessionSourceAbsentSession(t *testing.T) {
	base := t.TempDir()
	before := listTree(t, base)
	s, err := OpenSessionSource(base, "s-absent", "")
	if err != nil {
		t.Fatalf("the absent session was an error: %v", err)
	}
	defer s.Close()
	if _, err := s.ReadFile(1024, "wait", "w1.marker"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent marker read = %v, want os.ErrNotExist", err)
	}
	if _, err := s.ObserveFile("wait", "w1.marker"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent marker observe = %v, want os.ErrNotExist", err)
	}
	if len(s.Roster().Rows) != 0 {
		t.Fatalf("the absent session roster has rows: %#v", s.Roster().Rows)
	}
	if _, err := s.ObserveScratchReport("w1-final.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent scratch report = %v, want os.ErrNotExist", err)
	}
	if got := listTree(t, base); len(got) != len(before) {
		t.Fatalf("the absent-session source changed the base tree: %v -> %v", before, got)
	}
}

// TestSessionSourceComponentEscape: appended components must stay safe
// names under the opened roots; traversal and absolute names refuse
// before any open.
func TestSessionSourceComponentEscape(t *testing.T) {
	base, sd := sessionFixture(t, "s1")
	s := openSource(t, base, "s1", "")
	for _, components := range [][]string{
		{"../agents.tsv"},
		{"/etc/passwd"},
		{"wait", ".."},
		{""},
	} {
		if _, err := s.ReadFile(1024, components...); err == nil {
			t.Fatalf("the unsafe components %v were read", components)
		}
		if _, err := s.ObserveFile(components...); err == nil {
			t.Fatalf("the unsafe components %v were observed", components)
		}
	}
	// A non-directory intermediate refuses.
	if err := os.WriteFile(filepath.Join(sd, "wait", "flat"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile(1024, "wait", "flat", "inner"); err == nil {
		t.Fatalf("the file intermediate component was traversed")
	}
}

// TestSessionSourceReadCap: a marker longer than the 64 KiB cap refuses;
// the cap itself is read; the explicit cap of ReadFile applies the same
// way.
func TestSessionSourceReadCap(t *testing.T) {
	base, sd := sessionFixture(t, "s1")
	s := openSource(t, base, "s1", "")
	atCap := strings.Repeat("a", metadataInspectMax)
	overCap := strings.Repeat("b", metadataInspectMax+1)
	if err := os.WriteFile(filepath.Join(sd, "wait", "w1.at"), []byte(atCap), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "wait", "w1.over"), []byte(overCap), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadMetadata("wait", "w1.at")
	if err != nil || got != atCap {
		t.Fatalf("the at-cap marker was refused: %v", err)
	}
	if _, err := s.ReadMetadata("wait", "w1.over"); err == nil {
		t.Fatalf("the over-cap marker was read (truncation?)")
	}
	// The explicit cap: 1024 accepts the 1024-byte file and refuses the
	// next byte.
	small := strings.Repeat("c", 1024)
	if err := os.WriteFile(filepath.Join(sd, "wait", "w1.small"), []byte(small), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile(1024, "wait", "w1.small"); err != nil || got != small {
		t.Fatalf("the at-cap ReadFile was refused: %v", err)
	}
	if _, err := s.ReadFile(1024, "wait", "w1.at"); err == nil {
		t.Fatalf("the over-cap ReadFile was read")
	}
}

// TestSessionSourceLinkedAndNonregularFinal: a linked, a directory and a
// missing final entry are distinguished: links and nonregular entries
// refuse, absence is os.ErrNotExist.
func TestSessionSourceLinkedAndNonregularFinal(t *testing.T) {
	base, sd := sessionFixture(t, "s1")
	other := t.TempDir()
	target := filepath.Join(other, "target.txt")
	if err := os.WriteFile(target, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(sd, "wait", "w1.link")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	s := openSource(t, base, "s1", "")
	if _, err := s.ReadMetadata("wait", "w1.link"); err == nil {
		t.Fatalf("the linked marker was read through the link")
	}
	// The directory final entry refuses the read; the observation reports
	// its metadata (the caller decides the regular check).
	if _, err := s.ReadMetadata("wait"); err == nil {
		t.Fatalf("the directory was read as a file")
	}
	if _, err := s.ObserveFile("wait"); err != nil {
		t.Fatalf("the directory observation failed: %v", err)
	}
	if _, err := s.ReadMetadata("wait", "absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent read = %v, want os.ErrNotExist", err)
	}
}

// TestSessionSourceRosterBound: the acquisition reads the roster under
// the D7 bounds: an oversize roster is an acquisition error, a valid
// one is the snapshot.
func TestSessionSourceRosterBound(t *testing.T) {
	base := t.TempDir()
	sd := filepath.Join(base, "s1")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	roster := "w\tp1\tclaude\timplementer\t" + strings.Repeat("x", rosterInspectMaxRowLen)
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSessionSource(base, "s1", ""); err == nil {
		t.Fatalf("the oversize roster was accepted")
	}
}

// TestSessionSourceScratchRoot: the approved scratch base is opened only
// through plain levels; an existing linked level is an error, and a
// source without an approved base approves none.
func TestSessionSourceScratchRoot(t *testing.T) {
	base, sd := sessionFixture(t, "s1")
	tmp := t.TempDir()
	reports := filepath.Join(tmp, "soho", "s1", "reports")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(reports, "w1-final.md")
	if err := os.WriteFile(report, []byte("final\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, base, "s1", tmp)
	if s.ScratchReportPath("w1-final.md") != report {
		t.Fatalf("ScratchReportPath = %q, want %q", s.ScratchReportPath("w1-final.md"), report)
	}
	info, err := s.ObserveScratchReport("w1-final.md")
	if err != nil || info.Size() != 6 {
		t.Fatalf("scratch report observe = %v, want the 6-byte report", err)
	}
	if s.StateReportsPath("w1-final.md") != filepath.Join(sd, "reports", "w1-final.md") {
		t.Fatalf("StateReportsPath = %q, want the state reports dir", s.StateReportsPath("w1-final.md"))
	}
	// A source without an approved scratch base reads it as absent.
	s2 := openSource(t, base, "s1", "")
	if _, err := s2.ObserveScratchReport("w1-final.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved scratch report = %v, want os.ErrNotExist", err)
	}
	// A linked soho level is unsafe existing evidence, never absence.
	linkedTmp := t.TempDir()
	if err := os.Symlink(reports, filepath.Join(linkedTmp, "soho")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	s3 := openSource(t, base, "s1", linkedTmp)
	if _, err := s3.ObserveScratchReport("w1-final.md"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the linked scratch level must be an error distinct from absence: %v", err)
	}
}

// TestReadScratchReportLimited: the capped scratch report read refuses an
// overlong file and reads the cap itself.
func TestReadScratchReportLimited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w1.dispatch.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadScratchReportLimited(path, 4096); err != nil {
		t.Fatalf("the at-cap scratch file was refused: %v", err)
	}
	if _, err := ReadScratchReportLimited(path, 4095); err == nil {
		t.Fatalf("the over-cap scratch file was read")
	}
	if _, err := ReadScratchReportLimited(filepath.Join(dir, "absent"), 4096); err == nil {
		t.Fatalf("the absent scratch file was read")
	}
}

// listTree lists the regular files under a root as a stable comparison.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	out := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			out = append(out, strings.TrimPrefix(path, root))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
