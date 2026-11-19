// SoHo panel for Herdr.app. Reads soho on the shown space's machine and keeps
// the team, workers, reports, assistants and health in one mod. All side
// effects are user actions and are guarded while `herdr-app check` loads.
import { View, div } from "gpui-kit";
import { v_flex, h_flex, InputState } from "gpui-base";
import { exec, spaces, focused, theme, checking, run, openOverlay, readTextFile, nextCommand } from "herdr";
import * as M from "./model.js";
import { button, errorBlock, growText, markdown, notice, TEXT } from "./ui.js";
import {
  renderAssistants, renderEdit, renderEmpty, renderGrouped, renderHealth,
  renderList, renderNow, renderReport, sortMenu, toolTabs, moreMenu,
} from "./views.js";

const NOTICE_MS = 8000;
const REFRESH_MS = 15000;
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
      assistants: null, kindsText: "", modelsByKind: {}, choices: null, health: null,
    };
    this.errors = {};
    this.notes = {};
    this.caps = M.capabilities(null);
    this.busy = new Set();
    // Reads advance this sequence; writes carry their own context instead, so a
    // refresh never abandons a write in flight (F2).
    this.readSeq = 0;
    this.context = null;
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
    this.cleanupConfirm = false;
    this.cleanupContext = null;
    this.configText = null;
    this.updatedAt = null;
    this.lastVisibleAt = 0;

    const defaultFolder = this.currentFolder();
    this.folderKnown = defaultFolder;
    this.context = M.contextFor(this.space(), defaultFolder);
    this.applyFolderPrefs(defaultFolder);

    if (checking()) return;
    this.bindModelInput();
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

  sameAs(context) {
    return M.sameContext(context, this.currentContext());
  }

  // A context change invalidates every pending action and restores this
  // folder's own view and sort (F1, F13).
  adoptContext(context, cx) {
    const changed = this.context !== null && !M.sameContext(this.context, context);
    this.context = context;
    this.folderKnown = context.folder;
    this.applyFolderPrefs(context.folder);
    if (!changed) return;
    let dropped = false;
    if (this.edit && !M.sameContext(this.edit.context, context)) { this.edit = null; dropped = true; }
    if (this.undoHolder && !M.sameContext(this.undoHolder.context, context)) { this.undoHolder = null; dropped = true; }
    if (this.pending) { this.pending = null; dropped = true; }
    if (this.cleanupConfirm) { this.cleanupConfirm = false; this.cleanupContext = null; dropped = true; }
    if (dropped) this.showNotice(M.CONTEXT_CHANGED_MESSAGE, null, cx);
  }

  applyFolderPrefs(folder) {
    const state = M.viewStateFor(this.readPrefs(), folder, { view: "grouped", sort: { column: "role", dir: "asc" } });
    this.view = state.view;
    this.sort = state.sort;
  }

  // A read is accepted only for the space, machine and folder it started in.
  alive(gen, context) {
    return gen === this.readSeq && M.sameContext(context, this.currentContext());
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
    this.undoHolder = undo?.available ? undo : null;
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

  // One read: JSON or text, first stderr lines in the section it feeds. Reads
  // carry their own sequence; a busy key and an error key can differ from the
  // section so two consumers do not suppress or erase each other (F9, F14).
  async runSection(cx, section, args, gen, context, json = true, opts = {}) {
    const busyKey = opts.busyKey ?? `${section}|${context.folder}`;
    const errorKey = opts.errorKey ?? section;
    if (this.busy.has(busyKey)) return null;
    this.busy.add(busyKey);
    try {
      const r = await exec("soho", args, { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
      if (!this.alive(gen, context)) return null;
      if (r.code !== 0) {
        // A refusal the panel cannot change is a neutral note, not an error (decision 3).
        const note = opts.note ? opts.note(r.stderr) : "";
        if (note) {
          this.notes[errorKey] = note;
          this.errors[errorKey] = "";
          return null;
        }
        this.notes[errorKey] = "";
        this.errors[errorKey] = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
        return null;
      }
      this.errors[errorKey] = "";
      this.notes[errorKey] = "";
      if (!json) return r.stdout ?? "";
      try {
        return JSON.parse(r.stdout);
      } catch {
        this.errors[errorKey] = M.nonJsonMessage(`soho ${args.join(" ")}`, r.stderr);
        return null;
      }
    } catch (e) {
      if (this.alive(gen, context)) this.errors[errorKey] = firstLines(e?.message ?? e);
      return null;
    } finally {
      this.busy.delete(busyKey);
      this.notifySafe(cx);
    }
  }

  refresh(cx) {
    if (checking()) return;
    const space = this.space();
    if (!space) {
      this.errors.team = "no space shown";
      this.firstReadDone = true;
      cx.notify();
      return;
    }
    const folder = this.folderOf(space);
    const context = M.contextFor(space, folder);
    this.adoptContext(context, cx);
    const gen = ++this.readSeq;
    this.folderKnown = folder;
    cx.spawn(async (ctx) => {
      const capsP = this.runSection(ctx, "capabilities", ["capabilities", "--json"], gen, context);
      const teamP = this.runSection(ctx, "team", ["roles", "--json"], gen, context);
      const rosterP = this.runSection(ctx, "roster", ["roster", "--json"], gen, context, true,
        { note: M.rosterRefusal });
      const [capsRaw, team, roster] = await Promise.all([capsP, teamP, rosterP]);
      if (!this.alive(gen, context)) return;
      // A failed or malformed capability answer means the no-new-keys default,
      // a visible diagnostic and the independent reads still run (F12).
      const answer = M.capabilityAnswer(capsRaw);
      if (answer.caps) {
        this.data.capabilities = answer.caps;
        this.caps = M.capabilities(answer.caps);
      } else {
        this.data.capabilities = null;
        this.caps = M.capabilities(null);
        if (!this.errors.capabilities) this.errors.capabilities = answer.error;
      }
      if (team) this.data.team = team;
      if (roster) this.data.roster = M.normalizeRoster(roster);
      // A hidden tab never stays selected: the fallback happens before the render
      // that would paint it (decision 4).
      this.tab = M.resolveTab(this.tab, this.caps);
      this.firstReadDone = true;
      this.updatedAt = Date.now();
      ctx.notify();

      await this.readChoices(ctx, gen, context);
      if (!this.alive(gen, context)) return;
      const reads = M.sectionReads(this.data.capabilities ?? this.caps);
      if (reads.health) {
        const health = await this.runSection(ctx, "health", ["health", "--json"], gen, context);
        if (this.alive(gen, context) && health) this.data.health = health;
      }
      const agent = this.reportAgent
        || (this.tab === "now" && this.band === "wide" ? this.selectedWorker : "");
      if (agent) await this.loadReport(agent, gen, context, ctx);
      if (this.alive(gen, context)) ctx.notify();
    });
  }

  async readChoices(cx, gen, context) {
    if (checking()) return;
    const reads = M.sectionReads(this.caps);
    if (reads.assistantsJson) {
      const assistants = await this.runSection(cx, "assistants", ["kinds", "--json"], gen, context);
      if (!this.alive(gen, context) || !assistants) return;
      this.data.assistants = assistants;
      this.data.choices = M.assistantChoices(assistants, "", {}, this.caps);
    } else {
      const text = await this.runSection(cx, "assistants", ["kinds"], gen, context, false);
      if (!this.alive(gen, context) || text === null) return;
      this.data.kindsText = text;
      const modelsByKind = {};
      for (const a of M.parseKindsText(text).filter((row) => row.installed)) {
        const models = await this.runSection(cx, "assistants", ["models", a.kind], gen, context, false,
          { errorKey: M.modelsErrorKey(a.kind), busyKey: `assistants|${context.folder}` });
        if (!this.alive(gen, context)) return;
        modelsByKind[a.kind] = models ?? "";
      }
      this.data.modelsByKind = modelsByKind;
      this.data.choices = M.assistantChoices(null, text, modelsByKind, this.caps);
    }
    if (this.alive(gen, context)) cx.notify();
  }

  async loadReport(agentKey, gen, context, cx) {
    if (checking()) return;
    const worker = M.findWorker(this.data.roster?.agents, agentKey);
    if (!worker) return;
    const requested = M.workerKey(worker);
    this.reportAgent = requested;
    if (this.caps.reportJson) {
      const report = await this.runSection(cx, "report", ["report", worker.name, "--json"], gen, context, true,
        { busyKey: M.reportBusyKey(context.folder, requested) });
      if (!this.alive(gen, context) || !M.acceptsReport(this.reportAgent, requested)) return;
      if (report) {
        this.data.report = report;
        this.data.reportMarkdown = null;
      }
    } else if (worker.report_path) {
      try {
        const path = worker.report_path;
        const text = await readTextFile(path, { on: context.machine });
        if (!this.alive(gen, context) || !M.acceptsReport(this.reportAgent, requested)) return;
        this.data.reportMarkdown = text ?? `Report file is empty: ${path}`;
        this.data.report = null;
        this.errors.report = "";
      } catch (e) {
        if (this.alive(gen, context) && M.acceptsReport(this.reportAgent, requested)) {
          this.errors.report = firstLines(e?.message ?? e);
        }
      }
    } else {
      this.errors.report = "soho has no report --json and this worker has no report path";
    }
    if (this.alive(gen, context) && M.acceptsReport(this.reportAgent, requested)) cx.notify();
  }

  // Every state-changing action goes through here: no work while checking, and
  // a write runs only in the context it was opened in (F1). The busy state
  // always clears with a notify (F2).
  async runAction(cx, key, args, section, onSuccess, text, undo, actionContext) {
    if (checking()) return;
    const context = actionContext ?? this.currentContext();
    const decision = M.decideWrite({ actionContext: context, currentContext: this.currentContext(), busy: this.busy.has(key) });
    if (!decision.run) {
      if (decision.message) { this.errors[section] = decision.message; cx.notify(); }
      return;
    }
    this.busy.add(key);
    cx.notify();
    try {
      const r = await exec("soho", args, { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
      if (!this.sameAs(context)) return;
      if (r.code !== 0) {
        this.errors[section] = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
        return;
      }
      this.errors[section] = "";
      if (onSuccess) onSuccess(r);
      if (text) this.showNotice(text, undo, cx);
      this.refresh(cx);
    } catch (e) {
      if (this.sameAs(context)) this.errors[section] = firstLines(e?.message ?? e);
    } finally {
      this.busy.delete(key);
      this.notifySafe(cx);
    }
  }

  // Herdr delivers the suffix only: "close" for "soho-team.close" (F4).
  handleCommand(command, cx) {
    if (checking()) return;
    const pending = this.pending;
    const match = M.confirmationMatches(pending, command, this.data.roster?.agents ?? []);
    if (!match.ok) return;
    this.pending = null;
    if (pending.action === "close") {
      this.runAction(cx, `close:${pending.key}`, ["release", pending.name, "--close"], "roster",
        null, `${pending.name} closed`, null, pending.context);
    } else if (pending.action === "reopen") {
      this.runAction(cx, `reopen:${pending.key}`, ["reopen", pending.name], "roster",
        null, `${pending.name} reopened`, null, pending.context);
    }
  }

  // Controller callbacks used by the views.
  setTab(id, cx) {
    this.tab = id;
    this.menuOpen = "";
    this.cleanupConfirm = false;
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
    if (id === "refresh") this.refresh(cx);
    else if (id === "grouped" || id === "list") this.setView(id, cx);
    else if (id.startsWith("sort:")) this.setSort(id.slice(5), cx);
  }

  peek(worker, cx) {
    if (worker?.pane) run("terminal.peek", { terminal: worker.pane });
    else this.showNotice("No terminal address for this worker", null, cx);
  }

  showWorkerReport(worker, cx) {
    if (!worker) return;
    this.selectedWorker = M.workerKey(worker);
    this.reportAgent = this.selectedWorker;
    this.data.report = null;
    this.data.reportMarkdown = null;
    this.errors.report = "";
    cx.notify();
    if (!checking()) {
      const context = M.contextFor(this.space(), this.currentFolder());
      this.loadReport(this.reportAgent, this.readSeq, context, cx);
    }
  }

  openReport(worker, cx) {
    if (!worker) return;
    this.showWorkerReport(worker, cx);
    this.tab = "report";
    this.menuOpen = "";
    cx.notify();
  }

  confirmClose(worker, cx) {
    if (checking() || !worker) return;
    this.pending = { action: "close", name: worker.name, key: M.workerKey(worker), context: this.currentContext() };
    openOverlay({ mod: "confirm", kind: "dialog", width: 440, props: {
      title: "Close this worker?",
      detail: `Release ${worker.name} and close its pane. Its report stays available in the team's state.`,
      command: { name: "soho-team.close", args: { name: worker.name } },
    } });
  }

  confirmReopen(worker, cx) {
    if (checking() || !worker) return;
    this.pending = { action: "reopen", name: worker.name, key: M.workerKey(worker), context: this.currentContext() };
    openOverlay({ mod: "confirm", kind: "dialog", width: 440, props: {
      title: "Reopen this worker?",
      detail: `Start ${worker.name} fresh with the same role and assistant.`,
      command: { name: "soho-team.reopen", args: { name: worker.name } },
    } });
  }

  async setEffort(row, value, cx) {
    if (checking() || !row || EFFORTS.includes(value) === false) return;
    const target = M.effortTarget(row, this.groups());
    const scope = "project";
    const key = `effort:${target.key}`;
    const context = this.currentContext();
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
    const argv = M.configSetArgv(target.key, value, scope);
    try {
      if (this.caps.configDryRun) {
        const r = await exec("soho", [...argv, "--dry-run", "--json"],
          { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
        if (!this.sameAs(context)) return;
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
      if (!this.sameAs(context)) return;
      if (r.code !== 0) {
        this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
        return;
      }
      const what = target.lane ? `${target.label} lane effort` : `${row.role} effort`;
      const undo = M.undoPlan(previous, target.key, scope, this.caps);
      this.showNotice(M.noticeText(what, value, target.key, file),
        undo.available ? { ...undo, context } : null, cx);
      this.refresh(cx);
    } catch (e) {
      if (this.sameAs(context)) this.errors.config = firstLines(e?.message ?? e);
    } finally {
      this.busy.delete(key);
      this.notifySafe(cx);
    }
  }

  openEdit(sel, cx) {
    if (checking()) return;
    const group = sel.lane ? this.groups().find((g) => g.lane === sel.lane) : null;
    const row = sel.role ? this.roleByName(sel.role) : null;
    if (!group && !row) return;
    const byLane = Boolean(group) && sel.appliesTo !== "role";
    const targetRole = group ? M.roleForScope(sel.role, group) : (row?.role ?? "");
    this.edit = {
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
      context: this.currentContext(),
      token: 0,
    };
    this.tab = "team";
    this.menuOpen = "";
    this.bindModelInput();
    this.modelInput.set_value(this.edit.model);
    cx.notify();
    this.readChoices(cx, this.readSeq, M.contextFor(this.space(), this.currentFolder()));
    this.previewEdit(cx);
  }

  closeEdit(cx) {
    this.edit = null;
    this.menuOpen = "";
    cx.notify();
  }

  setEditField(field, value, cx) {
    if (!this.edit || this.busy.has("edit-save")) return;
    this.edit[field] = value;
    if (field === "model") this.modelInput.set_value(value);
    this.menuOpen = "";
    this.previewEdit(cx);
  }

  // Switching Applies to keeps the role that was opened (F5).
  setEditAppliesTo(appliesTo, cx) {
    if (!this.edit || this.busy.has("edit-save")) return;
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
  // while the new one runs and Save waits for it (F7).
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
      cx.notify();
      return;
    }
    if (!fields.length) {
      e.pending = false;
      e.preview = [];
      e.valid = true;
      e.error = "";
      e.previousByKey = {};
      cx.notify();
      return;
    }
    const context = M.contextFor(this.space(), this.currentFolder());
    let preview;
    if (this.caps.configDryRun) {
      const results = [];
      for (const f of fields) {
        const argv = [...M.configSetArgv(f.key, f.value, e.scope), "--dry-run", "--json"];
        try {
          const r = await exec("soho", argv, { cwd: context.folder || undefined, on: context.machine, timeout: 20000 });
          if (this.edit !== e || token !== e.token) return;
          if (!M.sameContext(context, this.currentContext())) {
            e.pending = false;
            e.valid = false;
            e.snapshot = null;
            e.error = M.CONTEXT_CHANGED_MESSAGE;
            cx.notify();
            return;
          }
          if (r.code !== 0) {
            e.error = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
            e.preview = [];
            e.valid = false;
            e.pending = false;
            cx.notify();
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
            cx.notify();
            return;
          }
          results.push(result);
        } catch (err) {
          if (this.edit !== e || token !== e.token) return;
          e.error = firstLines(err?.message ?? err);
          e.preview = [];
          e.valid = false;
          e.pending = false;
          cx.notify();
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
    cx.notify();
  }

  async saveEdit(cx) {
    if (checking() || !this.edit || this.busy.has("edit-save")) return;
    const e = this.edit;
    const state = M.previewState(e);
    if (!state.canSave) return;
    const snapshot = e.snapshot;
    if (!M.snapshotMatches(snapshot, e)) {
      e.error = "The preview is out of date; wait for it to update before saving.";
      cx.notify();
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
    try {
      for (let i = 0; i < snapshot.fields.length; i += 1) {
        const f = snapshot.fields[i];
        const r = await exec("soho", M.configSetArgv(f.key, f.value, scope),
          { cwd: snapshot.context.folder || undefined, on: snapshot.context.machine, timeout: 20000 });
        if (!this.sameAs(snapshot.context)) return;
        if (r.code !== 0) {
          const detail = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
          this.errors.config = M.partialFailureMessage(applied,
            snapshot.fields.slice(i).map((x) => x.key), detail);
          this.refresh(cx);
          return;
        }
        applied.push(f.key);
      }
      this.errors.config = "";
      const plans = snapshot.fields.map((f) => M.undoPlan(snapshot.previousByKey[f.key] ?? null, f.key, scope, this.caps));
      const undo = plans.every((p) => p.available)
        ? { available: true, plans, key: snapshot.fields[0].key, context: snapshot.context }
        : null;
      const what = e.lane && snapshot.appliesTo === "lane" ? `${e.laneLabel} lane settings` : `${e.role} settings`;
      if (this.edit === e) this.edit = null;
      this.showNotice(M.noticeText(what, snapshot.fields[0].value, snapshot.fields[0].key, snapshot.file), undo, cx);
      this.refresh(cx);
    } catch (err) {
      if (this.sameAs(snapshot.context)) {
        this.errors.config = M.partialFailureMessage(applied,
          snapshot.fields.slice(applied.length).map((f) => f.key), firstLines(err?.message ?? err));
      }
    } finally {
      this.busy.delete("edit-save");
      this.notifySafe(cx);
    }
  }

  // The clicked plan outlives the notice and is used to the end (F3).
  async undoLast(cx) {
    if (checking() || !this.undoHolder || this.busy.has("undo")) return;
    const holder = this.undoHolder;
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
    try {
      for (const plan of plans) {
        const r = await exec("soho", plan.args,
          { cwd: holder.context.folder || undefined, on: holder.context.machine, timeout: 20000 });
        if (!this.sameAs(holder.context)) return;
        if (r.code !== 0) {
          this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
          return;
        }
      }
      this.errors.config = "";
      this.undoHolder = null;
      this.showNotice(`Undid ${holder.key ?? M.planKey(plans[0])}`, null, cx);
      this.refresh(cx);
    } catch (e) {
      if (this.sameAs(holder.context)) this.errors.config = firstLines(e?.message ?? e);
    } finally {
      this.busy.delete("undo");
      this.notifySafe(cx);
    }
  }

  async openConfig(cx) {
    if (checking()) return;
    const path = this.configFile("project");
    if (!path) return;
    const context = this.currentContext();
    try {
      const text = await readTextFile(path, { on: context.machine });
      this.configText = text ?? `Config file is empty: ${path}`;
      this.tab = "config";
      cx.notify();
    } catch (e) {
      this.errors.config = firstLines(e?.message ?? e);
      cx.notify();
    }
  }

  setCleanupConfirm(value, cx) {
    this.cleanupConfirm = value;
    this.cleanupContext = value ? this.currentContext() : null;
    cx.notify();
  }

  cleanup(cx) {
    const context = this.cleanupContext ?? this.currentContext();
    this.cleanupConfirm = false;
    this.cleanupContext = null;
    this.runAction(cx, "cleanup", ["gc", "--yes"], "health", null,
      "Orphan copies and processes cleaned up", null, context);
  }

  probeAgain(cx) {
    if (!this.caps.assistantsJson) return;
    this.runAction(cx, "probe", ["setup", "--probe"], "assistants", null, "Assistants probed again");
  }

  render(cx) {
    const t = theme();
    const space = this.space();
    const folder = this.currentFolder();
    // A folder, space or machine change reloads that project's data, view and
    // sort, and drops pending actions that belonged to the old context
    // (F1, F13).
    const context = M.contextFor(space, folder);
    if (!checking() && this.context && !M.sameContext(this.context, context) && !this.refreshQueued) {
      this.refreshQueued = true;
      cx.spawn(async (ctx) => {
        this.refreshQueued = false;
        this.refresh(ctx);
      });
    }
    if (!checking()) this.lastVisibleAt = Date.now();
    const title = this.tab === "report"
      ? (M.findWorker(this.data.roster?.agents, this.reportAgent)?.name ?? "Report")
      : this.tab === "config" ? "Config"
        : this.edit ? `Edit · ${this.edit.laneLabel || this.edit.role || "role"}` : "SoHo";
    const header = h_flex().h(28).flex_none().gap(8).items_center().justify_between()
      .child(h_flex().min_w(0).gap(8).items_center()
        .when(this.tab === "report", (el) => el.child(button("back-now", "‹ Now", () => this.setTab("now", cx), { small: true })))
        .when(this.tab === "config", (el) => el.child(button("back-team", "‹ Team", () => this.setTab("team", cx), { small: true })))
        .child(div().min_w(0).truncate().text_size(16).font_semibold().text_color(t.foreground).child(title)))
      .child(h_flex().flex_none().gap(6).items_center()
        .when(this.tab === "assistants" && this.caps.assistantsJson, (el) => el.child(
          button("probe-again", "Probe again", (ctx) => this.probeAgain(ctx),
            { small: true, disabled: this.isBusy("probe") })))
        .when(this.band === "narrow", (el) => el.child(moreMenu(this, t, cx)))
        .child(button("refresh", "Refresh", (ctx) => this.refresh(ctx),
          { small: true, disabled: this.isBusy("team|" + folder) })));
    const project = div().h(18).flex_none().truncate().text_size(TEXT.caption).text_color(t.dim)
      .child(this.tab === "now" && space ? `${space.name} · ${folder}` : (folder || "no folder"));

    const body = this.renderBody(t, cx);
    const footer = h_flex().h(24).flex_none().gap(8).items_center()
      .child(growText(`From ${M.displayFile(this.configFile("project")) || "soho config"}`, t.dim, TEXT.caption))
      .when(Boolean(this.configFile("project") && this.tab !== "config"), (el) => el.child(
        button("open-config", "Open config", (ctx) => this.openConfig(ctx), { small: true })))
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
      .when(Boolean(this.noticeItem), (el) => el.child(notice(this.noticeItem.text, this.undoHolder,
        (ctx) => this.undoLast(ctx), t, { disabled: this.isBusy("undo") })));
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
    if (this.tab === "config") return markdown("soho-config", this.configText ?? "");
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
