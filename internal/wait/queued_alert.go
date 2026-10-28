package wait

import (
	"fmt"
	"time"
)

type queuedAlertIdentity struct {
	agent, path string
	epoch       int64
}

// observeQueuedPrompt runs after the probe, which removes a queued marker when
// the native working sequence changes. Output progress does not reset its age.
func observeQueuedPrompt(warned map[queuedAlertIdentity]bool, sd, agent string, now time.Time, minutes int64) {
	if minutes <= 0 {
		return
	}
	marker, ok := readWaitFile(sd, agent, "queued")
	if !ok {
		return
	}
	parts := promptPathMarker.FindStringSubmatch(marker)
	if parts == nil {
		return
	}
	epoch := markerEpoch(marker, now.Unix())
	age := (now.Unix() - epoch) / 60
	identity := queuedAlertIdentity{agent: agent, path: parts[1], epoch: epoch}
	if age < minutes || warned[identity] {
		return
	}
	warned[identity] = true
	hint := ""
	if rosterKind(sd, agent) == "pi" {
		hint = " (pi: Esc, then Enter)"
	}
	sendWarning(fmt.Sprintf("the prompt to '%s' has been queued for %d min while it works; it reads it when its turn ends. If a long tool call holds the turn, interrupt it in its pane%s or wait", agent, age, hint))
}
