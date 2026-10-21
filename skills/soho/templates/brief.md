# Brief — <slice name>

Role: <role> · Agent: <agent-name> · Run: <run-id> · Report language: <pt-BR|en>
Worktree: `<absolute path>` (only when the slice has its own worktree)

<!-- Before dispatching, reread this brief as the worker and run the checklist in references/orchestration-contract.md § Brief checklist: any place where the worker would have to choose is a gap. Two or three items per brief. A change to a brief already in flight is its own file, sent with `dispatch <agent> amend.md --amend` — never a hand-rolled prompt. -->

## Goal

One paragraph. What must be true when you are done.

## Decisions already made

What the worker must not choose: names, interfaces, formats, user-facing texts, values, exit codes. Each rule as an exact predicate with one example that matches and one that does not. Invariants that are not visible in the output (file mode, atomic replace, behavior on interrupt, platform paths).

## Expected result

What exists and is observable at the end: files, commands, output, behavior.

## Acceptance criteria

1. <criterion> — proved by `<command>` → `<expected output>`
2. …

## Failure matrix (only when the slice publishes, retains, prunes or deletes state)

<!-- Delete this section for any other slice. -->
One executable fixture per step boundary of publish → prune/delete → retry:

- [crash] a crash between publish and prune/delete: what must survive
- [retry] the same step repeated or retried after a failure: the result converges
- [clock] the clock goes backwards between runs: nothing still needed is pruned

Say which items have local proof (fixtures) and which need operational proof.

## When the brief does not decide

Do not choose. Mark the item `partial`, list the gap and the options you see under open questions, and continue with the other items. Never invent names, endpoints, flags, credentials, URLs or requirements.

## Owned files

<!-- read-only roles (the reviewers, the inspector) may omit this section; the dispatch lint skips it for them -->
- `path/to/file.ts` — what changes here
- `path/to/new-file.test.ts` — new

## Forbidden

- `src/lib/i18n/messages/*` — keys are delivered below, do not edit
- Anything under `src/server/auth/` — owned by another slice
- No commit, push, tag, or PR. The orchestrator owns git.

## Sources (local paths, read before editing)

- `docs/adr/0002-….md` — decision that constrains this slice
- `src/feature/existing-thing.ts:40-120` — pattern to follow
- `planning/<initiative>/business/<rule>.md#RULE-ID` — rule to satisfy

## Project rules that apply

- Code in English; user-facing copy via i18n keys only.
- Failing test before implementation for behavior changes.
- <any repo-specific rule copied verbatim here>

## Shared resources delivered ready

```ts
// i18n keys to reference (already added by the orchestrator)
issues.detail.linkProject = "…"
```

## Checks you may run

- <the exact focused check commands for this project and slice>
- Run only this slice's focused tests while iterating and before the report (in the herdr-soho repository: `go test ./internal/<package> -run <pattern>`). Run the full Go gates once at the end of the effort, not for every slice.
- Do NOT run the formatter or e2e.
- Give every test subprocess a bounded timeout; every branch of a fake CLI must handle its arguments explicitly.
- UI slices: say who runs the browser smoke (you, when you can open a browser; otherwise the orchestrator or an `inspector` after your report).
- Tests that open a local port (a fake server, a local database, a workers runtime) do not run inside the Codex sandbox (`listen EPERM`): mark them `partial`; the orchestrator runs them.
- A check that needs a live service (a cluster, a database, containers): say whether the worker may start one. By default it may not — it marks the item `[partial]` and the orchestrator runs the check. Name the exact check: a client-side dry run misses what the server validates (for Kubernetes, `kubectl apply --dry-run=server --validate=strict`).
- Write one file per tool call, a few hundred lines at most per call; grow a larger file with follow-up edits.

## Non-goals

- <explicit thing that looks adjacent but is out of scope>

## Report

Write Markdown to `<report-path>` using the format in `templates/report.md` (per item: `[done]` / `[partial]` / `[skipped]` + reason). When finished, reply with only that path.
