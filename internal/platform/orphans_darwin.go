//go:build darwin

package platform

import (
	"encoding/binary"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

// The kinfo_proc fields the orphan scan reads, past those of
// proc_identity_darwin.go: kp_eproc starts at 296, its e_ucred.cr_uid (the
// effective uid) sits at 296+124 and its e_ppid at 296+264.
const (
	darwinOffUID  = 420
	darwinOffPpid = 560
)

// The proc_info call that reads a process's current directory:
// PROC_INFO_CALL_PIDINFO with PROC_PIDVNODEPATHINFO fills a struct
// proc_vnodepathinfo (2352 bytes) whose pvi_cdir.vip_path (MAXPATHLEN
// bytes) starts at 152.
const (
	darwinProcInfoCallPidInfo = 2
	darwinPidVnodePathInfo    = 9
	darwinVnodePathInfoSize   = 2352
	darwinOffCdirPath         = 152
	darwinMaxPathLen          = 1024
)

func userOrphanProcesses() ([]OrphanProc, error) {
	uid := os.Geteuid()
	entries, err := darwinUserProcs(uid)
	if err != nil {
		return nil, err
	}
	out := []OrphanProc{}
	for _, entry := range selectOrphans(entries, uid, os.Getpid(), func(ppid int) bool { return ppid == 1 }) {
		cwd, err := procCwd(entry.pid)
		if err != nil || cwd == "" {
			continue // exited or unreadable meanwhile: left out
		}
		out = append(out, OrphanProc{Pid: entry.pid, Identity: entry.identity, Name: entry.name, Cwd: cwd, Started: entry.started})
	}
	return out, nil
}

// darwinUserProcs reads every kinfo_proc of the effective uid in one
// sysctl (CTL_KERN, KERN_PROC, KERN_PROC_UID). A record whose layout does
// not match (a zero pid, an empty name, no start time) or a zombie is left
// out; the identity is built exactly as procStartIdentity builds it.
func darwinUserProcs(uid int) ([]orphanEntry, error) {
	mib := [4]int32{1, 14, 5, int32(uid)} // CTL_KERN, KERN_PROC, KERN_PROC_UID
	var buf []byte
	n := uintptr(0)
	for attempt := 0; ; attempt++ {
		_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), 0, uintptr(unsafe.Pointer(&n)), 0, 0)
		if errno != 0 {
			return nil, errno
		}
		if n == 0 {
			return nil, nil
		}
		// Room for processes started between the two calls.
		n += n/8 + darwinKinfoProcSize*16
		buf = make([]byte, n)
		_, _, errno = syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
		if errno == syscall.ENOMEM && attempt < 3 {
			continue
		}
		if errno != 0 {
			return nil, errno
		}
		break
	}
	out := []orphanEntry{}
	for off := uintptr(0); off+darwinKinfoProcSize <= n; off += darwinKinfoProcSize {
		rec := buf[off : off+darwinKinfoProcSize]
		pid := int(int32(binary.LittleEndian.Uint32(rec[darwinOffPid:])))
		if pid <= 0 || rec[darwinOffStat] == darwinStatZombie {
			continue
		}
		sec := int64(binary.LittleEndian.Uint64(rec[0:8]))
		usec := int64(int32(binary.LittleEndian.Uint32(rec[8:12])))
		comm := rec[darwinOffComm : darwinOffComm+darwinCommLen]
		end := 0
		for end < len(comm) && comm[end] != 0 {
			end++
		}
		if sec <= 0 || end == 0 {
			continue
		}
		out = append(out, orphanEntry{
			pid:      pid,
			ppid:     int(int32(binary.LittleEndian.Uint32(rec[darwinOffPpid:]))),
			uid:      int(binary.LittleEndian.Uint32(rec[darwinOffUID:])),
			identity: "darwin:" + strconv.FormatInt(sec, 10) + "." + strconv.FormatInt(usec, 10),
			name:     string(comm[:end]),
			started:  time.Unix(sec, usec*1000),
		})
	}
	return out, nil
}

func procCwd(pid int) (string, error) {
	buf := make([]byte, darwinVnodePathInfoSize)
	r, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, darwinProcInfoCallPidInfo, uintptr(pid), darwinPidVnodePathInfo, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return "", errno
	}
	if r != darwinVnodePathInfoSize {
		return "", syscall.EINVAL
	}
	path := buf[darwinOffCdirPath : darwinOffCdirPath+darwinMaxPathLen]
	end := 0
	for end < len(path) && path[end] != 0 {
		end++
	}
	if end == 0 {
		return "", syscall.ENOENT
	}
	return string(path[:end]), nil
}
