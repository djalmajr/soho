package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

// doctorDoneOpen observes the current task through the same read-only status
// resolver as status. A report's mtime is the age status uses for completion;
// status itself does not persist a settlement timestamp. The listing proves
// the roster identity is still live, independently of report authority.
func doctorDoneOpen(ctx *core.Config, svc transport.Services, env platform.Env, cwd string, s *Say) {
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(*platform.ExitError); !ok {
				panic(value)
			}
		}
	}()
	caller := svc.Runtime.Caller()
	if !caller.Inside || caller.PaneID == "" {
		return
	}
	readEnv := env.Clone()
	readEnv["SOHO_NOWRITE"] = "1"
	session := core.WorkspaceID(ctx, readEnv, cwd)
	scratch := readEnv.Get("TMPDIR")
	if scratch == "" {
		scratch = os.TempDir()
	}
	source, err := core.OpenSessionSource(core.StateRootPath(ctx, readEnv, cwd), session, scratch)
	if err != nil {
		return
	}
	defer source.Close()
	rows := source.Roster().Rows
	if len(rows) == 0 {
		return
	}
	listing := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{Timeout: herdr.Timeout})
	if listing.Err != nil || !listing.Valid {
		return
	}
	live := map[string]bool{}
	for _, terminal := range listing.Value {
		if terminal.Name != "" && terminal.PaneID != "" && terminal.State != "gone" && terminal.State != "unavailable" && terminal.NativeState != "finished" {
			live[terminal.Name+"\x00"+terminal.PaneID] = true
		}
	}
	seen := map[string]bool{}
	now := platform.Now()
	for _, row := range rows {
		if !row.Valid {
			continue
		}
		name, pane := row.Fields[0], row.Fields[1]
		if seen[name] || pane == caller.PaneID || !live[name+"\x00"+pane] || !platform.ValidStateComponent(name) {
			continue
		}
		// Roster lookup selects the last row, not an earlier stale identity.
		current, ok := source.Roster().Row(name)
		if !ok || len(current.Fields) < 2 || current.Fields[1] != pane {
			continue
		}
		seen[name] = true
		pointer, err := source.ReadMetadata("last-report-" + name)
		if err != nil {
			continue
		}
		report := strings.TrimRight(pointer, "\n")
		info, err := source.ObserveReport(report)
		if root, approved := source.ApprovedReportRoot(report); approved && root == "scratch" && errors.Is(err, os.ErrNotExist) {
			report = source.StateReportMirror(report, filepath.Base(report))
			info, err = source.ObserveReport(report)
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			continue
		}
		age := now.Sub(info.ModTime())
		if age <= 30*time.Minute {
			continue
		}
		view := waitpkg.InspectAgentStatus(waitpkg.AgentStatusRequest{
			StateDir: source.StateDir(), SessionID: session, Agent: name, Cwd: cwd,
			Config: ctx, Env: readEnv, Services: svc, Source: source,
		})
		if view.State != "done" || !view.ReportReady || view.TerminalState == "unavailable" || view.TerminalState == "gone" {
			continue
		}
		s.Warning(fmt.Sprintf("worker '%s' finished its task %d min ago and is still open: release it (soho release %s --close), or keep it with a reason you state to the user", name, int64(age/time.Minute), name))
	}
}
