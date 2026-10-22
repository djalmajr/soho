# Worker collaboration (the review cycle)

The worker collaboration lets an author and a reviewer talk directly — implementer → reviewer → correction → new review — with the orchestrator supervising and making the final decision. The Go modules (`internal/collaboration`, `internal/communication`) are the single engine for policy, assignment and delivery: the native CLI calls them and the plugin calls that CLI. No interpreter fallback or second policy engine exists.

It is operational discipline between agents of the same system user, not isolation against an adversarial agent with shell and config access.

## When to use it

- A review round-trip through the orchestrator is too slow or too costly for the slice (every finding and fix would be re-typed by the caller).

Without an assignment the normal workflow stands: the report file is the completion signal and the orchestrator relays everything.

## Configuration

```ini
worker_messages=off
```

`off` (the default) is the compatible mode: workers keep refusing ordinary `send`, and a collaboration's policy reads as disabled. `policy` enables the named rules; **review is an example of rules, not a fixed mode of the engine**:

```ini
worker_messages=policy
worker_messages.rules.review_request.from=role:implementer
worker_messages.rules.review_request.to=role:reviewer
worker_messages.rules.review_request.types=review.ready,review.question
worker_messages.rules.review_request.scope=assignment

worker_messages.rules.review_feedback.from=role:reviewer
worker_messages.rules.review_feedback.to=role:implementer
worker_messages.rules.review_feedback.types=review.finding,review.question,review.result
worker_messages.rules.review_feedback.scope=assignment
```

- **Selectors** are `role:<role>`, `lane:<lane>` or `agent:<name>`, comma-separated lists, no wildcards. `agent:<name>` selects the current member of the assignment, not a lifetime permission on a reusable name.
- **Types** (first version): `review.ready`, `review.question`, `review.finding`, `review.result`. New scopes and types can be added without changing the rule mechanism.
- **Directional.** A rule authorizes `from` → `to` only; it never authorizes the reverse implicitly.
- **Scope** `assignment` requires an assignment created by the orchestrator with the exact participants and the task; a role rule never opens the floor to every agent of that role. The first version accepts local collaboration only: same workspace and logical repository, worktrees of that repository included. Cross-machine worker communication is a later step (orchestrator `send` to other machines is unaffected).
- **Default deny.** No matching rule, an invalid selector, an unknown type or an unreadable record refuses the delivery. The destination's `inbound=off` (that project's config, read where the target lives) prevails over every rule.
- **Layers.** A named rule is taken **whole from the highest layer that sets it** — its fields are never merged across layers; a partial definition in a higher layer is a visible error, not a merge. `enabled=off` is the explicit tombstone that deactivates an inherited rule. Setting `worker_messages=off` disables new worker messages and is visible in the collaboration state; it cannot undo messages already delivered.

## Commands

| Command | Who | What |
|---|---|---|
| `collaborate start <author> <reviewer> --brief <path> [--max-rounds N]` | orchestrator only | validates both participants (registered workers, one edit role and one review role, different model families, same logical repository), the brief (complete sections, concrete owned input files), fingerprints the owned inputs, and checks the policy for the three directions the cycle needs (`author→reviewer review.ready`, `reviewer→author review.finding`, `reviewer→author review.result`) before publishing. It does not open panes: spawn the workers first. `--max-rounds` is 1..100, default 3. |
| `collaborate status <assignment> [--json]` | any (read-only, runs under `SOHO_NOWRITE=1`) | phase, round, current revision (re-fingerprint of the owned inputs) and the cause of any problem (unreadable participants, `worker_messages=off`, …). |
| `collaborate event <assignment> ready\|findings\|corrected\|approved\|escalate --report <path> --revision <fingerprint>` | the assigned worker | persists the event for its role and phase, fingerprints the report (an absolute, non-empty file), and delivers the message to the counterpart. `--revision` must match the declared review inputs; a new revision stores a snapshot (`files` + `manifest.json`) under the assignment. |
| `collaborate stop <assignment>` | orchestrator only | records an explicit interruption (phase `escalated`). |
| `collaborate finalize <assignment> --verdict accept\|reject` | orchestrator only | the only way to end a collaboration. `accept` re-validates: participants still match, the revision still matches the owned inputs, no event has a `pending`/`uncertain` delivery, and every round report still exists with the stored hash. An escalated collaboration cannot be accepted without a fresh review. |
| `send <target> <message…> --assignment <id> --type review.question` | the assigned worker | free text inside an assignment is `review.question` only; anything else is published with `collaborate event` and its declared revision. `--assignment` and `--type` travel together. A worker's ordinary `send` (no assignment) is still refused before anything is sent: a worker reports through its report file. |

## Phases

`preparing` → `reviewing` (ready) → `fixing` (findings) → `reviewing` (corrected) → `awaiting-orchestrator` (approved) → `finished` (finalize). `stop`, a failure, a divergence or the round limit move the assignment to `escalated`; the orchestrator's decision (finalize or an explicit restart as a new assignment) ends it.

- **ready** — the author, in `preparing`. Publishes the review inputs; the revision changes and the snapshot is taken.
- **findings** — the reviewer, in `reviewing`. At the round limit the assignment goes to `escalated` instead of `fixing`.
- **corrected** — the author, in `fixing`. Increments the round (the limit default is 3 correction rounds); refused at the limit.
- **approved** — the reviewer, in `reviewing`. Requires a **passing review report** (the fixed `findings:` header with verdict `pass`, no open P0–P2, no `[partial]` items); the approval names the version it examined.
- **escalate** — either participant; no message is delivered (`not-required`), the phase becomes `escalated`.

A submitted delivery is **not** a receipt and not a review: the message's statuses (`submitted`, `queued`, `received`, `uncertain`, `refused`) describe transport, and the CLI's queue is not proof that the review happened. While a delivery is `pending` or `uncertain` the next event is refused until the orchestrator inspects and decides — there is no automatic resend and no exactly-once promise.

During the cycle the author's side is read-only for the reviewer: the reviewer examines the snapshot and its manifest, a `review.question` asks for clarification, and no text in the messages grants authority to expand the scope or to approve.

## What the orchestrator must watch

The native Team panel shows the assignment, phase, round, revision and delivery for registered workers. It reads the shared CLI `status`; its close control refuses active or unavailable collaboration state. Inspect and finalize the assignment through the orchestrator before closing it.

- `status`/`wait` on a participant of an active assignment report `collaborating` (with the assignment, phase, round, revision and last delivery) **instead of the usual state — including a stale complete report**, which is an artifact only while the assignment stands. `collaboration-unavailable` (exit 4) is an unreadable or unverifiable registration; `collaboration-disabled` (exit 4) is `worker_messages` not `policy` (or an unreadable policy). A workflow without an assignment keeps the report-as-completion contract.
- `collect` also reports the active assignment as metadata: participants, phase, round, revision, delivery and the last evidence path. It does not print an old report as a completed result or fall back to the terminal. `collect --verify` checks the last event's report hash and its file-hash lines; it does not accept or finalize the cycle. Missing hash lines and changed evidence both retain exit 16; an unreadable report exits 4. Final acceptance re-checks the input revision and every round report.
- `release` refuses a participant of an active collaboration — `--force` included; the cycle ends only through `stop` and `finalize`.
- A contract change (owned files, participants, scope) is not an in-place edit: `stop` or `finalize` the assignment and start a new one.
- The dispatch prompt of an assigned worker carries the active collaboration block (assignment, phase, round, counterpart, the `review.question` form, and the instruction to read `collaborate status <id> --json` first). It is appended only while an assignment is active; nothing is added to other workers' prompts.

## Guarantees (what checks them)

| Guarantee | What checks it | When | Window it leaves open | Write before it |
|---|---|---|---|---|
| `start`/`stop`/`finalize` are orchestrator-only | `collaborationOrchestrator`, `internal/cli/collaborate.go:259` | before any state is read | none — refused before the store is touched | none |
| free text never changes state to approved | `send` accepts only `review.question` free text (`internal/peer/peer.go:767`); other types need a persisted pending event for the revision (`internal/peer/assignment.go:67-77`); phases change only through `PrepareEvent`/`Finalize` (`internal/collaboration/events.go`) | at send / at event | none for state; delivery uncertainty stays visible (exit 15) | the event record (status `pending`) is persisted before the prompt is submitted |
| a stale approval cannot be finalized | `finalize accept` re-fingerprints the owned inputs (`CheckRevision`, `internal/collaboration/events.go:172`), re-hashes every round report and refuses `pending`/`uncertain` deliveries (`internal/collaboration/events.go:153-168`) | at `finalize accept` | a change between `approved` and `finalize` is caught by the same re-check; an `escalated` assignment cannot be accepted at all | none (read-only validation, then one record update) |
| a duplicate event is not re-delivered | `PrepareEvent` returns the stored event when its id matches (`internal/collaboration/events.go:43-48`) | at `collaborate event` | none — the record, not the prompt, is the authority | the record itself (already persisted) |
| `release` cannot end the cycle | `collaborationReleaseRefusal`, `internal/cli/release.go:59-65` | at `release`, before the release | none; `--force` does not bypass it | none |
| the NOWRITE reads write nothing | `nowriteReadInvocation` runs before any command (`internal/cli/cli.go:337-366`); a refused invocation writes nothing, not even a friction line (`internal/cli/cli.go:330-336`) | before dispatch | none | none |

## Limits, honestly stated

- Local collaboration only (one workspace, one logical repository, worktrees included). Worker messages between machines are not evaluated at the destination in this version.
- On Windows, worker prompts require Herdr's native executable. A `.cmd`/`.bat` launcher cannot preserve the complete multiline argument and is refused before it runs.
- Delivery is best-effort with an explicit `uncertain` state; after an uncertain result the orchestrator inspects the persisted state and resumes explicitly. There is no exactly-once and no automatic retry.
- The round limit and the escalations bound the cycle; they do not bound what the two agents can agree to — the scope is the assignment's owned files, and scope expansion goes back to the orchestrator.
- Snapshots and revision checks do not lock the author's files or isolate the agents at the operating-system level. A concurrent edit between a check and a write remains possible; the orchestrator must coordinate changes to the owned files until finalization.
- An interrupted lock or an orphan snapshot needs explicit inspection. The cycle does not automatically remove stale locks, replay messages or repair interrupted state.
