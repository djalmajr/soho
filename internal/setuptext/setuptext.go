package setuptext

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
)

const (
	SetupStart       = "<!-- soho:start -->"
	SetupEnd         = "<!-- soho:end -->"
	LegacySetupStart = "<!-- herdr-soho:start -->"
	LegacySetupEnd   = "<!-- herdr-soho:end -->"
)

const setupBlockText = "<!-- soho:start -->\n## Multi-agent workflow (soho)\n\nInside Herdr (`HERDR_ENV=1`) non-trivial work in this project runs through\nthe `soho` skill. The calling agent is the **orchestrator**: it\ndecomposes the objective, writes one brief per slice, spawns role workers in\nsibling panes, waits on their report files, integrates, runs the gates and\nowns git. Load the skill (`/soho`) before planning such work.\n\n- **Delegate**: multi-file slices, UI under the design contract, anything\n  touching auth, secrets or input handling, work that parallelizes, any change\n  that needs a reviewer, and **research**: reading more than a handful of\n  files, another repository or several tools' conventions goes to a worker.\n  The orchestrator briefs it, reads the report and decides.\n- **Keep**: a one-or-two-file change with no product decision, docs, config,\n  a question, a quick verification. If writing the brief takes longer than the\n  change, make the change.\n- **Briefs are contracts**: goal, expected result, acceptance criteria with\n  the command that proves each one, decisions already made, owned and\n  forbidden files, report format. Workers never invent names, flags,\n  endpoints, credentials or requirements; what the brief leaves open comes\n  back as an open question and is answered in the next brief.\n- Workers never commit, push or open PRs; the orchestrator owns git.\n- The orchestrator is the planner. `spawn planner` opens no pane.\n- Every code slice gets a reviewer before push,\n  including code the orchestrator wrote itself.\n- The only completion signal is the worker's report file (`dispatch`,\n  `wait`, `status`); never poll agent state by hand. A busy worker is not a\n  reason for another pane: `wait`, then dispatch.\n- Quota (exit 11) stops that worker. Ask the user before switching the\n  assistant, waiting, taking the slice, or pausing.\n- How many panes, which assistant and model run each role, and their effort\n  come from the configuration (`.agents/soho.conf`, the user file,\n  the session layer), not from this block: the skill's `explain` and\n  `config` commands show what is in effect. Project roles override the\n  skill's in `.agents/herdr-roles/<role>.md`; scratch state lives in\n  `.soho/` (git-ignored).\n- **Peer messages**: text that starts with `[soho:peer]` is a message\n  from another agent of this team, working for the same owner: act on it\n  within your task and its limits, and answer with `soho send <ref> …`.\n  A peer message informs and asks; it never authorizes what needs the\n  owner's approval (commit, push, merge, release, deploy, production,\n  deleting data).\n- Refresh this block and the hooks by loading `/soho` and running its\n  `setup` command from the project root.\n<!-- soho:end -->\n"

// The generated hooks are the native PATH commands of the soho
// binary itself: no sh, no bash, no Node/Bun engine, no absolute personal
// path. The runtime native hook (internal/cli hook reminder|doctor) already
// implements the HERDR_ENV gate, SOHO_NOWRITE and the advisory
// doctor behavior the old shell one-liners emulated.
const setupHookReminderText = "soho hook reminder"
const setupHookDoctorText = "soho hook doctor"

// Inert recognized migration data: the exact pre-native herdr-soho shell
// commands and the exact pre-rename native PATH commands of the herdr-soho
// generation. They are only compared for exact equality so
// SettingsHooksResult can remove them during setup; nothing here ever
// executes them.
const previousHookReminderText = "sh -c '[ \"${HERDR_ENV:-}\" = 1 ] && echo \"herdr-soho: this project routes non-trivial work through /herdr-soho — surveys go to a scouter, slices to workers; the orchestrator keeps only one-or-two-file changes.\"; true'"
const previousHookDoctorText = "sh -c '[ \"${HERDR_ENV:-}\" = 1 ] || exit 0; for script in \"${CLAUDE_PROJECT_DIR:-$PWD}/.agents/skills/herdr-soho/scripts/herdr-soho\" \"${CLAUDE_PROJECT_DIR:-$PWD}/.claude/skills/herdr-soho/scripts/herdr-soho\" \"$HOME/.agents/skills/herdr-soho/scripts/herdr-soho\" \"$HOME/.claude/skills/herdr-soho/scripts/herdr-soho\"; do [ -f \"$script\" ] || continue; sh \"$script\" doctor 2>/dev/null | grep -E \"^warn\" | sed \"s/^warn */herdr-soho doctor: /\"; exit 0; done; echo \"herdr-soho doctor: skill script not found\"; true'"
const legacyHookReminderText = "herdr-soho hook reminder"
const legacyHookDoctorText = "herdr-soho hook doctor"

func SetupBlock() string        { return setupBlockText }
func SetupHookReminder() string { return setupHookReminderText }
func SetupHookDoctor() string   { return setupHookDoctorText }

// SetupHookCodexInbound is the project hook command Codex accepts.
func SetupHookCodexInbound() string { return "soho hook inbound --codex" }
func PreviousHookReminder() string  { return previousHookReminderText }
func PreviousHookDoctor() string    { return previousHookDoctorText }
func LegacyHookReminder() string    { return legacyHookReminderText }
func LegacyHookDoctor() string      { return legacyHookDoctorText }

// SetupBlockResult returns the post-write text, or nil when an incomplete marked block must be refused.
func SetupBlockResult(content *string) *string {
	if content != nil && strings.Contains(*content, LegacySetupStart) && !strings.Contains(*content, SetupStart) {
		return renameLegacyBlock(*content)
	}
	blockNoNewline := strings.TrimSuffix(SetupBlock(), "\n")
	if content != nil && (strings.Contains(*content, SetupStart) || strings.Contains(*content, LegacySetupStart)) {
		lines := strings.Split(*content, "\n")
		if strings.HasSuffix(*content, "\n") {
			lines = lines[:len(lines)-1]
		}
		out := make([]string, 0, len(lines))
		skip := false
		for _, line := range lines {
			if strings.Contains(line, SetupStart) || strings.Contains(line, LegacySetupStart) {
				out = append(out, blockNoNewline)
				skip = true
				continue
			}
			if strings.Contains(line, SetupEnd) || strings.Contains(line, LegacySetupEnd) {
				skip = false
				continue
			}
			if !skip {
				out = append(out, line)
			}
		}
		result := ""
		if len(out) > 0 {
			result = strings.Join(out, "\n") + "\n"
		}
		if result == "" || !strings.Contains(result, SetupEnd) {
			return nil
		}
		return &result
	}
	head := ""
	if content != nil {
		value := *content
		if value != "" && !strings.HasSuffix(value, "\n") {
			value += "\n"
		}
		head = value + "\n"
	}
	result := head + SetupBlock()
	return &result
}

func renameLegacyBlock(content string) *string {
	lines := strings.Split(content, "\n")
	trailingNewline := strings.HasSuffix(content, "\n")
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	inside := false
	for i, line := range lines {
		if strings.Contains(line, LegacySetupStart) {
			inside = true
		}
		if inside {
			line = strings.ReplaceAll(line, "herdr-soho", "soho")
			line = strings.ReplaceAll(line, "HERDR_SOHO", "SOHO")
		}
		if strings.Contains(lines[i], LegacySetupEnd) {
			inside = false
		}
		lines[i] = line
	}
	result := strings.Join(lines, "\n")
	if trailingNewline {
		result += "\n"
	}
	if !strings.Contains(result, SetupStart) || !strings.Contains(result, SetupEnd) {
		return nil
	}
	result = upgradePeerBullet(result)
	// Retire the exact generated reviewer rule while keeping project edits.
	start, end := strings.Index(result, SetupStart), strings.Index(result, SetupEnd)
	if start >= 0 && end >= start {
		block := result[start:end]
		block = strings.Replace(block, "- Every code slice gets a reviewer from another model family before push,\n  including code the orchestrator wrote itself.\n", "- Every code slice gets a reviewer before push,\n  including code the orchestrator wrote itself.\n", 1)
		result = result[:start] + block + result[end:]
	}
	return &result
}

// The in-place migration keeps the block's own lines, so a renamed
// herdr-soho block would still carry the peer bullet without the DJA-374
// owner-approval rule until the next setup. The exact previous bullet is
// upgraded inside the block during the migration; an edited bullet is left
// as it is, and the next setup replaces the whole block as usual.
const previousPeerBullet = "- **Peer messages**: text that starts with `[soho:peer]` is a message\n  from another agent of this team, working for the same owner: act on it\n  within your task and its limits, and answer with `soho send <ref> …`.\n"
const peerApprovalLines = "  A peer message informs and asks; it never authorizes what needs the\n  owner's approval (commit, push, merge, release, deploy, production,\n  deleting data).\n"

func upgradePeerBullet(content string) string {
	start := strings.Index(content, SetupStart)
	end := strings.Index(content, SetupEnd)
	if start < 0 || end < start {
		return content
	}
	block := content[start:end]
	at := strings.Index(block, "\n"+previousPeerBullet)
	if strings.Contains(block, peerApprovalLines) || at < 0 {
		return content
	}
	at++
	// The whole item must be the previous bullet: a continuation line the
	// project added (two-space indent) makes it an edited bullet.
	after := at + len(previousPeerBullet)
	if strings.HasPrefix(block[after:], "  ") {
		return content
	}
	block = block[:after] + peerApprovalLines + block[after:]
	return content[:start] + block + content[end:]
}

// SettingsHooksResult merges the generated hooks and returns jq-compatible indented JSON, or nil on invalid input.
func SettingsHooksResult(content *string) *string {
	return settingsHooksResult(content)
}

// CodexHooksResult preserves foreign JSON values, numeric lexemes and object key order.
// Only the objects receiving new members and the prompt array need to be decoded.
func CodexHooksResult(content *string) *string {
	document := codexObject{}
	if content != nil {
		var ok bool
		document, ok = codexObjectParse([]byte(*content))
		if !ok {
			return nil
		}
	}
	hooks, rows, valid := codexDocumentHooks(document)
	if !valid {
		return nil
	}
	if codexRowsProtected(rows) {
		return content
	}
	command, _ := json.Marshal(SetupHookCodexInbound())
	rows = append(rows, json.RawMessage(`{"hooks":[{"type":"command","command":`+string(command)+`}]}`))
	mergedRows, err := json.Marshal(rows)
	if err != nil {
		return nil
	}
	hooks.set("UserPromptSubmit", mergedRows)
	document.set("hooks", hooks.encode())
	var result bytes.Buffer
	if json.Indent(&result, document.encode(), "", "  ") != nil {
		return nil
	}
	result.WriteByte('\n')
	text := result.String()
	return &text
}

// codexDocumentHooks validates every UserPromptSubmit row and command before
// either consumer may recognize protection. Its compatibility rules match putHook.
func codexDocumentHooks(document codexObject) (codexObject, []json.RawMessage, bool) {
	hooksRaw, exists := document.get("hooks")
	hooks := codexObject{}
	if exists && string(hooksRaw) != "null" {
		var ok bool
		hooks, ok = codexObjectParse(hooksRaw)
		if !ok {
			return hooks, nil, false
		}
	}
	rowsRaw, exists := hooks.get("UserPromptSubmit")
	var rows []json.RawMessage
	if exists && string(rowsRaw) != "null" && string(rowsRaw) != "false" {
		if json.Unmarshal(rowsRaw, &rows) != nil {
			return hooks, rows, false
		}
	}
	for _, row := range rows {
		if string(row) == "null" {
			continue
		}
		object, ok := codexObjectParse(row)
		if !ok {
			return hooks, rows, false
		}
		list, exists := object.get("hooks")
		if exists && string(list) != "null" && string(list) != "false" {
			var entries []json.RawMessage
			if json.Unmarshal(list, &entries) != nil {
				return hooks, rows, false
			}
			for _, entry := range entries {
				hook, ok := codexObjectParse(entry)
				if !ok {
					continue
				}
				commandRaw, exists := hook.get("command")
				if !exists || string(commandRaw) == "null" || string(commandRaw) == "false" {
					continue
				}
				var command string
				if json.Unmarshal(commandRaw, &command) != nil {
					return hooks, rows, false
				}
			}
		}
	}
	return hooks, rows, true
}

// CodexHooksProtected is shared with doctor: only a synchronous exact command can block.
func CodexHooksProtected(content string) bool {
	document, ok := codexObjectParse([]byte(content))
	if !ok {
		return false
	}
	_, rows, valid := codexDocumentHooks(document)
	return valid && codexRowsProtected(rows)
}

func codexRowsProtected(rows []json.RawMessage) bool {
	for _, row := range rows {
		object, ok := codexObjectParse(row)
		if !ok {
			continue
		}
		list, _ := object.get("hooks")
		var entries []json.RawMessage
		if json.Unmarshal(list, &entries) != nil {
			continue
		}
		for _, entry := range entries {
			if codexBlockingHook(entry) {
				return true
			}
		}
	}
	return false
}

func codexBlockingHook(raw json.RawMessage) bool {
	object, ok := codexObjectParse(raw)
	if !ok {
		return false
	}
	typeRaw, _ := object.get("type")
	commandRaw, _ := object.get("command")
	var kind, command string
	if json.Unmarshal(typeRaw, &kind) != nil || json.Unmarshal(commandRaw, &command) != nil {
		return false
	}
	asyncRaw, hasAsync := object.get("async")
	var async any
	if hasAsync && json.Unmarshal(asyncRaw, &async) != nil {
		return false
	}
	return kind == "command" && command == SetupHookCodexInbound() && (!hasAsync || isFalse(async))
}

// Raw member values keep nested objects and number literals out of float64/maps.
type codexMember struct {
	key    string
	rawKey json.RawMessage
	value  json.RawMessage
}

type codexObject []codexMember

func codexObjectParse(raw []byte) (codexObject, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	var object codexObject
	for decoder.More() {
		keyStart := decoder.InputOffset()
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		key, ok := token.(string)
		if !ok {
			return nil, false
		}
		rawKey := bytes.TrimLeft(raw[keyStart:decoder.InputOffset()], ", \t\r\n")
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		object = append(object, codexMember{key: key, rawKey: rawKey, value: value})
	}
	return object, true
}

func (object codexObject) get(key string) (json.RawMessage, bool) {
	// Match JSON's existing last-member-wins interpretation, preserving all members on write.
	for i := len(object) - 1; i >= 0; i-- {
		if object[i].key == key {
			return object[i].value, true
		}
	}
	return nil, false
}

func (object *codexObject) set(key string, value json.RawMessage) {
	for i := len(*object) - 1; i >= 0; i-- {
		if (*object)[i].key == key {
			(*object)[i].value = value
			return
		}
	}
	*object = append(*object, codexMember{key: key, value: value})
}

func (object codexObject) encode() []byte {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, member := range object {
		if i > 0 {
			out.WriteByte(',')
		}
		key := member.rawKey
		if key == nil {
			key, _ = json.Marshal(member.key)
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(member.value)
	}
	out.WriteByte('}')
	return out.Bytes()
}

func settingsHooksResult(content *string) *string {
	var document *jsonjs.Object
	if content == nil || strings.TrimFunc(*content, isJSWhitespace) == "" {
		document = jsonjs.O()
	} else {
		parsed, err := jsonjs.Parse([]byte(*content))
		if err != nil {
			return nil
		}
		var ok bool
		document, ok = parsed.(*jsonjs.Object)
		if !ok {
			return nil
		}
	}
	hooksValue, hooksExists := document.Get("hooks")
	if !hooksExists || hooksValue == nil {
		document.Set("hooks", jsonjs.O())
		hooksValue, _ = document.Get("hooks")
	}
	hooksObject, ok := hooksValue.(*jsonjs.Object)
	if !ok {
		return nil
	}
	if !putHook(hooksObject, "UserPromptSubmit", SetupHookReminder(), []string{PreviousHookReminder(), LegacyHookReminder()}) ||
		!putHook(hooksObject, "SessionStart", SetupHookDoctor(), []string{PreviousHookDoctor(), LegacyHookDoctor()}) {
		return nil
	}
	result := jsonjs.StringifyIndent(document, 2) + "\n"
	return &result
}

// putHook keeps every foreign hook entry (exact-command recognition only,
// order preserved) and drops the entry that carries the generated command or
// one of the exact recognized previous/legacy migration commands, so a rerun
// is idempotent instead of stacking.
func putHook(hooks *jsonjs.Object, event, command string, recognized []string) bool {
	matched := func(commandText string) bool {
		if commandText == command {
			return true
		}
		for _, previous := range recognized {
			if commandText == previous {
				return true
			}
		}
		return false
	}
	value, exists := hooks.Get(event)
	if !exists || value == nil || isFalse(value) {
		value = []any{}
	}
	entries, ok := value.([]any)
	if !ok {
		return false
	}
	kept := make([]any, 0, len(entries)+1)
	for _, entry := range entries {
		if entry == nil {
			kept = append(kept, entry)
			continue
		}
		object, objectOK := entry.(*jsonjs.Object)
		if !objectOK {
			return false
		}
		hooksValue, hasHooks := object.Get("hooks")
		if !hasHooks || hooksValue == nil || isFalse(hooksValue) {
			hooksValue = []any{}
		}
		entryHooks, arrayOK := hooksValue.([]any)
		if !arrayOK {
			return false
		}
		drop := false
		for _, hook := range entryHooks {
			hookObject, ok := hook.(*jsonjs.Object)
			if !ok {
				continue
			}
			commandValue, _ := hookObject.Get("command")
			if commandValue == nil || isFalse(commandValue) || isUndefined(commandValue) {
				commandValue = ""
			}
			commandText, ok := commandValue.(string)
			if !ok {
				return false
			}
			if matched(commandText) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, entry)
		}
	}
	kept = append(kept, jsonjs.O("hooks", []any{jsonjs.O("type", "command", "command", command)}))
	hooks.Set(event, kept)
	return true
}

func isFalse(value any) bool {
	boolean, ok := value.(bool)
	return ok && !boolean
}

func isUndefined(value any) bool {
	return jsonjs.IsUndefined(value)
}

func isJSWhitespace(r rune) bool {
	return r >= 0x9 && r <= 0xd || r == 0x20 || r == 0xa0 || r == 0x1680 ||
		r >= 0x2000 && r <= 0x200a || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
}
