//go:build linux

package platform

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// linuxUserHZ is the clock-tick unit of /proc/<pid>/stat's starttime: the
// kernel exports it as USER_HZ, fixed at 100 for user space.
const linuxUserHZ = 100

func userOrphanProcesses() ([]OrphanProc, error) {
	dir, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	bootID := strings.TrimSpace(string(boot))
	if err != nil || bootID == "" {
		return nil, os.ErrNotExist
	}
	btime := linuxBootTime()
	entries := []orphanEntry{}
	for _, item := range dir {
		pid, err := strconv.Atoi(item.Name())
		if err != nil || pid <= 0 {
			continue
		}
		entry, ok := linuxProcEntry(pid, bootID, btime)
		if !ok {
			continue // exited or unreadable meanwhile: left out
		}
		entries = append(entries, entry)
	}
	uid := os.Geteuid()
	reapers := map[int]bool{}
	isReaper := func(ppid int) bool {
		if ppid == 1 {
			return true
		}
		known, seen := reapers[ppid]
		if !seen {
			known = linuxProcUID(ppid) == uid && linuxProcComm(ppid) == "systemd"
			reapers[ppid] = known
		}
		return known
	}
	out := []OrphanProc{}
	for _, entry := range selectOrphans(entries, uid, os.Getpid(), isReaper) {
		cwd, err := procCwd(entry.pid)
		if err != nil || cwd == "" {
			continue
		}
		out = append(out, OrphanProc{Pid: entry.pid, Identity: entry.identity, Name: entry.name, Cwd: cwd, Started: entry.started})
	}
	return out, nil
}

// linuxProcEntry reads one /proc/<pid>: the owner uid of the directory,
// and from stat the name, the state, the ppid and the start time; the
// identity is built exactly as procStartIdentity builds it. A zombie is
// left out.
func linuxProcEntry(pid int, bootID string, btime int64) (orphanEntry, bool) {
	uid := linuxProcUID(pid)
	if uid < 0 {
		return orphanEntry{}, false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return orphanEntry{}, false
	}
	text := string(data)
	open, close := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if open < 0 || close < open {
		return orphanEntry{}, false
	}
	name := text[open+1 : close]
	rest := strings.Fields(text[close+1:])
	// rest[0] is field 3 (state), rest[1] field 4 (ppid), rest[19] field 22
	// (starttime).
	if len(rest) < 20 || name == "" {
		return orphanEntry{}, false
	}
	if rest[0] == "Z" || rest[0] == "X" || rest[0] == "x" {
		return orphanEntry{}, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return orphanEntry{}, false
	}
	ticks, err := strconv.ParseUint(rest[19], 10, 64)
	if err != nil {
		return orphanEntry{}, false
	}
	started := time.Time{}
	if btime > 0 {
		started = time.Unix(btime+int64(ticks/linuxUserHZ), int64(ticks%linuxUserHZ)*int64(time.Second/linuxUserHZ))
	}
	return orphanEntry{pid: pid, ppid: ppid, uid: uid, identity: "linux:" + bootID + ":" + rest[19], name: name, started: started}, true
}

// linuxProcUID is the owner uid of /proc/<pid>, or -1 when it cannot be
// read.
func linuxProcUID(pid int) int {
	info, err := os.Stat("/proc/" + strconv.Itoa(pid))
	if err != nil {
		return -1
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}

// linuxProcComm is /proc/<pid>/comm without its newline, or "".
func linuxProcComm(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// linuxBootTime is the btime line of /proc/stat (seconds since the epoch),
// or 0 when it cannot be read: the start time then reads as zero.
func linuxBootTime() int64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			if sec, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				return sec
			}
		}
	}
	return 0
}

func procCwd(pid int) (string, error) {
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
}
