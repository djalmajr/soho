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

export function capabilities(payload) {
  const out = {};
  for (const key of CAPABILITIES) {
    const camel = key.replace(/_([a-z])/g, (_m, c) => c.toUpperCase());
    out[camel] = Boolean(payload && typeof payload === "object" && Object.hasOwn(payload, key));
  }
  return out;
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

export function configSetCommand(key, value, scope) {
  return `soho config set ${key} ${value} --${scope === "user" ? "user" : "project"}`;
}

export function editFields({ appliesTo, lane, role, kind, model, effort }) {
  const prefix = appliesTo === "lane" && lane ? `lane.${lane}` : `role.${role}`;
  const fields = [];
  if (kind) fields.push({ key: `${prefix}.kind`, value: String(kind) });
  if (model) fields.push({ key: `${prefix}.model`, value: String(model) });
  if (effort) fields.push({ key: `${prefix}.effort`, value: String(effort) });
  return fields;
}

export function previewCommands(fields, scope) {
  return fields.map((f) => configSetCommand(f.key, f.value, scope));
}

// With config_dry_run the CLI owns each line; without it the panel composes
// the same lines locally. A dry-run result with valid:false is not a preview.
export function previewFromDryRuns(results, fields, scope) {
  const fallback = previewCommands(fields, scope);
  if (!Array.isArray(results) || results.length !== fields.length) {
    return { valid: true, error: "", commands: fallback };
  }
  const bad = results.find((r) => r && r.valid === false);
  if (bad) return { valid: false, error: String(bad.error ?? "invalid value"), commands: [] };
  return {
    valid: true,
    error: "",
    commands: results.map((r, i) => (r && typeof r.command === "string" ? r.command : fallback[i])),
  };
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
  return { available: true, args: ["config", "set", key, String(previous.in_file), flag] };
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

export function parseKindsText(text) {
  return String(text ?? "")
    .split("\n")
    .map((line) => line.trim().split(/\s+/))
    .filter((cells) => cells.length >= 5 && cells[0] !== "KIND")
    .map((cells) => ({
      kind: cells[0],
      executable: cells[1],
      family: cells[2],
      effort_ceiling: cells[3],
      installed: cells[4].toLowerCase() === "yes",
      mcp: cells[5] === "yes",
    }));
}

export function parseModelsText(text) {
  return String(text ?? "")
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

// The Edit form lists installed assistants. With assistants_json, only an
// assistant whose probe answered is offered; without it, `kinds` text is used.
export function assistantChoices(payload, kindsText, modelsByKind, caps) {
  const support = capabilities(caps);
  let assistants;
  if (support.assistantsJson && Array.isArray(payload?.assistants)) {
    assistants = payload.assistants
      // Installed and not known to fail: a probe that never ran (null) still
      // counts, so a fresh install lists its assistants; no-auth, quota and
      // error leave the list until "Probe again" says ready.
      .filter((a) => a && a.installed && (a.probe == null || a.probe.status === "ready"))
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
