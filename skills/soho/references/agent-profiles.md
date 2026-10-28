# Agent profiles: strengths and weaknesses by kind and role

What each assistant did well and badly while playing each role, from real use of this skill. Use it to choose `role.<role>.kind`, `lane.<name>.kind` and the model per role. It is evidence, not a benchmark:
- the brief, the effort and the project weigh as much as the model;
- models change.

Re-check in your own project with `soho stats` and the review headers before changing a team.

**Source.** Seven projects, on 2026-09-24 and 2026-09-25, with about 270 dispatched tasks:
- a backend with its deployment manifests;
- infrastructure scripts;
- a desktop app that also ships on Windows;
- a browser extension with its backend;
- three web apps.

More evidence, on 2026-10-01, from four more projects:
- a web console with a Go gateway and an Ansible rollout;
- a web app with chat and model-cost slices;
- a Windows infrastructure box;
- an administrative pass.
- **Time:** from the composed brief to the report.
- **Rounds:** new slices a review sent back.

## At a glance

| Kind (model, effort) | Best at | Weak at |
|---|---|---|
| `codex` (gpt-6-luna xhigh/max; gpt-6.1-sol medium) | Following a closed brief to the letter; test-first with literal RED/GREEN; honest `[partial]`; reviews without false positives; at `medium` (gpt-6.1-sol) findings proved with executed probes and a surviving mutation | Slow at `xhigh`; its sandbox blocks ports and `ps`, and cannot write under `.git`, so UI, e2e and anything that needs a server go unproved; behaviour of a platform it cannot run; at `medium` the reports run to 300+ lines and the suite runs in a copy by default |
| `grok` (grok-4.7, high/xhigh) | Speed (about 3× codex on the same project); large slices; lean SQL; scripts from a narrow brief; reviews without a false positive and a real P1 caught in review | Reshapes production code to make a test or the type check pass; writes expected values without checking the fixture; security and cleanup gaps in scripts; unusable as a builder on Windows until the first-use trust dialog is worked around |
| `cursor` (grok-4.7, high/xhigh) | Fast, precise review when it can execute what it reviews; multi-file backend slices; spikes that need a running server | Little rigor when there is nothing to execute; confident claims about runtime behaviour it cannot run; fills its context fast |
| `claude` (opus, xhigh) | Deepest review of UI, architecture and integration, proved with probes | Small sample (7 reviews) |
| `agy` (gemini-3.8-flash, medium/high) | Found a real privacy P1 in review; says what a test really proves | Missed a state change across requests with confidence 1.0; hits its quota; small sample |
| `pi` with a small self-hosted model (~27B, high) | Fast and cheap; no invented names; pastes real output; good with a strict report format (claims table, lookup) | Fixes the instance, not the class; skips validation against real systems; async UI transitions; the end of a stream (tested the first token, not the end) |

## By kind

### codex (gpt-6-luna)

**As implementer** (4 projects, about 40 tasks; median 13 to 33 min per project, max 59):
- **Strong:**
  - follows the brief to the letter: no `Owned files` or `Forbidden` violation in 18 tasks in one project;
  - writes the failing test first, pastes the literal RED and GREEN, and runs mutation probes that it restores;
  - marks `[partial]` with the right cause (sandbox, loopback);
  - invents no names, flags or endpoints;
  - writes complete reports.
- **Weak:**
  - slow: a 600-line slice with tests took 21 min;
  - without network, it replaced the integration test with tests of a helper predicate (the codex network note now asks for the integration test anyway);
  - platform behaviour it cannot observe, which only CI or the target OS caught:
    - a test path built with a literal `/`;
    - a lint rule of a newer CI toolchain;
    - a child process held by a Windows job object;
  - once skipped the neighbouring unit test of the file it changed.

**As implementer on a long port** (this repository's JS-to-Go port, 2026-09-28 to 2026-09-29: 4 panes at `high`, about 270 briefs, 43 of them amendments):
- **Strong on product code:**
  - each slice came with probes against the reference implementation and a mutation check in an isolated copy;
  - honest `[partial]` items, with the cause;
  - no commit, no `Forbidden` violation;
  - fix rounds passed re-review.
- **Weak:**
  - the first review of a product slice usually failed: 4 to 8 findings, 1 to 4 of them P1. Budget one fix round per slice;
  - **volume slices stop halfway.** On test-mirror slices of 70 to 140 items, every first round stopped after 6 to 15 min at 30% to 60%, the rest marked `[partial]` with "no subtest in this slice". One round wrote no test at all, only the inventory. Each slice took 2 to 5 amendments;
  - **weak tests that carry the right name.** A subtest titled like the reference test only called an internal helper where the reference ran the command: a reviewer removed a product branch and 25 such subtests stayed green. Others hid a difference behind `t.Skip`, rebuilt a different scenario, or declared an "accepted divergence" that no plan listed;
  - **tests that read the machine.** Fixtures inherited `TMPDIR` or `HOME`, where a model cache lived, and passed only on the machine that wrote the cache; `go test`'s result cache hid it for a day;
  - platform work it cannot run went by hypothesis: about 13 rounds to take the Windows tests from 12 failures to green. Two causes surfaced only after the brief asked for diagnostics printed in the failing assertion (an antivirus scan on the first start of a copied `.exe`, and `select {}` aborted by the Go runtime as a deadlock).

See "Cheap implementers" below for the briefs that avoid most of this.

**As designer** (1 project, 4 tasks, about 30 min):
- **Weak:** this was its worst role. It delivered UI and e2e tests it never ran. Its sandbox blocks what a browser e2e needs: a local port and, on macOS, the browser's own process setup and writes under `~/Library`. Network access does not lift it.
  - The orchestrator found, in the browser: 3 runtime bugs, 1 dependency that broke the dev server and about 8 wrong selectors.
  - It also removed a field without checking a test that consumed it.

**As reviewer or security-reviewer** (3 projects, about 45 tasks):
- **Time depends on the effort:**
  - at `xhigh`: median 10 to 12 min, max 35;
  - at `high` (one project, 6 tasks): about 2 to 3 min each, and it still found concrete P1s, one proved with the external CLI's own trace.
- **Strong:**
  - finds the class of bug an implementer left, and proves it with a probe in a throwaway copy;
  - on scripts: a wrong final copy, live tokens used in a rehearsal, and a probe that reported a false PASS and skipped its cleanup;
  - separates a static review from a proof on the real system;
  - no known false positive.
- **Weak:**
  - at `xhigh` a review costs 2 to 4 times the slice;
  - marks `[partial]` in most reviews for checks the environment cannot run (a missing tool, no network, no cluster).

**As scouter or researcher:**
- **Strong:** fast (6 to 17 min) and light on context when the evidence is text in the repository.
- **Weak:** when the evidence was screenshots it could not see, it matched IDs to themes by their order in the brief. It said so, but the map was useless for a visual decision.

**Failures seen:**
- a provider outage (`401`) that showed on screen from the first second and surfaced only as `settled-no-report`;
- a CLI self-update at start.

**As reviewer with gpt-6.1-sol medium** (appliance and skedly, 2026-10-01; a review in about 9 min and a re-review in about 8 min at appliance):
- **Strong:**
  - found the real problems and proved each one with a headless probe run against the production component: 3 + 1 findings (appliance);
  - proved a weak test with a mutation that survived; the weak test passed with the groups swapped (appliance);
  - a probe that held only the write, with a control that proved the cause of the P2, and a reproducible probe left behind (skedly);
  - honest `[partial]` (appliance);
  - the fix passed the re-review with 0 findings (skedly).
- **Weak:**
  - very long reports, 300+ lines (appliance);
  - runs the suite in a copy by default, to keep its cache clean; running it in the worktree needs an explicit authorization in the brief (appliance);
  - a full run timed out on a test outside the slice; it logged the timeout without blaming the patch (appliance);
  - its sandbox writes only the checkout and `/tmp`; the mutation copies went to `/tmp` (`mutation-copy --dest /tmp/…`), and that worked (skedly).

The operator moved the sol reviews to `low` on 2026-10-01. There is no evidence at `low` yet.

### grok (grok-4.7)

**As implementer** (2 projects, 16 tasks):
- one web project: 12 tasks, median 12 min, max 26, and 3 slices needed another round;
- scripts and manifests from a narrow brief: 4 tasks, about 2 min on average, max 5.
- **Strong:**
  - fast;
  - large slices done in one go: 13 files, with proof that route code splitting still worked;
  - correct, lean SQL: one aggregation with the right indexes;
  - proof of time-zone and DST edges.
- **Weak:**
  - design shortcuts to make a test or the type check pass:
    - exported page components only for a test, which disabled route code splitting (P1);
    - hung a function on a DOM node;
    - justified a change with a compiler option the project did not set;
  - wrote eval answer keys without checking them against the fixture;
  - scripts needed security and cleanup fixes that the review found. In one, a request body was cut at the first space by the way it was passed to an external CLI, and its test with a fake CLI did not see it; run the real binary with adversarial input;
  - in a long reused session, two reports mentioned a subject from outside the brief. Compact or clear its session before an unrelated slice (see "One agent, one growing session" in SKILL.md);
  - one pane gave no report after 9 min and was abandoned;
  - closed a task without reading a prompt sent to it mid-slice with a raw `herdr agent prompt`. The skill's way to add to a running slice is `dispatch --amend`, which asks for a new report.

**As documenter** (1 task, about 8 min): checked the code, and separated what was committed from what was still in review.

**As reviewer** (skedly and infra, 2026-10-01):
- **Strong:**
  - 5 reviews without a false positive: 4 passed with 0 findings, 1 with a real P3; about 15 min on average (skedly);
  - found the real P1 the implementer left: a file the build embeds with `include_bytes!` that it had not listed (infra);
  - good entry-point tables: in one review, every entry of the surface with the line of its check (skedly).
- **Weak:**
  - as a builder on Windows it was unusable until the first-use trust dialog is worked around. The dialog asks whether the contents of the directory are trusted; the spawn types `/context-window 500k` right after ready, and the answer "n" is read as "No, quit", so grok exits; 3 occurrences. The rc.12 backlog plans to detect the dialog (`blocked_at_startup`, exit 7) and not type before it is gone (infra).

### cursor running grok-4.7

**As reviewer** (5 projects, about 45 tasks; median or mean 3.8 to 9.4 min per project, max 25). **As security-reviewer** (1 project, 3 tasks; about 17 min, max 26).
- **Strong:**
  - fast;
  - very precise when it can execute what it reviews: no false positive in 7 reports in one project and 16 in another;
  - mutates in a throwaway copy outside the repository and shows the RED;
  - gives findings with a ready fix;
  - good at SQL, atomicity, tests, i18n and UX regressions, and at static contracts, such as a CSS selector against the attributes a component really emits;
  - no false test failures once the role said "run it before claiming it";
  - in one project, 3 of 6 reviews asked for fixes, with 3 P1 and 1 P2, all actionable.
- **Weak:**
  - little rigor when there is nothing to execute: in one project it passed 3 of 4 slices that had real problems;
  - confident claims about runtime behaviour it cannot run: a P1 about shell semantics was half wrong;
  - reviews SQL and tests by their text. It had no large UI to review in the project where a claude reviewer found a UI-level P1, so its depth there is unknown;
  - in a directory with `direnv`, the TUI did not come up, and the brief was typed into the shell.

**As reviewer on a long port** (this repository's JS-to-Go port, at `high`: about 25 reviews of Codex slices and of the orchestrator's own commits):
- **Strong:** the most useful role of the run. Every review executed probes against the reference implementation and a mutation in a throwaway copy, and about half of them failed the slice with real P1s:
  - an error blamed on a command that never ran;
  - a flag resolved by a file-name prefix;
  - `doctor --fix` writing a commented-out value;
  - a model regex in another dialect;
  - a safety check that said `ok` when its tool was not executable;
  - test mirrors that stayed green after the product was broken. It read the runtime's source (libuv) to settle what Node does on Windows, and said what only the target machine could prove. No false positive seen; one review read a tree that was already stale.
- **Operational:** the TUI ignores a `/compact` sent from outside: open a fresh pane per review. Its follow-up box truncates a queued prompt path, so `dispatch` reports "not confirmed" for a prompt that did arrive.
- **Best use with cheap implementers:** a sampled review of test mirrors ("does each test prove what its title promises? mutate the product and see which test fails") caught what the gate could not.

**As implementer** (2 projects: 3 spikes that need a server, about 23 min typical and max 28; multi-file backend and extension slices in the other):
- **Strong:**
  - large multi-file slices with a clear report of files, tests and limits, and an explicit `[skipped]` for tests that must wait for a release tag;
  - research with execution: it cites source and documentation files and invents no flag;
  - found a bundler behaviour on its own;
  - left the server running with its PIDs, as asked.
- **Weak:**
  - fills its context fast: 70% in one spike;
  - one change dropped a flag when a later poll omitted it; the orchestrator caught it;
  - ran a package install at the repository root without saying so;
  - wrote a proof file inside a build output directory.

### claude

**As reviewer** (opus, 1 project, 7 tasks; median 5 min, max 10):
- **Strong:** the deepest reviewer seen, with no false positive.
  - Proved a UI P1 with the framework's own compiler.
  - Wrote probes that fail on the original code and pass on the fix.
  - Checked a commit message against the schema it described.
- **Weak:** marks `[partial]` on items that need a browser the brief forbade. That is correct by the contract, but it triggers the warning.

**As reviewer with Sonnet 5.5 medium** (appliance and cinzel, 2026-10-01):
- **Strong:**
  - on an earlier slice it found 2 real P2s (appliance);
  - useful static review of a retired composition: it separated the integration from the content of the ancestral HEAD, identified the generic sources, and declared `[partial]` on what it did not verify (cinzel);
  - three P3 findings on preservation and consumers; the report was useful to decide retention and backup (cinzel);
  - shorter reports than the sol reviews (appliance).
- **Weak:**
  - less deep than the sol reviewer on the async transitions; on the Go/Ansible slices it found only P3s (appliance);
  - no execution test and no amendment round in the cinzel review.

**As scouter** (infra on Windows, 2026-10-01):
- **Weak:** compared a local clone with the remote without `git fetch` and asserted a wrong state of the fork. The scouter briefs now say to fetch before comparing with the remote.

### pi with a small self-hosted model (~27B)

**As implementer** (1 project, 35 tasks; median 8 min, max 18):
- **Strong:**
  - follows the brief to the letter;
  - no invented name, flag or endpoint in 35 tasks, and no `Forbidden` violation;
  - pastes real command output: every declared test run that was checked had run;
  - stops with options (`[partial]`) instead of guessing.
- **Weak:**
  - fixes the instance, not the class. Slices on untrusted input (host, path, URL) came back twice each: first one variant, then the next;
  - did not validate manifests against an API server. Three errors showed up in a strict server dry-run and on a live apply, not in the slice;
  - once created a local cluster the brief did not ask for. The role now forbids this.

**As implementer, continued** (Qwen 27B at `high`, 2026-10-01; skedly: 8 tasks, average 13.8 min, max 25.9; infra on Windows: a large 42-min slice; appliance: a UI slice, first delivery in about 16 min with 174 tests green):
- **Strong:**
  - good volume and coverage: 174 tests green on the first delivery, with light/dark screenshots and a detailed report (appliance);
  - honest `[partial]` with the proof by file:line: when the brief's premise did not hold (the TTS cost is recorded per character, not per minute), it said so and proved it (skedly);
  - mutation in a copy in every code slice (skedly);
  - good diff without invention (infra);
  - touched files outside its own only when a gate forced it, and said so (skedly).
- **Weak, the typical errors seen on 2026-10-01:**
  - async UI transitions: good on volume and coverage, but the weak point was the transitions (react-query pending/cache/refetch): 2 P2 on the first delivery of a UI slice (a hidden selection still sent on submit; text citing a service that was not there), and a new P2 after the first amendment (a reset with cached data during a refetch) — the brief has to list these scenarios (appliance);
  - the end of the stream: the brief said the write must not delay the stream; it tested the first token, not the end, and the write held the stream close (skedly, the P2 the sol review caught);
  - a file the build embeds with `include_bytes!` left out of the build (infra, the P1 the grok review caught);
  - installed `pyyaml` with `pip --user` without authorization (infra);
  - a regression it did not see: three stale fixed-count tests stayed masked behind tests that already failed by platform on Windows (infra).
- **Weak, seen on 2026-10-28** (`lbvllm/qwen3.8-27b`): it left the formatter unrun on files it had edited, and blamed the failing check on something else.
  - Twice in a row it blamed a failing `bun run check` on a file it had not touched and on the biome version. The cause was the formatting of its own files, even with the brief quoting the exact file and rule from biome's output (kashes).
  - It left `cargo fmt` pending in a file and attributed it to the base commit (ai-usagebar). There the diagnosis was right, but the pattern is the same.
  - The implementer role now asks for the project's formatter on the edited files before the check is reported.

**As scouter or researcher** (9 tasks; median 10 to 15 min): with the lookup directive, the surveys were ready for a decision. It once claimed an absence without searching. A survey of 173 files was very useful (infra on Windows, 2026-10-01).

**As documenter** (11 tasks; median 5 min): with the claims table, no round back. Without it, it stated the plausible. A docs slice (business docs, ADR, runbook, help) with a phrase-to-file:line table passed with 0 findings, and the rule zeroed the overclaims of the previous round: 6 P2 on 2026-09-30, 0 on 2026-10-01 (skedly).

**As tasker** (2 tasks): 2 to 3 min.

### agy (gemini-3.8-flash)

**As implementer** (1 project, 3 tasks; mean 12 min, max 15, medium effort): 2 fixes after review across the 3 slices; its `[partial]` items were declared.

**As reviewer** (1 project, high effort; median about 4 min over all the project's reviews):
- **Strong:**
  - found a real privacy P1 (a public cache header on authenticated content), with the place and the fix;
  - said what a test really proves, apart from what it seems to prove.
- **Weak:**
  - passed a change that dropped a flag when a later poll omitted it: a state change across requests, missed with confidence 1.0;
  - in another review, reported a passing dynamic check that cited a runtime version other than the installed one and test names that did not exist in the checkout (verdict `pass`, confidence 0.98); an amendment asking for the real commands corrected it;
  - hit its individual quota once and resumed after the reset.

## Field metrics (generated)

Generated by `soho metrics table --write` from the anonymized summaries under `evals/field/` (`metrics export`, reviewed by the operator before the commit). One row per role, slice type, kind, model and effort. Field data stays confounded by the task: use it to choose the next controlled run under `evals/`, not to settle a choice by itself. Do not edit between the markers.

<!-- soho:metrics-table:start -->
_No field summaries yet._
<!-- soho:metrics-table:end -->

## Validation on the target machine

Outside the skill, the orchestrator of the desktop project asked a claude on the Windows machine (`herdr --machine <m> agent prompt`) to run 7 native validations:
- no false positive;
- 2 real findings that no worker saw: a test path separator, and an uninstall hook that killed every running instance;
- it stopped to ask when the machine differed from the brief.

Platform behaviour needs a run on the platform. A reviewer that cannot run it should say so instead of guessing.

## Recommendation per role

- **implementer:**
  - `codex` for closed briefs and sensitive correctness (TDD, atomicity, payments, access control); for volume work, small slices and the quality bar of "Cheap implementers" in the first brief;
  - `pi` with Qwen 27B at `high` for volume and coverage: 8 tasks in about 14 min on average, and a good diff without invention (skedly and infra, 2026-10-01) — but with a brief that lists the async scenarios and forbids installing dependencies; the typical misses are the async UI transitions, the end of a stream (it tested the first token, not the end), a forgotten embedded file, and a regression masked by platform failures (2026-10-01);
  - `grok` for speed on well-specified slices and scripts, with a reviewer that looks for design shortcuts and missing cleanup;
  - `cursor` for multi-file backend slices and spikes that need a server;
  - a small model for closed, low-risk slices;
  - slices on untrusted input need a stronger model, or a brief that lists the hostile variants to test.
- **designer and UI work with e2e:** a kind that can open a port and run a browser. Not `codex` inside its sandbox.
- **reviewer:** choose by the checks the slice needs:
  - `claude` for UI, architecture and integration; Sonnet 5.5 medium is a useful static reviewer, but less deep than `codex` on the async transitions (appliance and cinzel, 2026-10-01);
  - `codex` with gpt-6.1-sol at `medium` proved useful: it executes the probes, proves a weak test with a surviving mutation, and is honest about `[partial]` (appliance and skedly, 2026-10-01). Expect long reports and the suite running in a copy by default; the operator moved it to `low` on 2026-10-01, with no evidence at `low` yet;
  - `cursor`/`grok` for SQL, atomicity and tests;
  - `codex` when the implementer is `grok` or `cursor`, at `high` effort (`xhigh` for security and large slices);
  - `agy` at `high` for privacy and access-control reviews, with a check for a state change across requests; `agy` at `medium` for bounded UI. Ask it for the exact command and pasted output of every check it says it ran. When its quota is spent, use another available assistant;
  - for platform behaviour, a run on the target machine.
- **security-reviewer:** `cursor`/`grok` or `codex`, both precise when they could execute their probes.
- **scouter and researcher:**
  - `codex` or a small model when the evidence is text in the repository; a small model on `pi` surveyed 173 files usefully (infra, 2026-10-01);
  - fetch before comparing a local clone with the remote: an opus scouter compared without `git fetch` and asserted a wrong state of the fork (infra, 2026-10-01);
  - none of them when the evidence is visual and the images are not reachable. Map it yourself first.
- **documenter:** `codex`, `grok` or a small model, with the claims table. `grok` and a small model are the cheapest.

## Cheap implementers

One goal of this skill is to build with cheap implementers (for example `codex` with gpt-6-luna, or a small model on `pi`) and keep quality with a strong reviewer. Cheap models follow a closed brief well and fail in predictable ways: they stop early on volume, write tests that look right and prove little, and trust the machine they run on. These briefs held up on a long port with four cheap panes:

1. **Small slices.** Keep a volume slice under about 40 items, or one module. Past that, the first round stops at 30% to 60% and asks for more rounds than a split would have cost.
2. **The quality bar goes in the first brief, not in an amendment:**
   - "Volume is not a reason for `[partial]`; a report with items left for lack of time does not close the slice: continue until done";
   - a test goes through the entry point the reference test uses (the command, not an internal helper), and asserts the exact values the reference asserts;
   - one mutation per group of items, and the test for that item must fail with it;
   - no skip to hide a difference, and no "accepted divergence" unless the orchestrator's plan lists it. A difference is a finding, with both outputs.
3. **An item map as the deliverable.** Ask for a file that maps every item to its proof (test name, command, output), with `[partial]` and the reason. Count the `[partial]` lines yourself. When a first round comes back more than about 30% partial, amend at once with "continue".
4. **Hermetic tests.** Fixtures set `HOME` and `USERPROFILE`, `XDG_CONFIG_HOME`, `TMPDIR` (caches live there) and a `PATH` with only the fakes. Gates run uncached (`go test -count=1`, `--no-cache`): a cached pass can hide a test that reads the machine.
5. **Platform work.** A cheap model cannot run Windows or macOS it does not have. Ask it for diagnostics printed in every failing assertion (environment, argv, paths, timings), run the tests on the target yourself, and send the log back as the next amendment.
6. **Check a "product bug" before acting on it.** A worker's worktree can be older than the integration branch: rerun the failing test on the integrated tree first.
7. **Review the tests, not only the code.** Besides the per-slice review, run a sampled review of the test mirrors: "does each test prove what its title promises? mutate the product and see which test fails". It found what the gate could not.
8. **Context.** Send a same-subject amendment to the same pane while its context is under about 70%. Past that, `/compact` and send an amendment that stands alone: it points to the original brief and to the item map.

## Directives these observations already put in the roles

- The reviewer runs a test before calling it wrong, and may mutate or build only in a throwaway copy.
- The implementer:
  - writes the integration test the brief asks for even when it cannot run it (the codex network note);
  - runs mutation checks in a throwaway copy when the tree is shared;
  - never starts local infrastructure the brief did not ask for;
  - finishes the list instead of leaving items for a later round;
  - ports a test through the entry point the original uses, and reports a difference instead of skipping it;
  - keeps tests hermetic and runs the gate uncached, and makes a failing platform assertion print what the next run needs;
  - installs no dependency the brief did not ask for: one made it in with `pip --user` on a Windows slice (infra, 2026-10-01).
- The brief for a UI slice lists the async scenarios the slice must cover (pending, cache, refetch): a UI slice missed its async state transitions (appliance, 2026-10-01).
- The brief for a streaming slice names the end of the stream when that is what must not wait: one slice tested the first token, not the end, and its write still held the stream's close (skedly, 2026-10-01).
- On Windows, the brief asks for a base × current comparison of the tests, or the tests run on Linux: a regression hides in a test that already failed by platform (infra, 2026-10-01).
- Workers stop the processes they started by PID, and never list every process command line.
- The scouter and the researcher look up before they state; the documenter fills a claims table.
- The designer reports how the UI was verified (`ui_verification`).
