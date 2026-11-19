import assert from "node:assert/strict";
import test from "node:test";
import {
  assistantChoices,
  assistantsWithRoles,
  bandForWidth,
  capabilities,
  configSetCommand,
  displayFile,
  editFields,
  effortTarget,
  groupRoles,
  modeLabel,
  noticeText,
  parseKindsText,
  parseModelsText,
  prefsFor,
  prefsRead,
  previewCommands,
  previewFromDryRuns,
  shortModel,
  sortRoles,
  sortedLayers,
  sortedReportItems,
  statusLabel,
  teamExists,
  toggleSort,
  undoPlan,
  workerStats,
} from "../model.js";

const roles = [
  { role: "implementer", lane: "build", kind: "pi", model: "opencode-go/hy4-flash-free", effort: "high", mode: "edit" },
  { role: "reviewer", lane: "review", kind: "codex", model: "gpt-6.2-sol", effort: "medium", mode: "read-only" },
  { role: "critical-implementer", lane: "critical", kind: "codex", model: "sol", effort: "medium", mode: "edit" },
  { role: "planner", lane: "", kind: "claude", model: "fable", effort: "high", mode: "read-only" },
];

test("grouping by lane uses labels and puts lane-less roles in Outside lanes", () => {
  const groups = groupRoles({ roles, lanes: [
    { name: "build", roles: ["implementer"], kind: "pi", model: "opencode-go/hy4-flash-free", effort: "high" },
    { name: "review", roles: ["reviewer"], kind: "codex", model: "gpt-6.2-sol", effort: "medium" },
    { name: "critical", roles: ["critical-implementer"], kind: "codex", model: "sol", effort: "medium" },
  ] }, { team_json: true });
  assert.deepEqual(groups.map((g) => g.label), ["Build", "Review", "Risky code", "Outside lanes"]);
  assert.deepEqual(groups[0].roles, ["implementer"]);
  assert.deepEqual(groups[3].roles, ["planner"]);
  assert.equal(groups[2].label, "Risky code");
});

test("grouping without team_json groups by roles[].lane", () => {
  const groups = groupRoles({ roles }, {});
  assert.deepEqual(groups.map((g) => g.label), ["Build", "Review", "Risky code", "Outside lanes"]);
  assert.equal(groups[1].model, "gpt-6.2-sol");
  assert.equal(teamExists({ roles }, {}), true);
});

test("lane labels capitalize unknown lanes, docs is Docs", () => {
  const groups = groupRoles({ roles: [{ role: "a", lane: "ops" }, { role: "b", lane: "docs" }] }, {});
  assert.deepEqual(groups.map((g) => g.label), ["Ops", "Docs"]);
});

test("sort asc and desc per column, second click reverses", () => {
  const cols = {
    role: ["critical-implementer", "implementer", "planner", "reviewer"],
    assistant: ["claude", "codex", "codex", "pi"],
    model: ["fable", "gpt-6.2-sol", "opencode-go/hy4-flash-free", "sol"],
    effort: ["high", "high", "medium", "medium"],
    mode: ["edit", "edit", "read-only", "read-only"],
  };
  for (const [column, expected] of Object.entries(cols)) {
    const asc = sortRoles(roles, { column, dir: "asc" }).map((r) => r[column === "assistant" ? "kind" : column]);
    assert.deepEqual(asc, expected, `asc ${column}`);
    const desc = sortRoles(roles, { column, dir: "desc" }).map((r) => r[column === "assistant" ? "kind" : column]);
    assert.deepEqual(desc, [...expected].reverse(), `desc ${column}`);
  }
  assert.deepEqual(toggleSort({ column: "role", dir: "asc" }, "role"), { column: "role", dir: "desc" });
  assert.deepEqual(toggleSort({ column: "role", dir: "desc" }, "kind"), { column: "kind", dir: "asc" });
});

test("band thresholds at 320, 321, 559 and 560 px", () => {
  assert.equal(bandForWidth(240), "narrow");
  assert.equal(bandForWidth(320), "narrow");
  assert.equal(bandForWidth(321), "medium");
  assert.equal(bandForWidth(559), "medium");
  assert.equal(bandForWidth(560), "wide");
});

test("effort target key for lane roles and roles outside lanes", () => {
  const groups = groupRoles({ roles }, { team_json: true });
  assert.deepEqual(effortTarget(roles[0], groups), {
    key: "lane.build.effort",
    appliesTo: "Applies to the Build lane: 1 roles",
    lane: "build",
    label: "Build",
    role: "",
  });
  assert.deepEqual(effortTarget(roles[3], groups), {
    key: "role.planner.effort",
    appliesTo: "Applies to this role only",
    lane: "",
    label: "planner",
    role: "planner",
  });
});

test("preview command strings for project and user scope", () => {
  const fields = editFields({ appliesTo: "lane", lane: "build", kind: "pi", model: "opencode-go/hy4-flash-free", effort: "high" });
  assert.deepEqual(fields.map((f) => f.key), ["lane.build.kind", "lane.build.model", "lane.build.effort"]);
  assert.deepEqual(previewCommands(fields, "project"), [
    "soho config set lane.build.kind pi --project",
    "soho config set lane.build.model opencode-go/hy4-flash-free --project",
    "soho config set lane.build.effort high --project",
  ]);
  assert.deepEqual(previewCommands(fields, "user"), [
    "soho config set lane.build.kind pi --user",
    "soho config set lane.build.model opencode-go/hy4-flash-free --user",
    "soho config set lane.build.effort high --user",
  ]);
  assert.equal(configSetCommand("role.reviewer.effort", "low", "user"), "soho config set role.reviewer.effort low --user");
  const roleFields = editFields({ appliesTo: "role", role: "reviewer", kind: "codex", model: "sol", effort: "low" });
  assert.deepEqual(roleFields.map((f) => f.key), ["role.reviewer.kind", "role.reviewer.model", "role.reviewer.effort"]);
});

test("dry-run preview uses CLI commands when valid and falls back without the capability", () => {
  const fields = editFields({ appliesTo: "role", role: "reviewer", effort: "low" });
  assert.deepEqual(previewFromDryRuns(null, fields, "project"), {
    valid: true, error: "", commands: ["soho config set role.reviewer.effort low --project"],
  });
  const results = [{ valid: true, command: "soho config set role.reviewer.effort low --project" }];
  assert.equal(previewFromDryRuns(results, fields, "project").commands[0], results[0].command);
  assert.deepEqual(previewFromDryRuns([{ valid: false, error: "bad effort" }], fields, "project"), {
    valid: false, error: "bad effort", commands: [],
  });
});

test("undo uses config unset for in_file null and set for a stored value", () => {
  const key = "lane.build.effort";
  assert.deepEqual(undoPlan({ in_file: null }, key, "project", { config_unset: true }), {
    available: true, args: ["config", "unset", key, "--project"],
  });
  assert.deepEqual(undoPlan({ in_file: "high" }, key, "user", {}), {
    available: true, args: ["config", "set", key, "high", "--user"],
  });
  assert.deepEqual(undoPlan({ in_file: null }, key, "project", {}), {
    available: false, args: [], reason: "soho has no config unset",
  });
  assert.equal(undoPlan(null, key, "project", { config_unset: true }).available, false);
});

test("notice text names the value, key and config file", () => {
  assert.equal(noticeText("Build lane effort", "xhigh", "lane.build.effort", "/Users/me/proj/.agents/soho.conf"),
    "Build lane effort is now xhigh · lane.build.effort in .agents/soho.conf");
  assert.equal(displayFile(""), "");
});

test("every capability is absent until its key is present", () => {
  assert.deepEqual(capabilities({}), {
    teamJson: false, configDryRun: false, configUnset: false, rosterWorkers: false,
    reportJson: false, assistantsJson: false, healthJson: false,
  });
  assert.deepEqual(capabilities({ team_json: true, config_dry_run: true, config_unset: true, roster_workers: true, report_json: true, assistants_json: true, health_json: true }), {
    teamJson: true, configDryRun: true, configUnset: true, rosterWorkers: true,
    reportJson: true, assistantsJson: true, healthJson: true,
  });
});

test("fallback without team_json treats team as existing and groups by row lane", () => {
  assert.equal(teamExists({ roles }, {}), true);
  assert.equal(teamExists({ team_exists: false }, { team_json: true }), false);
  assert.deepEqual(groupRoles({ roles }, {}).map((g) => g.roles[0]), ["implementer", "reviewer", "critical-implementer", "planner"]);
});

test("fallback without config_dry_run composes commands; without config_unset hides undo for null", () => {
  const fields = editFields({ appliesTo: "lane", lane: "build", effort: "xhigh" });
  assert.equal(previewFromDryRuns(undefined, fields, "project").commands[0], "soho config set lane.build.effort xhigh --project");
  assert.equal(undoPlan({ in_file: null }, "lane.build.effort", "project", {}).available, false);
  assert.equal(undoPlan({ in_file: "high" }, "lane.build.effort", "project", {}).available, true);
});

test("fallback without roster_workers still labels status from roster fields", () => {
  assert.equal(statusLabel("working"), "working");
  assert.equal(statusLabel("idle"), "idle");
  assert.equal(statusLabel("done", "ready"), "report in");
  assert.equal(statusLabel("done", "none"), "done");
  assert.deepEqual(workerStats([{ status: "working" }, { status: "done", report: "ready" }], 4), { working: 1, reportIn: 1, max: 4 });
});

test("status words map to labels: quota, gone and report ready", () => {
  assert.equal(statusLabel("quota"), "out of quota");
  assert.equal(statusLabel("gone"), "stopped");
  assert.equal(statusLabel("done", { report_state: "ready" }), "report in");
  assert.equal(statusLabel(""), "unknown");
});

test("fallback without assistants_json parses kinds text and models text", () => {
  const text = "KIND     EXECUTABLE    FAMILY     EFFORT   INSTALLED MCP\n" +
    "codex    codex         openai     max      yes       yes\n" +
    "grok     grok          xai        max      no        no\n";
  assert.equal(parseKindsText(text).length, 2);
  assert.deepEqual(parseModelsText("gpt-6.2-astra\n\ngpt-6.2-sol\n"), ["gpt-6.2-astra", "gpt-6.2-sol"]);
  const choices = assistantChoices(null, text, { codex: "gpt-6.2-sol\n", grok: "grok-4.7\n" }, {});
  assert.deepEqual(choices.assistants.map((a) => a.kind), ["codex"]);
  assert.deepEqual(choices.assistants[0].models, ["gpt-6.2-sol"]);
  assert.equal(choices.needsModelInput, false);
  const empty = assistantChoices(null, text, { codex: "\n" }, {});
  assert.equal(empty.needsModelInput, true);
});

test("assistants_json filters non-answering assistants and joins roles by kind", () => {
  const payload = { assistants: [
    { kind: "pi", installed: true, models: ["a"], probe: { status: "ready" } },
    { kind: "agy", installed: true, models: ["b"], probe: { status: "quota" } },
    { kind: "grok", installed: false, models: [] },
    { kind: "zed", installed: true, models: ["c"], probe: null },
  ] };
  const choices = assistantChoices(payload, "", {}, { assistants_json: true });
  assert.deepEqual(choices.assistants.map((a) => a.kind), ["pi", "zed"]);
  assert.deepEqual(assistantsWithRoles(choices.assistants, [])[0].roles, []);
  const withRoles = assistantsWithRoles(choices.assistants, [{ role: "implementer", lane: "build", kind: "pi", model: "a" }]);
  assert.deepEqual(withRoles[0].roles, [{ role: "implementer", lane: "build", model: "a" }]);
  const errored = assistantChoices({ assistants: [{ kind: "x", installed: true, probe: { status: "error" } }, { kind: "y", installed: true, probe: { status: "no-auth" } }] }, "", {}, { assistants_json: true });
  assert.deepEqual(errored.assistants, []);
});

test("fallback without report_json and health_json does not invent data", () => {
  const caps = capabilities({});
  assert.equal(caps.reportJson, false);
  assert.equal(caps.healthJson, false);
  assert.deepEqual(sortedReportItems([{ line: 1, mark: "done" }, { line: 2, mark: "partial" }]).map((i) => i.mark), ["partial", "done"]);
  assert.deepEqual(sortedLayers([{ name: "unit", result: "PASS" }, { name: "race", result: "NOT RUN", reason: "host gate" }]).map((l) => l.name), ["race", "unit"]);
});

test("helpers for display and preferences", () => {
  assert.equal(modeLabel("edit"), "edits");
  assert.equal(modeLabel("read-only"), "read-only");
  assert.equal(shortModel("opencode-go/hy4-flash-free"), "hy4-flash-free");
  const prefs = prefsFor({}, "/p", { view: "list", sort: { column: "kind", dir: "asc" } });
  assert.deepEqual(prefsRead(prefs, "/p"), { view: "list", sort: { column: "kind", dir: "asc" } });
  assert.deepEqual(prefsRead(prefs, "/q"), {});
});
