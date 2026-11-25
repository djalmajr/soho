package spawn

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/startup"
	"github.com/djalmajr/soho/internal/transport"
)

type ompStartupAgent struct {
	Name     string          `json:"name"`
	Pane     string          `json:"pane_id"`
	Kind     string          `json:"agent"`
	Terminal string          `json:"terminal_id"`
	State    string          `json:"agent_status"`
	Sequence json.Number     `json:"state_change_seq"`
	Session  ompAgentSession `json:"agent_session"`
}

type ompAgentSession struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Value  string `json:"value"`
}

func decodeOMPStartupAgent(raw string) ompStartupAgent {
	var reply struct {
		Result struct {
			Agent ompStartupAgent `json:"agent"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(raw), &reply) != nil {
		return ompStartupAgent{}
	}
	return reply.Result.Agent
}

func (agent ompStartupAgent) matches(o spawnOptions) bool {
	return o.pane != "" && agent.Name == o.name && agent.Pane == o.pane && agent.Kind == "omp" && agent.Terminal != ""
}

// observeOMPStart brackets a current, pane-addressed agent viewport with
// liveness checks under the same deadline. The start receipt pins the
// terminal even if its native session has not been reported yet.
func observeOMPStart(o spawnOptions, svc transport.Services, deadline time.Time) (string, string, bool) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	unverified := func(reason string) {
		core.DieFriction(fmt.Sprintf("spawn: could not verify OMP startup for '%s' in pane %s (%s); nothing was registered; the pane stays open for inspection", o.name, o.pane, reason), 4, "", "")
	}
	if o.ompLaunch == nil || !o.ompLaunch.matches(o) {
		unverified("the start did not prove the terminal identity")
	}
	launch := *o.ompLaunch
	target := transport.Target{ID: o.pane}
	waitingForAnimation := false
	expired := func() {
		if waitingForAnimation {
			unverified("the startup animation did not resolve before the startup window expired")
		}
		unverified("the startup window expired")
	}
	call := func() transport.Call {
		left := time.Until(deadline)
		if left <= 0 {
			expired()
		}
		return transport.Call{Timeout: left}
	}
	readAgent := func() ompStartupAgent {
		rep := svc.Legacy.AgentGetRaw(ctx, target, call())
		if !time.Now().Before(deadline) {
			expired()
		}
		if rep.Err != nil || !rep.Valid {
			unverified("the launched agent could not be observed")
		}
		return decodeOMPStartupAgent(rep.Value)
	}
	before := readAgent()
	if !before.matches(o) || before.Terminal != launch.Terminal || (launch.Session.Value != "" && before.Session != launch.Session) || before.Session.Value == "" {
		unverified("the launched terminal and OMP session could not be confirmed")
	}
	switch before.State {
	case "idle", "done", "working", "blocked":
	default:
		unverified("the launched agent was not live")
	}
	checkAgent := func() {
		if readAgent() != before {
			unverified("the agent identity or state changed while observing the pane")
		}
	}
	for {
		screen := svc.Runtime.Read(ctx, target, transport.Read{Source: startup.SourceVisible}, call())
		if !time.Now().Before(deadline) {
			expired()
		}
		if screen.Err != nil || !screen.Valid || (!waitingForAnimation && strings.TrimSpace(screen.Value) == "") {
			unverified("the visible screen could not be verified")
		}
		checkAgent()
		if waitingForAnimation {
			if isOMPSetupWizard(screen.Value) || isOMPStartupComposer(screen.Value) {
				return before.State, screen.Value, false
			}
		} else if !isOMPStartupAnimation(screen.Value) {
			return before.State, screen.Value, false
		}
		waitingForAnimation = true

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		// Recheck after the pause too, before reading the pane again.
		checkAgent()
	}
}

func ompVisibleLines(screen string) []string {
	var lines []string
	for _, line := range strings.Split(screen, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// isOMPStartupAnimation recognizes the full startup splash on its own. The
// paired beams and columns are the OMP logo in the captured animation.
func isOMPStartupAnimation(screen string) bool {
	lines := ompVisibleLines(screen)
	if len(lines) < 5 {
		return false
	}
	art := lines[:len(lines)-1]
	footer := lines[len(art)]
	// Linux renders the skip hint on its own line. macOS insets it in shading.
	if footer != "press ⏎ to skip" {
		left, right, ok := strings.Cut(footer, " press ⏎ to skip ")
		if !ok || strings.Trim(left, " ░") != "" || strings.Trim(right, " ░") != "" ||
			!strings.ContainsRune(left, '░') || !strings.ContainsRune(right, '░') {
			return false
		}
	}
	if !isOMPSetupSplash(art) {
		return false
	}
	const beam = "████████████████████████"
	const columns = "████    ████"
	logo := [...]string{beam, beam, columns, columns}
	for i := 0; i+len(logo) <= len(art); i++ {
		if slices.EqualFunc(art[i:i+len(logo)], logo[:], func(line, want string) bool {
			return strings.Trim(line, " ✦✧·") == want
		}) {
			return true
		}
	}
	return false
}

// isOMPStartupComposer checks the live editor footer, without interpreting
// the model or any text drawn above it.
func isOMPStartupComposer(screen string) bool {
	lines := ompVisibleLines(screen)
	if len(lines) < 2 {
		return false
	}
	status, hint := lines[len(lines)-2], lines[len(lines)-1]
	return strings.HasPrefix(status, "π > ") && strings.HasPrefix(hint, "╰─") &&
		strings.HasSuffix(hint, "⇧⇥ to change thinking effort")
}

// isOMPSetupWizard requires the captured first-run frame, including the
// provider selector and setup footer as the last visible content.
func isOMPSetupWizard(screen string) bool {
	lines := ompVisibleLines(screen)
	brand := slices.Index(lines, "omp")
	if brand < 0 || len(lines)-brand < 5 || lines[brand+1] != "Setup step 1 of 6" || lines[brand+2] != "Sign in to your providers" {
		return false
	}
	for _, line := range lines[:brand] {
		if strings.Trim(line, " █▒") != "" {
			return false
		}
	}
	pickerStart := brand + 3
	footer := len(lines) - 1
	picker := slices.IndexFunc(lines[pickerStart:footer], func(line string) bool {
		return strings.HasPrefix(line, "╭─ Select provider to login ") && strings.HasSuffix(line, "╮")
	})
	if picker < 0 {
		return false
	}
	switch lines[footer] {
	case "↑/↓ select · ⏎ confirm · ⎋ skip · ⌃C exit setup", "↑/↓ select · ⏎ confirm · ⎋ skip · Ctrl+C exit setup":
		return true
	case "press ⏎ to skip":
		body := lines[pickerStart+picker+1 : footer]
		// A partially cleared Linux overlay can leave the selected provider
		// and ordinary provider rows visible above the artwork.
		if len(body) > 0 && strings.HasPrefix(body[0], "│ ❯ ") && strings.HasSuffix(body[0], " █ │") {
			body = body[1:]
			for len(body) > 0 && strings.HasPrefix(body[0], "│   ") && strings.HasSuffix(body[0], " │ │") {
				body = body[1:]
			}
		}
		return isOMPSetupSplash(body)
	default:
		return false
	}
}

// isOMPSetupSplash recognizes artwork made of shaded blocks and sparkles.
func isOMPSetupSplash(lines []string) bool {
	var shading, sparkle bool
	for _, line := range lines {
		if strings.Trim(line, " ░▒▓█✦✧·") != "" {
			return false
		}
		shading = shading || strings.ContainsAny(line, "░▒▓")
		sparkle = sparkle || strings.ContainsAny(line, "✦✧")
	}
	return shading && sparkle
}
