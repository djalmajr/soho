package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// A rooted native link is the atomic no-replace boundary. Tests interleave a
// destination writer at this operation while retaining its real semantics.
var linkProjectRole = (*os.Root).Link

// migrateProjectRoles moves entries only through a project-rooted handle,
// so a raced or pre-existing symlink cannot redirect a move outside the project.
func migrateProjectRoles(project string, env platform.Env, dry bool) []string {
	old, next := filepath.Join(".agents", "herdr-roles"), filepath.Join(".agents", "soho-roles")
	root, err := os.OpenRoot(project)
	if err != nil {
		return nil
	}
	defer root.Close()
	info, err := root.Lstat(old)
	if os.IsNotExist(err) {
		return nil
	}
	kept := func(path string, cause any) string {
		return fmt.Sprintf("migrate: kept %s: %v", filepath.Join(project, path), cause)
	}
	if err != nil {
		return []string{kept(old, err)}
	}
	if !info.IsDir() {
		return []string{kept(old, "not a real directory; symlinks are not followed")}
	}
	verb := "moved"
	if dry {
		verb = "would move"
	}
	dest, err := root.Lstat(next)
	if os.IsNotExist(err) {
		if !dry {
			if err := root.Mkdir(next, 0o700); err != nil && !os.IsExist(err) {
				return []string{kept(old, err)}
			}
			dest, err = root.Lstat(next)
		} else {
			err = nil
		}
	}
	if err != nil {
		return []string{kept(old, err)}
	}
	if dest != nil && !dest.IsDir() {
		return []string{kept(old, "destination is not a real directory; symlinks are not followed")}
	}
	dir, err := root.Open(old)
	if err != nil {
		return []string{kept(old, err)}
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return []string{kept(old, err)}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	lines := []string{}
	remaining := 0
	moved := false
	for _, entry := range entries {
		src, dst := filepath.Join(old, entry.Name()), filepath.Join(next, entry.Name())
		info, err := root.Lstat(src)
		if err != nil || !info.Mode().IsRegular() {
			remaining++
			lines = append(lines, kept(src, "not a regular file; move by hand"))
			continue
		}
		if dry {
			if _, err := root.Lstat(dst); !os.IsNotExist(err) {
				remaining++
				lines = append(lines, kept(src, fmt.Sprintf("%s already exists; merge by hand", filepath.Join(project, dst))))
				continue
			}
		} else {
			if err := linkProjectRole(root, src, dst); err != nil {
				remaining++
				cause := any(fmt.Sprintf("atomic no-replace move unavailable or destination conflicted (%v); move by hand", err))
				if os.IsExist(err) {
					cause = fmt.Sprintf("%s already exists; merge by hand", filepath.Join(project, dst))
				}
				lines = append(lines, kept(src, cause))
				continue
			}
			if err := root.Remove(src); err != nil {
				remaining++
				lines = append(lines, kept(src, fmt.Sprintf("destination linked, source retained (%v)", err)))
				continue
			}
			moved = true
		}
		lines = append(lines, fmt.Sprintf("migrate: %s %s to %s", verb, filepath.Join(project, src), filepath.Join(project, dst)))
	}
	if remaining == 0 {
		if dry {
			lines = append(lines, "migrate: would remove the empty "+filepath.Join(project, old))
		} else if err := root.Remove(old); err != nil {
			lines = append(lines, kept(old, err))
		} else {
			lines = append(lines, "migrate: removed the empty "+filepath.Join(project, old))
		}
	}
	if moved {
		lines = append(lines, migrateRolesExclude(project, env)...)
	}
	return lines
}

const rolesSetupUsage = "usage: setup --roles [--plan|--dry-run]"

func cmdSetupRoles(args []string, env platform.Env, cwd string) {
	dry := false
	for _, arg := range args {
		switch arg {
		case "--roles":
		case "--plan", "--dry-run":
			dry = true
		default:
			platform.DieFriction(rolesSetupUsage, 2)
		}
	}
	project := platform.ProjectRoot(env, cwd)
	if skill := core.StatePathInSkill(env, project); skill != "" {
		platform.DieFriction(core.StateInSkillMessage("setup", project, skill), 2)
	}
	for _, line := range migrateProjectRoles(project, env, dry) {
		fmt.Fprintln(platform.Stdout, line)
	}
}

// Only Git's local exclude is extended; tracked ignore/instruction files are
// never candidates. Git resolves a linked worktree to its common exclude.
func migrateRolesExclude(project string, env platform.Env) []string {
	file := excludePath(project, env)
	if file == "" {
		return nil
	}
	if reason := excludeRedirect(project, env); reason != "" {
		return []string{"migrate: kept the exclude: " + reason}
	}
	info, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return []string{fmt.Sprintf("migrate: kept %s: not a readable regular exclude file", file)}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return []string{fmt.Sprintf("migrate: kept %s: %v", file, err)}
	}
	updated, rule := rolesExcludeResult(string(data))
	if rule == "" {
		return nil
	}
	if err := platform.AtomicWrite(file, updated); err != nil {
		return []string{fmt.Sprintf("migrate: kept %s: %v", file, err)}
	}
	return []string{fmt.Sprintf("migrate: added %s to %s", rule, file)}
}

func rolesExcludeResult(text string) (string, string) {
	rule := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		plain := strings.TrimSuffix(strings.TrimPrefix(line, "/"), "/")
		if plain == ".agents/soho-roles" {
			return text, ""
		}
		if rule == "" && plain == ".agents/herdr-roles" {
			rule = strings.Replace(line, "herdr-roles", "soho-roles", 1)
		}
	}
	if rule == "" {
		return text, ""
	}
	ending := "\n"
	if strings.Contains(text, "\r\n") {
		ending = "\r\n"
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += ending
	}
	return text + rule + ending, rule
}
