// Package testutil: DJA-392 frozen-golden adaptation for the per-role MCP
// server choice.
//
// DJA-392 intentional contract adaptation: roles with mcp=none add the
// kind's MCP-off flag after the effort args; the frozen bytes are unchanged.
//
// The rewrites below adapt the frozen parity goldens (parity-spawn.json,
// parity-dispatch.json and parity-kinds.json) to the DJA-392 contract
// without regenerating them from the implementation: the skill roles listed
// in McpOffRoles ship with frontmatter `mcp: none`, so a fresh spawn of such
// a role gains the kind's per-process MCP-off flag right after the effort
// args (item 5 of the DJA-392 brief), and the kinds without a per-process
// flag gain the one-line stderr warning instead. Every rewrite is anchored:
// a scenario that should adapt but whose anchor is missing returns an
// error, so a shifted golden fails loudly instead of being silently
// rewritten.
package testutil

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
)

// McpOffRoles are the skill roles that ship with frontmatter `mcp: none`
// (DJA-392 item 6); a spawn of any other role keeps `inherit`.
var McpOffRoles = map[string]bool{
	"reviewer": true, "implementer": true, "tasker": true,
	"scouter": true, "researcher": true, "documenter": true,
	"security-reviewer": true,
}

// McpOffArgs is the kind's static per-process MCP-off flag as it appears in
// a joined argv ("" when the kind has no static flag), mirroring
// kinds.KindMcpArgs so the oracle never imports the production package.
// Codex carries no static flag: its off args are dynamic (one
// -c mcp_servers.<name>.enabled=false pair per enabled server, built by the
// spawn from `codex mcp list --json`), so the frozen goldens — which pin
// the argv before the DJA-392 field and cannot carry a live listing — adapt
// as listing-free (no args) for codex.
func McpOffArgs(kind string) string {
	switch kind {
	case "claude":
		return "--strict-mcp-config"
	case "pi":
		return "--no-mcp"
	default:
		return ""
	}
}

// McpOffKind reports whether the kind can turn the user's MCP servers off
// per process at all (the `soho kinds` MCP column): claude and pi through a
// static flag, codex through its per-server override. It mirrors
// kinds.KindMcpOff.
func McpOffKind(kind string) bool {
	switch kind {
	case "claude", "codex", "pi":
		return true
	default:
		return false
	}
}

// McpWarnLine is the exact line a fresh spawn of an unsupported kind prints
// when mcp resolves to none, in the frozen-golden spelling (PROG: prefix).
func McpWarnLine(kind, agent string) string {
	return "PROG: warning: spawn: " + kind + " cannot turn MCP servers off per process; '" + agent + "' keeps the user's MCP servers\n"
}

// McpCodexListWarnLine is the exact line a fresh codex spawn prints when its
// `mcp list --json` run fails in the frozen fixtures: the fixtures' fake
// codex answers every call with an empty stdout, so the reason is always
// "non-JSON output" there. The production warning carries the real reason
// (codex not found in PATH, timed out after 10 s, exit <n>, non-JSON
// output).
func McpCodexListWarnLine(agent string) string {
	return "PROG: warning: spawn: codex could not list its MCP servers (non-JSON output); '" + agent + "' keeps the user's MCP servers\n"
}

// effortAnchor returns the two-field anchor after which the MCP-off flag is
// inserted for the kind (the flag goes right after the effort args, before
// the config and user args).
func effortAnchor(kind string) []string {
	switch kind {
	case "codex":
		return []string{"-c", `model_reasoning_effort="`} // prefix match on the value
	case "pi":
		return []string{"--thinking", ""}
	default:
		return []string{"--effort", ""}
	}
}

// insertMcpFlag appends the flag at its item-5 position inside the joined
// agent args. It fails when the effort anchor is missing and the args carry
// more than a model flag, where the position would be guesswork (the
// anchor-free forms are an empty argv or the model flag alone).
func insertMcpFlag(kind, args, flag string) (string, error) {
	fields := strings.Fields(args)
	anchor := effortAnchor(kind)
	for i := 0; i < len(fields); i++ {
		if fields[i] != anchor[0] || i+1 >= len(fields) {
			continue
		}
		if anchor[1] == "" || strings.HasPrefix(fields[i+1], anchor[1]) {
			return strings.Join(append(append([]string{}, fields[:i+2]...), append([]string{flag}, fields[i+2:]...)...), " "), nil
		}
	}
	if len(fields) > 2 {
		return "", fmt.Errorf("DJA-392 adaptation: no effort anchor in kind %s agent args %q; the position would be guesswork", kind, args)
	}
	if args == "" {
		return flag, nil
	}
	return args + " " + flag, nil
}

// firstSpawnDoc returns the first top-level JSON document of the output —
// the spawn JSON, which a `run` step may continue with the dispatch JSON
// and collected text. The spawn JSON is pretty-printed, so its closing
// brace is the first column-0 one; ok is false when the output does not
// begin with that document.
func firstSpawnDoc(out string) (string, bool) {
	trimmed := strings.TrimLeft(out, " \t")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", false
	}
	end := strings.Index(out, "\n}\n")
	if end < 0 {
		return "", false
	}
	return out[:end+3], true
}

// RewriteMcpSpawnJSON adapts the frozen spawn JSON: a fresh spawn (not a
// reuse) of a McpOffRoles role with a supported kind gains the flag in its
// agent_args field. Everything else passes through unchanged.
func RewriteMcpSpawnJSON(out string) (string, error) {
	doc, ok := firstSpawnDoc(out)
	if !ok {
		return out, nil
	}
	v, err := jsonjs.Parse([]byte(doc))
	if err != nil {
		return out, nil
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return out, nil
	}
	role, _ := obj.Get("role")
	roleName, _ := role.(string)
	if !McpOffRoles[roleName] {
		return out, nil
	}
	if reused, _ := obj.Get("reused"); reused == true {
		return out, nil
	}
	kindValue, _ := obj.Get("kind")
	kind, _ := kindValue.(string)
	flag := McpOffArgs(kind)
	if flag == "" {
		return out, nil
	}
	argsValue, ok := obj.Get("agent_args")
	if !ok {
		return "", fmt.Errorf("DJA-392 adaptation: spawn JSON of role %s (kind %s) has no agent_args anchor: %s", roleName, kind, out)
	}
	args, _ := argsValue.(string)
	newArgs, err := insertMcpFlag(kind, args, flag)
	if err != nil {
		return "", err
	}
	obj.Set("agent_args", newArgs)
	return jsonjs.StringifyIndent(obj, 2) + "\n" + out[len(doc):], nil
}

// RewriteMcpAgentStartLog adapts the frozen herdr call logs: an `agent
// start` line of a supported kind gains the flag at the item-5 position.
// Lines of the other kinds never change.
func RewriteMcpAgentStartLog(log string) (string, error) {
	lines := strings.Split(log, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "agent start ") {
			continue
		}
		fields := strings.Fields(line)
		kind := ""
		for j := 0; j+1 < len(fields); j++ {
			if fields[j] == "--kind" {
				kind = fields[j+1]
			}
		}
		flag := McpOffArgs(kind)
		if flag == "" {
			continue
		}
		sep := -1
		for j := 0; j < len(fields); j++ {
			if fields[j] == "--" {
				sep = j
				break
			}
		}
		args := ""
		if sep > 0 && sep < len(fields)-1 {
			args = strings.Join(fields[sep+1:], " ")
		}
		newArgs, err := insertMcpFlag(kind, args, flag)
		if err != nil {
			return "", fmt.Errorf("DJA-392 adaptation: %s", err)
		}
		if sep >= 0 {
			lines[i] = strings.Join(fields[:sep], " ") + " -- " + newArgs
		} else {
			lines[i] = strings.Join(fields, " ") + " -- " + newArgs
		}
	}
	return strings.Join(lines, "\n"), nil
}

// RewriteMcpErr adapts the frozen stderr of a fresh spawn: a McpOffRoles
// role whose kind has no per-process flag gains the one-line
// "cannot turn MCP servers off per process" warning; a fresh codex spawn
// gains the "could not list its MCP servers" warning (the fixtures' fake
// codex answers the listing with an empty stdout). The warning is emitted
// when the spawn builds its agent args — after every decision line (lane
// gone, …), before the startup-blocked line and the --tab-label line of the
// same spawn, and at the end otherwise.
func RewriteMcpErr(err, out string) (string, error) {
	doc, ok := firstSpawnDoc(out)
	if !ok {
		return err, nil
	}
	v, errp := jsonjs.Parse([]byte(doc))
	if errp != nil {
		return err, nil
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return err, nil
	}
	role, _ := obj.Get("role")
	roleName, _ := role.(string)
	if !McpOffRoles[roleName] {
		return err, nil
	}
	if reused, _ := obj.Get("reused"); reused == true {
		return err, nil
	}
	if _, ok := obj.Get("agent_args"); !ok {
		return err, nil // not a fresh spawn (kind-mismatch, busy, …)
	}
	kindValue, _ := obj.Get("kind")
	kind, _ := kindValue.(string)
	switch kind {
	case "claude", "pi":
		return err, nil // static flag, no warning
	case "codex":
		break // the dynamic listing fails in the frozen fixtures
	default:
		if McpOffArgs(kind) != "" {
			return err, nil
		}
	}
	name, _ := obj.Get("name")
	agent, _ := name.(string)
	var line string
	if kind == "codex" {
		line = McpCodexListWarnLine(agent)
	} else {
		line = McpWarnLine(kind, agent)
	}
	blocked := "PROG: warning: agent '" + agent + "' is blocked during startup"
	tabLabel := "PROG: warning: --tab-label ignored"
	lines := strings.Split(err, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, blocked) || strings.HasPrefix(l, tabLabel) {
			newLines := make([]string, 0, len(lines)+1)
			newLines = append(newLines, lines[:i]...)
			newLines = append(newLines, line[:len(line)-1])
			newLines = append(newLines, lines[i:]...)
			return strings.Join(newLines, "\n"), nil
		}
	}
	return err + line, nil
}

// IsMcpFrictionLog reports whether a frozen-golden final state file is the
// friction.log the DJA-392 MCP warning of a fresh spawn adds: the frozen
// scenarios predate the warning, so the legacy run left no friction.log and
// the native run must leave exactly its one (normalized) line. Nothing else
// may appear — any other content fails the comparison.
func IsMcpFrictionLog(key, content string) bool {
	if !strings.HasSuffix(key, "friction.log") {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) != 1 {
		return false
	}
	fields := strings.SplitN(lines[0], "\t", 4)
	if len(fields) != 4 || fields[1] != "warning" || (fields[2] != "run" && fields[2] != "spawn") {
		return false
	}
	if !strings.HasPrefix(fields[3], "spawn: ") || !strings.HasSuffix(fields[3], "keeps the user's MCP servers") {
		return false
	}
	return strings.Contains(fields[3], "cannot turn MCP servers off per process") ||
		strings.Contains(fields[3], "could not list its MCP servers")
}

// RewriteMcpKindsTable adapts the frozen `soho kinds` table: the MCP column
// (yes when the kind has a per-process flag, no otherwise) is appended.
func RewriteMcpKindsTable(out string) (string, error) {
	const header = "KIND     EXECUTABLE    FAMILY     EFFORT   INSTALLED"
	if !strings.Contains(out, header+"\n") {
		return out, nil
	}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if line == header {
			lines[i] = header + " MCP"
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kind, installed := fields[0], fields[len(fields)-1]
		if installed != "yes" && installed != "no" {
			continue
		}
		value := "no"
		if McpOffKind(kind) {
			value = "yes"
		}
		lines[i] = line + strings.Repeat(" ", 10-len(installed)) + value
	}
	return strings.Join(lines, "\n"), nil
}

// McpSpawnedAgents returns the agent names of the spawn JSON documents the
// step outputs carry (fresh or reuse; a `run` step's dispatch JSON and the
// collected text never count). The names anchor RewriteMcpRoster: only the
// rows those spawns wrote gain the roster's MCP field.
func McpSpawnedAgents(outs ...string) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, out := range outs {
		doc, ok := firstSpawnDoc(out)
		if !ok {
			continue
		}
		v, err := jsonjs.Parse([]byte(doc))
		if err != nil {
			continue
		}
		obj, ok := v.(*jsonjs.Object)
		if !ok {
			continue
		}
		nameValue, _ := obj.Get("name")
		name, _ := nameValue.(string)
		if name == "" {
			continue
		}
		_, fresh := obj.Get("created_pane")
		reusedValue, _ := obj.Get("reused")
		if !fresh && reusedValue != true {
			continue
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// RewriteMcpRoster adapts the frozen roster files: every row the scenario's
// spawns wrote — exactly 15 fields (the rows predate the MCP field) and the
// agent's name in spawned — gains the effective MCP choice as the 16th
// field: none for a McpOffRoles role, inherit for the rest. Rows of any
// other shape (seeded 11/12/8-field rows, rows of agents the scenario never
// spawned) pass through byte-identical. A 15-field anchored row with an
// empty role column fails instead of guessing, because no spawn writes such
// a row.
func RewriteMcpRoster(roster string, spawned []string) (string, error) {
	if roster == "" {
		return roster, nil
	}
	want := map[string]bool{}
	for _, name := range spawned {
		want[name] = true
	}
	lines := strings.Split(roster, "\n")
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 15 || !want[fields[0]] {
			continue
		}
		if fields[3] == "" {
			return "", fmt.Errorf("DJA-392 R2 roster adaptation: row %d of agent %q has no role column; the 15-field anchor cannot resolve the MCP choice", i, fields[0])
		}
		value := "inherit"
		if McpOffRoles[fields[3]] {
			value = "none"
		}
		lines[i] = line + "\t" + value
	}
	return strings.Join(lines, "\n"), nil
}
