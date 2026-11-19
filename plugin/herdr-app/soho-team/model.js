// Pure panel logic: no gpui-kit or herdr imports, so node --test can run it.
// Every command name and JSON field here comes from the panel brief's data
// contracts; this module never runs a command.

export const CAPABILITIES = [
  "team_json",
  "config_dry_run",
  "config_unset",
  "roster_workers",
  "report_json",
  "assistants_json",
  "health_json",
];

const LANE_LABELS = { build: "Build", review: "Review", critical: "Risky code", docs: "Docs" };
const SORT_FIELDS = {
  role: "role",
  assistant: "kind",
  kind: "kind",
  model: "model",
  effort: "effort",
  mode: "mode",
};

// Accepts the wire payload (`config_unset`) and the controller's own camel
// object (`configUnset`), so a capability handed back to a helper keeps its
// meaning instead of reading as absent (F3).
export function capabilities(payload) {
  const out = {};
  for (const key of CAPABILITIES) {
    const camel = key.replace(/_([a-z])/g, (_m, c) => c.toUpperCase());
    out[camel] = Boolean(payload && typeof payload === "object"
      && (Object.hasOwn(payload, key) || payload[camel] === true));
  }
  return out;
}

// A capabilities response that is not an object is a failed discovery, not a
// version without features (F12).
export function capabilityAnswer(payload) {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
    return { caps: null, error: "soho capabilities --json did not return an object" };
  }
  return { caps: payload, error: "" };
}

// Which independent reads a capability answer allows (F12).
export function sectionReads(caps) {
  const support = capabilities(caps);
  return {
    assistantsJson: support.assistantsJson,
    legacyKinds: !support.assistantsJson,
    health: support.healthJson,
  };
}

export const CONTEXT_CHANGED_MESSAGE =
  "The project, machine or folder changed; the pending change was discarded. Open it again from this project.";

// The identity every displayed dataset and pending action carries (F1).
export function contextFor(space, folder) {
  return { space: space?.id ?? "", machine: space?.machine ?? null, folder: folder ?? "" };
}

export function contextKey(context) {
  if (!context) return "";
  return `${context.space ?? ""}|${context.machine ?? ""}|${context.folder ?? ""}`;
}

export function sameContext(a, b) {
  return Boolean(a) && Boolean(b) && contextKey(a) === contextKey(b);
}

// A write is refused when its own context is missing or no longer the shown
// one. No read sequence enters this decision, so a refresh never abandons a
// write in flight (F1, F2).
export function writeGuard(actionContext, current) {
  if (!actionContext || !sameContext(actionContext, current)) {
    return { ok: false, message: CONTEXT_CHANGED_MESSAGE };
  }
  return { ok: true, message: "" };
}

export function decideWrite({ actionContext, currentContext, busy }) {
  if (busy) return { run: false, message: "" };
  const guard = writeGuard(actionContext, currentContext);
  return { run: guard.ok, message: guard.message };
}

// A multi-command action that fails part way says what it already wrote (F2).
export function partialFailureMessage(applied, remaining, detail) {
  const done = applied?.length ? `Applied ${applied.join(", ")}` : "Nothing was applied";
  const left = remaining?.length ? `not applied ${remaining.join(", ")}` : "nothing left to apply";
  const why = String(detail ?? "").trim();
  return `${done}; ${left}.${why ? ` ${why}` : ""}`;
}

export function laneLabel(lane) {
  if (!lane) return "Outside lanes";
  return LANE_LABELS[lane] ?? lane.charAt(0).toUpperCase() + lane.slice(1);
}

export function modeLabel(mode) {
  return mode === "edit" ? "edits" : mode === "read-only" ? "read-only" : (mode || "read-only");
}

export function shortModel(model) {
  if (!model) return "";
  return String(model).split("/").pop();
}

const unique = (values) => [...new Set(values.filter((v) => typeof v === "string" && v !== ""))];

function firstValue(roles, field) {
  for (const role of roles) if (role && role[field]) return role[field];
  return "";
}

// Groups by the role rows' own `lane` field (or a lane record's explicit role
// names), never by list position. Old soho has no `lanes`: group by roles[].lane.
export function groupRoles(payload, caps) {
  const support = capabilities(caps);
  const roles = Array.isArray(payload?.roles) ? payload.roles.filter((r) => r && r.role) : [];
  const byName = new Map(roles.map((r) => [r.role, r]));
  const laneRows = support.teamJson && Array.isArray(payload?.lanes) ? payload.lanes : [];
  const groups = [];

  for (const lane of laneRows) {
    const name = lane?.name ?? "";
    if (!name) continue;
    const names = unique(Array.isArray(lane.roles) ? lane.roles : []);
    const members = names.map((n) => byName.get(n)).filter(Boolean);
    for (const role of roles) if (role.lane === name && !names.includes(role.role)) members.push(role);
    groups.push({
      lane: name,
      label: laneLabel(name),
      kind: lane.kind || firstValue(members, "kind"),
      model: lane.model || firstValue(members, "model"),
      effort: lane.effort || firstValue(members, "effort"),
      roles: unique(members.map((r) => r.role)),
    });
  }

  for (const role of roles) {
    if (!role.lane || groups.some((g) => g.lane === role.lane)) continue;
    const members = roles.filter((r) => r.lane === role.lane);
    groups.push({
      lane: role.lane,
      label: laneLabel(role.lane),
      kind: firstValue(members, "kind"),
      model: firstValue(members, "model"),
      effort: firstValue(members, "effort"),
      roles: unique(members.map((r) => r.role)),
    });
  }

  const named = support.teamJson && Array.isArray(payload?.unassigned_roles)
    ? payload.unassigned_roles
    : [];
  const outside = unique([...named, ...roles.filter((r) => !r.lane).map((r) => r.role)]);
  if (outside.length) {
    const members = outside.map((n) => byName.get(n)).filter(Boolean);
    groups.push({
      lane: "",
      label: laneLabel(""),
      kind: firstValue(members, "kind"),
      model: firstValue(members, "model"),
      effort: firstValue(members, "effort"),
      roles: outside,
    });
  }
  return groups;
}

export function teamExists(payload, caps) {
  const support = capabilities(caps);
  if (!support.teamJson) return true;
  return payload?.team_exists !== false;
}

export function toggleSort(current, column) {
  if (!SORT_FIELDS[column]) return current;
  if (current?.column === column) return { column, dir: current.dir === "asc" ? "desc" : "asc" };
  return { column, dir: "asc" };
}

// The view and sort a folder has stored, or the defaults when it has no entry
// (F13).
export function viewStateFor(prefs, folder, defaults = { view: "grouped", sort: { column: "role", dir: "asc" } }) {
  const saved = prefsRead(prefs, folder);
  const view = saved.view === "list" ? "list" : saved.view === "grouped" ? "grouped" : defaults.view;
  const sort = saved.sort && typeof saved.sort === "object" && SORT_FIELDS[saved.sort.column]
    ? { column: saved.sort.column, dir: saved.sort.dir === "desc" ? "desc" : "asc" }
    : defaults.sort;
  return { view, sort };
}

// The narrow band also uses the two-line rows and drops the fixed header; its
// sort control is the More menu (F15).
export function listLayout(band) {
  const narrow = band === "narrow";
  return { narrow, showHeader: !narrow, sortControl: narrow ? "menu" : "header" };
}

export function sortRoles(rows, sort) {
  const field = SORT_FIELDS[sort?.column] ?? "role";
  const dir = sort?.dir === "desc" ? -1 : 1;
  const text = (row) => String(row?.[field] ?? "").toLocaleLowerCase();
  return [...rows].sort((a, b) => {
    const left = text(a);
    const right = text(b);
    if (left < right) return -1 * dir;
    if (left > right) return 1 * dir;
    return String(a?.role ?? "").localeCompare(String(b?.role ?? "")) * dir;
  });
}

export function bandForWidth(width) {
  if (typeof width !== "number" || width <= 320) return "narrow";
  if (width < 560) return "medium";
  return "wide";
}

export function effortTarget(role, groups) {
  const lane = role?.lane || "";
  if (lane) {
    const group = (groups ?? []).find((g) => g.lane === lane);
    const count = group?.roles?.length ?? 0;
    return {
      key: `lane.${lane}.effort`,
      appliesTo: `Applies to the ${laneLabel(lane)} lane: ${count} roles`,
      lane,
      label: laneLabel(lane),
      role: "",
    };
  }
  return {
    key: `role.${role?.role ?? ""}.effort`,
    appliesTo: "Applies to this role only",
    lane: "",
    label: role?.role ?? "",
    role: role?.role ?? "",
  };
}

// One literal operand, `<key>=<value>`, which the CLI already parses
// (internal/core/config.go:1232-1238): a value that starts with "-" or holds
// spaces cannot become a flag or move the destination (F8).
export function configSetArgv(key, value, scope) {
  return ["config", "set", `${key}=${value}`, scope === "user" ? "--user" : "--project"];
}

const SAFE_WORD = /^[A-Za-z0-9_@%+=:,./-]+$/;

// POSIX single-quote quoting for a preview line, so the shown boundaries are
// the real argv boundaries (F8).
export function shellQuote(value) {
  const text = String(value ?? "");
  if (text !== "" && SAFE_WORD.test(text)) return text;
  return `'${text.replace(/'/g, `'\\''`)}'`;
}

export function configSetCommand(key, value, scope) {
  const flag = scope === "user" ? "--user" : "--project";
  return `soho config set ${key}=${shellQuote(value)} ${flag}`;
}

export function editFields({ appliesTo, lane, role, kind, model, effort }) {
  const prefix = appliesTo === "lane" && lane ? `lane.${lane}` : `role.${role}`;
  const fields = [];
  if (kind) fields.push({ key: `${prefix}.kind`, value: String(kind) });
  if (model) fields.push({ key: `${prefix}.model`, value: String(model) });
  if (effort) fields.push({ key: `${prefix}.effort`, value: String(effort) });
  return fields;
}

// Switching Applies to keeps the role that was opened, not the lane's first
// member (F5).
export function roleForScope(selectedRole, group) {
  const members = Array.isArray(group?.roles) ? group.roles : [];
  if (selectedRole && members.includes(selectedRole)) return selectedRole;
  return members[0] ?? selectedRole ?? "";
}

// A role-only save needs a named role; a lane-header edit has to identify its
// target first (F5).
export function editProblem(edit) {
  if (!edit) return "No edit is open.";
  if (edit.appliesTo === "role" && !edit.role) {
    return "Choose which role this edit changes before saving to one role.";
  }
  if (edit.appliesTo === "lane" && !edit.lane) return "No lane is selected for this edit.";
  return "";
}

// The model input's change event feeds the draft and its preview (F6).
export function draftWithModel(edit, value) {
  if (!edit) return null;
  return { ...edit, model: String(value ?? "") };
}

// One immutable validated snapshot: what Save runs, with the values Undo
// restores and the destination it was previewed for (F7).
export function snapshotFor(edit, preview, previousByKey, file, context) {
  return {
    scope: edit.scope,
    appliesTo: edit.appliesTo,
    lane: edit.lane,
    role: edit.role,
    fields: editFields(edit).map((field) => ({ ...field })),
    commands: [...(preview?.commands ?? [])],
    previousByKey: { ...(previousByKey ?? {}) },
    file: file ?? "",
    context: context ? { ...context } : null,
  };
}

// Save stays disabled while a preview for the changed draft is pending, and
// the commands still on screen are marked stale (F7).
export function previewState(edit) {
  if (!edit) return { canSave: false, stale: false };
  const stale = Boolean(edit.pending) && Boolean(edit.snapshot);
  const canSave = !edit.pending && Boolean(edit.snapshot)
    && (edit.snapshot.fields?.length ?? 0) > 0 && edit.valid !== false;
  return { canSave, stale };
}

export function snapshotMatches(snapshot, edit) {
  if (!snapshot || !edit) return false;
  if (snapshot.scope !== edit.scope) return false;
  const fields = editFields(edit);
  if (snapshot.fields.length !== fields.length) return false;
  return fields.every((field, i) => snapshot.fields[i].key === field.key
    && snapshot.fields[i].value === field.value);
}

export function previewCommands(fields, scope) {
  return fields.map((f) => configSetCommand(f.key, f.value, scope));
}

// The panel composes every preview line itself, in the same argv shape a save
// runs, so a dry run only decides validity (F8). A valid:false result is not a
// preview.
export function previewFromDryRuns(results, fields, scope) {
  const commands = previewCommands(fields, scope);
  if (!Array.isArray(results) || results.length !== fields.length) {
    return { valid: true, error: "", commands };
  }
  const bad = results.find((r) => r && r.valid === false);
  if (bad) return { valid: false, error: String(bad.error ?? "invalid value"), commands: [] };
  return { valid: true, error: "", commands };
}

export function undoPlan(previous, key, scope, caps) {
  const support = capabilities(caps);
  const flag = scope === "user" ? "--user" : "--project";
  if (!previous || !Object.hasOwn(previous, "in_file")) {
    return { available: false, args: [], reason: "no previous value" };
  }
  if (previous.in_file === null) {
    if (!support.configUnset) {
      return { available: false, args: [], reason: "soho has no config unset" };
    }
    return { available: true, args: ["config", "unset", key, flag] };
  }
  return { available: true, args: configSetArgv(key, String(previous.in_file), scope) };
}

export function displayFile(file) {
  if (!file) return "";
  const index = String(file).lastIndexOf("/.agents/");
  if (index >= 0) return `.agents/${String(file).slice(index + 9)}`;
  return String(file).split("/").pop();
}

export function noticeText(what, value, key, file) {
  const where = displayFile(file);
  return `${what} is now ${value} · ${key}${where ? ` in ${where}` : ""}`;
}

export function statusLabel(status, report) {
  const reportState = typeof report === "string" ? report : report?.report_state ?? report?.state;
  if (status === "quota") return "out of quota";
  if (status === "gone") return "stopped";
  if (status === "done" && reportState === "ready") return "report in";
  if (status === "done") return "done";
  if (status === "working" || status === "idle" || status === "blocked") return status;
  return status || "unknown";
}

// A worker is its own roster row: its name plus Core terminal address.
export function workerKey(worker) {
  return `${worker?.name ?? ""}|${worker?.pane ?? ""}`;
}

export function findWorker(workers, key) {
  return (Array.isArray(workers) ? workers : []).find((w) => workerKey(w) === key) ?? null;
}

export function durationLabel(seconds) {
  const n = Number(seconds);
  if (!Number.isFinite(n) || n <= 0) return "";
  if (n < 60) return `${Math.round(n)} s`;
  return `${Math.round(n / 60)} min`;
}

export function workerStats(workers, max) {
  const rows = Array.isArray(workers) ? workers : [];
  return {
    working: rows.filter((w) => w?.status === "working").length,
    reportIn: rows.filter((w) => statusLabel(w?.status, w?.report) === "report in").length,
    max: Number(max) || 0,
  };
}

// A roster refusal is a neutral state, not an error: the panel runs outside
// the Herdr terminal the command needs today.
export const ROSTER_REFUSAL = "This soho cannot list workers from the panel yet.";

const ROSTER_REFUSAL_MARKERS = [
  "not running inside Herdr (HERDR_ENV != 1)",
  "not inside an hc terminal (HC_TERMINAL_ID is not set)",
];

export function rosterRefusal(stderr) {
  const text = String(stderr ?? "");
  return ROSTER_REFUSAL_MARKERS.some((marker) => text.includes(marker)) ? ROSTER_REFUSAL : "";
}

// The Now tiles: max is only a fact when it is a positive number.
export function statTiles(stats) {
  const rows = [
    [String(stats?.working ?? 0), "working"],
    [String(stats?.reportIn ?? 0), "report in"],
  ];
  const max = Number(stats?.max);
  if (Number.isFinite(max) && max > 0) rows.push([String(max), "max"]);
  return rows;
}

// Tabs follow the capabilities: a missing feature is hidden, never an error.
export function tabsFor(caps) {
  const support = capabilities(caps);
  const tabs = [
    { id: "team", label: "Team" },
    { id: "now", label: "Now" },
    { id: "assistants", label: "Assistants" },
  ];
  if (support.healthJson) tabs.push({ id: "health", label: "Health" });
  return tabs;
}

// A selected tab that lost its capability falls back to Team before the next
// render, so the hidden view never paints.
export function resolveTab(tab, caps) {
  return tabsFor(caps).some((entry) => entry.id === tab) ? tab : "team";
}

export function sortedReportItems(items) {
  const rows = Array.isArray(items) ? items : [];
  const weight = (item) => (item?.mark === "partial" || item?.mark === "env" ? 0 : 1);
  return [...rows].sort((a, b) => weight(a) - weight(b) || (a?.line ?? 0) - (b?.line ?? 0));
}

export function sortedLayers(layers) {
  const rows = Array.isArray(layers) ? layers : [];
  const weight = (layer) => (layer?.result === "PASS" || layer?.result === "NA" ? 1 : 0);
  return [...rows].sort((a, b) => weight(a) - weight(b));
}

export function layerResult(layer) {
  const result = layer?.result ?? "UNMARKED";
  return layer?.reason ? `${result} · ${layer.reason}` : result;
}

// Herdr delivers only the suffix of "<mod>.<command>" to nextCommand(); the
// prefixed spelling is accepted too (F4).
export function parseCommand(command) {
  if (typeof command !== "string" || !command) return null;
  const name = command.startsWith("soho-team.") ? command.slice("soho-team.".length) : command;
  if (!name || name.includes(".")) return null;
  return { name };
}

// A confirmation runs only for the pending action and the worker row it was
// opened on, matched by name and Core address, never by position (F4).
export function confirmationMatches(pending, command, workers) {
  const parsed = parseCommand(command);
  if (!pending || !parsed) return { ok: false, worker: null, reason: "no pending confirmation" };
  if (parsed.name !== pending.action) {
    return { ok: false, worker: null, reason: `no pending ${parsed.name} confirmation` };
  }
  const worker = findWorker(workers, pending.key);
  if (!worker || worker.name !== pending.name) {
    return { ok: false, worker: null, reason: "that worker row changed since the confirmation opened" };
  }
  return { ok: true, worker, reason: "" };
}

export function planKey(plan) {
  return plan?.args?.[2] ?? "config change";
}

// A report result belongs to the worker it was requested for (F9).
export function acceptsReport(selectionKey, requestedKey) {
  return Boolean(selectionKey) && Boolean(requestedKey) && selectionKey === requestedKey;
}

export function reportBusyKey(folder, key) {
  return `report|${folder}|${key}`;
}

// Legacy roster rows carry `state`, not `status` (internal/cli/roster.go:238-260).
export function normalizeWorker(worker) {
  if (!worker || typeof worker !== "object") return worker;
  if (worker.status != null && worker.status !== "") return worker;
  if (worker.state == null) return worker;
  return { ...worker, status: worker.state };
}

export function normalizeRoster(roster) {
  if (!roster || typeof roster !== "object") return roster;
  if (!Array.isArray(roster.agents)) return roster;
  return { ...roster, agents: roster.agents.map(normalizeWorker) };
}

// Only a copy that rendered or reported a non-zero size in the last 30 s is
// polled (F18); the installed API has no copy-visibility call.
export const VISIBLE_MS = 30000;

export function pollAllowed(lastVisibleAt, now = Date.now(), windowMs = VISIBLE_MS) {
  const last = Number(lastVisibleAt);
  if (!Number.isFinite(last) || last <= 0) return false;
  return now - last <= windowMs;
}

// A section's diagnostics are first stderr lines, also when the output was not
// JSON (F14).
export function nonJsonMessage(command, stderr) {
  const first = String(stderr ?? "").trim().split("\n").filter(Boolean).slice(0, 4).join("\n");
  const head = `${command} did not return JSON`;
  return first ? `${head}\n${first}` : head;
}

// Per-kind model diagnostics live beside the shared assistants section so a
// later lookup does not erase them (F14).
export function modelsErrorKey(kind) {
  return `models:${kind}`;
}

// What the cleanup confirmation lists before its button (F16).
export function gcCandidates(gc) {
  const rows = Array.isArray(gc?.would_remove) ? gc.would_remove : [];
  return rows.map((row) => ({
    path: String(row?.path ?? row?.name ?? row?.id ?? ""),
    size: Number(row?.bytes ?? row?.size ?? 0) || 0,
    reason: String(row?.reason ?? row?.state ?? row?.kind ?? ""),
  }));
}

// The legacy table is read by its header columns: KIND, EXECUTABLE, FAMILY,
// EFFORT, INSTALLED and MCP. INSTALLED and MCP come from the trailing columns,
// so a multi-word FAMILY and a blank EFFORT keep every other cell in place
// (F11).
export function parseKindsText(text) {
  const lines = String(text ?? "").split("\n");
  const rows = [];
  let columns = null;
  for (const raw of lines) {
    const line = raw.replace(/\s+$/, "");
    if (!line.trim()) continue;
    if (!columns) {
      const found = [...line.matchAll(/[A-Z][A-Z_]*/g)].map((m) => ({ name: m[0], at: m.index }));
      if (found.length >= 5 && found[0].name === "KIND") columns = found;
      continue;
    }
    const slice = (i) => {
      const from = columns[i].at;
      const to = i + 1 < columns.length ? columns[i + 1].at : line.length;
      return line.slice(from, to).trim();
    };
    const tokens = line.trim().split(/\s+/);
    if (tokens.length < 5) continue;
    const installed = tokens[tokens.length - 2].toLowerCase();
    const mcp = tokens[tokens.length - 1].toLowerCase();
    if (!["yes", "no"].includes(installed) || !["yes", "no"].includes(mcp)) continue;
    const kind = slice(0);
    if (!kind) continue;
    rows.push({
      kind,
      executable: slice(1),
      family: slice(2),
      effort_ceiling: slice(3),
      installed: installed === "yes",
      mcp: mcp === "yes",
    });
  }
  return rows;
}

export function parseModelsText(text) {
  return String(text ?? "")
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

// The Edit form lists installed assistants. With assistants_json, only an
// assistant whose probe answered "ready" is offered; without it, `kinds` text
// is used and installed is the only rule (F17).
export function assistantChoices(payload, kindsText, modelsByKind, caps) {
  const support = capabilities(caps);
  let assistants;
  if (support.assistantsJson && Array.isArray(payload?.assistants)) {
    assistants = payload.assistants
      .filter((a) => a && a.installed && a.probe?.status === "ready")
      .map((a) => ({
        kind: a.kind,
        models: Array.isArray(a.models) ? a.models : [],
        family: a.family ?? "",
        probe: a.probe ?? null,
      }));
  } else {
    assistants = parseKindsText(kindsText)
      .filter((a) => a.installed)
      .map((a) => ({
        kind: a.kind,
        models: parseModelsText(modelsByKind?.[a.kind] ?? ""),
        family: a.family,
        probe: null,
      }));
  }
  return {
    assistants,
    needsModelInput: assistants.some((a) => a.models.length === 0),
  };
}

export function assistantsWithRoles(assistants, roles) {
  const rows = Array.isArray(roles) ? roles : [];
  return assistants.map((a) => ({
    ...a,
    roles: rows
      .filter((r) => r && r.kind === a.kind)
      .map((r) => ({ role: r.role, lane: r.lane ?? "", model: r.model ?? "" })),
  }));
}

export function prefsFor(prefs, folder, patch) {
  const all = prefs && typeof prefs === "object" ? prefs : {};
  return { ...all, [folder]: { ...(all[folder] ?? {}), ...patch } };
}

export function prefsRead(prefs, folder) {
  return (prefs && typeof prefs === "object" && prefs[folder] && typeof prefs[folder] === "object")
    ? prefs[folder]
    : {};
}
