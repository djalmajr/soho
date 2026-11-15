# Portable Core readiness

This reference is a portable checklist for deciding whether native Herdr Core execution is genuinely available in the current environment; it does not replace project skill guidance or Go contracts.

## Scope

Portable readiness only covers local discovery: native identity, working directory, process ancestry, installed CLI requirements, and shell environment policy.

UI presentation and brief grammar stay in skill guidance; atomic guarded reuse, model selection, and report contracts stay in Go.

This file assigns no identity values, prescribes no universal replacement allowlist, relaxes no caller guards, and edits no global configuration.

## Native identity, working directory, and process ancestry

Treat native execution as available only when all three checks agree: verified caller identity, current working directory from a bounded caller projection, and process ancestry consistent with the claimed terminal.

Discover identity values from the environment and discovery commands; never assign, export, or invent them for documentation examples.

Refuse missing or ambiguous joins before any mutation; never fall back to focused-window context, stored folders, or launch-directory inference.

| Check | What it establishes | When it runs | Gap it leaves | Write before it |
|---|---|---|---|---|
| Caller identity discovery | Whether the caller runs under a genuine native terminal address | Before any layout, spawn, or release action | A matching name alone does not prove a live terminal | None; discovery is read-only |
| Working-directory projection | The actual project directory for the verified caller | Before hierarchy resolution and placement | Projection alone does not prove UI visibility | None; discovery is read-only |
| Process-ancestry consistency | Whether the caller process descends from the claimed terminal context | Before trusting identity and directory together | Ancestry alone does not authorize mutations | None; discovery is read-only |

## Runner-specific flags

Some installed runners require extra flags only when the installed CLI requires them; check the current installed CLI help before adding any flag.

Use Codex `--no-daemon` only when required by the installed CLI; never add it unconditionally and never document it as a universal runner option.

## Shell environment policy

When genuinely inherited HC context is lost because the selected runner's filters omit its names, an authorized process-local filter amendment may add exactly `HC_TERMINAL_ID`, `HC_SESSION`, and `HC_SOCKET` names while preserving all existing patterns including `HERDR_*`; this amendment never copies values, never replaces the full allowlist, never edits global configuration, and never claims every policy loses HC variables.

## Transport preference

Preserve both legacy TUI/herdr and native HC support in the same workflow; prefer HC only when genuinely available under the checks above.

When native checks fail or installed commands are absent, stop and report the readiness failure rather than simulating native context with fake environment variables; continue on the legacy path only after independently verified genuine legacy readiness, explicit transport selection, and already-authorized runtime scope, with no new approval requirement for already-authorized work.

## OpenCode profiles and model syntax

Native HC OpenCode uses the canonical primary profile as current candidate functionality, not as an automatically installed release; verify actual support from the actual selected Soho executable and candidate native profile result, since external OpenCode model syntax alone does not prove selected Soho support, as described in [transport detection and readiness](../SKILL.md#transport-detection-and-readiness).

Variant syntax such as `provider/model#variant` is optional and model-dependent rather than universal; do not promise that every model supports every variant, and do not promise wire-effort savings, universal MCP disablement, or arbitrary-code isolation.

## Pi runtime note

Pi needs its own external Node dependency; that dependency is not a Soho runtime fallback and does not make Pi available when Node is absent.

## UI layout authority

Native user UI layout guidance in [herdr-ui.md](herdr-ui.md) stays intact and remains authoritative for windows, spaces, tabs, and panes.

This file invents no rename or regrid APIs and documents no terminal-level or pane-level title command beyond what `herdr-ui.md` already states.

## Test layers spelling

Use the exact canonical spellings `NOT RUN: reason` and `n/a: reason` in status lines and examples; do not write `N/A` or `not_run`.

## Open questions

Gaps the portable copy cannot decide stay open for integration: exact Go enforcement points, complete variable inventories, and runner flag defaults must be verified against the integration source before they become normative claims.
