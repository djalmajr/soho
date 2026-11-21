// SoHo panel for Herdr.app. Reads soho on the shown space's machine and keeps
// the team, workers, reports, assistants and health in one mod. All side
// effects are user actions and are guarded while `herdr-app check` loads.
import { View, div } from "gpui-kit";
import { v_flex, h_flex, InputState } from "gpui-base";
import { exec, spaces, focused, theme, checking, run, openOverlay, readTextFile, nextCommand, watchFolder } from "herdr";
import * as M from "./model.js";
import { button, errorBlock, growText, markdown, notice, TEXT } from "./ui.js";
import {
  renderAssistants, renderEdit, renderEmpty, renderGrouped, renderHealth,
  renderList, renderNow, renderReport, sortMenu, toolTabs, moreMenu,
} from "./views.js";

const NOTICE_MS = 8000;
const REFRESH_MS = 15000;
const WATCH_REFRESH_MS = 300;
const EFFORTS = ["low", "medium", "high", "xhigh"];

const firstLines = (text, count = 4) =>
  String(text ?? "").trim().split("\n").filter(Boolean).slice(0, count).join("\n");

export default class SohoPanel extends View {
  init(_props, cx) {
    this.tab = "team";
    this.view = "grouped";
    this.sort = { column: "role", dir: "asc" };
    this.band = "";
    this.width = 0;
    this.data = {
      capabilities: null, team: null, roster: null, report: null, reportMarkdown: null,
      assistants: null, kindsText: "", modelsByKind: {}, choices: null, health: null, configText: null,
    };
    this.errors = {};
    this.notes = {};
    // The read that wrote each section's diagnostic, so a late answer from an
    // older request never repaints a newer one (R4-F3).
    this.diagSeq = {};
    this.caps = M.capabilities(null);
    this.busy = new Set();
    // Reads carry an origin, not a generation: a refresh that overlaps another
    // one coalesces with it and never invalidates its result (F1, F2).
    this.readSeq = 0;
    this.origins = {};
    this.originDefault = null;
    this.inflight = new Map();
    this.confirmSeq = 0;
    this.cleanupSeq = 0;
    this.draftSeq = 0;
    this.undoSeq = 0;
    this.context = null;
    this.contextVersion = 0;
    this.folderKnown = null;
    this.refreshQueued = false;
    this.firstReadDone = false;
    this.menuOpen = "";
    this.selectedWorker = "";
    this.reportAgent = "";
    this.edit = null;
    this.modelInput = InputState.new({ value: "" });
    this.modelBound = false;
    this.menuFocus = cx.focus_handle();
    this.moreFocus = cx.focus_handle();
    this.noticeItem = null;
    this.noticeTimer = null;
    this.undoHolder = null;
    this.pending = null;
    // A cleanup confirmation carries its own token and origin (F1, R3-F1);
    // null means no confirmation is pending.
    this.cleanupConfirm = null;
    this.updatedAt = null;
    this.lastVisibleAt = 0;
    this.watches = [];
    this.watchSignature = "";
    this.watchGeneration = 0;
    this.watchDirty = false;
    this.closed = false;
    this.pendingChange = false;
    this.wakeChangeLoop = null;
    this.cancelChangeDelay = null;
    this.refreshInFlight = 0;

    const defaultFolder = this.currentFolder();
    this.folderKnown = defaultFolder;
    this.context = M.contextFor(this.space(), defaultFolder);
    this.applyFolderPrefs(defaultFolder);

    if (checking()) return;
    this.bindModelInput();
    cx.spawn((ctx) => this.changeRefreshLoop(ctx));
    this.refresh(cx);
    // Poll only while this copy is visible (F18): the installed API has no
    // visibility call, so the last render or non-zero size is the signal.
    cx.timer.every(REFRESH_MS, (ctx) => {
      if (!M.pollAllowed(this.lastVisibleAt)) return;
      this.refresh(ctx);
    });
    cx.spawn(async (ctx) => {
      for (;;) {
        const command = await nextCommand();
        this.handleCommand(command, ctx);
      }
    });
  }

  // Herdr data and folder identity; a mod never keeps a cx from render/init.
  space() {
    return spaces().find((s) => s.active) ?? null;
  }

  folderOf(space) {
    if (!space) return "";
    const f = focused();
    if (f && f.space === space.id && f.cwd) return f.cwd;
    return space.folder || "";
  }

  currentFolder() {
    return this.folderOf(this.space());
  }

  currentContext() {
    return M.contextFor(this.space(), this.currentFolder());
  }

  // A read's origin, taken when the read starts; its sequence tells two reads
  // of the same context apart and never decides eligibility (F1, F2).
  beginRead(context) {
    return { ...M.originFor(context, ++this.readSeq), contextVersion: this.contextVersion };
  }

  // A captured identity (origin or context) against the shown one.
  boundTo(captured, context) {
    if (!captured) return false;
    if (captured.spaceId !== undefined) return M.originCurrent(captured, context);
    return M.sameContext(captured, context);
  }

  current(origin) {
    return this.boundTo(origin, this.currentContext());
  }

  // A render-time capture is a copy of the dataset origin, so the handler keeps
  // the identity it displayed after a refresh replaced it (F1, R3-F1). There is
  // no fallback: a capture taken with no origin runs nothing.
  originOf(name) {
    const origin = this.origins[name];
    return origin ? { ...origin } : null;
  }

  // A capture may be an origin object or a view capture that carries one.
  originFrom(captured) {
    if (!captured) return null;
    if (captured.spaceId !== undefined) return { ...captured };
    const origin = captured.origin;
    return origin && origin.spaceId !== undefined ? { ...origin } : null;
  }

  captureCurrent(capture) {
    const origin = this.originFrom(capture);
    return Boolean(origin) && this.current(origin);
  }

  // The row a handler displayed is still the current row with that identity
  // (F1, R3-F1).
  rowCurrent(capture) {
    return this.captureCurrent(capture)
      && M.rowStillCurrent(capture, this.data.team?.roles ?? [], this.groups());
  }

  workerCurrent(capture) {
    return this.captureCurrent(capture)
      && M.workerStillCurrent(capture, this.data.roster?.agents ?? []);
  }

  // A stale or missing capture runs nothing and refuses on screen (F1, R3-F1),
  // without taking away the Undo plan still shown.
  refuse(cx, text = M.STALE_ACTION_MESSAGE) {
    this.showNotice(text, this.undoHolder, cx);
    return false;
  }

  // A dataset is stored together with the origin of the read that produced it
  // (F1). A read that started earlier never overwrites one that started later,
  // so a poll admitted before a write cannot undo its reconciliation (F3).
  publish(name, value, origin) {
    const bound = origin ?? this.originDefault;
    const previous = this.origins[name];
    if (previous && bound
      && M.sameContext(M.contextOfOrigin(previous), M.contextOfOrigin(bound))
      && Number(previous.seq ?? 0) > Number(bound.seq ?? 0)) return;
    this.data[name] = value;
    this.origins[name] = bound;
    if (bound) this.originDefault = bound;
  }

  // Datasets bound to another context leave the view at once, so the loading
  // state shows instead of the old project's rows (F1).
  dropDatasets() {
    for (const name of Object.keys(this.data)) {
      if (name === "modelsByKind") this.data[name] = {};
      else if (name === "kindsText") this.data[name] = "";
      else this.data[name] = null;
    }
    this.origins = {};
    this.originDefault = null;
  }

  pruneDatasets() {
    const context = this.currentContext();
    for (const name of Object.keys(this.origins)) {
      if (M.originCurrent(this.origins[name], context)) continue;
      this.data[name] = name === "modelsByKind" ? {} : (name === "kindsText" ? "" : null);
      delete this.origins[name];
    }
  }

  busyKey(section, context) {
    return M.readBusyKey(section, context);
  }

  // A context change invalidates every pending action, drops the old
  // project's datasets and restores this folder's own view and sort (F1,
  // F8, F13).
  adoptContext(context, cx) {
    const changed = this.context !== null && !M.sameContext(this.context, context);
    this.context = context;
    this.folderKnown = context.folder;
    this.applyFolderPrefs(context.folder);
    if (!changed) return false;
    this.contextVersion += 1;
    this.invalidateWatches();
    this.dropDatasets();
    // Capability flags belong to the context that answered: until this
    // context's own reply arrives the panel knows nothing, so Health and
    // Probe again are not offered (F2, R3-F2).
    this.caps = M.capabilities(null);
    this.errors = {};
    this.notes = {};
    this.firstReadDone = false;
    this.updatedAt = null;
    this.reportAgent = "";
    this.selectedWorker = "";
    if (M.isDetailTab(this.tab)) this.tab = "team";
    this.tab = M.resolveTab(this.tab, this.caps);
    let dropped = false;
    if (this.edit && !this.boundTo(this.edit.origin ?? this.edit.context, context)) { this.edit = null; dropped = true; }
    if (this.undoHolder && !this.boundTo(this.undoHolder.origin ?? this.undoHolder.context, context)) { this.undoHolder = null; dropped = true; }
    if (this.pending && !this.boundTo(this.pending.origin ?? this.pending.context, context)) { this.pending = null; dropped = true; }
    if (this.cleanupConfirm) { this.cleanupConfirm = null; dropped = true; }
    // adoptContext also runs during render, whose cx must not start a
    // timer: the notice is shown from a task context instead.
    if (dropped) cx.spawn(async (ctx) => { this.showNotice(M.CONTEXT_CHANGED_MESSAGE, null, ctx); ctx.notify(); });
    return true;
  }

  applyFolderPrefs(folder) {
    const state = M.viewStateFor(this.readPrefs(), folder, { view: "grouped", sort: { column: "role", dir: "asc" } });
    this.view = state.view;
    this.sort = state.sort;
  }

  // Returning to the same space does not revive reads from the previous visit.
  // Overlapping reads within the same visit can still share a result.
  alive(origin) {
    return !this.closed && this.current(origin)
      && (origin.contextVersion ?? this.contextVersion) === this.contextVersion;
  }

  readPrefs() {
    try {
      const value = JSON.parse(localStorage.getItem("soho.prefs") ?? "{}");
      return value && typeof value === "object" ? value : {};
    } catch {
      return {};
    }
  }

  savePrefs() {
    try {
      localStorage.setItem("soho.prefs", JSON.stringify(
        M.prefsFor(this.readPrefs(), this.folderKnown ?? this.currentFolder(), { view: this.view, sort: this.sort })));
    } catch {
      // Preferences are not critical; a read-only session still works.
    }
  }

  groups() {
    return M.groupRoles(this.data.team ?? {}, this.data.capabilities ?? this.caps);
  }

  roleByName(name) {
    return (this.data.team?.roles ?? []).find((r) => r.role === name) ?? null;
  }

  editAssistants() {
    return this.data.choices?.assistants ?? [];
  }

  assistantCards() {
    const assistants = this.caps.assistantsJson && Array.isArray(this.data.assistants?.assistants)
      ? this.data.assistants.assistants
      : M.parseKindsText(this.data.kindsText).filter((a) => a.installed)
        .map((a) => ({ ...a, models: M.parseModelsText(this.data.modelsByKind[a.kind] ?? ""), probe: null }));
    return M.assistantsWithRoles(assistants, this.data.team?.roles ?? []);
  }

  reportWorker() {
    const byKey = M.findWorker(this.data.roster?.agents, this.reportAgent);
    if (byKey) return byKey;
    const name = this.data.report?.agent ?? this.reportAgent;
    return (this.data.roster?.agents ?? []).find((w) => w.name === name) ?? null;
  }

  isBusy(key) {
    return this.busy.has(key);
  }

  configFile(scope) {
    return (this.data.team?.config_files?.[scope] ?? (scope === "user" ? "" : ""));
  }

  showNotice(text, undo, cx) {
    const item = { text };
    this.noticeItem = item;
    // The rendered Undo keeps this plan id, so a later notice's plan is never
    // run by a button drawn for an earlier one (F1, R3-F1).
    this.undoHolder = undo?.available ? { ...undo, id: undo.id ?? (this.undoSeq += 1) } : null;
    this.noticeTimer?.cancel();
    this.noticeTimer = cx.timer.after(NOTICE_MS, (ctx) => {
      if (this.noticeItem === item) {
        this.noticeItem = null;
        this.undoHolder = null;
      }
      ctx.notify();
    });
  }

  // The typed model input feeds the draft and its preview (F6).
  bindModelInput() {
    if (this.modelBound) return;
    this.modelBound = this.modelInput.on("change", (_event, ctx) => this.modelChanged(ctx));
  }

  modelChanged(cx) {
    if (!this.edit || this.busy.has("edit-save")) return;
    const value = this.modelInput.value();
    if (this.edit.model === value) return;
    this.edit = M.draftWithModel(this.edit, value);
    this.previewEdit(cx);
  }

  notifySafe(cx) {
    try {
      cx.notify();
    } catch {
      // The copy is closed or reloaded: there is nothing left to redraw.
    }
  }

  // An event cx is valid only for the call that delivered it. Work that
  // outlives that call runs in a task and uses only the AsyncContext the task
  // hands back. `Task` itself is not thenable, so the returned promise settles
  // when the body ends: callers that await a write still resume after it.
  spawnFrom(cx, body) {
    let settle;
    const done = new Promise((resolve) => { settle = resolve; });
    cx.spawn(async (ctx) => {
      try {
        return await body(ctx);
      } finally {
        settle();
      }
    });
    return done;
  }

  // A section's diagnostic is written only by a read at least as new as the one
  // that wrote it last, so an older in-flight answer never repaints the result
  // of a newer reconcile (R4-F3).
  diagnose(key, origin, { note, error }) {
    const seq = Number(origin?.seq ?? 0);
    if (seq < Number(this.diagSeq[key] ?? -1)) return false;
    this.diagSeq[key] = seq;
    this.notes[key] = note;
    this.errors[key] = error;
    return true;
  }

  // One read: JSON or text, first stderr lines in the section it feeds. A read
  // is admitted by its whole origin and coalesces with an identical read still
  // in flight, so two refreshes of one context share one answer (F1, F2, F9,
  // F14). `opts.accept` can drop a diagnostic whose consumer moved on (F7).
  // The result carries the origin of the request that fetched it, so a caller
  // that joined an older request publishes with that request's sequence, never
  // with its own (R4-F3).
  async runSection(cx, section, args, origin, json = true, opts = {}) {
    const bound = this.originFrom(origin);
    if (!this.alive(bound)) return null;
    const context = M.contextOfOrigin(bound);
    const baseKey = `${opts.busyKey ?? M.readBusyKey(section, bound)}|visit:${bound.contextVersion ?? this.contextVersion}`;
    // A reconcile read always starts its own request, after the write it
    // reconciles, instead of joining one admitted before the write (F3).
    const busyKey = opts.fresh ? `${baseKey}|fresh|${bound.seq}` : baseKey;
    const errorKey = opts.errorKey ?? section;
    const moved = () => (typeof opts.accept === "function" ? !opts.accept() : false);
    const accept = () => this.alive(bound) && !moved();
    const running = opts.fresh ? null : this.inflight.get(busyKey);
    if (running) return running;
    this.busy.add(busyKey);
    const task = (async () => {
      try {
        const r = await exec("soho", args, { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
        if (!this.alive(bound)) return null;
        if (r.code !== 0) {
          // A refusal the panel cannot change is a neutral note, not an error (decision 3).
          const note = opts.note ? opts.note(r.stderr) : "";
          if (!accept()) return null;
          if (note) {
            this.diagnose(errorKey, bound, { note, error: "" });
            return null;
          }
          // An unexpected rejection clears the neutral note it contradicts (F10).
          this.diagnose(errorKey, bound, { note: "", error: firstLines(r.stderr || r.stdout || `soho exited ${r.code}`) });
          return null;
        }
        if (accept()) this.diagnose(errorKey, bound, { note: "", error: "" });
        if (!json) return { value: r.stdout ?? "", origin: bound };
        try {
          return { value: JSON.parse(r.stdout), origin: bound };
        } catch {
          if (accept()) {
            this.diagnose(errorKey, bound, { note: "", error: M.nonJsonMessage(`soho ${args.join(" ")}`, r.stderr) });
          }
          return null;
        }
      } catch (e) {
        // A thrown read contradicts a note as much as a rejected one does (F10).
        if (accept()) this.diagnose(errorKey, bound, { note: "", error: firstLines(e?.message ?? e) });
        return null;
      } finally {
        this.busy.delete(busyKey);
        this.inflight.delete(busyKey);
        if (!this.closed) this.notifySafe(cx);
      }
    })();
    this.inflight.set(busyKey, task);
    return task;
  }

  // `options.reconcile` marks the read that follows a write: its roles request
  // starts after that write instead of joining one admitted before it (F3).
  refresh(cx, options = {}) {
    if (this.closed || checking()) return;
    const space = this.space();
    const folder = this.folderOf(space);
    const context = M.contextFor(space, folder);
    this.adoptContext(context, cx);
    if (!space) {
      this.errors.team = "no space shown";
      this.firstReadDone = true;
      cx.notify();
      return;
    }
    const origin = this.beginRead(context);
    this.folderKnown = folder;
    this.refreshInFlight += 1;
    const read = async (ctx) => {
      try {
        const capsP = this.runSection(ctx, "capabilities", ["capabilities", "--json"], origin);
        const teamP = this.runSection(ctx, "team", ["roles", "--json"], origin, true,
          { fresh: Boolean(options.reconcile) });
        const rosterP = this.runSection(ctx, "roster", ["roster", "--json"], origin, true,
          { note: M.rosterRefusal });
        const [capsRead, teamRead, rosterRead] = await Promise.all([capsP, teamP, rosterP]);
        if (!this.alive(origin)) return;
        // A failed or malformed capability answer means the no-new-keys default,
        // a visible diagnostic and the independent reads still run (F12).
        const answer = M.capabilityAnswer(capsRead?.value ?? null);
        const capsOrigin = capsRead?.origin ?? origin;
        if (answer.caps) {
          this.publish("capabilities", answer.caps, capsOrigin);
          this.caps = M.capabilities(answer.caps);
        } else {
          this.publish("capabilities", null, capsOrigin);
          this.caps = M.capabilities(null);
          if (!this.errors.capabilities) this.diagnose("capabilities", capsOrigin, { note: "", error: answer.error });
        }
        if (teamRead) this.publish("team", teamRead.value, teamRead.origin);
        if (rosterRead) this.publish("roster", M.normalizeRoster(rosterRead.value), rosterRead.origin);
        this.syncWatches(context, ctx);
        // A hidden tab never stays selected: the fallback happens before the render
        // that would paint it (decision 4). A detail view keeps its tab (F8).
        this.tab = M.resolveTab(this.tab, this.caps);
        this.firstReadDone = true;
        this.updatedAt = Date.now();
        ctx.notify();

        await this.readChoices(ctx, origin);
        if (!this.alive(origin)) return;
        const reads = M.sectionReads(this.data.capabilities ?? this.caps);
        if (reads.health) {
          const health = await this.runSection(ctx, "health", ["health", "--json"], origin);
          if (this.alive(origin) && health) this.publish("health", health.value, health.origin);
        }
        const agent = this.reportAgent
          || (this.tab === "now" && this.band === "wide" ? this.selectedWorker : "");
        if (agent) await this.loadReport(agent, origin, ctx);
        if (this.alive(origin)) ctx.notify();
      } finally {
        this.finishRefresh();
      }
    };
    try {
      cx.spawn(read);
    } catch (error) {
      this.finishRefresh();
      throw error;
    }
  }

  // One watch per folder the roles read says this context shows. A batch is
  // matched against those paths, never against every file in the folder. Each
  // iteration starts a fresh watch: the API releases the call on settlement.
  syncWatches(context, cx) {
    if (checking()) return;
    const targets = M.watchTargets(this.data.team);
    const signature = JSON.stringify([M.contextKey(context), targets]);
    if (signature === this.watchSignature) return;
    this.watchGeneration += 1;
    this.cancelWatches();
    const generation = this.watchGeneration;
    this.watchSignature = signature;
    // watchFolder has no machine argument: a remote space keeps the timer.
    if (context?.machine) return;
    for (const target of targets) {
      let handle;
      try { handle = watchFolder(target.folder); } catch { continue; }
      if (!handle || typeof handle.then !== "function") continue;
      this.watches.push(handle);
      cx.spawn(async () => {
        try {
          for (;;) {
            let paths;
            try { paths = await handle; } catch { return; }
            if (this.watchGeneration !== generation) return;
            this.watches = this.watches.filter((w) => w !== handle);
            if (!Array.isArray(paths) || paths.length === 0) return;
            if (M.watchMatches(target, paths)) {
              if (M.pollAllowed(this.lastVisibleAt)) this.requestChangeRefresh();
              else this.watchDirty = true;
            }
            handle = watchFolder(target.folder);
            this.watches.push(handle);
          }
        } catch {
          // A rejected or failed watch is dropped; the 15 s timer stays.
        }
      });
    }
  }

  cancelWatches() {
    for (const handle of this.watches) {
      try { handle.cancel?.(); } catch { /* already released */ }
    }
    this.watches = [];
  }

  invalidateWatches() {
    this.watchGeneration += 1;
    this.watchSignature = "";
    this.cancelWatches();
    this.watchDirty = false;
    this.pendingChange = false;
    this.cancelChangeDelay?.();
    this.signalChangeLoop();
  }

  signalChangeLoop() {
    this.wakeChangeLoop?.();
    this.wakeChangeLoop = null;
  }

  waitForChangeSignal() {
    return new Promise((resolve) => { this.wakeChangeLoop = resolve; });
  }

  requestChangeRefresh() {
    this.pendingChange = true;
    this.signalChangeLoop();
  }

  finishRefresh() {
    this.refreshInFlight -= 1;
    if (this.refreshInFlight === 0) this.signalChangeLoop();
  }

  changeDelay(ctx) {
    return new Promise((resolve) => {
      const finish = () => {
        this.cancelChangeDelay = null;
        resolve();
      };
      const timer = ctx.timer.after(WATCH_REFRESH_MS, finish);
      this.cancelChangeDelay = () => {
        timer.cancel();
        finish();
      };
    });
  }

  // Only this task handles file-change wakes. It can use its own cx after the
  // debounce and waits for every admitted refresh before starting a fresh read.
  async changeRefreshLoop(ctx) {
    while (!this.closed) {
      if (!this.pendingChange) {
        await this.waitForChangeSignal();
        continue;
      }
      const version = this.contextVersion;
      await this.changeDelay(ctx);
      while (!this.closed && version === this.contextVersion && this.refreshInFlight > 0) {
        await this.waitForChangeSignal();
      }
      if (this.closed || version !== this.contextVersion) continue;
      this.pendingChange = false;
      if (M.pollAllowed(this.lastVisibleAt)) this.refresh(ctx);
      else this.watchDirty = true;
    }
  }

  async close() {
    this.closed = true;
    this.invalidateWatches();
  }

  async readChoices(cx, origin) {
    if (checking() || !this.alive(origin)) return;
    const reads = M.sectionReads(this.caps);
    return this.spawnFrom(cx, async (ctx) => {
      if (reads.assistantsJson) {
        const assistants = await this.runSection(ctx, "assistants", ["kinds", "--json"], origin);
        if (!this.alive(origin) || !assistants) return;
        this.publish("assistants", assistants.value, assistants.origin);
        this.publish("choices", M.assistantChoices(assistants.value, "", {}, this.caps), assistants.origin);
      } else {
        const kinds = await this.runSection(ctx, "assistants", ["kinds"], origin, false);
        if (!this.alive(origin) || !kinds) return;
        const text = kinds.value ?? "";
        this.publish("kindsText", text, kinds.origin);
        const modelsByKind = {};
        for (const a of M.parseKindsText(text).filter((row) => row.installed)) {
          const models = await this.runSection(ctx, "assistants", ["models", a.kind], origin, false,
            { errorKey: M.modelsErrorKey(a.kind), busyKey: M.readBusyKey(`assistants:${a.kind}`, origin) });
          if (!this.alive(origin)) return;
          modelsByKind[a.kind] = models?.value ?? "";
        }
        this.publish("modelsByKind", modelsByKind, kinds.origin);
        this.publish("choices", M.assistantChoices(null, text, modelsByKind, this.caps), kinds.origin);
      }
      if (this.alive(origin)) ctx.notify();
    });
  }

  // fresh: an explicit selection never joins a read already in flight for the
  // same worker, whose result carries an older sequence than the selection's
  // clear and would be refused (R5-F1).
  async loadReport(agentKey, origin, cx, fresh = false) {
    if (checking() || !this.alive(origin)) return;
    const worker = M.findWorker(this.data.roster?.agents, agentKey);
    if (!worker) return;
    const requested = M.workerKey(worker);
    this.reportAgent = requested;
    // A result or a diagnostic for a worker that lost the selection is dropped
    // before it can be published (F7).
    const stillWanted = () => M.acceptsReport(this.reportAgent, requested);
    return this.spawnFrom(cx, async (ctx) => {
      if (this.caps.reportJson) {
        const report = await this.runSection(ctx, "report", ["report", worker.name, "--json"], origin, true,
          { busyKey: M.reportBusyKey(origin, requested), accept: stillWanted, fresh });
        if (!this.alive(origin) || !stillWanted()) return;
        if (report) {
          this.publish("report", report.value, report.origin);
          this.publish("reportMarkdown", null, report.origin);
        }
      } else if (worker.report_path) {
        try {
          const path = worker.report_path;
          const text = await readTextFile(path, { on: origin.machine });
          if (!this.alive(origin) || !stillWanted()) return;
          this.publish("reportMarkdown", text ?? `Report file is empty: ${path}`, origin);
          this.publish("report", null, origin);
          this.errors.report = "";
        } catch (e) {
          if (this.alive(origin) && stillWanted()) {
            this.errors.report = firstLines(e?.message ?? e);
          }
        }
      } else {
        this.errors.report = "soho has no report --json and this worker has no report path";
      }
      if (this.alive(origin) && stillWanted()) ctx.notify();
    });
  }

  // Every state-changing action goes through here: no work while checking, and
  // a write runs only for the identity it was opened from (F1). The busy state
  // always clears with a notify (F2).
  async runAction(cx, key, args, section, onSuccess, text, undo, capture, opts = {}) {
    if (checking()) return;
    if (!this.captureCurrent(capture)) { this.refuse(cx); return; }
    const origin = this.originFrom(capture);
    const context = M.contextOfOrigin(origin);
    const errorKey = opts.errorKey ?? section;
    const kept = typeof opts.accept === "function" ? opts.accept : () => true;
    const decision = M.decideWrite({ actionContext: context, currentContext: this.currentContext(), busy: this.busy.has(key) });
    if (!decision.run) {
      if (decision.message) { this.errors[errorKey] = decision.message; cx.notify(); }
      return;
    }
    this.busy.add(key);
    cx.notify();
    return this.spawnFrom(cx, async (ctx) => {
      try {
        const r = await exec("soho", args, { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
        if (!this.current(origin)) return;
        if (r.code !== 0) {
          // The diagnostic belongs to the source that started the action, so a
          // source that left the screen paints nothing (F5, R3-F5).
          if (kept()) this.errors[errorKey] = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
          return;
        }
        // Only the diagnostic is scoped to the source's lifetime: a success in a
        // project that is still shown refreshes it, whatever became of the button
        // that started the action (R4-F5).
        if (kept()) this.errors[errorKey] = "";
        if (onSuccess) onSuccess(r);
        if (text) this.showNotice(text, undo, ctx);
        this.refresh(ctx, { reconcile: true });
      } catch (e) {
        if (this.current(origin) && kept()) this.errors[errorKey] = firstLines(e?.message ?? e);
      } finally {
        this.busy.delete(key);
        this.notifySafe(ctx);
      }
    });
  }

  // Herdr delivers the suffix only: "close_<token>" for
  // "soho-team.close_<token>". A suffix confirms only the dialog that emitted
  // it, in the context it was opened in, for the same Core address (F4).
  handleCommand(command, cx) {
    if (checking()) return;
    const pending = this.pending;
    const match = M.confirmationMatches(pending, command, this.data.roster?.agents ?? []);
    if (!match.ok) return;
    if (!this.boundTo(pending.origin ?? pending.context, this.currentContext())) {
      this.pending = null;
      this.showNotice(M.CONTEXT_CHANGED_MESSAGE, null, cx);
      return;
    }
    this.pending = null;
    if (pending.action === "close") {
      this.runAction(cx, `close:${pending.key}`, ["release", pending.name, "--close"], "roster",
        null, `${pending.name} closed`, null, pending);
    } else if (pending.action === "reopen") {
      this.runAction(cx, `reopen:${pending.key}`, ["reopen", pending.name], "roster",
        null, `${pending.name} reopened`, null, pending);
    }
  }

  // Controller callbacks used by the views.
  setTab(id, cx) {
    this.tab = id;
    this.menuOpen = "";
    this.cleanupConfirm = null;
    if (id === "team") this.edit = null;
    if (id === "now" && this.band === "wide" && this.selectedWorker) {
      this.refresh(cx);
    }
    cx.notify();
  }

  setView(view, cx) {
    this.view = view === "list" ? "list" : "grouped";
    this.menuOpen = "";
    this.savePrefs();
    cx.notify();
  }

  setSort(column, cx) {
    this.sort = M.toggleSort(this.sort, column);
    this.menuOpen = "";
    this.savePrefs();
    cx.notify();
  }

  openMenu(id, cx) {
    this.menuOpen = id;
    cx.notify();
  }

  moreAction(id, cx) {
    this.menuOpen = "";
    if (id === "grouped" || id === "list") this.setView(id, cx);
    else if (id.startsWith("sort:")) this.setSort(id.slice(5), cx);
  }

  peek(capture, cx) {
    if (!this.workerCurrent(capture)) { this.refuse(cx); return; }
    const worker = M.findWorker(this.data.roster?.agents, capture.key);
    if (worker?.pane) run("terminal.peek", { terminal: worker.pane });
    else this.showNotice("No terminal address for this worker", null, cx);
  }

  showWorkerReport(capture, cx) {
    if (!this.workerCurrent(capture)) { this.refuse(cx); return; }
    // An explicit selection is a read of its own: it carries a fresh sequence of
    // the captured project, so its clear and its response publish even when a
    // later refresh already moved another section past the roster row it came
    // from (R4-F4).
    const bound = this.originFrom(capture);
    const selection = this.beginRead(M.contextOfOrigin(bound));
    this.selectedWorker = capture.key;
    this.reportAgent = capture.key;
    this.publish("report", null, selection);
    this.publish("reportMarkdown", null, selection);
    this.errors.report = "";
    cx.notify();
    if (!checking()) this.loadReport(this.reportAgent, selection, cx, true);
  }

  openReport(capture, cx) {
    if (!this.workerCurrent(capture)) { this.refuse(cx); return; }
    this.showWorkerReport(capture, cx);
    this.tab = "report";
    this.menuOpen = "";
    cx.notify();
  }

  // Each dialog carries a token in its command suffix, so an older overlay's
  // suffix cannot confirm a later dialog for the same action. The pending
  // action keeps the identity it was opened for (F1, F4, R3-F1).
  confirmation(action, capture, cx) {
    if (!this.workerCurrent(capture)) { this.refuse(cx); return null; }
    const bound = this.originFrom(capture);
    const token = M.confirmationToken(++this.confirmSeq);
    this.pending = {
      action, name: capture.name, key: capture.key, token,
      origin: bound, context: M.contextOfOrigin(bound),
    };
    return token;
  }

  confirmClose(capture, cx) {
    if (checking()) return;
    const token = this.confirmation("close", capture, cx);
    if (!token) return;
    openOverlay({ mod: "confirm", kind: "dialog", width: 440, props: {
      title: "Close this worker?",
      detail: `Release ${capture.name} and close its pane. Its report stays available in the team's state.`,
      command: { name: `soho-team.close_${token}`, args: { name: capture.name } },
    } });
  }

  confirmReopen(capture, cx) {
    if (checking()) return;
    const token = this.confirmation("reopen", capture, cx);
    if (!token) return;
    openOverlay({ mod: "confirm", kind: "dialog", width: 440, props: {
      title: "Reopen this worker?",
      detail: `Start ${capture.name} fresh with the same role and assistant.`,
      command: { name: `soho-team.reopen_${token}`, args: { name: capture.name } },
    } });
  }

  // The effort select writes the target it displayed, for the row it displayed
  // (F1, R3-F1).
  async setEffort(capture, value, cx) {
    if (checking() || EFFORTS.includes(value) === false) return;
    if (!this.rowCurrent(capture)) { this.refuse(cx); return; }
    const bound = this.originFrom(capture);
    const scope = "project";
    const key = `effort:${capture.key}`;
    const context = M.contextOfOrigin(bound);
    const decision = M.decideWrite({ actionContext: context, currentContext: this.currentContext(), busy: this.busy.has(key) });
    if (!decision.run) {
      if (decision.message) { this.errors.config = decision.message; cx.notify(); }
      return;
    }
    this.menuOpen = "";
    this.busy.add(key);
    cx.notify();
    let previous = null;
    let file = this.configFile(scope);
    const argv = M.configSetArgv(capture.key, value, scope);
    return this.spawnFrom(cx, async (ctx) => {
      try {
        if (this.caps.configDryRun) {
          const r = await exec("soho", [...argv, "--dry-run", "--json"],
            { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
          if (!this.current(bound)) return;
          if (r.code !== 0) {
            this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
            return;
          }
          let plan;
          try {
            plan = JSON.parse(r.stdout);
          } catch {
            this.errors.config = M.nonJsonMessage(`soho ${[...argv, "--dry-run", "--json"].join(" ")}`, r.stderr);
            return;
          }
          if (plan.valid === false) {
            this.errors.config = String(plan.error ?? "invalid value");
            return;
          }
          previous = plan.previous ?? null;
          file = plan.file ?? file;
        }
        this.errors.config = "";
        const r = await exec("soho", argv, { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
        if (!this.current(bound)) return;
        if (r.code !== 0) {
          this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
          return;
        }
        const what = capture.lane ? `${capture.label} lane effort` : `${capture.role} effort`;
        const undo = M.undoPlan(previous, capture.key, scope, this.caps);
        this.showNotice(M.noticeText(what, value, capture.key, file),
          undo.available ? { ...undo, context, origin: bound } : null, ctx);
        this.refresh(ctx, { reconcile: true });
      } catch (e) {
        if (this.current(bound)) this.errors.config = firstLines(e?.message ?? e);
      } finally {
        this.busy.delete(key);
        this.notifySafe(ctx);
      }
    });
  }

  nextDraftId() {
    this.draftSeq += 1;
    return `draft-${this.draftSeq}`;
  }

  // The row or lane a handler displayed is still there, with that identity
  // (F1, R3-F1).
  openEdit(capture, cx) {
    if (checking()) return;
    if (!this.rowCurrent(capture)) { this.refuse(cx); return; }
    const bound = this.originFrom(capture);
    const sel = { role: capture.role ?? "", lane: capture.lane ?? "" };
    const group = sel.lane ? this.groups().find((g) => g.lane === sel.lane) : null;
    const row = sel.role ? this.roleByName(sel.role) : null;
    if (!group && !row) return;
    const byLane = Boolean(group) && sel.appliesTo !== "role";
    const targetRole = group ? M.roleForScope(sel.role, group) : (row?.role ?? "");
    this.edit = {
      id: this.nextDraftId(),
      appliesTo: byLane ? "lane" : "role",
      lane: group?.lane ?? row?.lane ?? "",
      laneLabel: group?.label ?? "",
      roleCount: group?.roles?.length ?? 1,
      role: byLane ? "" : targetRole,
      targetRole,
      kind: byLane ? (group.kind || "") : (row.kind || ""),
      model: byLane ? (group.model || "") : (row.model || ""),
      effort: byLane ? (group.effort || "") : (row.effort || ""),
      scope: "project",
      preview: [],
      valid: true,
      error: "",
      previous: null,
      previousByKey: {},
      file: this.configFile("project"),
      pending: false,
      snapshot: null,
      context: M.contextOfOrigin(bound),
      origin: bound,
      token: 0,
    };
    this.tab = "team";
    this.menuOpen = "";
    this.bindModelInput();
    this.modelInput.set_value(this.edit.model);
    cx.notify();
    this.readChoices(cx, bound);
    this.previewEdit(cx);
  }

  closeEdit(draftId, cx) {
    if (!this.edit || (draftId !== undefined && this.edit.id !== draftId)) { this.refuse(cx); return; }
    const id = this.edit.id;
    this.edit = null;
    if (id) delete this.errors[M.probeEditKey(id)];
    this.menuOpen = "";
    cx.notify();
  }

  setEditField(field, value, draftId, cx) {
    if (!this.edit || (draftId !== undefined && this.edit.id !== draftId)) { this.refuse(cx); return; }
    if (this.busy.has("edit-save")) return;
    this.edit[field] = value;
    if (field === "model") this.modelInput.set_value(value);
    this.menuOpen = "";
    this.previewEdit(cx);
  }

  // Switching Applies to keeps the role that was opened (F5).
  setEditAppliesTo(appliesTo, draftId, cx) {
    if (!this.edit || (draftId !== undefined && this.edit.id !== draftId)) { this.refuse(cx); return; }
    if (this.busy.has("edit-save")) return;
    const group = this.groups().find((g) => g.lane === this.edit.lane);
    this.edit.appliesTo = appliesTo === "role" ? "role" : "lane";
    if (this.edit.appliesTo === "lane") {
      if (group) {
        this.edit.kind = group.kind || "";
        this.edit.model = group.model || "";
        this.edit.effort = group.effort || "";
      }
    } else {
      const role = M.roleForScope(this.edit.role || this.edit.targetRole, group);
      const row = this.roleByName(role);
      this.edit.role = role;
      this.edit.kind = row?.kind || "";
      this.edit.model = row?.model || "";
      this.edit.effort = row?.effort || "";
    }
    this.modelInput.set_value(this.edit.model);
    this.previewEdit(cx);
  }

  // One validated snapshot for the exact draft; the previous preview is stale
  // while the new one runs and Save waits for it (F7). A draft with no fields
  // has no snapshot to save (F6), and the preview compares the draft's own
  // origin before it publishes anything (F1).
  async previewEdit(cx) {
    if (checking() || !this.edit) return;
    const e = this.edit;
    const token = ++e.token;
    e.pending = true;
    const problem = M.editProblem(e);
    const fields = M.editFields(e);
    if (problem) {
      e.pending = false;
      e.preview = [];
      e.valid = false;
      e.error = problem;
      e.snapshot = null;
      cx.notify();
      return;
    }
    if (!fields.length) {
      e.pending = false;
      e.preview = [];
      e.valid = true;
      e.error = "";
      e.previousByKey = {};
      e.snapshot = null;
      cx.notify();
      return;
    }
    const origin = this.originFrom(e.origin) ?? this.originFrom(e.context);
    const context = M.contextOfOrigin(origin);
    if (!origin || !this.current(origin)) {
      e.pending = false;
      e.valid = false;
      e.snapshot = null;
      e.error = M.CONTEXT_CHANGED_MESSAGE;
      cx.notify();
      return;
    }
    return this.spawnFrom(cx, async (ctx) => {
      let preview;
      if (this.caps.configDryRun) {
        const results = [];
        for (const f of fields) {
          const argv = [...M.configSetArgv(f.key, f.value, e.scope), "--dry-run", "--json"];
          try {
            const r = await exec("soho", argv, { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
            if (this.edit !== e || token !== e.token) return;
            if (!this.current(origin)) {
              e.pending = false;
              e.valid = false;
              e.snapshot = null;
              e.error = M.CONTEXT_CHANGED_MESSAGE;
              ctx.notify();
              return;
            }
            if (r.code !== 0) {
              e.error = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
              e.preview = [];
              e.valid = false;
              e.pending = false;
              ctx.notify();
              return;
            }
            let result;
            try {
              result = JSON.parse(r.stdout);
            } catch {
              e.error = M.nonJsonMessage(`soho ${argv.join(" ")}`, r.stderr);
              e.preview = [];
              e.valid = false;
              e.pending = false;
              ctx.notify();
              return;
            }
            results.push(result);
          } catch (err) {
            if (this.edit !== e || token !== e.token) return;
            e.error = firstLines(err?.message ?? err);
            e.preview = [];
            e.valid = false;
            e.pending = false;
            ctx.notify();
            return;
          }
        }
        preview = M.previewFromDryRuns(results, fields, e.scope);
        e.previous = results[0]?.previous ?? null;
        e.previousByKey = Object.fromEntries(results.map((r, i) => [fields[i].key, r?.previous ?? null]));
        e.file = results[0]?.file ?? this.configFile(e.scope);
      } else {
        preview = M.previewFromDryRuns(null, fields, e.scope);
        e.file = this.configFile(e.scope);
        e.previous = null;
        e.previousByKey = {};
      }
      if (this.edit !== e || token !== e.token) return;
      e.preview = preview.commands;
      e.valid = preview.valid;
      e.error = preview.error;
      e.snapshot = preview.valid ? M.snapshotFor(e, preview, e.previousByKey, e.file, context) : null;
      e.pending = false;
      ctx.notify();
    });
  }

  // Save runs only for the draft and the validated snapshot that rendered its
  // button; another draft, or a newer preview for this one, runs nothing
  // (F1, R3-F1).
  async saveEdit(cx, capturedId, capturedSnapshot) {
    if (checking() || !this.edit || this.busy.has("edit-save")) return;
    const e = this.edit;
    if (!capturedId || e.id !== capturedId) { this.refuse(cx); return; }
    if (!capturedSnapshot || e.snapshot !== capturedSnapshot || !M.snapshotMatches(capturedSnapshot, e)) {
      e.error = "The preview is out of date; wait for it to update before saving.";
      cx.notify();
      return;
    }
    const snapshot = capturedSnapshot;
    const state = M.previewState(e);
    if (!state.canSave) return;
    const origin = this.originFrom(snapshot.origin) ?? this.originFrom(e.origin);
    if (!origin || !this.current(origin)) {
      this.refuse(cx, M.CONTEXT_CHANGED_MESSAGE);
      return;
    }
    const decision = M.decideWrite({ actionContext: snapshot.context, currentContext: this.currentContext(), busy: false });
    if (!decision.run) {
      this.errors.config = decision.message;
      cx.notify();
      return;
    }
    this.busy.add("edit-save");
    cx.notify();
    const scope = snapshot.scope;
    const applied = [];
    return this.spawnFrom(cx, async (ctx) => {
      try {
        for (let i = 0; i < snapshot.fields.length; i += 1) {
          const f = snapshot.fields[i];
          const r = await exec("soho", M.configSetArgv(f.key, f.value, scope),
            { cwd: snapshot.context.folder || undefined, on: snapshot.context.machine, timeout: 20000 });
          if (!this.current(origin)) return;
          if (r.code !== 0) {
            const detail = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
            this.errors.config = M.saveFailureMessage(applied, f.key,
              snapshot.fields.slice(i + 1).map((x) => x.key), detail);
            // A partly applied Save reconciles the team with the CLI (F3).
            this.refresh(ctx, { reconcile: true });
            return;
          }
          applied.push(f.key);
        }
        this.errors.config = "";
        const plans = snapshot.fields.map((f) => M.undoPlan(snapshot.previousByKey[f.key] ?? null, f.key, scope, this.caps));
        const undo = plans.every((p) => p.available)
          ? { available: true, plans, key: snapshot.fields[0].key, context: snapshot.context, origin }
          : null;
        const what = e.lane && snapshot.appliesTo === "lane" ? `${e.laneLabel} lane settings` : `${e.role} settings`;
        if (this.edit === e) this.edit = null;
        this.showNotice(M.noticeText(what, snapshot.fields[0].value, snapshot.fields[0].key, snapshot.file), undo, ctx);
        this.refresh(ctx, { reconcile: true });
      } catch (err) {
        if (this.current(origin)) {
          const rejected = snapshot.fields[applied.length]?.key ?? null;
          this.errors.config = M.saveFailureMessage(applied, rejected,
            snapshot.fields.slice(applied.length + 1).map((f) => f.key), firstLines(err?.message ?? err));
          // The bridge rejected the command, so the team may be partly written (F3).
          this.refresh(ctx, { reconcile: true });
        }
      } finally {
        this.busy.delete("edit-save");
        this.notifySafe(ctx);
      }
    });
  }

  // The notice a handler displayed is still the notice that shows its Undo:
  // the captured plan id, not whatever plan is current when it is clicked
  // (F1, R3-F1). The clicked plan outlives the notice and is used to the end
  // (F3).
  noticeCapture() {
    if (!this.undoHolder) return null;
    const origin = this.originFrom(this.undoHolder.origin ?? this.undoHolder.context);
    return origin ? { id: this.undoHolder.id, origin } : null;
  }

  async undoLast(capture, cx) {
    if (checking() || this.busy.has("undo")) return;
    const holder = this.undoHolder;
    if (!holder || !capture || holder.id !== capture.id) { this.refuse(cx); return; }
    const origin = this.originFrom(holder.origin ?? holder.context);
    if (!origin || !this.current(origin)) {
      this.errors.config = M.CONTEXT_CHANGED_MESSAGE;
      this.undoHolder = null;
      cx.notify();
      return;
    }
    const decision = M.decideWrite({ actionContext: holder.context, currentContext: this.currentContext(), busy: false });
    if (!decision.run) {
      this.errors.config = decision.message;
      this.undoHolder = null;
      cx.notify();
      return;
    }
    this.busy.add("undo");
    cx.notify();
    const plans = holder.plans ?? [holder];
    return this.spawnFrom(cx, async (ctx) => {
      try {
        for (const plan of plans) {
          const r = await exec("soho", plan.args,
            { cwd: holder.context.folder || undefined, on: holder.context.machine, timeout: 20000 });
          if (!this.current(origin)) return;
          if (r.code !== 0) {
            this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
            return;
          }
        }
        this.errors.config = "";
        this.undoHolder = null;
        this.showNotice(`Undid ${holder.key ?? M.planKey(plans[0])}`, null, ctx);
        this.refresh(ctx, { reconcile: true });
      } catch (e) {
        if (this.current(origin)) this.errors.config = firstLines(e?.message ?? e);
      } finally {
        this.busy.delete("undo");
        this.notifySafe(ctx);
      }
    });
  }

  // The file is read in the origin it was opened from, and a late completion
  // for a context that changed is discarded instead of painted (F1, R3-F1).
  // The captured path is the one its footer displayed: a footer replaced by a
  // refresh cannot open the newer path, and opens nothing (R4-F2).
  async openConfig(capture, cx) {
    if (checking()) return;
    const bound = this.originFrom(capture);
    const path = capture?.path ?? "";
    if (!bound || !path || !this.current(bound)) { this.refuse(cx); return; }
    if (path !== this.configFile("project")) { this.refuse(cx); return; }
    const context = M.contextOfOrigin(bound);
    return this.spawnFrom(cx, async (ctx) => {
      try {
        const text = await readTextFile(path, { on: context.machine });
        if (!this.current(bound)) return;
        this.publish("configText", text ?? `Config file is empty: ${path}`, bound);
        this.tab = "config";
        ctx.notify();
      } catch (e) {
        if (!this.current(bound)) return;
        this.errors.config = firstLines(e?.message ?? e);
        ctx.notify();
      }
    });
  }

  // A cleanup confirmation is a token with the origin it was opened in, so a
  // dialog from another context or an earlier confirmation runs no gc
  // (F1, R3-F1). The opener carries the Health origin captured when its button
  // was drawn, and Cancel carries the token of the dialog it dismisses
  // (R4-F2).
  setCleanupConfirm(value, capture, cx) {
    if (value) {
      if (!this.captureCurrent(capture)) {
        // An opener drawn for another project cannot arm the dialog on screen,
        // and leaves none pending: the panel only shows one confirmation, and
        // it is not the one this click belongs to (R4-F2).
        this.cleanupConfirm = null;
        this.refuse(cx);
        cx.notify();
        return;
      }
      this.cleanupSeq += 1;
      this.cleanupConfirm = {
        token: M.confirmationToken(this.cleanupSeq),
        origin: this.originFrom(capture),
      };
    } else {
      // Cancel dismisses only the confirmation that rendered it.
      if (capture?.token && capture.token !== this.cleanupConfirm?.token) return;
      this.cleanupConfirm = null;
    }
    cx.notify();
  }

  cleanup(capture, cx) {
    const confirm = this.cleanupConfirm;
    if (!confirm || !capture || capture.token !== confirm.token) { this.refuse(cx); return; }
    const origin = this.originFrom(confirm.origin);
    if (!origin || !this.current(origin)) {
      this.cleanupConfirm = null;
      this.refuse(cx, M.CONTEXT_CHANGED_MESSAGE);
      return;
    }
    this.cleanupConfirm = null;
    this.runAction(cx, "cleanup", ["gc", "--yes"], "health", null,
      "Orphan copies and processes cleaned up", null, confirm);
  }

  // Probe again runs only where the controller can execute it (F11). Its source
  // comes from the rendered button: the Assistants view or the draft that
  // started it (F5, R3-F5). A draft dismissed while the probe runs has no slot
  // to paint, so its diagnostic is dropped.
  probeAgain(source, cx) {
    if (!this.caps.assistantsJson) return;
    const origin = this.originFrom(source);
    if (!origin || !this.current(origin)) { this.refuse(cx); return; }
    let errorKey = "assistants";
    let kept = () => true;
    if (source.kind === "edit") {
      const draftId = source.draftId;
      if (!this.edit || this.edit.id !== draftId) { this.refuse(cx); return; }
      errorKey = M.probeEditKey(draftId);
      kept = () => Boolean(this.edit) && this.edit.id === draftId;
    }
    this.runAction(cx, "probe", ["setup", "--probe"], "assistants",
      null, "Assistants probed again", null, { origin }, { errorKey, accept: kept });
  }

  render(cx) {
    const t = theme();
    const space = this.space();
    const folder = this.currentFolder();
    // A folder, space or machine change drops the old project's datasets before
    // this paint (the loading state shows, not the old rows), restores that
    // folder's view and sort, and reloads it (F1, F8, F13).
    const context = M.contextFor(space, folder);
    if (!checking() && !this.closed) {
      const changed = this.adoptContext(context, cx);
      this.pruneDatasets();
      this.lastVisibleAt = Date.now();
      if (changed && !this.refreshQueued) {
        this.refreshQueued = true;
        cx.spawn(async (ctx) => {
          this.refreshQueued = false;
          this.refresh(ctx);
        });
      } else if (this.watchDirty) {
        this.watchDirty = false;
        this.requestChangeRefresh();
      }
    }
    const title = this.tab === "report"
      ? (M.findWorker(this.data.roster?.agents, this.reportAgent)?.name ?? "Report")
      : this.tab === "config" ? "Config"
        : this.edit ? `Edit · ${this.edit.laneLabel || this.edit.role || "role"}` : "SoHo";
    // Every rendered write keeps the identity it displayed (F1, R3-F1).
    const probeOrigin = this.originOf("assistants") ?? this.originOf("capabilities");
    const configOrigin = this.originOf("team");
    // The footer names one file and its button opens that file; a refresh that
    // changes the path leaves the old handler with the old path (R4-F2).
    const configPath = this.configFile("project");
    const undoCapture = this.noticeCapture();
    const header = h_flex().h(28).flex_none().gap(8).items_center().justify_between()
      .child(h_flex().min_w(0).gap(8).items_center()
        .when(this.tab === "report", (el) => el.child(button("back-now", "‹ Now", (ctx) => this.setTab("now", ctx), { small: true })))
        .when(this.tab === "config", (el) => el.child(button("back-team", "‹ Team", (ctx) => this.setTab("team", ctx), { small: true })))
        .child(div().min_w(0).truncate().text_size(16).font_semibold().text_color(t.foreground).child(title)))
      .child(h_flex().flex_none().gap(6).items_center()
        .when(this.tab === "assistants" && this.caps.assistantsJson, (el) => el.child(
          button("probe-again", "Probe again",
            (ctx) => this.probeAgain({ kind: "assistants", origin: probeOrigin }, ctx),
            { small: true, disabled: this.isBusy("probe") })))
        .when(this.band === "narrow", (el) => el.child(moreMenu(this, t, cx))));
    const project = div().h(18).flex_none().truncate().text_size(TEXT.caption).text_color(t.dim)
      .child(this.tab === "now" && space ? `${space.name} · ${folder}` : (folder || "no folder"));

    const body = this.renderBody(t, cx);
    const footer = h_flex().h(24).flex_none().gap(8).items_center()
      .child(growText(`From ${M.displayFile(configPath) || "soho config"}`, t.dim, TEXT.caption))
      .when(Boolean(configPath && this.tab !== "config"), (el) => el.child(
        button("open-config", "Open config", (ctx) => this.openConfig({ origin: configOrigin, path: configPath }, ctx), { small: true })))
      .child(div().flex_none().text_size(TEXT.caption).text_color(t.dim)
        .child(this.updatedAt ? "Updated just now" : "Not read yet"));
    return v_flex().id("soho-root").size_full().min_w(0).min_h(0).bg(t.surface).text_color(t.foreground)
      .text_size(TEXT.small).p(10).gap(7)
      .on_size_change((size, ctx) => {
        this.width = size.width;
        if (size.width > 0 && size.height > 0) this.lastVisibleAt = Date.now();
        const band = M.bandForWidth(size.width);
        if (band !== this.band) { this.band = band; ctx.notify(); }
      })
      .child(header)
      .when(this.tab !== "report" && this.tab !== "config", (el) => el.child(toolTabs(this, t, cx)))
      .when(this.tab !== "report" && this.tab !== "config", (el) => el.child(project))
      .when(this.tab === "team", (el) => el.child(this.teamToolbar(t, cx)))
      .when(this.errors.capabilities, (el) => el.child(errorBlock(`capabilities: ${this.errors.capabilities}`, t)))
      .when(this.errors.config, (el) => el.child(errorBlock(this.errors.config, t)))
      .child(body)
      .child(footer)
      .when(Boolean(this.noticeItem), (el) => el.child(notice(this.noticeItem.text, this.undoHolder,
        (ctx) => this.undoLast(undoCapture, ctx), t, { disabled: this.isBusy("undo") })));
  }

  teamToolbar(t, cx) {
    const total = (this.data.team?.roles ?? []).length;
    return h_flex().h(26).flex_none().gap(8).items_center().justify_between()
      .child(this.band === "narrow" ? div()
        : segmentedView(this, t, cx))
      .child(h_flex().gap(8).items_center()
        .when(this.view === "list" && this.band !== "narrow", (el) => el.child(sortMenu(this, t, cx)))
        .child(div().flex_none().text_size(TEXT.caption).text_color(t.dim).child(`${total} roles`)));
  }

  renderBody(t, cx) {
    const loading = !this.firstReadDone;
    if (this.tab === "team" && loading) {
      return v_flex().flex_1().items_center().justify_center()
        .child(div().text_size(TEXT.small).text_color(t.dim).child("Reading the team…"));
    }
    if (this.tab === "team") {
      // The team diagnostic renders in every branch, the empty one included (F14).
      const body = M.teamExists(this.data.team ?? {}, this.data.capabilities ?? this.caps)
        ? this.teamBody(t, cx)
        : renderEmpty(this, t);
      return v_flex().flex_1().min_h(0).gap(6)
        .child(errorBlock(this.errors.team, t))
        .child(body);
    }
    if (this.tab === "now") {
      if (this.band === "wide") {
        const selected = M.findWorker(this.data.roster?.agents, this.selectedWorker);
        return h_flex().flex_1().min_h(0).gap(10)
          .child(v_flex().w(280).flex_none().min_h(0).child(renderNow(this, t, cx, { listOnly: true })))
          .child(v_flex().flex_1().min_w(0).min_h(0)
            .when(selected, (el) => el.child(renderReport(this, t, cx)))
            .when(!selected, (el) => el.child(div().text_size(TEXT.caption).text_color(t.dim)
              .child("Pick a worker to see its report."))));
      }
      return renderNow(this, t, cx);
    }
    if (this.tab === "report") return renderReport(this, t, cx);
    if (this.tab === "assistants") return renderAssistants(this, t);
    if (this.tab === "health") return renderHealth(this, t, cx);
    if (this.tab === "config") return markdown("soho-config", this.data.configText ?? "");
    return div();
  }

  teamBody(t, cx) {
    const list = this.view === "list" ? renderList(this, t, cx) : renderGrouped(this, t, cx);
    if (this.band === "wide" && this.edit) {
      return h_flex().flex_1().min_h(0).gap(10)
        .child(v_flex().w(320).flex_none().min_h(0).child(list))
        .child(v_flex().flex_1().min_w(0).min_h(0).overflow_y_scrollbar().child(renderEdit(this, t, cx)));
    }
    if (this.edit) return renderEdit(this, t, cx);
    return list;
  }
}

function segmentedView(app, t, cx) {
  const choices = [{ id: "grouped", label: "Grouped" }, { id: "list", label: "List" }];
  return h_flex().p(3).gap(3).rounded(7).bg(t.background)
    .children(choices.map((c) => div().id(`view-${c.id}`).h(22).px(9).items_center().rounded(5).cursor_pointer()
      .bg(app.view === c.id ? t.raised : t.background)
      .text_color(app.view === c.id ? t.foreground : t.muted)
      .text_size(TEXT.caption).hover((s) => s.text_color(t.foreground))
      .on_click((_e, ctx) => app.setView(c.id, ctx)).child(c.label)));
}
