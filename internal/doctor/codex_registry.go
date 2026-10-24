package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/codexenv"
	"github.com/djalmajr/soho/internal/platform"
)

func doctorCodexRegistryPolicy(env platform.Env, s *Say) {
	home := env.Get("CODEX_HOME")
	if home == "" {
		home = filepath.Join(platform.HomeDir(platform.Current(), env), ".codex")
	}
	path := filepath.Join(home, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	patterns, present, valid := registryIncludeOnly(string(data))
	if !valid {
		s.Warning("codex: invalid TOML in " + path + "; cannot check shell_environment_policy include_only")
		return
	}
	if !present {
		return
	}
	for _, pattern := range patterns {
		if codexenv.GlobMatch(pattern, "SOHO_REGISTRY_DIR") {
			return
		}
	}
	s.Warning(fmt.Sprintf(`codex: [shell_environment_policy] include_only in %s drops SOHO_* variables, so codex workers do not see SOHO_REGISTRY_DIR and register copies and processes in the wrong state dir; add "SOHO_*" to include_only`, path))
}

// registryIncludeOnly validates only shell_environment_policy/include_only.
// Unrelated syntax is ignored unless it prevents safe statement boundaries
// (such as an unclosed multiline string). Ambiguous target tables/keys and
// malformed pattern arrays are rejected with no file content in diagnostics.
// Headers, dotted keys and inline tables resolve to the same complete key path.
// The target table must use one introduction form; only root dotted assignments
// may repeat. Subtable and array-table headers do not introduce the target.
// This is not a general TOML validator; Codex diagnoses unrelated value errors.
func registryIncludeOnly(content string) ([]string, bool, bool) {
	statements, valid := policyStatements(content)
	if !valid {
		return nil, false, false
	}
	present, arrayTable := false, false
	var form policyTableForm
	var context []string
	var patterns []string
	readPatterns := func(value string) bool {
		if present {
			return false
		}
		patterns, valid = policyStringArray(strings.TrimSpace(value))
		present = valid
		return valid
	}
	for _, line := range statements {
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, false, false
			}
			arrayTable = strings.HasPrefix(line, "[[")
			if arrayTable {
				if !strings.HasSuffix(line, "]]") {
					return nil, false, false
				}
				line = line[1 : len(line)-1]
			}
			var rest string
			context, rest, valid = policyKeyPath(line[1 : len(line)-1])
			if !valid || rest != "" {
				return nil, false, false
			}
			if !arrayTable && policyPath(context) && !form.introduce(policyHeader) {
				return nil, false, false
			}
			continue
		}
		if arrayTable {
			continue
		}
		key, rest, keyValid := policyKeyPath(line)
		path := append(append([]string(nil), context...), key...)
		if !keyValid {
			if len(path) > 0 && path[0] == "shell_environment_policy" {
				return nil, false, false
			}
			continue
		}
		// Track every root assignment under the target, even an ignored key:
		// it defines the table shape independently of include_only's presence.
		if len(context) == 0 && len(key) > 1 && key[0] == "shell_environment_policy" && !form.introduce(policyDotted) {
			return nil, false, false
		}
		if !policyPath(path) && !policyIncludePath(path) {
			continue
		}
		if !strings.HasPrefix(rest, "=") {
			return nil, false, false
		}
		value := strings.TrimSpace(rest[1:])
		if policyIncludePath(path) {
			if !readPatterns(value) {
				return nil, false, false
			}
			continue
		}
		if !form.introduce(policyInline) || len(value) < 2 || value[0] != '{' || value[len(value)-1] != '}' {
			return nil, false, false
		}
		body := strings.TrimSpace(value[1 : len(value)-1])
		entries, ok := policySegments(body, ',')
		if !ok || strings.HasSuffix(body, ",") {
			return nil, false, false
		}
		for _, entry := range entries {
			key, rest, ok := policyKeyPath(entry)
			if !ok || !strings.HasPrefix(rest, "=") {
				return nil, false, false
			}
			if len(key) == 1 && key[0] == "include_only" && !readPatterns(rest[1:]) {
				return nil, false, false
			}
		}
	}
	return patterns, present, true
}

type policyTableForm uint8

const (
	policyHeader policyTableForm = iota + 1
	policyDotted
	policyInline
)

func (form *policyTableForm) introduce(next policyTableForm) bool {
	if *form != 0 && (*form != policyDotted || next != policyDotted) {
		return false
	}
	*form = next
	return true
}

func policyPath(path []string) bool {
	return len(path) == 1 && path[0] == "shell_environment_policy"
}

func policyIncludePath(path []string) bool {
	return len(path) == 2 && path[0] == "shell_environment_policy" && path[1] == "include_only"
}

// policyKeyPath keeps quoted dots inside a part and decodes only key names.
// The remainder starts at the assignment (or at ambiguous trailing syntax).
func policyKeyPath(input string) ([]string, string, bool) {
	s := strings.TrimSpace(input)
	var path []string
	for {
		if s == "" {
			return path, s, false
		}
		end := 0
		var part string
		if s[0] == '\'' || s[0] == '"' {
			q := s[0]
			end = 1
			for end < len(s) && s[end] != q {
				if q == '"' && s[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(s) {
				return path, s, false
			}
			part = s[1:end]
			end++
			if q == '"' {
				var err error
				part, err = strconv.Unquote(s[:end])
				if err != nil {
					return path, s, false
				}
			}
		} else {
			for end < len(s) && (s[end] >= 'a' && s[end] <= 'z' || s[end] >= 'A' && s[end] <= 'Z' || s[end] >= '0' && s[end] <= '9' || s[end] == '_' || s[end] == '-') {
				end++
			}
			if end == 0 {
				return path, s, false
			}
			part = s[:end]
		}
		path = append(path, part)
		s = strings.TrimSpace(s[end:])
		if !strings.HasPrefix(s, ".") {
			return path, s, true
		}
		s = strings.TrimSpace(s[1:])
	}
}

// Split logical TOML statements without interpreting unrelated values. Quoted
// text, including multiline strings, cannot impersonate the target table.
func policyStatements(content string) ([]string, bool) {
	return policySegments(content, '\n')
}

func policySegments(content string, separator byte) ([]string, bool) {
	var out []string
	var b strings.Builder
	quote := byte(0)
	triple, comment := false, false
	var stack []byte
	flush := func() {
		if line := strings.TrimSpace(b.String()); line != "" {
			out = append(out, line)
		}
		b.Reset()
	}
	for i := 0; i < len(content); i++ {
		c := content[i]
		if comment && c != '\n' {
			continue
		}
		comment = false
		if quote != 0 {
			b.WriteByte(c)
			if c == '\\' && quote == '"' && i+1 < len(content) {
				i++
				b.WriteByte(content[i])
				continue
			}
			if c == '\n' && !triple {
				return nil, false
			}
			if c == quote {
				if !triple {
					quote = 0
				} else if i+2 < len(content) && content[i+1] == c && content[i+2] == c {
					b.WriteString(content[i+1 : i+3])
					i += 2
					quote = 0
				}
			}
			continue
		}
		switch c {
		case '#':
			comment = true
			continue
		case '\'', '"':
			quote = c
			triple = i+2 < len(content) && content[i+1] == c && content[i+2] == c
			if triple {
				b.WriteString(content[i : i+2])
				i += 2
			}
		case '[', '{':
			stack = append(stack, c)
		case ']', '}':
			if len(stack) == 0 || (c == ']' && stack[len(stack)-1] != '[') || (c == '}' && stack[len(stack)-1] != '{') {
				return nil, false
			}
			stack = stack[:len(stack)-1]
		case '\n', ',':
			if c == separator && len(stack) == 0 {
				flush()
				continue
			}
		}
		b.WriteByte(c)
	}
	if quote != 0 || len(stack) != 0 {
		return nil, false
	}
	flush()
	return out, true
}

func policyStringArray(value string) ([]string, bool) {
	if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
		return nil, false
	}
	s := strings.TrimSpace(value[1 : len(value)-1])
	var out []string
	for s != "" {
		if s[0] != '"' && s[0] != '\'' {
			return nil, false
		}
		q, end := s[0], 1
		for end < len(s) {
			if q == '"' && s[end] == '\\' {
				end += 2
				continue
			}
			if s[end] == q {
				break
			}
			end++
		}
		if end >= len(s) {
			return nil, false
		}
		item := s[1:end]
		if q == '"' {
			var err error
			item, err = strconv.Unquote(s[:end+1])
			if err != nil {
				return nil, false
			}
		}
		out = append(out, item)
		s = strings.TrimSpace(s[end+1:])
		if s == "" {
			break
		}
		if s[0] != ',' {
			return nil, false
		}
		s = strings.TrimSpace(s[1:])
	}
	return out, true
}
