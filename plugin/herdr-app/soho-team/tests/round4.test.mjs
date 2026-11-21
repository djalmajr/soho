// Round 4: one test per finding F1 ... F5 of panel-mod-r3-20261119T142609.md.
// Every case renders under project A, keeps the handler the view created,
// moves to project B (or to another draft) and clicks that retained handler:
// the controller must run nothing, with a visible notice (F1). F2 ... F5 replay
// the reviewer's capability, reconciliation, report-key and diagnostic probes.
import assert from "node:assert/strict";
import test from "node:test";
import { register } from "node:module";
import * as M from "../model.js";
import * as H from "./stubs/herdr.mjs";
import { fakeCx, flush, tick } from "./stubs/context.mjs";
register("./stubs/loader.mjs", import.meta.url);
const { default: SohoPanel } = await import("../main.js");

globalThis.localStorage = {
  store: new Map(),
  getItem(key) { return this.store.has(key) ? this.store.get(key) : null; },
  setItem(key, value) { this.store.set(key, String(value)); },
  clear() { this.store.clear(); },
};

const ROLE = { role: "implementer", lane: "build", kind: "pi", model: "m-1", effort: "high", mode: "edit" };
const second = { role: "second", lane: "build", kind: "pi", model: "original", effort: "low", mode: "edit" };
const spaceA = () => ({ id: "space-a", machine: "m1", folder: "/w/a", active: true });
const spaceB = () => ({ id: "space-b", machine: "machine-b", folder: "/w/b", active: true });

const ok = (payload) => ({
  code: 0,
  stdout: typeof payload === "string" ? payload : JSON.stringify(payload ?? {}),
  stderr: "",
});

function reader(overrides = {}) {
  return async (_name, args) => {
    const key = args.join(" ");
    if (key === "capabilities --json") {
      return ok(overrides.capabilities ?? { schema: 1, team_json: true, roster_workers: true, report_json: true });
    }
    if (key === "roles --json") return ok(overrides.roles ?? { roles: [] });
    if (key === "roster --json") return ok(overrides.roster ?? { agents: [] });
    if (key === "health --json") return ok(overrides.health ?? {});
    if (key === "kinds") {
      return ok(overrides.kinds ?? "KIND     EXECUTABLE    FAMILY     EFFORT   INSTALLED MCP\n");
    }
    return ok({});
  };
}

function useFolder(space, folder) {
  H.state.spaces = [space];
  H.state.focused = { space: space.id, cwd: folder };
}

async function boot(cx, options = {}) {
  H.reset();
  if (localStorage.store) localStorage.clear();
  useFolder(options.space ?? spaceA(), options.folder ?? "/w/a");
  H.state.exec = options.exec ?? reader(options.reads ?? {});
  const panel = new SohoPanel();
  panel.init({}, cx);
  await flush(cx);
  panel.firstReadDone = true;
  if (options.team) panel.data.team = options.team;
  return panel;
}

function findRecord(root, id) {
  let found;
  const walk = (node) => {
    if (found || node == null) return;
    if (typeof node !== "object" && typeof node !== "function") return;
    const record = node.record;
    if (!record) return;
    if (record.ids.includes(id)) { found = record; return; }
    for (const child of record.children) walk(child);
  };
  walk(root);
  return found;
}

function textOf(node) {
  const strings = [];
  const walk = (value) => {
    if (value == null || typeof value === "boolean") return;
    if (typeof value === "string" || typeof value === "number") { strings.push(String(value)); return; }
    const record = value.record;
    if (!record) return;
    for (const child of record.children) walk(child);
  };
  walk(node);
  return strings.join(" | ");
}

// The bridge records the soho command as `name: "soho"` plus its argv.
const execCalls = () => H.state.calls.filter((call) => call.name === "soho");
const configCalls = () => execCalls().filter((call) => call.args?.[0] === "config");

// ---- F1: every rendered handler keeps the identity it displayed ----

test("R3-F1: a role row rendered under A runs nothing under B, even with the same role name", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE, second] } });
  const fromA = findRecord(panel.render(cx), "role-second").handlers.on_click;
  assert.equal(typeof fromA, "function");

  const bRow = { ...second, kind: "codex", model: "b-model" };
  H.state.exec = reader({ roles: { roles: [bRow] } });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  assert.ok(findRecord(panel.render(cx), "role-second"), "B shows a row with the same role name");
  const before = H.state.calls.length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(panel.edit, null, "A's row did not open B's draft");
  assert.equal(H.state.calls.length, before, "no read and no write ran");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");
});

test("R3-F1: an effort select rendered under A writes nothing under B", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE, second] } });
  panel.openMenu("effort-second", cx);
  const fromA = findRecord(panel.render(cx), "effort-second-option-medium").handlers.on_click;
  assert.equal(typeof fromA, "function");

  const bRow = { role: "second", lane: "build", kind: "codex", model: "b", effort: "low", mode: "edit" };
  H.state.exec = reader({ roles: { roles: [bRow] } });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  const before = configCalls().length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(configCalls().length, before, "A's effort target was not sent to B");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");
});

test("R3-F1: a Save rendered under A cannot write the draft opened after the switch", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE, second] } });
  findRecord(panel.render(cx), "role-second").handlers.on_click({}, cx);
  await flush(cx);
  const fromA = findRecord(panel.render(cx), "edit-save").handlers.on_click;
  assert.equal(typeof fromA, "function");

  const bRow = { role: "only-b", lane: "", kind: "pi", model: "b-model", effort: "low", mode: "edit" };
  H.state.exec = reader({ roles: { roles: [bRow] } });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  findRecord(panel.render(cx), "role-only-b").handlers.on_click({}, cx);
  await flush(cx);
  assert.ok(panel.edit, "another draft is open");
  const before = configCalls().length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(configCalls().length, before, "A's snapshot was not written");
  assert.ok(panel.edit, "B's draft stays open");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");
});

test("R3-F1: a Close rendered under A opens no dialog under B", async () => {
  const cx = fakeCx();
  const worker = { name: "w-1", pane: "%1", status: "done", report: "ready" };
  const panel = await boot(cx, { reads: { roster: { agents: [worker] } } });
  panel.data.roster = M.normalizeRoster({ agents: [worker] });
  panel.tab = "now";
  panel.band = "medium";
  const key = M.workerKey(worker);
  const fromA = findRecord(panel.render(cx), `close-${key}`).handlers.on_click;
  assert.equal(typeof fromA, "function");

  H.state.exec = reader({ roster: { agents: [worker] } });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  const overlays = H.state.overlays.length;
  const before = execCalls().filter((call) => call.args?.[0] === "release").length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(H.state.overlays.length, overlays, "no dialog opened for A's worker");
  assert.equal(execCalls().filter((call) => call.args?.[0] === "release").length, before);
  assert.equal(panel.pending, null);
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");

  // The control: the row B renders now still opens its own dialog.
  findRecord(panel.render(cx), `close-${key}`).handlers.on_click({}, cx);
  assert.equal(H.state.overlays.length, overlays + 1);
  assert.ok(panel.pending);
});

test("R3-F1: a Reopen rendered under A opens no dialog under B", async () => {
  const cx = fakeCx();
  const worker = { name: "w-2", pane: "%2", status: "gone" };
  const panel = await boot(cx, { reads: { roster: { agents: [worker] } } });
  panel.data.roster = M.normalizeRoster({ agents: [worker] });
  panel.tab = "now";
  panel.band = "medium";
  const key = M.workerKey(worker);
  const fromA = findRecord(panel.render(cx), `reopen-${key}`).handlers.on_click;
  assert.equal(typeof fromA, "function");

  H.state.exec = reader({ roster: { agents: [worker] } });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  const overlays = H.state.overlays.length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(H.state.overlays.length, overlays, "no dialog opened for A's worker");
  assert.equal(panel.pending, null);
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");

  findRecord(panel.render(cx), `reopen-${key}`).handlers.on_click({}, cx);
  assert.equal(H.state.overlays.length, overlays + 1, "B's own row still offers its dialog");
});

test("R3-F1: a cleanup confirmation rendered under A runs no gc under B", async () => {
  const cx = fakeCx();
  const caps = { schema: 1, team_json: true, health_json: true };
  const health = { gc: { total_bytes: 5, would_remove: [{ path: "/w/a/old-copy", bytes: 5, reason: "released" }] } };
  const panel = await boot(cx, { reads: { capabilities: caps, health } });
  panel.setTab("health", cx);
  findRecord(panel.render(cx), "cleanup-ask").handlers.on_click({}, cx);
  assert.ok(panel.cleanupConfirm, "the confirmation is pending");
  const fromA = findRecord(panel.render(cx), "cleanup-yes").handlers.on_click;
  assert.equal(typeof fromA, "function");

  H.state.exec = reader({ capabilities: caps, health });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  assert.equal(panel.cleanupConfirm, null, "the switch dropped the confirmation");
  const before = execCalls().filter((call) => call.args?.[0] === "gc").length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(execCalls().filter((call) => call.args?.[0] === "gc").length, before, "no gc ran");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");

  // A superseded confirmation of this context runs nothing either: its token
  // belongs to the dialog that is gone.
  panel.tab = "health";
  findRecord(panel.render(cx), "cleanup-ask").handlers.on_click({}, cx);
  const superseded = findRecord(panel.render(cx), "cleanup-yes").handlers.on_click;
  findRecord(panel.render(cx), "cleanup-no").handlers.on_click({}, cx);
  findRecord(panel.render(cx), "cleanup-ask").handlers.on_click({}, cx);
  const current = findRecord(panel.render(cx), "cleanup-yes").handlers.on_click;
  superseded({}, cx);
  await flush(cx);
  assert.equal(execCalls().filter((call) => call.args?.[0] === "gc").length, before,
    "the superseded confirmation ran no gc");
  assert.ok(panel.cleanupConfirm, "the current confirmation is still pending");

  // The control: the current confirmation of this context runs its gc.
  current({}, cx);
  await flush(cx);
  assert.equal(execCalls().filter((call) => call.args?.[0] === "gc").length, before + 1);
});

test("R3-F1: an Undo rendered under A runs no plan while B's plan is on screen", async () => {
  const cx = fakeCx();
  const caps = { schema: 1, team_json: true, config_dry_run: true };
  const read = reader({ capabilities: caps });
  const rows = { a: [ROLE], b: [{ role: "only-b", lane: "", kind: "pi", model: "b", effort: "low", mode: "edit" }] };
  let team = rows.a;
  H.state.exec = async (name, args) => {
    if (args[0] === "roles") return ok({ roles: team });
    if (args[0] === "config" && args.includes("--dry-run")) {
      return ok({ valid: true, previous: { in_file: "low" }, file: "soho.conf" });
    }
    return read(name, args);
  };
  const panel = await boot(cx, { exec: H.state.exec, team: { roles: [ROLE] } });
  panel.openMenu("effort-implementer", cx);
  findRecord(panel.render(cx), "effort-implementer-option-medium").handlers.on_click({}, cx);
  await flush(cx);
  assert.ok(panel.undoHolder, "A's notice offers Undo");
  const fromA = findRecord(panel.render(cx), "soho-undo").handlers.on_click;
  assert.equal(typeof fromA, "function");

  team = rows.b;
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  panel.openMenu("effort-only-b", cx);
  findRecord(panel.render(cx), "effort-only-b-option-high").handlers.on_click({}, cx);
  await flush(cx);
  assert.ok(panel.undoHolder, "B's own plan is the one on screen now");
  const bId = panel.undoHolder.id;
  const before = configCalls().length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(configCalls().length, before, "A's Undo did not run B's plan");
  assert.equal(panel.undoHolder?.id, bId, "B's plan is still the pending one");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");
});

test("R3-F1: a Report button rendered under A reads no report under B", async () => {
  const cx = fakeCx();
  const worker = { name: "w-1", pane: "%1", status: "working" };
  const panel = await boot(cx, { reads: { roster: { agents: [worker] } } });
  panel.data.roster = M.normalizeRoster({ agents: [worker] });
  panel.caps = M.capabilities({ report_json: true });
  panel.tab = "now";
  panel.band = "medium";
  const key = M.workerKey(worker);
  const fromA = findRecord(panel.render(cx), `report-${key}`).handlers.on_click;
  assert.equal(typeof fromA, "function");

  H.state.exec = reader({ roster: { agents: [worker] } });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  const before = execCalls().filter((call) => call.args?.[0] === "report").length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(execCalls().filter((call) => call.args?.[0] === "report").length, before);
  assert.notEqual(panel.tab, "report", "A's button did not open B's report");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");
});

test("R3-F1: an Open config rendered under A reads no file under B", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE], config_files: { project: "/w/a/.agents/soho.conf" } } });
  const fromA = findRecord(panel.render(cx), "open-config").handlers.on_click;
  assert.equal(typeof fromA, "function");

  H.state.readText = () => "settings";
  H.state.exec = reader({ roles: { roles: [ROLE] } });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  panel.data.team = { roles: [ROLE], config_files: { project: "/w/b/.agents/soho.conf" } };
  const before = H.state.calls.filter((call) => call.name === "readTextFile").length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(H.state.calls.filter((call) => call.name === "readTextFile").length, before);
  assert.notEqual(panel.tab, "config", "A's button did not open B's file");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");
});

// ---- F2: capability flags belong to the context that answered ----

test("R3-F2: adopting a context clears the capability flags until its own answer arrives", async () => {
  const cx = fakeCx();
  const capable = { schema: 1, team_json: true, assistants_json: true, health_json: true };
  const panel = await boot(cx, { reads: { capabilities: capable }, team: { roles: [ROLE] } });
  panel.tab = "assistants";
  assert.ok(findRecord(panel.render(cx), "probe-again"), "A's own answer offers the probe");

  let releaseB;
  const read = reader({ capabilities: capable });
  H.state.exec = async (name, args) => {
    if (args[0] === "capabilities") return new Promise((resolve) => { releaseB = resolve; });
    return read(name, args);
  };
  useFolder(spaceB(), "/w/b");
  const loading = panel.render(cx);
  await tick();
  assert.equal(typeof releaseB, "function", "B's capability read is in flight");
  assert.equal(findRecord(loading, "probe-again"), undefined, "A's flag did not draw B's probe button");
  assert.equal(findRecord(loading, "tab-health"), undefined, "A's flag did not draw B's Health tab");
  assert.doesNotMatch(textOf(loading), /has no assistants --json/,
    "an unknown capability is not reported as an absent one");

  releaseB(ok({ schema: 1, team_json: true }));
  await flush(cx);
  const after = panel.render(cx);
  assert.equal(findRecord(after, "probe-again"), undefined, "B's own answer has no assistants_json");
  assert.equal(findRecord(after, "tab-health"), undefined, "B's own answer has no health_json");
  assert.match(textOf(after), /has no assistants --json/,
    "B's own answer without the flag is reported as absent");
});

// ---- F3: the post-write reconciliation starts its own roles read ----

test("R3-F3: the reconcile after a write reads roles after the write, not the poll before it", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE, second] } });
  findRecord(panel.render(cx), "role-second").handlers.on_click({}, cx);
  await flush(cx);
  panel.edit.kind = "codex";
  await panel.previewEdit(cx);
  await flush(cx);

  const read = reader();
  let finishPoll;
  let polls = 0;
  let changed = false;
  H.state.exec = async (name, args) => {
    if (args[0] === "roles") {
      polls += 1;
      if (polls === 1) return new Promise((resolve) => { finishPoll = resolve; });
      return ok({ roles: [{ ...second, kind: changed ? "codex" : "pi" }] });
    }
    if (args[0] === "config" && args[2] === "role.second.kind=codex") {
      changed = true;
      return ok({});
    }
    if (args[0] === "config" && args[2] === "role.second.model=original") {
      return { code: 2, stdout: "", stderr: "invalid value 'original'" };
    }
    return read(name, args);
  };
  // A poll admitted before the write holds its roles read.
  panel.refresh(cx);
  await tick();
  assert.equal(polls, 1);
  findRecord(panel.render(cx), "edit-save").handlers.on_click({}, cx);
  await flush(cx);
  assert.equal(changed, true, "the kind write landed");
  assert.match(panel.errors.config, /Applied role\.second\.kind; rejected role\.second\.model/);
  assert.equal(polls, 2, "the reconcile started its own roles read instead of joining the poll");

  const lastRoles = H.state.calls.map((call, index) => ({ call, index }))
    .filter(({ call }) => call.name === "soho" && call.args?.[0] === "roles").at(-1);
  const kindWrite = H.state.calls.map((call, index) => ({ call, index }))
    .filter(({ call }) => call.name === "soho" && call.args?.[2] === "role.second.kind=codex").at(-1);
  assert.ok(lastRoles.index > kindWrite.index, "the second roles read began after the write");

  finishPoll(ok({ roles: [ROLE, second] }));
  await flush(cx);
  assert.equal(panel.roleByName("second")?.kind, "codex",
    "the pre-write poll answer did not undo the reconciliation");
});

// ---- F4: report reads are shared only for the full origin ----

test("R3-F4: another space at the same machine and folder reads its own report", async () => {
  const cx = fakeCx();
  const worker = { name: "same-worker", pane: "%1", status: "done", report: "ready" };
  const panel = await boot(cx, { reads: { roster: { agents: [worker] } } });
  panel.data.roster = M.normalizeRoster({ agents: [worker] });
  panel.caps = M.capabilities({ report_json: true });
  panel.tab = "now";
  panel.band = "medium";
  const key = M.workerKey(worker);

  let completeA;
  let reportReads = 0;
  H.state.exec = async (name, args) => {
    if (args[0] === "capabilities") return ok({ schema: 1, team_json: true, report_json: true });
    if (args[0] === "roles") return ok({ roles: [] });
    if (args[0] === "roster") return ok({ agents: [worker] });
    if (args[0] === "report") {
      if (++reportReads === 1) return new Promise((resolve) => { completeA = resolve; });
      return ok({ agent: worker.name, items: [{ line: 1, mark: "done", text: "current report" }] });
    }
    return ok({});
  };
  findRecord(panel.render(cx), `report-${key}`).handlers.on_click({}, cx);
  await tick();
  assert.equal(reportReads, 1, "A's report read is pending");

  // The same machine and folder, another space: a different project.
  useFolder({ id: "space-b", machine: "m1", folder: "/w/a", active: true }, "/w/a");
  panel.render(cx);
  await flush(cx);
  panel.tab = "now";
  panel.band = "medium";
  findRecord(panel.render(cx), `report-${key}`).handlers.on_click({}, cx);
  await flush(cx);
  completeA(ok({ agent: worker.name, items: [{ line: 1, mark: "done", text: "old report" }] }));
  await flush(cx);
  assert.equal(reportReads, 2, "B did not join A's pending report read");
  assert.equal(panel.data.report?.items?.[0]?.text, "current report");
});

// ---- F5: a probe diagnostic belongs to the source that started it ----

test("R3-F5: the Assistants probe shows its failure beside the retained Edit draft", async () => {
  const cx = fakeCx();
  const capable = { schema: 1, team_json: true, assistants_json: true };
  const read = reader({ capabilities: capable });
  H.state.exec = async (name, args) => {
    if (args[0] === "setup") return { code: 1, stdout: "", stderr: "assistants header probe failed" };
    return read(name, args);
  };
  const panel = await boot(cx, { exec: H.state.exec, reads: { capabilities: capable }, team: { roles: [ROLE, second] } });
  findRecord(panel.render(cx), "role-second").handlers.on_click({}, cx);
  await flush(cx);
  findRecord(panel.render(cx), "tab-assistants").handlers.on_click({}, cx);
  assert.ok(panel.edit, "the Team draft stays in memory");
  findRecord(panel.render(cx), "probe-again").handlers.on_click({}, cx);
  await flush(cx);
  assert.match(panel.errors.assistants, /assistants header probe failed/);
  assert.match(textOf(panel.render(cx)), /assistants header probe failed/,
    "the Assistants view shows its own failure");
  assert.equal(Object.keys(panel.errors).some((key) => key.startsWith("probe:edit:")), false,
    "nothing was written to a hidden draft slot");
});

test("R3-F5: a dismissed draft's probe failure never appears in its replacement", async () => {
  const cx = fakeCx();
  const capable = { schema: 1, team_json: true, assistants_json: true };
  const read = reader({ capabilities: capable });
  let finishProbe;
  H.state.exec = async (name, args) => {
    if (args[0] === "setup") return new Promise((resolve) => { finishProbe = resolve; });
    return read(name, args);
  };
  const panel = await boot(cx, { exec: H.state.exec, reads: { capabilities: capable }, team: { roles: [ROLE, second] } });
  findRecord(panel.render(cx), "role-implementer").handlers.on_click({}, cx);
  await flush(cx);
  findRecord(panel.render(cx), "edit-probe").handlers.on_click({}, cx);
  assert.equal(typeof finishProbe, "function", "the first draft's probe is in flight");
  findRecord(panel.render(cx), "edit-cancel").handlers.on_click({}, cx);
  findRecord(panel.render(cx), "role-second").handlers.on_click({}, cx);
  await flush(cx);
  assert.equal(panel.edit?.role, "second", "a replacement draft is open");

  finishProbe({ code: 1, stdout: "", stderr: "probe from first form failed" });
  await flush(cx);
  assert.equal(textOf(panel.render(cx)).includes("probe from first form failed"), false,
    "the replacement form does not display the dismissed draft's failure");
  assert.equal(Object.values(panel.errors).some((value) => String(value).includes("probe from first form failed")),
    false, "the late diagnostic was dropped");
});
