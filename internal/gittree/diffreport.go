package gittree

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
)

// DiffReport audits a recorded edit task once, at settle or successful collect.
// Its permanent marker and readable flag follow Check's scratch-mirror policy.
// Git/read/write failures leave the command's status and exit code unchanged.
func DiffReport(sidecar, agent, report string, env platform.Env, logFile, command string) *jsonjs.Object {
	o := read(sidecar)
	if o == nil || core.Nowrite(env) {
		return nil
	}
	tree, _ := stringField(o, "git_tree")
	head, _ := stringField(o, "git_head")
	role, _ := stringField(o, "role")
	if tree == "" || !diffHeadRE.MatchString(head) || role == "" || !core.RoleIsEdit(role, env, tree) {
		return nil
	}
	if checked, _ := o.Get("diff_checked"); checked == true {
		return nil
	}
	dispatched, ok := diffDispatchTime(sidecar, o)
	if !ok {
		return nil
	}
	owned, hasOwned, scopeOK := diffEffectiveOwned(sidecar, agent, o)
	if !scopeOK {
		return nil
	}
	marker, err := os.OpenFile(sidecar+".diff-checked", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil
	}
	if marker.Close() != nil {
		return nil
	}
	// numstat forces content comparison even when the index's stat cache is
	// stale; name-only can otherwise report unchanged files without refresh.
	names, ok := gitOutput(env, tree, "-c", "diff.autoRefreshIndex=false", "diff", "--numstat", "--no-renames", "-z", head, "--")
	if !ok {
		return nil
	}
	newNames, ok := gitOutput(env, tree, "ls-files", "--others", "--exclude-standard", "-z")
	if !ok {
		return nil
	}
	changed := map[string]bool{}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return nil
	}
	defer root.Close()
	tracked, fresh := []string{}, []string{}
	for _, record := range strings.Split(names, "\x00") {
		if parts := strings.SplitN(record, "\t", 3); len(parts) == 3 {
			if diffModifiedAfter(root, parts[2], dispatched) {
				changed[parts[2]] = true
				tracked = append(tracked, parts[2])
			}
		}
	}
	for _, name := range strings.Split(newNames, "\x00") {
		if name != "" && diffModifiedAfter(root, name, dispatched) {
			changed[name] = true
			fresh = append(fresh, name)
		}
	}
	// Restrict Git's actual stat/list output to the same filtered inventory.
	// Literal pathspecs keep glob-shaped file names from widening that set.
	stat, untracked := "", ""
	if len(tracked) > 0 {
		args := append([]string{"--literal-pathspecs", "-c", "diff.autoRefreshIndex=false", "diff", "--stat", head, "--"}, tracked...)
		stat, ok = gitOutput(env, tree, args...)
		if !ok {
			return nil
		}
	}
	if len(fresh) > 0 {
		args := append([]string{"--literal-pathspecs", "ls-files", "--others", "--exclude-standard", "--"}, fresh...)
		untracked, ok = gitOutput(env, tree, args...)
		if !ok {
			return nil
		}
	}
	// A clean tree has no audit output or artifact, but still consumes the
	// once-only attempt so a subsequent wait/collect cannot repeat it.
	if stat == "" && untracked == "" {
		o.Set("diff_checked", true)
		_ = core.WriteScratchFile(sidecar, jsonjs.Stringify(o)+"\n")
		return nil
	}
	artifact := strings.TrimSuffix(report, filepath.Ext(report)) + ".diff-stat.txt"
	if core.WriteScratchFile(artifact, stat+untracked) != nil {
		return nil
	}
	outside := []string{}
	if hasOwned {
		freshSet := make(map[string]bool, len(fresh))
		for _, name := range fresh {
			freshSet[name] = true
		}
		for file := range changed {
			covered := false
			for _, pattern := range owned {
				if diffOwnedCover(pattern, file, freshSet) {
					covered = true
					break
				}
			}
			if !covered {
				outside = append(outside, file)
			}
		}
	}
	text, err := core.ReadScratchReport(report)
	if err != nil {
		return nil
	}
	cited := diffCited(tree, string(text), changed)
	sort.Strings(outside)
	o.Set("diff_checked", true)
	if core.WriteScratchFile(sidecar, jsonjs.Stringify(o)+"\n") != nil {
		return nil
	}
	out := jsonjs.O("diff_stat", artifact)
	if len(outside) > 0 {
		out.Set("outside_owned", outside)
		core.Warn(fmt.Sprintf("'%s' changed files outside its Owned files: %s", agent, diffNames(outside)), logFile, command)
	}
	if len(cited) > 0 {
		out.Set("cited_unchanged", cited)
		core.Warn(fmt.Sprintf("report of '%s' cites files the tree shows unchanged: %s", agent, diffNames(cited)), logFile, command)
	}
	return out
}

func diffModifiedAfter(root *os.Root, file string, dispatched time.Time) bool {
	info, err := root.Lstat(filepath.FromSlash(file))
	return err == nil && info.ModTime().After(dispatched)
}

// task_report's stable stem identifies the original task, including when
// this sidecar/report belongs to an amendment or a mirrored scratch member.
func diffDispatchTime(sidecar string, o *jsonjs.Object) (time.Time, bool) {
	for _, original := range diffOriginalStems(sidecar, o) {
		for _, candidate := range []string{original + ".brief.md", original + ".md"} {
			if info, err := core.StatScratchReport(candidate); err == nil {
				return info.ModTime(), true
			}
		}
	}
	return time.Time{}, false
}

func diffOriginalStems(sidecar string, o *jsonjs.Object) []string {
	stem := strings.TrimSuffix(sidecar, ".dispatch.json")
	stems := []string{stem}
	if task, _ := stringField(o, "task_report"); strings.HasSuffix(task, ".current.md") {
		base := strings.TrimSuffix(filepath.Base(task), ".current.md")
		stems = []string{filepath.Join(filepath.Dir(sidecar), base), filepath.Join(filepath.Dir(task), "..", "briefs", base)}
	}
	return stems
}

// Scope follows the latest explicit member at or before this attempt. The
// stable task_report identity also determines the independent time cutoff.
func diffEffectiveOwned(sidecar, agent string, o *jsonjs.Object) ([]string, bool, bool) {
	text, err := diffComposedBrief(sidecar)
	if err != nil {
		return nil, false, false
	}
	if owned, found := diffOwned(text); found {
		return owned, true, true
	}
	task, _ := stringField(o, "task_report")
	if strings.HasSuffix(task, ".current.md") {
		sd := filepath.Clean(filepath.Join(filepath.Dir(task), ".."))
		_, pointerErr := os.Lstat(taskreport.TaskReportPointerPath(sd, agent))
		pointer := taskreport.ReadTaskReportPointer(sd, agent)
		// Only an absent pointer permits the original single-task fallback.
		// An existing invalid or unreadable pointer leaves the audit retryable.
		if pointer == nil && !os.IsNotExist(pointerErr) {
			return nil, false, false
		}
		if pointer != nil {
			identity, _ := stringField(pointer, "task_report")
			if identity == task {
				history, _ := pointer.Get("history")
				items, _ := history.([]any)
				current, _ := stringField(pointer, "current")
				ordered := append(append([]any{}, items...), current)
				at := -1
				for i, item := range ordered {
					member, _ := item.(string)
					if strings.TrimSuffix(filepath.Base(member), ".md")+".dispatch.json" == filepath.Base(sidecar) {
						at = i
					}
				}
				for i := at - 1; i >= 0; i-- {
					member, _ := ordered[i].(string)
					sc := Sidecar(sd, member)
					entry := read(sc)
					if entry == nil {
						return nil, false, false
					}
					identity, _ := stringField(entry, "task_report")
					if identity == task {
						text, err := diffComposedBrief(sc)
						if err != nil {
							return nil, false, false
						}
						if owned, found := diffOwned(text); found {
							return owned, true, true
						}
					}
				}
			}
		}
	}
	for _, original := range diffOriginalStems(sidecar, o) {
		member := original + ".dispatch.json"
		text, err := diffComposedBrief(member)
		if err != nil {
			// State and scratch can provide alternative original locations.
			if os.IsNotExist(err) && member != sidecar {
				continue
			}
			return nil, false, false
		}
		if owned, found := diffOwned(text); found {
			return owned, true, true
		}
	}
	return nil, false, true
}

func diffNames(names []string) string {
	if len(names) <= 10 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:10], ", ") + fmt.Sprintf(", … (+%d more)", len(names)-10)
}

func diffComposedBrief(sidecar string) (string, error) {
	stem := strings.TrimSuffix(sidecar, ".dispatch.json")
	var missing error
	for _, candidate := range []string{stem + ".brief.md", stem + ".md"} {
		b, err := core.ReadScratchReport(candidate)
		if err == nil {
			return string(b), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		missing = err
	}
	return "", missing
}

var diffHeadingRE = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.+?)\s*#*\s*$`)
var diffHeadRE = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
var diffFenceRE = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
var diffTokenRE = regexp.MustCompile("`([^`\r\n]+)`")
var diffBulletRE = regexp.MustCompile(`^\s*(?:[-+*]|[0-9]+[.)])\s+`)

// diffNewTestsItemRE matches the prose Owned item that covers new test
// files: "new tests" or "new test files", case-insensitive, with an
// optional trailing period.
var diffNewTestsItemRE = regexp.MustCompile(`(?i)^new (?:tests|test files)\.?$`)

// diffMarkdown blanks fenced lines, including longer/tilde fences, before
// headings or inline path tokens are interpreted.
func diffMarkdown(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	fence := ""
	for i, line := range lines {
		if m := diffFenceRE.FindStringSubmatch(line); m != nil {
			if fence == "" {
				fence = m[1]
			} else if m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.TrimSpace(m[2]) == "" {
				fence = ""
			}
			lines[i] = ""
			continue
		}
		if fence != "" {
			lines[i] = ""
		}
	}
	return lines
}

func diffHeading(line string) (string, int) {
	if m := diffHeadingRE.FindStringSubmatch(line); m != nil {
		return strings.ToLower(strings.Trim(strings.TrimSpace(m[2]), "*` ")), len(m[1])
	}
	return "", 0
}

func diffRelative(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || filepath.IsAbs(raw) || strings.HasPrefix(raw, "\\") || strings.Contains(raw, "://") {
		return ""
	}
	clean := path.Clean(filepath.ToSlash(raw))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}

func diffOwned(text string) ([]string, bool) {
	lines := diffMarkdown(text)
	// A composed role may itself contain headings. Only its brief is scope.
	for i, line := range lines {
		if title, level := diffHeading(line); title == "brief" && level == 1 {
			lines = lines[i+1:]
			break
		}
	}
	items, found, ownedLevel := []string{}, false, 0
	for _, line := range lines {
		if title, level := diffHeading(line); level != 0 {
			if title == "report contract" && level == 1 {
				break
			}
			if ownedLevel > 0 && level <= ownedLevel {
				ownedLevel = 0
			}
			if title == "owned files" {
				found, ownedLevel = true, level
			}
			continue
		}
		if ownedLevel == 0 {
			continue
		}
		push := func(raw string) {
			if value := diffRelative(raw); value != "" {
				if strings.HasSuffix(raw, "/") {
					value += "/"
				}
				items = append(items, value)
			}
		}
		matches := diffTokenRE.FindAllStringSubmatch(line, -1)
		if len(matches) > 0 {
			for _, m := range matches {
				push(m[1])
			}
		} else if item := strings.TrimSpace(diffBulletRE.ReplaceAllString(line, "")); item != "" {
			if diffNewTestsItemRE.MatchString(item) {
				// The prose item names a kind of file, not a path: it maps
				// to the new-tests pattern, not the first-word pattern a
				// path item yields.
				items = append(items, diffOwnedNewTests)
			} else {
				push(strings.Fields(item)[0])
			}
		}
	}
	return items, found
}

// diffOwnedNewTests is the Owned pattern the prose items "new tests" and
// "new test files" map to. It is in-memory only: the NUL byte is not a
// path character, so it never collides with a listed file.
const diffOwnedNewTests = "\x00new tests"

// diffNewTestFile reports whether the file's name (its last slash
// segment) is a test file: *_test.go, *.test.*, *.spec.*, test_*.py or
// *_test.py.
func diffNewTestFile(file string) bool {
	base := path.Base(file)
	for _, pattern := range []string{"*_test.go", "*.test.*", "*.spec.*", "test_*.py", "*_test.py"} {
		if matched, err := filepath.Match(pattern, base); err == nil && matched {
			return true
		}
	}
	return false
}

// diffOwnedCover is diffOwnedMatch with the new-tests rule of the audit:
// the pattern the prose Owned items "new tests" / "new test files" map
// to covers a changed file only when the file is new and untracked and
// its name is a test file; a modified tracked test file still counts as
// outside.
func diffOwnedCover(pattern, file string, fresh map[string]bool) bool {
	if pattern == diffOwnedNewTests {
		return fresh[file] && diffNewTestFile(file)
	}
	return diffOwnedMatch(pattern, file)
}

func diffOwnedMatch(pattern, file string) bool {
	if pattern == diffOwnedNewTests {
		// Without the fresh-file context (the ChangedPaths delta) the
		// pattern covers by test-file name alone.
		return diffNewTestFile(file)
	}
	directory := strings.HasSuffix(pattern, "/")
	parts, names := strings.Split(strings.TrimSuffix(pattern, "/"), "/"), strings.Split(file, "/")
	if (!directory && len(parts) != len(names)) || (directory && len(names) <= len(parts)) {
		return false
	}
	for i, part := range parts {
		matched, err := filepath.Match(part, names[i])
		if err != nil || !matched {
			return false
		}
	}
	return true
}

func diffCited(tree, report string, changed map[string]bool) []string {
	root, err := os.OpenRoot(tree)
	if err != nil {
		return nil
	}
	defer root.Close()
	seen, forbiddenLevel := map[string]bool{}, 0
	for _, line := range diffMarkdown(report) {
		if title, level := diffHeading(line); level != 0 {
			if forbiddenLevel > 0 && level <= forbiddenLevel {
				forbiddenLevel = 0
			}
			if title == "forbidden" || title == "forbidden files" {
				forbiddenLevel = level
			}
		}
		if forbiddenLevel > 0 {
			continue
		}
		for _, m := range diffTokenRE.FindAllStringSubmatch(line, -1) {
			file := diffRelative(m[1])
			if file == "" || (!strings.Contains(file, "/") && path.Ext(file) == "") || changed[file] {
				continue
			}
			if info, err := root.Stat(filepath.FromSlash(file)); err == nil && !info.IsDir() {
				seen[file] = true
			}
		}
	}
	out := []string{}
	for file := range seen {
		out = append(out, file)
	}
	sort.Strings(out)
	return out
}
