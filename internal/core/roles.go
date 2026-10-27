package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	textutil "github.com/djalmajr/soho/internal/text"
)

const EditRoles = "implementer designer tasker"
const ReviewRoles = "reviewer security-reviewer"
const ReviewRolesAll = "reviewer security-reviewer ui-reviewer inspector"

var repeatedSpacesRE = regexp.MustCompile(` +`)

func RoleDirs(env platform.Env, cwd string) []string {
	dirs := make([]string, 0, 3)
	project := filepath.Join(platform.ProjectRoot(env, cwd), ".agents", "soho-roles")
	if isDir(project) {
		dirs = append(dirs, project)
	}
	// One roles override: SOHO_ROLES.
	extra := env.Get("SOHO_ROLES")
	if extra != "" && isDir(extra) {
		dirs = append(dirs, extra)
	}
	dirs = append(dirs, filepath.Join(platform.SkillDir(env), "roles"))
	return dirs
}

func RoleFile(role string, env platform.Env, cwd string) string {
	if strings.HasSuffix(role, ".append") {
		return ""
	}
	for _, dir := range RoleDirs(env, cwd) {
		file := filepath.Join(dir, role+".md")
		if isFile(file) {
			return file
		}
	}
	return ""
}

func ResolveRole(role string, env platform.Env, cwd string) string {
	if file := RoleFile(role, env, cwd); file != "" {
		return file
	}
	platform.Die(fmt.Sprintf("unknown role '%s' (run: soho roles)", role)+UnknownRoleHint(role, env, cwd), 3)
	return ""
}

// UnknownRoleHint inspects only the legacy file's metadata. Role resolution
// continues to read exclusively from RoleDirs' current locations.
func UnknownRoleHint(role string, env platform.Env, cwd string) string {
	if role == "" || strings.ContainsAny(role, `/\`) || filepath.Base(role) != role {
		return ""
	}
	legacy := filepath.Join(platform.ProjectRoot(env, cwd), ".agents", "herdr-roles", role+".md")
	if _, err := os.Lstat(legacy); err == nil {
		return fmt.Sprintf("; .agents/herdr-roles/%s.md is the old location: run soho setup --roles to move it to .agents/soho-roles/", role)
	}
	return ""
}

func isDir(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func FmGet(file, key string) string {
	if strings.HasSuffix(file, ".append.md") {
		return ""
	}
	text, err := platform.ReadTextFile(file)
	if err != nil {
		return ""
	}
	lines := splitLines(text)
	if len(lines) == 0 || lines[0] != "---" {
		return ""
	}
	for _, line := range lines[1:] {
		if line == "---" {
			break
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 || trimJSWhitespace(line[:colon]) != key {
			continue
		}
		value := trimJSWhitespace(line[colon+1:])
		if strings.HasPrefix(value, "[") {
			value = value[1:]
		}
		if strings.HasSuffix(value, "]") {
			value = value[:len(value)-1]
		}
		value = strings.ReplaceAll(value, ",", " ")
		value = strings.ReplaceAll(value, `"`, "")
		return repeatedSpacesRE.ReplaceAllString(value, " ")
	}
	return ""
}

func RoleBody(file string) string {
	text, err := platform.ReadTextFile(file)
	if err != nil {
		return ""
	}
	lines := splitLines(text)
	if len(lines) == 0 {
		return ""
	}
	if lines[0] != "---" {
		return strings.Join(lines, "\n") + "\n"
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			if i+1 == len(lines) {
				return ""
			}
			return strings.Join(lines[i+1:], "\n") + "\n"
		}
	}
	return ""
}

// RoleBodyFor composes a resolved base and the project's body-only extension.
// ComposePrompt may supply its already-resolved base, preserving its explicit
// file contract. An absent base never turns an append into a standalone role.
func RoleBodyFor(role string, env platform.Env, cwd string, base ...string) string {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	file := ""
	if len(base) > 0 {
		file = base[0]
	} else {
		file = RoleFile(role, env, cwd)
	}
	if file == "" || !isFile(file) {
		return ""
	}
	body := RoleBody(file)
	appendFile := filepath.Join(platform.ProjectRoot(env, cwd), ".agents", "soho-roles", role+".append.md")
	if !isFile(appendFile) {
		return body
	}
	extension := RoleBody(appendFile)
	if extension == "" {
		return body
	}
	return strings.TrimRight(body, "\r\n") + "\n\n" + extension
}

func roleSource(role, file string, env platform.Env, cwd string) string {
	project := filepath.Join(platform.ProjectRoot(env, cwd), ".agents", "soho-roles")
	source := "skill"
	if filepath.Clean(file) == filepath.Join(project, role+".md") {
		source = "project"
	}
	if isFile(filepath.Join(project, role+".append.md")) {
		source += "+append"
	}
	return source
}

func RoleIsEdit(role string, env platform.Env, cwd string) bool {
	if hasWord(EditRoles, role) {
		return true
	}
	file := RoleFile(role, env, cwd)
	return file != "" && FmGet(file, "mode") == "edit"
}

func HistoryHasEdit(history string, env platform.Env, cwd string) bool {
	for _, role := range strings.Split(history, ",") {
		if RoleIsEdit(strings.TrimSpace(role), env, cwd) {
			return true
		}
	}
	return false
}

func IsReviewRole(role string) bool { return hasWord(ReviewRolesAll, role) }

func hasWord(list, needle string) bool {
	for _, word := range strings.Fields(list) {
		if word == needle {
			return true
		}
	}
	return false
}

func CmdRole(argv []string, env platform.Env, cwd string) {
	if len(argv) == 0 {
		_, _ = platform.Stderr.Write([]byte("soho: 1: role\n"))
		panic(&platform.ExitError{Code: 1})
	}
	f := ResolveRole(argv[0], env, cwd)
	alt := []string{}
	for _, value := range strings.Split(FmGet(f, "alternatives"), " ") {
		if value != "" {
			alt = append(alt, value)
		}
	}
	role := jsonjs.O("file", f, "name", FmGet(f, "name"), "kind", FmGet(f, "kind"), "alternatives", stringsToAny(alt), "mode", FmGet(f, "mode"), "timeout", FmGet(f, "timeout"), "effort", FmGet(f, "effort"), "model", FmGet(f, "model"), "approvals", FmGet(f, "approvals"), "description", FmGet(f, "description"))
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.StringifyIndent(role, 2))
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// roleTableRow is one row of the roles table: the role's file and the
// settings the text table and the JSON output both show. The rows keep the
// text order: the role dirs in priority order, the names sorted UTF16 per
// dir, one entry per role name (the first dir wins).
type roleTableRow struct {
	Role   string
	File   string
	Res    RoleSettings
	Mode   string
	Source string
}

func collectRoles(env platform.Env, cwd string) []roleTableRow {
	ctx := LoadConfig(env, cwd)
	rows := []roleTableRow{}
	seen := map[string]bool{}
	for _, dir := range RoleDirs(env, cwd) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".md") && !strings.HasSuffix(entry.Name(), ".append.md") {
				names = append(names, entry.Name())
			}
		}
		textutil.SortUTF16(names)
		for _, name := range names {
			file := filepath.Join(dir, name)
			if !isFile(file) {
				continue
			}
			role := strings.TrimSuffix(name, ".md")
			if seen[role] {
				continue
			}
			seen[role] = true
			rows = append(rows, roleTableRow{Role: role, File: file, Res: ResolveRoleSettings(role, &ctx, env, cwd, RoleFlags{}), Mode: FmGet(file, "mode"), Source: roleSource(role, file, env, cwd)})
		}
	}
	return rows
}

func CmdRoles(env platform.Env, cwd string) {
	lines := []string{padUTF16("ROLE", 18) + " " + padUTF16("KIND", 8) + " " + padUTF16("MODEL", 24) + " " + padUTF16("EFFORT", 8) + " " + padUTF16("MODE", 10) + " FROM"}
	cell := func(value string) string {
		if value == "" {
			return "-"
		}
		return value
	}
	for _, row := range collectRoles(env, cwd) {
		res := row.Res
		from := fmt.Sprintf("source: %s; kind: %s; model: %s; effort: %s; file: %s", row.Source, res.KindFrom, res.ModelFrom, res.EffortFrom, row.File)
		lines = append(lines, padUTF16(row.Role, 18)+" "+padUTF16(cell(res.Kind), 8)+" "+padUTF16(cell(res.ModelSpec), 24)+" "+padUTF16(cell(res.Effort), 8)+" "+padUTF16(cell(row.Mode), 10)+" "+from)
	}
	_, _ = fmt.Fprintln(platform.Stdout, strings.Join(lines, "\n"))
}

// CmdRolesJSON prints the roles table as one compact JSON object on one
// line (the plugin's read): the same roles, order and values as the text
// table, with the empty values as "" (the "-" cells are text-only).
func CmdRolesJSON(env platform.Env, cwd string) {
	rows := collectRoles(env, cwd)
	roles := make([]any, 0, len(rows))
	for _, row := range rows {
		res := row.Res
		roles = append(roles, jsonjs.O(
			"role", row.Role,
			"kind", res.Kind,
			"model", res.ModelSpec,
			"effort", res.Effort,
			"mode", row.Mode,
			"lane", res.Lane,
			"kind_from", res.KindFrom,
			"model_from", res.ModelFrom,
			"effort_from", res.EffortFrom,
			"file", row.File,
			"source", row.Source,
		))
	}
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O("roles", roles)))
}
