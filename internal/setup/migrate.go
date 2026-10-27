package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// formerUserConfigDirs and formerProjectConfigNames list the names the
// herdr-soho and herdr-agents generations wrote, in the order the
// migration visits them.
var (
	formerUserConfigDirs   = []string{"herdr-soho", "herdr-agents"}
	formerProjectConfNames = []string{"herdr-soho.conf", "herdr-agents.conf"}
)

// migrateNames moves what the herdr-soho and herdr-agents generations
// wrote to the soho names. It returns one line per step; with dry it
// only reports what it would do.
func migrateNames(ctx *core.Config, env platform.Env, cwd string, dry bool) []string {
	lines := []string{}
	moved := false
	verb := "moved"
	if dry {
		verb = "would move"
	}

	// User config: each entry of a former config dir goes to the soho
	// config dir; an entry that already exists there is kept for a manual
	// merge, and a former dir left empty is removed. In dry the moves do
	// not happen, so a destination projected earlier in this same run
	// still counts as existing: dry and the real run show the same
	// conflicts.
	novo := filepath.Dir(platform.UserConfigPath(platform.Current(), env))
	projected := map[string]bool{}
	for _, base := range formerUserConfigDirs {
		antigo := filepath.Join(filepath.Dir(novo), base)
		entries, err := os.ReadDir(antigo)
		if err != nil {
			continue
		}
		kept := 0
		for _, entry := range entries {
			src := filepath.Join(antigo, entry.Name())
			dst := filepath.Join(novo, entry.Name())
			_, lerr := os.Lstat(dst)
			if (lerr == nil || !os.IsNotExist(lerr)) || (dry && projected[dst]) {
				kept++
				lines = append(lines, fmt.Sprintf("migrate: kept %s: %s already exists; merge by hand", src, dst))
				continue
			}
			if dry {
				projected[dst] = true
				lines = append(lines, fmt.Sprintf("migrate: %s %s to %s", verb, src, dst))
				continue
			}
			if err := os.MkdirAll(novo, 0o700); err != nil {
				kept++
				lines = append(lines, fmt.Sprintf("migrate: kept %s: %v", src, err))
				continue
			}
			if err := os.Rename(src, dst); err != nil {
				kept++
				lines = append(lines, fmt.Sprintf("migrate: kept %s: %v", src, err))
				continue
			}
			moved = true
			lines = append(lines, fmt.Sprintf("migrate: %s %s to %s", verb, src, dst))
		}
		if kept == 0 {
			if dry {
				lines = append(lines, fmt.Sprintf("migrate: would remove the empty %s", antigo))
				continue
			}
			if err := os.Remove(antigo); err == nil {
				lines = append(lines, fmt.Sprintf("migrate: removed the empty %s", antigo))
			} else {
				lines = append(lines, fmt.Sprintf("migrate: kept %s: %v", antigo, err))
			}
		}
	}

	// Project config: each former .agents config moves to .agents/soho.conf;
	// once the first one is moved (or projected in dry) the second finds
	// the new file in place.
	root := platform.ProjectRoot(env, cwd)
	lines = append(lines, migrateProjectRoles(root, env, dry)...)
	novoProject := filepath.Join(root, ".agents", "soho.conf")
	for _, base := range formerProjectConfNames {
		antigo := filepath.Join(root, ".agents", base)
		info, err := os.Stat(antigo)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		_, lerr := os.Lstat(novoProject)
		if (lerr != nil && os.IsNotExist(lerr)) && !(dry && projected[novoProject]) {
			if !dry {
				if err := os.Rename(antigo, novoProject); err != nil {
					lines = append(lines, fmt.Sprintf("migrate: kept %s: %v", antigo, err))
					continue
				}
				moved = true
			} else {
				projected[novoProject] = true
			}
			lines = append(lines, fmt.Sprintf("migrate: %s %s to %s", verb, antigo, novoProject))
		} else {
			lines = append(lines, fmt.Sprintf("migrate: kept %s: %s already exists; merge by hand", antigo, novoProject))
		}
	}

	// Ignore rule: once the new project config is in place (or projected
	// in dry), the rule that ignored a former config extends itself with
	// the soho name in the same file, so the migrated file is not left
	// untracked.
	if lstatExists(novoProject) || projected[novoProject] {
		lines = append(lines, migrateIgnoreRule(root, env, dry)...)
	}

	// Reload after the config steps so the rest of the command reads the
	// migrated files; a dry run moved nothing, so it never reloads.
	if moved && !dry {
		*ctx = core.LoadConfig(env, cwd)
	}

	// State: the former state dir moves to the soho one only when the state
	// root is the default, the soho dir is absent, and no worker is still
	// listed in a roster. A state dir pinned by the environment or the
	// configuration keeps the former dir even when the pin resolves to the
	// default path.
	stateRoot := platform.StateProjectRoot(env, cwd)
	antigoState := filepath.Join(stateRoot, ".herdr-soho")
	novoState := filepath.Join(stateRoot, ".soho")
	if info, err := os.Stat(antigoState); err == nil && info.IsDir() {
		efetivo := core.StateRootPath(ctx, env, cwd)
		switch {
		case core.StateDirExplicitlyPinned(ctx, env) || efetivo != novoState:
			lines = append(lines, fmt.Sprintf("migrate: kept %s: the state dir is set to %s", antigoState, efetivo))
		case lstatExists(novoState) && !emptyDir(novoState):
			lines = append(lines, fmt.Sprintf("migrate: kept %s: %s already exists; remove %s once its reports are no longer needed", antigoState, novoState, antigoState))
		default:
			hasWorkers, inspectErr := formerRosterInspection(antigoState)
			if inspectErr != nil {
				lines = append(lines, fmt.Sprintf("migrate: kept %s: could not read its roster (%s); release the workers and run 'soho setup' again", antigoState, shortRosterError(inspectErr)))
				break
			}
			if hasWorkers {
				lines = append(lines, fmt.Sprintf("migrate: kept %s: workers are still in its roster; release them with 'soho release <name> --close' and run 'soho setup' again", antigoState))
				break
			}
			if !dry {
				if lstatExists(novoState) {
					// An empty soho state dir (for example one the doctor
					// created before the setup) counts as absent: remove it
					// so the rename can land there.
					if err := os.Remove(novoState); err != nil {
						lines = append(lines, fmt.Sprintf("migrate: kept %s: %v", antigoState, err))
						break
					}
				}
				if err := os.Rename(antigoState, novoState); err != nil {
					lines = append(lines, fmt.Sprintf("migrate: kept %s: %v", antigoState, err))
					break
				}
				// Reload after the move so the rest of the command sees the
				// state in its new place, including the moved session files.
				*ctx = core.LoadConfig(env, cwd)
			}
			lines = append(lines, fmt.Sprintf("migrate: %s %s to %s", verb, antigoState, novoState))
		}
	}
	return lines
}

func lstatExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// emptyDir reports whether path is a real directory with no entries: the
// Lstat must say directory and not a symlink, and the ReadDir must come
// back clean and empty. Only that case counts as absent for the state
// step.
func emptyDir(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

// formerRosterInspection inspects the agents.tsv of every workspace under
// the former state dir. It reports whether a worker is listed (a non-empty
// line that does not start with '#') and whether the roster could not be
// inspected at all: a missing agents.tsv counts as empty, but a state dir
// whose entries cannot be listed, or a roster that exists and cannot be
// read, is an inspection failure.
func formerRosterInspection(stateDir string) (bool, error) {
	sub, err := os.ReadDir(stateDir)
	if err != nil {
		return false, err
	}
	for _, entry := range sub {
		data, err := os.ReadFile(filepath.Join(stateDir, entry.Name(), "agents.tsv"))
		if err != nil {
			// A missing agents.tsv (including one behind a non-directory
			// entry) counts as an empty roster; anything else is a
			// roster that exists and cannot be read.
			if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			return false, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				return true, nil
			}
		}
	}
	return false, nil
}

// shortRosterError renders the roster failure without the long path: the
// underlying error of a path error, or the whole error when there is no
// path wrapper.
func shortRosterError(err error) string {
	if pe, ok := err.(*fs.PathError); ok {
		return pe.Err.Error()
	}
	return err.Error()
}
