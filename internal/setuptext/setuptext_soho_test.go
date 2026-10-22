package setuptext

import (
	"encoding/json"
	"strings"
	"testing"
)

// previousSohoBlock is the exact instruction block the herdr-soho generation
// of the setup wrote, frozen here as the migration input: the rename has to
// recognize this generation in place, keeping the project's own lines.
const previousSohoBlock = "<!-- herdr-soho:start -->\n## Multi-agent workflow (herdr-soho)\n\nInside Herdr (`HERDR_ENV=1`) non-trivial work in this project runs through\nthe `herdr-soho` skill. The calling agent is the **orchestrator**: it\ndecomposes the objective, writes one brief per slice, spawns role workers in\nsibling panes, waits on their report files, integrates, runs the gates and\nowns git. Load the skill (`/herdr-soho`) before planning such work.\n\n- **Delegate**: multi-file slices, UI under the design contract, anything\n  touching auth, secrets or input handling, work that parallelizes, any change\n  that needs a reviewer, and **research**: reading more than a handful of\n  files, another repository or several tools' conventions goes to a worker.\n  The orchestrator briefs it, reads the report and decides.\n- **Keep**: a one-or-two-file change with no product decision, docs, config,\n  a question, a quick verification. If writing the brief takes longer than the\n  change, make the change.\n- **Briefs are contracts**: goal, expected result, acceptance criteria with\n  the command that proves each one, decisions already made, owned and\n  forbidden files, report format. Workers never invent names, flags,\n  endpoints, credentials or requirements; what the brief leaves open comes\n  back as an open question and is answered in the next brief.\n- Workers never commit, push or open PRs; the orchestrator owns git.\n- The orchestrator is the planner. `spawn planner` opens no pane.\n- Every code slice gets a reviewer from another model family before push,\n  including code the orchestrator wrote itself.\n- The only completion signal is the worker's report file (`dispatch`,\n  `wait`, `status`); never poll agent state by hand. A busy worker is not a\n  reason for another pane: `wait`, then dispatch.\n- Quota (exit 11) stops that worker. Ask the user before switching the\n  assistant, waiting, taking the slice, or pausing.\n- How many panes, which assistant and model run each role, and their effort\n  come from the configuration (`.agents/herdr-soho.conf`, the user file,\n  the session layer), not from this block: the skill's `explain` and\n  `config` commands show what is in effect. Project roles override the\n  skill's in `.agents/herdr-roles/<role>.md`; scratch state lives in\n  `.herdr-soho/` (git-ignored).\n- **Peer messages**: text that starts with `[herdr-soho:peer]` is a message\n  from another agent of this team, working for the same owner: act on it\n  within your task and its limits, and answer with `herdr-soho send <ref> …`.\n- Refresh this block and the hooks by loading `/herdr-soho` and running its\n  `setup` command from the project root.\n<!-- herdr-soho:end -->\n"

// sohoBlock is the exact instruction block the soho generation of the setup
// must write, pinned independently of the implementation: the new markers,
// the soho title and the soho names (slash command, binary, .agents/soho.conf,
// .soho/ and [soho:peer]). DJA-374: the "Peer messages" bullet carries the
// owner-approval rule (a peer message informs and asks, it never
// authorizes what needs the owner's approval).
const sohoBlock = "<!-- soho:start -->\n## Multi-agent workflow (soho)\n\nInside Herdr (`HERDR_ENV=1`) non-trivial work in this project runs through\nthe `soho` skill. The calling agent is the **orchestrator**: it\ndecomposes the objective, writes one brief per slice, spawns role workers in\nsibling panes, waits on their report files, integrates, runs the gates and\nowns git. Load the skill (`/soho`) before planning such work.\n\n- **Delegate**: multi-file slices, UI under the design contract, anything\n  touching auth, secrets or input handling, work that parallelizes, any change\n  that needs a reviewer, and **research**: reading more than a handful of\n  files, another repository or several tools' conventions goes to a worker.\n  The orchestrator briefs it, reads the report and decides.\n- **Keep**: a one-or-two-file change with no product decision, docs, config,\n  a question, a quick verification. If writing the brief takes longer than the\n  change, make the change.\n- **Briefs are contracts**: goal, expected result, acceptance criteria with\n  the command that proves each one, decisions already made, owned and\n  forbidden files, report format. Workers never invent names, flags,\n  endpoints, credentials or requirements; what the brief leaves open comes\n  back as an open question and is answered in the next brief.\n- Workers never commit, push or open PRs; the orchestrator owns git.\n- The orchestrator is the planner. `spawn planner` opens no pane.\n- Every code slice gets a reviewer before push,\n  including code the orchestrator wrote itself.\n- The only completion signal is the worker's report file (`dispatch`,\n  `wait`, `status`); never poll agent state by hand. A busy worker is not a\n  reason for another pane: `wait`, then dispatch.\n- Quota (exit 11) stops that worker. Ask the user before switching the\n  assistant, waiting, taking the slice, or pausing.\n- How many panes, which assistant and model run each role, and their effort\n  come from the configuration (`.agents/soho.conf`, the user file,\n  the session layer), not from this block: the skill's `explain` and\n  `config` commands show what is in effect. Project roles override the\n  skill's in `.agents/herdr-roles/<role>.md`; scratch state lives in\n  `.soho/` (git-ignored).\n- **Peer messages**: text that starts with `[soho:peer]` is a message\n  from another agent of this team, working for the same owner: act on it\n  within your task and its limits, and answer with `soho send <ref> …`.\n  A peer message informs and asks; it never authorizes what needs the\n  owner's approval (commit, push, merge, release, deploy, production,\n  deleting data).\n- Refresh this block and the hooks by loading `/soho` and running its\n  `setup` command from the project root.\n<!-- soho:end -->\n"

// DJA-374: the "Peer messages" bullet gains the owner-approval rule — a
// peer message informs and asks, it never authorizes what needs the
// owner's approval. dja374OldBullet is the exact bullet the soho generation
// wrote before DJA-374 (pinned independently of the testutil DJA-374
// constants); dja374NewBullet is the approved replacement: the same three
// lines plus the three new ones.
const (
	dja374OldBullet = "- **Peer messages**: text that starts with `[soho:peer]` is a message\n  from another agent of this team, working for the same owner: act on it\n  within your task and its limits, and answer with `soho send <ref> …`."
	dja374NewBullet = dja374OldBullet + "\n  A peer message informs and asks; it never authorizes what needs the\n  owner's approval (commit, push, merge, release, deploy, production,\n  deleting data)."
)

func TestSohoBlockText(t *testing.T) {
	if SetupStart != "<!-- soho:start -->" || SetupEnd != "<!-- soho:end -->" {
		t.Fatalf("soho markers: start=%q end=%q", SetupStart, SetupEnd)
	}
	if SetupBlock() != sohoBlock {
		t.Fatalf("setup block is not the exact soho generation text:\n%s", SetupBlock())
	}
}

// TestSohoPeerBlockDJA374 pins the DJA-374 owner-approval rule: the
// generated block carries the exact new "Peer messages" bullet, and a setup
// over a file holding the pre-DJA-374 block replaces the whole block with
// the new one, keeping the rest of the file byte-identical.
func TestSohoPeerBlockDJA374(t *testing.T) {
	t.Run("the generated block carries the exact DJA-374 peer bullet", func(t *testing.T) {
		block := SetupBlock()
		if strings.Count(block, dja374NewBullet) != 1 {
			t.Fatalf("the DJA-374 peer bullet is not exactly once in the generated block:\n%s", block)
		}
		// The new lines end the bullet: the Refresh line follows directly.
		if !strings.Contains(block, "deleting data).\n- Refresh this block and the hooks by loading `/soho`") {
			t.Fatalf("the DJA-374 bullet is not followed by the Refresh line:\n%s", block)
		}
	})
	t.Run("setup over a file with the pre-DJA-374 block replaces the whole block and keeps the rest", func(t *testing.T) {
		// The pre-DJA-374 block: sohoBlock with the DJA-374 bullet reverted
		// to the previous bullet. Before the implementation sohoBlock is the
		// pre-DJA-374 block itself, so the revert is a no-op and the seed
		// still holds today's block.
		preDJA374Block := strings.Replace(sohoBlock, dja374NewBullet, dja374OldBullet, 1)
		in := "# Agent instructions\n\n" + preDJA374Block + "tail\n"
		want := "# Agent instructions\n\n" + strings.Replace(preDJA374Block, dja374OldBullet, dja374NewBullet, 1) + "tail\n"
		got := SetupBlockResult(ptr(in))
		if got == nil || *got != want {
			t.Fatalf("setup did not replace the pre-DJA-374 block:\ngot:\n%v\nwant:\n%s", got, want)
		}
	})
}

func TestSohoBlockMigration(t *testing.T) {
	t.Run("a herdr-soho block with project lines migrates in place, lines preserved", func(t *testing.T) {
		// The previous-generation block with one project-added line after the
		// heading: the migration renames the generation names and keeps the
		// project line in its place, like the herdr-agents migration.
		//
		// DJA-374: the in-place migration also upgrades the exact previous
		// peer bullet, so the owner-approval rule is installed on the first
		// setup while the project line stays in place.
		in := "# Agent instructions\n\n" + insertLineAfter(previousSohoBlock, "## Multi-agent workflow (herdr-soho)\n", "project line: keep me\n") + "\ntail\n"
		want := "# Agent instructions\n\n" + insertLineAfter(sohoBlock, "## Multi-agent workflow (soho)\n", "project line: keep me\n") + "\ntail\n"
		got := SetupBlockResult(ptr(in))
		if got == nil || *got != want {
			t.Fatalf("migration result:\n%v\nwant:\n%s", got, want)
		}
		// The migrated block is now a soho-generation block: the next setup
		// replaces it with the current block and drops the project line,
		// like any block text change (the blank line after the end marker is
		// outside the block and survives).
		second := SetupBlockResult(got)
		wantSecond := "# Agent instructions\n\n" + sohoBlock + "\ntail\n"
		if second == nil || *second != wantSecond {
			t.Fatalf("second setup did not replace the migrated block:\n%v\nwant:\n%s", second, wantSecond)
		}
	})
	t.Run("a herdr-soho block with an edited peer bullet keeps the bullet until the next setup", func(t *testing.T) {
		// DJA-374: only the exact previous bullet is upgraded in place; an
		// edited one is left to the next setup, which replaces the block.
		edited := strings.Replace(previousSohoBlock, "act on it\n  within your task and its limits", "act on it\n  within your task, its limits and our house rules", 1)
		if edited == previousSohoBlock {
			t.Fatal("the edit did not apply to the previous block")
		}
		got := SetupBlockResult(ptr("# Agent instructions\n\n" + edited + "tail\n"))
		if got == nil || strings.Contains(*got, "it never authorizes") || !strings.Contains(*got, "our house rules") {
			t.Fatalf("an edited peer bullet must migrate as it is:\n%v", got)
		}
		second := SetupBlockResult(got)
		if second == nil || *second != "# Agent instructions\n\n"+sohoBlock+"tail\n" {
			t.Fatalf("the next setup did not replace the block:\n%v", second)
		}
	})
	t.Run("a herdr-soho peer bullet with a project continuation line migrates as it is", func(t *testing.T) {
		// DJA-374 review: the three previous lines followed by a line the
		// project appended to the same item are an edited bullet, not the
		// exact previous one.
		marker := "answer with `herdr-soho send <ref> …`.\n"
		if !strings.Contains(previousSohoBlock, marker) {
			t.Fatal("the previous block does not hold the peer bullet's last line")
		}
		edited := strings.Replace(previousSohoBlock, marker, marker+"  Follow our project escalation rule.\n", 1)
		got := SetupBlockResult(ptr("# Agent instructions\n\n" + edited + "tail\n"))
		if got == nil || strings.Contains(*got, "it never authorizes") || !strings.Contains(*got, "send <ref> …`.\n  Follow our project escalation rule.\n") {
			t.Fatalf("a continued peer bullet must migrate as it is:\n%v", got)
		}
	})
	t.Run("a second run over the migrated block is a no-op", func(t *testing.T) {
		first := SetupBlockResult(ptr("# Agent instructions\n\n" + sohoBlock + "tail\n"))
		if first == nil {
			t.Fatal("first run refused the current block")
		}
		second := SetupBlockResult(first)
		if second == nil || *second != *first {
			t.Fatalf("rerun changed the file:\n%v\nwant:\n%v", second, first)
		}
	})
}

func TestSohoHooksMigration(t *testing.T) {
	t.Run("the herdr-soho native hooks and the previous shell hooks are replaced by the soho hooks", func(t *testing.T) {
		seed, err := json.Marshal(map[string]any{"hooks": map[string]any{
			"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "herdr-soho hook reminder"}}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo my own reminder"}}}},
			"SessionStart":     []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "herdr-soho hook doctor"}}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": PreviousHookDoctor()}}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		first := SettingsHooksResult(ptr(string(seed)))
		if first == nil {
			t.Fatal("merge refused the herdr-soho hooks")
		}
		second := SettingsHooksResult(first)
		if second == nil || *second != *first {
			t.Fatalf("rerun not stable: %v / %v", first, second)
		}
		var doc struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(*first), &doc); err != nil {
			t.Fatal(err)
		}
		up := doc.Hooks["UserPromptSubmit"]
		ss := doc.Hooks["SessionStart"]
		// Both the herdr-soho native hook entry and the previous shell hook
		// entry are dropped: the merged UserPromptSubmit keeps the foreign
		// command in order plus the soho hook, and the SessionStart carries
		// exactly the soho hook.
		if len(up) != 2 || len(ss) != 1 {
			t.Fatalf("entries: UPS=%d SS=%d: %s", len(up), len(ss), *first)
		}
		if up[0].Hooks[0].Command != "echo my own reminder" || up[1].Hooks[0].Command != "soho hook reminder" {
			t.Fatalf("UserPromptSubmit order=%v: %s", up, *first)
		}
		if len(ss[0].Hooks) != 1 || ss[0].Hooks[0].Command != "soho hook doctor" {
			t.Fatalf("SessionStart=%v: %s", ss, *first)
		}
		if strings.Contains(*first, "herdr-soho hook reminder") || strings.Contains(*first, "herdr-soho hook doctor") || strings.Contains(*first, PreviousHookDoctor()) {
			t.Fatalf("previous herdr-soho hooks survived the merge: %s", *first)
		}
	})
	t.Run("the generated hooks are the soho PATH commands", func(t *testing.T) {
		if SetupHookReminder() != "soho hook reminder" || SetupHookDoctor() != "soho hook doctor" {
			t.Fatalf("hook literals: reminder=%q doctor=%q", SetupHookReminder(), SetupHookDoctor())
		}
	})
}

func insertLineAfter(block, anchor, line string) string {
	i := strings.Index(block, anchor)
	if i < 0 {
		return block
	}
	return block[:i+len(anchor)] + line + block[i+len(anchor):]
}
