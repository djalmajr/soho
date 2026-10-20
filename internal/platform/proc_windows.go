//go:build windows

package platform

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	windowsProcessQueryLimitedInformation = 0x1000
	windowsProcessTerminate               = 0x0001
	windowsTH32CS_SNAPPROCESS             = 0x00000002
	windowsErrorInsufficientBuffer        = 122
	windowsErrorInvalidParameter          = 87
)

// ProcLiveness is the outcome of one by-pid liveness read:
//
//	ProcRunning the pid is running; the start time (the creation ticks)
//	            comes with it
//	ProcGone    the absence is proven: an OpenProcess failure with
//	            ERROR_INVALID_PARAMETER, or an opened process that is not
//	            STILL_ACTIVE
//	ProcUnknown the read failed; nothing is known. Unknown is not an
//	            absence: a registry line is never dropped or stopped on it.
type ProcLiveness int

const (
	ProcRunning ProcLiveness = iota
	ProcGone
	ProcUnknown
)

// SameStarted reports whether two start-time readings denote the same
// process start, comparing after collapsing internal whitespace (the
// windows readings are decimal ticks: no padding to collapse).
func SameStarted(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// windowsInvalidHandleValue is the handle Windows returns for a failed
// OpenProcess/CreateToolhelp32Snapshot; a successful handle is never zero
// or the invalid value.
const windowsInvalidHandleValue = ^uintptr(0)

// windowsFileTime is the FILETIME pair GetProcessTimes fills.
type windowsFileTime struct {
	Low  uint32
	High uint32
}

func (f windowsFileTime) ticks() int64 { return int64(f.High)<<32 | int64(f.Low) }

// procIdentity is one process's identity read by pid: its creation time
// in raw FILETIME ticks (100 ns, decimal; see windowsCreationIdentity)
// and its liveness as the three-way result. The windows parentage comes
// from the Toolhelp32 snapshots, not from this read.
type procIdentity struct {
	started string
}

// Test hooks; the zero values keep the production behavior.
// procStopAfterSnapshot runs between the process snapshot and the
// identity reads; procReadIdentity replaces the by-pid identity read
// (the collection pass and every pre-signal re-read); procSendTerm
// replaces the per-pid TERM (taskkill /PID, without /T).
// procParentSnapshotFunc is the Toolhelp32 snapshot read the stop path
// consults twice (the collection snapshot and the revalidation
// snapshot); the admission unit test injects the fake tables.
var (
	procStopAfterSnapshot  func(root int, env Env)
	procReadIdentity       = procReadIdentityReal
	procSendTerm           = windowsTaskkill
	procParentSnapshotFunc = windowsParentSnapshot
)

var (
	windowsKernel32 = syscall.NewLazyDLL("kernel32.dll")

	windowsProcOpenProcess               = windowsKernel32.NewProc("OpenProcess")
	windowsProcGetProcessTimes           = windowsKernel32.NewProc("GetProcessTimes")
	windowsProcQueryFullProcessImageName = windowsKernel32.NewProc("QueryFullProcessImageNameW")
	windowsProcCreateToolhelp32Snapshot  = windowsKernel32.NewProc("CreateToolhelp32Snapshot")
	windowsProcProcess32First            = windowsKernel32.NewProc("Process32FirstW")
	windowsProcProcess32Next             = windowsKernel32.NewProc("Process32NextW")
	windowsProcCloseHandle               = windowsKernel32.NewProc("CloseHandle")
	windowsProcTerminateProcess          = windowsKernel32.NewProc("TerminateProcess")
	windowsProcGetExitCodeProcess        = windowsKernel32.NewProc("GetExitCodeProcess")
)

// Every kernel32 call decides success by the return value, never by the
// error: syscall.LazyProc.Call always returns a non-nil error (the
// GetLastError snapshot), even on success. The error is read only when
// the return value indicates failure, as the cause.

// windowsOpenProcess opens the pid with the given access; a zero or
// INVALID_HANDLE_VALUE handle is the failure.
func windowsOpenProcess(access uint32, pid int) (uintptr, error) {
	handle, _, err := windowsProcOpenProcess.Call(uintptr(access), 0, uintptr(pid))
	if handle == 0 || handle == windowsInvalidHandleValue {
		return 0, err
	}
	return handle, nil
}

// windowsCreationTicks reads the creation time (FILETIME ticks of 100 ns)
// through the given handle; GetProcessTimes returns BOOL.
func windowsCreationTicks(handle uintptr) (int64, error) {
	var creation, exit, kernel, user windowsFileTime
	done, _, err := windowsProcGetProcessTimes.Call(handle,
		uintptr(unsafe.Pointer(&creation)), uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if done == 0 {
		return 0, err
	}
	return creation.ticks(), nil
}

// windowsImageName reads the executable path with QueryFullProcessImageNameW:
// the fourth argument is a pointer to a uint32 size (in: capacity in
// wide characters, out: the characters written), and the buffer is
// decoded over the written size. On ERROR_INSUFFICIENT_BUFFER the buffer
// doubles until 32768 characters.
func windowsImageName(handle uintptr) (string, error) {
	size := uint32(1024)
	for {
		buf := make([]uint16, size)
		done, _, err := windowsProcQueryFullProcessImageName.Call(handle, 0,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
		if done == 0 {
			if errno, ok := err.(syscall.Errno); ok && errno == windowsErrorInsufficientBuffer {
				size *= 2
				if size > 32768 {
					return "", err
				}
				continue
			}
			return "", err
		}
		return syscall.UTF16ToString(buf[:size]), nil
	}
}

// windowsBaseName is the last segment of an executable path.
func windowsBaseName(value string) string {
	trimmed := strings.TrimRight(value, `\`)
	if i := strings.LastIndexAny(trimmed, `\/`); i >= 0 {
		return trimmed[i+1:]
	}
	return value
}

// ReadProcFull reads one pid's creation time, image base name and the
// three-way liveness in one snapshot (the single OpenProcess read, the
// windows counterpart of the unix single ps read): the creation time is
// the raw FILETIME as decimal ticks of 100 ns and the name the base
// segment of the executable path, both empty unless the pid reads as
// running. The three-way result tells a proven absence (the open fails
// with ERROR_INVALID_PARAMETER, or the exit code is not STILL_ACTIVE) from
// an unreadable read (any other open or query failure): the procs add
// path refuses the first (exit 2) and reports the second (exit 4) instead
// of collapsing both into the dead-process refusal, and the one snapshot
// never races a second read to name the first read's failure.
func ReadProcFull(pid int, env Env) (string, string, ProcLiveness) {
	handle, live := windowsOpenProc(pid)
	if live != ProcRunning {
		return "", "", live
	}
	defer windowsProcCloseHandle.Call(handle)
	ticks, err := windowsCreationTicks(handle)
	if err != nil {
		return "", "", ProcUnknown
	}
	name, err := windowsImageName(handle)
	if err != nil {
		return "", "", ProcUnknown
	}
	return strconv.FormatInt(ticks, 10), windowsBaseName(name), ProcRunning
}

// ProcInfo reads the creation time and the image base name of one pid
// through kernel32 (OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION,
// GetProcessTimes, QueryFullProcessImageNameW). The creation time is the
// raw FILETIME as decimal ticks of 100 ns: the full precision the API
// offers, so two reads of the same process compare equal and two distinct
// creations within one second do not. ok is true only for a running pid:
// a proven absence and an unreadable read both read as not running (the
// three-way result is ReadProcFull).
func ProcInfo(pid int, env Env) (string, string, bool) {
	started, name, live := ReadProcFull(pid, env)
	return started, name, live == ProcRunning
}

// windowsGoneOpen reports whether a failed OpenProcess proves the pid's
// absence: ERROR_INVALID_PARAMETER (87) does; any other code — or no code
// at all — leaves the outcome unknown.
func windowsGoneOpen(err error) bool {
	e, ok := err.(syscall.Errno)
	return ok && int(e) == windowsErrorInvalidParameter
}

// windowsOpenProc opens the pid with a limited-information handle and
// reports the three-way liveness: an open failure with
// ERROR_INVALID_PARAMETER is the proven absence; any other open failure
// is unknown; an open process that is not STILL_ACTIVE is gone (an exited
// process stays openable while a handle to it is open, so the exit code
// decides, not the open).
func windowsOpenProc(pid int) (uintptr, ProcLiveness) {
	handle, err := windowsOpenProcess(windowsProcessQueryLimitedInformation, pid)
	if err != nil {
		if windowsGoneOpen(err) {
			return 0, ProcGone
		}
		return 0, ProcUnknown
	}
	switch windowsExitState(handle) {
	case ProcRunning:
		return handle, ProcRunning
	case ProcGone:
		windowsProcCloseHandle.Call(handle)
		return 0, ProcGone
	default:
		windowsProcCloseHandle.Call(handle)
		return 0, ProcUnknown
	}
}

// windowsProcStarted reads one pid's creation time (raw FILETIME ticks of
// 100 ns, decimal) and its liveness as the three-way result; a failed
// GetProcessTimes on an open, active process is unknown.
func windowsProcStarted(pid int) (string, ProcLiveness) {
	handle, live := windowsOpenProc(pid)
	if live != ProcRunning {
		return "", live
	}
	defer windowsProcCloseHandle.Call(handle)
	ticks, err := windowsCreationTicks(handle)
	if err != nil {
		return "", ProcUnknown
	}
	return strconv.FormatInt(ticks, 10), ProcRunning
}

// ReadProc reads one pid's start time and liveness as the three-way
// result the registry and stop paths need (windows: the creation ticks).
func ReadProc(pid int, env Env) (string, ProcLiveness) {
	return windowsProcStarted(pid)
}

// windowsParentSnapshot returns, from one Toolhelp32 process snapshot,
// the parent of every pid in the table (syscall.ProcessEntry32 is the
// PROCESSENTRY32W layout; only the pid and parent-pid fields are read —
// the image name field is never used). A zero or INVALID_HANDLE_VALUE
// snapshot handle and a BOOL 0 from Process32FirstW are failures.
func windowsParentSnapshot() (map[int]int, error) {
	snap, _, err := windowsProcCreateToolhelp32Snapshot.Call(windowsTH32CS_SNAPPROCESS, 0)
	if snap == 0 || snap == windowsInvalidHandleValue {
		return nil, err
	}
	defer windowsProcCloseHandle.Call(snap)
	out := map[int]int{}
	entry := syscall.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	done, _, firstErr := windowsProcProcess32First.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if done == 0 {
		return nil, firstErr
	}
	for {
		out[int(entry.ProcessID)] = int(entry.ParentProcessID)
		done, _, err = windowsProcProcess32Next.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if done == 0 {
			break
		}
	}
	return out, nil
}

// ParentPID reads the parent of this process from the process snapshot.
// -1 when the snapshot is unavailable or the entry was not found.
func ParentPID() int {
	parentOf, err := windowsParentSnapshot()
	if err != nil {
		return -1
	}
	parent, ok := parentOf[syscall.Getpid()]
	if !ok {
		return -1
	}
	return parent
}

// procDescendants lists the descendants of pid (every generation) from a
// Toolhelp32 process snapshot.
func procDescendants(pid int, env Env) ([]int, error) {
	parentOf, err := windowsParentSnapshot()
	if err != nil {
		return nil, err
	}
	children := map[int][]int{}
	for child, parent := range parentOf {
		children[parent] = append(children[parent], child)
	}
	out := []int{}
	seen := map[int]bool{pid: true}
	queue := []int{pid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range children[cur] {
			if seen[child] {
				continue
			}
			seen[child] = true
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out, nil
}

// procReadIdentityReal reads one pid's creation time with a
// limited-information handle, three-way: an OpenProcess failure with
// ERROR_INVALID_PARAMETER is the proven absence; any other failure is
// unknown; an open process that is not STILL_ACTIVE is gone.
func procReadIdentityReal(pid int, env Env) (procIdentity, ProcLiveness) {
	started, live := windowsProcStarted(pid)
	return procIdentity{started: started}, live
}

// windowsStillActiveCode is GetExitCodeProcess's STILL_ACTIVE (259).
const windowsStillActiveCode = 259

// windowsExitCode queries GetExitCodeProcess; ok is false when the call
// failed (BOOL 0), and then code means nothing. Replaceable in tests.
var windowsExitCode = func(handle uintptr) (code uint32, ok bool) {
	r, _, _ := windowsProcGetExitCodeProcess.Call(handle, uintptr(unsafe.Pointer(&code)))
	return code, r != 0
}

// windowsExitState reads the liveness of the process behind handle. An
// exited process stays openable while any handle to it is open (its
// parent's, for one), so OpenProcess alone does not prove it runs: the
// STILL_ACTIVE exit code is running, another exit code is the proven
// absence, and a failed query is unknown, never gone.
func windowsExitState(handle uintptr) ProcLiveness {
	code, ok := windowsExitCode(handle)
	switch {
	case !ok:
		return ProcUnknown
	case code == windowsStillActiveCode:
		return ProcRunning
	default:
		return ProcGone
	}
}

// procExists reports whether the pid may still run: only a proven
// absence (ERROR_INVALID_PARAMETER at open, or an exit code other than
// STILL_ACTIVE) is false, so a failed query keeps a wait polling and a
// stop from being declared done.
func procExists(pid int) bool {
	handle, err := windowsOpenProcess(windowsProcessQueryLimitedInformation, pid)
	if err != nil {
		return !windowsGoneOpen(err)
	}
	defer windowsProcCloseHandle.Call(handle)
	return windowsExitState(handle) != ProcGone
}

// procAliveStates reports which of the pids is still openable: one
// limited-information probe per pid (a stopped process's entry closes
// when its parent reaps it).
func procAliveStates(pids []int, env Env) map[int]bool {
	out := map[int]bool{}
	for _, p := range pids {
		out[p] = procExists(p)
	}
	return out
}

// StopProcessTree stops the windows process tree rooted at pid in three
// phases, mirroring the unix helper. expectedStarted is the creation time
// (FILETIME ticks) the caller registered for the root: the root is
// admitted only when its captured creation time is the same start
// (SameStarted), and a divergence stops the whole call with `process
// <pid> changed after the check`, no signal sent.
//
// Collection: a Toolhelp32 pid/ppid snapshot, then — for every
// candidate, by pid — the creation time (OpenProcess, GetProcessTimes,
// the STILL_ACTIVE exit code), read three-way. A second Toolhelp32
// snapshot is taken once the creation times are captured, and a
// descendant is admitted only when its current parent (the second
// snapshot) is the first snapshot's parent and its creation ticks are not
// older than its parent's captured ticks: a pid reused by a process older
// than the parent is left out and never signalled. An unreadable read of
// the root stops the call with an error: the stop is not claimed.
//
// TERM: from the deepest descendant to the root, the root last, a
// taskkill /PID <pid> (without /T, so only the verified pid is reached)
// after the pre-signal re-read of the creation time; an unreadable read
// of a verified target stops the call with an error.
//
// KILL: after up to 3 s, the survivors only, through
// OpenProcess(PROCESS_TERMINATE|PROCESS_QUERY_LIMITED_INFORMATION),
// re-reading the creation time through the same handle and calling
// TerminateProcess only when it still matches.
func StopProcessTree(pid int, expectedStarted string, env Env) error {
	return StopProcessTreeGuarded(pid, expectedStarted, env, nil)
}

// StopProcessTreeGuarded is StopProcessTree with a guard checked before
// each signal round, the TERM one and the KILL one: when guard reports
// false the call stops there, sends nothing more and returns
// ErrStopGuardChanged. A nil guard is StopProcessTree.
func StopProcessTreeGuarded(pid int, expectedStarted string, env Env, guard func() bool) error {
	parentOf, err := procParentSnapshotFunc()
	if err != nil {
		return err
	}
	if procStopAfterSnapshot != nil {
		procStopAfterSnapshot(pid, env)
	}
	members, depths := windowsTreeMembers(pid, parentOf)
	captured := map[int]string{}
	created := map[int]int64{}
	live := map[int]bool{}
	for _, member := range members { // root first, shallow to deep
		id, liveRead := procReadIdentity(member, env)
		switch liveRead {
		case ProcGone:
			continue
		case ProcUnknown:
			if member == pid {
				return fmt.Errorf("process %d is unreadable; not stopped", pid)
			}
			continue
		}
		captured[member] = id.started
		if ticks, parseErr := strconv.ParseInt(id.started, 10, 64); parseErr == nil {
			created[member] = ticks
			live[member] = true
		}
	}
	if !live[pid] {
		// The root is gone (or unreadable as ticks): nothing verified is a
		// clean no-op.
		return nil
	}
	if !SameStarted(captured[pid], expectedStarted) {
		return fmt.Errorf("process %d changed after the check", pid)
	}
	// The revalidation snapshot: the current parent of every candidate,
	// taken after the creation-time capture.
	parentOf2, err := procParentSnapshotFunc()
	if err != nil {
		return err
	}
	verified := map[int]bool{pid: true}
	for _, member := range members {
		if member == pid || !live[member] {
			continue
		}
		parent := parentOf[member]
		if !verified[parent] {
			continue
		}
		if parentOf2[member] != parent {
			continue // the current parent is not the snapshot's parent
		}
		if created[member] < created[parent] {
			continue // older than its parent: the pid was reused
		}
		verified[member] = true
	}
	targets := windowsVerifiedTargets(members, depths, verified)
	if len(targets) == 0 {
		return nil
	}
	if guard != nil && !guard() {
		return ErrStopGuardChanged
	}
	for _, target := range targets {
		id, liveRead := procReadIdentity(target, env)
		switch liveRead {
		case ProcGone:
			continue
		case ProcUnknown:
			return fmt.Errorf("process %d is unreadable; not stopped", target)
		}
		if !SameStarted(id.started, captured[target]) {
			continue
		}
		if err := procSendTerm(target, env); err != nil {
			// The final liveness check decides; the KILL round escalates.
		}
	}
	if windowsWaitGone(targets, 3*time.Second) {
		return nil
	}
	if guard != nil && !guard() {
		return ErrStopGuardChanged
	}
	for _, target := range targets {
		id, liveRead := procReadIdentity(target, env)
		switch liveRead {
		case ProcGone:
			continue
		case ProcUnknown:
			return fmt.Errorf("process %d is unreadable; not stopped", target)
		}
		if !SameStarted(id.started, captured[target]) {
			continue
		}
		if err := windowsTerminate(target, captured[target]); err != nil {
			// The final liveness check decides.
		}
	}
	if windowsWaitGone(targets, time.Second) {
		return nil
	}
	return fmt.Errorf("the tree of %d survived TerminateProcess (still running: %s)", pid, strings.Join(windowsInts(windowsStillRunning(targets)), ", "))
}

// windowsTreeMembers returns the root and its snapshot descendants (BFS
// order, root first) with each member's depth in the tree.
func windowsTreeMembers(pid int, parentOf map[int]int) ([]int, map[int]int) {
	children := map[int][]int{}
	for child, parent := range parentOf {
		children[parent] = append(children[parent], child)
	}
	members := []int{pid}
	depths := map[int]int{pid: 0}
	queue := []int{pid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range children[cur] {
			if _, seen := depths[child]; seen {
				continue
			}
			depths[child] = depths[cur] + 1
			members = append(members, child)
			queue = append(queue, child)
		}
	}
	return members, depths
}

// windowsVerifiedTargets orders the verified members deepest first (the
// root last), pid ascending within a depth.
func windowsVerifiedTargets(members []int, depths map[int]int, verified map[int]bool) []int {
	out := []int{}
	for _, member := range members {
		if verified[member] {
			out = append(out, member)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if depths[out[i]] != depths[out[j]] {
			return depths[out[i]] > depths[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// windowsStillRunning lists the pids still openable.
func windowsStillRunning(pids []int) []int {
	out := []int{}
	for _, pid := range pids {
		if procExists(pid) {
			out = append(out, pid)
		}
	}
	return out
}

// windowsTerminate opens the pid with PROCESS_TERMINATE (plus
// PROCESS_QUERY_LIMITED_INFORMATION), re-reads the creation time through
// the same handle and calls TerminateProcess only when it still matches
// the captured value; TerminateProcess returns BOOL.
func windowsTerminate(pid int, captured string) error {
	handle, err := windowsOpenProcess(windowsProcessTerminate|windowsProcessQueryLimitedInformation, pid)
	if err != nil {
		return err
	}
	defer windowsProcCloseHandle.Call(handle)
	ticks, err := windowsCreationTicks(handle)
	if err != nil {
		return err
	}
	if strconv.FormatInt(ticks, 10) != captured {
		return fmt.Errorf("process %d changed its identity before the terminate", pid)
	}
	done, _, termErr := windowsProcTerminateProcess.Call(handle, 0)
	if done == 0 {
		return termErr
	}
	return nil
}

func windowsInts(values []int) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strconv.Itoa(v)
	}
	return out
}

// windowsTaskkill sends the per-pid TERM: taskkill /PID <pid>, without
// /T, so only the pid verified by the pre-signal re-read is reached.
func windowsTaskkill(pid int, env Env) error {
	r := RunCli("taskkill", []string{"/PID", strconv.Itoa(pid)}, RunOptions{Env: env, TimeoutMs: 10_000})
	if r.NotFound || r.TimedOut || r.Error != "" || r.Status == nil || *r.Status != 0 {
		return fmt.Errorf("taskkill /PID %d failed", pid)
	}
	return nil
}

// windowsWaitGone polls every 100 ms until none of the pids is openable
// or the ceiling is reached.
func windowsWaitGone(pids []int, ceiling time.Duration) bool {
	deadline := time.Now().Add(ceiling)
	for {
		left := false
		for _, p := range pids {
			if procExists(p) {
				left = true
				break
			}
		}
		if !left {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
