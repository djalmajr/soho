package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/djalmajr/soho/internal/platform"
)

// CopiesParent selects the parent used by mutation-copy without creating it.
// configured reports whether the user supplied copies_dir.
func CopiesParent(ctx *Config, env platform.Env, cwd string) (path string, configured bool, err error) {
	value := Cfg(ctx, "copies_dir", "", env)
	if !ConfigValueOk("copies_dir", value, env, cwd) {
		return "", false, fmt.Errorf("invalid copies_dir: must be absolute or start with ~/")
	}
	if strings.HasPrefix(value, "~/") {
		home := env.Get("HOME")
		if platform.Current() == "win32" {
			home = env.Get("USERPROFILE")
		}
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		if home == "" {
			return "", false, fmt.Errorf("copies_dir cannot be resolved: HOME is unavailable")
		}
		if resolved, e := copiesCanonicalHome(home); e == nil {
			home = resolved
		}
		value = filepath.Join(home, filepath.FromSlash(value[2:]))
	}
	if value != "" {
		return value, true, nil
	}
	tmp := env.Get("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	return tmp, false, nil
}

func copiesCanonicalHome(input string) (string, error) {
	unresolved, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		resolved, e := filepath.EvalSymlinks(unresolved)
		if e == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(e, fs.ErrNotExist) && !errors.Is(e, syscall.ENOTDIR) {
			return "", e
		}
		parent := filepath.Dir(unresolved)
		if parent == unresolved {
			return "", e
		}
		tail = append(tail, filepath.Base(unresolved))
		unresolved = parent
	}
}
