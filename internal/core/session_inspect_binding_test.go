package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// Retain the root's exported-consumer regression from copy554023700.
func TestRootReviewScratchUnsafeVersusAbsent(t *testing.T) {
	base, _ := sessionFixture(t, "s1")
	scratch := t.TempDir()
	s := openSource(t, base, "s1", scratch)
	if _, err := s.ObserveScratchReport("a.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent control=%v", err)
	}
	foreign := t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(scratch, "soho")); err != nil {
		t.Skipf("cannot create owned fixture link: %v", err)
	}
	_, err := s.ObserveScratchReport("a.md")
	t.Logf("existing unsafe scratch level returns %v", err)
	if errors.Is(err, os.ErrNotExist) {
		t.Error("existing unsafe scratch directory collapsed to absence")
	}
}

// directory acquisition is scheduled per source, through the real
// OpenRoot operation; no production global or timing loop is involved.
func TestSessionSourceDirectoryBinding(t *testing.T) {
	for _, site := range []string{"session", "read-wait", "read-briefs", "observe-reports", "scratch-soho", "scratch-session", "scratch-reports"} {
		for _, moment := range []string{"before-open", "after-open"} {
			for _, replacement := range []string{"plain", "link", "gone"} {
				t.Run(site+"/"+moment+"/"+replacement, func(t *testing.T) {
					base, sd := sessionFixture(t, "s1")
					scratch := t.TempDir()
					canonicalScratch, err := filepath.EvalSymlinks(scratch)
					if err != nil {
						t.Fatal(err)
					}
					for _, dir := range []string{sd, filepath.Join(sd, "wait"), filepath.Join(sd, "briefs"), filepath.Join(sd, "reports"), filepath.Join(scratch, "soho"), filepath.Join(scratch, "soho", "s1"), filepath.Join(scratch, "soho", "s1", "reports")} {
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, "evidence"), []byte("original"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					target, component := sd, "s1"
					switch site {
					case "read-wait":
						target, component = filepath.Join(sd, "wait"), "wait"
					case "read-briefs":
						target, component = filepath.Join(sd, "briefs"), "briefs"
					case "observe-reports":
						target, component = filepath.Join(sd, "reports"), "reports"
					case "scratch-soho":
						target, component = filepath.Join(scratch, "soho"), "soho"
					case "scratch-session":
						target, component = filepath.Join(scratch, "soho", "s1"), "s1"
					case "scratch-reports":
						target, component = filepath.Join(scratch, "soho", "s1", "reports"), "reports"
					}
					// A sibling is inside the same parent, so os.Root's escape
					// guard alone cannot distinguish it from the checked entry.
					sibling := filepath.Join(filepath.Dir(target), "sibling")
					if err := os.Mkdir(sibling, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(sibling, "evidence"), []byte("sibling-data"), 0600); err != nil {
						t.Fatal(err)
					}
					if replacement == "link" {
						probe := filepath.Join(t.TempDir(), "probe")
						if err := os.Symlink(sibling, probe); err != nil {
							t.Skipf("fixture symlink unavailable: %v", err)
						}
					}
					fired := false
					var expected map[string]string
					swap := func() {
						if err := os.Rename(target, target+"-original"); err != nil {
							t.Fatal(err)
						}
						switch replacement {
						case "link":
							if err := os.Symlink(sibling, target); err != nil {
								t.Fatal(err)
							}
						case "plain":
							if err := os.Mkdir(target, 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(target, "evidence"), []byte("replacement-data"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						expected = inspectBindingSnapshot(t, base, scratch)
					}
					opener := func(parent *os.Root, name string) (*os.Root, error) {
						if fired || name != component {
							return parent.OpenRoot(name)
						}
						// The scratch session must not intercept initial state acquisition.
						if site == "scratch-session" && parent.Name() != filepath.Join(canonicalScratch, "soho") {
							return parent.OpenRoot(name)
						}
						fired = true
						if moment == "before-open" {
							swap()
							return parent.OpenRoot(name)
						}
						child, err := parent.OpenRoot(name)
						if err != nil {
							return nil, err
						}
						swap()
						return child, nil
					}
					s, err := openSessionSource(base, "s1", scratch, opener)
					if site == "session" {
						if s != nil {
							s.Close()
							t.Fatal("replacement session was acquired")
						}
					} else {
						if err != nil {
							t.Fatal(err)
						}
						defer s.Close()
						switch site {
						case "read-wait":
							_, err = s.ReadMetadata("wait", "evidence")
						case "read-briefs":
							_, err = s.ReadMetadata("briefs", "evidence")
						case "observe-reports":
							_, err = s.ObserveFile("reports", "evidence")
						default:
							_, err = s.ObserveScratchReport("evidence")
						}
					}
					if !fired || err == nil || errors.Is(err, os.ErrNotExist) {
						t.Fatalf("checked replacement must refuse, site=%s phase=%s replacement=%s fired=%v err=%v", site, moment, replacement, fired, err)
					}
					if !reflect.DeepEqual(expected, inspectBindingSnapshot(t, base, scratch)) {
						t.Fatal("inspection changed the scheduled fixture")
					}
				})
			}
		}
	}
}

func inspectBindingSnapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	snap := make(map[string]string)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			value := fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
			if info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				value += string(data)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				value += target
			}
			snap[path] = value
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return snap
}

func TestSessionSourceRetainedRootAndScratchLifetime(t *testing.T) {
	base, sd := sessionFixture(t, "s1")
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(sd, "wait", "evidence"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	s := openSource(t, base, "s1", scratch)
	if err := os.Rename(sd, sd+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "wait", "evidence"), []byte("replacement-data"), 0600); err != nil {
		t.Fatal(err)
	}
	before := inspectBindingSnapshot(t, base, scratch)
	if text, err := s.ReadMetadata("wait", "evidence"); err != nil || text != "original" {
		t.Fatalf("retained read=%q err=%v", text, err)
	}
	if info, err := s.ObserveFile("wait", "evidence"); err != nil || info.Size() != 8 {
		t.Fatalf("retained observation=%v err=%v", info, err)
	}
	if !reflect.DeepEqual(before, inspectBindingSnapshot(t, base, scratch)) {
		t.Fatal("retained source wrote the fixture")
	}
	if _, err := s.ObserveScratchReport("evidence"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initial scratch absence=%v", err)
	}
	reports := filepath.Join(scratch, "soho", "s1", "reports")
	if err := os.MkdirAll(reports, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reports, "evidence"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if info, err := s.ObserveScratchReport("evidence"); err != nil || info.Size() != 8 {
		t.Fatalf("new scratch observation=%v err=%v", info, err)
	}
	if err := os.Rename(reports, reports+"-saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObserveScratchReport("evidence"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch observation reused a retained reports handle: %v", err)
	}
}

func TestSessionSourceScratchErrorsAndNativeBase(t *testing.T) {
	for _, level := range []string{"base", "soho", "session", "reports"} {
		for _, kind := range []string{"absent", "file", "link"} {
			t.Run(level+"/"+kind, func(t *testing.T) {
				base, _ := sessionFixture(t, "s1")
				root := t.TempDir()
				scratch := filepath.Join(root, "scratch")
				target := scratch
				switch level {
				case "soho":
					target = filepath.Join(scratch, "soho")
				case "session":
					target = filepath.Join(scratch, "soho", "s1")
				case "reports":
					target = filepath.Join(scratch, "soho", "s1", "reports")
				}
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "file" {
					if err := os.WriteFile(target, []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "link" {
					other := t.TempDir()
					if err := os.Symlink(other, target); err != nil {
						t.Skip(err)
					}
				}
				s := openSource(t, base, "s1", scratch)
				before := inspectBindingSnapshot(t, root)
				_, err := s.ObserveScratchReport("evidence")
				// The trusted base may be an intentional alias; its empty
				// target genuinely lacks the appended soho level.
				missing := kind == "absent" || level == "base" && kind == "link"
				if missing != errors.Is(err, os.ErrNotExist) || err == nil {
					t.Fatalf("level=%s kind=%s err=%v missing=%v", level, kind, err, missing)
				}
				if !reflect.DeepEqual(before, inspectBindingSnapshot(t, root)) {
					t.Fatal("scratch inspection wrote fixture metadata")
				}
			})
		}
	}
	t.Run("native root and trusted alias", func(t *testing.T) {
		base, _ := sessionFixture(t, "s1")
		name := "native scratch base"
		if runtime.GOOS != "windows" {
			name = "native: scratch base"
		}
		scratch := filepath.Join(t.TempDir(), name)
		path := filepath.Join(scratch, "soho", "s1", "reports", "evidence")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		check := func(dir string) {
			s := openSource(t, base, "s1", dir)
			before := inspectBindingSnapshot(t, scratch)
			for i := 0; i < 2; i++ {
				info, err := s.ObserveScratchReport("evidence")
				if err != nil || info.Size() != 8 {
					t.Fatalf("native scratch base=%q volume=%q observation=%v err=%v", dir, filepath.VolumeName(dir), info, err)
				}
			}
			if !reflect.DeepEqual(before, inspectBindingSnapshot(t, scratch)) {
				t.Fatal("regular scratch observation changed the tree")
			}
		}
		check(scratch)
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(scratch, alias); err != nil {
			t.Skip(err)
		}
		check(alias)
	})
}
