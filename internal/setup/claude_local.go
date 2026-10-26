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
		v, _ := hooks.Get(event)
		rows := v.([]any)
		kept := rows[:len(rows)-1]
		old, exists := oldHooks.Get(event)
		if oldRows, ok := old.([]any); ok && len(oldRows) > len(kept) {
			changed = true
			if len(kept) > 0 {
				hooks.Set(event, kept)
			} else {
				hooks.Delete(event)
			}
		} else if exists {
			hooks.Set(event, old)
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
