package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

func dispatchGitFields(body, workerCwd, cwd, sd string, amend bool, prior *jsonjs.Object, env platform.Env) *jsonjs.Object {
	if amend {
		if prior != nil {
			if v, ok := prior.Get("current"); ok {
				if report, ok := v.(string); ok && report != "" {
					return gittree.Fields(gittree.Sidecar(sd, report))
				}
			}
		}
		return nil
	}
	tree := dispatch.DeclaredWorktree(body)
	if st, err := os.Stat(tree); err != nil || !st.IsDir() {
		tree = workerCwd
	}
	if tree == "" {
		tree = cwd
	}
	return gittree.Snapshot(env, tree)
}

// collectGitCheck shares wait's comparison while keeping collect's report body
// output and active-collaboration/read-only contracts unchanged.
func collectGitCheck(args []string, ctx *core.Config, env platform.Env, cwd, logFile string) {
	if len(args) == 0 || core.Nowrite(env) {
		return
	}
	agent := args[0]
	sd := core.StateDir(ctx, env, cwd)
	if line := core.RosterLine(sd, agent); line != "" {
		pane := ""
		if fields := strings.Split(line, "\t"); len(fields) > 1 {
			pane = fields[1]
		}
		if _, active := waitpkg.CollaborationViewForPane(sd, pane, ctx, waitpkg.DerivedWorkspaceEnv(ctx, env, cwd)); active {
			return
		}
	}
	report := core.LastReport(sd, agent)
	if report == "" {
		return
	}
	if _, err := os.Stat(report); err != nil {
		report = filepath.Join(sd, "reports", filepath.Base(report))
	}
	if st, err := os.Stat(report); err == nil && st.Size() > 0 {
		gittree.Check(gittree.Sidecar(sd, report), agent, env, logFile, "collect")
	}
}
