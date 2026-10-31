package gittree

import (
	"errors"
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
	return diffEffectiveOwnedRead(sidecar, agent, o, commandOwnedSeams())
}

// diffEffectiveOwnedRead is the shared owned-scope walk: the composed
// brief read, the task-report pointer read and the historical sidecar
// reads come from the supplied seams, so the command, the read-only
// and the inspection-with-source callers decide only on the reads
// (ordinary, capped, or confined and capped) — the locate, the pointer
// identity, the history order and the owned-files decision are one
// algorithm.
func diffEffectiveOwnedRead(sidecar, agent string, o *jsonjs.Object, seams ownedReadSeams) ([]string, bool, bool) {
	text, err := diffComposedBriefRead(sidecar, seams.brief)
	if err != nil {
		return nil, false, false
	}
	if owned, found := diffOwned(text); found {
		return owned, true, true
	}
	task, _ := stringField(o, "task_report")
	if strings.HasSuffix(task, ".current.md") {
		sd := filepath.Clean(filepath.Join(filepath.Dir(task), ".."))
		pointer, pointerInvalid := seams.pointer(sd, agent)
		// Only an absent pointer permits the original single-task fallback.
		// An existing invalid or unreadable pointer leaves the audit
		// retryable.
		if pointer == nil && pointerInvalid {
			return nil, false, false
		}
		if pointer != nil {
			identity, _ := stringField(pointer, "task_report")
			if identity == task {
				history, _ := pointer.Get("history")
				items, _ := history.([]any)
				if seams.historyMembersMax > 0 && len(items) > seams.historyMembersMax {
					return nil, false, false
				}
				current, _ := stringField(pointer, "current")
				ordered := append(append([]any{}, items...), current)
				at := -1
				for i, item := range ordered {
					member, _ := item.(string)
					if strings.TrimSuffix(filepath.Base(member), ".md")+".dispatch.json" == filepath.Base(sidecar) {
						at = i
					}
				}
				// The single aggregate per-walk budget (auxHistoryBytesMax)
				// covers all historical content the loop consumes: the
				// locate-candidate sidecar reads (parseMember), the
				// selected sidecar entry reads (readHistEntry) and the
				// composed historical briefs (histBrief). It counts actual
				// successful bytes, including repeated physical reads; the
				// initial current composed brief keeps its separate
				// per-file cap, and the ordinary command and read-only
				// seams (historyBytesMax 0) keep their behavior.
				var histBytes int64
				budgetExceeded := false
				account := func(n int) {
					histBytes += int64(n)
					if seams.historyBytesMax > 0 && histBytes > seams.historyBytesMax {
						budgetExceeded = true
					}
				}
				parseMember := func(p string) *jsonjs.Object {
					raw, rerr := seams.rawLocate(p)
					if rerr == nil {
						account(len(raw))
					}
					return parseObjectBytes(raw, rerr)
				}
				readHistEntry := func(p string) ([]byte, error) {
					raw, err := seams.rawEntry(p)
					if err == nil {
						account(len(raw))
					}
					return raw, err
				}
				histBrief := func(candidate string) ([]byte, error) {
					raw, err := seams.brief(candidate)
					if err == nil {
						account(len(raw))
					}
					return raw, err
				}
				for i := at - 1; i >= 0; i-- {
					if budgetExceeded {
						return nil, false, false
					}
					member, _ := ordered[i].(string)
					sc := sidecarLocate(sd, member, parseMember)
					if budgetExceeded {
						return nil, false, false
					}
					raw, rerr := readHistEntry(sc)
					if budgetExceeded {
						return nil, false, false
					}
					entry := parseObjectBytes(raw, rerr)
					if entry == nil {
						return nil, false, false
					}
					identity, _ := stringField(entry, "task_report")
					if identity == task {
						text, err := diffComposedBriefRead(sc, histBrief)
						if err != nil {
							return nil, false, false
						}
						if budgetExceeded {
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
		text, err := diffComposedBriefRead(member, seams.brief)
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

// auxBriefLimit, auxPointerLimit, auxHistoryMembersMax and
// auxHistoryBytesMax are the D7 inspection caps of the owned-scope walk
// with a session source: the composed brief bytes, the task-report
// pointer bytes, the historical members and the aggregate per-walk
// historical content bytes (the locate-candidate sidecar reads, the
// selected sidecar reads and the composed historical briefs). The
// sidecar candidates keep the inspection's existing per-sidecar cap
// (sidecarInspectLimit, task.go).
const (
	auxBriefLimit        = 4 << 20
	auxPointerLimit      = 64 << 10
	auxHistoryMembersMax = 4096
	auxHistoryBytesMax   = 4 << 20
)

// parseObjectBytes is the shared sidecar/pointer parse over supplied
// bytes: a valid JSON object, or nil for unreadable or non-object
// bytes (the ordinary readers' refusal).
func parseObjectBytes(raw []byte, err error) *jsonjs.Object {
	if err != nil {
		return nil
	}
	value, perr := jsonjs.Parse(raw)
	if perr != nil {
		return nil
	}
	o, _ := value.(*jsonjs.Object)
	return o
}

// ownedReadSeams are the owned-scope walk's read decisions: the command
// caller's ordinary scratch readers (unbounded), the read-only caller's
// capped sidecar readers, and the inspection-with-source caller's
// confined capped readers; the walk itself is one shared algorithm.
type ownedReadSeams struct {
	rawLocate         func(string) ([]byte, error) // the historical member's sidecar candidate read
	rawEntry          func(string) ([]byte, error) // the historical member's sidecar entry read
	brief             func(candidate string) ([]byte, error)
	pointer           func(sd, agent string) (*jsonjs.Object, bool)
	historyMembersMax int
	historyBytesMax   int64
}

// commandOwnedSeams is the command's walk over the ordinary scratch
// readers: unbounded, no history limits — the exact pre-inspection
// behavior.
func commandOwnedSeams() ownedReadSeams {
	return ownedReadSeams{rawLocate: core.ReadScratchReport, rawEntry: core.ReadScratchReport, brief: core.ReadScratchReport, pointer: commandTaskPointer}
}

// readOnlyOwnedSeams is the read-only (no source) walk: the capped
// sidecar entry reads and the ordinary unbounded brief and locate
// reads, the shared command task-report pointer.
func readOnlyOwnedSeams() ownedReadSeams {
	return ownedReadSeams{rawLocate: core.ReadScratchReport, rawEntry: func(p string) ([]byte, error) { return core.ReadScratchReportLimited(p, sidecarInspectLimit) }, brief: core.ReadScratchReport, pointer: commandTaskPointer}
}

// sourceOwnedSeams is the read-only walk with a session source: every
// read confined to the source's retained roots at the D7 caps.
func sourceOwnedSeams(src *core.SessionSource) ownedReadSeams {
	aux := func(max int64) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) { raw, err := src.ReadAuxFile(p, max); return []byte(raw), err }
	}
	return ownedReadSeams{rawLocate: aux(sidecarInspectLimit), rawEntry: aux(sidecarInspectLimit), brief: aux(auxBriefLimit), pointer: sourceTaskPointer(src), historyMembersMax: auxHistoryMembersMax, historyBytesMax: auxHistoryBytesMax}
}

// commandTaskPointer is the command's task-report pointer read: the
// ordinary pointer read with the existing invalid-versus-absent
// discrimination the audit already relies on.
func commandTaskPointer(sd, agent string) (*jsonjs.Object, bool) {
	_, pointerErr := os.Lstat(taskreport.TaskReportPointerPath(sd, agent))
	pointer := taskreport.ReadTaskReportPointer(sd, agent)
	return pointer, pointer == nil && !os.IsNotExist(pointerErr)
}

// sourceTaskPointer is the inspection's task-report pointer read over
// the source's retained roots: the pointer is read only at the session
// directory the pointer location names (the state or the approved
// scratch session dir, in every base alias), bounded and confined; an
// existing pointer the cap or the confinement refuses, or a location
// outside the approved roots, is invalid (the audit stays retryable)
// while a truly absent pointer at an approved location is absent.
func sourceTaskPointer(src *core.SessionSource) func(sd, agent string) (*jsonjs.Object, bool) {
	return func(sd, agent string) (*jsonjs.Object, bool) {
		raw, err := src.ReadAuxFile(taskreport.TaskReportPointerPath(sd, agent), auxPointerLimit)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, false
			}
			return nil, true
		}
		pointer := taskreport.ParseTaskReportPointer([]byte(raw))
		return pointer, pointer == nil
	}
}

func diffNames(names []string) string {
	if len(names) <= 10 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:10], ", ") + fmt.Sprintf(", … (+%d more)", len(names)-10)
}

// diffComposedBriefRead is the composed-brief read the audit and the
// changed-paths walk share: the sidecar's own brief first, then the
// task-report stem's brief. The supplied reader is the only read
// decision: the command and read-only callers pass the ordinary
// scratch read, the inspection with a source passes its capped
// confined read.
func diffComposedBriefRead(sidecar string, briefRead func(candidate string) ([]byte, error)) (string, error) {
	stem := strings.TrimSuffix(sidecar, ".dispatch.json")
	var missing error
	for _, candidate := range []string{stem + ".brief.md", stem + ".md"} {
		b, err := briefRead(candidate)
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

// diffComposedBrief is the command's composed-brief read over the
// ordinary scratch reader: the shared walk with the ordinary read
// decision.
func diffComposedBrief(sidecar string) (string, error) {
	return diffComposedBriefRead(sidecar, core.ReadScratchReport)
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
