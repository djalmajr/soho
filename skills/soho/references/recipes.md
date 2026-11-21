# Brief recipes for recurring task kinds

These are starting shapes for the orchestrator, not a second brief contract. Read [templates/brief.md](../templates/brief.md) for the full contract; each skeleton below names the sections that change for one task kind and the evidence that belongs in them. Fill the examples with the task's own names, paths and commands, then run `soho lint <file> --role <role>` before dispatch.

## Bug fix

- Save the reported input, the command and the observed output before anyone edits code. The report keeps that first output as the baseline.
- Use the same input for the fix proof. A different or softened input does not show that the reported failure went away.
- Name the competing causes you considered. Each one gets a probe and an observed result; an unexplored cause stays open instead of being declared impossible.
- Bound the blast radius: list the files the fix may touch and the behavior that must stay unchanged.
- Keep local fixture proof apart from anything that needs an external service or a machine the worker cannot run.

```markdown
# Brief — fix the reported parser failure

Role: implementer · Agent: <agent-name> · Run: <run-id> · Report language: en
Worktree: /work/example

## Goal

Make the saved failing input pass without changing behavior for other inputs.

## Decisions already made

- The proof uses the original failing input, not a similar case.
- No root cause is assumed; each competing cause needs a probe and an observed result.

## Expected result

- The saved input passes the same command, and the report shows the output from before and after the edit.

## Acceptance criteria

1. The saved input fails before the edit — proved by `<failing command>` → captured failure output.
2. The same input passes after the edit — proved by `<same command>` → captured passing output.
3. Each competing cause is ruled out — proved by `<probe>` → observed result.

## Sources (local paths, read before editing)

- Failure report with the exact input and observed output
- Code path and existing tests named by the report

## Owned files

- internal/example/parser.go — the fix
- internal/example/parser_test.go — regression test for the saved input

## Forbidden

- No commit or push; the orchestrator owns git.

## Checks you may run

- The project's focused test command for the owned files.

## Report

Per-item marks, with the before and after output pasted from the two runs.
```

## Refactor

- Write the characterization test before any structural edit. It records the behavior the current code actually has, including the odd cases you are not trying to change.
- Run that test on the untouched code; right after it record the test file's SHA-256 (`shasum -a 256 <file>` on macOS, `sha256sum <file>` on Linux) in the report. That output is the baseline for equivalence.
- After the refactor record the digest again; it must equal the baseline and the test must pass. A changed test hides a behavior change; that is a new decision, not part of this slice. No `git diff` of an untracked or staged file proves this.
- Keep structure, names and files that may move inside Owned files, and name the consumer checks that catch a missed call site.
- A larger move can add a separate equivalence probe, but it never replaces the original characterization test.

```markdown
# Brief — restructure the report formatter

Role: implementer · Agent: <agent-name> · Run: <run-id> · Report language: en
Worktree: /work/example

## Goal

Change the internal structure of the formatter while keeping its observable behavior identical.

## Decisions already made

- The characterization test is written and green before the first structural edit; its SHA-256 is recorded immediately after the baseline run.
- That test file's SHA-256 is unchanged after the refactor; a new behavior needs a new decision, not an edit to it.

## Expected result

- The test passes before and after with equal recorded digests, and the focused consumer checks stay green.

## Acceptance criteria

1. The test passes on the untouched code and its digest is recorded right after that run — proved by `<focused test> && <hash command> <file>` → captured passing output and digest.
2. The same file digest is unchanged after the refactor — proved by `<hash command> <file>` → digest equal to the baseline.
3. The same test passes on the refactored code — proved by `<focused test>` → captured passing output.

## Sources (local paths, read before editing)

- Current implementation and its characterization test
- Consumer files that call the module being restructured

## Owned files

- internal/example/formatter.go — the structural change
- internal/example/formatter_characterization_test.go — the characterization test

## Forbidden

- No commit or push; the orchestrator owns git.

## Checks you may run

- The project's focused test command for the owned files and their consumers.

## Report

Per-item marks, with the two test outputs and both digests pasted from the runs.
```

## Phased delivery

- Write the phase order once, outside the worker briefs. For each phase, name the files it owns, the earlier phase it depends on and the proof that releases the next phase.
- The orchestrator commits each accepted phase on the work branch before dispatching the next; the next phase's brief names that commit on its `Baseline:` line. Dispatch one phase at a time with only that phase's Owned files and acceptance criteria.
- The dependency is observable: a later phase starts only after the earlier phase's report and proof exist and any review findings on it are closed. Phase 2's report carries its own focused output and cites phase 1's accepted report; phase 3's gate output belongs to phase 3's report.
- A phase's delta is `git diff --name-only --no-renames <baseline>` (full paths, both sides of a move) plus each untracked file from the `??` lines of `git status --short --untracked-files=all` (plain status collapses a new directory into one entry); both must match its Owned files. Name shared inputs, interfaces and generated files as delivered ready or owned by exactly one phase; integration gates, review and git stay with the orchestrator across all phases.

```markdown
# Brief — phase 2 of the settings migration

Role: implementer · Agent: <agent-name> · Run: <run-id> · Report language: en
Worktree: /work/example
Baseline: <accepted phase 1 commit>

## Goal

Land phase 2 after phase 1 has settled, with no edit to files owned by another phase.

## Decisions already made

- Phase order is phase 1 → phase 2 → phase 3; this brief covers phase 2 only. The orchestrator committed the accepted phase 1 on the work branch; this Baseline names that commit.
- Phase 2 depends on phase 1's interface being present; phase 3 starts only after this phase's report and proof.
- Shared inputs named below are delivered ready, not edited here.

## Expected result

- Phase 2 files exist, its focused proof passes, and the delta against Baseline lists only phase 2 files.

## Acceptance criteria

1. Phase 1's interface is present — proved by `<command>` → observed output.
2. Phase 2 behavior passes its focused check — proved by `<command>` → observed output.
3. The phase delta matches Owned files — proved by `git diff --name-only --no-renames <baseline>` and the `??` lines of `git status --short --untracked-files=all` → only the listed paths in both outputs.

## Sources (local paths, read before editing)

- Phase 1 report and the interface it delivered
- Phase plan naming the files and the proof for phase 2 and phase 3

## Owned files

- internal/example/settings/phase2.go — this phase's implementation
- internal/example/settings/phase2_test.go — this phase's proof

## Forbidden

- No edits to phase 1 or phase 3 files, no commit or push; the orchestrator owns git and integration gates.

## Checks you may run

- The project's focused test command for this phase's owned files.

## Report

Per-item marks, with this phase's focused output pasted and phase 1's accepted report cited; phase 3's gate output belongs to phase 3's report.
```

## Investigation

- Give the reader a cited answer, not a summary of what was read. Every claim names a `file:line` or captured command output from this project's tree or the installed source under investigation.
- State the search scope: files and versions consulted, the exact queries, and what an empty result covered. An absence claim from one search is not an absence proof.
- Separate what the evidence shows from what you infer. A claim you cannot source stays under `caveats` with the alternative explanation.
- When the question needs an implementation slice, hand off explicitly: name the decision the orchestrator must make, the likely owned files and the check the implementer brief must prove. Do not edit anything in this slice.
- Dispatch this shape to a read-only `scouter` or `researcher`; the orchestrator decides what to do with the answer.

```markdown
# Brief — find where the timeout default is set

Role: scouter · Agent: <agent-name> · Run: <run-id> · Report language: en
Worktree: /work/example

## Goal

Answer where the timeout default is set and how it reaches the caller, without changing the project.

## Decisions already made

- Every claim cites `file:line` or captured command output; unsupported inference goes under `caveats`.
- This slice is read-only. An implementation handoff names a decision, likely owned files and the proof, but edits nothing.

## Expected result

- The report answers the question with citations, names the sources searched and lists anything still unknown.
- If an edit is needed, the report gives the orchestrator a handoff with the decision, likely files and check for a later brief.

## Acceptance criteria

1. Each claim cites `file:line` or captured command output — proved by reading the report.
2. Absence claims name the exact queries, scope and alternative tried — proved by the report's search notes.
3. A handoff appears only when the answer needs an edit, with a decision, likely files and check — proved by reading the report.

## Sources (local paths, read before editing)

- Code path named by the question and the installed dependency source when relevant

## Forbidden

- Read-only: no file edits, no commits, no pushes.

## Checks you may run

- Read-only lookup commands and, when behavior must be confirmed, the project's focused test command.

## Report

Per-item marks, with the cited paths and command output that answer each item.
```
