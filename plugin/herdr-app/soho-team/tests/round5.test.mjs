// Round 5: one test per finding F1 ... F5 of panel-mod-r4-20261119T145557.md.
// Each case replays the reviewer's residual scenario through the rendering
// callbacks the panel actually installs, then asserts the outcome the parent
// commit had for the same events (F4, F5) or the identity the button displayed
// (F1, F2, F3).
import assert from "node:assert/strict";
import test from "node:test";
import { register } from "node:module";
import * as M from "../model.js";
import * as H from "./stubs/herdr.mjs";
register("./stubs/loader.mjs", import.meta.url);
const { default: SohoPanel } = await import("../main.js");

globalThis.localStorage = {
  store: new Map(),
  getItem(key) { return this.store.has(key) ? this.store.get(key) : null; },
  setItem(key, value) { this.store.set(key, String(value)); },
  clear() { this.store.clear(); },
};

const ROLE = { role: "implementer", lane: "build", kind: "pi", model: "m-1", effort: "high", mode: "edit" };
const SECOND = { role: "second", lane: "build", kind: "pi", model: "original", effort: "low", mode: "edit" };
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
    spawn(body) {
      const promise = Promise.resolve().then(() => body(ctx));
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

const click = (panel, cx, id) => {
  const handler = findRecord(panel.render(cx), id)?.handlers.on_click;
  assert.equal(typeof handler, "function", `${id} renders a click handler`);
  return handler;
};

// The bridge records the soho command as `name: "soho"` plus its argv.
const execCalls = () => H.state.calls.filter((call) => call.name === "soho");
const configCalls = () => execCalls().filter((call) => call.args?.[0] === "config");
const fileReads = () => H.state.calls.filter((call) => call.name === "readTextFile");
const isDryRun = (call) => call.args.includes("--dry-run");

// ---- F1: Save is bound to the snapshot the button displayed ----

test("R4-F1: a retained Save writes no newer preview of its own draft", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, {
    reads: { capabilities: { schema: 1, team_json: true, config_dry_run: true } },
    team: { roles: [ROLE, SECOND] },
  });
  click(panel, cx, "role-second")({}, cx);
  await flush(cx);
  assert.ok(panel.edit?.snapshot, "the opened draft validated a snapshot");

  // The handler the Save button installs while it displays the `low` preview.
  const retainedSave = click(panel, cx, "edit-save");
  click(panel, cx, "edit-effort-high")({}, cx);
  await flush(cx);
  assert.equal(panel.edit.snapshot.fields.find((f) => f.key.endsWith(".effort"))?.value, "high",
    "the draft previewed the newer effort within the same draft");
  const before = configCalls().length;

  retainedSave({}, cx);
  await flush(cx);
  assert.deepEqual(configCalls().slice(before).map((call) => call.args), [],
    "the retained Save sent no config command for the newer preview");
  assert.match(String(panel.edit?.error ?? ""), /out of date/,
    "the draft says why the save did not run");
  assert.ok(panel.edit, "the draft stays open");

  // The control: the Save this render installs writes the preview it shows.
  const current = configCalls().length;
  click(panel, cx, "edit-save")({}, cx);
  await flush(cx);
  const writes = configCalls().slice(current).filter((call) => !isDryRun(call)).map((call) => call.args);
  assert.ok(writes.some((args) => args[2] === "role.second.effort=high"),
    `the current Save wrote the shown value: ${JSON.stringify(writes)}`);
});

// ---- F2: a rendered action keeps the origin and target it displayed ----

test("R4-F2: a retained Clean up opener arms no confirmation after the project changed", async () => {
  const cx = fakeCx();
  const caps = { schema: 1, team_json: true, health_json: true };
  const health = { gc: { would_remove: [{ path: "/w/old-copy", bytes: 5, reason: "released" }] } };
  const panel = await boot(cx, { reads: { capabilities: caps, health } });
  panel.setTab("health", cx);
  const fromA = click(panel, cx, "cleanup-ask");

  H.state.exec = reader({ capabilities: caps, health: { gc: { would_remove: [{ path: "/w/b/old-copy", bytes: 5, reason: "released" }] } } });
  useFolder(spaceB(), "/w/b");
  panel.refresh(cx);
  await flush(cx);
  panel.setTab("health", cx);
  assert.ok(findRecord(panel.render(cx), "cleanup-ask"), "B offers its own Clean up");

  fromA({}, cx);
  assert.equal(panel.cleanupConfirm, null, "A's opener armed nothing in B");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");

  // The control: B's own opener arms the confirmation it renders.
  click(panel, cx, "cleanup-ask")({}, cx);
  assert.ok(panel.cleanupConfirm, "B's own opener armed its confirmation");
});

test("R4-F2: a retained Open config opens only the file its footer displayed", async () => {
  const cx = fakeCx();
  let path = "/w/a/.agents/soho.conf";
  H.state.readText = () => "settings";
  const panel = await boot(cx, {
    exec: async (name, args) => (args[0] === "roles"
      ? ok({ roles: [ROLE], config_files: { project: path } })
      : reader()(name, args)),
  });
  const fromA = click(panel, cx, "open-config");

  path = "/w/a/.agents/new.conf";
  panel.refresh(cx);
  await flush(cx);
  assert.match(textOf(panel.render(cx)), /new\.conf/, "the footer shows the new file");
  const before = fileReads().length;

  fromA({}, cx);
  await flush(cx);
  assert.equal(fileReads().length, before, "the retained footer read no file at all");
  assert.notEqual(panel.tab, "config", "the retained footer did not open a file view");
  assert.ok(findRecord(panel.render(cx), "soho-notice"), "the refusal is visible");

  // The control: the footer this render installs opens the path it displays.
  click(panel, cx, "open-config")({}, cx);
  await flush(cx);
  assert.equal(fileReads().at(-1)?.args?.[0], "/w/a/.agents/new.conf");
  assert.equal(panel.tab, "config");
});

test("R4-F2: a retained Cancel dismisses only the confirmation it rendered", async () => {
  const cx = fakeCx();
  const caps = { schema: 1, team_json: true, health_json: true };
  const health = { gc: { would_remove: [{ path: "/w/a/old-copy", bytes: 5, reason: "released" }] } };
  const panel = await boot(cx, { reads: { capabilities: caps, health } });
  panel.setTab("health", cx);
  click(panel, cx, "cleanup-ask")({}, cx);
  const first = panel.cleanupConfirm?.token;
  assert.ok(first, "the first confirmation is pending");
  const retainedCancel = click(panel, cx, "cleanup-no");

  // The dialog it rendered dismisses itself, and a newer one takes its place.
  click(panel, cx, "cleanup-no")({}, cx);
  assert.equal(panel.cleanupConfirm, null);
  click(panel, cx, "cleanup-ask")({}, cx);
  const current = panel.cleanupConfirm?.token;
  assert.ok(current);
  assert.notEqual(current, first, "the newer confirmation carries its own token");

  retainedCancel({}, cx);
  assert.equal(panel.cleanupConfirm?.token, current, "the older Cancel dismissed nothing");

  // The control: the Cancel this render installs dismisses its own dialog.
  click(panel, cx, "cleanup-no")({}, cx);
  assert.equal(panel.cleanupConfirm, null);
});

// ---- F3: a coalesced read publishes with the request that fetched it ----

test("R4-F3: a refresh joining a pre-write poll does not restore the pre-write team", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, {
    reads: { capabilities: { schema: 1, team_json: true, config_dry_run: true } },
    team: { roles: [ROLE, SECOND] },
  });
  click(panel, cx, "role-second")({}, cx);
  await flush(cx);
  click(panel, cx, "edit-effort-high")({}, cx);
  await flush(cx);
  assert.equal(panel.edit?.snapshot?.fields.find((f) => f.key.endsWith(".effort"))?.value, "high",
    "the draft validated the effort it will save");

  let releasePoll;
  let rolesReads = 0;
  let effort = "low";
  H.state.exec = async (name, args) => {
    if (args[0] === "roles") {
      rolesReads += 1;
      if (rolesReads === 1) return new Promise((resolve) => { releasePoll = resolve; });
      return ok({ roles: [ROLE, { ...SECOND, effort }] });
    }
    if (args[0] === "config" && args[2] === "role.second.effort=high") {
      effort = "high";
      return ok({});
    }
    return reader()(name, args);
  };

  // The poll admitted before the write holds its roles read.
  panel.refresh(cx);
  await tick();
  assert.equal(rolesReads, 1, "the pre-write poll is in flight");
  click(panel, cx, "edit-save")({}, cx);
  await flush(cx);
  assert.equal(panel.roleByName("second")?.effort, "high", "the reconcile published the write");

  // A second poll joins the request admitted before the write.
  panel.refresh(cx);
  await flush(cx);
  assert.equal(rolesReads, 2, "the joining refresh shared the held roles read");
  releasePoll(ok({ roles: [ROLE, SECOND] }));
  await flush(cx);
  assert.equal(panel.roleByName("second")?.effort, "high",
    "the older response did not overwrite the newer reconcile");
});

test("R4-F3: an older failing poll does not repaint the section a newer reconcile answered", async () => {
  const cx = fakeCx();
  const panel = await boot(cx, { team: { roles: [ROLE, SECOND] } });
  let rolesReads = 0;
  let releasePoll;
  H.state.exec = async (name, args) => {
    if (args[0] === "roles") {
      rolesReads += 1;
      if (rolesReads === 1) return new Promise((resolve) => { releasePoll = resolve; });
      return ok({ roles: [ROLE, SECOND] });
    }
    return reader()(name, args);
  };
  panel.refresh(cx);
  await tick();
  assert.equal(rolesReads, 1, "the poll admitted before the write is in flight");
  panel.refresh(cx, { reconcile: true });
  await flush(cx);
  assert.equal(rolesReads, 2, "the reconcile started its own roles read");
  assert.ok(!panel.errors.team, "the reconcile left no team diagnostic");

  releasePoll({ code: 1, stdout: "", stderr: "roles read admitted before the write failed" });
  await flush(cx);
  assert.ok(!panel.errors.team, "the older failure did not repaint the reconciled section");
  assert.equal(panel.roleByName("second")?.effort, "low", "the reconciled rows are still published");

  // The control: a current failure does paint its own section.
  H.state.exec = async (name, args) => (args[0] === "roles"
    ? { code: 1, stdout: "", stderr: "current roles read failed" }
    : reader()(name, args));
  panel.refresh(cx);
  await flush(cx);
  assert.match(String(panel.errors.team), /current roles read failed/);
});

// ---- F4: an explicit Report selection has its own read sequence ----

test("R4-F4: a Report selection publishes even when the last roster poll failed", async () => {
  const cx = fakeCx();
  const workers = [
    { name: "first", pane: "t1", status: "done", report: "ready" },
    { name: "second", pane: "t2", status: "done", report: "ready" },
  ];
  let failRoster = false;
  const panel = await boot(cx, {
    exec: async (name, args) => {
      if (args[0] === "capabilities") return ok({ schema: 1, team_json: true, report_json: true });
      if (args[0] === "roles") return ok({ roles: [ROLE] });
      if (args[0] === "roster") {
        return failRoster
          ? { code: 1, stdout: "", stderr: "temporary roster failure" }
          : ok({ agents: workers });
      }
      if (args[0] === "report") {
        return ok({ agent: args[1], items: [{ line: 1, mark: "done", text: `${args[1]} report` }] });
      }
      return ok({});
    },
  });
  panel.setTab("now", cx);
  click(panel, cx, "report-first|t1")({}, cx);
  await flush(cx);
  assert.equal(panel.data.report?.agent, "first");

  failRoster = true;
  panel.refresh(cx);
  await flush(cx);
  panel.setTab("now", cx);
  assert.match(String(panel.errors.roster ?? ""), /temporary roster failure/,
    "the roster poll failed and the previous rows stay on screen");

  // The clicked worker's own read stays in flight, so the clear it published
  // is what the panel holds in the meantime.
  let releaseReport;
  const read = H.state.exec;
  H.state.exec = async (name, args) => {
    if (args[0] === "report" && args[1] === "second") {
      return new Promise((resolve) => { releaseReport = resolve; });
    }
    return read(name, args);
  };
  click(panel, cx, "report-second|t2")({}, cx);
  await tick();
  assert.equal(typeof releaseReport, "function", "the second worker's report read is in flight");
  assert.equal(panel.reportAgent, "second|t2");
  assert.equal(panel.data.report, null, "the previous worker's report left the view with the selection");

  releaseReport(ok({ agent: "second", items: [{ line: 1, mark: "done", text: "second report" }] }));
  await flush(cx);
  assert.equal(panel.data.report?.agent, "second", "the clicked worker's report is published");
  const lastReportCall = execCalls().filter((call) => call.args?.[0] === "report").at(-1)?.args;
  assert.deepEqual(lastReportCall, ["report", "second", "--json"]);
});

// ---- F5: a successful probe refreshes the project its project still shows ----

test("R4-F5: a successful editor probe refreshes the project after its draft closed", async () => {
  const cx = fakeCx();
  const capable = { schema: 1, team_json: true, assistants_json: true };
  let finishProbe;
  let ready = false;
  const panel = await boot(cx, {
    exec: async (name, args) => {
      if (args[0] === "setup") return new Promise((resolve) => { finishProbe = resolve; });
      if (args[0] === "kinds") {
        return ok({ assistants: [{ kind: "codex", installed: true, probe: { status: ready ? "ready" : "no-auth" } }] });
      }
      return reader({ capabilities: capable })(name, args);
    },
    team: { roles: [ROLE, SECOND] },
  });
  click(panel, cx, "role-implementer")({}, cx);
  await flush(cx);
  assert.ok(panel.edit, "the draft is open");
  click(panel, cx, "edit-probe")({}, cx);
  assert.equal(typeof finishProbe, "function", "the probe is in flight");
  click(panel, cx, "edit-cancel")({}, cx);
  assert.equal(panel.edit, null, "the draft closed while the probe ran");

  ready = true;
  const before = execCalls().length;
  finishProbe(ok({}));
  await flush(cx);
  const commands = execCalls().slice(before).map((call) => call.args?.[0]);
  assert.ok(commands.includes("capabilities"), `the success refreshed the capabilities: ${JSON.stringify(commands)}`);
  assert.ok(commands.includes("roles"), "the success refreshed the team");
  assert.ok(commands.includes("roster"), "the success refreshed the roster");
  assert.ok(commands.includes("kinds"), "the success refreshed the assistants");
  panel.setTab("assistants", cx);
  assert.match(textOf(panel.render(cx)), /answering/, "the Assistants view shows the new probe result");
});

test("R5-F1: reopening a worker's Report while a refresh holds its read shows the report", async () => {
  const cx = fakeCx();
  const workers = [{ name: "first", pane: "t1", status: "done", report: "ready" }];
  const report = { agent: "first", items: [{ line: 1, mark: "done", text: "first report" }] };
  let reads = 0;
  let finishPoll;
  const panel = await boot(cx, {
    exec: async (name, args) => {
      if (args[0] === "capabilities") return ok({ schema: 1, team_json: true, report_json: true });
      if (args[0] === "roles") return ok({ roles: [ROLE] });
      if (args[0] === "roster") return ok({ agents: workers });
      if (args[0] === "report") {
        reads += 1;
        if (reads === 2) return new Promise((resolve) => { finishPoll = resolve; });
        return ok(report);
      }
      return ok({});
    },
  });
  panel.setTab("now", cx);
  click(panel, cx, "report-first|t1")({}, cx);
  await flush(cx);
  assert.equal(panel.data.report?.agent, "first");

  // The refresh re-reads the selected report and holds that read open.
  panel.refresh(cx);
  await flush(cx);
  assert.equal(typeof finishPoll, "function", "the refresh started a report read");

  // Back to Now and the same worker's Report again, on a fresh render.
  panel.setTab("now", cx);
  click(panel, cx, "report-first|t1")({}, cx);
  await flush(cx);
  finishPoll(ok(report));
  await flush(cx);
  assert.equal(reads, 3, "the selection started its own read instead of joining the held one");
  assert.equal(panel.data.report?.agent, "first", "the selected worker's report is published");
  assert.match(textOf(panel.render(cx)), /first report/);
});
