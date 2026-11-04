package setup

import (
	"path/filepath"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

const claudeLocalHookRel = ".claude/settings.local.json"

func claudeSettingsPath(root string, local bool) string {
	if local {
		return filepath.Join(root, filepath.FromSlash(claudeLocalHookRel))
	}
	return filepath.Join(root, ".claude", "settings.json")
}

// Reuse the merge's exact ownership recognition: its last row is the newly
// generated hook, and every preceding row is a preserved foreign entry.
func sharedHooksMigration(root string) (string, *string) {
	file := claudeSettingsPath(root, false)
	before, err := platform.ReadTextFile(file)
	if err != nil {
		return file, nil
	}
	merged := setuptext.SettingsHooksResult(&before)
	if merged == nil {
		return file, nil
	}
	original, _ := jsonjs.Parse([]byte(before))
	parsed, _ := jsonjs.Parse([]byte(*merged))
	document := parsed.(*jsonjs.Object)
	originalDoc, ok := original.(*jsonjs.Object)
	if !ok {
		return file, nil
	}
	originalHooks, _ := originalDoc.Get("hooks")
	oldHooks, ok := originalHooks.(*jsonjs.Object)
	if !ok {
		return file, nil
	}
	value, _ := document.Get("hooks")
	hooks := value.(*jsonjs.Object)
	changed := false
	for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
		old, exists := oldHooks.Get(event)
		oldRows, _ := old.([]any)
		known := recognizedSohoCommands(event)
		if !exists || !rowsContainSoho(oldRows, known) {
			// The shared file carries no soho command for this event: the
			// merge's appended row must not land in it; keep the file's own
			// value (or the absence of it).
			if exists {
				hooks.Set(event, old)
			} else {
				hooks.Delete(event)
			}
			continue
		}
		// Compare by content, not by row count: a mixed row survives the
		// merge with only its foreign entries, so the count no longer tells
		// whether the shared file still carries soho commands.
		changed = true
		target := rowsWithoutSoho(oldRows, known)
		if len(target) > 0 {
			hooks.Set(event, target)
		} else {
			hooks.Delete(event)
		}
	}
	if !changed {
		return file, nil
	}
	if len(hooks.Keys()) == 0 {
		document.Delete("hooks")
	}
	after := jsonjs.StringifyIndent(document, 2) + "\n"
	return file, &after
}

func migrateSharedHooks(root string, env platform.Env) {
	file, after := sharedHooksMigration(root)
	if after == nil {
		return
	}
	if warning := sharedHooksTrackingWarning(trackingStatus(root, ".claude/settings.json", env)); warning != "" {
		core.Warn(warning, "", "setup")
		return
	}
	if err := platform.AtomicWrite(file, *after); err != nil {
		platform.DieFriction("could not migrate hooks from "+file, 4)
	}
}

func sharedHooksTrackingWarning(status fileTracking) string {
	if status == trackingUntracked {
		return ""
	}
	warning := "shared .claude/settings.json still holds soho hooks and needs a manual commit or removal"
	if status == trackingUnknown {
		warning += "; tracking status could not be determined; file left untouched"
	}
	return warning
}

// recognizedSohoCommands is the exact-command recognition of the generated
// hook of an event plus the previous/legacy migration commands, the same
// set the merge recognizes when it removes soho entries.
func recognizedSohoCommands(event string) []string {
	switch event {
	case "UserPromptSubmit":
		return []string{setuptext.SetupHookReminder(), setuptext.PreviousHookReminder(), setuptext.LegacyHookReminder()}
	case "SessionStart":
		return []string{setuptext.SetupHookDoctor(), setuptext.PreviousHookDoctor(), setuptext.LegacyHookDoctor()}
	}
	return nil
}

// rowsContainSoho reports whether any row of the event still carries a
// soho-recognized command entry (exact command match only).
func rowsContainSoho(rows []any, known []string) bool {
	for _, row := range rows {
		object, ok := row.(*jsonjs.Object)
		if !ok {
			continue
		}
		hooksValue, hasHooks := object.Get("hooks")
		if !hasHooks || hooksValue == nil || isFalseValue(hooksValue) {
			continue
		}
		list, ok := hooksValue.([]any)
		if !ok {
			continue
		}
		for _, hook := range list {
			hookObject, ok := hook.(*jsonjs.Object)
			if !ok {
				continue
			}
			commandValue, _ := hookObject.Get("command")
			if commandValue == nil || isFalseValue(commandValue) || jsonjs.IsUndefined(commandValue) {
				continue
			}
			commandText, ok := commandValue.(string)
			if !ok {
				continue
			}
			for _, k := range known {
				if commandText == k {
					return true
				}
			}
		}
	}
	return false
}

// rowsWithoutSoho returns the event's rows with the soho-recognized entries
// removed from each row: a row that had soho commands among others keeps its
// foreign entries, its other members and its place; a row that only had soho
// commands leaves. The original rows are not mutated.
func rowsWithoutSoho(rows []any, known []string) []any {
	if rows == nil {
		return nil
	}
	kept := make([]any, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			kept = append(kept, row)
			continue
		}
		object, ok := row.(*jsonjs.Object)
		if !ok {
			kept = append(kept, row)
			continue
		}
		hooksValue, hasHooks := object.Get("hooks")
		if !hasHooks || hooksValue == nil || isFalseValue(hooksValue) {
			kept = append(kept, row)
			continue
		}
		list, ok := hooksValue.([]any)
		if !ok {
			kept = append(kept, row)
			continue
		}
		filtered := make([]any, 0, len(list))
		changed := false
		for _, hook := range list {
			hookObject, ok := hook.(*jsonjs.Object)
			if !ok {
				filtered = append(filtered, hook)
				continue
			}
			commandValue, _ := hookObject.Get("command")
			if commandValue == nil || isFalseValue(commandValue) || jsonjs.IsUndefined(commandValue) {
				filtered = append(filtered, hook)
				continue
			}
			commandText, ok := commandValue.(string)
			if !ok {
				// A non-text command: the merge already refused the file,
				// so this row is kept unstrippable.
				filtered = append(filtered, hook)
				continue
			}
			matched := false
			for _, k := range known {
				if commandText == k {
					matched = true
					break
				}
			}
			if matched {
				changed = true
				continue
			}
			filtered = append(filtered, hook)
		}
		if !changed {
			kept = append(kept, row)
			continue
		}
		if len(filtered) == 0 {
			continue
		}
		// Rebuild the row with its key order; the original stays as it was.
		clone := jsonjs.O()
		for _, key := range object.Keys() {
			v, _ := object.Get(key)
			if key == "hooks" {
				v = filtered
			}
			clone.Set(key, v)
		}
		kept = append(kept, clone)
	}
	return kept
}

func isFalseValue(value any) bool {
	b, ok := value.(bool)
	return ok && !b
}
