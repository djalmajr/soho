// Round 3: one test per finding F1 ... F13 of panel-mod-r2-20261119T135456.md,
// the reviewer's F1 and F4 probe scenarios included. Main.js runs headless
// through the recording stubs, so these assert the controller and the rendered
// record, not only the model helpers.
import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { register } from "node:module";
import * as M from "../model.js";
import * as H from "./stubs/herdr.mjs";
register("./stubs/loader.mjs", import.meta.url);
const { default: SohoPanel } = await import("../main.js");
const V = await import("../views.js");

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
    if (key === "kinds") {
      return ok(overrides.kinds ?? "KIND     EXECUTABLE    FAMILY     EFFORT   INSTALLED MCP\n");
    }
    return ok({});
  };
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

function fakeCx() {
  const ctx = {
    notifies: 0,
    tasks: [],
    timers: { every: [], after: [] },
    notify() { ctx.notifies += 1; },
    focus_handle() { return { focus() {} }; },
    timer: {
      every(ms, callback) { ctx.timers.every.push({ ms, callback }); return { cancel() {} }; },
      after(ms, callback) {
        const entry = { ms, callback, cancelled: false };
        ctx.timers.after.push(entry);
        return { cancel() { entry.cancelled = true; } };
      },
    },
    // gpui-kit calls the body in the delivering call, then adopts its promise.
    spawn(body) {
      const promise = Promise.resolve(body(ctx));
      ctx.tasks.push(promise);
      return promise;
    },
  };
  return ctx;
}

async function flush(cx) {
  // A read the test holds open must not block the drain: detach it and keep
  // advancing the microtask queue until no new task appears.
  for (let i = 0; i < 80; i += 1) {
    const pending = cx.tasks.splice(0);
    for (const task of pending) task.catch(() => {});
    if (pending.length) await Promise.race([Promise.allSettled(pending), tick()]);
    await tick();
    if (!pending.length && cx.tasks.length === 0) return;
  }
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

function flatten(root) {
  const strings = [];
  const ids = [];
  const walk = (node) => {
    if (node == null || typeof node === "boolean") return;
    if (typeof node === "string" || typeof node === "number") { strings.push(String(node)); return; }
    const record = node.record;
    if (!record) return;
    ids.push(...record.ids);
    for (const child of record.children) walk(child);
  };
  walk(root);
  return { strings, ids };
}

function textOf(node) {
  if (node && node.record === undefined && Array.isArray(node.children)) {
    const strings = [];
    const walk = (record) => {
      for (const child of record?.children ?? []) {
        if (typeof child === "string" || typeof child === "number") { strings.push(String(child)); continue; }
        if (child?.record) walk(child.record);
      }
    };
    walk(node);
    return strings.join(" | ");
  }
  return flatten(node).strings.join(" | ");
}

const T = {
  background: "#1", raised: "#2", foreground: "#3", muted: "#4", border: "#5", input: "#6",
  canvas: "#7", surface: "#8", dim: "#9", accent: "#a", popover: "#b", solid: "#c",
  solid_foreground: "#d", hover: "#e", working: "#f", blocked: "#10", unknown: "#11",
  done: "#12", ansi1: "#13", ansi3: "#14",
};

const releases = () => H.state.calls.filter((call) => call.args?.[0] === "release");
const configWrites = () => H.state.calls.filter((call) => call.args?.[0] === "config");
const rolesReads = () => H.state.calls.filter((call) => call.args?.[0] === "roles");

// ---- F1: origin-bound datasets and actions ----

test("R2-F1: the former project's rows are gone before the new project answers and cannot seed a Save", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE, second] } });
  assert.ok(findRecord(panel.render(cx), "role-second"), "the row is painted in its own project");

  // The reviewer's probe: switch to project B, hold B's roles read, and try the
  // row that A left on screen.
  let replyTeam;
  const read = reader();
  H.state.exec = async (name, args) => {
    if (args[0] === "roles") return new Promise((resolve) => { replyTeam = resolve; });
    return read(name, args);
  };
  useFolder(spaceB(), "/w/b");
  const root = panel.render(cx);
  assert.equal(findRecord(root, "role-second"), undefined, "no row of the former project survives the switch");
  assert.equal(panel.data.team, null, "the former project's team left the view");
  assert.match(textOf(root), /Reading the team/);
  const save = findRecord(root, "edit-save");
  assert.equal(save, undefined, "no Save is reachable from the former project");
  await tick();
  replyTeam(ok({ roles: [] }));
  await flush(cx);
  assert.deepEqual(configWrites(), [], "nothing was written against project B");
});

test("R2-F1: a machine switch at the same path reads the new machine and paints its rows", async () => {
  const cx = fakeCx();
  const panel = await boot(cx);
  let replyA;
  const read = reader();
  H.state.exec = async (name, args, options) => {
    if (args[0] === "roles") {
      if (options?.on === "m1") return new Promise((resolve) => { replyA = resolve; });
      return ok({ roles: [{ role: "only-on-b", lane: "", kind: "pi", model: "b", effort: "low" }] });
    }
    return read(name, args);
  };
  panel.refresh(cx);
  await tick();
  const switchedAt = H.state.calls.length;
  useFolder({ id: "space-a", machine: "m2", folder: "/w/a", active: true }, "/w/a");
  panel.refresh(cx);
  replyA(ok({ roles: [] }));
  await flush(cx);
  const seen = H.state.calls.slice(switchedAt)
    .filter((call) => call.args?.[0] === "roles").map((call) => call.options?.on);
  assert.deepEqual(seen, ["m2"], "the new machine's own roles read ran, beside the one still in flight");
  assert.match(textOf(panel.render(cx)), /only-on-b/);
});

test("R2-F1: a delayed Open config cannot paint the former project's file", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE], config_files: { project: "/a/.agents/soho.conf" } } });
  let complete;
  H.state.readText = () => new Promise((resolve) => { complete = resolve; });
  // The capture is the one the footer installs: the team origin it read plus
  // the file path it displayed (R4-F2).
  const reading = panel.openConfig({ origin: panel.originOf("team"), path: panel.configFile("project") }, cx);
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await tick();
  complete("project A settings");
  await reading;
  await flush(cx);
  assert.notEqual(panel.tab, "config", "the late file did not open a tab");
  assert.equal(panel.data.configText, null, "the late file was discarded");
});

// ---- F2: overlapping reads of one origin ----

test("R2-F2: a same-origin refresh coalesces the read in flight and keeps its answer", async () => {
  const cx = fakeCx();
  const panel = await boot(cx);
  let respond;
  let capabilitiesCalls = 0;
  const read = reader();
  H.state.exec = async (name, args) => {
    if (args[0] === "capabilities") {
      capabilitiesCalls += 1;
      return new Promise((resolve) => { respond = resolve; });
    }
    return read(name, args);
  };
  panel.refresh(cx);
  await tick();
  panel.refresh(cx);
  await tick();
  assert.equal(capabilitiesCalls, 1, "the second refresh joined the read still in flight");
  respond(ok({ schema: 1, team_json: true, roster_workers: true, health_json: true }));
  await flush(cx);
  assert.equal(panel.errors.capabilities, "", "a coalesced read is not a malformed answer");
  assert.equal(panel.data.capabilities?.health_json, true);
  assert.ok(M.tabsFor(panel.caps).some((tab) => tab.id === "health"), "Health keeps its tab");
});

// ---- F3: reconcile a partly applied Save ----

test("R2-F3: a rejected command in a partly applied Save names the keys and refreshes the team", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE, second] } });
  const read = reader();
  let failed = false;
  let rolesAfterFailure = 0;
  H.state.exec = async (name, args) => {
    if (args[0] === "roles" && failed) rolesAfterFailure += 1;
    if (args[0] === "config" && args[2] === "role.second.model=original") {
      failed = true;
      throw new Error("machine unreachable");
    }
    return read(name, args);
  };
  panel.openEdit(M.rowCapture(panel.originOf("team"), { role: "second" }), cx);
  await flush(cx);
  await panel.saveEdit(cx, panel.edit.id, panel.edit.snapshot);
  await flush(cx);
  assert.equal(panel.errors.config,
    "Applied role.second.kind; rejected role.second.model: machine unreachable; not tried role.second.effort.");
  assert.equal(panel.busy.has("edit-save"), false);
  assert.ok(rolesAfterFailure > 0, "a roles read follows the rejected command");
});

// ---- F4: one token per confirmation dialog ----

test("R2-F4: an earlier dialog's confirmation cannot release the worker of the dialog that replaced it", async () => {
  const cx = fakeCx();
  const a = { name: "a-1", pane: "%1", status: "done", report: "ready" };
  const b = { name: "b-1", pane: "%2", status: "done", report: "ready" };
  const panel = await boot(cx, { reads: { roster: { agents: [a, b] } } });
  panel.data.roster = M.normalizeRoster({ agents: [a, b] });

  panel.confirmClose(M.workerCapture(panel.originOf("roster"), a), cx);
  const older = H.state.overlays[0].props.command.name.split(".")[1];
  panel.confirmClose(M.workerCapture(panel.originOf("roster"), b), cx);
  const current = H.state.overlays[1].props.command.name.split(".")[1];
  assert.match(older, /^close_[0-9]+$/);
  assert.notEqual(older, current, "each dialog carries its own token");
  panel.handleCommand(older, cx);
  await flush(cx);
  assert.deepEqual(releases(), [], "the replaced dialog's suffix is ignored");
  panel.handleCommand(current, cx);
  await flush(cx);
  assert.deepEqual(releases().map((call) => call.args), [["release", "b-1", "--close"]]);

  // A suffix of another context, and one of another Core address, are ignored.
  const before = releases().length;
  panel.confirmClose(M.workerCapture(panel.originOf("roster"), a), cx);
  const suffix = H.state.overlays.at(-1).props.command.name.split(".")[1];
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  panel.handleCommand(suffix, cx);
  await flush(cx);
  assert.equal(releases().length, before, "the context changed under the dialog");
  panel.confirmClose(M.workerCapture(panel.originOf("roster"), a), cx);
  const moved = H.state.overlays.at(-1).props.command.name.split(".")[1];
  panel.data.roster = M.normalizeRoster({ agents: [{ ...a, pane: "%9" }] });
  panel.handleCommand(moved, cx);
  await flush(cx);
  assert.equal(releases().length, before, "the Core address changed under the dialog");
});

// ---- F5: the buttons use the controller's busy key ----

test("R2-F5: Close and Reopen are disabled by the same worker key the controller marks busy", async () => {
  const cx = fakeCx();
  const done = { name: "a-1", pane: "%1", status: "done", report: "ready" };
  const gone = { name: "b-1", pane: "%2", status: "gone" };
  const panel = await boot(cx, { reads: { roster: { agents: [done, gone] } } });
  panel.data.roster = M.normalizeRoster({ agents: [done, gone] });
  panel.band = "medium";
  panel.tab = "now";

  const held = [];
  const read = reader({ roster: { agents: [done, gone] } });
  H.state.exec = async (name, args) => {
    if (args[0] === "release" || args[0] === "reopen") {
      return new Promise((resolve) => { held.push(resolve); });
    }
    return read(name, args);
  };
  const suffix = () => H.state.overlays.at(-1).props.command.name.split(".")[1];
  panel.confirmClose(M.workerCapture(panel.originOf("roster"), done), cx);
  panel.handleCommand(suffix(), cx);
  panel.confirmReopen(M.workerCapture(panel.originOf("roster"), gone), cx);
  panel.handleCommand(suffix(), cx);
  assert.equal(held.length, 2, "both commands are in flight");
  assert.equal(panel.isBusy(`close:${M.workerKey(done)}`), true);
  assert.equal(panel.isBusy(`reopen:${M.workerKey(gone)}`), true);
  const root = panel.render(cx);
  assert.equal(findRecord(root, `close-${M.workerKey(done)}`).handlers.on_click, undefined,
    "the rendered Close is disabled while its write runs");
  assert.equal(findRecord(root, `reopen-${M.workerKey(gone)}`).handlers.on_click,
    undefined, "the stopped worker's Reopen is disabled too while its write runs");
  for (const resolve of held) resolve(ok({}));
  await flush(cx);

  // The same keys come back as buttons once the write is over.
  assert.equal(typeof findRecord(panel.render(cx), `close-${M.workerKey(done)}`).handlers.on_click,
    "function");
});

// ---- F6: no preview, no Save for a draft with no fields ----

test("R2-F6: a draft whose fields are all cleared has no snapshot, no Save and no write", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE] } });
  panel.openEdit(M.rowCapture(panel.originOf("team"), { role: "implementer" }), cx);
  await flush(cx);
  assert.ok(panel.edit.snapshot, "the opened draft has a snapshot");
  panel.setEditField("kind", "", panel.edit.id, cx);
  panel.setEditField("model", "", panel.edit.id, cx);
  panel.setEditField("effort", "", panel.edit.id, cx);
  await flush(cx);
  assert.deepEqual(panel.edit.preview, []);
  assert.equal(panel.edit.snapshot, null, "no validated snapshot without fields");
  assert.equal(M.previewState(panel.edit).canSave, false);
  const root = panel.render(cx);
  assert.equal(findRecord(root, "edit-save"), undefined, "no Save is drawn for an empty draft");
  assert.match(textOf(root), /Choose a value to preview the config commands/);
  await panel.saveEdit(cx, panel.edit.id, panel.edit.snapshot);
  await flush(cx);
  assert.deepEqual(configWrites(), []);
});

// ---- F7: a report diagnostic follows the selection ----

test("R2-F7: a late report failure for the worker that lost the selection is discarded", async () => {
  const cx = fakeCx();
  const a = { name: "a-1", pane: "%1", status: "working" };
  const b = { name: "b-1", pane: "%2", status: "working" };
  const panel = await boot(cx, { reads: { roster: { agents: [a, b] } } });
  panel.data.roster = M.normalizeRoster({ agents: [a, b] });
  panel.caps = M.capabilities({ report_json: true });
  const pending = new Map();
  const read = reader();
  H.state.exec = async (name, args) => {
    if (args[0] === "report") return new Promise((resolve) => pending.set(args[1], resolve));
    return read(name, args);
  };
  panel.openReport(M.workerCapture(panel.originOf("roster"), a), cx);
  panel.openReport(M.workerCapture(panel.originOf("roster"), b), cx);
  await tick();
  assert.equal(pending.size, 2, "both reads started");
  pending.get("b-1")(ok({ agent: "b-1", items: [{ line: 1, mark: "done", text: "B report" }] }));
  await tick();
  await flush(cx);
  pending.get("a-1")({ code: 1, stdout: "", stderr: "A-only diagnostic" });
  await flush(cx);
  assert.equal(panel.errors.report, "", "the stale diagnostic never reaches the shared slot");
  const shown = textOf(panel.render(cx));
  assert.match(shown, /B report/);
  assert.doesNotMatch(shown, /A-only diagnostic/);
});

// ---- F8: a same-context refresh keeps the detail views ----

test("R2-F8: a same-context refresh keeps Report and Config open with their data", async () => {
  const cx = fakeCx();
  const w = { name: "w-1", pane: "%1", status: "done", report: "ready" };
  const panel = await boot(cx, { reads: { roster: { agents: [w] } } });
  panel.data.roster = M.normalizeRoster({ agents: [w] });

  panel.openReport(M.workerCapture(panel.originOf("roster"), w), cx);
  await flush(cx);
  assert.equal(panel.tab, "report");
  panel.refresh(cx);
  await flush(cx);
  assert.equal(panel.tab, "report", "Report survives an ordinary refresh");
  assert.ok(panel.data.report, "the report data is still there");

  panel.data.team = { roles: [ROLE], config_files: { project: "/w/a/.agents/soho.conf" } };
  H.state.readText = () => "project settings";
  await panel.openConfig({ origin: panel.originOf("team"), path: panel.configFile("project") }, cx);
  assert.equal(panel.tab, "config");
  panel.refresh(cx);
  await flush(cx);
  assert.equal(panel.tab, "config", "Config survives an ordinary refresh");
  assert.equal(panel.data.configText, "project settings");
});

// ---- F9: assistant and probe diagnostics where they belong ----

test("R2-F9: a per-kind model error renders beside its kind, not only in the error slot", async () => {
  const cx = fakeCx();
  const cell = (kind, exe, family, effort, installed, mcp) =>
    `${kind.padEnd(8)} ${exe.padEnd(13)} ${family.padEnd(10)} ${effort.padEnd(8)} ${installed.padEnd(9)} ${mcp}`;
  const kinds = [
    cell("KIND", "EXECUTABLE", "FAMILY", "EFFORT", "INSTALLED", "MCP"),
    cell("cursor", "cursor-agent", "by model", "xhigh", "yes", "yes"),
    cell("pi", "pi", "by model", "max", "yes", "no"),
  ].join("\n");
  const read = reader({ kinds, capabilities: { schema: 1, team_json: true } });
  const exec = async (name, args) => {
    if (args[0] === "models" && args[1] === "cursor") {
      return { code: 2, stdout: "", stderr: "cursor model list failed" };
    }
    return read(name, args);
  };
  const curator = { role: "curator", lane: "", kind: "cursor", model: "c-1", effort: "low", mode: "edit" };
  const panel = await boot(cx, { exec, reads: { kinds, capabilities: { schema: 1, team_json: true } }, team: { roles: [curator] } });
  assert.match(panel.errors[M.modelsErrorKey("cursor")], /cursor model list failed/);
  assert.equal(panel.errors[M.modelsErrorKey("pi")], "", "the other kind's lookup succeeded");

  const assistants = V.renderAssistants(panel, T, cx);
  assert.match(textOf(findRecord(assistants, "assistant-cursor")), /cursor model list failed/);
  assert.doesNotMatch(textOf(findRecord(assistants, "assistant-pi")), /cursor model list failed/,
    "the diagnostic stays beside its own kind");

  panel.openEdit(M.rowCapture(panel.originOf("team"), { role: "curator" }), cx);
  await flush(cx);
  assert.match(textOf(V.renderEdit(panel, T, cx)), /cursor model list failed/);
});

test("R2-F9: a Probe again failure started in Edit renders inside that form", async () => {
  const cx = fakeCx();
  const capable = { schema: 1, team_json: true, assistants_json: true };
  const read = reader({ capabilities: capable });
  const exec = async (name, args) => {
    if (args[0] === "setup") return { code: 1, stdout: "", stderr: "probe failed: no login" };
    return read(name, args);
  };
  const panel = await boot(cx, { exec, reads: { capabilities: capable }, team: { roles: [ROLE] } });
  panel.openEdit(M.rowCapture(panel.originOf("team"), { role: "implementer" }), cx);
  await flush(cx);
  assert.deepEqual(panel.editAssistants(), [], "no assistant answered yet");
  await panel.probeAgain({ kind: "edit", draftId: panel.edit.id, origin: panel.edit.origin }, cx);
  await flush(cx);
  assert.equal(panel.errors.assistants, "", "the tab's error slot is untouched");
  assert.match(panel.errors[M.probeEditKey(panel.edit.id)], /probe failed/);
  assert.match(textOf(V.renderEdit(panel, T, cx)), /probe failed: no login/);
});

// ---- F10: an unexpected rejection clears the neutral note ----

test("R2-F10: an unexpected rejected roster read clears the neutral note and shows the error", async () => {
  const cx = fakeCx();
  const refusal = "soho: not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside";
  const read = reader();
  let mode = "refuse";
  const exec = async (name, args) => {
    if (args[0] === "roster") {
      if (mode === "refuse") return { code: 1, stdout: "", stderr: refusal };
      throw new Error("machine unreachable");
    }
    return read(name, args);
  };
  const panel = await boot(cx, { exec });
  assert.equal(panel.notes.roster, M.ROSTER_REFUSAL);
  assert.equal(panel.errors.roster, "");
  mode = "throw";
  panel.refresh(cx);
  await flush(cx);
  assert.equal(panel.notes.roster, "", "the note it contradicts is gone");
  assert.equal(panel.errors.roster, "machine unreachable");
  panel.tab = "now";
  const shown = textOf(panel.render(cx));
  assert.match(shown, /machine unreachable/);
  assert.doesNotMatch(shown, /cannot list workers from the panel yet/);
});

// ---- F11: Probe again only where the controller can run it ----

function emptyEditor(caps) {
  const edit = {
    appliesTo: "role", lane: "build", laneLabel: "Build", role: "implementer", roleCount: 1,
    kind: "", model: "", effort: "low", scope: "project", preview: [], pending: false, valid: true, error: "",
    origin: { spaceId: "space-a", machine: "m1", folder: "/w/a", seq: 1 },
  };
  edit.snapshot = null;
  return {
    band: "wide", tab: "team", busy: new Set(), errors: {}, menuOpen: "", menuFocus: {},
    modelInput: null, edit, caps,
    data: { choices: { assistants: [] } },
    editAssistants: () => [],
    probeAgain: () => {}, openMenu: () => {}, setEditField: () => {}, setEditAppliesTo: () => {},
    saveEdit: () => {}, closeEdit: () => {},
  };
}

test("R2-F11: Probe again exists only where the controller can run it", async () => {
  const cx = fakeCx();
  const legacy = flatten(V.renderEdit(emptyEditor(M.capabilities({})), T, cx));
  assert.equal(legacy.ids.includes("edit-probe"), false, "no dead trigger in the legacy empty state");
  assert.match(legacy.strings.join(" | "), /No assistant installed/);
  const capable = flatten(V.renderEdit(emptyEditor(M.capabilities({ assistants_json: true })), T, cx));
  assert.ok(capable.ids.includes("edit-probe"), "the capable empty state offers the probe");
  assert.match(capable.strings.join(" | "), /No assistant has answered yet/);

  // The controller refuses the probe it cannot run, and runs it where it can.
  const read = reader({ capabilities: { schema: 1, team_json: true } });
  const panel = await boot(cx, { reads: { capabilities: { schema: 1, team_json: true } }, team: { roles: [ROLE] } });
  panel.openEdit(M.rowCapture(panel.originOf("team"), { role: "implementer" }), cx);
  await flush(cx);
  H.state.exec = read;
  const before = H.state.calls.filter((call) => call.args?.[0] === "setup").length;
  panel.probeAgain(null, cx);
  await flush(cx);
  assert.equal(H.state.calls.filter((call) => call.args?.[0] === "setup").length, before);
});

// ---- F12: the documentation matches the tab selector ----

test("R2-F12: the README says the Health tab is hidden without health_json", () => {
  const readme = readFileSync(new URL("../README.md", import.meta.url), "utf8");
  assert.match(readme, /without `health_json` the Health tab is hidden/);
  assert.doesNotMatch(readme, /Health view says that the version cannot report health/);
  assert.match(readme, /Health is in the tab bar only when the version reports `health_json`/);
});

// ---- F13: the partial-failure test counts the reconciliation read ----

test("R2-F13: a new roles request follows the failure, in both partial-failure paths", async () => {
  for (const mode of ["nonzero", "reject"]) {
    const cx = fakeCx();
    const panel = await boot(cx, { team: { roles: [ROLE, second] } });
    const read = reader();
    let failed = false;
    let rolesAfterFailure = 0;
    H.state.exec = async (name, args) => {
      if (args[0] === "roles" && failed) rolesAfterFailure += 1;
      if (args[0] === "config" && args[2] === "role.second.model=original") {
        failed = true;
        if (mode === "reject") throw new Error("bridge down");
        return { code: 2, stdout: "", stderr: "invalid value 'original'" };
      }
      return read(name, args);
    };
    panel.openEdit(M.rowCapture(panel.originOf("team"), { role: "second" }), cx);
    await flush(cx);
    const before = rolesReads().length;
    await panel.saveEdit(cx, panel.edit.id, panel.edit.snapshot);
    await flush(cx);
    assert.ok(rolesAfterFailure > 0, `${mode}: a roles read follows the failure`);
    assert.ok(rolesReads().length > before, `${mode}: the reconciliation read reached the CLI`);
    assert.match(panel.errors.config,
      /^Applied role\.second\.kind; rejected role\.second\.model: .+; not tried role\.second\.effort\.$/);
  }
});
