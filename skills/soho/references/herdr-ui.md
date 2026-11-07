# Native Herdr.app and Herdr Core UI organization

This reference defines the supported window, space, tab, and pane organization routines when running Soho on native Herdr Core (`SOHO_TRANSPORT=hc`). It complements `SKILL.md` and uses the current command surface from installed `herdr-app --skill`, `herdr-app commands`, and `hc --skill`.

## Layer separation and authority

`hc` (Herdr Core) executes programs in persistent terminals and tracks agent lifecycle; it does not arrange windows, spaces, or tabs.

`herdr-app` controls the desktop window, spaces, tabs, layout, and visual placement; it does not manage agent orchestration logic.

Your native terminal address is `$HC_TERMINAL_ID` (for example illustrative `t7`); this is an internal terminal address, not a window pane ID (for example illustrative `p12`). Illustrative example IDs are never targets to copy; every actual target comes strictly from discovery.

Do not use fake `HERDR_*` environment variables to simulate legacy Herdr context; native execution checks actual `$HC_TERMINAL_ID`, `SOHO_TRANSPORT=hc`, verified caller identity, and supported installed commands.

## Caller verification and hierarchy discovery

Before initiating window or layout actions, verify the caller's actual environment and locate its exact place in the window tree.

Never assume the currently focused space, tab, or folder belongs to the calling project; the user may have focused another workspace, tab, or window.

The actual `herdr-app ls` structured hierarchy is `spaces[].tabs[].terminals[]`, containing `terminal` string and `pane` string fields; `herdr-app ls` does not provide the caller's working directory.

Establish the caller's actual working directory from a safe bounded `hc` caller projection independently via `hc get $HC_TERMINAL_ID` (reading `cwd` and `foreground.cwd`); never infer current project location from launch cwd or `space.folder`. Never collect or output full raw terminal argv or environment listings.

Join the bounded caller projection with `herdr-app ls` to locate your verified `$HC_TERMINAL_ID` and map it to its containing space ID (`space.id`), tab ID (`tab.id`), and the matched terminal entry's `pane` string. Explicitly refuse missing or ambiguous caller joins before any mutation; there is no nested pane object in the hierarchy and `herdr-app ls` supplies no working directory, so never fall back to focused window context or stored space folders.

Pass explicit string IDs (`--id`, `--space`, `--pane`, `--terminal`) to all `herdr-app do <command>` invocations; never rely on implicit active-window targets.

## Supported command matrix

All window management commands run via `herdr-app do <cmd> [--key value]` or JSON arguments `herdr-app do <cmd> '{"key":"value"}'`.

| Capability | Command | Arguments | Semantics and constraints |
|---|---|---|---|
| Safe background inspect | `hc read` | `<terminal>` | Primary inspection method. Read-only terminal log capture without window placement, focus changes, or UI side effects. |
| Tab rename | `tab.rename` | `id?`, `name?` | Renames the specified tab. Explicit `--id` is required to avoid renaming whichever tab the user is currently viewing. |
| Show terminal | `terminal.show` | `terminal?`, `pane?` | Temporarily shows a worker terminal in place of a verified owned pane: `--terminal <worker> --pane <owned-pane>`. Return to caller by supplying the SAME verified pane ID with no terminal: `--pane <same-owned-pane>`. Never call without arguments. |
| Peek terminal | `terminal.peek` | `terminal` (req) | Shows an overlay peek of a worker terminal over the tab without changing window layout. Takes ONLY `--terminal` (no `--pane`). Requires verified owned visible location and user intent; never use as an automatic background check. |
| Split pane | `pane.split` | `direction` (req), `pane?`, `argv?`, `mod?`, `props?` | Splits target `--pane` `right` or `down`. Use to add sibling worker panes while preserving the caller's requested full-height column. |
| Pin terminal | `terminal.pin` | `terminal` (req), `pinned` (req) | Sets durable cross-client pin flag kept with the terminal record. |
| Reveal hidden worker | `terminal.keep` | `terminal` (req), `place?` | Attaches a pane (`place`: `tab`, `right`, `down`) to an unplaced terminal and enables Core keep loss recovery (replacing a lost program with a shell). Takes NO `--pane` argument; verify placement context before calling. |
| Park pane | `terminal.park` | `terminal` (req) | Detaches the terminal's pane into headless execution and disables Core keep recovery policy. Not a cosmetic toggle. |
| Focus pane/terminal | `pane.focus` / `agent.focus` | `pane?` / `terminal` (req) | Shifts window focus. Screen-local; requires explicit user intent. Do not steal focus during background work. |
| Close pane | `pane.close` | `pane?` | Closes the pane and TERMINATES its running terminal process. Use only on owned completed workers. |
| Close tab | `tab.close` | `id?`, `confirmed?` | Closes the tab and TERMINATES all terminals inside it. Requires explicit confirmation. |

## Terminal and pane naming reality

Current native Herdr.app commands provide NO terminal-level or pane-level rename or title command.

There is NO native counterpart to legacy Herdr automatic `<role>: <task>` or checkmark (`✓`) pane title updates.

Never attempt to invent CLI aliases, synthetic OSC escape sequences, private source patches, or duplicate agents to simulate pane titles.

Worker identity is established at creation time using the unique agent name (for example `soho spawn implementer --name worker-api`).

Tab names represent project or phase scope (for example `api: auth-refactor`), not the fleeting status of an individual worker.

Task completion is proven strictly by the settled report file or assignment status collected by Soho, never by a UI label or terminal status word.

## Inspection, visibility, and lifecycle safety

Default background inspection must always use read-only `hc read <terminal>`. It captures screen and log state without disruption, layout changes, or focus shifts.

`terminal.peek`, `terminal.keep`, `terminal.show`, `pane.focus`, and `agent.focus` are user-facing UI interactions that require the intended owned visible location and explicit user intent. They must never be executed as automatic background actions over whatever project or workspace the user is viewing.

Exact terminal identity alone does not target a UI location; commands like `terminal.peek` accept only `--terminal` and render onto the active tab. If the user is working in another project or window, calling `terminal.peek` displays an overlay on top of that unrelated workspace. If the owned UI context cannot be verified without stealing focus, defer the UI action and inspect via `hc read <terminal>`.

For `terminal.show`, retain the same verified owned pane ID for both show and return: show via `herdr-app do terminal.show --terminal <worker-id> --pane <owned-pane-id>`, and restore the original pane by invoking `herdr-app do terminal.show --pane <same-owned-pane-id>` with no `--terminal`. Never invoke `terminal.show` without arguments, as an argument-free return operates on whichever pane currently holds focus.

Do not invent `--pane` arguments for commands that do not support them: `terminal.peek` takes only `--terminal`, and `terminal.keep` takes only `--terminal` and optional `--place`.

`terminal.keep` and `terminal.park` change Herdr Core process recovery policy, not merely window visibility. Core keep replaces a lost program with a shell; it never resumes or reruns the agent or task, and ordinary exits or explicit kills remain final. Do not claim preservation of the agent process or task context. `terminal.keep` reveals an unplaced terminal by attaching a pane and enables that recovery policy; `terminal.park` detaches the pane into headless execution and disables recovery. Neither command is a cosmetic minimization toggle. No extra user approval flow is required for already-authorized owned cleanup once reports are verified and settled.

Never create a new terminal or worker process solely for presentation or window display.

Closing a pane (`pane.close`) or tab (`tab.close`) terminates all running processes hosted within it. Never close a pane or tab to manage visibility.

Close only verified owned worker terminals after their reports have been verified and settled under standard Soho release protocols (`soho release <worker-name> --close`).

The orchestrator's caller terminal and its requested full-height column must always be kept alive and preserved while adding needed sibling worker panes; never close the caller pane or disrupt its primary working layout.

## Durable versus screen-local state

Durable state persists across window restarts and syncs across clients: space names, tab names, terminal labels (`herdr.space`, `herdr.tab`), terminal pin flags (`terminal.pin`), and Core keep policies.

Screen-local state applies only to the active window instance: focus selection, overlay popups, zoom level, and tab settling (`tab.settle`/`tab.unsettle`).

Do not expect screen-local focus or settle actions to be observable on other client connections or to survive window restarts.

## Standard native UI routine

1. Verify environment: Confirm `SOHO_TRANSPORT=hc` and `$HC_TERMINAL_ID` is set, and verify caller identity and current working directory via bounded `hc get $HC_TERMINAL_ID`.
2. Resolve hierarchy: Run `herdr-app ls` and correlate `$HC_TERMINAL_ID` across `spaces[].tabs[].terminals[]` to discover your project's containing `space.id`, `tab.id`, and the matched terminal entry's caller `pane` string. Explicitly refuse missing or ambiguous caller joins before issuing layout or UI actions; never fall back to focused context or stored folder paths.
3. Set team tab label: Run `herdr-app do tab.rename --id <tab-id> --name "<project>: <phase>"`. Always provide explicit `--id` and preserve user-assigned custom names.
4. Spawn workers and manage visibility: Native Soho spawn (`soho spawn <role> --name <unique-name>`) returns a Core worker terminal and does not by itself prove a desktop pane exists. Inspect the actual returned/registered worker terminal ID and app hierarchy; if the worker is hidden and the team already has user authorization for visible sibling panels, use `terminal.keep` for that same worker only after verifying the intended placement context, then relist to confirm actual location and caller-column preservation. If focus belongs to another project, defer placement rather than assuming terminal identity alone selects the destination. Reuse existing user authorization for the team's visible layout and cleanup without re-prompting for each worker or action, and do not start a duplicate process for display.
5. Default background inspection: Inspect worker progress safely and without disruption using read-only `hc read <worker-term-id>`.
6. User-intended UI inspection: When explicitly requested by the user and the caller's tab is verified visible, use `herdr-app do terminal.show --terminal <worker-term-id> --pane <caller-pane-id>`, then return via `herdr-app do terminal.show --pane <caller-pane-id>`. If visible context cannot be confirmed without stealing focus, defer the UI action.
7. Verify completion: Await report delivery through `soho wait` or `soho dispatch`. The report file remains the single source of truth for completion.
8. Safe cleanup: Run `soho release <worker-name> --close` once verified. This closes only the verified owned worker terminal, preserving the orchestrator and sibling tabs.
