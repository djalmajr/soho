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
	call := func() transport.Call {
		left := time.Until(deadline)
		if left <= 0 {
			unverified("the startup window expired")
		}
		return transport.Call{Timeout: left}
	}
	readAgent := func() ompStartupAgent {
		rep := svc.Legacy.AgentGetRaw(ctx, target, call())
		if !time.Now().Before(deadline) || rep.Err != nil || !rep.Valid {
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
	screen := svc.Runtime.Read(ctx, target, transport.Read{Source: startup.SourceVisible}, call())
	if !time.Now().Before(deadline) || screen.Err != nil || !screen.Valid || strings.TrimSpace(screen.Value) == "" {
		unverified("the visible screen could not be verified")
	}
	after := readAgent()
	if after != before {
		unverified("the agent identity or state changed while observing the pane")
	}
	return after.State, screen.Value, false
}

// isOMPSetupWizard requires the captured first-run frame, including the
// provider selector and setup footer as the last visible content.
func isOMPSetupWizard(screen string) bool {
	var lines []string
	for _, line := range strings.Split(screen, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
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
	case "↑/↓ select · ⏎ confirm · ⎋ skip · ⌃C exit setup":
		return true
	case "press ⏎ to skip":
		return isOMPSetupSplash(lines[pickerStart+picker+1 : footer])
	default:
		return false
	}
}

// The observed Linux splash covers the picker rows with shaded blocks and
// sparkles. Ordinary text between the picker and the skip footer breaks it.
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
