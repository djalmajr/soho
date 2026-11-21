// Folder-watch checks: the panel follows the files its roles read reports, and
// falls back to its unchanged timer when a watch cannot be established.
import assert from "node:assert/strict";
import test from "node:test";
import { register } from "node:module";
import * as H from "./stubs/herdr.mjs";
import { fakeCx, flush } from "./stubs/context.mjs";
register("./stubs/loader.mjs", import.meta.url);
const { default: SohoPanel } = await import("../main.js");
const M = await import("../model.js");

globalThis.localStorage = {
  store: new Map(),
  getItem(key) { return this.store.has(key) ? this.store.get(key) : null; },
  setItem(key, value) { this.store.set(key, String(value)); },
  clear() { this.store.clear(); },
};

const PROJECT = "/w/a/.agents/soho.conf";
const PROJECT_DIR = "/w/a/.agents";
const USER = "/home/u/.config/soho/config";
const USER_DIR = "/home/u/.config/soho";
const SESSION = "/state/ws/session.conf";
const ROSTER = "/state/ws/agents.tsv";
const STATE_DIR = "/state/ws";
const REPORTS = "/state/ws/reports";
const WAIT_FILE = "/state/ws/wait/soh498.md";

const roles = (overrides = {}) => ({
  roles: [],
  config_files: { project: PROJECT, user: USER, session: SESSION, ...overrides.config_files },
  team_files: { roster: ROSTER, reports: REPORTS, ...overrides.team_files },
});
const OLD_ROLES = { roles: [], config_files: { project: PROJECT, user: USER } };
const spaceA = () => ({ id: "space-a", machine: null, folder: "/w/a", active: true });
const spaceB = () => ({ id: "space-b", machine: null, folder: "/w/b", active: true });
const remoteSpace = () => ({ id: "space-r", machine: "remote-fixture", folder: "/w/a", active: true });

const ok = (payload) => ({
  code: 0,
  stdout: typeof payload === "string" ? payload : JSON.stringify(payload ?? {}),
  stderr: "",
});

function reader(payload = roles()) {
  return async (_name, args) => {
    const key = args.join(" ");
    if (key === "capabilities --json") return ok({ schema: 1, team_json: true, roster_workers: true });
    if (key === "roles --json") return ok(payload);
    if (key === "roster --json") return ok({ agents: [] });
    if (key === "kinds") return ok("KIND     EXECUTABLE    FAMILY     EFFORT   INSTALLED MCP\n");
    return ok({});
  };
}

function fireTimer(cx) {
  const due = cx.timers.after.find((entry) => !entry.cancelled && entry.ms === 300);
  assert.ok(due, "one 300 ms refresh was queued");
  due.cancelled = true;
  due.callback();
}

function pendingTimers(cx) {
  return cx.timers.after.filter((entry) => !entry.cancelled && entry.ms === 300);
}

// Roles reads carry a marker so a held read can be told apart from the
// follow-up that must run after it settles (F1).
async function bootControlled(t, options = {}) {
  const state = { marker: "initial", hold: null, active: 0, maxActive: 0 };
  const space = options.space ?? spaceA();
  H.reset();
  localStorage.clear();
  H.state.spaces = [space];
  H.state.focused = { space: space.id, cwd: options.folder ?? "/w/a" };
  H.state.exec = async (_name, args) => {
    const key = args.join(" ");
    if (key === "roles --json") {
      const marker = state.marker;
      state.active += 1;
      state.maxActive = Math.max(state.maxActive, state.active);
      try {
        if (state.hold) await state.hold;
        return ok({ ...roles(), marker });
      } finally {
        state.active -= 1;
      }
    }
    if (key === "capabilities --json") return ok({ schema: 1, team_json: true, roster_workers: true });
    if (key === "roster --json") return ok({ agents: [] });
    if (key === "kinds") return ok("KIND     EXECUTABLE    FAMILY     EFFORT   INSTALLED MCP\n");
    return ok({});
  };
  const cx = fakeCx();
  const panel = new SohoPanel();
  cx.event((eventCx) => panel.init({}, eventCx));
  await flush(cx);
  panel.lastVisibleAt = Date.now();
  t.after(async () => {
    await panel.close(cx);
    for (const entry of H.state.watches) entry.handle.cancel();
    await flush(cx);
  });
  return {
    panel, cx, state,
    snapshot: () => panel.data.team?.marker,
    pause() {
      let resume;
      state.hold = new Promise((resolve) => { resume = resolve; });
      const release = () => { state.hold = null; resume(); };
      t.after(release);
      return release;
    },
    set(marker) { state.marker = marker; },
  };
}

function roleReads() {
  return H.state.calls.filter((call) => call.name === "soho" && call.args?.join(" ") === "roles --json").length;
}

function activeFolders() {
  return H.activeWatches().slice().sort();
}

function render(panel, cx) {
  return cx.event((eventCx) => panel.render(eventCx));
}

async function boot(t, options = {}) {
  H.reset();
  localStorage.clear();
  H.state.spaces = [options.space ?? spaceA()];
  H.state.focused = { space: options.space?.id ?? "space-a", cwd: options.folder ?? "/w/a" };
  H.state.exec = options.exec ?? reader(options.payload ?? roles());
  const cx = fakeCx();
  const panel = new SohoPanel();
  cx.event((eventCx) => panel.init({}, eventCx));
  await flush(cx);
  // A real copy renders after init, which is the visibility signal.
  if (!options.hidden) panel.lastVisibleAt = Date.now();
  t.after(async () => {
    await panel.close(cx);
    for (const entry of H.state.watches) entry.handle.cancel();
    await flush(cx);
  });
  return { panel, cx };
}

test("watch: one watch per shown folder, with the 15 s timer still armed", async (t) => {
  const { cx } = await boot(t);
  assert.deepEqual(activeFolders(), [PROJECT_DIR, USER_DIR, REPORTS, STATE_DIR].sort());
  assert.deepEqual(cx.timers.every.map((entry) => entry.ms), [15000]);
  assert.equal(roleReads(), 1, "the initial roles read ran once");
});

test("watch: a batch naming the project file refreshes once after the delay", async (t) => {
  const { cx } = await boot(t);
  assert.equal(H.emit(PROJECT_DIR, [PROJECT]), true);
  await flush(cx);
  assert.equal(roleReads(), 1, "nothing refreshes before the delay");
  assert.deepEqual(cx.timers.after.map((entry) => entry.ms), [300]);
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 2, "the queued refresh ran once");
});

test("watch: two batches inside the window join one refresh", async (t) => {
  const { cx } = await boot(t);
  assert.equal(H.emit(STATE_DIR, [ROSTER]), true);
  await flush(cx);
  assert.equal(H.emit(STATE_DIR, [SESSION]), true);
  await flush(cx);
  assert.deepEqual(cx.timers.after.map((entry) => entry.ms), [300]);
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 2, "both batches joined the one refresh");
});

test("watch: an unrelated path in a watched folder refreshes nothing", async (t) => {
  const { cx } = await boot(t);
  assert.equal(H.emit(STATE_DIR, [WAIT_FILE]), true);
  await flush(cx);
  assert.deepEqual(cx.timers.after, [], "no refresh was queued");
  assert.equal(roleReads(), 1);
});

test("watch: a batch naming the watched root refreshes everything", async (t) => {
  const { cx } = await boot(t);
  assert.equal(H.emit(USER_DIR, [USER_DIR]), true);
  await flush(cx);
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 2);
});

test("watch: a hidden copy marks the batch dirty and refreshes once at render", async (t) => {
  const { panel, cx } = await boot(t);
  panel.lastVisibleAt = 0;
  assert.equal(H.emit(PROJECT_DIR, [PROJECT]), true);
  await flush(cx);
  assert.equal(panel.watchDirty, true);
  assert.deepEqual(cx.timers.after, [], "a hidden copy schedules no visible refresh");
  render(panel, cx);
  await flush(cx);
  assert.equal(roleReads(), 1, "the next render starts the same debounce window");
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 2, "the next render woke one refresh");
  render(panel, cx);
  await flush(cx);
  assert.equal(roleReads(), 2, "a later render did not refresh again");
});

test("watch: a context change cancels the old watches and starts the new ones", async (t) => {
  const { panel, cx } = await boot(t);
  const old = H.state.watches.slice();
  H.state.spaces = [spaceB()];
  H.state.focused = { space: "space-b", cwd: "/w/b" };
  // Hold the new context's roles read: cancellation must not wait for it.
  let releaseTeam;
  const gate = new Promise((resolve) => { releaseTeam = resolve; });
  t.after(releaseTeam);
  let hold = false;
  const readNew = reader(roles({
    config_files: { project: "/w/b/.agents/soho.conf" },
    team_files: { roster: "/state/b/agents.tsv", reports: "/state/b/reports" },
  }));
  H.state.exec = async (name, args) => {
    if (hold && args.join(" ") === "roles --json") await gate;
    return readNew(name, args);
  };
  hold = true;
  render(panel, cx);
  await flush(cx);
  assert.ok(old.every((entry) => entry.settled), "every old watch was cancelled");
  assert.deepEqual(activeFolders(), [], "no watch starts before the new paths are read");
  releaseTeam();
  await flush(cx);
  assert.deepEqual(activeFolders(), ["/home/u/.config/soho", "/state/b", "/state/b/reports",
    "/state/ws", "/w/b/.agents"].sort());
  assert.equal(H.emit("/w/b/.agents", ["/w/b/.agents/soho.conf"]), true);
  await flush(cx);
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 3, "the new watches refresh the new context");
});

test("watch: a rejected watch is dropped silently and the timer still works", async (t) => {
  const { panel, cx } = await boot(t);
  assert.equal(H.fail(PROJECT_DIR, new Error("watch refused")), true);
  await flush(cx);
  assert.deepEqual(cx.timers.after, [], "a rejected watch queues no refresh");
  assert.equal(Object.values(panel.errors).filter(Boolean).length, 0, "no diagnostic was added");
  cx.timers.every[0].callback();
  await flush(cx);
  assert.equal(roleReads(), 2, "the unchanged 15 s timer still refreshes");
});

test("watch: a null session path is not watched, but the other paths are", async (t) => {
  const { cx } = await boot(t, { payload: roles({ config_files: { session: null } }) });
  assert.ok(H.activeWatches().includes(STATE_DIR), "the roster's folder is still watched");
  assert.equal(H.emit(STATE_DIR, [SESSION]), true);
  await flush(cx);
  assert.deepEqual(cx.timers.after, [], "the null session file matches nothing");
  assert.equal(H.emit(PROJECT_DIR, [PROJECT]), true);
  await flush(cx);
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 2);
});

test("watch: a roster-file batch refreshes", async (t) => {
  const { cx } = await boot(t);
  assert.equal(H.emit(STATE_DIR, [ROSTER]), true);
  await flush(cx);
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 2);
});

test("watch: a path under the reports folder refreshes", async (t) => {
  const { cx } = await boot(t);
  assert.equal(H.emit(REPORTS, [`${REPORTS}/worker.md`]), true);
  await flush(cx);
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 2);
});

test("watch: a wait file in the state directory does nothing", async (t) => {
  const { cx } = await boot(t);
  assert.equal(H.emit(STATE_DIR, [WAIT_FILE]), true);
  await flush(cx);
  assert.deepEqual(cx.timers.after, []);
  assert.equal(roleReads(), 1);
});

test("watch: there is no manual Refresh button or More action", async (t) => {
  const { panel, cx } = await boot(t);
  const ids = (element) => {
    const record = element?.record;
    if (!record) return [];
    return [...(record.ids ?? []), ...(record.children ?? []).flatMap(ids)];
  };
  const normal = ids(render(panel, cx));
  assert.ok(!normal.includes("refresh"), "no header Refresh button");
  panel.band = "narrow";
  panel.menuOpen = "more-menu";
  const narrow = ids(render(panel, cx));
  assert.ok(!narrow.includes("more-menu-option-refresh"), "no More menu Refresh entry");
  panel.moreAction("refresh", cx);
  await flush(cx);
  assert.equal(roleReads(), 1, "the removed action refreshes nothing");
});

test("watch: closing the copy cancels every pending watch", async (t) => {
  const { panel } = await boot(t);
  const opened = H.state.watches.slice();
  assert.ok(opened.length > 0);
  await panel.close({});
  assert.deepEqual(H.activeWatches(), []);
  assert.ok(opened.every((entry) => entry.settled), "each pending watch was released");
});

test("watch: an older soho without session or team_files still watches the config files", async (t) => {
  const { panel, cx } = await boot(t, { payload: OLD_ROLES });
  assert.deepEqual(activeFolders(), [PROJECT_DIR, USER_DIR].sort());
  assert.equal(Object.values(panel.errors).filter(Boolean).length, 0, "the missing fields are not an error");
  assert.deepEqual(cx.timers.every.map((entry) => entry.ms), [15000]);
  assert.equal(H.emit(PROJECT_DIR, [PROJECT]), true);
  await flush(cx);
  fireTimer(cx);
  await flush(cx);
  assert.equal(roleReads(), 2, "the project watch refreshes");
  cx.timers.every[0].callback();
  await flush(cx);
  assert.equal(roleReads(), 3, "the timer fallback still refreshes");
});

// F1: a change arriving during a refresh waits for it, never joining a read
// that started before the file changed.
for (const mode of ["prior poll", "prior watch refresh", "hidden batch at render"]) {
  test(`F1: ${mode} gets one fresh read after the owner settles`, async (t) => {
    const x = await bootControlled(t);
    const release = x.pause();
    if (mode === "prior watch refresh") {
      assert.ok(H.emit(PROJECT_DIR, [PROJECT]));
      await flush(x.cx);
      fireTimer(x.cx);
    } else {
      x.cx.timers.every[0].callback();
    }
    await flush(x.cx);
    assert.equal(roleReads(), 2, "the owner's roles read started");
    assert.equal(x.snapshot(), "initial");
    x.set("edited");
    if (mode === "hidden batch at render") x.panel.lastVisibleAt = 0;
    assert.ok(H.emit(PROJECT_DIR, [PROJECT]));
    await flush(x.cx);
    if (mode === "hidden batch at render") {
      assert.equal(x.panel.watchDirty, true);
      render(x.panel, x.cx);
      await flush(x.cx);
    }
    fireTimer(x.cx);
    await flush(x.cx);
    assert.equal(roleReads(), 2, `${mode} did not start a second roles read in flight`);
    assert.equal(x.snapshot(), "initial");
    release();
    await flush(x.cx);
    assert.equal(x.cx.state.staleUses, 0, "the follow-up never uses the expired render context");
    assert.equal(roleReads(), 3, "exactly one follow-up read ran after the owner settled");
    assert.equal(x.snapshot(), "edited", "the edited payload is shown without waiting for a poll");
    assert.equal(x.state.maxActive, 1, "no two roles reads were in flight at once");
    assert.equal(x.panel.refreshInFlight, 0, "no owner is left in flight");
  });
}

test("F1: a change waits for a pending roster read before starting another roles read", async (t) => {
  const x = await bootControlled(t);
  let releaseRoster;
  const gate = new Promise((resolve) => { releaseRoster = resolve; });
  t.after(releaseRoster);
  const read = H.state.exec;
  H.state.exec = async (name, args) => {
    if (args[0] === "roster") await gate;
    return read(name, args);
  };
  x.cx.timers.every[0].callback();
  await flush(x.cx);
  assert.equal(roleReads(), 2);
  assert.ok(x.panel.inflight.size > 0, "the owner is still settling");
  x.set("edited");
  assert.ok(H.emit(PROJECT_DIR, [PROJECT]));
  await flush(x.cx);
  fireTimer(x.cx);
  await flush(x.cx);
  assert.equal(roleReads(), 2, "the watch change waits for the owner");
  assert.equal(H.state.calls.filter((c) => c.args?.join(" ") === "roster --json").length, 2,
    "boot and the poll each read the roster; the change added none");
  releaseRoster();
  await flush(x.cx);
  assert.equal(roleReads(), 3);
  assert.equal(x.snapshot(), "edited");
});

test("F1: a second change during a watch refresh gets its own follow-up", async (t) => {
  const x = await bootControlled(t);
  x.set("first edit");
  const release = x.pause();
  assert.ok(H.emit(PROJECT_DIR, [PROJECT]));
  await flush(x.cx);
  fireTimer(x.cx);
  await flush(x.cx);
  assert.equal(roleReads(), 2, "the first change started its read");
  x.set("second edit");
  assert.ok(H.emit(PROJECT_DIR, [PROJECT]));
  await flush(x.cx);
  fireTimer(x.cx);
  await flush(x.cx);
  assert.equal(roleReads(), 2, "the second change cannot join that read");
  release();
  await flush(x.cx);
  assert.equal(roleReads(), 3);
  assert.equal(x.snapshot(), "second edit");
  assert.equal(x.state.maxActive, 1);
});

test("F3: a remote space registers no watch and keeps the 15 s timer", async (t) => {
  const { panel, cx } = await boot(t, { space: remoteSpace() });
  assert.deepEqual(activeFolders(), []);
  assert.equal(H.state.calls.filter((call) => call.name === "watchFolder").length, 0);
  const routed = H.state.calls.find((call) => call.name === "soho" && call.args?.join(" ") === "roles --json");
  assert.equal(routed.options.on, "remote-fixture", "reads still go to the remote machine");
  assert.deepEqual(cx.timers.every.map((entry) => entry.ms), [15000]);
  cx.timers.every[0].callback();
  await flush(cx);
  assert.equal(roleReads(), 2, "the timer still refreshes the remote context");
  assert.equal(panel.errors.team, "", "no watch error was added");
});

test("F4: an exact trailing-space sibling does not match the shown file", async (t) => {
  const { cx } = await boot(t);
  assert.equal(H.emit(PROJECT_DIR, [PROJECT + " "]), true);
  await flush(cx);
  assert.deepEqual(pendingTimers(cx), []);
  assert.equal(roleReads(), 1);
});

test("F4: root-level and drive-root files get root parents", () => {
  assert.deepEqual(M.watchTargets({ config_files: { project: "/soho.conf" } }),
    [{ folder: "/", files: ["/soho.conf"], dirs: [] }]);
  assert.deepEqual(M.watchTargets({ config_files: { project: "C:\\soho.conf" } }),
    [{ folder: "C:\\", files: ["C:\\soho.conf"], dirs: [] }]);
  assert.deepEqual(M.watchTargets({ config_files: { project: "/work/parent\\name/soho.conf" } }),
    [{ folder: "/work/parent\\name", files: ["/work/parent\\name/soho.conf"], dirs: [] }]);
});

test("round 2 F2: A to pending B to A reopens the watches and delivers an edit", async (t) => {
  const x = await bootControlled(t);
  let releaseB;
  const gateB = new Promise((resolve) => { releaseB = resolve; });
  t.after(releaseB);
  const read = H.state.exec;
  H.state.exec = async (name, args, options) => {
    if (args[0] === "roles" && options.cwd === "/w/b") {
      await gateB;
      return ok({ ...roles(), marker: "from B" });
    }
    return read(name, args);
  };
  const original = activeFolders();
  H.state.spaces = [spaceB()];
  H.state.focused = { space: "space-b", cwd: "/w/b" };
  render(x.panel, x.cx);
  await flush(x.cx);
  assert.equal(roleReads(), 2, "B's roles request is pending");
  assert.deepEqual(activeFolders(), []);

  H.state.spaces = [spaceA()];
  H.state.focused = { space: "space-a", cwd: "/w/a" };
  render(x.panel, x.cx);
  await flush(x.cx);
  assert.equal(x.snapshot(), "initial", "A loaded before B settled");
  assert.deepEqual(activeFolders(), original, "all of A's folders were watched again");
  releaseB();
  await flush(x.cx);
  assert.equal(x.snapshot(), "initial", "the late B read could not publish");
  x.set("edited on A");
  assert.ok(H.emit(PROJECT_DIR, [PROJECT]));
  await flush(x.cx);
  fireTimer(x.cx);
  await flush(x.cx);
  assert.equal(roleReads(), 4);
  assert.equal(x.snapshot(), "edited on A");
});

test("round 2 F2: returning to A does not reuse an unsettled read from the previous visit", async (t) => {
  const x = await bootControlled(t);
  let releaseOldA;
  const gateA = new Promise((resolve) => { releaseOldA = resolve; });
  t.after(releaseOldA);
  const read = H.state.exec;
  H.state.exec = async (name, args, options) => {
    if (args[0] === "roles" && options.cwd === "/w/a" && roleReads() === 2) {
      await gateA;
      return ok({ ...roles(), marker: "previous visit" });
    }
    return read(name, args);
  };
  x.cx.timers.every[0].callback();
  await flush(x.cx);
  assert.equal(roleReads(), 2, "the earlier visit has a held roles request");

  H.state.spaces = [spaceB()];
  H.state.focused = { space: "space-b", cwd: "/w/b" };
  render(x.panel, x.cx);
  await flush(x.cx);
  H.state.spaces = [spaceA()];
  H.state.focused = { space: "space-a", cwd: "/w/a" };
  x.set("new visit");
  render(x.panel, x.cx);
  await flush(x.cx);
  assert.equal(roleReads(), 4, "A started a new roles request");
  assert.equal(x.snapshot(), "new visit");
  releaseOldA();
  await flush(x.cx);
  assert.equal(x.snapshot(), "new visit", "the previous visit did not publish");
});

test("round 2 F3 / round 1 F5: a settled old watch at context change neither rearms nor refreshes", async (t) => {
  const { panel, cx } = await boot(t);
  const old = H.state.watches.find((entry) => entry.folder === PROJECT_DIR);
  assert.ok(old);
  assert.ok(H.emit(PROJECT_DIR, [PROJECT]), "the old watch settled with a matching batch");
  // Render synchronously before the settled watch gets its continuation.
  H.state.spaces = [spaceB()];
  H.state.focused = { space: "space-b", cwd: "/w/b" };
  const handlesBefore = H.state.watches.length;
  let releaseRoles;
  const gate = new Promise((resolve) => { releaseRoles = resolve; });
  t.after(releaseRoles);
  let hold = true;
  const read = H.state.exec;
  H.state.exec = async (name, args) => {
    if (hold && args.join(" ") === "roles --json") await gate;
    return read(name, args);
  };
  render(panel, cx);
  await flush(cx);
  assert.ok(old.settled);
  assert.equal(H.state.watches.length, handlesBefore, "the old continuation did not rearm");
  assert.deepEqual(pendingTimers(cx), [], "the old continuation scheduled no refresh");
  hold = false;
  releaseRoles();
  await flush(cx);
  assert.ok(H.state.watches.slice(handlesBefore).some((entry) => !entry.settled && entry.folder === PROJECT_DIR),
    "the new context armed its own watches");
});

test("round 2 F4: closing with a held refresh and pending change prevents publication and new watches", async (t) => {
  const x = await bootControlled(t);
  x.set("late result");
  const release = x.pause();
  x.cx.timers.every[0].callback();
  await flush(x.cx);
  assert.equal(roleReads(), 2, "the poll is holding a roles read");
  assert.ok(H.emit(PROJECT_DIR, [PROJECT]));
  await flush(x.cx);
  fireTimer(x.cx);
  await flush(x.cx);
  assert.equal(roleReads(), 2, "the file change is waiting for the poll");
  const opened = H.state.watches.length;
  const before = { ...x.panel.data };
  const updatedAt = x.panel.updatedAt;
  const notifies = x.cx.notifies;
  await x.panel.close(x.cx);
  assert.deepEqual(activeFolders(), []);
  await flush(x.cx);
  assert.equal(x.cx.state.liveTasks, 1, "the refresh loop and watches exited while the poll was held");
  x.cx.timers.every[0].callback();
  release();
  await flush(x.cx);
  assert.equal(x.snapshot(), "initial", "the late roles result did not publish");
  assert.deepEqual(x.panel.data, before, "no section published after close");
  assert.equal(x.panel.updatedAt, updatedAt);
  assert.equal(x.cx.notifies, notifies);
  assert.equal(H.state.watches.length, opened, "no watch was opened after close");
  assert.deepEqual(activeFolders(), []);
  assert.deepEqual(pendingTimers(x.cx), []);
  assert.equal(roleReads(), 2, "the queued change did not run after close");
  assert.equal(x.panel.refreshInFlight, 0);
});

test("round 2 F4: closing in the debounce window cancels the timer and exits the loop", async (t) => {
  const { panel, cx } = await boot(t);
  assert.ok(H.emit(PROJECT_DIR, [PROJECT]));
  await flush(cx);
  assert.equal(pendingTimers(cx).length, 1);
  await panel.close();
  await flush(cx);
  assert.deepEqual(pendingTimers(cx), []);
  assert.deepEqual(activeFolders(), []);
  assert.equal(cx.state.liveTasks, 0);
  assert.equal(roleReads(), 1);
});
