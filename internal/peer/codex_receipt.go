package peer

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/sessionref"
	"github.com/djalmajr/soho/internal/transcript"
	"github.com/djalmajr/soho/internal/transport"
)

func newCodexReceipt(target resolvedTarget, info agentInfo, closing string, env platform.Env) *transcript.CodexReceipt {
	if env.Get("SOHO_TRANSPORT") == "hc" || target.Machine != sessionref.LocalMachine || target.Kind != "codex" || !codexBinding(info) || info.PaneID != target.TargetArg {
		return nil
	}
	home := env.Get("CODEX_HOME")
	if home == "" {
		home = env.Get("HOME")
		if home == "" {
			home = env.Get("USERPROFILE")
		}
		if home == "" {
			return nil
		}
		home = filepath.Join(home, ".codex")
	}
	return transcript.NewCodexReceipt(home, info.SessionValue, closing)
}

func codexBinding(info agentInfo) bool {
	supportedState := info.Status == "idle" || info.Status == "done" || info.Status == "working" || info.Status == "blocked"
	return info.OK && supportedState && info.PaneID != "" && info.Kind == "codex" && info.SessionAgent == "codex" && info.SessionKind == "id" && transcript.CanonicalCodexUUID(info.SessionValue)
}

func sameCodexBinding(a, b agentInfo) bool {
	return codexBinding(a) && codexBinding(b) && a.PaneID == b.PaneID && a.Kind == b.Kind && a.SessionAgent == b.SessionAgent && a.SessionKind == b.SessionKind && a.SessionValue == b.SessionValue
}

func codexReceiptTaken(reader *transcript.CodexReceipt, before agentInfo, svc transport.Services, target resolvedTarget, visible, id, status string, recentLines int, deadline time.Time, read func(string, int, int) (string, string)) bool {
	if reader == nil || !positiveScreenClear(target.Kind, visible, id, status) || !codexReceiptFrame(visible, id) {
		return false
	}
	// Each transport call gets only the caller's then-remaining window, with
	// the same one-second floor as the other stalled proof calls.
	callBudget := func() int { return floorCallBudget(int(time.Until(deadline).Milliseconds())) }
	return reader.Receipt(func() bool {
		after := agentGet(svc, target.Machine, target.TargetArg, callBudget())
		if !sameCodexBinding(before, after) {
			return false
		}
		// A receiver refusal may have scrolled off the visible frame.
		_, _ = read("recent-unwrapped", recentLines, callBudget())
		// Re-read the visible frame at the binding boundary too: a queue or
		// refusal can appear while the journal is being verified.
		frame, cause := read("visible", 0, callBudget())
		return cause == "" && positiveScreenClear(target.Kind, frame, id, after.Status) && codexReceiptFrame(frame, id)
	})
}

// All positive paths share these queue/composer/dialog vetoes. Existing
// Claude/Pi transcript formats keep their own recognition policy.
func positiveScreenClear(kind, visible, id, status string) bool {
	if isDialogScreen(visible, kind, status) || idInInputBox(kind, visible, id) || steeringQueued(visible, id) || claudeQueued(visible, id) || codexQueued(visible, id) {
		return false
	}
	// For legacy state/screen proofs, historical text outside an unrecognized
	// composer is not pending input. Codex retains its conservative whole-frame
	// veto and supported-renderer checks, including composer-with-history cases.
	if _, recognized := provider.ComposerRegion(kind, visible); (kind == "codex" || recognized) && messageStillInScreen(kind, visible, PeerEndLine(id), id) {
		return false
	}
	if kind == "codex" {
		// Existing recognized delivered-history frames remain valid for
		// independent screen proof even when the composer scrolled away.
		if _, ok := provider.ComposerRegion(kind, visible); !ok && !peerMessageInHistory(visible, id) {
			return false
		}
		for _, line := range strings.Split(visible, "\n") {
			head := strings.TrimSpace(line)
			lower := strings.ToLower(head)
			own := strings.Contains(head, PeerOpenTag(id))
			arrow := false
			for _, c := range head {
				arrow = c >= 0x2190 && c <= 0x21ff || c >= 0x27f0 && c <= 0x27ff || c >= 0x2900 && c <= 0x297f
				break
			}
			if strings.HasPrefix(head, "↳") || own && (arrow || strings.Contains(lower, "queue") || strings.Contains(lower, "pending") || strings.Contains(lower, "follow-up")) {
				return false
			}
		}
	}
	return true
}

// The additional journal proof supports the observed renderer envelope.
// Merely finding a cursor glyph in an otherwise unknown frame is insufficient.
func codexReceiptFrame(visible, id string) bool {
	_, ok := provider.ComposerRegion("codex", visible)
	// A visible own marker with an unfamiliar queue rendering cannot be
	// promoted by the journal. Independent recognized history proof remains.
	return ok && strings.Contains(visible, "• Working (") && strings.Contains(visible, "GPT-") &&
		!strings.Contains(visible, PeerOpenTag(id)) && !strings.Contains(visible, PeerEndLine(id)) &&
		!strings.Contains(strings.ToLower(visible), "pending") && !strings.Contains(strings.ToLower(visible), "queue") && !strings.Contains(strings.ToLower(visible), "follow-up")
}
