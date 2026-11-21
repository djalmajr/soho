// The five views of the SoHo panel. Values come from the controller; these
// functions only describe elements and send clicks back through `app`.
import { div, svg } from "gpui-kit";
import { h_flex, v_flex, Input } from "gpui-base";
import { theme } from "herdr";
import * as M from "./model.js";
import { button, chip, chooser, dot, errorBlock, field, growText, markdown, sectionTitle, segmented, TEXT, trunc } from "./ui.js";

const EFFORTS = ["low", "medium", "high", "xhigh"];
const SORTS = [
  { id: "role", label: "role" },
  { id: "assistant", label: "assistant" },
  { id: "model", label: "model" },
  { id: "effort", label: "effort" },
  { id: "mode", label: "mode" },
];

const busy = (app, key) => app.busy.has(key);
const rowsOf = (app) => Array.isArray(app.data.team?.roles) ? app.data.team.roles : [];
// Handlers take the identity a view displayed when it rendered; the lookup
// happens here, at render time, never when the click arrives (F1, R3-F1).
const originOf = (app, name) => (typeof app.originOf === "function" ? app.originOf(name) : null);

function effortCell(app, row, group, t, cx, origin) {
  const target = M.effortTarget(row, app.groups());
  const id = `effort-${row.role}`;
  const capture = M.effortCapture(origin, row, target);
  // The select keeps its own click: stopping it here keeps the row's Edit from
  // opening when the operator only changes the effort (decision 1).
  return div().id(`effort-stop-${row.role}`).flex_none()
    .on_click((_e, ctx) => ctx.stop_propagation())
    .child(chooser({
      id,
      label: row.effort || "—",
      value: row.effort,
      choices: EFFORTS.map((v) => ({ id: v, label: v })),
      note: target.appliesTo,
      open: app.menuOpen === id,
      focus: app.menuFocus,
      width: 96,
      disabled: busy(app, `effort:${target.key}`),
      onOpenChange: (open, ctx) => app.openMenu(open ? id : "", ctx),
      onChoose: (choice, ctx) => app.setEffort(capture, choice.id, ctx),
    }));
}

// The row is the Edit button: a click anywhere but the effort select opens the
// form for the role named in this row (decision 1).
function roleRow(app, row, group, t, cx, origin, { narrow = false, list = false } = {}) {
  const key = `role-${row.role}`;
  const name = div().id(`${key}-name`).min_w(0).flex_1().truncate()
    .text_size(TEXT.small).text_color(t.foreground).child(row.role);
  const meta = list
    ? div().min_w(0).flex_1().truncate().text_size(TEXT.caption).text_color(t.dim)
      .child(`${row.kind ?? ""} · ${M.shortModel(row.model)}`)
    : div();
  const controls = h_flex().flex_none().gap(6).items_center()
    .child(div().min_w(72).px(4).text_size(TEXT.caption).text_color(t.muted).child(M.modeLabel(row.mode)))
    .child(effortCell(app, row, group, t, cx, origin));
  const capture = M.rowCapture(origin, row);
  const open = (_e, ctx) => app.openEdit(capture, ctx);
  return (narrow
    ? v_flex().id(key).px(8).py(6).gap(5).rounded(6).bg(t.canvas).cursor_pointer()
      .hover((s) => s.bg(t.raised)).on_click(open)
      .child(h_flex().gap(6).items_center().child(name))
      .child(h_flex().gap(6).items_center().child(meta).child(controls))
    : h_flex().id(key).h(list ? 42 : 30).px(7).gap(6).items_center().rounded(6).bg(t.canvas).cursor_pointer()
      .hover((s) => s.bg(t.raised)).on_click(open)
      .child(h_flex().min_w(0).flex_1().gap(6).items_center().child(name).child(meta))
      .child(controls));
}

// A lane header is the lane's Edit button. The "Outside lanes" header names no
// lane, so it stays a header instead of guessing a role (decision 1).
function groupHeader(app, group, t, origin) {
  const editable = Boolean(group.lane);
  const capture = M.laneCapture(origin, group.lane);
  return h_flex().id(`group-${group.lane || "outside"}`).h(26).px(4).gap(8).items_center()
    .when(editable, (el) => el.cursor_pointer().hover((s) => s.bg(t.raised))
      .on_click((_e, ctx) => app.openEdit(capture, ctx)))
    .child(growText(group.label.toUpperCase(), t.muted, TEXT.caption))
    .when(Boolean(group.kind), (el) => el.child(chip(group.kind, t)))
    .when(Boolean(group.model), (el) => el.child(trunc(M.shortModel(group.model), 140, t.dim, TEXT.caption)));
}

export function renderGrouped(app, t, cx) {
  const groups = app.groups();
  if (!groups.length) {
    return div().py(8).text_size(TEXT.small).text_color(t.dim).child("No roles found.");
  }
  const origin = originOf(app, "team");
  const narrow = app.band === "narrow";
  return v_flex().flex_1().min_h(0).gap(8)
    .child(v_flex().flex_1().min_h(0).overflow_y_scrollbar().gap(8)
      .children(groups.map((group) =>
        v_flex().gap(3)
          .child(groupHeader(app, group, t, origin))
          .children(M.sortRoles(group.roles.map((name) => app.roleByName(name)), app.sort)
            .filter(Boolean)
            .map((row) => roleRow(app, row, group, t, cx, origin, { narrow }))))));
}

function sortHeader(app, column, label, width, t) {
  const on = app.sort.column === column;
  return div().id(`sort-${column}`).w(width).flex_none().px(6).cursor_pointer()
    .text_size(TEXT.caption).text_color(on ? t.foreground : t.dim)
    .on_click((_e, cx) => app.setSort(column, cx))
    .child(`${label}${on ? (app.sort.dir === "asc" ? " ↑" : " ↓") : ""}`);
}

// The narrow band also uses the two-line rows and drops the fixed header; its
// sort control lives in the More menu (F15).
export function renderList(app, t, cx) {
  const rows = M.sortRoles(rowsOf(app), app.sort);
  const layout = M.listLayout(app.band);
  const origin = originOf(app, "team");
  if (!rows.length) {
    return div().py(8).text_size(TEXT.small).text_color(t.dim).child("No roles found.");
  }
  return v_flex().flex_1().min_h(0).gap(3)
    .when(layout.showHeader, (el) => el.child(h_flex().h(24).px(7).gap(6).items_center()
      .child(sortHeader(app, "role", "Role", 180, t))
      .child(sortHeader(app, "mode", "Mode", 72, t))
      .child(sortHeader(app, "effort", "Effort", 96, t))))
    .child(v_flex().flex_1().min_h(0).overflow_y_scrollbar().gap(2)
      .children(rows.map((row) => roleRow(app, row, null, t, cx, origin, { list: true, narrow: layout.narrow }))));
}

export function renderEmpty(app, t) {
  return v_flex().flex_1().min_h(0).items_center().justify_center().gap(12).px(16)
    .child(div().w(48).h(48).rounded(10).border_1().border_color(t.border).bg(t.raised))
    .child(div().text_size(TEXT.title).font_semibold().text_color(t.foreground).child("No team in this project"))
    .child(div().text_size(TEXT.small).text_color(t.muted)
      .child("soho is not set up here yet. In the project's orchestrator, run:"))
    .child(div().px(14).py(7).rounded(7).border_1().border_color(t.border).bg(t.input)
      .text_size(TEXT.small).text_color(t.foreground).child("soho init"))
    .child(div().text_size(TEXT.caption).text_color(t.dim).child("The roles show up here once the team exists."));
}

function stats(app, t) {
  const roster = app.data.roster ?? {};
  const s = M.workerStats(roster.agents, roster.settings?.max_workers ?? roster.max_workers);
  return h_flex().gap(8)
    .children(M.statTiles(s).map(([number, label]) =>
      v_flex().id(`stat-${label}`).flex_1().gap(2).px(10).py(6).rounded(7).bg(t.canvas)
        .child(div().text_size(19).font_semibold().text_color(t.foreground).child(number))
        .child(div().text_size(TEXT.caption).text_color(t.dim).child(label))));
}

function statusColor(status, t) {
  if (status === "working") return t.working;
  if (status === "quota" || status === "blocked") return t.blocked;
  if (status === "gone") return t.unknown;
  return t.done;
}

function workerCard(app, worker, t, cx, { wide = false } = {}) {
  const key = M.workerKey(worker);
  const label = M.statusLabel(worker.status, worker.report);
  const time = label === "report in" ? label : (M.durationLabel(worker.task_s ?? worker.activity_s) || label);
  const selected = app.selectedWorker === key;
  const hasTerminal = Boolean(worker.pane);
  const canReport = app.caps.reportJson || Boolean(worker.report_path);
  const task = div().min_w(0).truncate().text_size(TEXT.caption).text_color(t.muted).child(worker.task ?? "");
  const capture = M.workerCapture(originOf(app, "roster"), worker);
  // Close and Reopen use the same worker key the controller marks busy (F5).
  const actions = h_flex().gap(6).items_center()
    .when(hasTerminal, (el) => el.child(button(`term-${key}`, "Terminal", (ctx) => app.peek(capture, ctx), { small: true })))
    .when(canReport, (el) => el.child(button(`report-${key}`, "Report", (ctx) => app.openReport(capture, ctx), { small: true })))
    .when(label === "report in", (el) => el.child(button(`close-${key}`, "Close", (ctx) => app.confirmClose(capture, ctx),
      { small: true, disabled: busy(app, `close:${key}`) })))
    .when(label === "stopped", (el) => el.child(button(`reopen-${key}`, "Reopen", (ctx) => app.confirmReopen(capture, ctx),
      { small: true, disabled: busy(app, `reopen:${key}`) })));
  const name = div().min_w(0).flex_1().truncate().text_size(TEXT.small).font_semibold().text_color(t.foreground).child(worker.name);
  return v_flex().id(`worker-${key}`).gap(6).px(10).py(8).rounded(7)
    .border_1().border_color(selected ? t.accent : t.border).bg(selected ? t.raised : t.canvas)
    .cursor_pointer().hover((s) => s.bg(t.raised))
    .on_click((_e, ctx) => { if (wide && canReport) app.showWorkerReport(capture, ctx); else { app.selectedWorker = key; ctx.notify(); } })
    .child(h_flex().gap(8).items_center().child(dot(statusColor(worker.status, t), t)).child(name)
      .child(div().flex_none().text_size(TEXT.caption).text_color(t.dim).child(time)))
    .child(task)
    .child(h_flex().gap(6).items_center().child(chip(worker.kind, t))
      .child(growText(`${worker.role ?? ""}${worker.model ? ` · ${M.shortModel(worker.model)}` : ""}`, t.dim, TEXT.caption))
      .child(actions));
}

export function renderNow(app, t, cx, { listOnly = false } = {}) {
  const agents = app.data.roster?.agents ?? [];
  const note = app.notes?.roster ?? "";
  const content = v_flex().flex_1().min_h(0).gap(8)
    // The tiles need a roster answer; a neutral note replaces them (decision 3).
    .when(!listOnly && !note, (el) => el.child(stats(app, t)))
    .when(Boolean(note), (el) => el.child(div().id("roster-note").py(10)
      .text_size(TEXT.small).text_color(t.muted).child(note)))
    .child(errorBlock(app.errors.roster, t))
    .when(!note && agents.length === 0, (el) => el.child(div().py(12).text_size(TEXT.small).text_color(t.dim)
      .child(app.firstReadDone ? "No workers in this project." : "Reading the team…")))
    .child(v_flex().flex_1().min_h(0).overflow_y_scrollbar().gap(8)
      .children(agents.map((w) => workerCard(app, w, t, cx, { wide: listOnly }))));
  if (!listOnly) return content;
  return content;
}

function reportItem(item, t) {
  const bad = item.mark === "partial" || item.mark === "env";
  return h_flex().id(`item-${item.line}`).gap(8).px(8).py(5).rounded(6).bg(t.canvas)
    .items_start()
    .child(div().w(58).flex_none().text_size(TEXT.caption).text_color(bad ? t.ansi3 : t.done).child(item.mark))
    .child(growText(item.text ?? "", t.muted, TEXT.caption));
}

function layerRow(layer, t) {
  const ok = layer.result === "PASS";
  return h_flex().gap(8).px(8).py(4).rounded(6).bg(t.canvas).items_center()
    .child(growText(layer.name ?? "", t.foreground, TEXT.caption))
    .child(div().flex_none().text_size(TEXT.caption).text_color(ok ? t.done : t.ansi3).child(M.layerResult(layer)));
}

export function renderReport(app, t, cx) {
  const r = app.data.report;
  if (app.data.reportMarkdown != null) {
    return v_flex().flex_1().min_h(0).gap(8)
      .child(errorBlock(app.errors.report, t))
      .child(markdown(`report-file-${app.reportAgent ?? "agent"}`, app.data.reportMarkdown));
  }
  if (!r) {
    return v_flex().flex_1().gap(8).child(errorBlock(app.errors.report || "No report selected.", t));
  }
  const counts = r.counts ?? {};
  const bad = (counts.partial ?? 0) + (counts.env ?? 0);
  const failed = (r.test_layers?.layers ?? []).filter((l) => l.result !== "PASS" && l.result !== "NA");
  const responses = r.review_responses?.fixed ?? [];
  const worker = app.reportWorker();
  const workerCapture = worker ? M.workerCapture(originOf(app, "roster"), worker) : null;
  return v_flex().flex_1().min_h(0).gap(6)
    .child(errorBlock(app.errors.report, t))
    .when(bad > 0, (el) => el.child(div().px(9).py(6).rounded(6).border_1().border_color(t.border)
      .bg(t.raised).text_size(TEXT.caption).text_color(t.ansi3)
      .child(`${bad} items partial · not a pass: check them before push`)))
    .child(sectionTitle("Items", t))
    .child(v_flex().flex_1().min_h(0).overflow_y_scrollbar().gap(2)
      .children([
        ...M.sortedReportItems(r.items).map((item) => reportItem(item, t)),
        ...(failed.length ? [sectionTitle("Test layers", t), ...M.sortedLayers(failed).map((l) => layerRow(l, t))] : []),
        ...(responses.length ? [sectionTitle("Review responses", t), ...responses.map((n) =>
          reportItem({ line: n, mark: "fixed", text: `F${n} · fixed` }, t))] : []),
        ...((r.warnings ?? []).length ? [sectionTitle("Warnings", t), ...r.warnings.map((w) =>
          reportItem({ line: "", mark: w.kind, text: w.message }, t))] : []),
      ]))
    .child(h_flex().gap(6).justify_end()
      .when(Boolean(worker?.pane), (el) => el.child(button("report-terminal", "Terminal", (cx) => app.peek(workerCapture, cx), { small: true })))
      .when(Boolean(worker?.pane), (el) => el.child(button("report-reopen", "Reopen", (cx) => app.confirmReopen(workerCapture, cx),
        { small: true, disabled: busy(app, `reopen:${M.workerKey(worker)}`) })))
      .when(Boolean(worker), (el) => el.child(button("report-close", "Close", (cx) => app.confirmClose(workerCapture, cx),
        { small: true, primary: true, disabled: busy(app, `close:${M.workerKey(worker)}`) }))));
}

function probeLabel(probe) {
  if (!probe) return "not probed";
  if (probe.status === "ready") return "answering";
  if (probe.status === "no-auth") return "needs auth";
  if (probe.status === "quota") return "out of quota";
  return probe.status;
}

function assistantCard(app, a, t) {
  const status = probeLabel(a.probe);
  const color = status === "answering" ? t.working : status === "out of quota" ? t.blocked : t.unknown;
  const roleNames = a.roles ?? [];
  // A model lookup that failed for this kind stays beside this kind (F9).
  const kindError = app.errors?.[M.modelsErrorKey(a.kind)] ?? "";
  return v_flex().id(`assistant-${a.kind}`).gap(6).px(10).py(8).rounded(7).border_1().border_color(t.border).bg(t.canvas)
    .child(h_flex().gap(8).items_center().child(dot(color, t))
      .child(div().flex_1().truncate().text_size(TEXT.small).font_semibold().text_color(t.foreground).child(a.kind))
      .child(div().flex_none().text_size(TEXT.caption).text_color(t.dim).child(status)))
    .when(Boolean(kindError), (el) => el.child(errorBlock(kindError, t)))
    .when(Boolean(a.family || a.models?.length), (el) => el.child(growText(
      [a.family, ...(a.models ?? []).slice(0, 3)].filter(Boolean).join(" · "), t.dim, TEXT.caption)))
    .when(roleNames.length > 0, (el) => el.child(h_flex().gap(5).items_center()
      .child(chip(M.laneLabel(roleNames[0].lane) || "Roles", t))
      .child(growText(roleNames.map((r) => r.role).join(", "), t.dim, TEXT.caption))));
}

export function renderAssistants(app, t) {
  const assistants = app.assistantCards();
  return v_flex().flex_1().min_h(0).gap(8)
    .child(errorBlock(app.errors.assistants, t))
    // Until this context's capability answer arrives the flag is unknown, not
    // absent, so the legacy note waits for the reply (F2, R3-F2).
    .when(!app.caps.assistantsJson && app.firstReadDone, (el) => el.child(div().text_size(TEXT.caption).text_color(t.dim)
      .child("This soho version has no assistants --json; showing soho kinds.")))
    .when(assistants.length === 0, (el) => el.child(div().py(10).text_size(TEXT.small).text_color(t.dim)
      .child(app.firstReadDone ? "No assistants found." : "Reading the team…")))
    .child(v_flex().flex_1().min_h(0).overflow_y_scrollbar().gap(8)
      .children(assistants.map((a) => assistantCard(app, a, t))));
}

function diskRow(name, detail, size, t) {
  return h_flex().gap(8).h(34).px(8).items_center().rounded(6).bg(t.canvas)
    .child(growText(`${name} · ${detail}`, t.muted, TEXT.caption))
    .child(div().flex_none().text_size(TEXT.caption).text_color(t.dim).child(size));
}

function bytes(n) {
  if (!Number.isFinite(Number(n)) || n <= 0) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let value = Number(n);
  let i = 0;
  while (value >= 1024 && i < units.length - 1) { value /= 1024; i += 1; }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[i]}`;
}

export function renderHealth(app, t, cx) {
  const h = app.data.health;
  if (!h) {
    // A missing health answer is only an error when the command failed (decision 4).
    return v_flex().flex_1().gap(8)
      .child(errorBlock(app.errors.health, t))
      .when(!app.errors.health, (el) => el.child(div().py(8).text_size(TEXT.small).text_color(t.dim)
        .child(app.firstReadDone ? "Reading health…" : "Reading the team…")));
  }
  const doctor = h.doctor ?? {};
  const copies = h.copies ?? {};
  const procs = h.processes ?? {};
  const gc = h.gc ?? {};
  const update = h.update ?? {};
  const removing = M.gcCandidates(gc);
  const confirming = app.cleanupConfirm;
  // The opener carries the Health origin this render read, and the dialog's own
  // token, so an older button arms nothing and an older Cancel dismisses only
  // the dialog it rendered (R4-F2).
  const healthOrigin = originOf(app, "health");
  const cleanupCapture = { token: confirming ? confirming.token : "", origin: healthOrigin };
  return v_flex().flex_1().min_h(0).gap(6)
    .child(errorBlock(app.errors.health, t))
    .child(sectionTitle("Update", t))
    .child(div().px(9).py(7).rounded(6).border_1().border_color(t.border).bg(t.canvas)
      .text_size(TEXT.caption).text_color(t.muted)
      .child(update.latest ? `${update.latest} is out; you run ${update.current ?? "an unknown version"}.`
        : (update.reason || "No update check available.")))
    .child(sectionTitle(`Doctor · ${doctor.warnings?.length ?? 0} warning(s)`, t))
    .child(v_flex().gap(2).children((doctor.warnings ?? []).map((w) =>
      h_flex().gap(8).px(8).py(5).rounded(6).bg(t.canvas)
        .child(div().flex_none().text_size(TEXT.caption).text_color(t.ansi3).child(w.level ?? "warn"))
        .child(growText(w.message ?? "", t.muted, TEXT.caption)))))
    .child(sectionTitle(`Copies · ${copies.total ?? (copies.rows ?? []).length} · ${bytes(copies.total_bytes)}`, t))
    .child(v_flex().gap(2).children((copies.rows ?? []).map((row) =>
      diskRow(row.path?.split("/").pop() ?? "", `${row.owner ?? "?"} · ${row.state ?? "?"}`, bytes(row.bytes), t))))
    .child(sectionTitle(`Processes · ${(procs.unregistered_orphans ?? []).length} left behind`, t))
    .child(v_flex().gap(2).children((procs.unregistered_orphans ?? []).map((p) =>
      diskRow(String(p.pid), `${p.name ?? ""} · ${p.cwd ?? ""} · ${p.state ?? ""}`, "orphan", t))))
    .child(sectionTitle("Clean up", t))
    .child(div().px(8).py(5).text_size(TEXT.caption).text_color(t.dim)
      .child(removing.length ? `gc would remove ${removing.length} item(s) (${bytes(gc.total_bytes)}).`
        : "Nothing for gc to remove."))
    // The confirmation lists every candidate it would delete (F16).
    .when(confirming, (el) => el.child(v_flex().gap(2).children(removing.map((row) =>
      diskRow(row.path, row.reason || "no reason reported", bytes(row.size), t)))))
    .child(h_flex().justify_end().gap(6)
      .when(!confirming, (el) => el.child(button("cleanup-ask", "Clean up", (ctx) => app.setCleanupConfirm(true, { origin: healthOrigin }, ctx),
        { small: true, disabled: removing.length === 0 })))
      .when(confirming, (el) => el.child(div().text_size(TEXT.caption).text_color(t.ansi3).child("Remove these copies and stop orphan processes?")))
      .when(confirming, (el) => el.child(button("cleanup-no", "Cancel", (ctx) => app.setCleanupConfirm(false, cleanupCapture, ctx), { small: true })))
      .when(confirming, (el) => el.child(button("cleanup-yes", "Clean up", (ctx) => app.cleanup(cleanupCapture, ctx),
        { small: true, primary: true, disabled: busy(app, "cleanup") }))));
}

export function renderEdit(app, t, cx) {
  const e = app.edit;
  if (!e) return div();
  const preview = M.previewState(e);
  const scopeChoices = [{ id: "project", label: "Project file" }, { id: "user", label: "User file" }];
  const applies = [{ id: "lane", label: e.laneLabel ? `${e.laneLabel} lane · ${e.roleCount} roles` : "this lane" },
    { id: "role", label: e.role ? `${e.role} only` : "this role only" }];
  const assistants = app.editAssistants();
  const choices = assistants.map((a) => ({ id: a.kind, label: a.kind }));
  const models = assistants.find((a) => a.kind === e.kind)?.models ?? [];
  const modelChoices = models.map((m) => ({ id: m, label: m }));
  const fileHelp = e.scope === "project" ? ".agents/soho.conf · applies to new workers; open ones keep their assistant." :
    "User config · applies to new workers; open ones keep their assistant.";
  // A model lookup that failed for the chosen kind shows in this form (F9);
  // Probe again exists only where the controller can run it (F11).
  const canProbe = Boolean(app.caps?.assistantsJson);
  const kindError = e.kind ? (app.errors?.[M.modelsErrorKey(e.kind)] ?? "") : "";
  // A probe failure belongs to this draft's own slot: a dismissed or replaced
  // draft never shows another draft's failure (F5, R3-F5).
  const probeError = app.errors?.[M.probeEditKey(e.id)] ?? "";
  const probeSource = { kind: "edit", draftId: e.id, origin: e.origin };
  const assistantChildren = assistants.length
    ? [chooser({ id: "edit-kind", label: e.kind || "choose", value: e.kind, choices,
      open: app.menuOpen === "edit-kind", focus: app.menuFocus, width: 240, small: false,
      onOpenChange: (open, ctx) => app.openMenu(open ? "edit-kind" : "", ctx),
      onChoose: (choice, ctx) => app.setEditField("kind", choice.id, e.id, ctx) }),
    ...(kindError ? [errorBlock(kindError, t)] : [])]
    : [h_flex().gap(8).items_center()
      .child(div().text_size(TEXT.caption).text_color(t.muted)
        .child(canProbe ? "No assistant has answered yet" : "No assistant installed"))
      .when(canProbe, (el) => el.child(button("edit-probe", "Probe again", (ctx) => app.probeAgain(probeSource, ctx),
        { small: true, disabled: busy(app, "probe") })))];
  const fields = M.editFields(e);
  // Save keeps the draft and the validated snapshot this render displayed, so a
  // newer preview for the same draft is never written by an older button
  // (R4-F1).
  const saveDraftId = e.id;
  const saveSnapshot = e.snapshot;
  return v_flex().flex_1().min_h(0).gap(12)
    .child(field("Applies to", "", segmented("edit-applies", applies, e.appliesTo,
      (id, ctx) => app.setEditAppliesTo(id, e.id, ctx), t)))
    .child(field("Assistant", assistants.length
      ? "Only assistants installed and answering are listed."
      : "", ...assistantChildren))
    .when(Boolean(probeError), (el) => el.child(errorBlock(probeError, t)))
    .child(field("Model", "", modelChoices.length
      ? chooser({ id: "edit-model", label: e.model || "choose", value: e.model, choices: modelChoices,
        open: app.menuOpen === "edit-model", focus: app.menuFocus, width: 240, small: false,
        onOpenChange: (open, ctx) => app.openMenu(open ? "edit-model" : "", ctx),
        onChoose: (choice, ctx) => app.setEditField("model", choice.id, e.id, ctx) })
      : h_flex().id("edit-model-input").h(32).px(10).rounded(7).border_1().border_color(t.input).bg(t.input)
        .child(Input.new(app.modelInput).h_full().text_size(TEXT.small).text_color(t.foreground))))
    .child(field("Effort", "", segmented("edit-effort", EFFORTS.map((v) => ({ id: v, label: v })), e.effort,
      (id, ctx) => app.setEditField("effort", id, e.id, ctx), t)))
    .child(field("Save to", fileHelp, segmented("edit-scope", scopeChoices, e.scope,
      (id, ctx) => app.setEditField("scope", id, e.id, ctx), t)))
    .child(sectionTitle("Commands", t))
    .child(v_flex().gap(3).px(10).py(8).rounded(7).border_1().border_color(t.border).bg(t.background)
      .children(e.preview.length ? e.preview.map((line) =>
        div().min_w(0).truncate().text_size(TEXT.caption).text_color(t.muted).child(line))
        : [div().text_size(TEXT.caption).text_color(t.dim).child("Choose a value to preview the config commands.")])
      .when(preview.stale, (el) => el.child(div().text_size(TEXT.caption).text_color(t.ansi3)
        .child("This preview is stale: it belongs to the previous draft. Save waits for the new one."))))
    .when(Boolean(e.error), (el) => el.child(errorBlock(e.error, t)))
    .child(h_flex().gap(8).justify_end()
      .child(button("edit-cancel", "Cancel", (ctx) => app.closeEdit(e.id, ctx), { small: true }))
      // A draft with no changed fields has no snapshot, so it offers no Save (F6).
      .when(fields.length > 0, (el) => el.child(button("edit-save", "Save", (ctx) => app.saveEdit(ctx, saveDraftId, saveSnapshot),
        { small: true, primary: true, disabled: busy(app, "edit-save") || !preview.canSave }))));
}

// Equal geometry in every state: selection changes only bg and text color, so
// no label moves (decision 2).
export function toolTabs(app, t, cx) {
  const pad = app.band === "narrow" ? 8 : 12;
  return h_flex().gap(3).p(3).rounded(7).bg(t.background).items_center()
    .children(M.tabsFor(app.caps).map((item) =>
      h_flex().id(`tab-${item.id}`).h(24).flex_none().px(pad).items_center().justify_center().rounded(5)
        .bg(app.tab === item.id ? t.raised : t.background)
        .text_color(app.tab === item.id ? t.foreground : t.muted)
        .text_size(TEXT.caption).cursor_pointer().hover((s) => s.text_color(t.foreground))
        .on_click((_e, ctx) => app.setTab(item.id, ctx))
        .child(item.label)));
}

export function sortMenu(app, t, cx) {
  const id = "sort-menu";
  const current = SORTS.find((s) => s.id === app.sort.column)?.label ?? "role";
  return chooser({ id, label: `Sort: ${current}, ${app.sort.dir === "asc" ? "A–Z" : "Z–A"}`,
    value: app.sort.column, choices: SORTS, open: app.menuOpen === id, focus: app.menuFocus, width: 170,
    onOpenChange: (open, ctx) => app.openMenu(open ? id : "", ctx),
    onChoose: (choice, ctx) => app.setSort(choice.id, ctx) });
}

export function moreMenu(app, t, cx) {
  const id = "more-menu";
  const choices = [
    { id: "grouped", label: "Grouped view" },
    { id: "list", label: "List view" },
  ];
  // The narrow band has no header row, so sorting moves here (F15).
  if (app.tab === "team") {
    for (const column of SORTS) {
      const on = app.sort.column === column.id;
      choices.push({ id: `sort:${column.id}`,
        label: `Sort by ${column.label}${on ? (app.sort.dir === "asc" ? " ↑" : " ↓") : ""}` });
    }
  }
  return chooser({ id, label: "More", value: "", choices, open: app.menuOpen === id, focus: app.moreFocus, width: 170,
    onOpenChange: (open, ctx) => app.openMenu(open ? id : "", ctx),
    onChoose: (choice, ctx) => app.moreAction(choice.id, ctx) });
}
