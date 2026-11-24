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
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/text"
)

// Legacy sidecars did not record the executable bit. Preserve their content-only comparison.
func sameFingerprint(before any, now string) bool {
	old, ok := before.(string)
	if !ok {
		return false
	}
	if strings.HasPrefix(old, "file:") && !strings.HasPrefix(old, "file:true:") && !strings.HasPrefix(old, "file:false:") {
		return old == "file:"+strings.TrimPrefix(strings.TrimPrefix(now, "file:true:"), "file:false:")
	}
	return old == now
}

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
	owned, hasOwned, scopeOK := diffEffectiveOwnedRead(sidecar, agent, o, auditOwnedSeams(env))
	if !scopeOK {
		return nil
	}
	snapshot, snapshotPresent := snapshotFromSidecar(sidecar, o)
	if snapshotPresent {
		for _, name := range snapshot.Keys() {
			v, _ := snapshot.Get(name)
			if _, ok := v.(string); !ok {
				snapshotPresent = false
				break
			}
		}
	}
	markerValue, _ := o.Get("git_snapshot_file")
	incomplete := markerValue == true && !snapshotPresent
	// An unreadable optional scope cannot suppress the existing Owned audit.
	orchestrator, _, _ := diffEffectiveSectionRead(sidecar, agent, o, auditOwnedSeams(env), "orchestrator files")
	if !incomplete {
		marker, err := os.OpenFile(sidecar+".diff-checked", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil
		}
		if marker.Close() != nil {
			return nil
		}
	}
	// Only a descendant HEAD can separate intervening commits from the
	// worker's uncommitted changes without changing the historical fallback.
	base := head
	committed := []string{}
	if current, ok := gitOutput(env, tree, "rev-parse", "--verify", "HEAD"); ok {
		current = strings.TrimSpace(current)
		if diffHeadRE.MatchString(current) && current != head {
			if _, ancestor := gitOutput(env, tree, "merge-base", "--is-ancestor", head, current); ancestor {
				names, ok := gitOutput(env, tree, "diff", "--name-only", "--no-renames", "-z", head, current, "--")
				if !ok {
					return nil
				}
				for _, name := range strings.Split(names, "\x00") {
					if name != "" {
						committed = append(committed, name)
					}
				}
				sort.Strings(committed)
				base = current
			}
		}
	}
	// numstat forces content comparison even when the index's stat cache is
	// stale; name-only can otherwise report unchanged files without refresh.
	names, ok := gitOutput(env, tree, "-c", "diff.autoRefreshIndex=false", "diff", "--numstat", "--no-renames", "-z", base, "--")
	if !ok {
		return nil
	}
	newNames, ok := gitOutput(env, tree, "ls-files", "--others", "--exclude-standard", "-z")
	if !ok {
		return nil
	}
	changed := map[string]bool{}
	headChanged := map[string]bool{}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return nil
	}
	defer root.Close()
	changedNow := func(name string) bool {
		headChanged[name] = true
		if !snapshotPresent {
			return diffModifiedAfter(root, name, dispatched)
		}
		before, exists := snapshot.Get(name)
		if !exists {
			return true
		}
		now, ok := contentHashForGit(env, tree, root, name)
		return !ok || !sameFingerprint(before, now) || strings.HasPrefix(now, "gitlink:") && diffModifiedAfter(root, name, dispatched)
	}
	tracked, fresh := []string{}, []string{}
	for _, record := range strings.Split(names, "\x00") {
		if parts := strings.SplitN(record, "\t", 3); len(parts) == 3 {
			if changedNow(parts[2]) {
				changed[parts[2]] = true
				tracked = append(tracked, parts[2])
			}
		}
	}
	for _, name := range strings.Split(newNames, "\x00") {
		if name != "" && changedNow(name) {
			changed[name] = true
			fresh = append(fresh, name)
		}
	}
	snapshotOnly := []string{}
	committedSet := make(map[string]bool, len(committed))
	for _, name := range committed {
		committedSet[name] = true
	}
	if snapshotPresent {
		for _, name := range snapshot.Keys() {
			if headChanged[name] || committedSet[name] {
				continue
			}
			before, _ := snapshot.Get(name)
			now, ok := contentHashForGit(env, tree, root, name)
			if !ok || !sameFingerprint(before, now) || strings.HasPrefix(now, "gitlink:") && diffModifiedAfter(root, name, dispatched) {
				changed[name] = true
				snapshotOnly = append(snapshotOnly, name)
			}
		}
	}
	// Restrict Git's actual stat/list output to the same filtered inventory.
	// Literal pathspecs keep glob-shaped file names from widening that set.
	stat, untracked := "", ""
	if len(tracked) > 0 {
		args := append([]string{"--literal-pathspecs", "-c", "diff.autoRefreshIndex=false", "diff", "--stat", base, "--"}, tracked...)
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
	// A clean tree has no artifact and no worker-diff output (only the
	// committed_during_task list when HEAD moved), but still consumes the
	// once-only attempt so a subsequent wait/collect cannot repeat it.
	if stat == "" && untracked == "" && len(changed) == 0 {
		if !incomplete {
			o.Set("diff_checked", true)
			_ = core.WriteScratchFile(sidecar, jsonjs.Stringify(o)+"\n")
		}
		if len(committed) > 0 {
			return jsonjs.O("committed_during_task", committed)
		}
		return nil
	}
	sort.Strings(snapshotOnly)
	if len(snapshotOnly) > 0 {
		stat += "Snapshot-only changes:\n" + strings.Join(snapshotOnly, "\n") + "\n"
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
				for _, pattern := range orchestrator {
					if diffOwnedCover(pattern, file, freshSet) {
						covered = true
						break
					}
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
	for _, file := range committed {
		changed[file] = true
	}
	if snapshotPresent {
		for name := range headChanged {
			changed[name] = true
		}
		for _, name := range snapshot.Keys() {
			changed[name] = true
		}
	}
	cited := diffCited(tree, string(text), changed)
	sort.Strings(outside)
	if !incomplete {
		o.Set("diff_checked", true)
		if core.WriteScratchFile(sidecar, jsonjs.Stringify(o)+"\n") != nil {
			return nil
		}
	}
	out := jsonjs.O("diff_stat", artifact)
	if len(committed) > 0 {
		out.Set("committed_during_task", committed)
	}
	if len(outside) > 0 {
		out.Set("outside_owned", outside)
		core.Warn(fmt.Sprintf("worktree files changed during the task for '%s' outside its Owned files: %s", agent, diffNames(outside)), logFile, command)
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
	owned, found, ok := diffEffectiveSectionRead(sidecar, agent, o, seams, "owned files")
	if !ok || !found {
		return owned, found, ok
	}
	filtered := owned[:0]
	inherit := false
	for _, item := range owned {
		if item == "\x00inherit previous brief" {
			inherit = true
		} else {
			filtered = append(filtered, item)
		}
	}
	owned = filtered
	if inherit {
		if value, present := o.Get("git_previous_owned"); present {
			if items, valid := value.([]any); valid {
				for _, item := range items {
					if s, valid := item.(string); valid {
						owned = append(owned, s)
					}
				}
			}
		}
	}
	return owned, found, ok
}

// EffectiveOwned resolves the recorded task scope, including inherited entries.
func EffectiveOwned(sidecar, agent string) ([]string, bool) {
	o := read(sidecar)
	if o == nil {
		return nil, false
	}
	owned, found, ok := diffEffectiveOwned(sidecar, agent, o)
	return owned, found && ok
}

// PreviousOwned reads only the previous task's own declared scope.
func PreviousOwned(sidecar, agent, tree string) ([]string, bool) {
	return PreviousOwnedWithPointer(sidecar, agent, tree, nil)
}

// PreviousOwnedWithPointer resolves history against a previously saved pointer.
func PreviousOwnedWithPointer(sidecar, agent, tree string, prior *jsonjs.Object) ([]string, bool) {
	o := read(sidecar)
	if o == nil {
		return nil, false
	}
	previousTree, _ := stringField(o, "git_tree")
	if previousTree == "" || platform.CanonPath(previousTree, platform.Current()) != platform.CanonPath(tree, platform.Current()) {
		return nil, false
	}
	seams := commandOwnedSeams()
	if prior != nil {
		seams.pointer = func(string, string) (*jsonjs.Object, bool) { return prior, false }
	}
	owned, _, ok := diffEffectiveSectionRead(sidecar, agent, o, seams, "owned files")
	filtered := owned[:0]
	for _, item := range owned {
		if item != "\x00inherit previous brief" {
			filtered = append(filtered, item)
		}
	}
	return filtered, ok
}

// normalizedOwnedSection copies lines and replaces one section body with the
// shared Owned normalizer. diffMarkdown has already blanked fenced lines.
func normalizedOwnedSection(lines []string, start, end int) []string {
	out := append([]string(nil), lines...)
	filtered, _ := OwnedSectionLines(lines[start:end])
	copy(out[start:end], filtered)
	return out
}

// nextSectionHeading is the end of the section body that starts at from.
func nextSectionHeading(lines []string, from int) int {
	for i := from; i < len(lines); i++ {
		if _, n := diffHeading(lines[i]); n != 0 {
			return i
		}
	}
	return len(lines)
}

// InheritsPrevious recognizes the explicit bullet only inside an Owned files
// section, after the same label and emphasis normalization the lint and the
// overlap reader apply. An inherit under a negative label is not a directive.
func InheritsPrevious(text string) bool {
	level := 0
	lines := diffMarkdown(text)
	for i, line := range lines {
		if title, n := diffHeading(line); title == "brief" && n == 1 {
			lines = lines[i+1:]
			break
		}
	}
	normalized := lines
	for i := 0; i < len(lines); i++ {
		if title, n := diffHeading(lines[i]); n != 0 {
			if title == "report contract" && n == 1 {
				break
			}
			if level != 0 && n <= level {
				level = 0
			}
			if title == "owned files" {
				level = n
				normalized = normalizedOwnedSection(lines, i+1, nextSectionHeading(lines, i+1))
			}
			continue
		}
		if level != 0 && strings.TrimSpace(normalized[i]) == "- inherit: previous brief" {
			return true
		}
	}
	return false
}

func diffEffectiveSectionRead(sidecar, agent string, o *jsonjs.Object, seams ownedReadSeams, section string) ([]string, bool, bool) {
	tree, _ := stringField(o, "git_tree")
	goos := platform.Current()
	text, err := diffComposedBriefRead(sidecar, seams.brief)
	if err != nil {
		return nil, false, false
	}
	recordedRoots := seams.recordedRootNames(sidecar, o, tree)
	if owned, found := diffSectionNames(text, tree, recordedRoots, goos, section); found {
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
						if owned, found := diffSectionNames(text, tree, recordedRoots, goos, section); found {
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
		if owned, found := diffSectionNames(text, tree, recordedRoots, goos, section); found {
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
	// auditRoots marks the settle/collect audit: its root-entry exception
	// also accepts the exact root names the dispatch recorded (the snapshot
	// keys and the Git baseline at git_head), read with env. The lint and the
	// dispatch overlap reader keep the live-tree lookup.
	auditRoots bool
	env        platform.Env
}

// commandOwnedSeams is the command's walk over the ordinary scratch
// readers: unbounded, no history limits — the exact pre-inspection
// behavior.
func commandOwnedSeams() ownedReadSeams {
	return ownedReadSeams{rawLocate: core.ReadScratchReport, rawEntry: core.ReadScratchReport, brief: core.ReadScratchReport, pointer: commandTaskPointer}
}

// auditOwnedSeams is the settle/collect audit's walk: the command's ordinary
// reads plus the recorded dispatch root names, so a declared root entry the
// task deleted still counts as owned scope.
func auditOwnedSeams(env platform.Env) ownedReadSeams {
	seams := commandOwnedSeams()
	seams.auditRoots = true
	seams.env = env
	return seams
}

// recordedRootNames is the exact root-entry source the audit adds to the live
// tree: the root names of the recorded dispatch snapshot and of the Git
// baseline at the recorded head. A reader without auditRoots checks nothing
// extra; a missing snapshot or a failed Git read leaves the other source.
func (s ownedReadSeams) recordedRootNames(sidecar string, o *jsonjs.Object, tree string) map[string]bool {
	if !s.auditRoots || tree == "" {
		return nil
	}
	names := map[string]bool{}
	if snapshot, ok := snapshotFromSidecar(sidecar, o); ok {
		for _, name := range snapshot.Keys() {
			if diffRootEntryName(name) {
				names[name] = true
			}
		}
	}
	if head, _ := stringField(o, "git_head"); diffHeadRE.MatchString(head) {
		if out, ok := gitOutput(s.env, tree, "ls-tree", "--name-only", "-z", head); ok {
			for _, raw := range strings.Split(out, "\x00") {
				if name := strings.TrimSuffix(raw, "/"); diffRootEntryName(name) {
					names[name] = true
				}
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// diffRootEntryName reports whether a recorded path is an entry at the tree
// root: one name with no separator.
func diffRootEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
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

// PathEntryAbsolute reports whether the audit path entry is spelled
// absolute under goos: a leading slash (every goos), a leading backslash
// (treated as absolute the way diffRelative has always refused it), or a
// Windows drive prefix (win32 only). Relative entries keep their spelling.
func PathEntryAbsolute(entry, goos string) bool {
	if strings.HasPrefix(entry, "\\") || strings.HasPrefix(entry, "/") {
		return true
	}
	if goos == "win32" {
		return len(entry) >= 3 && entry[1] == ':' && (entry[2] == '/' || entry[2] == '\\')
	}
	return false
}

// resolvedPrefix is one side of a recorded-tree comparison after symlink
// resolution: the whole path when it exists, otherwise the largest existing
// prefix resolved with the unresolvable tail re-attached — the entry file
// may not exist yet (the worker has not created it), so the whole entry
// often cannot be EvalSymlinks'd. A path with no existing prefix keeps its
// cleaned lexical form, cleaned with the requested system's separator rules
// (path.Clean outside win32: slash semantics, case preserved, a backslash a
// name character; the host's filepath.Clean otherwise — win32 canonical
// cleaning re-normalizes separators anyway), so a table comparison on
// another platform stays purely lexical through platform.PathInside on
// every host. A drive-prefixed path is absolute by its own prefix and is
// never joined with the working directory, so a win32-style table on
// another platform stays clean. A path from the foreign family (a win32
// spelling on a POSIX host, or a POSIX spelling on a Windows host) skips
// the host resolution entirely — on a Windows host the native walk would
// resolve a Unix-rooted spelling against the host's drive (the drive root
// is an existing prefix, so /work/repo would become D:\work\repo) or join
// it to the host's cwd, and on a POSIX host a drive-prefixed spelling
// carries no slash to anchor the walk — and the shared selected-system
// canonicalization (platform.CanonPath) stands in for it.
func resolvedPrefix(p, goos string) string {
	// Foreign path family: no host EvalSymlinks, cwd join or prefix walk —
	// only the shared selected-system lexical canonicalization, so a
	// foreign spelling never acquires a native cwd or filesystem prefix
	// (DJA-661, native Windows). Darwin and linux share the POSIX family
	// and keep the native prefix/symlink resolution below; native win32
	// resolution is unchanged. The comparison forms stay lexical only;
	// spellingTail still recovers the entry's original relative case.
	if (goos == "win32") != (platform.Current() == "win32") {
		return platform.CanonPath(p, goos)
	}
	if eval, err := filepath.EvalSymlinks(p); err == nil {
		return eval
	}
	cleaned := filepath.Clean(p)
	if goos != "win32" {
		cleaned = path.Clean(p)
	}
	abs := cleaned
	if !filepath.IsAbs(cleaned) && !driveAbsolute(cleaned) {
		wd, err := os.Getwd()
		if err != nil {
			return cleaned
		}
		abs = filepath.Join(wd, cleaned)
	}
	var tail []string
	for dir := abs; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
		if eval, err := filepath.EvalSymlinks(dir); err == nil {
			for _, comp := range tail {
				eval = filepath.Join(eval, comp)
			}
			return eval
		}
	}
	return cleaned
}

// auditPathCanon normalizes one side of a recorded-tree comparison the way
// the shared comparator does (platform.CanonPath, the normalization
// dispatch.SamePath applies): on a native path family the largest existing
// prefix is resolved first (resolvedPrefix, where a plain EvalSymlinks
// gives up); on a foreign family the selected-system canonicalization
// stands in for the resolution outright. win32 then folds case, drive and
// separators and the other platforms clean. The containment itself is the
// shared platform.PathInside rule (auditWithin); this helper only produces
// the two spellings it compares, so the tree-relative form can be read off
// the same prefix.
func auditPathCanon(p, goos string) string {
	return platform.CanonPath(resolvedPrefix(p, goos), goos)
}

// auditWithin is the shared platform.PathInside containment decision for
// two auditPathCanon spellings: equal, or the child carries the parent's
// spelling plus the separator (a root parent already ends with it).
func auditWithin(child, parent, goos string) bool {
	return platform.PathInside(child, parent, goos)
}

// TreeRelative rewrites one audit path entry to the tree-relative spelling
// the owned coverage and citation checks compare, or "" when the entry
// covers nothing inside the recorded tree. A relative entry keeps today's
// diffRelative decision (cleaned slash form; ".", ".." and "../x" cover
// nothing). An absolute entry inside the tree — a file, a folder, a glob
// or the root — is the same entry as its relative spelling, and the
// comparison follows the shared platform rules (CanonPath/PathInside, the
// SamePath/PathWithin normalization): a symlink on either side, backslash
// and case under win32, and the largest existing prefix resolved (the
// entry file may not exist yet). The canonical forms decide the comparison
// only; the returned relative keeps the entry's own spelling: the entry,
// cleaned with its separators normalized to slashes, contributes its last
// n parts (n = the canonical relative's part count) when they canonicalize
// to the canonical relative, which a `..`/`.` spelling or a different-depth
// symlink prefix cannot break; only a symlink inside the suffix that
// renames a part returns the canonical relative (DJA-661 round 3). An
// absolute entry outside the tree covers nothing inside it; an entry that
// resolves to the tree root itself is ".".
func TreeRelative(entry, tree, goos string) string {
	entry = strings.TrimSpace(entry)
	if entry == "" || strings.HasPrefix(entry, "\\") || strings.Contains(entry, "://") {
		return ""
	}
	if !PathEntryAbsolute(entry, goos) {
		return diffRelative(entry)
	}
	if tree == "" {
		return ""
	}
	ec, rc := auditPathCanon(entry, goos), auditPathCanon(tree, goos)
	if ec == "" || rc == "" || !auditWithin(ec, rc, goos) {
		return ""
	}
	if ec == rc {
		return "."
	}
	if !strings.HasPrefix(ec, rc+"/") {
		return ""
	}
	rel := ec[len(rc)+1:]
	if tail := spellingTail(entry, rel, goos); tail != "" {
		rel = tail
	}
	return rel
}

// spellingTail reads the tree-relative tail off the entry's own spelling:
// the entry, with its separators normalized to slashes and path.Clean'd,
// keeps its case; the last n parts (n = the canonical relative's part
// count) are the spelling when they canonicalize to the canonical relative.
// A mismatch — a symlink inside the suffix renaming a part, or a cleaned
// entry with fewer parts than the relative — returns the canonical
// relative instead (DJA-661 round 3).
func spellingTail(entry, rel, goos string) string {
	n := strings.Count(rel, "/") + 1
	raw := entry
	if goos == "win32" {
		raw = strings.ReplaceAll(raw, `\`, "/")
	}
	parts := splitSpellingComponents(path.Clean(raw))
	if len(parts) < n {
		return ""
	}
	tail := strings.Join(parts[len(parts)-n:], "/")
	folded := tail
	if goos == "win32" {
		folded = strings.ToLower(tail)
	}
	if folded != rel {
		return ""
	}
	return tail
}

// driveAbsolute reports the win32 drive-prefix absolute form (x:/ or x:\
// after any case folding): such a path is absolute by its own prefix,
// whatever the host's notion of a leading slash is.
func driveAbsolute(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// splitSpellingComponents splits an uncleaned path on both separators and
// drops the empty pieces a leading or doubled separator leaves behind.
func splitSpellingComponents(p string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		out = append(out, part)
	}
	return out
}

// OwnedEntryIsFolder reports whether a recorded Owned entry carries the
// folder marker: a trailing slash on every system, and a trailing backslash
// on win32, where the backslash is the separator. Outside win32 the
// backslash is a name character and the entry keeps its file spelling.
// The marker makes the entry cover its children the way the
// trailing-slash form always has (DJA-661 F3).
func OwnedEntryIsFolder(entry, goos string) bool {
	if strings.HasSuffix(entry, "/") {
		return true
	}
	return goos == "win32" && strings.HasSuffix(entry, `\`)
}

// diffOwnedRootPattern is the converted form of an absolute Owned entry
// that points at the recorded tree itself: it covers the whole tree.
const diffOwnedRootPattern = "."

func diffOwned(text, tree, goos string) ([]string, bool) {
	return diffSection(text, tree, goos, "owned files")
}

// diffOwnedExtension is the final extension a backticked token may end
// with: 1 to 10 lowercase letters and digits. A capital makes the token
// prose, such as the identifier ai.openChat or the name NOTES.MD.
var diffOwnedExtension = regexp.MustCompile(`\.[a-z0-9]{1,10}$`)

// diffOwnedPlainExtension is the same extension matched case-insensitively:
// a plain (unquoted) token keeps the grammar it had before the backticked
// rule, so a plain NOTES.MD names a path while a backticked one is prose.
var diffOwnedPlainExtension = regexp.MustCompile(`(?i)\.[a-z0-9]{1,10}$`)

// diffOwnedGlobCharacters are the glob characters a token may carry: a
// pattern such as src/*.go, tool? or src/[ab].ts names a path.
const diffOwnedGlobCharacters = "/*\\[]?"

// diffOwnedItemLabelRE matches the positive section label a brief may put in
// front of an item's value, such as "Owned:" or "May edit:" with or without
// emphasis: a colon-terminated label word, then the value.
var diffOwnedItemLabelRE = regexp.MustCompile(`^[*_]{0,2}[\p{L}][\p{L}\p{N} ]{0,40}:[*_]{0,2}[ \t]+`)

// diffOwnedLabelMarkerRE strips a list marker before a section label is read.
var diffOwnedLabelMarkerRE = regexp.MustCompile(`^(?:[-*+]|[0-9]+[.)])[ \t]+`)

// diffOwnedNegativeLabels and diffOwnedPositiveLabels are the section label
// vocabulary the Owned readers recognize. They are the same words the
// dispatch lint and the dispatch overlap reader apply (ownedLabel,
// internal/dispatch): a negative label excludes its line and the lines after
// it until a positive label, a positive label opens the items that follow.
var diffOwnedNegativeLabels = []string{"proibido", "proibidos", "forbidden", "nao tocar", "do not touch", "fora do escopo", "out of scope"}
var diffOwnedPositiveLabels = []string{"pode tocar", "owned", "may edit", "arquivos proprios", "arquivos de posse"}

// OwnedLabel identifies a fixed label at the start of an Owned files line;
// remainder is the value after a positive label's colon, without the label's
// own closing emphasis ("**Owned:** x"), without a list marker of its own
// ("Owned: - x") and without a symmetric emphasis pair around it
// ("Owned: **x**"), so neither can become part of a path. A glob such as
// src/** or **/x.go has no matching pair and is kept whole. This is the one
// label reader the lint, the overlap reader and the audit all use. Names are
// compared folded and without marks, so Nao tocar and Não tocar are the same.
func OwnedLabel(line string) (negative, positive bool, remainder string) {
	line = strings.TrimSpace(line)
	line = diffOwnedLabelMarkerRE.ReplaceAllString(line, "")
	trimmed := strings.TrimLeft(line, " \t*_")
	labelEmphasis := line[:len(line)-len(trimmed)]
	line = trimmed
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return false, false, ""
	}
	name := text.JSLower(text.StripMarksNFD(strings.TrimSpace(strings.TrimRight(line[:colon], "*_ \t"))))
	for _, label := range diffOwnedNegativeLabels {
		if name == label {
			return true, false, ""
		}
	}
	for _, label := range diffOwnedPositiveLabels {
		if name == label {
			return false, true, OwnedLabelValue(line[colon+1:], strings.TrimSpace(labelEmphasis))
		}
	}
	return false, false, ""
}

// OwnedLabelValue cleans the text after a positive label's colon; see OwnedLabel.
func OwnedLabelValue(value, labelEmphasis string) string {
	if labelEmphasis != "" {
		value = strings.TrimPrefix(value, labelEmphasis)
	}
	value = strings.TrimSpace(value)
	value = strings.TrimSpace(diffOwnedLabelMarkerRE.ReplaceAllString(value, ""))
	for _, pair := range []string{"**", "__", "*", "_"} {
		if len(value) > 2*len(pair) && strings.HasPrefix(value, pair) && strings.HasSuffix(value, pair) {
			value = strings.TrimSpace(value[len(pair) : len(value)-len(pair)])
			break
		}
	}
	return value
}

// diffFenceMask reports fenced lines with the audit's fence grammar, the same
// one diffMarkdown blanks before headings and tokens are interpreted.
func diffFenceMask(lines []string) []bool {
	mask := make([]bool, len(lines))
	fence := ""
	for i, line := range lines {
		if m := diffFenceRE.FindStringSubmatch(line); m != nil {
			if fence == "" {
				fence = m[1]
			} else if m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.TrimSpace(m[2]) == "" {
				fence = ""
			}
			mask[i] = true
			continue
		}
		mask[i] = fence != ""
	}
	return mask
}

// OwnedSectionLines filters one Owned section with the audit's fence mask,
// keeping line positions. OwnedSectionLinesWithMask applies the same shared
// normalizer with a caller supplied mask, so the dispatch readers keep their
// own fence grammar without a second label parser.
func OwnedSectionLines(lines []string) ([]string, []bool) {
	mask := diffFenceMask(lines)
	return OwnedSectionLinesWithMask(lines, mask), mask
}

// OwnedSectionLinesWithMask strips each positive label and its emphasis,
// drops negative lines until the next positive label, and leaves fenced lines
// out. The rest keeps a list marker, so a bare path after the label
// ("- Owned: src/ui/app.go") is still read as an owned item.
func OwnedSectionLinesWithMask(lines []string, fenced []bool) []string {
	out := make([]string, len(lines))
	excluded := false
	for i, line := range lines {
		if fenced[i] {
			continue
		}
		negative, positive, rest := OwnedLabel(line)
		if negative {
			excluded = true
			continue
		}
		if positive {
			excluded = false
			if strings.TrimSpace(rest) != "" {
				out[i] = "- " + rest
			}
			continue
		}
		if !excluded {
			out[i] = line
		}
	}
	return out
}

// OwnedItemLabel classifies the section label at the start of an Owned files
// or Orchestrator files line, so the audit keeps the same positive and
// negative labels out of (or into) the owned set as the lint and the
// dispatch overlap reader.
func OwnedItemLabel(line string) (negative, positive bool) {
	negative, positive, _ = OwnedLabel(line)
	return negative, positive
}

// diffOwnedPlainToken trims the prose punctuation around a plain path token.
// A parenthesis at either end is only prose when the token's parentheses are
// unbalanced, so a path such as src/(setup) keeps its spelling.
func diffOwnedPlainToken(token string) string {
	name := strings.TrimRight(token, ".,;:")
	if strings.Count(name, "(") == strings.Count(name, ")") {
		return name
	}
	return strings.Trim(name, "()")
}

// ownedTokenIsPath is the one path-shape rule for a backticked Owned token: a
// path separator, a glob character, or a final extension of 1 to 10 lowercase
// letters and digits. Prose punctuation around the token is not part of the
// name, so a dotfile (.env) and a name with digits in the extension (.mp4)
// qualify while an identifier (ai.openChat, attachSelectionToChat) and an
// uppercase extension (.MD) do not.
func ownedTokenIsPath(token string) bool {
	name := diffOwnedPlainToken(token)
	return name != "" && (strings.ContainsAny(name, diffOwnedGlobCharacters) || diffOwnedExtension.MatchString(name))
}

// ownedPlainTokenIsPath is the one path-shape rule for a plain (unquoted)
// Owned token, the grammar plain tokens kept before the backticked rule: a
// separator or a final extension of 1 to 10 letters and digits, matched
// case-insensitively. Prose words never qualify.
func ownedPlainTokenIsPath(token string) bool {
	name := diffOwnedPlainToken(token)
	return name != "" && (strings.ContainsAny(name, `/*\`) || diffOwnedPlainExtension.MatchString(name))
}

// OwnedPlainPaths reports the plain (unquoted) path tokens of one Owned item
// in item order, each trimmed of prose punctuation. The audit, the lint and
// the dispatch overlap warning all go through this rule.
func OwnedPlainPaths(item string) []string {
	var out []string
	for _, token := range strings.Fields(item) {
		if ownedPlainTokenIsPath(token) {
			out = append(out, diffOwnedPlainToken(token))
		}
	}
	return out
}

// OwnedQuotedPaths filters the backticked tokens of one Owned item: a token
// counts when it is path-shaped, and an item that is only one backticked token
// keeps it even without a shape, because a bare name such as Makefile or
// README means the file at the worktree root. A token that is not path-shaped
// still counts when root carries an entry of exactly that name, or when the
// recorded root names carry it (the settled audit's dispatch snapshot and Git
// baseline); root "" and nil names (a reader without them) apply the shape
// rule and the live root only. Every Owned reader (the audit, the lint and the
// dispatch overlap warning) goes through this rule.
func OwnedQuotedPaths(item string, tokens []string, root string, recordedRoots map[string]bool) []string {
	out := make([]string, 0, len(tokens))
	only := len(tokens) == 1 && ownedQuotedAlone(item)
	for _, token := range tokens {
		if only || ownedTokenIsPath(token) || ownedRootEntryExists(root, recordedRoots, token) {
			out = append(out, token)
		}
	}
	return out
}

// OwnedItemPaths is the shared path reader for a labeled Owned item that
// carries a backticked note: backticked paths when any name one, otherwise
// the item's first whitespace-separated token when that token is path-shaped.
// Nothing after that first token adds scope. An item with no backticked
// token to read (no span, or an assignment value the caller withheld) is left
// to its caller's plain-token rule and contributes nothing here.
func OwnedItemPaths(item string, quoted []string, root string, recordedRoots map[string]bool) []string {
	if len(quoted) == 0 {
		return nil
	}
	if paths := OwnedQuotedPaths(item, quoted, root, recordedRoots); len(paths) > 0 {
		return paths
	}
	fields := strings.Fields(item)
	if len(fields) == 0 {
		return nil
	}
	first := fields[0]
	// A first token that opens an aside (a parenthesis, bracket or quote)
	// starts prose, not a declaration, even when a path follows the mark.
	if open, _ := utf8.DecodeRuneInString(first); strings.ContainsRune("([{\"'“‘«", open) || !ownedPlainTokenIsPath(first) {
		return nil
	}
	return []string{diffOwnedPlainToken(first)}
}

// ownedRootEntryExists reports whether the reader's root carries an entry of
// exactly this name: the live tree it resolved, plus the exact root names it
// recorded at dispatch (recordedRoots). root is the declared worktree a reader
// already resolved, never the caller's own directory; a token with a glob
// character or a separator is a pattern and not a root name, so recordedRoots
// never answers for a nested path.
func ownedRootEntryExists(root string, recordedRoots map[string]bool, name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, diffOwnedGlobCharacters) {
		return false
	}
	if recordedRoots[name] {
		return true
	}
	if root == "" {
		return false
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return false
	}
	defer r.Close()
	if _, err := r.Lstat(name); err != nil {
		return false
	}
	return true
}

// ownedQuotedAlone reports whether the item text outside its backticked spans
// is empty or only sentence punctuation, once a positive section label
// ("Owned:") is stripped: the item names nothing but one token.
func ownedQuotedAlone(item string) bool {
	item = diffOwnedItemLabelRE.ReplaceAllString(strings.TrimSpace(item), "")
	return strings.Trim(diffTokenRE.ReplaceAllString(item, ""), " \t.,;:") == ""
}

// diffOwnedNoFilesWords are the empty-scope words the dispatch lint already
// recognizes for an explicit empty Owned declaration. The lint must not warn
// about one of those items.
var diffOwnedNoFilesWords = []string{"none", "nenhum", "nenhuma", "nada", "nothing", "n/a"}

func diffOwnedDeclaresNoFiles(item string) bool {
	item = strings.TrimSpace(item)
	if item != "" && strings.ContainsRune("-*+", rune(item[0])) {
		item = strings.TrimSpace(item[1:])
	}
	if item == "—" || item == "-" {
		return true
	}
	lower := strings.ToLower(item)
	for _, word := range diffOwnedNoFilesWords {
		if !strings.HasPrefix(lower, word) {
			continue
		}
		rest := lower[len(word):]
		if rest == "" {
			return true
		}
		if r, _ := utf8.DecodeRuneInString(rest); !unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// ownedItemLintTokens returns the path tokens of one Owned item with the
// same rules the audit parser uses, root being the worktree whose root the
// reader checks ("" applies the shape rule only). An exempt item (empty
// scope, inherit, new tests, or a continuation line) contributes no tokens
// and no warning.
func ownedItemLintTokens(line, root string) ([]string, bool) {
	if strings.TrimSpace(line) == "- inherit: previous brief" || !diffBulletRE.MatchString(line) {
		return nil, true
	}
	item := strings.TrimSpace(diffBulletRE.ReplaceAllString(line, ""))
	if item == "" || diffNewTestsItemRE.MatchString(item) || diffOwnedDeclaresNoFiles(item) {
		return nil, true
	}
	if matches := diffTokenRE.FindAllStringSubmatch(line, -1); len(matches) > 0 {
		// Backticked tokens count exactly as written when they name a path.
		tokens := make([]string, 0, len(matches))
		for _, m := range matches {
			tokens = append(tokens, m[1])
		}
		return OwnedItemPaths(item, tokens, root, nil), false
	}
	return OwnedPlainPaths(item), false
}

// OwnedSectionDeclaresScope reports whether a normalized Owned section
// declares any scope at all: a path, a path pattern, the new-tests pattern,
// an explicit no-files declaration, or an inherit directive. Prose lines name
// paths the same way the audit reads them; a section with nothing but prose
// and no path declares none.
func OwnedSectionDeclaresScope(lines []string, root string) bool {
	for _, line := range lines {
		item := strings.TrimSpace(diffBulletRE.ReplaceAllString(line, ""))
		if item == "" {
			continue
		}
		if diffOwnedDeclaresNoFiles(item) || diffNewTestsItemRE.MatchString(item) || strings.TrimSpace(line) == "- inherit: previous brief" {
			return true
		}
		if matches := diffTokenRE.FindAllStringSubmatch(line, -1); len(matches) > 0 {
			tokens := make([]string, 0, len(matches))
			for _, m := range matches {
				tokens = append(tokens, m[1])
			}
			if len(OwnedItemPaths(item, tokens, root, nil)) > 0 {
				return true
			}
			continue
		}
		if len(OwnedPlainPaths(item)) > 0 {
			return true
		}
	}
	return false
}

// OwnedItemLintMessages returns the short warning for an Owned section line
// that names no path. The caller prefixes the brief path and item label.
func OwnedItemLintMessages(lines []string, root string) []string {
	var out []string
	for _, line := range lines {
		tokens, skip := ownedItemLintTokens(line, root)
		if skip {
			continue
		}
		if len(tokens) == 0 {
			item := strings.TrimSpace(diffBulletRE.ReplaceAllString(line, ""))
			out = append(out, fmt.Sprintf("'%s' names no path", diffOwnedCut(item, 60)))
		}
	}
	return out
}

// OwnedBareNames returns the distinct bare names in the Owned items. A bare
// name has no path separator and no glob character: it means the file at the
// worktree root. The caller checks the root and decides whether to warn.
func OwnedBareNames(lines []string, root string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range lines {
		tokens, skip := ownedItemLintTokens(line, root)
		if skip {
			continue
		}
		for _, token := range tokens {
			if token == "" || strings.ContainsAny(token, `/\\*?[]`) || seen[token] {
				continue
			}
			seen[token] = true
			out = append(out, token)
		}
	}
	return out
}

func diffOwnedCut(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func diffSection(text, tree, goos, section string) ([]string, bool) {
	return diffSectionNames(text, tree, nil, goos, section)
}

// diffSectionNames is diffSection with the exact root names a reader recorded
// at dispatch (nil keeps the live-tree lookup only). The settled audit passes
// the snapshot and Git-baseline names, so a tracked root entry the task
// deleted still counts as declared scope.
func diffSectionNames(text, tree string, recordedRoots map[string]bool, goos, section string) ([]string, bool) {
	lines := diffMarkdown(text)
	// A composed role may itself contain headings. Only its brief is scope.
	for i, line := range lines {
		if title, level := diffHeading(line); title == "brief" && level == 1 {
			lines = lines[i+1:]
			break
		}
	}
	items, found, ownedLevel := []string{}, false, 0
	// Labeled sections read the shared normalized stream: labels and their
	// emphasis are stripped and negative lines are dropped before the audit
	// recognizes a directive or tests a path token.
	labeled := ownedItemRules(section)
	normalized := lines
	for i := 0; i < len(lines); i++ {
		if title, level := diffHeading(lines[i]); level != 0 {
			if title == "report contract" && level == 1 {
				break
			}
			if ownedLevel > 0 && level <= ownedLevel {
				ownedLevel = 0
			}
			if title == section {
				found, ownedLevel = true, level
				if labeled {
					normalized = normalizedOwnedSection(lines, i+1, nextSectionHeading(lines, i+1))
				}
			}
			continue
		}
		if ownedLevel == 0 {
			continue
		}
		line := normalized[i]
		if section == "owned files" && strings.TrimSpace(line) == "- inherit: previous brief" {
			items = append(items, "\x00inherit previous brief")
			continue
		}
		push := func(raw string) {
			value := TreeRelative(raw, tree, goos)
			if value == "" {
				return
			}
			if value != diffOwnedRootPattern && OwnedEntryIsFolder(raw, goos) {
				value += "/"
			}
			items = append(items, value)
		}
		item := strings.TrimSpace(diffBulletRE.ReplaceAllString(line, ""))
		matches := diffTokenRE.FindAllStringSubmatch(line, -1)
		if len(matches) > 0 {
			tokens := make([]string, 0, len(matches))
			for _, m := range matches {
				tokens = append(tokens, m[1])
			}
			if labeled {
				tokens = OwnedItemPaths(item, tokens, tree, recordedRoots)
			}
			for _, token := range tokens {
				push(token)
			}
		} else if item != "" {
			if diffNewTestsItemRE.MatchString(item) {
				// The prose item names a kind of file, not a path: it maps
				// to the new-tests pattern, not the first-word pattern a
				// path item yields.
				items = append(items, diffOwnedNewTests)
			} else if !labeled {
				push(strings.Fields(item)[0])
			} else {
				// Both Owned files and Orchestrator files count every
				// path-shaped plain token; prose words alone own nothing.
				for _, token := range OwnedPlainPaths(item) {
					push(token)
				}
			}
		}
	}
	return items, found
}

// ownedItemRules reports whether a section shares the Owned item rules: Owned
// files and Orchestrator files do, every other section keeps its own.
func ownedItemRules(section string) bool {
	return section == "owned files" || section == "orchestrator files"
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
	if pattern == diffOwnedRootPattern {
		// An absolute Owned entry that points at the recorded tree itself:
		// it covers the whole tree.
		return file != ""
	}
	directory := strings.HasSuffix(pattern, "/")
	parts, names := strings.Split(strings.TrimSuffix(pattern, "/"), "/"), strings.Split(file, "/")
	type state struct{ part, name int }
	memo := map[state]bool{}
	visited := map[state]bool{}
	var match func(int, int) bool
	match = func(i, j int) bool {
		key := state{i, j}
		if visited[key] {
			return memo[key]
		}
		visited[key] = true
		result := false
		switch {
		case i == len(parts):
			result = j == len(names) && !directory || j < len(names) && directory
		case parts[i] == "**":
			result = match(i+1, j) || j < len(names) && match(i, j+1)
		case j < len(names):
			matched, err := filepath.Match(parts[i], names[j])
			result = err == nil && matched && match(i+1, j+1)
		}
		memo[key] = result
		return result
	}
	return match(0, 0)
}

// diffFilesChangedTitles are the report section titles that list the files
// the worker claims to have changed, case-insensitive at any heading level
// (DJA-666): the cited check reads only inside them, so a file the report
// merely reads or cites elsewhere is never "cited unchanged". A section
// ends at the next heading of the same or a lower level.
var diffFilesChangedTitles = map[string]bool{
	"files changed":        true,
	"arquivos alterados":   true,
	"arquivos modificados": true,
	"arquivos mudados":     true,
}

func diffCited(tree, report string, changed map[string]bool) []string {
	root, err := os.OpenRoot(tree)
	if err != nil {
		return nil
	}
	defer root.Close()
	seen := map[string]bool{}
	inChanged, changedLevel := false, 0
	forbiddenLevel := 0
	for _, line := range diffMarkdown(report) {
		title, level := diffHeading(line)
		if level != 0 {
			// The section closes on any heading of the same or a lower
			// level (the Files changed title re-opens it right after). The
			// close never touches the forbidden state: forbidden has its
			// own scope (DJA-661 round 3).
			if inChanged && level <= changedLevel {
				inChanged = false
			}
			// Forbidden ends only on a heading of the same or a lower level
			// than the one that opened it — like any other heading, a Files
			// changed title at that level ends it; a deeper one never does,
			// and no section opening or closing ever does.
			if forbiddenLevel > 0 && level <= forbiddenLevel {
				forbiddenLevel = 0
			}
			if diffFilesChangedTitles[title] {
				inChanged, changedLevel = true, level
			} else if forbiddenLevel == 0 && (title == "forbidden" || title == "forbidden files") {
				forbiddenLevel = level
			}
			continue
		}
		if !inChanged || forbiddenLevel > 0 {
			continue
		}
		for _, m := range diffTokenRE.FindAllStringSubmatch(line, -1) {
			file := TreeRelative(m[1], tree, platform.Current())
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
