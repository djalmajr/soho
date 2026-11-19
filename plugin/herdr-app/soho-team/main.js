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
    this.caps = M.capabilities(null);
    this.busy = new Set();
    this.generation = 0;
    this.folderKnown = null;
    this.firstReadDone = false;
    this.hovered = "";
    this.menuOpen = "";
    this.selectedWorker = "";
    this.reportAgent = "";
    this.edit = null;
    this.modelInput = InputState.new({ value: "" });
    this.menuFocus = cx.focus_handle();
    this.moreFocus = cx.focus_handle();
    this.noticeItem = null;
    this.noticeTimer = null;
    this.undo = null;
    this.cleanupConfirm = false;
    this.configText = null;
    this.updatedAt = null;

    const prefs = this.readPrefs();
    const defaultFolder = this.currentFolder();
    if (prefs[defaultFolder]?.view === "list") this.view = "list";
    if (prefs[defaultFolder]?.sort) this.sort = prefs[defaultFolder].sort;

    if (checking()) return;
    this.refresh(cx);
    cx.timer.every(REFRESH_MS, (ctx) => this.refresh(ctx));
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

  alive(generation, folder) {
    return generation === this.generation && this.currentFolder() === folder;
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
    this.undo = undo?.available ? undo : null;
    this.noticeTimer?.cancel();
    this.noticeTimer = cx.timer.after(NOTICE_MS, (ctx) => {
      if (this.noticeItem === item) {
        this.noticeItem = null;
        this.undo = null;
      }
      ctx.notify();
    });
  }

  // One read: JSON or text, first stderr lines in the section it feeds.
  async runSection(cx, section, args, gen, folder, space, json = true) {
    const key = `${section}|${folder}`;
    if (this.busy.has(key)) return null;
    this.busy.add(key);
    try {
      const r = await exec("soho", args, { cwd: folder || undefined, on: space?.machine, timeout: 20000 });
      if (!this.alive(gen, folder)) return null;
      if (r.code !== 0) {
        this.errors[section] = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
        return null;
      }
      this.errors[section] = "";
      if (!json) return r.stdout ?? "";
      try {
        return JSON.parse(r.stdout);
      } catch {
        this.errors[section] = `soho ${args.join(" ")} did not return JSON`;
        return null;
      }
    } catch (e) {
      if (this.alive(gen, folder)) this.errors[section] = firstLines(e?.message ?? e);
      return null;
    } finally {
      this.busy.delete(key);
      if (this.alive(gen, folder)) cx.notify();
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
    const gen = ++this.generation;
    this.folderKnown = folder;
    cx.spawn(async (ctx) => {
      const capsP = this.runSection(ctx, "capabilities", ["capabilities", "--json"], gen, folder, space);
      const teamP = this.runSection(ctx, "team", ["roles", "--json"], gen, folder, space);
      const rosterP = this.runSection(ctx, "roster", ["roster", "--json"], gen, folder, space);
      const [caps, team, roster] = await Promise.all([capsP, teamP, rosterP]);
      if (!this.alive(gen, folder)) return;
      if (caps) {
        this.data.capabilities = caps;
        this.caps = M.capabilities(caps);
      }
      if (team) this.data.team = team;
      if (roster) this.data.roster = roster;
      this.firstReadDone = true;
      this.updatedAt = Date.now();
      ctx.notify();

      if (!caps) return;
      await this.readChoices(ctx, gen, folder, space);
      if (!this.alive(gen, folder)) return;
      if (this.caps.healthJson) {
        const health = await this.runSection(ctx, "health", ["health", "--json"], gen, folder, space);
        if (this.alive(gen, folder) && health) this.data.health = health;
      }
      const agent = this.reportAgent
        || (this.tab === "now" && this.band === "wide" ? this.selectedWorker : "");
      if (agent) await this.loadReport(agent, gen, folder, space, ctx);
      if (this.alive(gen, folder)) ctx.notify();
    });
  }

  async readChoices(cx, gen, folder, space) {
    if (checking()) return;
    if (this.caps.assistantsJson) {
      const assistants = await this.runSection(cx, "assistants", ["kinds", "--json"], gen, folder, space);
      if (!this.alive(gen, folder) || !assistants) return;
      this.data.assistants = assistants;
      this.data.choices = M.assistantChoices(assistants, "", {}, this.caps);
    } else {
      const text = await this.runSection(cx, "assistants", ["kinds"], gen, folder, space, false);
      if (!this.alive(gen, folder) || text === null) return;
      this.data.kindsText = text;
      const modelsByKind = {};
      for (const a of M.parseKindsText(text).filter((row) => row.installed)) {
        const models = await this.runSection(cx, "assistants", ["models", a.kind], gen, folder, space, false);
        if (!this.alive(gen, folder)) return;
        modelsByKind[a.kind] = models ?? "";
      }
      this.data.modelsByKind = modelsByKind;
      this.data.choices = M.assistantChoices(null, text, modelsByKind, this.caps);
    }
    if (this.alive(gen, folder)) cx.notify();
  }

  async loadReport(agentKey, gen, folder, space, cx) {
    if (checking()) return;
    const worker = M.findWorker(this.data.roster?.agents, agentKey);
    if (!worker) return;
    this.reportAgent = M.workerKey(worker);
    if (this.caps.reportJson) {
      const report = await this.runSection(cx, "report", ["report", worker.name, "--json"], gen, folder, space);
      if (!this.alive(gen, folder)) return;
      if (report) {
        this.data.report = report;
        this.data.reportMarkdown = null;
      }
    } else if (worker.report_path) {
      try {
        const path = worker.report_path;
        const text = await readTextFile(path, { on: space?.machine });
        if (!this.alive(gen, folder)) return;
        this.data.reportMarkdown = text ?? `Report file is empty: ${path}`;
        this.data.report = null;
        this.errors.report = "";
      } catch (e) {
        if (this.alive(gen, folder)) this.errors.report = firstLines(e?.message ?? e);
      }
    } else {
      this.errors.report = "soho has no report --json and this worker has no report path";
    }
    if (this.alive(gen, folder)) cx.notify();
  }

  // Every state-changing action goes through here: no work while checking.
  async runAction(cx, key, args, section, onSuccess, text, undo) {
    if (checking() || this.busy.has(key)) return;
    this.busy.add(key);
    cx.notify();
    const folder = this.currentFolder();
    const space = this.space();
    const gen = this.generation;
    try {
      const r = await exec("soho", args, { cwd: folder || undefined, on: space?.machine, timeout: 20000 });
      if (!this.alive(gen, folder)) return;
      if (r.code !== 0) {
        this.errors[section] = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
        return;
      }
      this.errors[section] = "";
      if (onSuccess) onSuccess(r);
      if (text) this.showNotice(text, undo, cx);
      this.refresh(cx);
    } catch (e) {
      if (this.alive(gen, folder)) this.errors[section] = firstLines(e?.message ?? e);
    } finally {
      this.busy.delete(key);
      if (this.alive(gen, folder)) cx.notify();
    }
  }

  handleCommand(command, cx) {
    if (checking() || typeof command !== "string") return;
    const [mod, name, ...rest] = command.split(".");
    if (mod !== "soho-team" || rest.length) return;
    const args = this.pendingCommandArgs ?? {};
    if (name === "close") {
      this.runAction(cx, `close:${args.name}`, ["release", args.name, "--close"], "roster",
        null, `${args.name} closed`);
    } else if (name === "reopen") {
      this.runAction(cx, `reopen:${args.name}`, ["reopen", args.name], "roster",
        null, `${args.name} reopened`);
    }
    this.pendingCommandArgs = null;
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
      const space = this.space();
      const folder = this.currentFolder();
      this.loadReport(this.reportAgent, this.generation, folder, space, cx);
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
    this.pendingCommandArgs = { name: worker.name, terminal: worker.pane ?? "" };
    openOverlay({ mod: "confirm", kind: "dialog", width: 440, props: {
      title: "Close this worker?",
      detail: `Release ${worker.name} and close its pane. Its report stays available in the team's state.`,
      command: { name: "soho-team.close", args: { name: worker.name } },
    } });
  }

  confirmReopen(worker, cx) {
    if (checking() || !worker) return;
    this.pendingCommandArgs = { name: worker.name, terminal: worker.pane ?? "" };
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
    const flag = "--project";
    const key = `effort:${target.key}`;
    if (this.busy.has(key)) return;
    this.menuOpen = "";
    this.busy.add(key);
    cx.notify();
    const folder = this.currentFolder();
    const space = this.space();
    const gen = this.generation;
    let previous = null;
    let file = this.configFile(scope);
    try {
      if (this.caps.configDryRun) {
        const r = await exec("soho", ["config", "set", target.key, value, flag, "--dry-run", "--json"],
          { cwd: folder || undefined, on: space?.machine, timeout: 20000 });
        if (!this.alive(gen, folder)) return;
        if (r.code !== 0) {
          this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
          return;
        }
        let plan;
        try {
          plan = JSON.parse(r.stdout);
        } catch {
          this.errors.config = "soho config set --dry-run --json did not return JSON";
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
      const args = ["config", "set", target.key, value, flag];
      const r = await exec("soho", args, { cwd: folder || undefined, on: space?.machine, timeout: 20000 });
      if (!this.alive(gen, folder)) return;
      if (r.code !== 0) {
        this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
        return;
      }
      const what = target.lane ? `${target.label} lane effort` : `${row.role} effort`;
      const undo = M.undoPlan(previous, target.key, scope, this.caps);
      this.showNotice(M.noticeText(what, value, target.key, file), undo, cx);
      this.refresh(cx);
    } catch (e) {
      if (this.alive(gen, folder)) this.errors.config = firstLines(e?.message ?? e);
    } finally {
      this.busy.delete(key);
      if (this.alive(gen, folder)) cx.notify();
    }
  }

  openEdit(sel, cx) {
    if (checking()) return;
    const group = sel.lane ? this.groups().find((g) => g.lane === sel.lane) : null;
    const row = sel.role ? this.roleByName(sel.role) : null;
    if (!group && !row) return;
    const byLane = Boolean(group);
    this.edit = {
      appliesTo: byLane ? "lane" : "role",
      lane: group?.lane ?? row?.lane ?? "",
      laneLabel: group?.label ?? "",
      roleCount: group?.roles?.length ?? 1,
      role: byLane ? "" : row.role,
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
      token: 0,
    };
    this.tab = "team";
    this.menuOpen = "";
    this.modelInput.set_value(this.edit.model);
    cx.notify();
    const space = this.space();
    const folder = this.currentFolder();
    this.readChoices(cx, this.generation, folder, space);
    this.previewEdit(cx);
  }

  closeEdit(cx) {
    this.edit = null;
    this.menuOpen = "";
    cx.notify();
  }

  setEditField(field, value, cx) {
    if (!this.edit) return;
    this.edit[field] = value;
    if (field === "model") this.modelInput.set_value(value);
    this.menuOpen = "";
    this.previewEdit(cx);
  }

  setEditAppliesTo(appliesTo, cx) {
    if (!this.edit) return;
    const group = this.groups().find((g) => g.lane === this.edit.lane);
    const row = this.roleByName(this.edit.role);
    this.edit.appliesTo = appliesTo;
    if (!group) return;
    if (appliesTo === "lane") {
      this.edit.kind = group.kind || "";
      this.edit.model = group.model || "";
      this.edit.effort = group.effort || "";
    } else if (group.roles[0]) {
      const first = this.roleByName(group.roles[0]) ?? {};
      this.edit.role = first.role ?? "";
      this.edit.kind = first.kind || "";
      this.edit.model = first.model || "";
      this.edit.effort = first.effort || "";
    }
    this.modelInput.set_value(this.edit.model);
    this.previewEdit(cx);
    void row;
  }

  async previewEdit(cx) {
    if (checking() || !this.edit) return;
    const e = this.edit;
    const token = ++e.token;
    const fields = M.editFields(e);
    if (!fields.length) {
      e.preview = [];
      e.valid = true;
      e.error = "";
      e.previousByKey = {};
      cx.notify();
      return;
    }
    let preview;
    if (this.caps.configDryRun) {
      const folder = this.currentFolder();
      const space = this.space();
      const gen = this.generation;
      const results = [];
      for (const f of fields) {
        const flag = e.scope === "user" ? "--user" : "--project";
        try {
          const r = await exec("soho", ["config", "set", f.key, f.value, flag, "--dry-run", "--json"],
            { cwd: folder || undefined, on: space?.machine, timeout: 20000 });
          if (this.edit !== e || token !== e.token || !this.alive(gen, folder)) return;
          if (r.code !== 0) {
            e.error = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
            e.preview = [];
            e.valid = false;
            cx.notify();
            return;
          }
          results.push(JSON.parse(r.stdout));
        } catch (err) {
          e.error = firstLines(err?.message ?? err);
          e.preview = [];
          e.valid = false;
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
    cx.notify();
  }

  async saveEdit(cx) {
    if (checking() || !this.edit || this.busy.has("edit-save")) return;
    const e = this.edit;
    const fields = M.editFields(e);
    if (!fields.length || !e.valid) return;
    this.busy.add("edit-save");
    cx.notify();
    const folder = this.currentFolder();
    const space = this.space();
    const gen = this.generation;
    const flag = e.scope === "user" ? "--user" : "--project";
    const plans = [];
    try {
      for (const f of fields) {
        const r = await exec("soho", ["config", "set", f.key, f.value, flag],
          { cwd: folder || undefined, on: space?.machine, timeout: 20000 });
        if (!this.alive(gen, folder)) return;
        if (r.code !== 0) {
          this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
          return;
        }
        plans.push(M.undoPlan(e.previousByKey[f.key] ?? null, f.key, e.scope, this.caps));
      }
      this.errors.config = "";
      const what = e.lane && e.appliesTo === "lane" ? `${e.laneLabel} lane settings` : `${e.role} settings`;
      const undo = plans.every((p) => p.available)
        ? { available: true, plans, key: fields[0].key }
        : null;
      this.edit = null;
      this.showNotice(M.noticeText(what, fields[0].value, fields[0].key, e.file), undo, cx);
      this.refresh(cx);
    } catch (err) {
      if (this.alive(gen, folder)) this.errors.config = firstLines(err?.message ?? err);
    } finally {
      this.busy.delete("edit-save");
      if (this.alive(gen, folder)) cx.notify();
    }
  }

  async undo(cx) {
    if (checking() || !this.undo) return;
    const plans = this.undo.plans ?? [this.undo];
    const key = "undo";
    if (this.busy.has(key)) return;
    this.busy.add(key);
    const folder = this.currentFolder();
    const space = this.space();
    const gen = this.generation;
    try {
      for (const plan of plans) {
        const r = await exec("soho", plan.args, { cwd: folder || undefined, on: space?.machine, timeout: 20000 });
        if (!this.alive(gen, folder)) return;
        if (r.code !== 0) {
          this.errors.config = firstLines(r.stderr || r.stdout || `soho exited ${r.code}`);
          return;
        }
      }
      this.errors.config = "";
      const undone = this.undo.key ?? planKey(plans[0]);
      this.showNotice(`Undid ${undone}`, null, cx);
      this.refresh(cx);
    } catch (e) {
      if (this.alive(gen, folder)) this.errors.config = firstLines(e?.message ?? e);
    } finally {
      this.busy.delete(key);
      if (this.alive(gen, folder)) cx.notify();
    }
  }

  async openConfig(cx) {
    if (checking()) return;
    const path = this.configFile("project");
    if (!path) return;
    try {
      const text = await readTextFile(path, { on: this.space()?.machine });
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
    cx.notify();
  }

  cleanup(cx) {
    this.cleanupConfirm = false;
    this.runAction(cx, "cleanup", ["gc", "--yes"], "health", null, "Orphan copies and processes cleaned up");
  }

  probeAgain(cx) {
    if (!this.caps.assistantsJson) return;
    this.runAction(cx, "probe", ["setup", "--probe"], "assistants", null, "Assistants probed again");
  }

  render(cx) {
    const t = theme();
    const space = this.space();
    const folder = this.currentFolder();
    if (!checking() && this.folderKnown !== null && folder !== this.folderKnown) this.refresh(cx);
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
      .when(Boolean(this.noticeItem), (el) => el.child(notice(this.noticeItem.text, this.undo, (ctx) => this.undo(ctx), t)));
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
      if (!M.teamExists(this.data.team ?? {}, this.data.capabilities ?? this.caps)) return renderEmpty(this, t);
      const list = this.view === "list" ? renderList(this, t, cx) : renderGrouped(this, t, cx);
      if (this.band === "wide" && this.edit) {
        return h_flex().flex_1().min_h(0).gap(10)
          .child(v_flex().w(320).flex_none().min_h(0).child(list))
          .child(v_flex().flex_1().min_w(0).min_h(0).overflow_y_scrollbar().child(renderEdit(this, t, cx)));
      }
      if (this.edit) return renderEdit(this, t, cx);
      return list;
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

function planKey(plan) {
  return plan?.args?.[2] ?? "config change";
}
