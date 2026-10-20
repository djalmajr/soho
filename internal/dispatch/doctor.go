package dispatch

import (
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

const sandboxGitNote = "- Your sandbox cannot write under .git: do not run git mv, git checkout, git add or git commit. Describe renames and restores in the report; the orchestrator runs them.\n"
const sandboxNetNote = "- Your sandbox has no network, local ports included: tests that start a local server fail with \"Operation not permitted\". Mark them [partial] and say so; the orchestrator runs them. Still write the integration tests the brief asks for, even if you cannot run them here; do not replace them with unit tests of helpers, and add a test seam (an injectable value) when the code depends on something fixed, such as the build type.\n"

// Issue 60: full approvals do not guarantee that a sandboxed Windows
// worker can execute Node/Bun. The note says the required runtimes may be
// missing or denied, asks for at most one minimal version probe per
// required runtime in the actual worker sandbox (only when the brief opts
// in with a `## Runtime capability probe` section naming the required
// commands), records the exact command/exit/result, forbids repeated
// identical denied attempts, routes the blocked gate to an
// orchestrator-produced log of the same revision, keeps the proof
// [partial] until resolved, and requires external evidence to be checked
// for command/source/revision before it may resolve the item.
const sandboxWinRuntimeNote = "- Your sandbox may not have the runtimes your brief requires (node, bun or another) or may deny them even with full approvals, so a check on the host PATH is not proof about this sandbox. If your brief carries a `## Runtime capability probe` section naming the required commands, run at most one minimal version probe per required runtime in this worker sandbox and record the exact command, exit code and result; never repeat an identical denied probe. A denied or missing runtime keeps its item [partial]; route the blocked gate to the orchestrator for an orchestrator-produced log of the same revision; check any external evidence for the same command, source and revision before letting it resolve the item, and it must never silently turn an unresolved [partial] into a pass.\n"

// currentPlatform is the platform seam of the composed prompts: the
// runtime boundary (platform.Current, win32 on Windows) the composition
// derives the worker host from, never a guessed worker kind or a host PATH
// check. Tests inject another platform for portable coverage.
var currentPlatform = platform.Current

// SandboxNotesFor returns the Codex sandbox constraints for the worker's
// opening args on the given host platform: the .git note, the network
// note when the network is not released, and, on win32 only, the runtime
// note (issue 60). Unsandboxed approvals, danger-full-access and the bypass
// flag lift every note.
func SandboxNotesFor(kind, agentArgs, approvals, goos string) []string {
	if kind != "codex" || approvals == "unsandboxed" {
		return nil
	}
	args := strings.Fields(agentArgs)
	full, net := false, false
	for _, a := range args {
		if a == "danger-full-access" || a == "--dangerously-bypass-approvals-and-sandbox" {
			full = true
		}
		if strings.HasSuffix(a, "network_access=true") {
			net = true
		}
	}
	var notes []string
	if !full {
		notes = append(notes, sandboxGitNote)
		if !net {
			notes = append(notes, sandboxNetNote)
		}
		if goos == "win32" {
			notes = append(notes, sandboxWinRuntimeNote)
		}
	}
	return notes
}

// SandboxNotes returns the platform-independent Codex sandbox constraints
// for the worker's opening args: the .git and network notes only. The
// doctor's network-decision consumer counts these notes (more than one
// means the network stays off), so it must not see the Windows note.
func SandboxNotes(kind, agentArgs string, approvals ...string) []string {
	mode := ""
	if len(approvals) > 0 {
		mode = approvals[0]
	}
	return SandboxNotesFor(kind, agentArgs, mode, "")
}
