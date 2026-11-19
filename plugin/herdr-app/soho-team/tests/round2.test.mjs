// Round 2: the owner's feedback from the installed panel. Model decisions for
// the row targets, the roster refusal, the counter tiles and the tab list.
import assert from "node:assert/strict";
import test from "node:test";
import {
  ROSTER_REFUSAL,
  resolveTab,
  rosterRefusal,
  statTiles,
  tabsFor,
  workerStats,
} from "../model.js";

test("R3: only the two roster refusals become the neutral note", () => {
  assert.equal(rosterRefusal("soho: not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside"),
    ROSTER_REFUSAL);
  assert.equal(rosterRefusal("soho: not inside an hc terminal (HC_TERMINAL_ID is not set); refusing to control a session from outside"),
    ROSTER_REFUSAL);
  assert.equal(rosterRefusal("soho: not running inside Herdr (HERDR_ENV != 1)"), ROSTER_REFUSAL);
  // Anything else stays an error: a broken state file, a missing folder, a crash.
  assert.equal(rosterRefusal("soho: open /p/state.json: permission denied"), "");
  assert.equal(rosterRefusal(""), "");
  assert.equal(rosterRefusal(null), "");
  assert.equal(rosterRefusal(undefined), "");
  assert.equal(ROSTER_REFUSAL, "This soho cannot list workers from the panel yet.");
});

test("R3: the max tile appears only for a positive maximum", () => {
  assert.deepEqual(statTiles(workerStats([], 4)), [["0", "working"], ["0", "report in"], ["4", "max"]]);
  assert.deepEqual(statTiles(workerStats([], 0)), [["0", "working"], ["0", "report in"]]);
  assert.deepEqual(statTiles(workerStats([], undefined)), [["0", "working"], ["0", "report in"]]);
  assert.deepEqual(statTiles(workerStats([], -1)), [["0", "working"], ["0", "report in"]]);
  assert.deepEqual(statTiles(workerStats([], "8")), [["0", "working"], ["0", "report in"], ["8", "max"]]);
  assert.deepEqual(statTiles(workerStats([{ status: "working" }, { status: "done", report: "ready" }], 2)),
    [["1", "working"], ["1", "report in"], ["2", "max"]]);
});

test("R4: the tab list follows the capabilities and a hidden tab falls back to Team", () => {
  assert.deepEqual(tabsFor({ health_json: true }).map((tab) => tab.id), ["team", "now", "assistants", "health"]);
  assert.deepEqual(tabsFor({}).map((tab) => tab.id), ["team", "now", "assistants"]);
  assert.deepEqual(tabsFor(null).map((tab) => tab.id), ["team", "now", "assistants"]);
  // Assistants always stays: it has the legacy kinds fallback.
  assert.ok(tabsFor({}).some((tab) => tab.id === "assistants"));
  assert.equal(resolveTab("health", { health_json: true }), "health");
  assert.equal(resolveTab("health", {}), "team");
  assert.equal(resolveTab("now", {}), "now");
  assert.equal(resolveTab("team", {}), "team");
  // The camel capability object the controller holds works too.
  assert.equal(resolveTab("health", { healthJson: true }), "health");
});
