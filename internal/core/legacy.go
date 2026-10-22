package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

// LegacyUserConfigPath is the former user configuration file, kept so the
// doctor and the setup migration can detect a leftover old installation.
// Nothing reads the file itself: the binary only reads the soho names.
func LegacyUserConfigPath(platformName string, env platform.Env) string {
	if xdg := env.Get("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "herdr-soho", "config")
	}
	if platformName == "win32" {
		if appdata := env.Get("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "herdr-soho", "config")
		}
		return filepath.Join(platform.HomeDir(platformName, env), "AppData", "Roaming", "herdr-soho", "config")
	}
	return filepath.Join(platform.HomeDir(platformName, env), ".config", "herdr-soho", "config")
}

func LegacyProjectConfigPath(root string) string {
	return filepath.Join(root, ".agents", "herdr-soho.conf")
}

func LegacyStatePath(root string) string { return filepath.Join(root, ".herdr-soho") }

// LegacyLeftovers names the former herdr-soho and herdr-agents artifacts
// an old installation left behind, for the doctor's leftover warnings: the
// former HERDR_SOHO_ environment variables (non-empty, alphabetical), then
// the former project config and state dir, then the former user config
// dirs, the former herdr-agents project config and state dir, and an old
// herdr-soho binary still on PATH, each when present. Nothing it names is
// read: they are detection-only paths.
func LegacyLeftovers(env platform.Env, cwd, platformName string) []string {
	out := make([]string, 0)
	names := make([]string, 0)
	for name, value := range env {
		if strings.HasPrefix(name, "HERDR_SOHO_") && value != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, fmt.Sprintf("environment variable %s is no longer read; rename it to SOHO_%s", name, strings.TrimPrefix(name, "HERDR_SOHO_")))
	}
	root := platform.ProjectRoot(env, cwd)
	if isFile(LegacyProjectConfigPath(root)) {
		out = append(out, fmt.Sprintf("former project config %s is no longer read: run 'soho setup' to move it to %s", LegacyProjectConfigPath(root), filepath.Join(root, ".agents", "soho.conf")))
	}
	if isDir(LegacyStatePath(root)) {
		current := filepath.Join(root, ".soho")
		ctx := LoadConfig(env, cwd)
		effective := StateRootPath(&ctx, env, cwd)
		// setup moves the former dir only onto an absent or empty one and
		// never when the state dir is pinned; in those cases it keeps the
		// former one, so say what setup says instead of sending the user to
		// it. Removal is advised only when both dirs are real directories
		// and nothing is pinned: a pin or a link may still reach the state
		// through the former dir. A former dir the state dir resolves into
		// is in use: no warning.
		switch {
		case stateWithinFormer(LegacyStatePath(root), effective):
		case StateDirExplicitlyPinned(&ctx, env):
			out = append(out, fmt.Sprintf("former state dir %s is kept: the state dir is set to %s", LegacyStatePath(root), effective))
		case dirHasContent(current) && realDir(current) && realDir(LegacyStatePath(root)):
			out = append(out, fmt.Sprintf("former state dir %s is no longer used and %s already exists: remove %s once its reports are no longer needed", LegacyStatePath(root), current, LegacyStatePath(root)))
		case dirHasContent(current):
			out = append(out, fmt.Sprintf("former state dir %s is kept: %s already exists", LegacyStatePath(root), current))
		default:
			out = append(out, fmt.Sprintf("former state dir %s is no longer used: run 'soho setup' to move it to %s once no worker is live", LegacyStatePath(root), current))
		}
	}
	formerUserDir := filepath.Dir(LegacyUserConfigPath(platformName, env))
	for _, dir := range []string{formerUserDir, filepath.Join(filepath.Dir(formerUserDir), "herdr-agents")} {
		if dirHasContent(dir) {
			out = append(out, fmt.Sprintf("former user config dir %s is still there: run 'soho setup' to move it to %s", dir, filepath.Dir(platform.UserConfigPath(platformName, env))))
		}
	}
	if agentsProject := filepath.Join(root, ".agents", "herdr-agents.conf"); isFile(agentsProject) {
		out = append(out, fmt.Sprintf("former project config %s is no longer read: run 'soho setup' to move it to %s", agentsProject, filepath.Join(root, ".agents", "soho.conf")))
	}
	if agentsState := filepath.Join(root, ".herdr-agents"); isDir(agentsState) {
		out = append(out, fmt.Sprintf("former state dir %s is no longer used: remove it once its reports are no longer needed", agentsState))
	}
	if binary, found := platform.FindExecutable("herdr-soho", env, platformName); found {
		out = append(out, fmt.Sprintf("an old herdr-soho binary is still on PATH at %s: remove it; soho replaces it", binary))
	}
	return out
}

// dirHasContent reports a directory that exists and holds at least one
// entry: an empty former dir has nothing to migrate.
func dirHasContent(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// stateWithinFormer reports whether the effective state dir is the former
// dir or lies under it, either as written or once symlinks are resolved: a
// pin reached through a link still lives in the former tree, and a former
// dir that is a link to the state dir is an alias of it, not a leftover.
func stateWithinFormer(former, effective string) bool {
	if pathWithinOrSame(former, effective) {
		return true
	}
	return pathWithinOrSame(resolveExisting(former), resolveExisting(effective))
}

// resolveExisting resolves the symlinks of the longest existing prefix of
// p and keeps the rest as written, so a state dir not created yet still
// resolves through a linked parent.
func resolveExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// pathWithinOrSame reports whether candidate is dir itself or lies under it,
// compared lexically after cleaning.
func pathWithinOrSame(dir, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(candidate))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// realDir reports a directory that is not a symlink.
func realDir(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.IsDir()
}
