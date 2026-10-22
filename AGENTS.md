# soho

This repository contains the distributable skill in `skills/soho/` and an optional Herdr plugin in `plugin/`.

- The skill, CLI command, and plugin identifier are named `soho`. They replace the former `herdr-soho` without a compatibility layer: no former name (`HERDR_SOHO_*` variables, `herdr-soho` config and state paths, `[herdr-soho:peer]` markers, the `djalmajr.herdr-soho` plugin) is read. `soho setup` migrates them and `soho doctor` reports what is left. Never publish a second installable skill for the old name.

- Keep the CLI's report, dispatch, model-family metadata, and configuration rules as the shared implementation. Plugin entrypoints call those rules; they must not maintain a second copy.
- Check the workspace, repository root, and pane context before a plugin action changes state. A plugin invocation can follow the focused Herdr workspace rather than the invoking shell's `HERDR_*` variables.
- Keep skill, plugin, and documentation portable. Do not add machine paths, private provider names, credentials, or project-specific examples.
- Go is the sole project runtime and test/tooling implementation. Do not add Node.js, Bun, Bash or interpreter fallbacks. External agent CLI dependencies are outside this boundary. Run focused tests while editing, then gofmt, `go vet ./...`, `go test ./...` and `go test -race -timeout=30m ./...` before shipping. The integrated race suite has a 30-minute package watchdog; individual timeout and lifecycle assertions retain their original bounds. Validate affected installation/process/terminal contracts natively on macOS, Linux and Windows with the retired runtimes unavailable; cross-compilation is not execution proof. Assert consumer contracts directly: exit codes, machine-readable fields, table columns, error messages and side effects. Never generate whole expected command output from the implementation to turn a failure green.
- Write all documentation in English. Keep each prose paragraph and list item on one physical line; preserve meaningful Markdown structure and intentional line breaks.
