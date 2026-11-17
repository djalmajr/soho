package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

var doctorCopiesSize = core.CopiesDirSize
var doctorCopiesSizeTimeout = 5 * time.Second

func doctorCopiesPressure(ctx *core.Config, env platform.Env, cwd string, s *Say) {
	workspace := backend.New(env).Runtime.Caller().WorkspaceID
	stateDir := filepath.Join(core.StateRootPath(ctx, env, cwd), workspace)
	part, parent := core.CopiesWarningPressure(ctx, env, stateDir)
	if part == "" {
		return
	}
	s.Warning(part + "; 'soho gc --include-unregistered' removes this project's")
	if workspace == "" {
		return
	}
	rows, err := core.ReadCopies(stateDir)
	if err != nil {
		return
	}
	entries := core.UnregisteredCopies(core.CopiesScanDirs(env, cwd), rows, time.Now(), -time.Duration(1<<63-1))
	parentDevice, parentOK := core.CopiesDevice(parent)
	// Determine the same git common-directory identity stored in origin markers.
	result := platform.RunCli("git", []string{"-C", cwd, "rev-parse", "--path-format=absolute", "--git-common-dir"}, platform.RunOptions{Env: env, Cwd: cwd, TimeoutMs: 2000})
	project := ""
	if result.Status != nil && *result.Status == 0 {
		project, _ = filepath.EvalSymlinks(strings.TrimSpace(result.Stdout))
	}
	type item struct {
		path, project string
		size          int64
		known         bool
	}
	items := make([]item, 0, len(entries))
	deadline, cancel := context.WithTimeout(context.Background(), doctorCopiesSizeTimeout)
	defer cancel()
	for _, entry := range entries {
		device, ok := platform.DiskDevice(entry.Path)
		if !parentOK || !ok || device != parentDevice {
			continue
		}
		label := "other project"
		if marker, _, ok := core.ReadCopyOriginMarker(entry.Path + ".soho-origin"); ok && project != "" && (marker == project || (platform.Current() == "win32" && strings.EqualFold(marker, project))) {
			label = "this project"
		}
		if deadline.Err() != nil {
			items = append(items, item{path: entry.Path, project: label})
			continue
		}
		ch := make(chan struct {
			n  int64
			ok bool
		}, 1)
		measure := doctorCopiesSize
		go func(path string) {
			n, ok := measure(deadline, path)
			ch <- struct {
				n  int64
				ok bool
			}{n, ok}
		}(entry.Path)
		row := item{path: entry.Path, project: label}
		select {
		case size := <-ch:
			row.size, row.known = size.n, size.ok
		case <-deadline.Done():
		}
		items = append(items, row)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].known != items[j].known {
			return items[i].known
		}
		return items[i].size > items[j].size
	})
	for i, row := range items {
		if i == 5 {
			break
		}
		size := "size unknown"
		if row.known {
			size = platform.HumanSize(row.size)
		}
		fmt.Fprintf(s.Out, "  %s  %s  %s\n", size, row.path, row.project)
	}
}
