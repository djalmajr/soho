# Validation

Template for the project's `VALIDATION.md`, the delivery contract of a change. The `tester` role fills the text between `<...>` with real values, each command after running it, and the `validator` role runs the filled file on the exact commit and reports one result per item. A filled file keeps this section order and these headings; a command that was not run is not written into the file.

## Scope

<when this validation applies: which changes, surfaces or platforms it covers, and what it is in addition to — never a replacement for — the repository gates, green CI and the review>

## Preconditions

<check every precondition first and record the exact command, version and output of each; a missing precondition makes the affected item BLOCKED, never PASS>

- <precondition, with the command or check that proves it>

## Test layers

<one line per layer, in this order; the real command after running it, or `n/a: <reason>` for a layer that does not apply>

- unit: `<command>`
- integration: `<command>`
- contract: `<command>`
- e2e: `<command>`
- mutation: `<command>`

## Items

<numbered list; an item that is always run carries `(required)`; each item names the exact command or procedure and the evidence it must produce>

1. <item (required)>
2. <item>

## Evidence

<paste the real outputs (stdout, stderr, exit codes) verbatim in fenced blocks, unedited and untruncated; name every redaction (what kind of value, and where); record where the evidence lives and who reads it — a path, a link, a count or a report assertion alone is not evidence>

## Results

- PASS: ran and passed.
- FAIL: ran and failed.
- NOT RUN: could run, and did not.
- BLOCKED: a precondition is missing; the item names which.

The overall result is the worst of the item results, in this order: FAIL, BLOCKED, NOT RUN, PASS. A required item that is not PASS forbids an overall PASS, and an item that was not proved is never PASS. A command that exits 0 without exercising anything (no test matched, an empty selection, a skipped suite) is NOT RUN.

## What this does not prove

<what this validation does not replace or cover: the repository gates, green CI or a separate review, other platforms or team shapes, anything the change does not touch>
