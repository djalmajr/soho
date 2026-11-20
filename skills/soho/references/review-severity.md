# Review severity: levels, the verdict and when review rounds end

This page defines the P0–P3 levels the `reviewer` role uses, how the `security-reviewer` scale maps onto them, which levels fail a review, and how the orchestrator ends a run of review rounds. It also records the sources the definitions were drawn from. The owner chose these rules on 2026-11-20 after a research pass, and revised them the same day after a feedback round from six orchestrators: P2 now needs an impact, and a slice may end with a point fix after its third review. The levels are soho's own; where a source says something different, the section "Where soho departs from the sources" says so.

## Levels

A level applies only to what the reviewer's Stop criterion makes a finding: a tighter bound on a test that already discriminates stays a note, and behavior identical to the `Baseline:` goes under `## Follow-ups`. A level measures who reaches the defect, by which path, and what it costs. It does not measure how long the fix takes, or how many rounds came before. Published text (a message, a doc line, a citation) is graded the same way, by who follows it.

| Level | Definition | Example |
|---|---|---|
| P0 | The change breaks the product, loses or corrupts data, or opens a security hole for every input or user, with no assumption about how it is used. | An inverted permission check lets any logged-in user read another user's records. |
| P1 | A main, expected path of the change fails or gives a wrong result under ordinary use, or the change contradicts a requirement the brief names. | A list endpoint drops the last item whenever the item count equals the page size. |
| P2 | A supported caller, on a normal usage path, reaches a defect with one of these impacts while the main path works: a wrong result or a false message the user sees, data lost or corrupted, a security exposure, a process, route, secret or other resource left alive or wrongly used, or a mandatory gate broken. Changed code with no test in a layer `VALIDATION.md` declares with a command is also P2. | A parser handles every sample but throws on an empty array that the API schema allows. |
| P3 | Anything below P2: a trigger no supported caller produces on a normal usage path, an environment no supported deployment uses, or a defect with none of the P2 impacts. | A cache key collides for two inputs that the documented schema forbids and no supported writer emits. |

A normal usage path is one a user or a supported caller takes in ordinary use. A forced interleaving is not one, and neither is a state fabricated by a test or a write to the system's own storage from outside its code. These are P3 unless they lose or corrupt data or open a security exposure:
- performance with no visible effect for the user;
- a bounded delay;
- the failure of a failure path that already has a fallback;
- a race that needs a forced interleaving: a step pinned between two internal calls by a test, a debugger or an injected delay. Actions that supported callers take on their own, such as Cancel while a request is pending or two users submitting at nearly the same moment, are ordinary use;
- internal state that only another defect or a direct write to storage produces (a supported writer that itself writes wrong state is graded by its own path);
- a defect inside test code, such as a flaky test or a leaking mock.

When the reviewer cannot tell whether a supported caller reaches the trigger on a normal usage path, it picks the lower level and names the assumption that would raise it. A trigger a supported caller is known to produce on a normal path keeps its level, however rare. A finding whose trigger depends on an assumption the reviewer cannot name from the code or the brief is at most P3. A variant of a class that an earlier round of the same slice already fixed is at most P3 unless the reviewer names the normal usage path that produces it. A missing test in a layer `VALIDATION.md` declares with a command is P2 in every round.

## The verdict

The report's first line fails when any P0, P1 or P2 finding remains open (`reviewer.md`, report contract). P3 never fails a review.

## Security findings

The `security-reviewer` grades by impact if the vulnerable path is reached: critical, high, medium, low or info. Its header line maps critical to P0, high to P1, medium to P2, and low or info to P3. Confidence says how sure the reviewer is that the path is reached; a medium finding whose reachability the reviewer could not show is reported as low, with the missing step named.

## One class, one round

When a finding is an instance of a class (path normalization, an id format, a boundary value, a state transition), the reviewer probes the neighboring variants of that class in the same round and lists each variant it tried, with its result, in that one finding. The sweep stays on the code path the finding touches: the changed function, its callers and the code it calls. A variant it did not try, or could not run because a provider refused the probe, is named as untried, not left for the next round. The correction brief asks the implementer to sweep the same class before delivering, and to test each untried variant.

## What the orchestrator does with each level

- **P0 and P1:** fixed before push, in the same slice.
- **P2:** fixed in the same round. A `declined:` answer in `## Review responses` closes a P0–P2 only when the reviewer agrees the finding does not apply: a false positive is dropped, and behavior identical to the `Baseline:` goes under `## Follow-ups`. A reason about scope, ownership or schedule leaves the finding open, and it keeps failing the verdict.
- **P3:** never opens a round. A P3 about behavior becomes a tracked card with an owner. A P3 about wording or a citation is fixed in the same slice without a new full review round, and the fix is named in the delivery.
- **Behavior identical to the `Baseline:`** goes under `## Follow-ups` and becomes a card when it is worth doing, even when the change passes near it.
- **A defect in test code that breaks a gate** (the test suite or CI fails because of a test or a mock) is fixed by the orchestrator before push, without a review round.

## When rounds end

- Every correction review keeps the slice's original `Baseline:`. The brief may also name the last reviewed revision so the reviewer reads that delta first; the original baseline still decides what is pre-existing, so a defect introduced in round one never turns into a follow-up.
- The count of rounds belongs to the slice. It survives a change of worker or of assistant. A task moved to another assistant because a provider refused it is the same round.
- After two fix rounds on one slice with P2 findings of the same `[class:]`, the third review brief lists the classes already seen and asks for the class sweep above.
- A fourth full round is not opened. When the only P0–P2 findings the third review leaves open are P2s and each has an obvious fix of a few lines (open P3s do not count), the orchestrator applies a point fix:
  - it fixes each one, with a test that fails without the fix and a mutant that test kills;
  - a reviewer checks only that delta, answering one question per finding: is it fixed, and does the delta add a defect?
  - this check is not a full round. If it fails, the slice stops.
- When the third review leaves a P0 or P1 open, or a P2 whose fix is not small, the slice stops without being accepted and the orchestrator tells the owner. A simplified version (the part that keeps producing edge cases dropped, or an existing rule reused instead of a new one) is a new slice, with its own brief and its own review count. Only P3 findings may be left as cards: an open P0, P1 or P2 never lets a slice finalize.
- Inside a `collaborate` assignment the engine runs the rounds (`orchestration.md`). It counts only its own reviews, and its default of 3 would allow a fourth. Start one only at a slice's first review, with `--max-rounds 2` (the engine then escalates a blocker at the third review, and its approval refuses an open P0–P2) and with an assignment brief that already asks for the class sweep and tells the third reviewer to read the earlier findings reports listed by `collaborate status <id> --json`, name their classes and sweep them even when the earlier instances are fixed, since no later brief reaches the reviewer during the assignment. There a wording or citation P3 becomes a card instead of an edit, because an edit changes the inputs the assignment binds. When an assignment ends or is replaced before the slice is accepted, the remaining reviews go through ordinary dispatch and count the reviews already done; a point fix after the third review also goes through dispatch.

## Where soho departs from the sources

- In the Codex review rubric, P0–P3 are urgency: P2 is "Normal. To be fixed eventually", and the verdict ignores non-blocking issues. In Chromium's bug triage, priority is schedule: a P2 bug "can safely be punted from a release". Neither uses P2 as a merge gate. soho keeps P2 blocking and narrows its definition instead, so that the blocking rule, the `wait` header parser and the `collaborate` approval stay as they are.
- No primary source found in the research sets a numeric cap on review rounds. The two-plus-one rule above is soho's own, informed by the sources' advice to approve with follow-ups and not to let a change stall.
- The P2 impact list, the normal-usage-path test, the point fix after the third review and the handling of test-code defects come from soho's own feedback round of 2026-11-20, not from the sources: under the first version, reviewers cited a caller for almost any interleaving, and slices stopped at the limit over findings the owner then fixed in a few lines.
- Codex does not flag bugs that existed before the change. soho scopes P0–P2 by `Baseline:` the same way, and lists the rest under `## Follow-ups` instead of dropping it.

## Sources

Fetched 2026-11-20 by the research worker. The Codex rubric was read directly, at commit `bb5b9c6240136de8ff4d30215e2341c44559aa04` of `openai/codex`; the other excerpts were relayed by a web-fetch step and not re-opened. Quotes are short excerpts; follow the link for the full text.

| Source | What it says | Link |
|---|---|---|
| OpenAI Codex CLI review rubric | P0 "Drop everything to fix. Blocking release, operations, or major usage. Only use for universal issues that do not depend on any assumptions about the inputs." P1 "Urgent. Should be addressed in the next cycle." P2 "Normal. To be fixed eventually." P3 "Low. Nice to have." The verdict: "Ignore non-blocking issues such as style, formatting, typos, documentation, and other nits." Flag only bugs where "The bug was introduced in the commit (pre-existing bugs should not be flagged)." and "Do not stop at the first qualifying finding." | https://github.com/openai/codex/blob/bb5b9c6240136de8ff4d30215e2341c44559aa04/codex-rs/prompts/templates/review/rubric.md |
| Google engineering practices: the standard of review | Approve when the change "definitely improves the overall code health of the system", since "there is no such thing as 'perfect' code—there is only better code." "Don't let a CL sit around because the author and the reviewer can't come to an agreement." | https://google.github.io/eng-practices/review/reviewer/standard.html |
| Google engineering practices: comments | "Consider labeling the severity of your comments, differentiating required changes from guidelines or suggestions": `Nit:`, `Optional (or Consider):`, `FYI:`. | https://google.github.io/eng-practices/review/reviewer/comments.html |
| Google engineering practices: speed (LGTM with comments) | A reviewer gives approval while leaving unresolved comments when "The suggestions are minor" or "The comments don't have to be addressed by the developer". | https://google.github.io/eng-practices/review/reviewer/speed.html |
| Google engineering practices: pushback | A later cleanup "never happens" unless done right after the change; otherwise "file a bug for the cleanup and assign it to themselves so that it doesn't get lost." | https://google.github.io/eng-practices/review/reviewer/pushback.html |
| Conventional Comments | Labels (issue, suggestion, nitpick, question…) with the decorations `(blocking)` and `(non-blocking)`: a blocking comment prevents acceptance until it is resolved. Without a decoration, the team's own default decides; nitpick, thought and note are non-blocking by nature. | https://conventionalcomments.org/ |
| Chromium triage best practices | Bug priority as release schedule: P0 emergency, P1 needed for the current milestone, P2 wanted for the current milestone and "can safely be punted from a release", P3 not time sensitive. | https://www.chromium.org/for-testers/bug-reporting-guidelines/triage-best-practices |
| Chromium security severity guidelines | Critical S0, High S1, Medium S2, Low S3, mapped S0 to P0, S1 and S2 to P1, S3 to P3; an exploit in the wild is P0 "regardless of the severity rating". | https://chromium.googlesource.com/chromium/src/+/main/docs/security/severity-guidelines.md |
| Mozilla Bugzilla fields | Priority and severity are separate fields: priority schedules the work, severity grades the impact. | https://wiki.mozilla.org/BMO/UserGuide/BugFields |
| CVSS v4.0 specification | Qualitative ratings None, Low 0.1–3.9, Medium 4.0–6.9, High 7.0–8.9, Critical 9.0–10.0, "intended to help organizations properly assess and prioritize"; CVSS sets no merge gate. | https://www.first.org/cvss/v4.0/specification-document |
| Gerrit review labels | "Any -2 blocks submit." −1 "Does not block submit." "Any +2 enables submit." | https://gerrit-review.googlesource.com/Documentation/config-labels.html |
| GitHub pull request reviews | Comment, Approve, or Request changes; Request changes flags feedback "the author should address before merging". | https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/reviewing-changes-in-pull-requests/about-pull-request-reviews |
