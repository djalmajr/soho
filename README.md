# soho

The standalone home of the `soho` skill and plugin. Role-based coding agents run in [Herdr](https://herdr.dev). The calling agent orchestrates workers in Herdr panes, sends self-contained briefs, waits for file-based reports, and owns integration and Git operations.

The name *soho* comes from **SO**ftware **HO**use: the calling agent runs a small software house of role agents — builders, reviewers, researchers — and keeps the planning, integration and release work to itself.

The repository contains:

- [`skills/soho/`](skills/soho/) — the agent skill, roles, templates and resources embedded in the native Go binary.
- [`plugin/`](plugin/) — an optional local Herdr plugin that exposes workspace-scoped actions (`doctor`, `roster`, `team`) through the same CLI, a unified session picker (`pick`) with filters, table/tree views and a read-only team view (`i`) of the selected local workspace. The read-only actions open the team panel, which inspects the focused project with the CLI running in `SOHO_NOWRITE=1`; only the panel's `x` and `g` confirmations run a write (a release or `gc --yes`), never without the on-screen confirmation.
- [`docs/guide.md`](docs/guide.md) — usage and configuration guide.

## Install the binary and skill

The CLI and optional plugin use one native Go implementation. They require Herdr and the external agent CLIs you choose; this project has no Node.js, Bun or Bash runtime dependency. A released binary includes the skill's roles, defaults, references and templates. If no explicit or installed skill tree exists, it materializes a verified bundle in the user cache. Read-only mode only uses an already published bundle and never creates one.

Choose `soho_<os>_<arch>` (`.exe` on Windows) on the [releases](https://github.com/djalmajr/soho/releases) page. Supported targets are macOS, Linux and Windows, each for amd64 and arm64. Download `SHA256SUMS` from the same release and verify the exact artifact before running it. On Linux, use `sha256sum`; on macOS, use `shasum -a 256`; on Windows, use `Get-FileHash -Algorithm SHA256`. Compare the displayed digest with the matching filename in `SHA256SUMS`. On POSIX, make the verified artifact executable with `chmod +x`.

Run the verified download from a temporary directory to install it, supplying the appropriate artifact name for your platform:

```text
./soho_linux_amd64 install --version vX.Y.Z
```

```powershell
.\soho_windows_amd64.exe install --version vX.Y.Z
```

`--dir PATH` overrides `SOHO_INSTALL_DIR`; the default is `HOME/.local/bin` on POSIX or `LOCALAPPDATA/Programs/soho` on Windows. The native installer downloads the platform artifact and its matching `SHA256SUMS`, requires one valid checksum entry, verifies the bytes before replacement and cleans its own temporary files on failure. Without `--version`, it selects the latest stable release; request a prerelease explicitly. It prints PATH advice and does not change shell configuration or the registry. On Windows, when the destination is running (for example, the installed `soho.exe` running its own `install`) and cannot be replaced directly, the installer renames it to `soho.exe.old` (or the first free `soho.exe.old-<n>` when that name is taken), puts the verified binary in its place, prints that move, and moves it back if the new binary cannot be put in place. Next to each backup it writes a `<backup>.soho-install` marker with the backup's sha256; the next install removes only the backups whose marker matches their bytes, once they are no longer running, and never touches a `soho.exe.old*` file without one (a backup whose marker could not be written stays, as the install's output says).

With Go 1.25 or newer, a source checkout is another bootstrap route:

```text
go run ./cmd/soho install --version vX.Y.Z
```

Run `soho env` once to resolve or materialize the embedded skill. To expose the skill to your agent's skill loader, copy the resource directory printed by `env` into that agent's supported skill directory, or keep an existing installed skill tree. `SOHO_SKILL_DIR` selects an explicit resource tree. No second legacy skill is installed.

All commands, including `collaborate`, `send` and `find`, run through the same Go CLI. The [guide](docs/guide.md#worker-collaboration) documents the review cycle and `worker_messages` policy, and the [ephemeral job contract](docs/job-contract.md) specifies the dispatcher-driven `job` family (ephemeral jobs on a machine, with a draft pull request kept by the job supervisor). [VALIDATION.md](VALIDATION.md) describes how a maintainer validates a pull request before merge with a real job run on the exact pull request head.

## Migrating from herdr-soho

`soho` was named `herdr-soho` until `v0.3.0`. No former name is read anymore: not the `HERDR_SOHO_*` variables, the `herdr-soho` configuration files, the `.herdr-soho/` state directory, the `[herdr-soho:peer]` markers nor the `djalmajr.herdr-soho` plugin. Migrate each machine and project once:

1. Install `soho` as described above. The installer leaves an old `herdr-soho` binary alone; remove it from your `PATH` (`soho doctor` shows where it is). On Windows its default directory was `LOCALAPPDATA/Programs/herdr-soho`.
2. Run `soho setup` from each project root, in the mode the project already uses (`--local` or not). Without overwriting anything, it moves:
   - the user configuration directory `herdr-soho` (or the older `herdr-agents`) into `soho`, `teams/` included;
   - `.agents/herdr-soho.conf` (or `.agents/herdr-agents.conf`) to `.agents/soho.conf`;
   - the `.herdr-soho/` state directory to `.soho/`, once no worker is left in its roster;
   - the `<!-- herdr-soho:start -->` block and the `herdr-soho hook …` commands to their `soho` versions;
   - the `djalmajr.herdr-soho` plugin to `djalmajr.soho`: for a release binary it installs the new plugin, points the Herdr shortcuts at `djalmajr.soho.*` (keeping a backup of `config.toml`) and unregisters the old plugin.

   `soho setup --dry-run` lists the moves first. A name that exists on both sides is left in place and reported. Run it before any other `soho` command in that project: until the move, the commands that open the state dir refuse with exit 2, and `doctor` and `send` write nothing under `.soho/`.
3. Rename each `HERDR_SOHO_<NAME>` variable to `SOHO_<NAME>`.
4. Run `soho doctor`; it lists the former files, directories, variables and binary that are still present.

Agents and scripts that called `herdr-soho` call `soho` with the same subcommands, and a job wake hook reads `SOHO_JOB_ID`, `SOHO_JOB_SEQ`, `SOHO_JOB_EVENT` and `SOHO_JOB_IDEMPOTENCY_KEY`.

## Optional Herdr plugin

The plugin supports Herdr 0.9.1+ on Linux, macOS and Windows. Its actions and picker run the `soho` binary (the Go CLI), which must be on the Herdr server's `PATH` — install the verified native binary as described above. If the binary was installed after the server started, restart the server in a shell that sees it.

From a checkout of this repository, link it once:

```sh
herdr plugin link "$PWD/plugin"
herdr plugin action list
```

Or install it from GitHub, for example with the tag of the installed release:

```sh
herdr plugin install djalmajr/soho/plugin --ref <tag>
```

A `djalmajr.herdr-soho` plugin registered before the rename is replaced by `soho setup`; see [Migrating from herdr-soho](#migrating-from-herdr-soho).

The `team`, `roster` and `doctor` actions open the team panel on the workspace currently focused in Herdr (which can differ from the invoking shell's `HERDR_*` variables): `team` and `roster` start on the team view, `doctor` starts on the doctor view. The panel names the resolved workspace and cwd on its first line, and never refreshes by itself (`r` reloads). Its only writes are `x` (release the selected worker) and `g` (`gc --yes`), and only after their on-screen confirmation; every other CLI call runs with `SOHO_NOWRITE=1`.

### Finding a session

The `pick` action ("Sessions: search, copy or focus") opens a centered native popup at 80% of the terminal width and height, including the border. The default scope shows all panes; `f` cycles All, Agents and Terminals while the list is focused. `t` cycles All, Working, Blocked, Idle, Done and Unknown status filters. Both filters combine with text search. The modal starts in Tree, grouped by machine and workspace. `v` switches between Tree and Table (Machine, Workspace, Pane, Agent and Status columns), saving the last choice per user on each machine so it survives reopening across workspaces. Local panes appear first and remote results arrive progressively; failed machines show a status message. The list keeps the selected session visible, adapts to terminal resize, and anchors selection details and controls at the bottom.

With the list focused, `p` sorts pane names inside each workspace, `w` sorts workspace groups inside each machine, and `s` sorts panes by status (blocked, working, idle, done, unknown). Repeating a sort key reverses its direction. Machine groups remain local-first, then by machine label. `Tab` switches between search and list focus; in search, c/p/w/s/v/f/t/r are ordinary text. Search matches reference, name, kind, status, task, workspace and tab labels, cwd and machine case-insensitively. `↑`/`↓` select one session; `PgUp`/`PgDn` move by the visible page and the vertical mouse wheel moves three items per notch. Group headers are never selected. `c` copies the selected reference and keeps the modal open with confirmation in its status bar. `Enter` navigates to the selected pane and focuses it; `Esc` or `Ctrl-C` close. Switching view or sort preserves the selected full session reference. For a remote session, navigation focuses the pane on that machine's server; it does not switch the machine displayed by the local Herdr client. If navigation fails, the popup stays open and shows the cause without changing the clipboard. The copied text contains only the complete machine/pane reference:

```text
windows/w3:p
```

The copy contains no agent metadata, cwd or trailing newline. Tree rows end with the pane ID in parentheses, for example `build · working (w3:p1)`; the machine remains identified by its group heading. Long pane names are shortened to preserve the ID. Copy prefers the platform's native tool (macOS `pbcopy`; Windows PowerShell `Set-Clipboard`; Linux `wl-copy`, then `xclip -selection clipboard`, then `xsel --clipboard --input`) and, when none exists or they fail, writes OSC 52, which Herdr forwards to the user's terminal. The modal reports the clipboard result in its status bar; OSC 52 delivery depends on the terminal's clipboard support. `Ctrl+Enter` remains a compatibility alias for navigation.

To bind the action to a key, add an entry to the Herdr config (any key you like):

```toml
[[keys.command]]
key = "prefix+l" # any key
type = "plugin_action"
command = "djalmajr.soho.pick"
```

The selected session details include its task. Status totals and the last update time appear in the header; the list refreshes every 10 s, with `r` while the list is focused, or with `Ctrl-R` at any time. A selected reference that disappears during refresh is not silently replaced as the target of copy or navigation. Mouse tracking is temporary and its previous settings are restored when the popup exits.

Use one shortcut for `djalmajr.soho.pick`; the separate public `board` action and pane have been removed. Existing `soho plugin board` and `soho plugin bridge board` CLI calls remain compatibility aliases for the same session modal.

### Team panel

The `team`, `roster` and `doctor` actions open the same panel over the focused pane: `soho plugin team` (and `--view doctor` for the doctor view). The first line is always `soho · <workspace> · <cwd>`; `1`–`4` (or `Tab`) switch the views, each reloads on demand (no auto-refresh) and shows the CLI's exit code and stderr when a call fails:

1. **team** (`equipe` on screen) — what the team is doing (`explain`, wrapped) and its workers: the first roster table, one line per worker, `↑`/`↓` select (orchestrator first as the roster gives it). `Enter` opens a scrollable report of the selected worker (`collect <agent> --lines 60`, `↑`/`↓` and `PgUp`/`PgDn` scroll). `x` confirms on the panel (`release --close <agent> in <workspace> (<cwd>)? y/N`) and, with `y`, releases that worker (`--close`) and reloads the view.
2. **doctor** — the output of `soho doctor` for the focused workspace.
3. **resources** (`recursos` on screen) — the `gc` dry-run (pressure). `g` confirms (`gc --yes in <cwd>? y/N`) and, with `y`, runs `gc --yes` and reloads.
4. **friction** — `soho friction --summary`.

A confirmation runs its write only on a `y` pressed on its own after the prompt has been on screen for 0.4 s with no other input: a paste (the panel turns on the terminal's bracketed paste and drops pasted text) or a key typed along with the one that opened it never confirms.

`q` closes the panel from a main view and `Ctrl-C` from any view; `Esc` backs out of a report or a confirmation and closes a main view. Every read call runs in the target's cwd with the target's `HERDR_*` ids and `SOHO_NOWRITE=1`. A focused pane from another workspace, or whose cwd does not exist, shows the cause on one line instead of the panel, and closes with `Esc`.

## Develop locally

Use Go 1.25 or newer. There are no third-party Go module dependencies and no generator step:

```text
go test ./...
go test -race -timeout=30m ./...
go vet ./...
go build ./cmd/soho
go run ./cmd/soho-release build --version dev --dest dist
```

Run focused tests while editing, then the integrated tests, race detector, formatting and vet gates before shipping. The integrated race suite uses a 30-minute package watchdog because native fixture CLIs are re-executed under instrumentation; individual behavior deadlines remain unchanged. The release tool builds six native artifacts plus `SHA256SUMS` into a fresh directory; it refuses an existing destination. Its publish command requires explicit release authority. Cross-compilation verifies compilation only; affected process, locking, installation and terminal behavior also need native target execution. Frozen compatibility JSON under `internal/testdata/legacy` preserves historical oracle bytes without a JavaScript generator. The [evaluation protocol](docs/evaluation-protocol.md) describes the native evaluator.

For configuration, commands, and the report contract, see the [guide](docs/guide.md) and the [skill](skills/soho/SKILL.md). The [architecture](docs/architecture.md) explains the boundary between the CLI and optional plugin.
