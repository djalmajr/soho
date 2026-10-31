package core

// F2 auxiliary confined reads for the read-only inspection: the bounded
// directory enumeration, the approved-location file reads (the composed
// briefs, the sidecars, the task-report pointers and the reports-dir
// entries) and the protected mirror observation, all through the
// session root the source retained at acquisition — never a raw reopen
// of the state or scratch paths.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/platform"
)

// auxDirEntriesMax is the D7 inspection cap of one confined directory
// enumeration: a directory with more entries refuses (it is not
// truncated), so the collaboration store's walk stays bounded.
const auxDirEntriesMax = 4096

// ReadDir enumerates the plain directory entry names under the session
// root at the given component, through the retained root: the component
// must be a plain directory (the acquisition check, identity verified),
// and only plain directory entries (never links or nonregular entries,
// each identity-checked against the opened directory) are returned. The
// enumeration is bounded at auxDirEntriesMax entries; a directory over
// the cap is an error, never a truncated listing. Absence is
// os.ErrNotExist; it writes nothing.
func (s *SessionSource) ReadDir(name string) ([]string, error) {
	if s.root == nil {
		return nil, os.ErrNotExist
	}
	ro, err := openInspectDirectory(s.root, name, s.openDirectory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = ro.Close() }()
	f, err := ro.OpenFile(".", os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	names := []string{}
	total := 0
	for {
		batch, rerr := f.Readdir(auxDirEntriesMax)
		total += len(batch)
		for _, entry := range batch {
			if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
				continue
			}
			info, lerr := ro.Lstat(entry.Name())
			if lerr != nil {
				if errors.Is(lerr, os.ErrNotExist) {
					continue
				}
				return nil, lerr
			}
			if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				continue
			}
			names = append(names, entry.Name())
		}
		if total > auxDirEntriesMax {
			return nil, fmt.Errorf("the directory %q exceeds the %d-entry inspection cap", name, auxDirEntriesMax)
		}
		if rerr == io.EOF {
			return names, nil
		}
		if rerr != nil {
			return nil, rerr
		}
	}
}

// ReadAuxFile reads one bounded text file at an approved inspection
// auxiliary location through the retained roots: the session's root
// level (the task-report pointer), briefs dir or reports dir, under the
// state session dir (in every base alias) or the approved scratch
// session dir. A path that names any other location is refused without
// a read. Absence is os.ErrNotExist; a present entry that is linked,
// nonregular, unstable or over the supplied cap refuses. It writes
// nothing.
func (s *SessionSource) ReadAuxFile(path string, max int64) (string, error) {
	name := filepath.Base(path)
	clean := filepath.Clean(path)
	components, scratch, ok := s.auxMatch(clean, name)
	if !ok {
		return "", errors.New("the path is not an approved inspection location")
	}
	if !scratch {
		return s.ReadFile(max, components...)
	}
	root, err := openScratchRoot(s.scratchBase, s.session, s.openDirectory)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	data, err := s.confinedRead(root, max, components...)
	if err != nil {
		return "", err
	}
	return platform.DecodeText(data), nil
}

// auxMatch reports which approved auxiliary location one cleaned path
// names: the components under the session dir (the root level, the
// briefs dir or the reports dir) and whether the location is the
// approved scratch session dir, or ok=false when the path names any
// other location.
func (s *SessionSource) auxMatch(clean, name string) (components []string, scratch, ok bool) {
	type form struct {
		session string
		rel     []string
		scratch bool
	}
	forms := []form{}
	add := func(session string, dir, file string, sc bool) {
		if dir == "" {
			forms = append(forms, form{session, []string{file}, sc})
		} else {
			forms = append(forms, form{session, []string{dir, file}, sc})
		}
	}
	stateSession := filepath.Join(s.stateBase, s.session)
	add(stateSession, "", name, false)
	add(stateSession, "briefs", name, false)
	add(stateSession, "reports", name, false)
	if canon, err := filepath.EvalSymlinks(s.stateBase); err == nil && canon != s.stateBase {
		canonSession := filepath.Join(canon, s.session)
		add(canonSession, "", name, false)
		add(canonSession, "briefs", name, false)
		add(canonSession, "reports", name, false)
	}
	if s.scratchBase != "" {
		scratchSession := filepath.Join(s.scratchBase, "soho", s.session)
		add(scratchSession, "", name, true)
		add(scratchSession, "briefs", name, true)
		add(scratchSession, "reports", name, true)
		if canon, err := filepath.EvalSymlinks(s.scratchBase); err == nil && canon != s.scratchBase {
			canonSession := filepath.Join(canon, "soho", s.session)
			add(canonSession, "", name, true)
			add(canonSession, "briefs", name, true)
			add(canonSession, "reports", name, true)
		}
	}
	for _, f := range forms {
		parts := append([]string{f.session}, f.rel...)
		if filepath.Join(parts...) == clean {
			return f.rel, f.scratch, true
		}
	}
	return nil, false, false
}

// StateReportMirror is the protected mirror observation under the
// source's retained session root: the reports dir is a plain directory
// under the session root, the mirror a regular nonempty file owned by
// the current user (or of unknown owner); it returns the mirror's state
// path, or the supplied report's path when no valid mirror stands — the
// same stand-in decision the command's protectedMirror makes, without
// reopening the state dir by raw path.
func (s *SessionSource) StateReportMirror(report, name string) string {
	if s.root == nil {
		return report
	}
	info, err := confinedObserve(s.root, s.openDirectory, "reports", name)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return report
	}
	uid := os.Getuid()
	owner, known := ScratchOwnerOf(info)
	if uid < 0 || (known && owner == uid) {
		return s.StateReportsPath(name)
	}
	return report
}
