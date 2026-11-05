# Design lint

The tool that checks the interface against the project's `DESIGN.md` is chosen by the project's stack, here. The `tester` declares the `design` layer of `VALIDATION.md` with the command this reference names; the role names no stack, and a project that already has its own parity script points the layer at that script instead.

## Tailwind v4 with React, Vue or Svelte

`@shadcn/lint` (current: 0.2.5) — an agent-first linter for Tailwind design systems: it works with the design system the project already has, and does not require shadcn/ui. It runs through ESLint or Oxlint, and the choice depends on the markup: Oxlint reads only the script blocks of Vue and Svelte files, so their templates need ESLint with the framework's parser, or the interface goes unchecked; React works with either. A project that runs Biome adds one of them beside it, by the same rule. Its diagnostics explain what is wrong and suggest the allowed value from the project's components, variants and theme. The rules it ships:

- `no-restyle` — restyling a component with `className`.
- `no-raw-colors` — raw colors such as `bg-pink-500`.
- `no-arbitrary-values` — arbitrary values such as `p-[13px]`.
- `no-inline-styles` — inline styles and `<style>` elements.
- `no-unknown-classes` — classes Tailwind cannot generate, such as `rounded-huge`.
- `require-static-classes` — component classes the linter cannot read, such as a class built from a template expression.

Source: https://github.com/shadcn-ui/lint

## The DESIGN.md file itself

`@google/design.md` (current: 0.4.0) lints the file against its format: structure, token references and contrast. `npx @google/design.md lint DESIGN.md` — on Windows the dot-free alias, `npx -p @google/design.md designmd lint DESIGN.md`. Source: https://github.com/google-labs-code/design.md

## Installing

Installing any of these packages is the project's job and takes the owner's OK; soho installs nothing. The versions named here are the ones checked at the source when this file is written, not a claim about what they do.
