// An event cx is valid only for the call that delivered it. These checks drive
// every async controller path from a cx that goes stale as soon as the call
// returns: any use after that point throws the host's stale-context message.
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

const STALE_MESSAGE = "cx is no longer valid: it was captured during an earlier call and used later. Use cx.spawn or take cx from the callback arguments instead.";
const ROLE = { role: "implementer", lane: "build", kind: "pi", model: "m-1", effort: "high", mode: "edit" };
const WORKER = { name: "impl-1", pane: "%1", status: "working" };
const CONFIG = "/w/a/.agents/soho.conf";
const CAPS = {
  schema: 1, team_json: true, config_dry_run: true, config_unset: true,
  roster_workers: true, report_json: true, assistants_json: true, health_json: true,
};

const ok = (payload) => ({
  code: 0,
  stdout: typeof payload === "string" ? payload : JSON.stringify(payload ?? {}),
  stderr: "",
});

function execRouter(_name, args) {
  const key = args.join(" ");
  if (key === "capabilities --json") return ok(CAPS);
  if (key === "roles --json") return ok({ roles: [ROLE] });
  if (key === "roster --json") return ok({ agents: [WORKER] });
  if (key === "kinds --json") return ok({ assistants: [{ kind: "pi", installed: true }] });
  if (args[0] === "report") return ok({ agent: args[1], items: [] });
  if (args[0] === "config" && args.includes("--dry-run")) {
    return ok({ valid: true, previous: { in_file: "old" }, file: CONFIG });
  }
  return ok({});
}

function newLog() {
  return {
    uses: 0, staleUses: 0, notifies: 0, expired: false,
    tasks: [], timers: { every: [], after: [] },
  };
}

// One shared log for the event cx and every AsyncContext it spawns, so a single
// flush drains the whole tree of tasks.
function makeCx(state, expiring = false) {
  const check = () => {
    state.uses += 1;
    if (expiring && state.expired) {
      state.staleUses += 1;
      throw new Error(STALE_MESSAGE);
    }
  };
  return {
    state,
    notify() { check(); state.notifies += 1; },
    focus_handle() { check(); return { focus() {} }; },
    timer: {
      every(ms, callback) { check(); state.timers.every.push({ ms, callback }); return { cancel() {} }; },
      after(ms, callback) {
        check();
        const entry = { ms, callback, cancelled: false };
        state.timers.after.push(entry);
        return { cancel() { entry.cancelled = true; } };
      },
    },
    spawn(body) {
      check();
      const asyncCtx = makeCx(state);
      const promise = Promise.resolve(body(asyncCtx));
      state.tasks.push(promise);
      return promise;
    },
    expire() { if (expiring) state.expired = true; },
  };
}

const eventCx = () => makeCx(newLog(), true);
const tick = () => new Promise((resolve) => setImmediate(resolve));

async function flush(cx) {
  for (let i = 0; i < 80; i += 1) {
    const pending = cx.state.tasks.splice(0);
    for (const task of pending) task.catch(() => {});
    if (pending.length) await Promise.race([Promise.allSettled(pending), tick()]);
    await tick();
    if (!pending.length && cx.state.tasks.length === 0) return;
  }
}

async function boot() {
  H.reset();
  localStorage.clear();
  H.state.spaces = [{ id: "space-a", machine: "m1", folder: "/w/a", active: true }];
  H.state.focused = { space: "space-a", cwd: "/w/a" };
  H.state.exec = execRouter;
  H.state.readText = async () => "config text";
  const panel = new SohoPanel();
  const cx = makeCx(newLog());
  panel.init({}, cx);
  await flush(cx);
  panel.data.team = { roles: [ROLE], config_files: { project: CONFIG } };
  panel.data.roster = M.normalizeRoster({ agents: [WORKER] });
  return panel;
}

function draftFor(panel) {
  const context = M.contextFor(panel.space(), panel.currentFolder());
  const edit = {
    id: "draft-stale", appliesTo: "role", role: "implementer", lane: "build",
    laneLabel: "Build", kind: "pi", model: "m-1", effort: "high", scope: "project",
    pending: false, valid: true, error: "", preview: [], token: 0, snapshot: null,
    previous: null, previousByKey: {}, file: CONFIG, context,
    origin: panel.originOf("team"),
  };
  const previous = Object.fromEntries(M.editFields(edit).map((field) => [field.key, { in_file: "old" }]));
  edit.snapshot = M.snapshotFor(edit, M.previewFromDryRuns(null, M.editFields(edit), edit.scope),
    previous, CONFIG, context);
  return edit;
}

function assertClean(panel, cx) {
  assert.equal(cx.state.staleUses, 0, "the event cx was never used after its call");
  assert.equal(JSON.stringify(panel.errors).includes(STALE_MESSAGE), false);
}

function assertReconciledAfter(write) {
  const calls = H.state.calls.map((call) => (call.args ?? []).join(" "));
  const at = calls.indexOf(write);
  assert.ok(at >= 0, `the write ${write} ran`);
  assert.ok(calls.findIndex((call, i) => i > at && call === "roles --json") >= 0,
    `the reconcile read ran after ${write}`);
}

test("stale cx: the helper expires after the call and a spawned context does not", async () => {
  const cx = eventCx();
  let asyncCtx = null;
  cx.spawn(async (ctx) => { asyncCtx = ctx; await tick(); ctx.notify(); });
  cx.expire();
  assert.throws(() => cx.notify(), (error) => error.message === STALE_MESSAGE);
  const staleAfterExpire = cx.state.staleUses;
  assert.equal(staleAfterExpire, 1);
  await flush(cx);
  assert.ok(asyncCtx);
  assert.equal(cx.state.staleUses, staleAfterExpire, "the spawned context added no stale use");
  assert.ok(asyncCtx.state.notifies > 0, "the AsyncContext still notifies after an await");
});

test("stale cx: a Refresh click clears a stale team error on the task context", async () => {
  const panel = await boot();
  panel.errors.team = STALE_MESSAGE;
  const cx = eventCx();
  panel.refresh(cx);
  cx.expire();
  await flush(cx);
  assertClean(panel, cx);
  assert.equal(panel.errors.team, "", "the successful team read cleared the old diagnostic");
  assert.ok(H.state.calls.some((call) => call.args?.join(" ") === "roles --json"));
});

test("stale cx: runAction still notices and reconciles after its await", async () => {
  const panel = await boot();
  panel.errors.team = STALE_MESSAGE;
  const capture = M.rowCapture(panel.originOf("team"), ROLE);
  const cx = eventCx();
  panel.runAction(cx, "close:impl-1", ["release", "impl-1", "--close"], "team",
    null, "impl-1 closed", null, capture);
  cx.expire();
  await flush(cx);
  assertClean(panel, cx);
  assert.equal(panel.noticeItem?.text, "impl-1 closed");
  assertReconciledAfter("release impl-1 --close");
  assert.equal(panel.errors.team, "");
});

test("stale cx: setEffort writes, notices and reconciles on the task context", async () => {
  const panel = await boot();
  const capture = M.effortCapture(panel.originOf("team"), ROLE,
    { key: "role.implementer.effort", appliesTo: "role", label: "Implementer" });
  const cx = eventCx();
  panel.setEffort(capture, "low", cx);
  cx.expire();
  await flush(cx);
  assertClean(panel, cx);
  assert.match(panel.noticeItem?.text ?? "", /effort is now low/);
  assertReconciledAfter("config set role.implementer.effort=low --project");
});

test("stale cx: previewEdit finishes its dry runs on the task context", async () => {
  const panel = await boot();
  panel.edit = draftFor(panel);
  const cx = eventCx();
  const pending = panel.previewEdit(cx);
  cx.expire();
  await pending;
  await flush(cx);
  assertClean(panel, cx);
  assert.equal(panel.edit.pending, false);
  assert.equal(panel.edit.preview.length, 3);
  assert.ok(panel.edit.snapshot);
});

test("stale cx: saveEdit writes, notices and reconciles on the task context", async () => {
  const panel = await boot();
  panel.edit = draftFor(panel);
  const cx = eventCx();
  panel.saveEdit(cx, panel.edit.id, panel.edit.snapshot);
  cx.expire();
  await flush(cx);
  assertClean(panel, cx);
  assert.equal(panel.edit, null);
  assert.match(panel.noticeItem?.text ?? "", /settings is now pi/);
  assertReconciledAfter("config set role.implementer.kind=pi --project");
});

test("stale cx: undoLast runs the captured plan and reconciles on the task context", async () => {
  const panel = await boot();
  panel.edit = draftFor(panel);
  const saveCx = makeCx(newLog());
  panel.saveEdit(saveCx, panel.edit.id, panel.edit.snapshot);
  await flush(saveCx);
  const captured = panel.noticeCapture();
  assert.ok(captured?.id, "the Save left an Undo plan");
  const cx = eventCx();
  panel.undoLast(captured, cx);
  cx.expire();
  await flush(cx);
  assertClean(panel, cx);
  assert.equal(panel.noticeItem?.text, "Undid role.implementer.kind");
  assertReconciledAfter("config set role.implementer.kind=old --project");
});

test("stale cx: openConfig publishes the file on the task context", async () => {
  const panel = await boot();
  const cx = eventCx();
  panel.openConfig({ origin: panel.originOf("team"), path: CONFIG }, cx);
  cx.expire();
  await flush(cx);
  assertClean(panel, cx);
  assert.equal(panel.tab, "config");
  assert.equal(panel.data.configText, "config text");
});

test("stale cx: loadReport and its Report button read on the task context", async () => {
  const panel = await boot();
  const origin = panel.originOf("roster");
  const cx = eventCx();
  panel.loadReport(M.workerKey(WORKER), origin, cx, true);
  cx.expire();
  await flush(cx);
  assertClean(panel, cx);
  assert.equal(panel.data.report?.agent, "impl-1");

  const buttonCx = eventCx();
  panel.showWorkerReport(M.workerCapture(panel.originOf("roster"), WORKER), buttonCx);
  buttonCx.expire();
  await flush(buttonCx);
  assertClean(panel, buttonCx);
  assert.equal(panel.data.report?.agent, "impl-1");
});

test("stale cx: readChoices publishes its choices on the task context", async () => {
  const panel = await boot();
  panel.data.choices = null;
  const cx = eventCx();
  panel.readChoices(cx, panel.originOf("team"));
  cx.expire();
  await flush(cx);
  assertClean(panel, cx);
  assert.ok(panel.data.choices, "the kinds read published its choices");
});
