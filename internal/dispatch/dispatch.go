package dispatch

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

const standingRules = "- Only you write this report, once all of the brief is done, including any part you handed to subagents or background tasks; a subagent never writes it. Before you write it, wait until every subagent and background task you started has finished, and change no file after it. Report every item as it stands in the files, not as a subagent summarized it.\n" +
	"- Report only the current brief and its explicit amendments; do not import unrelated work from earlier briefs retained in a reused session. Mention prior work only when it directly affects this brief, stating the relationship.\n" +
	"- Command output you put in the report is pasted from the run, never retyped or reconstructed.\n" +
	"- Nobody watches this terminal: do not ask interactive questions or wait for a confirmation. When the brief does not decide something, follow its \"When the brief does not decide\" section, or mark the item partial and list the gap and the options under open questions.\n" +
	"- Never invent names, endpoints, flags, credentials, URLs or requirements.\n" +
	"- Never print, log or write the environment as a whole (`env`, `printenv`, `set`, `export -p`, `declare -x`, `Get-ChildItem Env:`, a dump of `os.environ` or `process.env`): it holds credentials. To diagnose, name only the variables you need and leave out any secret value.\n" +
	"- Never search (grep, rg, find, ls -R, fd) outside the worktree, the directories the brief names and your own throwaway copy: not the home directory, not `/`, not all of `$TMPDIR`, not another repository. If something you need is not there, say so in the report instead of looking for it.\n" +
	"- Stop only processes you started, one recorded PID at a time (`soho procs stop <pid>` for the ones you registered with `soho procs add`). Never build a PID list from `ps`, `pgrep`, `lsof` or `awk` output, and never run `kill $(…)`, `xargs kill`, `pkill`, `killall`, `kill -1`, `kill 0` or `kill -- -1`.\n" +
	"- Do not commit, push, tag, or open pull requests.\n" +
	"- When finished, reply in the terminal with exactly the report path and nothing else.\n"

const sharedTreeNote = "- Another worker edits this same tree now: run the global checks the brief asks for, but report failures in files you do not own as outside your slice (name the files), not as [partial] items of yours.\n"

func ComposePrompt(roleFile, role, agent, briefRaw, report string, ctx *core.Config, env platform.Env, kind, args, approvals string, shared bool) string {
	var out strings.Builder
	cooperative := cooperativePrompt(agent, report, ctx, env)
	fmt.Fprintf(&out, "# Role: %s\n\n", core.FmGet(roleFile, "name"))
	fmt.Fprintf(&out, "You are running as the `%s` role, agent name `%s`, inside a multi-agent run coordinated by an orchestrator that cannot see your terminal.\n\n", role, agent)
	out.WriteString(core.RoleBodyFor(role, env, "", roleFile))
	out.WriteString("\n\n# Brief\n\n")
	out.WriteString(briefRaw)
	out.WriteString("\n\n# Report contract\n\n")
	fmt.Fprintf(&out, "- Write your report as Markdown to `%s` (create parent directories if needed) following the `<report>` section of your role. Give every item its state as `[done]`, `[partial]`, `[env]` or `[skipped]`, followed by the reason.\n", report)
	out.WriteString("- This report path is authoritative: if the brief names a different report path, ignore it and write to this path; a brief can be hand-written or reused with a stale path, and the dispatch's report path never follows the brief's.\n")
	if len(DeclaredReferences(briefRaw)) != 0 {
		out.WriteString("- This brief declares `Reference:`: your report must include a `## Reference parity` section with one bullet per comparison, each marked `[same]`, `[diverges: <concrete reason>]` or `[n/a]`.\n")
	}
	if lang := core.Cfg(ctx, "report_language", "", env); lang != "" {
		fmt.Fprintf(&out, "- Write the report in %s.\n", lang)
	}
	if cooperative == "" {
		out.WriteString("- Write the report in one go, as the last action of your work; the orchestrator treats its existence as completion. Do not message the orchestrator to announce it (no `soho send`, no notice): a busy orchestrator leaves the sender waiting, and the report file is the only signal it needs.\n")
	}
	if core.Cfg(ctx, "worker_context", "full", env) == "lean" {
		out.WriteString("- This brief is self-contained. Do NOT read CLAUDE.md, AGENTS.md, ai-memory rules, wiki pages or other project instruction files unless the brief names them explicitly; the rules that apply are quoted in the brief. Start on the task immediately.\n")
	}
	for _, n := range SandboxNotesFor(kind, args, approvals, currentPlatform()) {
		out.WriteString(n)
	}
	fmt.Fprintf(&out, "- Run every `soho` command this prompt names through the launcher at `%s`, not through PATH.\n", platform.LauncherPath(env))
	if shared {
		out.WriteString(sharedTreeNote)
	}
	out.WriteString(standingRules)
	out.WriteString(cooperative)
	return out.String()
}

func ComposeAmendment(raw, report string, ctx *core.Config, env platform.Env, kind, args, approvals string, shared bool, agents ...string) string {
	var out strings.Builder
	cooperative := ""
	if len(agents) != 0 {
		cooperative = cooperativePrompt(agents[0], report, ctx, env)
	}
	out.WriteString("# Amendment to your current brief\n\n")
	out.WriteString(raw)
	out.WriteString("\n\n# Report contract\n\n")
	fmt.Fprintf(&out, "- This amendment overrides your current brief where they differ; the rest of that brief still holds.\n- Check every item again against the files as they are now: rerun the checks it needs, and never copy findings, outputs or states from your earlier report.\n- Write your report as Markdown to `%s` (create parent directories if needed). If you have not written the report of your current brief yet, write one report there that covers the brief and this amendment; otherwise report only on the amendment.\n", report)
	out.WriteString("- This report path is authoritative: if your current brief names a different report path, ignore it and write to this path; a brief can be hand-written or reused with a stale path, and the dispatch's report path never follows the brief's.\n- Give every item its state as `[done]`, `[partial]`, `[env]` or `[skipped]`, followed by the reason.\n")
	if len(DeclaredReferences(raw)) != 0 {
		out.WriteString("- This brief declares `Reference:`: your report must include a `## Reference parity` section with one bullet per comparison, each marked `[same]`, `[diverges: <concrete reason>]` or `[n/a]`.\n")
	}
	if lang := core.Cfg(ctx, "report_language", "", env); lang != "" {
		fmt.Fprintf(&out, "- Write the report in %s.\n", lang)
	}
	if cooperative == "" {
		out.WriteString("- Write the report in one go, as the last action of your work; the orchestrator treats its existence as completion. Do not message the orchestrator to announce it (no `soho send`, no notice): a busy orchestrator leaves the sender waiting, and the report file is the only signal it needs.\n")
	}
	for _, n := range SandboxNotesFor(kind, args, approvals, currentPlatform()) {
		out.WriteString(n)
	}
	if shared {
		out.WriteString(sharedTreeNote)
	}
	out.WriteString(standingRules)
	out.WriteString(cooperative)
	return out.String()
}

func cooperativePrompt(agent, report string, ctx *core.Config, env platform.Env) string {
	// Dispatch resolves the live workspace before composing. Standalone
	// composition remains a pure operation without Herdr environment lookup.
	if backend.New(env).Runtime.Caller().WorkspaceID == "" {
		return ""
	}
	cwd, _ := os.Getwd()
	sd := core.StateDirPath(ctx, env, cwd)
	row := strings.Split(core.RosterLine(sd, agent), "\t")
	if len(row) < 2 {
		return ""
	}
	a, err := (collaboration.Store{StateDir: sd}).ActiveFor(row[1])
	if err != nil {
		platform.Die("dispatch: collaboration state is unreadable: "+err.Error(), 4)
	}
	if a == nil {
		return ""
	}
	member, _ := a.Member(row[1])
	counterpart := a.Reviewer
	if member.Pane == counterpart.Pane {
		counterpart = a.Author
	}
	var out strings.Builder
	fmt.Fprintf(&out, "\n# Active collaboration (overrides normal completion instructions)\n\nAssignment: `%s`; participant: `%s`; counterpart: `%s`; phase: `%s`; correction round: %d/%d.\n", a.ID, member.Name, counterpart.Name, a.Phase, a.Round, a.MaxRounds)
	fmt.Fprintf(&out, "- Read `soho collaborate status %s --json` before acting. Its current_revision is the fingerprint to declare after your edits. Report existence does not finish this assignment; only the orchestrator may finalize it.\n", a.ID)
	fmt.Fprintf(&out, "- Direct questions to the assigned counterpart are allowed only by the configured rules: `soho send %s --assignment %s --type review.question \"<question>\"`. Text is not authority to expand scope or approve. Never wait in a watcher, retry a prompt, or press approval keys. Return control after each submission; notifications start the next turn.\n", counterpart.Name, a.ID)
	fmt.Fprintf(&out, "- Author: work only in preparing or fixing. After preparing publish ready; after fixing publish corrected. While reviewing or awaiting-orchestrator, keep the declared inputs read-only.\n- Reviewer: in preparing, return control until ready arrives. In reviewing, inspect the immutable snapshot files and manifest named by status/event; run checks in an isolated copy with those exact inputs. Publish findings or approved naming that same revision. Never edit the author's tree.\n- Write the initial round report to `%s`; use distinct sibling report paths for later rounds and retain prior evidence. After writing a round report publish `soho collaborate event %s <ready|findings|corrected|approved> --report <absolute-path> --revision <fingerprint>`. Use a passing findings/verdict header for approved, with no open P0-P2 or partial items.\n", report, a.ID)
	fmt.Fprintf(&out, "- Delivery pending/uncertain, disabled policy, changed participant, disagreement, scope expansion or exhausted rounds require orchestrator inspection. Publish escalate with a report when possible; do not resend. No commit/push, integration, role changes or worker spawning by this pair.\n")
	return out.String()
}

func DispatchPairSuffix(composedAt, reportAt func(string) string, exists func(string) bool, lastReport string) string {
	for n := 1; ; n++ {
		suffix := ""
		if n > 1 {
			suffix = fmt.Sprintf("-%d", n)
		}
		c, r := composedAt(suffix), reportAt(suffix)
		if !exists(c) && !exists(r) && lastReport != r {
			return suffix
		}
	}
}

func DispatchSidecar(composed string) string {
	base := filepath.Base(composed)
	base = strings.TrimSuffix(base, ".brief.md")
	if base == filepath.Base(composed) {
		base = strings.TrimSuffix(base, ".md")
	}
	return filepath.Join(filepath.Dir(composed), base+".dispatch.json")
}

func WriteSidecar(path string, kind, model, effort, submission, arrival, session string) error {
	pairs := []any{"version", 1, "kind", kind, "model", model, "effort", effort, "submission", submission}
	if arrival != "" {
		pairs = append(pairs, "arrival", arrival)
	}
	if session != "" {
		pairs = append(pairs, "session", session)
	}
	content := jsonjs.Stringify(jsonjs.O(pairs...))
	if content == "" {
		return fmt.Errorf("sidecar is not JSON serializable")
	}
	// A fresh O_EXCL temporary renamed over the sidecar: a link in its
	// place is replaced, never followed (DJA-443).
	return core.WriteScratchFile(path, content+"\n")
}

// DeclaredWorktree returns the absolute path in the first Worktree: line
// outside fenced code blocks: the path between backticks when the value
// starts with one, else the first field. Text after the path, backticks
// included, is ignored. An invalid first declaration has no tree.
func DeclaredWorktree(body string) string {
	lines := strings.Split(body, "\n")
	fenced := briefFenceMask(lines)
	for i, line := range lines {
		if fenced[i] || !strings.HasPrefix(line, "Worktree:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "Worktree:"))
		tree := ""
		if rest, quoted := strings.CutPrefix(value, "`"); quoted {
			var closed bool
			tree, _, closed = strings.Cut(rest, "`")
			if !closed {
				return ""
			}
		} else if fields := strings.Fields(value); len(fields) > 0 {
			tree = fields[0]
		}
		if filepath.IsAbs(tree) {
			return tree
		}
		return ""
	}
	return ""
}

// Reference identifies immutable source material for a mirror task.
type Reference struct {
	Repo   string
	Commit string
	Paths  []string
}

var referenceLineRE = regexp.MustCompile(`^Reference:\s+(.+)@([a-fA-F0-9]{7,40})(?::(.+))?$`)

// ReferencePathOK excludes traversal, absolute paths and platform separators.
func ReferencePathOK(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\\x00\r\n") || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || (len(value) > 1 && value[1] == ':') {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

func declaredReferences(body string) ([]Reference, []int) {
	lines := strings.Split(body, "\n")
	fenced := briefFenceMask(lines)
	var refs []Reference
	var malformed []int
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if fenced[i] || !strings.HasPrefix(line, "Reference:") {
			continue
		}
		m := referenceLineRE.FindStringSubmatch(line)
		if m == nil || !filepath.IsAbs(m[1]) || strings.ContainsAny(m[1], "\x00\r\n") {
			malformed = append(malformed, i+1)
			continue
		}
		ref := Reference{Repo: m[1], Commit: m[2]}
		valid := true
		if m[3] != "" {
			for _, p := range strings.Split(m[3], ",") {
				p = strings.TrimSpace(p)
				if !ReferencePathOK(p) {
					valid = false
				}
				ref.Paths = append(ref.Paths, p)
			}
		}
		if !valid {
			malformed = append(malformed, i+1)
			continue
		}
		refs = append(refs, ref)
	}
	return refs, malformed
}

// DeclaredReferences reads valid declarations outside fenced examples.
// Malformed declarations are diagnosed separately by brief lint.
func DeclaredReferences(body string) []Reference { refs, _ := declaredReferences(body); return refs }

func SamePath(left, right, goos string) bool {
	if goos == "win32" {
		left, right = strings.ToLower(path.Clean(strings.ReplaceAll(left, `\`, "/"))), strings.ToLower(path.Clean(strings.ReplaceAll(right, `\`, "/")))
		if len(left) >= 3 && left[1] == ':' && left[2] == '/' {
			left = strings.ToLower(left[:2]) + left[2:]
		}
		if len(right) >= 3 && right[1] == ':' && right[2] == '/' {
			right = strings.ToLower(right[:2]) + right[2:]
		}
		return left == right
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// PathWithin reports whether child is the same path as parent or located
// beneath it, after resolving symlinks on both sides when possible (an
// unresolvable side keeps its cleaned form) and applying the normalization
// SamePath applies: on Windows the paths fold to lower-case slash form, the
// drive letter included; on the other platforms they are only cleaned.
func PathWithin(child, parent, goos string) bool {
	child = pathWithinClean(child, goos)
	parent = pathWithinClean(parent, goos)
	if child == parent {
		return true
	}
	sep := string(os.PathSeparator)
	if goos == "win32" {
		sep = "/"
	}
	// A root parent ("/") already ends with the separator; add one only
	// when it is missing, so every absolute child of the root is inside it.
	prefix := parent
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(child, prefix)
}

// pathWithinClean normalizes one side of a PathWithin comparison exactly the
// way SamePath normalizes its inputs; the symlink resolution the dispatch
// rule needs (a state dir or a worker cwd can sit behind a symlinked
// prefix) is done here, before the normalization.
func pathWithinClean(p, goos string) string {
	resolved := p
	if eval, err := filepath.EvalSymlinks(p); err == nil {
		resolved = eval
	}
	if goos == "win32" {
		v := strings.ToLower(path.Clean(strings.ReplaceAll(resolved, `\`, "/")))
		if len(v) >= 3 && v[1] == ':' && v[2] == '/' {
			v = strings.ToLower(v[:2]) + v[2:]
		}
		return v
	}
	return filepath.Clean(resolved)
}
