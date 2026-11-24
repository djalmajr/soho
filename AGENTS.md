# soho

The distributable skill lives in `skills/soho/` and the optional Herdr plugin in `plugin/`.

- Use `soho` for the skill, CLI and plugin. Runtime support for `HERDR_SOHO_*`, old `herdr-soho` config/state paths, `[herdr-soho:peer]` and `djalmajr.herdr-soho` has been retired. `soho setup` migrates them and `soho doctor` flags leftovers. Publish only the `soho` skill.
- Plugin entrypoints must reuse the CLI's report, dispatch, model-family metadata and configuration rules. Before mutating state, verify the workspace, repository root and pane. Plugin actions can follow Herdr focus even when the shell's `HERDR_*` variables point elsewhere.
- Keep the skill, plugin and docs portable. Exclude machine paths, private providers, credentials and project-specific examples. Write docs in English with one physical line per prose paragraph or list item. Preserve Markdown structure and intentional breaks.
- Use Go for runtime, tests and tooling. Node.js, Bun, Bash and other interpreter fallbacks are prohibited. External agent CLI dependencies are exempt. Run focused tests while editing, then `gofmt`, `go vet ./...`, `go test ./...` and `go test -race -timeout=30m ./...` before shipping. Keep individual timeout and lifecycle assertions at their original bounds. Verify affected install, process and terminal contracts on macOS, Linux and Windows with the retired runtimes absent. Cross-compilation gives build evidence only. Assert consumer exit codes, machine-readable fields, table columns, errors and side effects directly. Never derive whole expected output from the implementation to clear a failure.

<!-- soho:start -->
## Orchestration

Load `/soho` before planning non-trivial work inside Herdr (`HERDR_ENV=1` on legacy TUI, `SOHO_TRANSPORT=hc` on native Core). The calling agent plans, briefs, dispatches, collects reports, integrates, runs gates and owns Git. Workers never commit, push or open PRs. `spawn planner` opens no pane.

- Delegate research spanning several files, repositories or tools, parallel or multi-file slices, UI under a design contract, auth, secrets, input handling and work requiring review. Handle questions, quick checks and small docs or config changes directly. Every code slice, including the orchestrator's, gets review before push. For UI, read root `DESIGN.md` and the affected spec under `design/` when present, then run its design validation.
- Write one brief per slice with its goal, result, acceptance commands, settled decisions, owned and forbidden files, and report format. Workers report open questions for the next brief and may not invent names, flags, endpoints, credentials or requirements.
- Use the report file as completion evidence through `soho dispatch`, `wait` and `status`. Use `wait` for a busy worker before dispatching more work to it. Quota exit 11 stops that worker. Ask the owner before switching assistants, waiting, taking over the slice or pausing.
- On legacy TUI, spawn workers in sibling panes. On Core, spawn them as child terminals without panels. Run builds, tests and scripts as child programs with `hc run --bg --outlive-parent --name <project>-<role> -- <program> <args>` so their exit notes wake the orchestrator. Workers stay alive after reporting. Use `dispatch --no-wait`, arm `soho wait <agent> --wake`, then end the turn or continue independent work. On a done note, run `soho wait <agent>` once. On checkpoint or timeout (exit 9), rearm. Keep foreground waits and polling loops out of the Core workflow.
- Check effective panes, roles, assistant, model and effort with `soho config show` and `soho explain`. Settings come from `.agents/soho.conf`, user config and the session layer. Project role files in `.agents/soho-roles/` override shipped roles. Ignored scratch state lives in `.soho/`.
- Treat `[soho:peer:<id>]` on legacy TUI and `<hc from="…">` on Core as team messages within your scope. Reply without a tag or signature using `soho send <ref> …` or `hc send <terminal> …`. Owner approval still governs commits, push, merge, releases, deploys, production actions and data deletion wherever required.
<!-- soho:end -->

Preview setup from the project root with `soho setup --plan --target AGENTS.md`. Applying setup to this file regenerates the marked section and refreshes hooks. Keep project-specific rules outside the markers.
