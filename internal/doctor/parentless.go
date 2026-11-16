package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// doctorNativeParentless is a read-only, best-effort snapshot of this roster.
func doctorNativeParentless(ctx *core.Config, svc transport.Services, env platform.Env, cwd string, s *Say) {
	if core.TransportMode(ctx, env) != "hc" || !svc.Runtime.Caller().Inside {
		return
	}
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(*platform.ExitError); !ok {
				panic(value)
			}
		}
	}()
	readEnv := env.Clone()
	readEnv["SOHO_NOWRITE"] = "1"
	rows := core.RosterRows(core.StateDirPath(ctx, readEnv, cwd))
	listing := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if listing.Err != nil || !listing.Valid {
		return
	}
	live := make(map[string]transport.Terminal, len(listing.Value))
	for _, terminal := range listing.Value {
		if terminal.NativeState == "live" {
			live[terminal.PaneID] = terminal
		}
	}
	last := make(map[string]int, len(rows))
	for i, row := range rows {
		name, _, _ := strings.Cut(row, "\t")
		last[name] = i
	}
	for i, row := range rows {
		cols := strings.Split(row, "\t")
		if len(cols) < 2 || cols[0] == "" || cols[1] == "" || last[cols[0]] != i {
			continue
		}
		terminal, found := live[cols[1]]
		if found && terminal.ParentKnown && terminal.Parent == "" {
			s.Warning(fmt.Sprintf("native worker '%s' (terminal %s) has no parent terminal: its exit note reaches nobody and it outlives the orchestrator unseen; release it (soho release %s --close) and spawn it again from the orchestrator's terminal", cols[0], cols[1], cols[0]))
		}
	}
}
