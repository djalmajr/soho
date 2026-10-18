# soho

Give the calling agent an **omp-style team**: one CLI agent per role (`scouter`, `researcher`, `designer`, `implementer`, `tasker`, `reviewer`, `security-reviewer`, `ui-reviewer`, `inspector`, `documenter`) running in [Herdr](https://herdr.dev) panes. `planner` is the orchestrator itself (`spawn planner` exits 12 and opens no pane), and a nested orchestrator takes the `sub-orchestrator` role. The caller stays the orchestrator: it decomposes, writes briefs, dispatches, collects file-based reports, integrates, runs the gates, and owns git.

## When to use

- You are running an agent inside Herdr (`HERDR_ENV=1`) and want parallel workers with distinct responsibilities, or a reviewer from another model family before pushing.
- The user asks for "a designer agent", "a reviewer", "a herd", "workers", or "like omp's agents".

Do not use it outside Herdr, or for a change small enough to do directly: the orchestrator keeps one-or-two-file edits, docs, config, questions and quick verifications for itself (if the brief takes longer than the change, make the change) and delegates the rest.

## How it works

```text
roles/<role>.md  ──▶  spawn (pane split + agent start)  ──▶  dispatch (role + brief → prompt file)
                                                              │
   .agents/herdr-roles/<role>.md overrides per project        ▼
                                                    report file  ◀── collect
```

- **Roles** are markdown files with frontmatter (`kind`, `alternatives`, `mode`, `timeout`) and a prompt body, like omp's `.omp/agents/*.md`. A project overrides any role by dropping `.agents/herdr-roles/<role>.md`. The shipped roles set their own `timeout` (reviewer and security-reviewer run 30 min); a role without one falls back to `dispatch_timeout` (15 min). That budget is the role's effective timeout — scaled by its effective effort (`xhigh` × 1.5, `max` × 2, else × 1) — and `dispatch` and a `wait` without `--timeout` both use it (a multi-agent `wait` allows the largest across its roles).
- **Table answers cite the lookup.** A `scouter` or `researcher` answer that depends on a lookup table (types, keys, routes, registries) also reads and cites the function that consults it — normalization, prefixes and fallbacks decide what actually matches, so an entry in the table does not prove a value matches.
- **Kinds** are Herdr agent kinds (`codex`, `claude`, `grok`, `agy`, `cursor`, `pi`, `opencode`, …). A fresh install uses the role defaults — implementation and research on `grok`, review and documentation on `codex`, security review on `claude`, design and visual QA on `agy` — when nothing is configured; that is a starting point, not a policy (see "Assistant and model choice").
- **Briefs** follow `templates/brief.md`: goal, owned files, forbidden files, local sources, applicable rules, allowed checks, report format. Workers never commit or push.
- **Reports** are files under the state dir, per-item with three states (`[done]` / `[partial]` / `[skipped]` + reason), so collection never depends on scraping a TUI. The report path in the dispatch's composed prompt is authoritative: a brief's `# Report` section can name a different literal path (hand-written or reused), but the worker writes to the dispatch's path anyway, and the dispatch's routing (JSON, `last-report`, wait marker) never follows the brief's.

## The team: panels and lanes

The other panes are **lanes**: sessions that take, in order, any role in their group, each with a capacity of workers (`lane.<name>.panes`). The preset is resolved at run time from `panes` (2|3|4, default 4), counting your pane:

| Panes | The team |
|---|---|
| 4 (default) | You + the `build` lane with **two** builders + the `review` lane with one reviewer |
| 3 | You + one builder + one reviewer |
| 2 | You + one builder; you review, from another model family than the builders (pick it by hand) |

- The `build` lane holds `implementer`, `designer`, `tasker`, `scouter` and `researcher`. Research is build work: a free builder maps the code or reads another repository, or the orchestrator does it.
- The `review` lane holds `reviewer`, `security-reviewer`, `ui-reviewer` and `inspector`; a session never reviews code it wrote.
- A full lane means `wait` for its report, then dispatch — not a new pane (`spawn` reports the lane busy, exit 10).
- Worker names follow the lanes: `build`, `build-2` (the second builder of a four-panel team), `review`, `docs`.

## Strict or flex, and the documenter

`pane_mode=strict` (default) opens no temporary worker: each lane stays within its capacity, and documentation is build work — the `documenter` borrows a build slot.

`pane_mode=flex` may open up to `flex_extra` (1) **temporary** worker above the panel count, only for the roles in `flex_roles` (`reviewer,documenter`): a second reviewer or the `documenter` in its own `docs` lane. With one free slot the review comes first — it unblocks the push; documentation waits.

The **documenter** edits documentation only (README, guides, references, ADRs, CHANGELOG), never code, and works after a slice passed review, from the spec and the committed diff. Documentation that describes behavior (commands, flags, config) goes to a reviewer; the rest the orchestrator checks. Its report is a claims table: each factual claim with its source (`path:line`, or the exact read-only command and what it printed); a claim it could not verify goes to open questions, not into the text.

## Who is who

- **Orchestrator:** whichever agent runs the skill from a Herdr pane. Nothing to configure; today that is usually Claude Code, but a Codex pane running the skill would orchestrate the same way.
- **Roles:** `roles/<role>.md` files. Change who plays a role with `--kind` at spawn time (one agent), a project override in `.agents/herdr-roles/<role>.md` (one repo), or by editing the skill's file (everywhere).
- **Names:** `init` renames the caller to `orchestrator`. With lanes on (the default) workers are named after their lane (`build`, `build-2`, `review`, `docs`), not after the role; agent names are global in Herdr, so a name another workspace already uses gets the next free suffix, and a `--name` that is taken does the same (with a warning). `lanes=off` keeps the old role names (`reviewer`, `reviewer-2`, …); a nested orchestrator is `sub-orchestrator`.
- **Pane titles:** `init` titles an untitled pane `orchestrator: <project>`; `soho title "<objective>"` sets `orchestrator: <objective>` (the objective is cut at 60 code points) and `title --clear` clears it, so the sidebar shows what this session leads. Every `dispatch` also titles the worker's pane `<role>: <task>`.

## Commands

```text
soho init                              # you become `orchestrator`; an untitled pane gets one
soho title "slice 2"                   # this pane: `orchestrator: slice 2`
soho roles [--json]
soho spawn implementer                 # the agent is named after its lane (`build`)
soho dispatch build brief.md           # waits for the report file
soho collect build [--verify]          # active collaboration: metadata and last evidence path; otherwise prints the report; an agent still working or blocked with no report: stderr and exit 4 (`--lines` forces the terminal); --verify checks the evidence hash and its sha256 lines (exit 16 on changed/missing or no hash lines, 4 if unreadable)
soho stats [--since <date>] [--by role|kind|model|agent|effort] [--json] # tasks, times and review findings; date is YYYY-MM-DD or ISO 8601
soho friction add "<text>" [--brief P] # record one friction note (level note, command friction)
soho run scouter brief.md               # spawn + dispatch + collect
soho wait build review                 # block until every report exists
soho roster [--scope workspace|server] [--json]   # live agents with role/kind/pane/state/report and the current task (TASK); default workspace; --scope server adds the other local workspaces, grouped and identified
soho send build "check the report"     # peer message: a ref (local/w12:p1, windows/w3:p1) or a name on the local server; --file <path>; --now; --timeout MS (default 600000)
soho collaborate start <author> <reviewer> --brief <path> [--max-rounds N]   # orchestrator only: open a review cycle (distinct model families; see "Worker collaboration")
soho release build --close             # closes only panes the skill created; never a participant of an active collaboration
soho clean --older-than 7              # drop gone agents, delete old briefs/reports
soho find [words] [--machine <label>]… [--all] [--json] [--timeout MS]  # live panes with a paste-ready reference, filtered by the words; local rows first, remotes as they arrive, under one global deadline
```

`stats` scans composed prompts and reports in the workspace state and temporary routing directories. It counts accepted dispatches and legacy pairs without a sidecar. Kind, model and effort come from the dispatch snapshot; an absent snapshot or empty field groups as `(unknown)`. With `--by` omitted, it groups by role and JSON has `roles`, `lost_briefs` and `review`; with `--by` (including `--by role`), JSON has `by`, `groups`, `lost_briefs` and `review`. The review table covers reviewer, security-reviewer, ui-reviewer and inspector. Other date formats exit 2. Each counted pair is exactly one of a task, amendment or reuse. An agent's first non-amendment brief of its worker session is a task; every later non-amendment brief of the same session counts under `reuses`, with or without a role change. Same session means the same `started` value recorded in the sidecars; a legacy sidecar without one falls back to the same kind and model as the agent's earlier non-amendment brief. `briefs` (tasks plus reuses) is shown after `tasks` in the table and in the JSON of each group. An amendment belongs to the brief it amends: it is never lost or pending on its own, and it does not decide which brief is the agent's last. A brief without a report is pending while its agent is in the roster and it is the agent's last non-amendment brief, and lost otherwise. Each group also has `no_report`, `minutes`, `partials` and `not_received`. The last field records a dispatch's arrival-check result even if a later wait recovers. JSON `lost_briefs` maps each group to the stored composed-prompt paths for lost pairs, and the text output lists them below the tables.

The reviewer dispatch refuses a reviewer whose model family matches a live edit agent (exit 5) unless `--allow-same-family` is given. `--for <author>` (an agent, a family, or a kind with a fixed family such as `codex`) compares only with whoever wrote the slice; an author whose family is unknown falls back to the whole-roster check. That keeps the check when an editor of the reviewer's family works on another slice, and it covers code the orchestrator wrote.

To change a brief a worker already has, busy or finished, write the amendment to a file and run `soho dispatch build amend.md --amend`. The amendment gets a new report that `wait` watches, and the pane keeps its task. A busy worker reads it when its CLI delivers a message sent mid-task (most queue it). Never send an amendment with `herdr agent prompt` by hand: `wait` would keep watching the old report.

`SOHO_NOWRITE=1` is a read-only inspection mode. The optional plugin's team panel runs its reads with it. Only these invocations run:
- the exact `doctor`, so `doctor --fix` cannot write;
- `roster` with an optional `--scope workspace|server` (the default is workspace; `--scope server` is still a read);
- the exact `explain`;
- the exact `config`, but not `config set` or `config --project`;
- the exact `roles`, but not `role <name>`;
- `friction` with its reading options, but not `friction add`;
- `collect <agent> [--lines N] [--verify]`;
- the exact `copies`, but not `copies add`;
- the exact `procs`, but not `procs add`;
- `status [agents…]`;
- `gc` without `--yes`;
- `collaborate status <assignment> [--json]` (the read of an active collaboration; the other `collaborate` operations write);
- `capabilities --json`.

The CLI then never writes to the project or the state: no `.gitignore` entry, no state directory, no friction log and no task-report sync. A state directory that does not exist reads as empty. Every other invocation exits 2 with a message that lists the accepted ones. Without the env, the CLI behaves exactly as before.

## Finding a session

`soho find [words]` lists the live panes of the local Herdr — with `--machine <label>` (repeatable) or `--all` (every enabled machine of `herdr machine list`), of other machines too — one line per pane: the reference `<machine>/<ws>:<pane>`, the agent name, kind, status, workspace and tab labels, and the cwd. Every word must match (name, kind, status, labels, ids, cwd or title, case-insensitive); a single word that is a reference matches only that machine's pane and queries that machine even without `--machine`. `--json` prints the full entry per line.

`find` is progressive. The local rows are published as soon as the local snapshot is available — before the machines are even enumerated — and each remote machine's rows are published as that machine finishes, in arrival order (not machine order), while the remotes are queried in parallel, at most 4 at a time. One global deadline covers the whole discovery, enumeration included: 30000 ms by default, adjustable with `--timeout MS` (a positive integer; an invalid value exits 2 before any herdr call). Each subprocess gets the smaller of 30000 ms and the time left on the deadline, and a cancellation ends only the subprocesses the query started. stdout stays structured — TSV, or one JSON object per line — while load, failure and deadline diagnostics (a slow or failing machine, a stopped `machine list`) go to stderr; results already printed are not discarded when a later machine fails. Exit 0 with at least one entry, 1 with none (like `grep`), 2 on bad usage, 4 when the local machine is unavailable; a remote machine that fails prints a stderr line and the rest is still listed. `find` changes nothing.

The reference is the handle for a pane: copy it from a `find` line and paste it into the chat to name the pane (a later command or the orchestrator resolves it).

The optional plugin also opens a centered native session popup at 80% of terminal width and height, including the border. The `pick` action ("Sessions: search, copy or focus") lists panes from the local server and enabled machines. Tree is the initial view; `v` switches to Table (Machine, Workspace, Pane, Agent and Status columns) and back, remembering the last choice per user on each machine across workspaces. Both views keep machine and workspace groups together: `p` sorts pane names, `w` sorts workspace groups, `s` sorts statuses (blocked, working, idle, done, unknown), and repeating a key reverses direction. `Tab` switches search/list focus; sorting and view keys are ordinary letters in search. With list focus, `f` cycles All/Agents/Terminals and `t` cycles All/Working/Blocked/Idle/Done/Unknown; these filters combine with text search. The text filter matches reference, name, kind, status, task, workspace/tab labels, cwd and machine. `PgUp`/`PgDn` move one visible page of items and the vertical mouse wheel moves three items per notch, without selecting group headers. Search treats all printable control letters as text. View and sort changes preserve the selected full reference; the layout adapts to resize and fixes details and controls at the bottom. With list focus, `c` copies only the full reference (for example `windows/w3:p`) and keeps the modal open with status-bar feedback; in search, `c` is ordinary text. `Enter` focuses the selected pane, `Ctrl+Enter` remains a navigation alias, and `Esc`/`Ctrl-C` close. Remote focus selects on that server and does not switch the machine displayed locally. With list focus, `i` opens the team view of the selected row's workspace in the same frame: a `ROSTER` block and a `ROLES` block, the output of `roster` and `roles` run in that workspace's directory with `SOHO_NOWRITE=1`, so the view never writes. A failed read shows its cause in its own block and keeps the other; a workspace without a soho team shows `no soho team in this workspace`. The arrows and `PgUp`/`PgDn` scroll the view, `Esc` or `i` return to the list with the same selection, and `Ctrl-C` closes the modal; in search, `i` is ordinary text. The view reads the local machine only: a row from another machine shows `team view: not available yet for remote machines`. The unified picker and `find` share the Go discovery operation and its deadline, concurrency and cancellation. Bind the picker with a `[[keys.command]]` entry of `type = "plugin_action"` and `command = "djalmajr.soho.pick"`.

The plugin provides two complementary panels:

- **The unified picker** includes task details, status totals and the last update time. It refreshes every 10 s, with `r` while the list is focused or `Ctrl-R` from either focus. Selection stays pinned by full reference during refresh; a missing selected session does not become a different copy/focus target. Temporary mouse tracking is restored on exit. Use one shortcut for `pick`; the separate public board action/pane has been removed, while `plugin board` and `plugin bridge board` remain CLI compatibility aliases for this modal. The preference is stored in `session-view` beside the user soho config. Missing, invalid or unreadable preferences select Tree without rewriting them. Only an explicit list-focus `v` writes the preference; a failed save leaves the current modal usable and displays a notice.
- **The team panel** (the `team` action; `roster` and `doctor` open it too) shows the focused workspace: what the team is doing, its workers and their last reports, the `doctor`, the resource pressure with what `gc` would free, and the friction summary. Active collaborations show assignment, phase, round, revision and delivery; close is refused until the orchestrator ends the assignment. Its only writes are `release --close` of a worker and `gc --yes`, and each runs only after an on-screen `y`.

The README's plugin section has the keys and the install steps.

## Peer messages

`send <ref|name> <message…>` sends one peer message to a local agent or a saved remote machine. Use the receiving machine’s configured profile name in references such as `local/w12:p1` or `windows/w3:p1`; a hostname shown in a remote header may need mapping to that profile. The header is metadata only: after the opening line `[soho:peer] #<id> Message from another agent …` it carries the sender reference (with the sending machine’s hostname for a remote target), the known sender name, kind, role and model, the send time and the exact reply command, and unknown fields are omitted; the body is quoted with `>`, and the closing line is `[/soho:peer] #<id> end of message`. Receipt checks also accept the legacy closing marker. Quoted payload markers, longer IDs with the same prefix, old transcripts and input text do not prove consumption. By default, a working or blocked target is waited on for `--timeout MS` (600000); `--now` submits immediately and lets its CLI decide how to queue or steer it. Neither option bypasses trust, approval or question dialogs. The prompt is submitted once, with no automatic text resend. Fresh state, transcript and provider-specific history or queue evidence determine `sent` or `queued`. A collapsed Cursor follow-up without a readable matching ID remains uncertain. One retry Enter is permitted only when a readable, recognized composer holds this exact message’s opening header and complete ID, with no dialog; unknown layouts and failed reads permit no key. Failed key transport is reported truthfully. Exit 15 means delivery was not proved, including a prompt that may still be in the input or an unobservable queue; read the pane and saved `<state>/wait/send-<id>.screen` before any retry. Exit 17 means the target remained busy or showed a dialog; nothing is typed into that dialog.

The target project decides whether it accepts peer messages: `inbound=off` in that project's configuration refuses with exit 18. For a local target the sending side reads that key in the target's directory, with the target's session layer and without the sender's `SOHO_*` variables — so the sender's own configuration can never authorize or refuse on the target's behalf. For a remote target the policy is not consulted (the sending machine cannot read the remote project). Every attempt appends one line to `<state>/peer-messages.tsv` (timestamp, sender ref, target ref, result, character count, message id — never the message body). Exit codes: 0 sent, 2 usage, 4 target unavailable or unreadable screen, 15 not received / lost / unverified, 17 still busy or showing dialog, 18 refused by `inbound=off`.

### Worker messages inside a collaboration

A registered worker's ordinary `send` stays refused — a worker reports through its report file, not by message (exit 2). Inside an active assignment the worker may send the counterpart free text of type `review.question` only:

```text
soho send <counterpart> --assignment <id> --type review.question "<question>"
```

`--assignment` and `--type` travel together; any other type is published with `collaborate event` and its declared revision, not with `send`. The message is allowed only when the `worker_messages` policy authorizes that sender, destination, type and assignment, the destination's `inbound` is not `off`, and the sender is one of the assignment's participants; the destination is fixed to the assigned counterpart. Delivery is a short submission (the sender does not wait for the recipient to settle, answer or report): the result is `submitted`, `queued`, `received`, `refused` or `uncertain`; an `uncertain` delivery exits 15 and the next event is refused until the orchestrator inspects the persisted state — there is no automatic resend, and the delivery statuses describe transport, not the review. Free text grants no authority: it cannot approve, expand the scope or end the cycle.

## Worker collaboration

The review cycle — author → reviewer → correction → new review — runs between two workers with the orchestrator supervising and deciding. The full reference (policy, events, revisions, delivery, guarantees and limits) is `skills/soho/references/orchestration.md`; this section is the working summary.

**Configuration** (`worker_messages`, default `off`): `off` keeps every worker refusal; `policy` enables named rules. Each rule is directional (`from` → `to` only, no implicit reverse), scoped to the assignment, and taken whole from the highest layer that sets it (`enabled=off` is the explicit tombstone; a partial higher-layer rule is a visible error). Selectors are `role:<role>`, `lane:<lane>`, `agent:<name>` (comma lists, no wildcards); the known types are `review.ready`, `review.question`, `review.finding`, `review.result`. A message with no matching rule is refused (default deny), and the destination's `inbound=off` prevails over every rule. The first version is local only: same workspace and logical repository, worktrees included. Example with two directional rules — the request (ready and questions) and the feedback (findings, questions and the result):

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

**Lifecycle.** The orchestrator opens and closes the cycle; the workers only publish events:

```text
soho collaborate start <author> <reviewer> --brief <path> [--max-rounds N]   # orchestrator only
soho collaborate status <assignment> [--json]                                # read-only (NOWRITE)
soho collaborate event <assignment> ready|findings|corrected|approved|escalate --report <path> --revision <fingerprint>  # the assigned worker
soho collaborate stop <assignment>                                            # orchestrator only
soho collaborate finalize <assignment> --verdict accept|reject                # orchestrator only
```

`start` validates the participants (registered workers, an edit author and a review worker from **another model family** — same family exits 5 — and the same logical repository), the brief (complete sections, concrete owned input files), the fingerprints of the owned inputs, and the policy for the directions the cycle needs, before publishing. Phases: `preparing` → `reviewing` → `fixing` → `reviewing` → … → `awaiting-orchestrator` → `finished`; `stop`, a failure, a divergence or the round limit (default 3 correction rounds, `--max-rounds` 1..100) move it to `escalated`. `ready` and `corrected` come from the author, `findings` and `approved` from the reviewer; every event carries an absolute report (its hash is stored) and the fingerprint of the inputs it declares — a new revision stores a snapshot (`files` + `manifest.json`) under the assignment, and the approval names the version it examined. `approved` needs a passing review report (verdict `pass`, no open P0–P2, no `[partial]` items). Changing any declared input invalidates the approval.

**The orchestrator's part.** `status`/`wait`/`collect` on a participant of an active assignment report `collaborating` (phase, round, revision, last delivery) instead of the usual state — a stale complete report is an artifact only while the assignment stands. `release` refuses a participant of an active collaboration, `--force` included. `finalize accept` re-validates everything (participants, current fingerprint, no unresolved delivery, every round report still present with its stored hash) before the cycle counts as accepted; a contract change is a `stop`/`finalize` plus a new assignment, never an in-place edit. After an `uncertain` delivery (exit 15) the orchestrator inspects the persisted state and resumes explicitly — no automatic resend, no exactly-once. `collect` includes the participants and last evidence path without printing a report body or reading the terminal; `--verify` checks the last event's report and its file hashes. It never accepts the cycle.

**Shared implementation.** The native Go CLI owns collaboration policy, assignment validation, peer delivery and the state commands. Plugin entrypoints call those same rules; they do not implement a second policy engine. `capabilities --json` reports `worker_collaboration: 1`. No JavaScript or interpreter fallback is shipped.

## Ephemeral jobs

An ephemeral job is one self-contained orchestration run driven by a dispatcher, not a human inside Herdr. The authority for every command, flag, exit code, field name, event type and limit is `docs/job-contract.md`; this section is the working summary.

**What a job is, and who drives it.** The dispatcher chooses the machine and runs `job start --id <id> --repo <org/repo>` on it, sending the JSON brief on stdin (`--brief <file|->`, where `-` is stdin). `job start` runs outside Herdr: it validates, prepares the isolated worktree at `<checkout>/.worktrees/job-<id>` on branch `job/<id>` (or `--mode workspace`, the checkout itself, only for jobs that change no code), resolves the team, opens the Herdr workspace `job-<id>` and starts `job supervise --id <id> --repo <org/repo>` in its first pane, where the supervisor runs the `job-orchestrator` role. Nobody drives the job's panes by hand: the dispatcher talks only to the job through the commands below, never to the job's workers, and people give feedback to the dispatcher, which relays it to the job.

**Reading and steering.** Every subcommand prints exactly one JSON line on stdout, except `events`, which prints JSON lines. `status`, `wait`, `collect`, `list` and `events` read files only and run under `SOHO_NOWRITE=1`, while `amend`, `send`, `cancel` and `ack` write a control file under the job directory that the supervisor picks up, and none of them needs to run inside Herdr.

- `job status --id <id>` — read-only: state, event count, last seq, pending decisions, and the always-present `partial` / `partial_collaborations` pair (a `done` job may still carry `partial: true` when a collaboration was left partial; see the contract).
- `job wait --id <id> [--timeout MS]` — blocks until a terminal state.
- `job events --id <id> [--since <seq>] [--wait MS]` — the event log, the source of truth: the events with `seq > since` in order, long-polling with `--wait`, ending in the fixed `{"eventos":"fim","ultimo_seq":N,"estado":"<state>"}` line.
- `job collect --id <id> [--md] [--verify]` — the final report (JSON, or `report.md` with `--md`).
- `job amend --id <id> [<file>|-]` — feedback that changes the contract (goal, acceptance, decisions, constraints, scope) or answers a `question`/`blocked`; it becomes a formal amendment with a new report and unblocks the job. When in doubt, use `amend`.
- `job send --id <id> [<file>|-]` — a non-blocking note to the job orchestrator; it does not unblock the job.
- `job ack --id <id> --upto <seq>` — the dispatcher routed every decision up to `<seq>` (monotonic; a lower value is a no-op).
- `job cancel --id <id> [--grace <seconds>]` — the job waits `--grace` seconds (default 120), then stops and reports what it has.
- `job close --id <id> [--force]` — release the job (exits 24 while a decision is still unacknowledged; `--force` closes anyway).
- `job list [--state <state>]` — job counts by state on this machine.

**Machine configuration.** The keys in the user configuration file: `machine_label` (the canonical name of this machine, checked against the brief's `maquina`), `job_orgs` (the comma-separated organizations a job may check out; empty allows no organization), `job_repos_root` (checkout root, default `~/repo.git`, `%USERPROFILE%\repo.git` on Windows; checkouts live at `<root>/<org>/<repo>`), `job_timeout` (the default active budget in minutes, default 120; time spent `blocked` does not count), `job_blocked_timeout` (the blocked ceiling in minutes, 1..43200, default 1440: a job blocked longer than that in total ends `timeout` with `motivo` `tempo de bloqueio esgotado`; a `blocked_warning` event fires at 80%), `job_finishing_timeout` (the budget in minutes of the terminal procedure, 1..1440, default 10, recorded with the job at `job start`), `job_auto_cleanup` (`on`, the default, or `off`: the job's automatic close, which at the end removes the job's own worktree when it is clean and pushed and closes the job's workspace; resolved with the job's checkout as the project, so the repository's `.agents/soho.conf` beats the user file and can opt the job out, and recorded with the job at `job start`) and `job_human_input_minutes` (1..1440, default 5: the minutes the workspace close waits for the panes outside the job to stop changing their screen; resolved and recorded with the job like `job_auto_cleanup`), and `job_wake_cmd` (the optional wake hook, below). The team is configured per machine, not in the repository: in the user configuration directory (`$XDG_CONFIG_HOME/soho`, `%APPDATA%\soho` on Windows, otherwise `~/.config/soho`), `teams/example-org/example-repo.conf` holds the team for one repository on this machine and `teams/default.conf` the machine fallback. Resolution at `job start`: the repository file, else the machine default, else the repository's `.agents/soho.conf` when it sets at least one `lane.*.roles`, else exit 2 — a job never falls back to the built-in 2/3/4-pane presets.

**Git.** Workers never commit or push, and this is the one intentional adaptation of the general rule that soho never commits or pushes: the job orchestrator commits once per integrated slice inside the job worktree, and only the job supervisor pushes — every new commit of `job/<id>` (it watches `HEAD` every 30 seconds; `job checkpoint` pushes immediately), never with `--force` — and opens one draft pull request. The supervisor never merges and never marks the pull request ready for review; a job that ends with no commit opens none.

**Collaborations at the end of a job.** On every terminal path, before the terminal status is visible, the supervisor asks the collaborations the job owns (its workspace and repository root) to finish with the stop amendment, waits within the existing grace, and persists one outcome per collaboration (`finished`, `partial` or `unavailable`, with safely read evidence) in `state.json` and `report.json`. A job whose orchestrator completed is still `done` when a collaboration stayed partial, and says so with `partial: true` and the IDs in `partial_collaborations`; failed, canceled and timeout outcomes keep their own status and carry the same fields. The partial collaborations are then interrupted (`stop`, then `finalize --verdict reject`, never `accept`) without rewriting their recorded outcome.

**Release and automatic close.** On a terminal state the supervisor finishes the job's collaborations as above, releases the workers and the job orchestrator, runs `gc --yes` for the job's registered copies and processes, and runs the automatic close (the worktree removal only after a successful release; the workspace close also after a failed one, as before). With `job_auto_cleanup=on` (the default) it removes the job's own worktree when it is clean and pushed — `git worktree remove` without `--force`, the local branch stays, and `workspace` mode has no job worktree to remove — and closes the Herdr workspace: at once when no pane outside the job is left, otherwise after the panes that are not the job's have not changed their screen for `job_human_input_minutes` (Herdr does not expose human input, so the wait is on screen changes); past six such waits it fires `needs_attention` with `motivo` `human_input` and leaves the workspace open. The events are additive: `needs_attention` wakes the dispatcher (once per reason of a kept worktree — `dirty`, `not_pushed`, `check_failed`, `remove_refused`, `not_owned` — `human_input`, or `close_failed` for a close that failed), `workspace_closed` fires once right before the close (the supervisor runs in the workspace and ends with it), and `folder_retained` (the job folder's path and size) fires once in both modes; with `job_auto_cleanup=off` nothing is removed and the workspace stays open, so only `folder_retained` fires. `jobs/<id>/` is never removed, `report.json` records `limpeza.worktree` (`removed` or `kept`) and the informational `removivel`, and removing a kept worktree is the user's decision, done by hand.

**Decisions and acknowledgement.** A relevant decision is published by the job orchestrator as a `decision` event and aggregated into `report.json` under `memoria.global` or `memoria.projeto` with its event `seq`. The dispatcher is the single maintainer of the shared memory: it classifies each decision, stores it, then calls `job ack --id <id> --upto <seq>`. A decision is pending while its `seq` is greater than `decisions_acked_seq`; `status` and `collect` report `decisions_pendentes`, and `job close` exits 24 while one is pending, listing the pending `seq` values.

**The wake hook.** `job_wake_cmd` is an argv string, split on spaces and run without a shell, for the event types the contract marks as waking the dispatcher: the command gets the event JSON on stdin and the environment variables `SOHO_JOB_ID`, `SOHO_JOB_SEQ`, `SOHO_JOB_EVENT` and `SOHO_JOB_IDEMPOTENCY_KEY`. It is retried at most 3 times (after 10, 30 and 90 seconds), bounded by a timeout, and a failure is logged as friction and never fails the job; an absolute command path is allowed, values with spaces are not. Waking is best effort — a dispatcher that misses a wake is still correct with `job events`.

**Requirements.** Herdr 0.9.1 or newer (0.9.3 or newer recommended on Windows). On Windows, `job start` never starts a Herdr server; when no running server is reachable it exits 4.

## Good to know

- The role prompt is the worker's first message; the project's `CLAUDE.md`/`AGENTS.md`, hooks, and permission mode still apply.
- `--effort low|medium|high|xhigh|max` is one ladder for every kind, clamped to what the CLI supports (claude, pi and codex up to `max`, with the chosen model's own ceiling on top for codex; cursor and grok `xhigh`; agy and gemini `high`). Without it the agent keeps its own configured default. Cursor encodes effort in the model id, so pass `--model` too: the spec is strict (spawn dies 2 before a pane when it matches no id of `cursor-agent --list-models`), but the step that appends the effort suffix passes the model through unchanged with a warning (`cursor model '<m>' not in --list-models; passing it through unchanged`) when the list does not confirm the resolved id.
- `--approvals full` removes tool and MCP prompts (each CLI's own flags, inside its sandbox). Hook-trust and first-visit trust dialogs are left to you; pass the native flag after `--` if you want them gone.
- State lives in `<repo>/.soho/` (gitignored) so sandboxed workers can write their reports. Codex denies writes under `.agents/` and `.codex/`, so the state deliberately avoids those. `clean` removes old briefs and reports. The codex `workspace-write` sandbox is narrower than that: it cannot write under `.git` (`git mv`, `git checkout -- <file>` fail on `.git/index.lock`) and has no network, local ports included. The worker's prompt says so; the orchestrator runs the git operations and the network tests, or grants network with `args.codex=-c sandbox_workspace_write.network_access=true` (or the same flag in `role.<role>.args`/`lane.<name>.args` when that role or lane is configured with kind codex: scoped args only reach the kind they were configured for, and `spawn` refuses resume flags such as claude's `-c`).
- Herdr reports lifecycle state, not turns; `dispatch` waits for the report file, not just for `idle`.
- A CLI that updates itself at start and exits (the Codex auto-update) is relaunched once by `spawn`, in the same pane with the same args; an exit without the update marker makes `spawn` exit 4 with the last screen lines. A roster line whose name is alive in another pane shows `gone` (it is stale: the recorded agent died); `max_workers` counts a line only when a live agent with the same name sits in the line's pane, and a spawn reusing a name removes the stale line.
- A Claude Code orchestrator's command tool caps each call at 10 minutes: run `wait` (or a waiting `dispatch`) in the background with the harness's own notification, not `timeout` in front; `wait --any` exits 0 as soon as one report lands, so run it again for the rest.
- Two edit agents sharing a cwd see each other's in-progress changes: `spawn` warns (new and reused workers alike) to give each a git worktree (`spawn --cwd <worktree>`), and the worker's composed prompt adds a line telling it to report failures in files it does not own as outside its slice. A `done` report routed through `$TMPDIR/soho/<ws>/reports/` is mirrored back into the state dir (best effort). Linked worktrees share the main checkout's state; briefs for a worker in a worktree should use absolute paths, and `dispatch` warns when a cited relative path exists only in the orchestrator's checkout.
- One agent name is one growing session. Reuse the pane for the next slice instead of closing it. Same slice or subject: reuse it as it is (`--amend` for fix rounds). Another subject in the same project: send `/compact` (Claude Code, Codex, pi, opencode) with `herdr agent prompt <name> "/compact"` while it is idle, read the pane until the CLI confirms it (Codex prints `Context compacted`), then dispatch. An unrelated subject: clear the session the same way (`/new` in Codex and pi, `/clear` in Claude Code). A CLI without such a command: `release --close` and spawn again. Close a worker with `release <name> --close` only when the session will not use it again (only panes this skill created close). A name still live in Herdr, even after a plain `release`, makes the spawn choose a suffix. A report covers only the current brief and its explicit amendments.
- `release --close` ends the agent; without `--close` it keeps running.

## Configuration

`key=value` files layered as skill defaults → `~/.config/soho/config` (user) → `<repo>/.agents/soho.conf` (project) → `<state>/session.conf` (this Herdr workspace, via `session set`, never versioned) → `SOHO_<KEY>` → flags. `soho config` shows the effective values and where each came from (scalar keys, then the dotted `args.*`/`role.*`/`model.*`/`effort.*`/`context_window.*`/`lane.*` keys in the file's own spelling). `brief_lint_aliases` sets alternate brief section headings. Projects and machines set up under the former name `herdr-soho` keep working during the compatibility window: the CLI reads `HERDR_SOHO_*`, the `herdr-soho` config files and the `.herdr-soho/` state directory while the new names are absent, and `setup` migrates the instruction blocks and hooks the old skill wrote (see the README's "Renamed from herdr-soho" section). For a third-party fork, `setup --plan --local` previews a local `CLAUDE.local.md` block and Claude hooks; `setup --local` writes them. Local setup keeps the block and state directory out of Git through `info/exclude`, leaving tracked upstream instructions and `.gitignore` untouched. `doctor` recognizes the local block. The hooks go to `.claude/settings.json`, which local setup neither excludes nor checks for tracking; use `--no-hooks` when that file must remain untouched. Repeat `--local` on later setup calls, or run `session set setup_target local` after the first local setup. A project config can hold the key only if the fork already ignores that file. Claude Code reads `CLAUDE.local.md`; other harnesses need their own local instruction route. In a new fork, run local setup before the first `init` if the state directory is not already ignored; `init` can otherwise add it to the tracked `.gitignore`. Kind and model travel together per layer: a lane or role model from a layer below the layer that set the effective kind is discarded, and `--kind` without `--model` discards every configured lane/role model; the role file's own `model` is discarded too when the kind comes from a config layer or a flag. `doctor` warns about a discarded model and about a kind+model pair that does not resolve against the kind's model list; a lane with a model but no kind is checked against the kind of each of its roles. `doctor --fix [--panes 2|3|4] [--user|--session]` normalizes the project, user or session file (`--session` exits 2 outside a resolvable workspace). The team shape lives in the same place: `panes` (2|3|4), `lanes` (on|off), `pane_mode` (`strict|flex`) and per-lane keys (`lane.<name>.roles|kind|model|effort|approvals|panes|args`). Two more keys carry native args to a subset of the workers: `role.<role>.args` (with `lanes=off`) and `lane.<name>.args` (every worker of the lane; a lane session is shared by every role in it, so the per-role key never applies inside one). They are appended after `args.<kind>` (the kind-wide native args), before the native args given after `--` to `spawn`. A worker keeps the args it opened with: after a change, an idle worker started with other args is not reused. Resource pressure: `pressure_disk_free_percent` (default 15) and `pressure_swap_percent` (default 80) make `spawn`, `wait` and `status` warn one stderr line when the state dir's disk is below the free limit or the swap use passes the used limit (Windows measures the disk, not the swap; `0` turns each one off, and a measurement failure stays silent). The harder, spawn-only limits `pressure_refuse_disk_free_percent` (default 5) and `pressure_refuse_swap_percent` (default 95, `0` turns each one off) make `spawn` — and `run`/`reopen` when they would open a new pane — refuse to open one: before any herdr call that creates a pane or agent, with exit 25, no new roster line and no state written; `--force` skips only the refusal (the warning line still goes out), idle reuse is never refused, and a given `--pane` creates no pane, so it is never refused. Typical project file:

```ini
layout=split
approvals=full
auto_approve=off
reuse_workers=on
notify=on
```

## Assistant and model choice

Which assistant does which work is a per-user or per-project choice in the configuration, not a policy: the guided setup asks, offering only assistants whose probe answers, and there is no fixed ranking of providers. Models track the latest release: a value is an exact id, a CLI alias, or a regex over the ids the CLI lists that resolves to the **newest** match at spawn time (shipped defaults: `model.claude.orchestrator=fable`, `model.claude.worker=opus`, `model.codex.worker=sol|gpt-5`, `model.cursor.worker=grok|muse`, `model.agy.worker=gemini|opus`). Projects override any of it, per kind or per role (`role.reviewer.model=…`, `role.reviewer.effort=…`, `role.reviewer.kind=…`), in `.agents/soho.conf`.

The one rule that does not move: the reviewer of a slice comes from **another model family** than its implementer (`dispatch` enforces it for workers, exit 5; for code the orchestrator wrote, pass `--for <its family>` so the check compares with it). For `cursor`, `pi` and `opencode` the family comes from the model id. The generic kinds (`pi`, `opencode`) ship no default model: set a `provider/id` yourself in the user or project file; with none set, the CLI uses its own default (`skills/soho/references/kinds.md`).

Each worker's MCP servers are the user's by default (`mcp=inherit`); `mcp=none` runs the worker without them, which keeps the worker process tree small (a reviewer rarely needs a browser or design server). The value comes from the `--mcp inherit|none` flag of `spawn`/`run`, `lane.<name>.mcp`, `role.<role>.mcp` or the role frontmatter `mcp:`, in that order of strength, defaulting to `inherit`. `none` translates to the kind's per-process flag, inserted right after the effort args and before the configured and user args: claude `--strict-mcp-config`, pi `--no-mcp`, and for codex one `-c mcp_servers.<name>.enabled=false` pair per enabled server — the spawn runs `codex mcp list --json` in its own PATH and env (10 s timeout, reading only `name` and `enabled`; the transport command lines may carry credentials) and the flags follow the listing order; when that listing fails (codex not found, timeout, nonzero exit, non-JSON output) the spawn keeps the user's servers and prints `soho: warning: spawn: codex could not list its MCP servers (<reason>); '<agent>' keeps the user's MCP servers`, and zero enabled servers change nothing. The other kinds print a one-line warning and keep the user's MCP servers. The effective choice is the last field of the worker's roster row; `reopen` forwards it as `--mcp <value>` so a worker keeps the MCP it was opened with (a row without the field passes no `--mcp` and the normal resolution decides). Reuse requires the same choice: an idle worker opened with the other value is not reused (`lanes=off` opens a new worker; a lane opens a new pane when it has a slot, else exits 13 with `spawn: lane '<lane>' has no idle worker matching MCP '<value>' (idle: '<agent>' was opened with MCP '<other>'); release it or raise the lane's panes`), and a row without the field matches any request. The reviewer, implementer, tasker, scouter, researcher, documenter and security-reviewer roles ship with `mcp: none`.

## Token budget

`worker_context=lean` stops workers from reading `CLAUDE.md`/`AGENTS.md`, memory and skills before the task (Codex: `project_doc_max_bytes=0`); the brief must quote the rules that apply. `effort.<kind>` spends budget where there is headroom (shipped: `effort.grok=xhigh`, `effort.cursor=xhigh`). `context_window.grok` opens new grok spawns with that context window (`500k` or a token count like `256000`): the spawn types `/context-window <value>` into the fresh grok and confirms the `Context window set to <value>` line (a spawn that cannot confirm is warned, not failed); only grok uses the key. `reuse_workers=on` avoids paying the startup cost again for the same role.

## Approvals without a human

`--approvals full` maps to each CLI's non-interactive flags. When a dialog still appears, `auto_approve=on` in the config answers it with the CLI's default "yes" and keeps waiting (bounded by `max_auto_approvals`, every answer logged). The same dialog a third time in a row is left blocked (`auto_approve: the same dialog came back 3 times for '<agent>'; leaving it blocked`) and the `blocked` JSON line carries the dialog in `dialog`. A **question** — a decision prompt — is never answered for the worker: the wait reports `question` (exit 7) and a person decides. Off by default: a blocked worker is reported and a person decides.

## Guard rails

Lessons from real runs are enforced by the script, not just documented: `release --close` refuses to kill a worker mid-task, `dispatch` lints the brief structure (read-only roles need no `Owned files`), the reviewer family check is strict by default, and every error or warning lands in `soho friction` for review at the end of a run. The brief lint names the reason of every missing section in the warning (`Goal` — the worker does not know what the slice is for; `Expected result` — nothing says when the slice is done; `Owned files` — workers without owned files collide; `Forbidden` — nothing keeps the worker out of other files; `Report` — the worker may never write one; the no-commit line — the worker may commit or push) and flags the empty-inline-code symptom (`brief_lint=warn|strict`; `off` silences it). `brief_lint_aliases` (`Section=Heading|Heading` items) lets an alternate heading prefix satisfy a section. Built-in prefixes also accept Portuguese headings: Goal (`Objetivo`, `Meta`), Expected result (`Resultado esperado`, `Critérios de aceitação`, `Critérios de aceite`, `Pronto quando`), Owned files (`Arquivos`, `Escopo`), Forbidden (`Proibido`, `Fora do escopo`, `Restrições`) and Report (`Relatório`), with case- and accent-insensitive matching. Portuguese built-in headings count only when, after the prefix and leading spaces, the title ends or continues with a non-letter, non-digit; English headings and configured aliases keep simple prefix matching. In `Owned files`, a line whose text starts after any list marker and leading spaces or `*`/`_` emphasis with `nenhum`, `nenhuma`, `não`, `nunca`, `exceto`, `not`, `never`, `none`, `except`, `excluding` or `outside` contributes no paths; a negation later in the line does not exclude its paths. `friction add "<text>" [--brief <path>]` records an observed friction the tools do not log themselves; every line of the log keeps its four columns (date, level, command, message).

## Waiting for workers

Without an assignment, the report file is the completion signal. `dispatch` waits for it by default; `wait a b c` blocks on several; `status a b c` is the non-blocking check (`status` with no names checks the registered team); `roster` shows a `REPORT` column and the worker's current task (`TASK`, the pane title text, `-` when none, cut to 40 characters). Do not poll Herdr agent states by hand: they flicker `idle`/`done` mid-task. Because the report's existence ends the wait, every prompt tells the worker that only it writes the report, after all of the brief is done, and never a subagent it started.

The report file is the completion signal **of the workflow without an assignment.** While a participant has an active collaboration, `status`, `wait` and `collect` report `collaborating` (with the assignment, phase, round, revision and last delivery) before the usual state — including a stale complete report, which stays an artifact until the orchestrator finalizes. `collaboration-unavailable` and `collaboration-disabled` (exit 4) make an unreadable registration or a non-`policy` mode visible instead of the report's `done`.

`wait` prints one JSON line per agent and distinguishes why a report has not landed:

- `blocked` (exit 7): the worker stopped on an approval dialog.
- `question` (exit 7): the worker asked a question. It is never answered for the worker — read the pane and ask the user.
- `provider-error` (exit 14): the worker's model provider is down or rejects authentication (for example `Request timed out`, `503: {…}`, `401`, `Incorrect API key`, or a revoked refresh token). The JSON `cause` is sanitized. A terminal authentication failure is reported on the first `wait` or `status` observation and after `spawn` starts the CLI. A received `dispatch --no-wait` prompt can report the same failure immediately while the current report is missing; a completed report takes precedence. If the same auth cause was already visible before dispatch, `--no-wait` leaves it unattributed even if its line reappears: redraw or replay can mimic a new failure. Use `wait` or `status` to inspect the worker.
- `capacity` (exit 14): the provider refused because it was full (for example an error naming `capacity` or `overload`, or status 529). The wait first sends the worker "continue" up to `provider_retries` times, `provider_retry_delay` seconds apart, and reports `capacity` only when that did not help.
- `not-received` (exit 15): receipt was not proved. Only the exact path in a recognized composer authorizes one dispatch Enter or up to three bounded later wait attempts. A text resend requires a readable, recognized empty composer and no current-path evidence; history, unknown layout, another draft or failed reads leave delivery uncertain. Read the pane before retrying.
- `queued` (`dispatch --no-wait`, exit 0): the worker is already `working` and a genuine anchored prompt or queue line identifies this dispatch's exact composed basename or radical. Generic chrome, longer filenames and prose quotations do not count. No key is sent while it remains working; a later `wait` checks quota/provider/dialog outcomes first and permits a bounded Enter retry only when the shared provider classifier recognizes the actual composer holding this exact path.

The other outcomes: `quota` (exit 11, the account's quota is out), `settled-no-report` or `gone` (exit 6), `unavailable` (exit 4 — restore access and retry; never spawn a replacement), `timeout` (exit 9 — not a failure: the role's effective timeout expired while the worker may still be working; the line carries `elapsed_ms` and the last probed `state`, and a friction line suggests re-running with twice the timeout; run `wait` again). When several agents finish in one `wait`, the exit is the most severe of 4, 11, 14, 15, 7 and 6.

A `done` report that still marks items `[partial]` is not a pass (every prompt asks for each item's state as `[done]`, `[partial]` or `[skipped]`): the JSON line gains `partial: N` (only when N > 0, after `report`) and the wait warns `report of '<agent>' marks N item(s) partial: a partial item is not a pass; read them before commit, push or release` (once per report: a second `wait` on the same report warns nothing, a new report warns again). The `dispatch` JSON carries the same `partial: N` right after `report_exists` (after `amend`, when present).

Review reports open with the fixed first line `findings: N (P0 a, P1 b, P2 c, P3 d) | verdict: pass|fail` (English whatever the report language; `fail` when a P0 or P1 remains): a `done` JSON line of such a report gains `verdict`, `findings` and `severity` (after `report`, before `partial`), and the `dispatch` JSON carries the same fields right after `report_exists`. The wait warns on a review report without the header and on a header whose P0..P3 sum differs from `findings` (the numbers are kept as parsed, never recomputed). The review roles tell the worker to run a test before calling it wrong, and to say so when it cannot run it — reading the code is not proof that a test fails.

The `dispatch` JSON is one line with `wait_status` first (`dispatch … | tail -1` returns the whole JSON). When an amendment re-pointed the report mid-wait, it gains `settled_report` (right after `report`), and `report_exists` qualifies that report. Before the send, `dispatch` warns when the new brief owns files another worker is still editing (advisory, up to five paths). A codex worker's composed prompt carries the sandbox notes — no writing under `.git`, no network (local ports included) — unless its opening args grant the access; the network note tells the worker to still write the integration tests the brief asks for — marked `[partial]`, with a test seam when the code depends on a fixed value. An `unavailable` that is a `herdr agent get` killed by a signal (exit ≥ 128, e.g. 137 under load) is retried (after 1 s, then 2 s) before counting; the cause then names the signal (`herdr agent get was killed (exit <rc>, <signal>: memory pressure or an external kill)`).

## Reusing workers

`reuse_workers=on` (the default, or `spawn --reuse`) hands back an idle worker whose last report is already written instead of opening a new pane. With lanes on (the default) the lane's idle worker is reused even when the next brief is another role of the same lane; a lane never mixes CLIs (a kind mismatch exits 13: `release --close` it and set `lane.<name>.kind`). Cheaper and keeps the worker's context; before an unrelated slice, compact or clear the reused session (see "One agent name is one growing session" under "Good to know") rather than passing `--fresh`, which opens another pane.

## Feeding improvements back

When the skill itself causes friction, the orchestrator files an issue on `feedback_repo` (the skill's own repository) using `templates/issue.md`, with the scenario, the exact error, the environment (`soho env`) and the effective config. `feedback=ask|on|off` decides whether it asks first.

With `feedback=local`, a maintainer of the skill works on the same machine, and no issue is filed. `soho feedback send <report.md> "<summary>"` saves the report in `feedback_dir` as `from-<project>-<date>.md` (never over an existing file). When `feedback_to` names a pane or an agent, that maintainer also gets one line with the summary and the path.

## Orchestrator responsibilities

The skill enforces the transport and the report contract. The orchestrator still has to: ask direction before planning, keep slices on disjoint files, deliver shared resources ready in the brief, run the full gates once at integration, review with a different family before push, and say only what was proved. The full contract is in `skills/soho/references/orchestration-contract.md`, and the worker collaboration cycle (policy, assignment, events, finalize and recovery) is in `skills/soho/references/orchestration.md`.

## Which assistant for which role

`skills/soho/references/agent-profiles.md` records what each assistant did well and badly in each role in real use (speed, rounds back, false positives, and what the sandbox kept it from proving), with a recommendation per role. The orchestrator reads it when it proposes a team in the guided setup, and when a slice falls on a known weak spot of the configured assistant. It is evidence, not a benchmark: check it against `soho stats` in your own project.

## When something goes wrong

`skills/soho/references/troubleshooting.md` lists every failure seen while validating the skill (sandbox denials — including `.git` and network — premature `done`, Cursor model syntax, startup dialogs, a codex CLI that updates itself at start, focus, command guards, stale roster lines, an `auto_approve` dialog that loops, a worker stuck without a report, the skill's own exit 137, `wait` timing out while the worker is still working, and the worktree/mutation isolation rules) with the fix and the validation procedure for a new kind.

## Codex

When Codex runs inside a Herdr pane, its default shell environment policy (`[shell_environment_policy] inherit = "core"`) drops all `HERDR_*` environment variables (`HERDR_ENV`, `HERDR_PANE_ID`, etc.) from child commands it runs. As a result, `soho` cannot see that it is running inside Herdr.

The recommended fix preserves what `core` already passes and adds `HERDR_*`:

```toml
[shell_environment_policy]
inherit = "all"
include_only = ["HOME", "LANG", "LOGNAME", "PATH", "SHELL", "USER", "TMPDIR", "HERDR_*"]
```

Every key defined under `[shell_environment_policy.set]` must also be added to `include_only` (because `include_only` is applied after `set`). After saving the changes in `~/.codex/config.toml` (or `${CODEX_HOME}/config.toml`), restart Codex for the changes to take effect. Run `soho doctor` to verify.

## Requirements

- Herdr ≥ 0.9 with the agent CLIs you intend to use installed and detected (`herdr agent` lists the kinds).
- The native `soho` binary on the Herdr server PATH. Go 1.25+ is required only for source builds, development and the evaluator.
- `HERDR_ENV=1` in the calling pane.

## Caller promotion and recovery

`soho promote` explicitly promotes the calling registered `sub-orchestrator`, with no target or bypass flag. It resolves the native caller pane/workspace/project, verifies the complete roster row, and refuses active collaboration, unresolved assigned work, present registered copies, running processes or unreadable ownership state. A working caller is allowed. It checks those obligations again under the roster lock before removing exactly its worker row, then applies the configured unique orchestrator name when needed and preserves the task objective in the pane title. It never releases, closes, moves, focuses or kills a pane or resource, and does not create a released orphan. Return codes: 0 promoted/already promoted, 2 usage/outside Herdr/read-only refusal, 3 open obligations or invalid role/report, 4 unreadable/stale/corrupt scope, 6 row removed but rename/title incomplete (retry the command). The roster lock does not synchronize other resource registries: finish or hand off concurrent registration work before promotion.

`regrid` protects the native calling pane even if its roster row still says `sub-orchestrator`: that caller is excluded from worker capacity and is never parked, moved or closed. Other sub-orchestrators continue to count as workers. It resolves actual caller context before changing topology, retains current worker IDs after successful moves, relists panes after rebuilding the caller grid, and performs bounded recovery after a partial failure. An unsuccessful recovery preserves live workers and reports where they remain; it does not close them to hide the failure.

`procs add <pid>` reads identity and liveness in one native snapshot. A proven exited process is refused with exit 2; an unreadable identity that the kernel has not proved absent is refused with exit 4 and is not registered. Unix modifier states such as `Ss`, `S+` and `SN` preserve their exact start time; zombie states with modifiers are gone. A failed `ps` result, including partial output, cannot prove an existing process gone. After a failed ps read, only kernel-confirmed absence permits cleanup; Windows uses its existing native process handles and creation-time checks.
