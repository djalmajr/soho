package setup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

// formerProjectIgnoreRules are the lines that ignored the former project
// configs. A line that, after trimming the spaces at the ends, is exactly
// one of these is the rule the ignore migration extends with the soho
// name.
var formerProjectIgnoreRules = []string{
	"/.agents/herdr-soho.conf",
	".agents/herdr-soho.conf",
	"/.agents/herdr-agents.conf",
	".agents/herdr-agents.conf",
}

// migrateIgnoreRule extends the rule that ignored a former project config
// with the soho.conf name, so the migrated file is not left untracked and
// committed by accident. It looks only at <root>/.gitignore and the git
// exclude, in that order, and acts on the first of them that still has a
// former rule: the new line lands right after that rule, copying its
// leading-slash style and the file's line endings, and at most one line is
// added per setup run. The step does nothing outside a git repository or
// when .agents/soho.conf is already ignored; with dry it only reports.
func migrateIgnoreRule(root string, env platform.Env, dry bool) []string {
	if !insideGitRepo(root, env) {
		return nil
	}
	if confAlreadyIgnored(root, env) {
		return nil
	}
	exclude := excludePath(root, env)
	for _, file := range []string{filepath.Join(root, ".gitignore"), exclude} {
		if file == "" || !lstatExists(file) {
			continue
		}
		if file == exclude {
			return migrateIgnoreExclude(root, env, dry, file)
		}
		// Raw bytes, not ReadTextFile: its CRLF-to-LF normalization would
		// change the endings of the lines this step leaves untouched.
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		updated, rule, found := insertSohoConfRule(string(data))
		if !found {
			continue
		}
		if dry {
			return []string{fmt.Sprintf("migrate: would add %s to %s", rule, file)}
		}
		if err := platform.AtomicWrite(file, updated); err != nil {
			return []string{fmt.Sprintf("migrate: kept %s: %v", file, err)}
		}
		return []string{fmt.Sprintf("migrate: added %s to %s", rule, file)}
	}
	return nil
}

func migrateIgnoreExclude(root string, env platform.Env, dry bool, file string) []string {
	var result []string
	if reason := lockedCommonExclude(root, env, "setup", dry, func(locked string) string {
		info, err := os.Lstat(locked)
		if os.IsNotExist(err) {
			return ""
		}
		if err != nil || !info.Mode().IsRegular() {
			result = []string{fmt.Sprintf("migrate: kept %s: not a readable regular exclude file", locked)}
			return ""
		}
		data, err := os.ReadFile(locked)
		if err != nil {
			result = []string{fmt.Sprintf("migrate: kept %s: %v", locked, err)}
			return ""
		}
		updated, rule, found := insertSohoConfRule(string(data))
		if !found {
			return ""
		}
		if dry {
			result = []string{fmt.Sprintf("migrate: would add %s to %s", rule, locked)}
			return ""
		}
		if err := writeSharedExclude(locked, string(data), updated, false); err != nil {
			result = []string{fmt.Sprintf("migrate: kept %s: %v", locked, err)}
			return ""
		}
		result = []string{fmt.Sprintf("migrate: added %s to %s", rule, locked)}
		return ""
	}); reason != "" {
		return []string{"migrate: kept the exclude: " + reason}
	}
	return result
}

// insideGitRepo reports whether root is inside a git repository, through
// the git-dir probe (exit status only, no output leaked to the terminal).
func insideGitRepo(root string, env platform.Env) bool {
	git, ok := platform.FindExecutable("git", env, platform.Current())
	if !ok {
		return false
	}
	cmd := exec.Command(git, "-C", root, "rev-parse", "--git-dir")
	cmd.Env = env.List()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// confAlreadyIgnored reports whether git already ignores the new project
// config by pattern alone. The --no-index keeps the answer valid even when
// the file is in the index; exit 0 means ignored (for example when the
// whole .agents/ dir is ignored), and nothing is added in that case.
func confAlreadyIgnored(root string, env platform.Env) bool {
	git, ok := platform.FindExecutable("git", env, platform.Current())
	if !ok {
		return false
	}
	cmd := exec.Command(git, "-C", root, "check-ignore", "-q", "--no-index", "--", ".agents/soho.conf")
	cmd.Env = env.List()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// insertSohoConfRule rewrites the file text with the soho.conf rule right
// after the first former rule it finds. The new line copies the former
// rule's leading-slash style and the file's line endings (\r\n when the
// file uses them, \n otherwise), and the former rule stays in place. When
// the former rule is the last line of a file without a final newline, the
// missing newline is supplied as the separator so the new rule gets its
// own line. The boolean reports whether a former rule was found.
func insertSohoConfRule(text string) (updated, rule string, found bool) {
	ending := "\n"
	if strings.Contains(text, "\r\n") {
		ending = "\r\n"
	}
	rest := text
	offset := 0
	for {
		idx := strings.IndexByte(rest, '\n')
		var line string
		var after int
		if idx < 0 {
			line = rest
			after = offset + len(rest)
		} else {
			line = rest[:idx]
			after = offset + idx + 1
		}
		if rule, ok := sohoConfRuleFor(strings.TrimSpace(line)); ok {
			if idx < 0 {
				// The former rule is the last line and the file ends without
				// a newline: appending the new rule right after it would glue
				// both rules into one line, so the file's missing final newline
				// is supplied as the separator and the new rule stays the
				// line-less last line.
				return text + ending + rule, rule, true
			}
			return text[:after] + rule + ending + text[after:], rule, true
		}
		if idx < 0 {
			return text, "", false
		}
		rest = rest[idx+1:]
		offset = after
	}
}

// sohoConfRuleFor reports whether the trimmed line is one of the former
// project-config ignore rules and, when it is, returns the soho.conf rule
// that copies its leading-slash style.
func sohoConfRuleFor(trimmed string) (string, bool) {
	for _, rule := range formerProjectIgnoreRules {
		if trimmed == rule {
			prefix := ""
			if strings.HasPrefix(rule, "/") {
				prefix = "/"
			}
			return prefix + ".agents/soho.conf", true
		}
	}
	return "", false
}
