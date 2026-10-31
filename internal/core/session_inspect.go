package core

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/platform"
)

// metadataInspectMax is the D7 private cap of one individual task title,
// last-report pointer or wait marker the inspection reads: a larger file
// is refused as unverifiable metadata, never truncated.
const metadataInspectMax = 64 << 10

// SessionSource is one safely acquired known-session metadata source for
// the read-only inspection: the trusted existing state base (a symlink
// alias is accepted; the opened root is the canonical directory), the
// opaque session (validated as a portable safe component before any name
// is formed) and the known original TMPDIR (the approved scratch base,
// "" when none is approved). Acquisition opens the session root and reads
// the bounded roster once; while the source is open, every read and
// observation is confined under opened roots: appended components are
// safe components, every intermediate entry is a plain directory, the
// final entry a regular nonlinked file, opened nonblocking with its
// identity verified against the parent, and read under the D7 caps. The
// disposable scratch tree is opened per observation (it may appear or
// disappear at any time), never cached. Path strings use the configured
// bases (the ordinary readers' forms); approved report paths are
// recognized in every base alias. Absence creates nothing, and the
// source never writes, locks, caches, repairs or mirrors.
type SessionSource struct {
	stateBase     string
	session       string
	scratchBase   string
	root          *os.Root // the session root; nil when the session dir is absent
	roster        RosterSnapshot
	openDirectory inspectDirectoryOpener
}

// OpenSessionSource acquires the source for the known state base and
// opaque session. The base must exist (a trusted alias is accepted; the
// opened root is the canonical directory), a missing session dir or
// roster is a valid empty source, and an unsafe session or roster is an
// error. scratchBase is the known original TMPDIR whose <base>/soho/
// <session> reports level is an approved report observation root; ""
// approves none.
func OpenSessionSource(stateBase, sessionID, scratchBase string) (*SessionSource, error) {
	return openSessionSource(stateBase, sessionID, scratchBase, nil)
}

// inspectDirectoryOpener is a per-source filesystem dependency. A nil
// opener uses the nonblocking openReadOnlyDirectory acquisition (a
// raced-in FIFO refuses without blocking); it is never a package-global
// hook.
type inspectDirectoryOpener func(*os.Root, string) (*os.Root, error)

func openSessionSource(stateBase, sessionID, scratchBase string, open inspectDirectoryOpener) (*SessionSource, error) {
	if !platform.ValidStateComponent(sessionID) {
		return nil, fmt.Errorf("the session %q is not a safe state component", sessionID)
	}
	base, err := filepath.EvalSymlinks(stateBase)
	if err != nil {
		return nil, fmt.Errorf("the state base %s is not an existing directory: %v", stateBase, err)
	}
	if info, err := os.Lstat(base); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("the state base %s is not a directory", stateBase)
	}
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		return nil, fmt.Errorf("the state base %s cannot be opened: %v", stateBase, err)
	}
	root, err := openInspectDirectory(baseRoot, sessionID, open)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = baseRoot.Close()
		return nil, fmt.Errorf("the session %s cannot be acquired: %v", sessionID, err)
	}
	_ = baseRoot.Close()
	roster, err := RosterRowsInspect(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	s := &SessionSource{stateBase: stateBase, session: sessionID, scratchBase: scratchBase, root: root, roster: roster, openDirectory: open}
	return s, nil
}

// openInspectDirectory retains only the checked plain child directory.
// Absence before acquisition is missing; disappearance or replacement
// after that check is an error, never missing evidence. The caller owns
// the returned root. Failed acquisitions close their opened child here.
func openInspectDirectory(parent *os.Root, name string, open inspectDirectoryOpener) (*os.Root, error) {
	if !platform.ValidStateComponent(name) {
		return nil, fmt.Errorf("the directory component %q is not safe", name)
	}
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	plain := func(info fs.FileInfo) bool {
		return info.IsDir() && info.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0
	}
	if !plain(before) {
		return nil, fmt.Errorf("the directory component %q is not a plain directory", name)
	}
	var child *os.Root
	if open == nil {
		child, err = openReadOnlyDirectory(parent, name)
	} else {
		child, err = open(parent, name)
	}
	if err != nil {
		return nil, fmt.Errorf("the checked directory %q cannot be opened: %v", name, err)
	}
	opened, statErr := child.Stat(".")
	after, afterErr := parent.Lstat(name)
	if statErr != nil || afterErr != nil || !plain(opened) || !plain(after) || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		_ = child.Close()
		return nil, fmt.Errorf("the checked directory %q changed kind or identity during acquisition", name)
	}
	return child, nil
}

// openScratchRoot opens only the checked appended soho/session levels.
// The configured existing base is a trusted native filesystem path (a
// drive, UNC path or intentional alias is not a portable component).
// Only true absence returns os.ErrNotExist; unsafe existing levels fail.
func openScratchRoot(base, session string, open inspectDirectoryOpener) (*os.Root, error) {
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	soho, err := openInspectDirectory(root, "soho", open)
	if err != nil {
		return nil, err
	}
	defer soho.Close()
	return openInspectDirectory(soho, session, open)
}

// Close releases the opened roots (idempotent enough for the scoped
// lifetime: it is called once when the producer is done with the
// source).
func (s *SessionSource) Close() error {
	if s.root != nil {
		return s.root.Close()
	}
	return nil
}

func (s *SessionSource) StateBase() string { return s.stateBase }
func (s *SessionSource) SessionID() string { return s.session }

// StateDir is the plain joined path of the session state dir, for the
// path-based seams the inspection still reuses (the collaboration store,
// the compact marker parent and the sidecar lookup); it performs no I/O.
func (s *SessionSource) StateDir() string {
	return filepath.Join(s.stateBase, s.session)
}

// Roster is the acquired bounded roster snapshot.
func (s *SessionSource) Roster() RosterSnapshot { return s.roster }

// StateReportsPath is the approved state reports path of one report name
// (the exact session's state reports dir, in the configured base's form).
func (s *SessionSource) StateReportsPath(name string) string {
	return filepath.Join(s.stateBase, s.session, "reports", name)
}

// ScratchReportPath is the approved scratch reports path of one report
// name under the known original TMPDIR, in the configured base's form, or
// "" when no scratch base was approved.
func (s *SessionSource) ScratchReportPath(name string) string {
	if s.scratchBase == "" {
		return ""
	}
	return filepath.Join(s.scratchBase, "soho", s.session, "reports", name)
}

// stateReportForms is the approved state reports path of one report name
// in every base alias: the configured string and the canonical
// directory (a pointer written by a process that knows the base under a
// different alias still names the same confined root).
func (s *SessionSource) stateReportForms(name string) []string {
	forms := []string{filepath.Join(s.stateBase, s.session, "reports", name)}
	if canon, err := filepath.EvalSymlinks(s.stateBase); err == nil && canon != s.stateBase {
		forms = append(forms, filepath.Join(canon, s.session, "reports", name))
	}
	return forms
}

// scratchReportForms is the approved scratch reports path of one report
// name in every base alias; nil when no scratch base was approved.
func (s *SessionSource) scratchReportForms(name string) []string {
	if s.scratchBase == "" {
		return nil
	}
	forms := []string{filepath.Join(s.scratchBase, "soho", s.session, "reports", name)}
	if canon, err := filepath.EvalSymlinks(s.scratchBase); err == nil && canon != s.scratchBase {
		forms = append(forms, filepath.Join(canon, "soho", s.session, "reports", name))
	}
	return forms
}

// ObserveReport observes one report entry through its approved root: the
// state reports dir when the path is an approved state form, the scratch
// reports dir when it is an approved scratch form, os.ErrNotExist when
// the path is approved nowhere. The metadata is the confined observation
// (size, mtime, identity) — never the report bytes.
func (s *SessionSource) ObserveReport(report string) (fs.FileInfo, error) {
	name := filepath.Base(report)
	for _, form := range s.stateReportForms(name) {
		if filepath.Clean(report) == form {
			return s.ObserveStateReport(name)
		}
	}
	for _, form := range s.scratchReportForms(name) {
		if filepath.Clean(report) == form {
			return s.ObserveScratchReport(name)
		}
	}
	return nil, os.ErrNotExist
}

// ApprovedReportRoot reports which approved root one report path names —
// "state" or "scratch" — for the observation's absent/unsafe decision
// (the protected mirror applies only to the scratch root), or ok=false
// when the path is approved nowhere.
func (s *SessionSource) ApprovedReportRoot(report string) (string, bool) {
	name := filepath.Base(report)
	for _, form := range s.stateReportForms(name) {
		if filepath.Clean(report) == form {
			return "state", true
		}
	}
	for _, form := range s.scratchReportForms(name) {
		if filepath.Clean(report) == form {
			return "scratch", true
		}
	}
	return "", false
}

// ReadMetadata reads one private metadata file (the last-report pointer,
// a wait marker, the task title) through the opened roots at the D7
// 64 KiB cap: the pointer, marker and title are small bounded metadata,
// not the report or transcript they only point at. Absent entries are
// os.ErrNotExist; unsafe, unstable, nonregular, linked or oversize
// entries refuse.
func (s *SessionSource) ReadMetadata(components ...string) (string, error) {
	return s.ReadFile(metadataInspectMax, components...)
}

// ReadFile reads one bounded text file under the session root: every
// component is validated as a safe state component, every intermediate
// entry must be a plain directory and the final entry a regular
// nonlinked file; the open is nonblocking, its identity is verified
// against the parent, the read is capped at max bytes (a longer file is
// refused, never truncated) and the raw bytes pass the source's
// byte-to-text boundary: invalid UTF-8 is refused there (never repaired
// with replacement characters), and valid bytes are decoded the way the
// state text reads decode them (CRLF as LF). Absence is os.ErrNotExist,
// including a source whose session dir is absent. It writes nothing.
func (s *SessionSource) ReadFile(max int64, components ...string) (string, error) {
	data, err := s.confinedRead(s.root, max, components...)
	if err != nil {
		return "", err
	}
	return decodeSessionText(data)
}

// decodeSessionText is the source's byte-to-text boundary: raw invalid
// UTF-8 is refused with an ordinary error before any decoding, never
// repaired with replacement characters — the consumers that reject
// invalid UTF-8 (the approved task title, the collector's optional task
// metadata) see the failure, and the required private state evidence
// retains its refusal. Valid input keeps DecodeText's WHATWG decoding
// and CRLF normalization.
func decodeSessionText(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("the session metadata is not valid UTF-8")
	}
	return platform.DecodeText(data), nil
}

// ObserveFile reports one entry's metadata (size, mtime, identity) under
// the session root without reading or opening the final entry: the same
// component validation and plain-directory confinement as ReadFile.
func (s *SessionSource) ObserveFile(components ...string) (fs.FileInfo, error) {
	return confinedObserve(s.root, s.openDirectory, components...)
}

// ObserveStateReport observes one entry of the exact session's state
// reports dir.
func (s *SessionSource) ObserveStateReport(name string) (fs.FileInfo, error) {
	return confinedObserve(s.root, s.openDirectory, "reports", name)
}

// ObserveScratchReport observes one entry of the approved scratch
// reports dir: the disposable scratch tree is opened for this
// observation only (a fresh confined root, released here), so an absent
// scratch level reads as os.ErrNotExist; unsafe existing levels are
// errors, never followed through a link or read from a cached handle.
func (s *SessionSource) ObserveScratchReport(name string) (fs.FileInfo, error) {
	if s.scratchBase == "" {
		return nil, os.ErrNotExist
	}
	root, err := openScratchRoot(s.scratchBase, s.session, s.openDirectory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return confinedObserve(root, s.openDirectory, "reports", name)
}

// confinedRead is the shared confined bounded read under one opened
// root (nil root: the entry is absent).
func (s *SessionSource) confinedRead(root *os.Root, max int64, components ...string) ([]byte, error) {
	if len(components) == 0 {
		return nil, errors.New("no file components")
	}
	for _, c := range components {
		if !platform.ValidStateComponent(c) {
			return nil, fmt.Errorf("the state component %q is not safe", c)
		}
	}
	if root == nil {
		return nil, os.ErrNotExist
	}
	// The intermediate roots opened while walking are released together
	// at the end, in reverse order.
	roots := make([]*os.Root, 0, len(components))
	defer func() {
		for i := len(roots) - 1; i >= 0; i-- {
			_ = roots[i].Close()
		}
	}()
	ro := root
	for i, c := range components {
		if i < len(components)-1 {
			next, err := openInspectDirectory(ro, c, s.openDirectory)
			if err != nil {
				return nil, err
			}
			roots = append(roots, next)
			ro = next
		} else {
			info, err := ro.Lstat(c)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, platform.ErrNotRegular
			}
			f, err := ro.OpenFile(c, platform.ReadOnlyNonBlocking, 0)
			if err != nil {
				return nil, err
			}
			defer func() { _ = f.Close() }()
			opened, err := f.Stat()
			if err != nil {
				return nil, err
			}
			after, err := ro.Lstat(c)
			if err != nil {
				return nil, err
			}
			if !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(info, opened) || !os.SameFile(opened, after) {
				return nil, platform.ErrNotRegular
			}
			data, err := io.ReadAll(io.LimitReader(f, max+1))
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > max {
				return nil, fmt.Errorf("the file exceeds the %d-byte inspection cap", max)
			}
			return data, nil
		}
	}
	return nil, errors.New("unreachable: no final component")
}

// confinedObserve is the shared confined metadata observation under one
// opened root (nil root: the entry is absent): plain directories for the
// intermediate components, then the final entry's Lstat, without opening
// or reading it.
func confinedObserve(root *os.Root, open inspectDirectoryOpener, components ...string) (fs.FileInfo, error) {
	if len(components) == 0 {
		return nil, errors.New("no file components")
	}
	for _, c := range components {
		if !platform.ValidStateComponent(c) {
			return nil, fmt.Errorf("the state component %q is not safe", c)
		}
	}
	if root == nil {
		return nil, os.ErrNotExist
	}
	roots := make([]*os.Root, 0, len(components))
	defer func() {
		for i := len(roots) - 1; i >= 0; i-- {
			_ = roots[i].Close()
		}
	}()
	ro := root
	for i, c := range components {
		if i < len(components)-1 {
			next, err := openInspectDirectory(ro, c, open)
			if err != nil {
				return nil, err
			}
			roots = append(roots, next)
			ro = next
		} else {
			return ro.Lstat(c)
		}
	}
	return nil, errors.New("unreachable: no final component")
}

// ReadScratchReportLimited reads a scratch report-level file the way the
// inspection seam reads sidecar metadata: the same existing scratch read
// guarantees (a regular nonlinked file of the current user, opened
// nonblocking, identity verified against the checked handle) with the
// D7 sidecar cap — a file longer than limit bytes is refused as an
// error, never truncated.
func ReadScratchReportLimited(path string, limit int64) ([]byte, error) {
	info, err := StatScratchReport(path)
	if err != nil {
		return nil, err
	}
	f, err := platform.OpenRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, &ScratchRefusedError{Kind: "reports", Path: path, Cause: "it changed after the check"}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &ScratchRefusedError{Kind: "reports", Path: path, Cause: fmt.Sprintf("it exceeds the %d-byte inspection cap", limit)}
	}
	return data, nil
}
