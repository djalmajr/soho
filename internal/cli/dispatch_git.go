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
	"github.com/djalmajr/soho/internal/taskreport"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

func dispatchGitFields(body, workerCwd, cwd, sd, agent string, amend bool, prior *jsonjs.Object, env platform.Env) *jsonjs.Object {
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
	fields := gittree.Snapshot(env, tree)
	if fields != nil {
		if owned, ok := dispatchPreviousOwned(sd, agent, tree, prior); ok {
			fields.Set("git_previous_owned", owned)
		}
	}
	return fields
}

func dispatchPreviousOwned(sd, agent, tree string, prior *jsonjs.Object) ([]string, bool) {
	if prior == nil {
		return nil, false
	}
	v, _ := prior.Get("current")
	report, _ := v.(string)
	if report == "" {
		return nil, false
	}
	return gittree.PreviousOwned(gittree.Sidecar(sd, report), agent, tree)
}

func dispatchCaptureGit(sidecar, body, workerCwd, cwd, sd, agent string, amend bool, prior *jsonjs.Object, env platform.Env) *jsonjs.Object {
	fields := dispatchGitFields(body, workerCwd, cwd, sd, agent, amend, prior, env)
	gittree.Preserve(sidecar, fields)
	return fields
}

func dispatchLintOptions(body, workerCwd, cwd, sd, agent, role string, ctx *core.Config, env platform.Env) dispatch.BriefLintOptions {
	tree := dispatch.DeclaredWorktree(body)
	if st, err := os.Stat(tree); err != nil || !st.IsDir() {
		tree = workerCwd
	}
	if tree == "" {
		tree = cwd
	}
	_, previous := dispatchPreviousOwned(sd, agent, tree, taskreport.ReadTaskReportPointer(sd, agent))
	return dispatch.BriefLintOptions{ReadOnly: !core.RoleIsEdit(role, env, cwd), WorkerCwd: workerCwd, OrchestratorCwd: cwd, PreviousTask: previous}
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
		gittree.DiffReport(gittree.Sidecar(sd, report), agent, report, env, logFile, "collect")
	}
}
