package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

var cleanDaysPattern = regexp.MustCompile(`^[0-9]+$`)

func cmdClean(argv []string, ctx *core.Config, env platform.Env, cwd string) {
	days := "7"
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--older-than" {
			if i+1 >= len(argv) {
				core.DieFriction("clean: --older-than expects a value", 2, frictionLogPath, "clean")
			}
			days = argv[i+1]
			i++
		} else {
			core.DieFriction("clean: unknown option "+argv[i], 2, frictionLogPath, "clean")
		}
	}
	sd := core.StateDir(ctx, env, cwd)
	live := liveAgentsFriction(env)
	waitDir := filepath.Join(sd, "wait")
	waitEntries, err := os.ReadDir(waitDir)
	if err != nil {
		core.DieFriction(fmt.Sprintf("clean: could not read %s (%v)", waitDir, err), 4, frictionLogPath, "clean")
	}
	for _, line := range core.RosterRows(sd) {
		f := strings.Split(line, "\t")
		name, pane := f[0], ""
		if len(f) > 1 {
			pane = f[1]
		}
		if name == "" {
			continue
		}
		alive := false
		for _, agent := range live {
			if strictEqual(field(agent, "name"), name) || strictEqual(field(agent, "pane_id"), pane) {
				alive = true
				break
			}
		}
		if alive {
			continue
		}
		core.RosterRemove(sd, name)
		_ = os.Remove(core.LastReportPath(sd, name))
		for _, entry := range waitEntries {
			if strings.HasPrefix(entry.Name(), name+".") {
				_ = os.Remove(filepath.Join(waitDir, entry.Name()))
			}
		}
		fmt.Fprintf(platform.Stdout, "dropped gone agent %s\n", name)
	}
	keep := ""
	entries, _ := os.ReadDir(sd)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "last-report-") {
			continue
		}
		if raw, err := os.ReadFile(filepath.Join(sd, entry.Name())); err == nil {
			keep += string(raw)
		}
	}
	daysValid := cleanDaysPattern.MatchString(days)
	threshold, parseErr := parseCleanDays(days)
	if parseErr != nil {
		daysValid = false
	}
	removed := 0
	for _, dir := range []string{"briefs", "reports"} {
		root := filepath.Join(sd, dir)
		_ = filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() || strings.HasSuffix(entry.Name(), ".current.md") {
				return nil
			}
			if strings.HasSuffix(file, ".dispatch.json.git-checked") {
				if _, err := os.Lstat(strings.TrimSuffix(file, ".git-checked")); err == nil || !os.IsNotExist(err) {
					return nil // The marker shares its sidecar's lifetime.
				}
			}
			info, statErr := entry.Info()
			if statErr != nil {
				return nil
			}
			if !daysValid || int64(platform.Now().Sub(info.ModTime())/(24*time.Hour)) <= threshold || strings.Contains(keep, file) {
				return nil
			}
			if os.Remove(file) == nil {
				removed++
				if strings.HasSuffix(file, ".dispatch.json") && os.Remove(file+".git-checked") == nil {
					removed++
				}
			}
			return nil
		})
	}
	fmt.Fprintf(platform.Stdout, "removed %d files older than %s days under %s\n", removed, days, sd)
}

func parseCleanDays(value string) (int64, error) {
	if !cleanDaysPattern.MatchString(value) {
		return 0, fmt.Errorf("invalid day count")
	}
	var n int64
	for _, r := range value {
		n = n*10 + int64(r-'0')
		if n > 1<<60 {
			return 0, fmt.Errorf("day count too large")
		}
	}
	return n, nil
}
