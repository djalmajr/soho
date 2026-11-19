// One test per review finding F1 ... F18 of panel-mod-r-20261119T131843.md.
// The decision logic lives in model.js so these run without gpui-kit or herdr;
// main.js and views.js call the same functions.
import assert from "node:assert/strict";
import test from "node:test";
import * as M from "../model.js";

const projectA = M.contextFor({ id: "space-a", machine: "mac-mini" }, "/work/a");
const projectB = M.contextFor({ id: "space-b", machine: null }, "/work/b");
const sameFolderOtherMachine = M.contextFor({ id: "space-c", machine: "linux-box" }, "/work/a");

const roles = [
  { role: "implementer", lane: "build", kind: "pi", model: "a", effort: "high" },
  { role: "reviewer", lane: "build", kind: "codex", model: "b", effort: "low" },
  { role: "planner", lane: "", kind: "claude", model: "c", effort: "high" },
];

test("F1: every pending action carries space, machine and folder; a mismatch refuses the write", () => {
  assert.equal(M.contextKey(projectA), "space-a|mac-mini|/work/a");
  assert.equal(M.sameContext(projectA, projectA), true);
  assert.equal(M.sameContext(projectA, projectB), false);
  // The same folder on another machine is a different project context.
  assert.equal(M.sameContext(projectA, sameFolderOtherMachine), false);
  assert.deepEqual(M.writeGuard(projectA, projectA), { ok: true, message: "" });
  const refused = M.writeGuard(projectA, projectB);
  assert.equal(refused.ok, false);
  assert.equal(refused.message, M.CONTEXT_CHANGED_MESSAGE);
  assert.equal(M.writeGuard(null, projectA).ok, false);
  const decisions = M.decideWrite({ actionContext: projectA, currentContext: projectB, busy: false });
  assert.equal(decisions.run, false);
  assert.equal(decisions.message, M.CONTEXT_CHANGED_MESSAGE);
  assert.equal(M.decideWrite({ actionContext: projectA, currentContext: projectB, busy: true }).message, "");
});

test("F2: a refresh never blocks a write in its own context, and a partial save names both sides", () => {
  // The write decision takes no read sequence: same context stays writable
  // however many reads started in between.
  assert.equal(M.decideWrite({ actionContext: projectA, currentContext: projectA, busy: false }).run, true);
  assert.equal(M.partialFailureMessage(["lane.build.kind", "lane.build.model"], ["lane.build.effort"], "soho exited 2: invalid value"),
    "Applied lane.build.kind, lane.build.model; not applied lane.build.effort. soho exited 2: invalid value");
  assert.equal(M.partialFailureMessage([], ["lane.build.kind"], "boom"),
    "Nothing was applied; not applied lane.build.kind. boom");
});

test("F3: an already-rendered capability object still advertises config_unset", () => {
  assert.equal(M.capabilities({ configUnset: true }).configUnset, true);
  assert.equal(M.capabilities({ config_unset: true }).configUnset, true);
  assert.equal(M.capabilities({}).configUnset, false);
  const camel = M.capabilities({ config_unset: true });
  assert.deepEqual(M.undoPlan({ in_file: null }, "lane.build.effort", "project", camel), {
    available: true, args: ["config", "unset", "lane.build.effort", "--project"],
  });
  assert.equal(M.undoPlan({ in_file: null }, "lane.build.effort", "project", { configUnset: false }).available, false);
});

test("F4: Herdr's suffix-only command runs only for the matching pending confirmation and row", () => {
  const workers = [
    { name: "implementer-1", pane: "%3" },
    { name: "reviewer-1", pane: "%4" },
  ];
  assert.deepEqual(M.parseCommand("close"), { name: "close" });
  assert.deepEqual(M.parseCommand("soho-team.close"), { name: "close" });
  assert.equal(M.parseCommand("soho-team.close.extra"), null);
  assert.equal(M.parseCommand(""), null);
  const pending = { action: "close", name: "reviewer-1", key: M.workerKey(workers[1]) };
  assert.equal(M.confirmationMatches(pending, "close", workers).worker.name, "reviewer-1");
  assert.equal(M.confirmationMatches(pending, "soho-team.close", workers).ok, true);
  // A different action, a renamed row and a row that is gone all refuse.
  assert.equal(M.confirmationMatches(pending, "reopen", workers).ok, false);
  assert.equal(M.confirmationMatches(pending, "close", [workers[0]]).ok, false);
  assert.equal(M.confirmationMatches({ ...pending, name: "someone-else" }, "close", workers).ok, false);
  assert.equal(M.confirmationMatches(null, "close", workers).ok, false);
});

test("F5: switching Applies to keeps the opened role, and a lane edit names its target first", () => {
  const group = M.groupRoles({ roles }, {}).find((g) => g.lane === "build");
  assert.deepEqual(group.roles, ["implementer", "reviewer"]);
  assert.equal(M.roleForScope("reviewer", group), "reviewer");
  assert.equal(M.roleForScope("", group), "implementer");
  assert.equal(M.roleForScope("ghost", group), "implementer");
  assert.equal(M.editProblem({ appliesTo: "role", role: "" }),
    "Choose which role this edit changes before saving to one role.");
  assert.equal(M.editProblem({ appliesTo: "role", role: "reviewer" }), "");
  assert.equal(M.editProblem({ appliesTo: "lane", lane: "build" }), "");
});

test("F6: the typed model value feeds the draft, its preview and its save", () => {
  const edit = { appliesTo: "role", role: "reviewer", kind: "codex", model: "original", effort: "low" };
  const typed = M.draftWithModel(edit, "model with spaces");
  assert.equal(typed.model, "model with spaces");
  assert.equal(M.editFields(typed).find((f) => f.key === "role.reviewer.model").value, "model with spaces");
  assert.deepEqual(M.previewCommands(M.editFields(typed), "project"), [
    "soho config set role.reviewer.kind=codex --project",
    "soho config set role.reviewer.model='model with spaces' --project",
    "soho config set role.reviewer.effort=low --project",
  ]);
  assert.equal(M.draftWithModel(null, "x"), null);
});

test("F7: Save waits for the preview of the exact draft and the old one is marked stale", () => {
  const edit = { appliesTo: "role", role: "reviewer", kind: "codex", model: "a", effort: "low", scope: "project", pending: false, valid: true };
  const preview = M.previewFromDryRuns(null, M.editFields(edit), edit.scope);
  edit.snapshot = M.snapshotFor(edit, preview, { "role.reviewer.model": { in_file: "z" } }, "/p/.agents/soho.conf", projectA);
  assert.deepEqual(M.previewState(edit), { canSave: true, stale: false });
  assert.equal(M.snapshotMatches(edit.snapshot, edit), true);
  // A new preview for a changed draft: nothing to save, the old lines are stale.
  const pending = { ...edit, pending: true };
  assert.deepEqual(M.previewState(pending), { canSave: false, stale: true });
  const switched = { ...edit, scope: "user" };
  assert.equal(M.snapshotMatches(edit.snapshot, switched), false);
  assert.equal(M.previewState({ ...edit, valid: false }).canSave, false);
  assert.equal(M.previewState({ ...edit, snapshot: null }).canSave, false);
});

test("F8: a value is one literal operand, and the preview quotes its real boundaries", () => {
  assert.deepEqual(M.configSetArgv("role.reviewer.model", "--add-dir /x", "project"),
    ["config", "set", "role.reviewer.model=--add-dir /x", "--project"]);
  assert.deepEqual(M.configSetArgv("role.reviewer.model", "--user", "project"),
    ["config", "set", "role.reviewer.model=--user", "--project"]);
  assert.equal(M.configSetCommand("role.reviewer.model", "--add-dir /x", "project"),
    "soho config set role.reviewer.model='--add-dir /x' --project");
  assert.equal(M.configSetCommand("role.reviewer.model", "plain-id", "project"),
    "soho config set role.reviewer.model=plain-id --project");
  assert.equal(M.configSetCommand("role.reviewer.model", "it's", "user"),
    "soho config set role.reviewer.model='it'\\''s' --user");
  assert.equal(M.shellQuote("a b"), "'a b'");
  assert.equal(M.shellQuote(""), "''");
  assert.deepEqual(M.undoPlan({ in_file: "--dash" }, "role.x.model", "project", {}).args,
    ["config", "set", "role.x.model=--dash", "--project"]);
});

test("F9: a report result is accepted only for the worker that asked for it", () => {
  const a = M.workerKey({ name: "implementer-1", pane: "%3" });
  const b = M.workerKey({ name: "reviewer-1", pane: "%4" });
  assert.equal(M.acceptsReport(b, a), false);
  assert.equal(M.acceptsReport(a, a), true);
  assert.equal(M.acceptsReport("", a), false);
  assert.equal(M.acceptsReport(b, undefined), false);
  assert.equal(M.reportBusyKey("/p", b), "report|/p|reviewer-1|%4");
});

test("F10: the legacy roster shape's state becomes the worker status", () => {
  const legacy = {
    name: "implementer-1", role: "implementer", previous_role: "", kind: "pi", pane: "%3",
    tab: "1", state: "working", report: "none", cwd: "/p", task: "Do the slice",
  };
  const [row] = M.normalizeRoster({ agents: [legacy] }).agents;
  assert.equal(row.status, "working");
  assert.equal(M.statusLabel(row.status, row.report), "working");
  assert.deepEqual(M.workerStats([row], 4), { working: 1, reportIn: 0, max: 4 });
  // A worker with `status` already is untouched, and so is a gone row.
  assert.equal(M.normalizeWorker({ name: "x", status: "done", state: "working" }).status, "done");
  assert.equal(M.normalizeWorker({ name: "x", state: "gone" }).status, "gone");
  assert.equal(M.statusLabel(M.normalizeWorker({ name: "x", state: "gone" }).status), "stopped");
});

// The exact format of kinds.CmdKinds: `%-8s %-13s %-10s %-8s %-9s %s`.
const kindsRow = (kind, exe, family, effort, installed, mcp) =>
  `${kind.padEnd(8)} ${exe.padEnd(13)} ${family.padEnd(10)} ${effort.padEnd(8)} ${installed.padEnd(9)} ${mcp}`;

test("F11: the real kinds table parses by its header, with a multi-word FAMILY and a blank EFFORT", () => {
  const text = [
    kindsRow("KIND", "EXECUTABLE", "FAMILY", "EFFORT", "INSTALLED", "MCP"),
    kindsRow("cursor", "cursor-agent", "by model", "xhigh", "yes", "yes"),
    kindsRow("pi", "pi", "by model", "max", "yes", "no"),
    kindsRow("codex", "codex", "openai", "max", "yes", "yes"),
    kindsRow("unknown", "unknown-cli", "custom", "", "no", "no"),
    kindsRow("grok", "grok", "xai", "xhigh", "no", "no"),
  ].join("\n");
  const rows = M.parseKindsText(text);
  assert.deepEqual(rows.map((r) => r.kind), ["cursor", "pi", "codex", "unknown", "grok"]);
  assert.deepEqual(rows.map((r) => r.installed), [true, true, true, false, false]);
  assert.equal(rows[0].family, "by model");
  assert.equal(rows[0].effort_ceiling, "xhigh");
  assert.equal(rows[3].effort_ceiling, "");
  assert.equal(rows[0].mcp, true);
  assert.equal(rows[1].mcp, false);
  assert.deepEqual(M.assistantChoices(null, text, {}, {}).assistants.map((a) => a.kind), ["cursor", "pi", "codex"]);
});

test("F12: a failed or malformed capability answer keeps the default and the independent reads", () => {
  assert.deepEqual(M.capabilityAnswer(null),
    { caps: null, error: "soho capabilities --json did not return an object" });
  assert.deepEqual(M.capabilityAnswer(["schema"]),
    { caps: null, error: "soho capabilities --json did not return an object" });
  assert.deepEqual(M.capabilityAnswer({ schema: 1 }), { caps: { schema: 1 }, error: "" });
  assert.deepEqual(M.sectionReads(M.capabilities(null)),
    { assistantsJson: false, legacyKinds: true, health: false });
  assert.deepEqual(M.sectionReads(M.capabilities({ assistants_json: true, health_json: true })),
    { assistantsJson: true, legacyKinds: false, health: true });
});

test("F13: each folder restores its own view and sort, with defaults for a new folder", () => {
  const prefs = {
    "/work/a": { view: "list", sort: { column: "model", dir: "desc" } },
    "/work/b": { view: "grouped", sort: { column: "effort", dir: "asc" } },
    "/work/bad": { view: "sideways", sort: { column: "nope", dir: "up" } },
  };
  const defaults = { view: "grouped", sort: { column: "role", dir: "asc" } };
  assert.deepEqual(M.viewStateFor(prefs, "/work/a", defaults), { view: "list", sort: { column: "model", dir: "desc" } });
  assert.deepEqual(M.viewStateFor(prefs, "/work/b", defaults), { view: "grouped", sort: { column: "effort", dir: "asc" } });
  assert.deepEqual(M.viewStateFor(prefs, "/work/new", defaults), defaults);
  assert.deepEqual(M.viewStateFor(prefs, "/work/bad", defaults), defaults);
});

test("F14: a non-JSON answer keeps its stderr, and model diagnostics stay per kind", () => {
  assert.equal(M.nonJsonMessage("soho kinds --json", "warning: no HOME\nsecond line\n"),
    "soho kinds --json did not return JSON\nwarning: no HOME\nsecond line");
  assert.equal(M.nonJsonMessage("soho kinds --json", ""), "soho kinds --json did not return JSON");
  assert.equal(M.modelsErrorKey("pi"), "models:pi");
  assert.notEqual(M.modelsErrorKey("pi"), M.modelsErrorKey("codex"));
});

test("F15: the List view uses the two-line narrow layout and moves sorting to the menu", () => {
  assert.deepEqual(M.listLayout("narrow"), { narrow: true, showHeader: false, sortControl: "menu" });
  assert.deepEqual(M.listLayout("medium"), { narrow: false, showHeader: true, sortControl: "header" });
  assert.deepEqual(M.listLayout("wide"), { narrow: false, showHeader: true, sortControl: "header" });
});

test("F16: the cleanup confirmation lists every gc candidate with its reason and size", () => {
  const candidates = M.gcCandidates({ would_remove: [
    { path: "/tmp/soho-copies/one", bytes: 2048, reason: "owner released" },
    { path: "/tmp/soho-copies/two", state: "idle" },
    { name: "three", size: 10, kind: "orphan process" },
  ] });
  assert.deepEqual(candidates, [
    { path: "/tmp/soho-copies/one", size: 2048, reason: "owner released" },
    { path: "/tmp/soho-copies/two", size: 0, reason: "idle" },
    { path: "three", size: 10, reason: "orphan process" },
  ]);
  assert.deepEqual(M.gcCandidates(null), []);
});

test("F17: only an assistant that answered ready is offered, and the legacy path stays installed-only", () => {
  const payload = { assistants: [
    { kind: "pi", installed: true, models: [], probe: { status: "ready" } },
    { kind: "codex", installed: true, models: [], probe: null },
    { kind: "agy", installed: true, models: [], probe: { status: "no-auth" } },
    { kind: "grok", installed: false, models: [], probe: { status: "ready" } },
  ] };
  assert.deepEqual(M.assistantChoices(payload, "", {}, { assistants_json: true }).assistants.map((a) => a.kind), ["pi"]);
  assert.deepEqual(
    M.assistantChoices({ assistants: [{ kind: "pi", installed: true, probe: null }] }, "", {}, { assistants_json: true }).assistants,
    []);
  const text = "KIND     EXECUTABLE    FAMILY     EFFORT   INSTALLED MCP\n" +
    "codex    codex         openai     max      yes       no\n" +
    "grok     grok          xai        max      no        no\n";
  // The legacy text path never had a probe: installed is the rule there.
  assert.deepEqual(M.assistantChoices(null, text, {}, {}).assistants.map((a) => a.kind), ["codex"]);
});

test("F18: polling is allowed only for a copy that rendered or had a size in the last 30 s", () => {
  const now = 1_000_000;
  assert.equal(M.pollAllowed(0, now), false);
  assert.equal(M.pollAllowed(null, now), false);
  assert.equal(M.pollAllowed(undefined, now), false);
  assert.equal(M.pollAllowed(now - 1_000, now), true);
  assert.equal(M.pollAllowed(now - 29_000, now), true);
  assert.equal(M.pollAllowed(now - 31_000, now), false);
  assert.equal(M.VISIBLE_MS, 30000);
});
