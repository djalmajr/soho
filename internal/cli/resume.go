package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

const restartResumeNote = "The machine restarted while you worked on this task: your processes died and /tmp was wiped. Do not revert the worktree. Re-create any temporary artifacts you need, check what is already done, and continue the same task. The original brief is %s; write your report to the report path this message gives."

type resumeResult struct {
	Agent  string `json:"agent"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func cmdResume(args []string, ctx *core.Config, env platform.Env, cwd string) int {
	all, dry := false, false
	names := []string{}
	for _, arg := range args {
		switch arg {
		case "--all":
			all = true
		case "--dry-run":
			dry = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintln(platform.Stderr, "soho: resume: unknown option", arg)
				return 2
			}
			names = append(names, arg)
		}
	}
	if !all && len(names) == 0 {
		fmt.Fprintln(platform.Stderr, "soho: usage: resume [<agent>…] [--all] [--dry-run]")
		return 2
	}
	results := []resumeResult{}
	emit := func() { _ = json.NewEncoder(platform.Stdout).Encode(results) }
	writeFailure := func(agent string, err error) int {
		fmt.Fprintln(platform.Stderr, "soho: resume:", err)
		results = append(results, resumeResult{agent, "skipped", err.Error()})
		emit()
		return 4
	}
	boot, err := platform.BootTime()
	if err != nil || boot.IsZero() {
		fmt.Fprintln(platform.Stderr, "soho: resume: machine boot time is unknown")
		emit()
		return 4
	}
	if boot.After(platform.Now()) {
		fmt.Fprintln(platform.Stderr, "soho: resume: the boot time is inconsistent with the current clock; nothing was sent")
		emit()
		return 4
	}
	sd := core.StateDirPath(ctx, env, cwd)
	if all {
		for _, line := range core.RosterRows(sd) {
			if !strings.HasPrefix(line, "#") {
				names = append(names, strings.Split(line, "\t")[0])
			}
		}
	}
	seen := map[string]bool{}
	code := 0
	for _, agent := range names {
		if seen[agent] || agent == "" {
			continue
		}
		seen[agent] = true
		task := waitpkg.InspectRestartTask(agent, sd, boot, env)
		if task.Code != 0 {
			results = append(results, resumeResult{agent, "skipped", task.Reason})
			fmt.Fprintf(platform.Stderr, "soho: resume: '%s': %s\n", agent, task.Reason)
			if (task.Code == 4 || !all) && code == 0 {
				code = task.Code
			}
			continue
		}
		note := fmt.Sprintf(restartResumeNote, task.Brief)
		if dry {
			results = append(results, resumeResult{agent, "would-resume", note})
			continue
		}
		dir := filepath.Join(sd, "briefs")
		if err = os.MkdirAll(dir, 0700); err != nil {
			return writeFailure(agent, err)
		}
		f, err := os.CreateTemp(dir, agent+"-resume-*.md")
		if err != nil {
			return writeFailure(agent, err)
		}
		path := f.Name()
		_, err = f.WriteString(note + "\n")
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return writeFailure(agent, err)
		}
		sent := dispatchResumeAmendment(agent, path, ctx, env, cwd)
		if sent != 0 {
			status := "skipped"
			if sent == 15 {
				status = "not-received"
			}
			results = append(results, resumeResult{agent, status, fmt.Sprintf("amendment failed (exit %d)", sent)})
			if code == 0 {
				code = sent
			}
			continue
		}
		results = append(results, resumeResult{agent, "resumed", task.Reason})
	}
	emit()
	return code
}
func dispatchResumeAmendment(agent, brief string, ctx *core.Config, env platform.Env, cwd string) (code int) {
	previous := platform.Stdout
	var captured bytes.Buffer
	platform.Stdout = &captured
	defer func() {
		platform.Stdout = previous
		if value := recover(); value != nil {
			if exit, ok := value.(*platform.ExitError); ok {
				code = exit.Code
				fmt.Fprintln(platform.Stderr, "soho:", exit.Msg)
			} else {
				panic(value)
			}
		}
	}()
	return cmdDispatchWithOptions([]string{agent, brief, "--amend", "--no-wait"}, ctx, env, cwd, dispatchOptions{noResend: true})
}
