package core

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
	textutil "github.com/djalmajr/soho/internal/text"
)

var presetLaneKeyRE = regexp.MustCompile(`^lane_([A-Za-z0-9_]+)_(roles|kind|model|models|effort|approvals|panes|args|mcp)$`)

func IsPresetLaneSignature(signature, mode string) bool {
	if signature == "" || IsLegacyPresetSignature(signature, mode) || signature == LegacyPresetSignature("3") || signature == LegacyPresetSignature("4") {
		return true
	}
	return signature == PresetSignature("4", "strict") || signature == PresetSignature("2", "strict")
}

// legacyPresetGroup reports the old group's destination and its affected roles.
func legacyPresetGroup(group string) (target string, roles []string) {
	switch group {
	case "build":
		return "lane.builders", SplitRoles(PresetLaneRoles("builders", "4", "strict"))
	case "review":
		return "lane.reviewers", SplitRoles(PresetLaneRoles("reviewers", "4", "strict"))
	case "critical":
		return "role.critical-implementer", []string{"critical-implementer"}
	case "docs":
		return "role.documenter", []string{"documenter"}
	}
	return "", nil
}

func legacyPresetTarget(group, attr, prefix string) string {
	if attr == "roles" {
		return ""
	}
	if group == "critical" {
		switch attr {
		case "models":
			return prefix + ".model"
		case "panes":
			return "lane.builders.panes"
		}
	}
	if group == "docs" && (attr == "models" || attr == "panes") {
		return ""
	}
	return prefix + "." + attr
}

type PresetGroupWarning struct{ Key, Message string }

// LegacyPresetGroupWarnings is shared by doctor and spawn. An empty role
// requests every warning. Only file keys can be repaired by doctor --fix.
func LegacyPresetGroupWarnings(ctx *Config, env platform.Env, role string) []PresetGroupWarning {
	if !LanesEnabled(ctx, env) || !IsPresetLaneSignature(EffectiveLaneSignature(ctx, env), PaneMode(ctx, env)) {
		return nil
	}
	var warnings []PresetGroupWarning
	keys := append([]string(nil), ctx.Order...)
	textutil.SortUTF16(keys)
	for _, key := range keys {
		match := presetLaneKeyRE.FindStringSubmatch(key)
		if match == nil || CfgSource(ctx, key, env) == "env" {
			continue
		}
		group, attr := match[1], match[2]
		prefix, roles := legacyPresetGroup(group)
		if prefix == "" || (role != "" && !contains(roles, role)) {
			continue
		}
		old := "lane." + group + "." + attr
		target := legacyPresetTarget(group, attr, prefix)
		message := old + " no longer applies. "
		switch {
		case target == "":
			message += "Run 'soho doctor --fix' to remove it."
		case group == "critical" && attr == "panes":
			message += "'soho doctor --fix' adds it to an explicit " + target + " or removes it."
		default:
			message += "'soho doctor --fix' moves it to " + target + "."
		}
		warnings = append(warnings, PresetGroupWarning{key, message})
	}
	return warnings
}

func dropPresetFileExtras(file, panes string, lines *[]string, mode string, keepZero bool) {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		platform.Die(fmt.Sprintf("could not rewrite %s (file left untouched)", file), 4)
	}
	rawLines := splitLines(raw)
	entries := map[string]ConfigPair{}
	for _, pair := range ConfigTextPairs([]byte(raw)) {
		entries[pair.Key] = pair
	}
	last := map[string]int{}
	keys := make([]string, len(rawLines))
	for i, line := range rawLines {
		body, _, _ := strings.Cut(line, "#")
		if key, _, ok := strings.Cut(body, "="); ok {
			keys[i] = NormalizeKey(key)
			last[keys[i]] = i
		}
	}
	criticalPanes := foldCriticalPanes(entries, last, rawLines)
	_, explicitModel := entries["role_critical_implementer_model"]
	criticalModels := splitLaneModels(entries["lane_critical_models"].Value)

	var out, notices []string
	for i, line := range rawLines {
		key := keys[i]
		switch {
		case key == "max_workers" && !keepZero:
			*lines = append(*lines, "removed max_workers="+textutil.QuoteControls(entries[key].Value)+" (derived from the lanes)")
			continue
		case key == "split_max_panes":
			*lines = append(*lines, "removed split_max_panes="+textutil.QuoteControls(entries[key].Value)+" (derived from panes)")
			continue
		}
		match := presetLaneKeyRE.FindStringSubmatch(key)
		if match == nil {
			out = append(out, line)
			continue
		}
		group, attr := match[1], match[2]
		if contains(PresetLaneNamesFor("4", mode), group) {
			if attr != "roles" {
				out = append(out, line)
			}
			continue
		}
		if last[key] != i {
			continue
		}
		old := "lane." + group + "." + attr
		prefix, _ := legacyPresetGroup(group)
		if group == "read" && attr != "roles" {
			prefix = "lane.reviewers"
		}
		if prefix == "" {
			if attr != "roles" {
				*lines = append(*lines, fmt.Sprintf("removed %s=%s (no group '%s' in the panes=%s preset)", old, textutil.QuoteControls(entries[key].Value), group, panes))
			}
			continue
		}
		target := legacyPresetTarget(group, attr, prefix)
		if target == "" {
			notices = append(notices, old+" removed (the preset supplies roles and group settings)")
			continue
		}
		if group == "critical" && attr == "panes" {
			notices = append(notices, criticalPanes)
			continue
		}
		_, targetSet := entries[NormalizeKey(target)]
		_, oldReviewSet := entries[LaneKey("review", attr)]
		switch {
		case group == "critical" && attr == "models":
			note := " (rotation lists belong to a group)"
			switch {
			case explicitModel:
				notices = append(notices, old+" → "+target+" (kept existing target)"+note)
			case len(criticalModels) == 0:
				notices = append(notices, old+" removed (no model in the list)"+note)
			default:
				// Use one pair of quotes so reload preserves the first item.
				out = append(out, target+"=\""+criticalModels[0]+"\"")
				notices = append(notices, old+" → "+target+note)
			}
		case targetSet || (group == "read" && oldReviewSet):
			notices = append(notices, old+" → "+target+" (kept existing target)")
		case group == "critical" && attr == "model" && len(criticalModels) > 0:
			notices = append(notices, old+" → "+target+" (used the first entry from lane.critical.models)")
		default:
			_, tail, _ := strings.Cut(line, "=")
			out = append(out, target+"="+tail)
			notices = append(notices, old+" → "+target)
		}
	}
	content := strings.Join(out, "\n")
	if len(out) > 0 {
		content += "\n"
	}
	if err := platform.AtomicWrite(file, content); err != nil {
		platform.Die(fmt.Sprintf("could not rewrite %s (file left untouched)", file), 4)
	}
	for _, notice := range notices {
		fmt.Fprintln(platform.Stderr, notice)
	}
}

func foldCriticalPanes(entries map[string]ConfigPair, last map[string]int, rawLines []string) string {
	const old = "lane.critical.panes"
	const target = "lane.builders.panes"
	build := NormalizeKey(target)
	if _, ok := entries[build]; !ok {
		build = "lane_build_panes"
	}
	buildValue, criticalValue := entries[build].Value, entries[NormalizeKey(old)].Value
	if !positiveLaneRE.MatchString(buildValue) || !positiveLaneRE.MatchString(criticalValue) {
		return old + " removed (requires positive explicit capacity in " + target + " to add it)"
	}
	n, nErr := strconv.Atoi(buildValue)
	extra, extraErr := strconv.Atoi(criticalValue)
	if nErr != nil || extraErr != nil || extra > int(^uint(0)>>1)-n {
		return old + " removed (capacity for " + target + " is out of range)"
	}
	i := last[build]
	_, comment := splitTrailingComment(rawLines[i])
	rawLines[i] = entries[build].Original + "=" + strconv.Itoa(n+extra) + comment
	return old + " → " + target + " (added to explicit capacity)"
}
