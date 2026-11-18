# Architecture

The skill, native Go CLI and optional Herdr plugin are distributed together so their contracts can be reviewed and released as one change. `plugin/herdr-plugin.toml` declares Herdr actions; their implementation is Go under `internal/plugin`, calling the shared CLI rules. There is no JavaScript engine, shell launcher or runtime fallback.

## Responsibilities

| Layer | Owns |
|---|---|
| Agent skill | When to delegate, role instructions, brief and report contracts, review and integration guidance |
| CLI | Configuration, role and model resolution, pane orchestration, prompt delivery, report-based completion, collection, and statistics |
| Plugin | Herdr menu actions, terminal UI, context selection, event-driven presentation, and shortcuts |

The CLI is the programmatic interface for agents and scripts. Plugin commands invoke it with an explicit workspace context; they do not copy its state machine or report parsing. The plugin is optional and its installation is user-wide, while CLI state is scoped to the current project and Herdr workspace.

## Boundary for new features

- Keep an operation in the CLI when it takes dynamic arguments, must work without the plugin, or defines an observable workflow contract. `spawn`, `dispatch`, `wait`, `collect`, and `setup` meet this test.
- Expose an existing CLI operation in the plugin when Herdr context or a terminal interface makes it easier to use. `doctor`, `roster`, and later interactive collection or layout actions fit here.
- Put a feature only in the plugin when it exists solely to customize Herdr's interface, such as a keybinding, Agent view, popup, or link handler.

Herdr plugin actions and panes are declared in a manifest. An interactive terminal pane can gather runtime choices; a fixed action cannot receive arbitrary CLI arguments. Plugin actions receive the focused Herdr context, which can differ from the invoking shell's `HERDR_*` variables. Any action that mutates project state must show the resolved target and confirm it first.

The report file remains the completion signal for a delegated task. Herdr agent status and events can prompt a check, but do not replace that contract.

## Native distribution and tests

`cmd/soho` is the shared executable. `skills/assets.go` embeds only resources and publishes an exact digest bundle in an explicit env-derived cache when no installed resource tree exists. Cache publication uses owned staging, verified contents and a filesystem boundary; corrupt or foreign published data is refused. NOWRITE and read-only lookup never materialize resources. `SOHO_BIN` may select an absolute native executable for worker dispatch; invalid overrides are refused before worker compact or prompt effects.

`internal/install` owns bounded downloads, checksum verification and safe file replacement. The CLI owns explicit-env destination defaults and PATH advice. `cmd/soho-release` builds six targets and generates or validates checksums; its explicit publish operation invokes the native GitHub CLI with argument arrays. The workflow uses thin native PowerShell bootstrap around git/go/gh, without Node actions or Bash logic. External agent CLIs remain separate products with their own dependencies.

Native Go tests use executable Go fixtures and frozen historical JSON oracles. The evaluation command prepares manifested worker copies and runs hidden Go probes; result measurement and scope checks share one native implementation. Formatting, vet, tests and race gates are complemented by native platform proof for process, locking, installation and terminal contracts.
