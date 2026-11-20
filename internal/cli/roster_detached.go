package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/hcbackend"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	herdrtext "github.com/djalmajr/soho/internal/text"
)

// detachedHcTimeout bounds each read-only hc get of a roster row's address.
const detachedHcTimeout = 2 * time.Second

// runDetachedRoster decides from the invocation shape and the caller's
// transport identity whether roster --json takes the read-only detached path,
// and prints it when it does. Under hc a caller without HC_TERMINAL_ID has no
// terminal; under herdr a caller without HERDR_ENV is outside Herdr. Every
// other shape keeps the existing command and its refusals. The configuration
// comes from core's read-only seam: no caller lookup, no transport call, no
// skill materialization and no state write, including on the decision itself.
func runDetachedRoster(argv []string, env platform.Env, cwd string) bool {
	_, jsonMode, ok := rosterArgs(argv)
	if !ok || !jsonMode {
		return false
	}
	cfg, err := core.LoadReadOnlyConfig(env, platform.StateProjectRoot(env, cwd), "", core.UserSelection{})
	if err != nil {
		return false
	}
	var detached bool
	if core.TransportMode(&cfg.Config, env) == "hc" {
		detached = env.Get("HC_TERMINAL_ID") == ""
	} else {
		detached = env.Get("HERDR_ENV") != "1"
	}
	if !detached {
		return false
	}
	cmdRosterDetached(cfg, env)
	return true
}

// cmdRosterDetached prints the state-root-wide, read-only roster. It uses the
// already resolved state root and never resolves a caller, initializes state,
// logs friction or calls a worker transport for the herdr rows.
func cmdRosterDetached(cfg core.ReadOnlyConfig, env platform.Env) int {
	ctx := &cfg.Config
	agents := detachedRosterAgents(cfg.StateRoot, env)
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O(
		"workspace", "",
		"scope", "server",
		"settings", jsonjs.O(
			"layout", core.Cfg(ctx, "layout", "split", env),
			"reuse_workers", core.Cfg(ctx, "reuse_workers", "on", env),
			"multi_role", core.Cfg(ctx, "multi_role", "on", env),
			"auto_approve", core.Cfg(ctx, "auto_approve", "off", env),
		),
		"agents", agents,
		"others", []any{},
		"detached", true,
		"state_root", cfg.StateRoot,
	)))
	return 0
}

// detachedRosterAgents reads every workspace directory under root, in readdir
// order, and projects each roster row into the panel's agent fields with an
// added workspace field. A missing root, an empty root, a workspace without a
// roster and a row without an address are all reads of what exists, not
// errors: nothing is created to make them present.
func detachedRosterAgents(root string, env platform.Env) []any {
	agents := []any{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return agents
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workspace := entry.Name()
		dir := filepath.Join(root, workspace)
		for _, line := range core.RosterRows(dir) {
			fields := strings.Split(line, "\t")
			col := func(i int) string {
				if i < len(fields) {
					return fields[i]
				}
				return ""
			}
			name, address, kind, role := col(0), col(1), col(2), col(3)
			if name == "" {
				continue
			}
			status := "unknown"
			if detachedHCAddress(address) {
				if state, ok := detachedHCStatus(env, address); ok && state != "" {
					status = state
				}
			}
			// The panel's report and task fields keep the in-terminal rules:
			// a publishable path is a regular file with content, and no
			// report path is JSON null rather than an empty string.
			report := "none"
			var reportPath any
			if path := core.LastReport(dir, name); path != "" {
				if info, serr := os.Stat(path); serr == nil && info.Size() > 0 {
					report = "ready"
					if info.Mode().IsRegular() {
						reportPath = path
					}
				} else {
					report = "pending"
				}
			}
			task := ""
			if raw, rerr := os.ReadFile(filepath.Join(dir, "task-"+name)); rerr == nil {
				if text := strings.TrimRight(herdrtext.ToWellFormedUTF8(string(raw)), "\n"); text != "" {
					task = text
				}
			}
			agents = append(agents, jsonjs.O(
				"name", name, "role", role, "previous_role", detachedPreviousRole(col(10), role),
				"kind", kind, "pane", address, "tab", "", "state", "unknown",
				"report", report, "cwd", col(6), "task", task,
				"model", col(8), "effort", col(14), "status", status,
				"report_path", reportPath, "task_s", nil, "activity_s", nil,
				"workspace", workspace,
			))
		}
	}
	return agents
}

func detachedPreviousRole(history, role string) string {
	if history != "" && history != role {
		return history
	}
	return ""
}

// detachedHCAddress reports whether a roster address names an hc terminal.
// Herdr pane ids carry a workspace prefix and a colon ("ws:p1"); an hc
// address is the single terminal token ("t7", "machine/t7") the client hands
// to hc as one argv element. Addresses with control characters are not valid
// argv elements and stay unknown.
func detachedHCAddress(address string) bool {
	return address != "" && !strings.Contains(address, ":") && !strings.ContainsAny(address, "\x00\r\n")
}

// detachedHCStatus performs the path's one read-only get for a row and maps
// its record with the in-terminal roster's own stateOf rule, exposed as
// hcbackend.StatusOfRecord: finished (or a not-found get) is gone, a live
// record's reported status passes through, and a live record without one is
// unknown. Another failed get, a timeout, a missing hc and an unparseable
// answer leave the row's status unknown without mapping the record.
func detachedHCStatus(env platform.Env, address string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), detachedHcTimeout)
	defer cancel()
	raw, err := hc.New(env).Run(ctx, "get", address)
	if err != nil {
		var hcErr *hc.Error
		if errors.As(err, &hcErr) && hcErr.Exit == hc.ExitNotFound {
			return "gone", true
		}
		return "", false
	}
	value, err := jsonjs.Parse([]byte(raw))
	if err != nil {
		return "", false
	}
	record, ok := value.(*jsonjs.Object)
	if !ok {
		return "", false
	}
	return hcbackend.StatusOfRecord(record), true
}
