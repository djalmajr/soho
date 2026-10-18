package core

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

// Footprint is one read-only measurement of a team's footprint: the
// registered copies that still exist on disk (how many, and their bytes),
// the state dir's bytes (a registered copy inside it kept out, so it
// counts once, as a copy), the workspace scratch dir's bytes and their
// total, and the registered processes that read as running (how many, the
// RSS summed in bytes, and how many of them could not be read).
type Footprint struct {
	Copies       int
	CopiesBytes  int64
	StateBytes   int64
	ScratchBytes int64
	Total        int64
	Running      int
	RSS          int64
	Unreadable   int
}

// DirSize walks a directory and sums the sizes of its regular files
// without following symlinks (an unreadable entry is simply not counted).
// A missing root reads as zero. The walk is shared by the copy-size
// measure of internal/cli and by the footprint measure.
func DirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// HumanSize formats a byte count the way the copy and gc reports do:
// plain bytes below 1 KiB, one decimal at KB, MB, GB or TB above (1 GB is
// 1024^3 bytes, the same base).
func HumanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	value := float64(n)
	for _, unit := range []struct {
		size float64
		name string
	}{
		{1 << 40, "TB"},
		{1 << 30, "GB"},
		{1 << 20, "MB"},
	} {
		if value >= unit.size {
			return strconv.FormatFloat(value/unit.size, 'f', 1, 64) + " " + unit.name
		}
	}
	return strconv.FormatFloat(value/1024, 'f', 1, 64) + " KB"
}

// FootprintLimitReached reports whether a disk total reaches a
// footprint_warn_gb limit (1 GB = 1024^3 bytes). A limit at or below 0 is
// off, and a limit too large to express in bytes is never reached (no
// overflow into a negative bound).
func FootprintLimitReached(total, limitGB int64) bool {
	if limitGB <= 0 || limitGB > math.MaxInt64>>30 {
		return false
	}
	return total >= limitGB<<30
}

// FootprintDiskText formats the on-disk part of a footprint in the shared
// human sizes: "<total> on disk (copies <copies> in <n>, state <state>,
// scratch <scratch>)". The explain line and the doctor warning build on it
// so the sizes are formatted in exactly one place.
func FootprintDiskText(fp *Footprint) string {
	return HumanSize(fp.Total) + " on disk (copies " + HumanSize(fp.CopiesBytes) + " in " +
		strconv.Itoa(fp.Copies) + ", state " + HumanSize(fp.StateBytes) + ", scratch " +
		HumanSize(fp.ScratchBytes) + ")"
}

// readRSS is the by-pid RSS read (the bytes the process has resident now)
// behind the footprint measure: a seam the tests point at a failure to
// cover the unreadable path.
var readRSS = platform.ReadProcessRSS

// MeasureFootprint measures a workspace's footprint from its state dir,
// read-only (it never writes to the registries or anywhere else):
//
//   - copies: the registry lines whose path exists as a directory, counted
//     and sized with the same walk as the copy measure (a vanished line
//     counts nothing);
//   - state: the state dir's bytes, with the registered copies inside it
//     kept out (such a copy counts once, as a copy); an empty stateDir
//     measures zero and reads no registry;
//   - scratch: the bytes of <TMPDIR (or os.TempDir() without TMPDIR)>/
//     soho/<workspaceID>, the workspace scratch the dispatch writes; an
//     empty workspaceID measures zero;
//   - processes: the registry lines that read as running by the same
//     identity check as `soho procs` (pid plus start time), counted, with
//     the RSS read now and summed (an unreadable RSS counts as unreadable,
//     and the process still counts). No process command line is read.
func MeasureFootprint(stateDir, workspaceID string, env platform.Env) *Footprint {
	fp := &Footprint{}
	if stateDir == "" {
		return fp
	}
	skip := map[string]bool{}
	if rows, err := ReadCopies(stateDir); err == nil {
		for _, row := range rows {
			if info, e := os.Stat(row.Path); e != nil || !info.IsDir() {
				continue
			}
			fp.Copies++
			fp.CopiesBytes += DirSize(row.Path)
			if rel, e := filepath.Rel(stateDir, row.Path); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				skip[filepath.Clean(row.Path)] = true
			}
		}
	}
	var stateTotal int64
	_ = filepath.WalkDir(stateDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if skip[filepath.Clean(path)] {
			return filepath.SkipDir
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			stateTotal += info.Size()
		}
		return nil
	})
	fp.StateBytes = stateTotal
	if workspaceID != "" {
		tmp := env.Get("TMPDIR")
		if tmp == "" {
			tmp = os.TempDir()
		}
		fp.ScratchBytes = DirSize(filepath.Join(tmp, "soho", workspaceID))
	}
	fp.Total = fp.CopiesBytes + fp.StateBytes + fp.ScratchBytes
	if rows, err := ReadProcs(stateDir); err == nil {
		for _, row := range rows {
			if ProcRowState(row, env) != "running" {
				continue
			}
			fp.Running++
			rss, ok := readRSS(row.Pid, env)
			if !ok {
				fp.Unreadable++
				continue
			}
			fp.RSS += rss
		}
	}
	return fp
}
