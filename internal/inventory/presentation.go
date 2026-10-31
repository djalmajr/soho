package inventory

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// FoldStatus consumes the shared resolver's words after live identity
// matching. A fresh gone probe always omits the candidate; report authority
// never revives a closed terminal. The boolean reports whether to include it.
func FoldStatus(effective, terminal string) (State, *Reason, bool) {
	if effective == "gone" || terminal == "gone" {
		return StateUnknown, nil, false
	}
	var reason Reason
	switch effective {
	case "working", "compacting", "collaborating":
		return StateWorking, nil, true
	case "done":
		return StateDone, nil, true
	case "idle":
		return StateIdle, nil, true
	case "compact-interrupted":
		reason = ReasonCompact
	case "question":
		reason = ReasonQuestion
	case "blocked":
		reason = ReasonApproval
	case "quota":
		reason = ReasonQuota
	case "capacity", "provider-error", "provider-refusal":
		reason = ReasonProvider
	case "not-received":
		reason = ReasonNotReceived
	case "no-report-yet":
		switch terminal {
		case "done":
			reason = ReasonNoReport
		case "idle":
			return StateIdle, nil, true
		default:
			return StateUnknown, nil, true
		}
	default:
		return StateUnknown, nil, true
	}
	return StateNeedsYou, &reason, true
}

// TaskTitle formats only the persisted title metadata. Worker metadata
// carries the current dispatch role prefix, which can differ from its
// spawn role; an orchestrator's literal objective has no such decoration.
func TaskTitle(raw string, worker bool) *string {
	if !utf8.ValidString(raw) {
		return nil
	}
	text := strings.TrimSpace(raw)
	if worker {
		if prefix, rest, ok := strings.Cut(text, ": "); ok && prefix != "" {
			text = rest
		}
		text = strings.TrimSuffix(text, " ✓")
	}
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 120 {
		text = string(runes[:120])
	}
	if text == "" {
		return nil
	}
	return &text
}

// OptionalModel preserves a valid persisted model without resolving defaults.
func OptionalModel(raw string) *string {
	if strings.TrimSpace(raw) == "" || !utf8.ValidString(raw) || strings.ContainsFunc(raw, unicode.IsControl) {
		return nil
	}
	return &raw
}

func OptionalEffort(raw string) *string {
	switch raw {
	case "low", "medium", "high", "xhigh", "max":
		return &raw
	default:
		return nil
	}
}

// ParseStartedMS interprets the historical zone-less spawn stamp only in
// the caller's explicit host location; it never substitutes an observation time.
func ParseStartedMS(raw string, loc *time.Location) (int64, bool) {
	if loc == nil {
		return 0, false
	}
	const layout = "20060102T150405"
	started, err := time.ParseInLocation(layout, raw, loc)
	if err != nil || started.Format(layout) != raw || started.UnixMilli() < 0 {
		return 0, false
	}
	return started.UnixMilli(), true
}
