# Pull request validation

This document is the procedure a maintainer follows to validate a pull request before merge by running a real, supported `soho job` end to end on the exact pull request head, and to record the verdict (PASS, FAIL or BLOCKED) with raw evidence. The authority for every `job` command, flag, exit code, field and event is [docs/job-contract.md](docs/job-contract.md); this document adds the validation procedure on top of it and changes no product behavior.

## Purpose and scope

The repository gates (unit tests, race tests, vet, formatting) prove the code in isolation. They do not prove that a binary built from the pull request head can start a job, create a native Herdr workspace, run a real worker, produce a report and release every resource. This validation proves exactly that, once per platform the pull request touches, and nothing more.

It applies to every pull request before merge. A pull request whose change the smoke cannot observe still runs it, as proof that the candidate binary completes a real job; the smoke never replaces a test of the change itself.

## What counts as a real smoke

A real smoke has all of these properties:

- The candidate binary, built from the exact pull request head, runs `job start` for real (not `--dry-run`).
- Herdr creates the native workspace `job-<id>` with its panes.
- A real worker CLI executes the bounded task.
- The job orchestrator writes an actual report.
- The job reaches a durable terminal status in `state.json` and `report.json`.
- The supervisor closes the job workspace.

None of the following is a real smoke: a test double, a fake transport, a fixture CLI, a dry run, or an actor's self-report. Source or a binary from another head is never candidate proof. Do not simulate actors, `gh`, authentication or state, and do not edit files under `jobs/<id>/` by hand.

## Prerequisites

If one of these is unavailable, the verdict is BLOCKED (see Verdict criteria), never PASS.

- Herdr 0.9.1 or newer (0.9.3 or newer recommended on Windows) with a running, reachable server. On Windows `job start` never starts a server and exits 4 when none is reachable.
- `gh auth status` succeeds, and the authenticated account can push to the sandbox repository.
- The machine configuration keys `machine_label`, `job_orgs` (it must list the sandbox organization), `job_repos_root`, and optionally `job_timeout` and `job_blocked_timeout`, exist in the user configuration file `config` inside the `soho` user configuration directory.
- A team file exists in that directory (`teams/<org>/<repo>.conf` or `teams/default.conf`), or the sandbox repository carries `.agents/soho.conf` with at least one `lane.*.roles`. A job never falls back to the built-in pane presets, so without one of these `job start` exits 2.
- The worker CLI named by the team is installed and authenticated, runs without interactive approvals (`approvals=full` in the team file), and will not stop at a first-use workspace trust dialog for a new directory. See [docs/startup-trust.md](docs/startup-trust.md); a job that ends `blocked` at such a dialog is BLOCKED, not PASS.
- Before the first smoke, open the job orchestrator's CLI once, interactively, at the sandbox checkout's root, and accept its trust prompt; the job worktrees below that root inherit the trust and open without the dialog. See [docs/startup-trust.md](docs/startup-trust.md) for the other CLIs. Never grant trust to the home directory, to a parent directory outside the sandbox, or to the product repository.
- `auto_approve` stays off, so that no unclassified dialog is answered automatically. That does not disable launch-time grants: `approvals=full` already grants trust and MCP access at launch in some adapters (for example the Cursor `--trust` and `--approve-mcps` flags and the Claude project MCP enablement), so before launch inspect the server account's inherited project resources and MCP configuration and record the installed adapter's full-mode trust and MCP grants.

### Environment consistency

`job start` creates the job workspace without forwarding environment variables. The supervisor therefore runs with the environment of the Herdr server and reads the user configuration file that environment resolves. `job supervise` finds the job by scanning `<job_repos_root>/*/*` from that configuration, and the other subcommands (`status`, `wait`, `events`, `collect`, `ack`, `cancel`, `close`, `list`) locate the job the same way from the configuration of the shell that runs them.

The configuration that `job start` reads (for example through `XDG_CONFIG_HOME`) must resolve to the same user configuration file as the Herdr server's environment; otherwise the supervisor cannot find the job. Each side also resolves its own absolute state root: `SOHO_DIR` takes precedence over `SOHO_STATE_DIR`, which takes precedence over the `state_dir` setting, and the legacy `HERDR_SOHO_DIR` and `HERDR_SOHO_STATE_DIR` variables are copied when the new ones are absent, so a state override in only one environment is not a valid isolated setup. The machine keys are read only from the user configuration file, with no project or session override. Record the effective user configuration, the machine keys, the team and session configuration and the absolute state root for the invoking shell, the Herdr server environment and the worker environment, with secrets redacted, and stop, as BLOCKED, unless both job readers resolve the same stored job.

## Build provenance

Capture every command in this section raw (see Evidence and how to paste it). Run them in a clean checkout of the pull request head.

```text
git rev-parse HEAD
git rev-parse HEAD^{tree}
git status --porcelain
go version
go build -o <evidence-dir>/soho ./cmd/soho
go version -m <evidence-dir>/soho
```

On Windows the output file is `<evidence-dir>/soho.exe`. Then compute the SHA-256 of the binary with the platform's hash tool and record the value.

Copy the `skills/soho` tree of the same checkout into the binary's directory, so that `SKILL.md`, `roles/` and the rest sit next to `<evidence-dir>/soho`. Then run `<bin> env` and require the line `skill dir: <evidence-dir> (executable)`. Role files resolve in the order: the repository's `.agents/herdr-roles/`, the directory in `SOHO_ROLES`, then the skill. The skill resolves in the order `SOHO_SKILL_DIR`, a skill tree beside the executable (searched in the executable's directory and its parents), the skill installed in the home directory, and the embedded skill. The candidate's tree beside the executable must win in every environment the job uses, not only in the maintainer's shell: the Herdr server launches the supervisor and the workers with its own environment. Record, by name only and never by value, whether `SOHO_SKILL_DIR`, `SOHO_ROLES` or their legacy aliases `HERDR_SOHO_SKILL_DIR` and `HERDR_SOHO_ROLES` (which the CLI maps to the new names) is set in the maintainer's shell and in the Herdr server's environment, and whether the sandbox carries `.agents/herdr-roles/`. Any of them makes the validation BLOCKED until it is removed for the run. Without it, a candidate that adds a role runs against an older installed skill and the job ends `failed` with the motivo `job: cannot ensure the job lane`, because the lane setup rejects the new role as an invalid value.

- `git status --porcelain` must print nothing.
- The pull request head SHA as the forge reports it must equal `git rev-parse HEAD`.
- In the `go version -m` output, `vcs.revision` must equal that head SHA and `vcs.modified` must be `false`.

Every later command runs that binary by its absolute path (called `<bin>` below), never a binary found on `PATH`. `job start` launches the supervisor with the launcher path, which prefers the `SOHO_BIN` override (and its legacy `HERDR_SOHO_BIN` fallback) over the candidate's own executable path. Before invoking the candidate, ensure both are absent or resolve to the verified candidate, and enforce the same executable identity in the Herdr server and worker environments. A provenance mismatch stops the validation as FAIL.

## Sandbox repository and bounded task

Use a dedicated, disposable repository that the maintainer controls, referred to as `<org>/<sandbox-repo>`. Never use the product repository or any production repository. Before any `job start`, preflight the sandbox checkout: it must exist from an SSH clone, and every effective origin fetch and push URL must be recorded and verified against the intended forge repository and base. The supervisor pushes through that origin, while the pull request uses the repository given to it; a reused checkout with a divergent fetch or push URL can receive commits outside the sandbox before the job's own checks run. Stop on any mismatch and record the verdict as BLOCKED with the recorded URLs in the evidence. The effects of a job on it are:

- With `--mode worktree` (the default), the job creates `<checkout>/.worktrees/job-<id>` on branch `job/<id>`, where `<checkout>` is `<job_repos_root>/<org>/<sandbox-repo>`.
- The supervisor pushes `job/<id>` to the sandbox remote even when the job made no commit; the remote branch then points at the base commit.
- A job with at least one commit also opens one draft pull request in the sandbox. Nothing is merged and nothing is marked ready for review.
- The worktree, the local branch and `jobs/<id>/` are kept; none of them is removed automatically.
- Pushes never use `--force`.
- `--mode workspace` is only for jobs that change no code and refuses a dirty or locked checkout. This validation uses the default worktree mode.

The task is deliberately harmless and deterministic: create one text file `smoke/<id>.txt` whose only line is `ok <id>`, and change nothing else. The brief below is an example; every value in angle brackets or marked as an example is a placeholder to replace. The fields are those of the contract's Brief section.

```json
{
  "schema": 1,
  "id": "<id>",
  "origem": { "tipo": "conversa", "ref": "<validation-reference>" },
  "repo": "<org>/<sandbox-repo>",
  "base": "<base-branch>",
  "modo": "worktree",
  "objetivo": "Create and commit the file smoke/<id>.txt whose only line is: ok <id>",
  "aceite": [
    {
      "criterio": "smoke/<id>.txt has a line that is exactly: ok <id>",
      "prova": "git grep --no-index -c -E -e \"^ok <id>$\" -- smoke/<id>.txt"
    },
    {
      "criterio": "smoke/<id>.txt has exactly one line",
      "prova": "git grep --no-index -c -e \"\" -- smoke/<id>.txt"
    }
  ],
  "decisoes": ["commit smoke/<id>.txt in exactly one commit titled: test: add smoke marker <id>"],
  "restricoes": ["change no file other than smoke/<id>.txt"],
  "nao_objetivos": ["any change to code, configuration or documentation"],
  "idioma": "en"
}
```

The `id` in the brief must equal `--id`, and `repo` must equal `--repo`. `job start` takes the active budget from `--timeout` (minutes, 1 to 1440), not from the brief; use a small fixed value (30 in the examples) and never widen it after the job has started. Each `aceite[].prova` command is read-only, and each one prints `smoke/<id>.txt:1` when the file is correct; together they prove a single line equal to `ok <id>`. Run every `prova` on a scratch copy of the expected file with the git the job will use before `job start`: the job orchestrator runs it as written, and a command its tools reject (an unsupported option, for example) leaves the criterion partial and ends the job `failed`. The `job start --dry-run` below also validates and lints the brief, but it does not run the `prova` commands, so adapt the example if either check fails.

Use a fresh `<id>` for every attempt: the id is the idempotency key, and starting the same id again returns the recorded job (or exits 20 for a different brief) instead of running a new one.

## Procedure

Run each command through `<bin>`, and capture each command's stdout, stderr and exit code to separate files in `<evidence-dir>`, with the UTC start and end timestamp of the command (see Evidence and how to paste it). `<evidence-dir>` is outside the repository checkout.

1. Check the engine capabilities of the candidate binary: `<bin> capabilities --json`. It must print `{"schema":1,"worker_collaboration":1,"ephemeral_job":1,"job_events":1,"transport":"herdr"}` (`transport` is `hc` when the configuration selects it).
2. Record the Herdr workspaces before the job: `herdr workspace list`.
3. Validate the start without side effects: `<bin> job start --id <id> --repo <org>/<sandbox-repo> --base <base-branch> --brief <brief-file> --timeout 30 --dry-run`. It must exit 0 and print `"status":"dry_run"`. Do not continue on another exit code; record the cause (a configuration or prerequisite gap is BLOCKED).
4. Start the real job with the same arguments, without `--dry-run`: `<bin> job start --id <id> --repo <org>/<sandbox-repo> --base <base-branch> --brief <brief-file> --timeout 30`. It must exit 0 and print the status line of a `running` job. `--base` may be omitted, in which case the base comes from the brief, then from the remote default branch.
5. Read the state: `<bin> job status --id <id>`.
6. Follow the events, repeating with the last `ultimo_seq` until the state is terminal: `<bin> job events --id <id> --since 0 --wait <ms>`, then `<bin> job events --id <id> --since <ultimo_seq> --wait <ms>`. `--wait` accepts at most 600000 ms.
7. Wait for the terminal state, repeating until the printed `status` is terminal: `<bin> job wait --id <id> --timeout 600000`. The maximum per call is 600000 ms; repeating a read does not widen the job budget. See Observing the run for the exit codes of this command.
8. After the terminal status, read `job events` until the `cleanup` event, and only then collect the report in three forms: `<bin> job collect --id <id>`, `<bin> job collect --id <id> --md` and `<bin> job collect --id <id> --verify`. The release, the garbage collection and the workspace close happen around that event, so a collect taken earlier can describe a job that is still being cleaned up. `limpeza.workspace` is the supervisor's record of the close, not proof that the workspace is gone: step 9 observes that. The last command recomputes the SHA-256 of every `artefatos[].path` of `report.json` and exits 16 when one differs or is missing.
9. Record `herdr workspace list` again after the terminal state, as described in Observing the run.
10. Only when `decisions_pendentes`, as printed by `job status` and `job collect`, is not empty: read each pending decision, record it in the evidence, and run `<bin> job ack --id <id> --upto <seq>` with the highest pending `seq`.
11. Close the job: `<bin> job close --id <id>`. Never use `--force` in a validation run. It exits 24 while a decision is unacknowledged.
12. Record the machine's job counts: `<bin> job list`.
13. Check the sandbox: the file `smoke/<id>.txt` on `origin/job/<id>` has the single line `ok <id>`, and the draft pull request listed in `report.json` under `pr` exists, is a draft and is open. Use the forge's own view or read-only git commands, and record the output.

## Observing the run

Observation is read-only. Never type into, answer a dialog in, or otherwise act inside the job's panes; the dispatcher talks only to the job, and a manual action inside a pane invalidates the validation.

- While the job runs, `herdr workspace list` must show the workspace labeled `job-<id>`. Record its workspace id, and run `herdr pane list --workspace <workspace-id>` to record the panes: the supervisor pane and at least one agent pane, the job orchestrator that the supervisor spawns (its `worker_spawned` event names it). Take this record while the job is `running`; the workspace is closed at the end of the job.
- The events must include `accepted`, `preparing`, `worker_spawned`, `worker_done`, `terminal` and `cleanup`, and their `seq` values must be consecutive integers from 1 with no gaps. The last line of `job events` is always `{"eventos":"fim","ultimo_seq":N,"estado":"<state>"}`. A job that made a commit also has `commit`, `push` and `pr_opened` events.
- The terminal status becomes visible before the supervisor releases the workers, appends `cleanup` and closes the workspace, and `limpeza.workspace` in `report.json` is written before the close happens. After `job wait` returns a terminal status, keep reading `job events` until the `cleanup` event, then run `herdr workspace list` (repeat it for a short while if the workspace is still listed). The workspace `job-<id>` must no longer be listed; the report field alone is not proof.
- `job wait` exits 0 for `done`, 7 for `blocked`, 9 for `timeout`, 19 for `failed` and 21 for `canceled`. It also returns when the job is `blocked`, and it exits 9 when its own `--timeout` bound elapses with the job still running, so read the `status` in the printed line instead of the exit code alone.
- A `blocked` job is not finished. The validation does not answer it; record its events and `motivo`. `job wait` exits 7 for every `blocked` status, whether the reason is quota, a provider error or a dialog, so distinguish the cause using `motivo` and the recorded events, not exit codes. End the attempt with `<bin> job cancel --id <id>`, recording that command like the others. Cancel queues a stop request and returns without waiting for the finalization; it may commit and push unfinished files and create or retain a draft pull request. After cancel, observe the terminal `canceled` state, the `cleanup` event and the actual disappearance of the workspace, and retain all resulting evidence.

## Verdict criteria (PASS, FAIL, BLOCKED)

PASS requires all of these:

- The provenance checks hold.
- `job start` exits 0.
- The workspace `job-<id>` was observed live with the supervisor pane and the job orchestrator's pane.
- The events include `accepted`, `preparing`, `worker_spawned`, `worker_done`, `terminal` and `cleanup`, with consecutive `seq`.
- The terminal `status` is `done` (exit 0) with `partial: false`.
- Every item in `report.json` `itens` that comes from the brief — the objective, the `aceite` criteria and the `decisoes` — has `estado` `done`; an item the job orchestrator adds about its own process may be `skipped` only when its role permits it and the item carries the reason. Any item with `estado` `partial` is FAIL. `limpeza.workspace` is `closed`.
- `report.md` exists.
- `job collect --verify` exits 0.
- The sandbox shows the expected file on `job/<id>` and the draft pull request.
- `herdr workspace list` no longer shows `job-<id>`.
- `job close` exits 0.

FAIL is any of these:

- A terminal status other than `done`, caused by the product.
- A missing report.
- A failed `job collect --verify`.
- A workspace left open after a terminal state.
- A provenance mismatch.
- Any manual action inside the job's panes.

BLOCKED is a prerequisite that is unavailable (no reachable Herdr server, authentication, sandbox, machine or team configuration, quota or provider capacity), or a job that ends `blocked` for a reason outside the product (quota, provider error, an unconfigured first-use dialog), recorded with its evidence. BLOCKED is never PASS and never waives a gate: the pull request stays unvalidated until a PASS is recorded.

## Native platform applicability (macOS, Linux, Windows)

The smoke is native. Cross-compilation is not execution proof, and a run on one platform says nothing about another. Run it on each platform whose installation, process or terminal contract the pull request touches (macOS, Linux, Windows), and record the verdict and the evidence per platform. A platform that cannot be run is BLOCKED for that platform, with the reason.

- Windows: the user configuration directory is `%APPDATA%\soho` unless `XDG_CONFIG_HOME` is set, in which case that variable takes precedence. The default `job_repos_root` is `%USERPROFILE%\repo.git`. A running Herdr server is required, because `job start` never starts one there.
- macOS and Linux: the user configuration directory is `$XDG_CONFIG_HOME/soho` when that variable is set, otherwise `~/.config/soho`. The default `job_repos_root` is `~/repo.git`.

## Evidence and how to paste it

For every command, save stdout, stderr and the exit code to separate files in `<evidence-dir>`, together with the UTC start and end timestamps, for example:

```text
date -u +%Y-%m-%dT%H:%M:%SZ > <evidence-dir>/<step>.start
<command> > <evidence-dir>/<step>.stdout 2> <evidence-dir>/<step>.stderr; echo "exit=$?" > <evidence-dir>/<step>.exit
date -u +%Y-%m-%dT%H:%M:%SZ > <evidence-dir>/<step>.end
```

In PowerShell, read the exit code from `$LASTEXITCODE` and the timestamps from `(Get-Date).ToUniversalTime().ToString('o')`.

In the private validation record:

- Keep the sanitized raw command output, the reports and the events, with their hashes, exclusively here.
- Paste stdout, stderr and exit codes verbatim in fenced code blocks, unedited and untruncated. The only permitted change is redacting secrets, and each redaction is named (what kind of value, and where).
- Include the exact head SHA, the tree hash, the binary SHA-256 and the full `go version -m` output.
- Include the final `report.json` and `report.md` from `job collect`, and the events log.
- Keep a hash index: the SHA-256 of every evidence file, one per line.

In the pull request:

- Publish only the public summary, the issue identifiers and the verdict. Machine names, local paths, hosts, accounts, models and costs stay in the private validation record.

## What this validation does not prove

- It does not replace the repository gates. `gofmt`, `go vet ./...`, `go test ./...` and `go test -race -timeout=30m ./...` still run on the same frozen head, and each must pass.
- It does not replace green CI or a separate review. Only the repository owner-maintainer merges, and only after a review OK on the exact head, green CI and a recorded smoke PASS. A CI failure stops blocking only when it is proven, with the evidence recorded, that it already exists on the base and is unrelated to the pull request; that exception never waives the smoke.
- It proves one bounded task with one team shape on one machine and one platform. It does not prove other team shapes, other worker CLIs, other platforms, blocked-job recovery, cancellation, timeouts or large repositories unless the pull request changes them and the validation exercises them.
- It does not prove the quality of a worker's output beyond the bounded task, nor the behavior of the sandbox forge or of the worker provider.
