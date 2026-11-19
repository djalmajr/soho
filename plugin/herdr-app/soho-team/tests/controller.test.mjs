// Controller-level checks: the fixes of findings F1, F2, F3, F4, F6, F9, F13 and
// F18 are wired into main.js, not only present in model.js. The loader maps the
// Herdr built-ins to recording stubs so the real view class runs headless.
import assert from "node:assert/strict";
import test from "node:test";
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
const spaceA = () => ({ id: "space-a", machine: "m1", folder: "/w/a", active: true });
const spaceB = () => ({ id: "space-b", machine: null, folder: "/w/b", active: true });

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

function useFolder(space, folder) {
  H.state.spaces = [space];
  H.state.focused = { space: space.id, cwd: folder };
}

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
    spawn(body) {
      const promise = Promise.resolve().then(() => body(ctx));
      ctx.tasks.push(promise);
      return promise;
    },
  };
  return ctx;
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

async function flush(cx) {
  for (let i = 0; i < 50; i += 1) {
    const pending = cx.tasks.splice(0);
    if (pending.length) await Promise.allSettled(pending);
    // A promise chain started by hand (showWorkerReport) advances one microtask
    // per await; give each round a macrotask to drain them.
    await tick();
    if (!pending.length && cx.tasks.length === 0) return;
  }
}

async function boot(cx, options = {}) {
  H.reset();
  if (typeof localStorage !== "undefined" && localStorage.store) localStorage.clear();
  if (options.prefs) localStorage.setItem("soho.prefs", JSON.stringify(options.prefs));
  useFolder(options.space ?? spaceA(), options.folder ?? "/w/a");
  H.state.exec = options.exec ?? reader(options.reads ?? {});
  const panel = new SohoPanel();
  panel.init({}, cx);
  await flush(cx);
  panel.data.team = options.team ?? { roles: [ROLE] };
  return panel;
}

function readyEdit(context, previousByKey = {}) {
  const edit = {
    appliesTo: "role", lane: "build", laneLabel: "Build", role: "implementer",
    kind: "pi", model: "m-1", effort: "high", scope: "project",
    pending: false, valid: true, context,
  };
  edit.snapshot = M.snapshotFor(edit, M.previewFromDryRuns(null, M.editFields(edit), "project"),
    previousByKey, "/w/a/.agents/soho.conf", context);
  return edit;
}

test("F1 controller: a context change drops the open edit and refuses its save", async () => {
  const cx = fakeCx();
  const panel = await boot(cx);
  panel.openEdit({ role: "implementer" }, cx);
  await flush(cx);
  assert.equal(panel.edit.context.folder, "/w/a");
  assert.equal(panel.edit.context.machine, "m1");

  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  assert.equal(panel.edit, null);
  assert.equal(panel.noticeItem?.text, M.CONTEXT_CHANGED_MESSAGE);

  const stale = readyEdit(M.contextFor(spaceA(), "/w/a"));
  panel.edit = stale;
  const before = H.state.calls.filter((call) => call.args?.[0] === "config").length;
  await panel.saveEdit(cx);
  await flush(cx);
  assert.equal(panel.errors.config, M.CONTEXT_CHANGED_MESSAGE);
  assert.equal(H.state.calls.filter((call) => call.args?.[0] === "config").length, before);
});

test("F1 controller: the same folder path on another machine is still a context change", async () => {
  const cx = fakeCx();
  const panel = await boot(cx);
  panel.openEdit({ role: "implementer" }, cx);
  await flush(cx);
  assert.equal(panel.edit.context.machine, "m1");
  // Same space id and folder path, another machine: the review's second probe.
  useFolder({ id: "space-a", machine: "m2", folder: "/w/a", active: true }, "/w/a");
  const readsBefore = H.state.calls.length;
  panel.render(cx);
  await flush(cx);
  assert.equal(panel.context.machine, "m2");
  assert.equal(panel.edit, null);
  assert.ok(H.state.calls.length > readsBefore, "the new machine's data was read");
});

test("F2 controller: a refresh during a save neither abandons it nor hides a partial failure", async () => {
  const cx = fakeCx();
  const panel = await boot(cx);
  const writes = [];
  const read = reader();
  H.state.exec = async (name, args) => {
    if (args[0] === "config" && args[1] === "set") {
      writes.push(args.join(" "));
      if (writes.length === 1) {
        panel.refresh(cx); // the read sequence advances mid-save
        return ok({});
      }
      return { code: 2, stdout: "", stderr: "invalid value 'high'" };
    }
    return read(name, args);
  };
  panel.edit = readyEdit(M.contextFor(spaceA(), "/w/a"));
  const notifiesBefore = cx.notifies;
  await panel.saveEdit(cx);
  await flush(cx);
  assert.equal(writes.length, 2, "the second write still runs after the refresh");
  // Every write is one literal key=value operand followed by the destination.
  assert.deepEqual(H.state.calls.filter((call) => call.args?.[0] === "config").map((call) => call.args),
    [["config", "set", "role.implementer.kind=pi", "--project"],
      ["config", "set", "role.implementer.model=m-1", "--project"]]);
  assert.match(panel.errors.config, /^Applied role\.implementer\.kind; not applied role\.implementer\.model, role\.implementer\.effort\. invalid value 'high'$/);
  assert.equal(panel.busy.has("edit-save"), false);
  assert.ok(cx.notifies > notifiesBefore);
  assert.ok(H.state.calls.some((call) => call.args?.join(" ") === "roles --json"), "the refresh ran");
});

test("F3 controller: Undo runs the clicked unset plan even after the notice expires", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, {
    reads: { capabilities: { schema: 1, team_json: true, roster_workers: true, config_unset: true } },
  });
  panel.edit = readyEdit(M.contextFor(spaceA(), "/w/a"), {
    "role.implementer.kind": { in_file: null },
    "role.implementer.model": { in_file: "old-model" },
    "role.implementer.effort": { in_file: "low" },
  });
  await panel.saveEdit(cx);
  await flush(cx);
  assert.equal(panel.undoHolder.plans[0].args.join(" "), "config unset role.implementer.kind --project");

  const read = reader();
  H.state.exec = async (name, args) => {
    if (args[0] === "config" && args[1] === "unset") {
      // The notice expires while the command is in flight.
      panel.noticeItem = null;
      panel.undoHolder = null;
      return ok({});
    }
    return read(name, args);
  };
  await panel.undoLast(cx);
  await flush(cx);
  assert.equal(H.state.calls.filter((call) => call.args?.join(" ") === "config unset role.implementer.kind --project").length, 1);
  assert.equal(panel.noticeItem?.text, "Undid role.implementer.kind");
  assert.equal(panel.busy.has("undo"), false);
});

test("F4 controller: the suffix-only confirmation runs release only for the pending row", async () => {
  const cx = fakeCx();
  const worker = { name: "implementer-1", pane: "%3", status: "done", report: "ready" };
  const panel = await boot(cx, { reads: { roster: { agents: [worker] } } });
  panel.data.roster = M.normalizeRoster({ agents: [worker] });

  panel.confirmClose(worker, cx);
  assert.equal(panel.pending.action, "close");
  panel.handleCommand("close", cx);
  await flush(cx);
  assert.equal(H.state.calls.filter((call) => call.args?.[0] === "release").length, 1);
  assert.deepEqual(H.state.calls.find((call) => call.args?.[0] === "release").args, ["release", "implementer-1", "--close"]);

  const before = H.state.calls.filter((call) => call.args?.[0] === "release").length;
  panel.confirmClose(worker, cx);
  panel.handleCommand("reopen", cx); // a different action
  await flush(cx);
  panel.confirmReopen(worker, cx);
  panel.data.roster = { agents: [{ name: "other-1", pane: "%9", status: "gone" }] }; // the row changed
  panel.handleCommand("close", cx);
  await flush(cx);
  assert.equal(H.state.calls.filter((call) => call.args?.[0] === "release").length, before);
});

test("F6 controller: the typed model reaches the draft and the preview", async () => {
  const cx = fakeCx();
  const panel = await boot(cx);
  panel.openEdit({ role: "implementer" }, cx);
  await flush(cx);
  panel.modelInput.type("model with spaces", cx);
  await flush(cx);
  assert.equal(panel.edit.model, "model with spaces");
  assert.ok(panel.edit.preview.includes("soho config set role.implementer.model='model with spaces' --project"));
  assert.equal(panel.modelInput.value(), "model with spaces");
});

test("F9 controller: a late report for the previous worker is discarded", async () => {
  const cx = fakeCx();
  const w1 = { name: "implementer-1", pane: "%1", status: "working" };
  const w2 = { name: "reviewer-1", pane: "%2", status: "working" };
  const panel = await boot(cx, { reads: { roster: { agents: [w1, w2] } } });
  panel.data.roster = M.normalizeRoster({ agents: [w1, w2] });
  panel.caps = M.capabilities({ report_json: true });

  const pending = new Map();
  const read = reader();
  H.state.exec = async (name, args) => {
    if (args[0] === "report") {
      return new Promise((resolve) => pending.set(args[1], resolve));
    }
    return read(name, args);
  };
  panel.showWorkerReport(w1, cx);
  panel.showWorkerReport(w2, cx);
  await tick();
  assert.equal(pending.size, 2, "both reads started (keyed by worker)");
  pending.get("reviewer-1")(ok({ agent: "reviewer-1" }));
  await tick();
  pending.get("implementer-1")(ok({ agent: "implementer-1" }));
  await flush(cx);
  assert.equal(panel.data.report.agent, "reviewer-1");
  assert.equal(panel.reportAgent, M.workerKey(w2));
});

test("F13 controller: a folder change restores that folder's view and sort", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { prefs: {
    "/w/a": { view: "list", sort: { column: "model", dir: "desc" } },
    "/w/b": { view: "grouped", sort: { column: "effort", dir: "asc" } },
  } });
  assert.equal(panel.view, "list");
  assert.deepEqual(panel.sort, { column: "model", dir: "desc" });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  assert.equal(panel.view, "grouped");
  assert.deepEqual(panel.sort, { column: "effort", dir: "asc" });
  useFolder({ id: "space-c", machine: null, folder: "/w/c", active: true }, "/w/c");
  panel.refresh(cx);
  await flush(cx);
  assert.equal(panel.view, "grouped");
  assert.deepEqual(panel.sort, { column: "role", dir: "asc" });
});

test("F7 controller: Save waits for the preview of the current draft and runs the snapshot", async () => {
  const cx = fakeCx();
  const panel = await boot(cx);
  const context = M.contextFor(spaceA(), "/w/a");
  const stale = readyEdit(context);
  // A preview for a changed draft is still running: no write at all.
  panel.edit = { ...stale, pending: true };
  await panel.saveEdit(cx);
  await flush(cx);
  assert.equal(H.state.calls.filter((call) => call.args?.[0] === "config").length, 0);
  assert.equal(panel.busy.has("edit-save"), false);
  // The draft no longer matches its snapshot: refused with a message.
  panel.edit = { ...stale, scope: "user" };
  await panel.saveEdit(cx);
  await flush(cx);
  assert.equal(H.state.calls.filter((call) => call.args?.[0] === "config").length, 0);
  assert.match(panel.edit.error, /out of date/);
  // The matching snapshot writes exactly the commands that were previewed.
  panel.edit = readyEdit(context);
  await panel.saveEdit(cx);
  await flush(cx);
  const written = H.state.calls.filter((call) => call.args?.[0] === "config").map((call) => call.args.join(" "));
  assert.deepEqual(written, [
    "config set role.implementer.kind=pi --project",
    "config set role.implementer.model=m-1 --project",
    "config set role.implementer.effort=high --project",
  ]);
  assert.equal(panel.edit, null);
});

test("F10 controller: a legacy roster row is normalized before the views read it", async () => {
  const cx = fakeCx();
  const legacy = { name: "implementer-1", role: "implementer", kind: "pi", pane: "%3", tab: "1", state: "working", report: "none" };
  const panel = await boot(cx, { reads: { roster: { agents: [legacy] } } });
  assert.equal(panel.data.roster.agents[0].status, "working");
  assert.deepEqual(M.workerStats(panel.data.roster.agents, 4), { working: 1, reportIn: 0, max: 4 });
});

test("F11 controller: the legacy kinds read offers every installed column row", async () => {
  const cx = fakeCx();
  const row = (kind, exe, family, effort, installed, mcp) =>
    `${kind.padEnd(8)} ${exe.padEnd(13)} ${family.padEnd(10)} ${effort.padEnd(8)} ${installed.padEnd(9)} ${mcp}`;
  const kinds = [
    row("KIND", "EXECUTABLE", "FAMILY", "EFFORT", "INSTALLED", "MCP"),
    row("cursor", "cursor-agent", "by model", "xhigh", "yes", "yes"),
    row("pi", "pi", "by model", "max", "yes", "no"),
    row("grok", "grok", "xai", "xhigh", "no", "no"),
  ].join("\n");
  const panel = await boot(cx, { reads: { kinds } });
  assert.deepEqual(panel.data.choices.assistants.map((a) => a.kind), ["cursor", "pi"]);
});

test("F12/F14 controller: a malformed capability answer keeps its stderr and the kinds read", async () => {
  const cx = fakeCx();
  const read = reader();
  H.state.exec = async (name, args) => {
    if (args.join(" ") === "capabilities --json") return { code: 0, stdout: "not json", stderr: "warn: no schema" };
    return read(name, args);
  };
  useFolder(spaceA(), "/w/a");
  const panel = new SohoPanel();
  panel.init({}, cx);
  await flush(cx);
  assert.equal(panel.caps.teamJson, false);
  assert.match(panel.errors.capabilities, /did not return JSON/);
  assert.match(panel.errors.capabilities, /warn: no schema/);
  assert.ok(H.state.calls.some((call) => call.args?.join(" ") === "kinds"), "the legacy kinds read still ran");
  assert.ok(panel.data.choices);
});

function findRecord(root, id) {
  let found = null;
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

function stopCtx() {
  return { stopped: 0, stop_propagation() { this.stopped += 1; }, notify() {} };
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

test("F5 controller: the opened role survives lane and role scope switches", async () => {
  const cx = fakeCx();
  const other = { role: "reviewer", lane: "build", kind: "codex", model: "sol", effort: "low", mode: "read-only" };
  const panel = await boot(cx, { team: { roles: [ROLE, other] } });
  panel.openEdit({ role: "reviewer" }, cx);
  await flush(cx);
  assert.equal(panel.edit.role, "reviewer");
  panel.setEditAppliesTo("lane", cx);
  await flush(cx);
  assert.equal(panel.edit.appliesTo, "lane");
  panel.setEditAppliesTo("role", cx);
  await flush(cx);
  assert.equal(panel.edit.appliesTo, "role");
  assert.equal(panel.edit.role, "reviewer");
  assert.ok(panel.edit.snapshot.fields.every((field) => field.key.startsWith("role.reviewer.")));
});

test("F15 view: List drops its header and renders the narrow rows in the narrow band", async () => {
  const cx = fakeCx();
  const opened = [];
  const app = {
    band: "narrow", view: "list", sort: { column: "role", dir: "asc" }, busy: new Set(),
    errors: {}, notes: {}, menuOpen: "", menuFocus: {}, selectedWorker: "",
    data: { team: { roles: [ROLE] }, roster: { agents: [] }, report: null, reportMarkdown: null },
    caps: M.capabilities({}),
    groups: () => M.groupRoles({ roles: [ROLE] }, {}),
    roleByName: (name) => [ROLE].find((role) => role.role === name) ?? null,
    setSort: () => {}, openEdit: (sel) => opened.push(sel), setEffort: () => {}, openMenu: () => {},
  };
  const narrowRoot = V.renderList(app, H.theme(), cx);
  const narrow = flatten(narrowRoot);
  assert.equal(narrow.ids.includes("sort-role"), false, "no fixed header in the narrow band");
  assert.ok(narrow.ids.includes("role-implementer"));
  // The row itself is the Edit target; no button is drawn (decision 1).
  assert.equal(narrow.ids.includes("role-implementer-edit"), false);
  const narrowRow = findRecord(narrowRoot, "role-implementer");
  assert.equal(typeof narrowRow.handlers.on_click, "function");
  assert.equal(narrowRow.children.length, 2, "the narrow row keeps its two lines");
  const wide = flatten(V.renderList({ ...app, band: "wide" }, H.theme(), cx));
  assert.ok(wide.ids.includes("sort-role"), "the header and its sort control return above 320 px");
  assert.equal(wide.ids.includes("role-implementer-edit"), false, "no Edit button in the wide list either");
});

test("F16 view: the cleanup confirmation lists the candidates before the button", async () => {
  const cx = fakeCx();
  const app = {
    band: "wide", tab: "health", busy: new Set(), errors: {}, cleanupConfirm: true,
    data: { health: {
      doctor: { warnings: [] },
      copies: { total: 0, rows: [] },
      processes: { unregistered_orphans: [] },
      gc: { total_bytes: 2048, would_remove: [
        { path: "/tmp/soho-copies/one", bytes: 2048, reason: "owner released" },
        { path: "/tmp/soho-copies/two", bytes: 0, state: "idle" },
      ] },
      update: {},
    } },
    setCleanupConfirm: () => {}, cleanup: () => {},
  };
  const { strings, ids } = flatten(V.renderHealth(app, H.theme(), cx));
  const text = strings.join(" | ");
  assert.match(text, /\/tmp\/soho-copies\/one · owner released/);
  assert.match(text, /\/tmp\/soho-copies\/two · idle/);
  assert.ok(ids.includes("cleanup-yes"));
});

test("F17 view: with no answering assistant, Edit says so and offers Probe again", async () => {
  const cx = fakeCx();
  const context = M.contextFor(spaceA(), "/w/a");
  const edit = readyEdit(context);
  edit.preview = [...edit.snapshot.commands];
  const app = {
    band: "wide", tab: "team", busy: new Set(), errors: {}, menuOpen: "", menuFocus: {},
    modelInput: null,
    edit,
    data: { choices: { assistants: [] } },
    editAssistants: () => [],
    probeAgain: () => {}, openMenu: () => {}, setEditField: () => {}, setEditAppliesTo: () => {},
    saveEdit: () => {}, closeEdit: () => {},
  };
  const { strings, ids } = flatten(V.renderEdit(app, H.theme(), cx));
  assert.match(strings.join(" | "), /No assistant has answered yet/);
  assert.ok(ids.includes("edit-probe"));
});

test("F18 controller: the timer polls only while the copy reported visibility", async () => {
  const cx = fakeCx();
  const panel = await boot(cx);
  assert.equal(cx.timers.every.length, 1);
  const timer = cx.timers.every[0].callback;
  const before = H.state.calls.length;
  panel.lastVisibleAt = 0;
  timer(cx);
  await tick();
  assert.equal(H.state.calls.length, before, "no read while hidden");
  panel.lastVisibleAt = Date.now();
  timer(cx);
  await flush(cx);
  assert.ok(H.state.calls.length > before, "reads while visible");
});

// ---- Round 2: the owner's feedback from the app ----

const T = {
  background: "#000001", raised: "#000002", foreground: "#000003", muted: "#000004",
  border: "#000005", input: "#000006", canvas: "#000007", surface: "#000008",
  dim: "#000009", accent: "#00000a", popover: "#00000b", solid: "#00000c",
  solid_foreground: "#00000d", hover: "#00000e", working: "#00000f",
  blocked: "#000010", unknown: "#000011", done: "#000012", ansi1: "#000013", ansi3: "#000014",
};

function teamApp(overrides = {}) {
  const rolesList = overrides.roles ?? [ROLE];
  const opened = overrides.opened ?? [];
  return {
    band: overrides.band ?? "wide",
    view: overrides.view ?? "grouped",
    tab: overrides.tab ?? "team",
    sort: { column: "role", dir: "asc" },
    busy: new Set(),
    errors: {},
    notes: {},
    menuOpen: "",
    menuFocus: {},
    modelInput: null,
    selectedWorker: "",
    firstReadDone: true,
    caps: M.capabilities(overrides.caps ?? {}),
    data: { team: { roles: rolesList }, roster: { agents: [] }, report: null, reportMarkdown: null, choices: { assistants: [] } },
    groups: () => M.groupRoles({ roles: rolesList }, {}),
    roleByName: (name) => rolesList.find((role) => role.role === name) ?? null,
    openEdit: (sel) => opened.push(sel),
    setSort: () => {}, setEffort: () => {}, openMenu: () => {}, setView: () => {}, setTab: () => {},
    probeAgain: () => {}, cleanup: () => {}, setCleanupConfirm: () => {},
    saveEdit: () => {}, closeEdit: () => {}, setEditField: () => {}, setEditAppliesTo: () => {},
  };
}

test("R1 view: a role row opens Edit for the role named in that row, in Grouped and in List", () => {
  const opened = [];
  const app = teamApp({ opened });
  const grouped = V.renderGrouped(app, T, fakeCx());
  findRecord(grouped, "role-implementer").handlers.on_click({}, stopCtx());
  assert.deepEqual(opened, [{ role: "implementer" }]);
  opened.length = 0;
  const listed = V.renderList(app, T, fakeCx());
  findRecord(listed, "role-implementer").handlers.on_click({}, stopCtx());
  assert.deepEqual(opened, [{ role: "implementer" }]);
  // The row that names the role is the one that opens it: another row opens its own.
  const second = { role: "reviewer", lane: "build", kind: "codex", model: "sol", effort: "low", mode: "read-only" };
  opened.length = 0;
  const two = teamApp({ opened, roles: [ROLE, second] });
  findRecord(V.renderGrouped(two, T, fakeCx()), "role-reviewer").handlers.on_click({}, stopCtx());
  assert.deepEqual(opened, [{ role: "reviewer" }]);
});

test("R1 view: a lane header opens Edit for that lane, and Outside lanes stays a header", () => {
  const opened = [];
  const outside = { role: "planner", lane: "", kind: "claude", model: "c", effort: "high", mode: "read-only" };
  const app = teamApp({ opened, roles: [ROLE, outside] });
  const root = V.renderGrouped(app, T, fakeCx());
  const lane = findRecord(root, "group-build");
  assert.equal(typeof lane.handlers.on_click, "function");
  lane.handlers.on_click({}, stopCtx());
  assert.deepEqual(opened, [{ lane: "build" }]);
  const outsideHeader = findRecord(root, "group-outside");
  assert.equal(outsideHeader.handlers.on_click, undefined, "no lane to edit, no trigger");
});

test("R1 view: no row or lane header renders an Edit button", () => {
  const app = teamApp({ roles: [ROLE, { role: "planner", lane: "", kind: "claude", model: "c", effort: "high", mode: "read-only" }] });
  for (const root of [V.renderGrouped(app, T, fakeCx()), V.renderList(app, T, fakeCx())]) {
    const { ids } = flatten(root);
    assert.equal(ids.some((id) => id.endsWith("-edit")), false, "no row Edit button");
    assert.equal(ids.some((id) => id.startsWith("group-edit-")), false, "no lane Edit button");
  }
});

test("R1 view: the effort select stops its click before the row", () => {
  const opened = [];
  const app = teamApp({ opened });
  const root = V.renderGrouped(app, T, fakeCx());
  const wrapper = findRecord(root, "effort-stop-implementer");
  assert.equal(typeof wrapper.handlers.on_click, "function");
  const ctx = stopCtx();
  wrapper.handlers.on_click({}, ctx);
  assert.equal(ctx.stopped, 1);
  assert.deepEqual(opened, [], "the effort click never opens Edit");
});

test("R2 view: selecting a tab changes only its background and text color", () => {
  const app = teamApp({ tab: "now", caps: { health_json: true } });
  const root = V.toolTabs(app, T, fakeCx());
  const tabs = ["team", "now", "assistants", "health"].map((id) => findRecord(root, `tab-${id}`));
  const layout = (record) => record.calls.filter(([name]) => name !== "bg" && name !== "text_color");
  for (const record of tabs) assert.deepEqual(layout(record), layout(tabs[0]), "same height, padding and font in every state");
  for (const record of tabs) {
    assert.ok(layout(record).some(([name]) => name === "justify_center"), "label centered horizontally");
    assert.ok(layout(record).some(([name]) => name === "items_center"), "label centered vertically");
  }
  const color = (record, name) => record.calls.find(([key]) => key === name)?.[1];
  assert.equal(color(tabs[1], "bg"), T.raised);
  assert.equal(color(tabs[1], "text_color"), T.foreground);
  for (const record of [tabs[0], tabs[2], tabs[3]]) {
    assert.equal(color(record, "bg"), T.background);
    assert.equal(color(record, "text_color"), T.muted);
  }
  // The same geometry in the narrow band; only the padding number changes.
  const narrow = V.toolTabs(teamApp({ tab: "team", band: "narrow" }), T, fakeCx());
  const narrowTeam = findRecord(narrow, "tab-team");
  const narrowNow = findRecord(narrow, "tab-now");
  assert.deepEqual(layout(narrowTeam), layout(narrowNow));
  assert.ok(layout(narrowTeam).some(([name, value]) => name === "px" && value === 8));
});

test("R3 controller: the roster refusal is a neutral note, not an error", async () => {
  const cx = fakeCx();
  const read = reader();
  const refusals = [
    "soho: not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside",
    "soho: not inside an hc terminal (HC_TERMINAL_ID is not set); refusing to control a session from outside",
  ];
  for (const stderr of refusals) {
    H.state.exec = async (name, args) => {
      if (args.join(" ") === "roster --json") return { code: 1, stdout: "", stderr: `${stderr}\n` };
      return read(name, args);
    };
    useFolder(spaceA(), "/w/a");
    const panel = new SohoPanel();
    panel.init({}, cx);
    await flush(cx);
    assert.equal(panel.notes.roster, M.ROSTER_REFUSAL);
    assert.equal(panel.errors.roster, "", "the red error box is not used");
    assert.equal(panel.data.roster, null);
  }
});

test("R3 view: the refusal note hides the tiles and the empty line", () => {
  const app = teamApp({ tab: "now" });
  app.notes = { roster: M.ROSTER_REFUSAL };
  const { strings, ids } = flatten(V.renderNow(app, T, fakeCx()));
  const text = strings.join(" | ");
  assert.match(text, /This soho cannot list workers from the panel yet\./);
  assert.equal(ids.some((id) => id.startsWith("stat-")), false, "no counter tiles");
  assert.doesNotMatch(text, /No workers in this project\./);
  // With no note the tiles return, and only a positive max is shown.
  app.notes = {};
  app.data.roster = { agents: [], max_workers: 0 };
  const plain = flatten(V.renderNow(app, T, fakeCx()));
  assert.ok(plain.ids.includes("stat-working"));
  assert.equal(plain.ids.includes("stat-max"), false);
  app.data.roster = { agents: [], max_workers: 4 };
  assert.ok(flatten(V.renderNow(app, T, fakeCx())).ids.includes("stat-max"));
});

test("R4 view: Health without an answer and without an error is neutral", () => {
  const app = teamApp({ tab: "health" });
  app.data.health = null;
  const neutral = flatten(V.renderHealth(app, T, fakeCx()));
  assert.match(neutral.strings.join(" | "), /Reading health/);
  assert.equal(neutral.ids.some((id) => id.startsWith("err-")), false);
  app.errors = { health: "soho health --json did not return JSON" };
  assert.ok(flatten(V.renderHealth(app, T, fakeCx())).ids.some((id) => id.startsWith("err-")));
});

test("R4 controller: losing health_json while Health is selected falls back to Team", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, {
    reads: { capabilities: { schema: 1, team_json: true, roster_workers: true, health_json: true } },
  });
  panel.tab = "health";
  assert.equal(M.resolveTab(panel.tab, panel.caps), "health");
  H.state.exec = reader({ capabilities: { schema: 1, team_json: true, roster_workers: true } });
  panel.refresh(cx);
  await flush(cx);
  assert.equal(panel.tab, "team");
  assert.equal(panel.errors.health, "");
});
