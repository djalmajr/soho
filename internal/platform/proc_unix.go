//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// procStartRe splits one `ps -o lstart=,state=,comm=` line: the raw lstart
// field (day space-padded, LC_ALL=C abbreviations), the state (the base
// letter plus the modifier flags ps appends, e.g. Ss, S+, R+, SN, S<,
// Sl) and the rest of the line, the comm. The lstart is stored exactly as
// the system prints it, so two reads of the same process compare equal.
var procStartRe = regexp.MustCompile(`^([A-Z][a-z]{2} [A-Z][a-z]{2}  ?\d{1,2} \d{2}:\d{2}:\d{2} \d{4})\s+([A-Za-z?][A-Za-z+<]*)\s+(.+)$`)

// procStateGone reports whether a ps state token is the zombie base state
// (with or without the modifier flags ps appends, e.g. Z+, Zs): a dead
// process left unreaped is not running, and a flagged zombie must not
// appear running or survive the cleanup poll.
func procStateGone(state string) bool {
	return strings.HasPrefix(state, "Z")
}

// procStateRunning reports whether a ps state token's base letter is a
// live state (R, S, D, T, W, X, I, U, or the tracing-stop t; the modifier
// letters ps appends, s + n < l, do not change the liveness), or whether
// the token carries the BSD E flag, "the process is trying to exit"
// (macOS prints it as ?E, or after a base letter, while the exit runs): an
// exiting process is still in the table with a readable identity, so it
// reads as present — the stop verifies its start time, signals it and
// waits for its actual disappearance (a zombie or an absent pid) instead
// of skipping it or calling it unreadable. Any other base letter — or an
// empty token — is not a positive running read: the caller confirms the
// line against the kernel, and an unknown state never reads as running.
// Linux ps has no E flag.
func procStateRunning(state string) bool {
	if state == "" {
		return false
	}
	switch state[0] {
	case 'R', 'S', 'D', 'T', 'W', 'X', 'I', 'U', 't':
		return true
	}
	return strings.Contains(state, "E")
}

// procLocale returns env with LC_ALL=C, so ps prints the date fields in
// the fixed English abbreviated form the parser expects.
func procLocale(env Env) Env {
	out := env.Clone()
	out["LC_ALL"] = "C"
	return out
}

// ProcLiveness is the outcome of one by-pid liveness read:
//
//	ProcRunning the pid is running; the start time comes with it
//	ProcGone    the absence is proven: the pid does not exist, or it is a
//	            zombie left unreaped (a dead process is not running)
//	ProcUnknown the read failed; nothing is known. Unknown is not an
//	            absence: a registry line is never dropped or stopped on it.
type ProcLiveness int

const (
	ProcRunning ProcLiveness = iota
	ProcGone
	ProcUnknown
)

// SameStarted reports whether two start-time readings denote the same
// process start, comparing after collapsing internal whitespace: the unix
// lstart pads the day field with spaces, so a raw reading (the registry
// copy) and a field-rejoined reading (the stop pass) of the same process
// differ in padding only.
func SameStarted(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// procReadDenied reports whether a failed ps read failed on a permission
// denial: the exec itself was denied (an EPERM/EACCES or
// "operation not permitted" error) or ps ran but the system refused its
// table read (the denial on its stderr). A missing binary, a timeout or
// any other failure is not a denial.
func procReadDenied(r RunResult) bool {
	lower := strings.ToLower(r.Error + "\n" + r.Stderr)
	for _, marker := range []string{"eperm", "eacces", "operation not permitted", "permission denied", "access denied"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// deniedCause is the cause of an unreadable read: only a ps read that
// failed on a permission denial names one. The kernel probe never does:
// its EPERM is a pid owned by another user, not a denied table read, and
// a pid it proves gone has no unreadable read to explain.
func deniedCause(live ProcLiveness, r RunResult) error {
	if live != ProcUnknown || !procReadDenied(r) {
		return nil
	}
	return ErrProcReadPermissionDenied
}

// procInfoFull reads the start time, the command base name and the
// liveness of one pid with `ps -o lstart=,state=,comm= -p <pid>`
// (LC_ALL=C, a 5 s ceiling): only that pid, never a command-line listing
// (it may carry credentials). started is the start time exactly as the
// system prints it, name the base name of the command. The liveness is
// the three-way read: the state is the base letter plus the modifier
// flags ps appends (Ss, S+, R+, SN, S<, Sl), and a zombie base state (Z,
// with or without flags) is gone. When ps exits non-zero, is missing or
// times out, the line does not parse, or the state base letter is not a
// known live or zombie state, the read is confirmed with kill(pid, 0) —
// ESRCH is the proven absence, any other result (nil or EPERM among them)
// is unknown. A zombie (a dead process left unreaped by its parent) is
// not running. The cause is ErrProcReadPermissionDenied when an unknown
// read failed because ps was denied, nil otherwise.
func procInfoFull(pid int, env Env) (string, string, ProcLiveness, error) {
	r := RunCli("ps", []string{"-o", "lstart=,state=,comm=", "-p", strconv.Itoa(pid)}, RunOptions{Env: procLocale(env), TimeoutMs: 5000})
	if r.NotFound || r.TimedOut || r.Error != "" || r.Status == nil || *r.Status != 0 {
		live := procKernelGone(pid)
		return "", "", live, deniedCause(live, r)
	}
	m := procStartRe.FindStringSubmatch(strings.TrimSpace(r.Stdout))
	if m == nil {
		live := procKernelGone(pid)
		return "", "", live, deniedCause(live, r)
	}
	switch {
	case procStateGone(m[2]):
		return "", "", ProcGone, nil
	case procStateRunning(m[2]):
		return m[1], basePath(m[3]), ProcRunning, nil
	}
	live := procKernelGone(pid)
	return "", "", live, deniedCause(live, r)
}

// procKernelGone confirms a failed ps read against the kernel: kill(pid,
// 0). ESRCH is the proven absence; any other result — nil (the pid
// exists) or EPERM (it exists, owned by another user) — is unknown: a
// failed read is not an absence.
func procKernelGone(pid int) ProcLiveness {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return ProcGone
	}
	return ProcUnknown
}

// ReadProcFull reads one pid's start time, command base name and the
// three-way liveness in one snapshot (the single by-pid ps read): started
// is the start time exactly as the system prints it and name the base
// name of the command, both empty unless the pid reads as running. The
// three-way result tells a proven absence (the pid does not exist or is a
// zombie) from an unreadable read (the ps failed, or its line did not
// parse, and the kernel did not report the absence): the procs add path
// refuses the first (exit 2) and reports the second (exit 4) instead of
// collapsing both into the dead-process refusal, and the one snapshot
// never races a second read to name the first read's failure.
func ReadProcFull(pid int, env Env) (string, string, ProcLiveness) {
	started, name, live, _ := procInfoFull(pid, env)
	return started, name, live
}

// ReadProcFullCause is ReadProcFull with the cause of an unreadable read
// when the read can name one: ErrProcReadPermissionDenied when an
// unknown read failed because the ps read was denied, nil otherwise. The
// single snapshot and the three-way liveness are unchanged.
func ReadProcFullCause(pid int, env Env) (string, string, ProcLiveness, error) {
	return procInfoFull(pid, env)
}

// ProcInfo reads the start time and the command base name of one pid.
// ok is true only for a running pid: a proven absence and an unreadable
// read both read as not running (the three-way result is ReadProcFull).
func ProcInfo(pid int, env Env) (string, string, bool) {
	started, name, live := ReadProcFull(pid, env)
	return started, name, live == ProcRunning
}

// ReadProc reads one pid's start time and liveness as the three-way
// result the registry and stop paths need.
func ReadProc(pid int, env Env) (string, ProcLiveness) {
	started, _, live := ReadProcFull(pid, env)
	return started, live
}

// basePath is filepath.Base without importing path/filepath in the hot
// error paths: the comm is a path on some systems, and only its last
// segment is the name.
func basePath(value string) string {
	trimmed := strings.TrimRight(value, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// ParentPID is the pid of the process that invoked this soho (the
// caller's shell), for the procs add refusal.
func ParentPID() int { return os.Getppid() }

// procIdentity is one process's identity read by pid: its start time
// (empty when the table does not print one) and the current parent pid
// when the line carries it. The liveness comes with the read as the
// three-way result.
type procIdentity struct {
	started   string
	ppid      int
	ppidKnown bool
}

// Test hooks; the zero values keep the production behavior.
// procStopAfterSnapshot runs between the pid/ppid snapshot and the
// identity reads; procReadIdentity replaces the by-pid identity read
// (the collection pass and every pre-signal re-read); procSendTerm
// replaces the TERM send.
var (
	procStopAfterSnapshot func(root int, env Env)
	procReadIdentity      = procReadIdentityReal
	procSendTerm          = func(pid int, env Env) error { return syscall.Kill(pid, syscall.SIGTERM) }
)

// procReadIdentityReal reads one pid's start time, current parent pid and
// state with `ps -o lstart=,ppid=,state= -p <pid>` (LC_ALL=C, a 5 s
// ceiling): only those fields, never a command line. The lstart is
// re-joined with single spaces, a normalization that is stable for the
// life of the process, so two reads of the same process compare equal.
// The read is three-way: a parsed line says running or gone (the state is
// the base letter plus the modifier flags ps appends, and a zombie base
// state, with or without flags, is not running); when ps exits non-zero,
// is missing, times out, the table carries no line for this pid, or the
// state base letter is not a known live or zombie state, the read is
// confirmed with kill(pid, 0) — ESRCH is gone, anything else is unknown.
// A pid whose line does not print a start time or a parent (a reduced
// table) reads both as unknown and keeps only the state.
func procReadIdentityReal(pid int, env Env) (procIdentity, ProcLiveness) {
	r := RunCli("ps", []string{"-o", "lstart=,ppid=,state=", "-p", strconv.Itoa(pid)}, RunOptions{Env: procLocale(env), TimeoutMs: 5000})
	if r.NotFound || r.TimedOut || r.Error != "" || r.Status == nil || *r.Status != 0 {
		return procIdentity{}, procKernelGone(pid)
	}
	for _, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		state := fields[len(fields)-1]
		if len(fields) == 2 && isAllDigit(fields[0]) {
			// The line carries a pid and a state: it counts only when it
			// is the queried pid's own line (a table row of another pid is
			// not this pid's answer).
			if linePid, err := strconv.Atoi(fields[0]); err != nil || linePid != pid {
				continue
			}
			switch {
			case procStateGone(state):
				return procIdentity{}, ProcGone
			case procStateRunning(state):
				return procIdentity{}, ProcRunning
			}
			return procIdentity{}, procKernelGone(pid)
		}
		ppid, err := strconv.Atoi(fields[len(fields)-2])
		if err != nil {
			continue
		}
		id := procIdentity{
			started:   strings.Join(fields[:len(fields)-2], " "),
			ppid:      ppid,
			ppidKnown: true,
		}
		switch {
		case procStateGone(state):
			return id, ProcGone
		case procStateRunning(state):
			return id, ProcRunning
		}
		return procIdentity{}, procKernelGone(pid)
	}
	return procIdentity{}, procKernelGone(pid)
}

func isAllDigit(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// procParentSnapshot returns, from one `ps -A -o pid=,ppid=` call, the
// parent of every pid in the table: only the pid/ppid pair, never command
// lines. A ps failure is an error, not an empty table.
func procParentSnapshot(env Env) (map[int]int, error) {
	r := RunCli("ps", []string{"-A", "-o", "pid=,ppid="}, RunOptions{Env: procLocale(env), TimeoutMs: 5000})
	if r.NotFound || r.TimedOut || r.Error != "" || r.Status == nil || *r.Status != 0 {
		return nil, fmt.Errorf("ps -A failed (exit %s)", statusText(r))
	}
	out := map[int]int{}
	for _, line := range strings.Split(r.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		child, err1 := strconv.Atoi(fields[0])
		parent, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		out[child] = parent
	}
	return out, nil
}

// procDescendants lists the descendants of pid (every generation) from a
// pid/ppid snapshot.
func procDescendants(pid int, env Env) ([]int, error) {
	parentOf, err := procParentSnapshot(env)
	if err != nil {
		return nil, err
	}
	children := procChildren(parentOf)
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

func procChildren(parentOf map[int]int) map[int][]int {
	children := map[int][]int{}
	for child, parent := range parentOf {
		children[parent] = append(children[parent], child)
	}
	return children
}

func statusText(r RunResult) string {
	if r.Status == nil {
		return "none"
	}
	return strconv.Itoa(*r.Status)
}

// StopProcessTree stops the unix process tree rooted at pid in three
// phases. expectedStarted is the start time the caller registered for the
// root: the root is admitted only when its collected start time is the
// same start (SameStarted), and a divergence stops the whole call with
// `process <pid> changed after the check`, no signal sent.
//
// Collection: a pid/ppid snapshot, then — for every candidate, by pid —
// the start time and the current parent, read three-way (running with the
// start time, a proven absence, an unreadable read). Only the root
// verified against expectedStarted and the pids still children of an
// already-verified pid are kept; a pid that reparented or vanished leaves
// the list and is never signalled. An unreadable read of the root stops
// the call with an error: the stop is not claimed.
//
// TERM: from the deepest descendant to the root, the root last.
// Immediately before each signal the pid's start time is read again and
// the signal goes only when it still matches the collected one; an
// unreadable read of a verified target stops the call with an error.
//
// KILL: after up to 3 s, the survivors only, with the same re-read before
// each signal. A pid that lost its parent because the parent got the TERM
// is still the same process by its start time and is escalated.
//
// An error is returned when the snapshot fails, when the root diverges
// from its registered start or goes unreadable, when a verified target
// goes unreadable, or when anything of the tree survives the kill. The
// unix lstart has second precision: in practice a pid is not recycled
// within the same second, and the re-read before each signal shrinks the
// window a recycled pid could hide behind.
func StopProcessTree(pid int, expectedStarted string, env Env) error {
	return StopProcessTreeGuarded(pid, expectedStarted, env, nil)
}

// StopProcessTreeGuarded is StopProcessTree with a guard checked before
// each signal round, the TERM one and the KILL one: when guard reports
// false the call stops there, sends nothing more and returns
// ErrStopGuardChanged. A nil guard is StopProcessTree.
func StopProcessTreeGuarded(pid int, expectedStarted string, env Env, guard func() bool) error {
	parentOf, err := procParentSnapshot(env)
	if err != nil {
		return err
	}
	if procStopAfterSnapshot != nil {
		procStopAfterSnapshot(pid, env)
	}
	members, depths := procTreeMembers(pid, parentOf)
	captured := map[int]string{}
	verified := map[int]bool{}
	for _, member := range members { // root first, shallow to deep
		id, live := procReadIdentity(member, env)
		switch live {
		case ProcGone:
			continue
		case ProcUnknown:
			if member == pid {
				return fmt.Errorf("process %d is unreadable; not stopped", pid)
			}
			continue
		}
		if member == pid {
			if !SameStarted(id.started, expectedStarted) {
				return fmt.Errorf("process %d changed after the check", pid)
			}
		} else {
			parent := parentOf[member]
			if !verified[parent] {
				continue
			}
			if id.ppidKnown && id.ppid != parent {
				continue // reparented since the snapshot
			}
		}
		captured[member] = id.started
		verified[member] = true
	}
	targets := procVerifiedTargets(members, depths, verified)
	if len(targets) == 0 {
		return nil
	}
	if guard != nil && !guard() {
		return ErrStopGuardChanged
	}
	for _, target := range targets {
		id, live := procReadIdentity(target, env)
		switch live {
		case ProcGone:
			continue
		case ProcUnknown:
			return fmt.Errorf("process %d is unreadable; not stopped", target)
		}
		if !SameStarted(id.started, captured[target]) {
			continue
		}
		if err := procSendTerm(target, env); err != nil && !errors.Is(err, syscall.ESRCH) {
			// The final liveness check decides; the KILL round escalates.
		}
	}
	if waitTreeGone(targets, 3*time.Second, env) {
		return nil
	}
	if guard != nil && !guard() {
		return ErrStopGuardChanged
	}
	for _, target := range targets {
		id, live := procReadIdentity(target, env)
		switch live {
		case ProcGone:
			continue
		case ProcUnknown:
			return fmt.Errorf("process %d is unreadable; not stopped", target)
		}
		if !SameStarted(id.started, captured[target]) {
			continue
		}
		_ = syscall.Kill(target, syscall.SIGKILL)
	}
	if waitTreeGone(targets, time.Second, env) {
		return nil
	}
	return fmt.Errorf("the tree of %d survived SIGKILL (still running: %s)", pid, strings.Join(mapInts(procStillRunning(targets, env)), ", "))
}

// procTreeMembers returns the root and its snapshot descendants (BFS
// order, root first) with each member's depth in the tree.
func procTreeMembers(pid int, parentOf map[int]int) ([]int, map[int]int) {
	children := procChildren(parentOf)
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

// procVerifiedTargets orders the verified members deepest first (the root
// last), pid ascending within a depth.
func procVerifiedTargets(members []int, depths map[int]int, verified map[int]bool) []int {
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

// procStillRunning lists, one by-pid read at a time, the pids still
// running (a zombie is not running; an unreadable read does not count).
func procStillRunning(pids []int, env Env) []int {
	out := []int{}
	for _, pid := range pids {
		if _, live := procReadIdentity(pid, env); live == ProcRunning {
			out = append(out, pid)
		}
	}
	return out
}

// procAliveStates reports, from one `ps -o pid=,state=` call, which of the
// pids is still running; a zombie base state (with or without the
// modifier flags ps appends, Z+, Zs) is not running, and any other state,
// including one this parser does not know, keeps the pid alive: a stop is
// never claimed on a doubt. When ps itself is missing or times out, every
// pid reads as alive (the conservative behavior). When ps ran but failed
// (non-zero status, an error, or a missing status), its stdout — empty or
// partial — is not liveness proof and is not parsed: every pid is
// confirmed against the kernel instead, and only ESRCH establishes gone;
// an existing or denied (EPERM) pid stays alive or uncertain. A normal
// non-zero empty ps, the one where every queried pid genuinely exited,
// still reads as gone, because the kernel confirms each absence.
func procAliveStates(pids []int, env Env) map[int]bool {
	out := map[int]bool{}
	if len(pids) == 0 {
		return out
	}
	r := RunCli("ps", []string{"-o", "pid=,state=", "-p", strings.Join(mapInts(pids), ",")}, RunOptions{Env: procLocale(env), TimeoutMs: 2000})
	if r.NotFound || r.TimedOut {
		for _, p := range pids {
			out[p] = true
		}
		return out
	}
	if r.Error != "" || r.Status == nil || *r.Status != 0 {
		for _, p := range pids {
			out[p] = procKernelGone(p) != ProcGone
		}
		return out
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		out[pid] = !procStateGone(fields[1])
	}
	return out
}

// waitTreeGone polls every 100 ms until none of the pids is running (a
// zombie is not running) or the ceiling is reached.
func waitTreeGone(targets []int, ceiling time.Duration, env Env) bool {
	deadline := time.Now().Add(ceiling)
	for {
		living := procAliveStates(targets, env)
		left := false
		for _, t := range targets {
			if living[t] {
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

func mapInts(values []int) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strconv.Itoa(v)
	}
	return out
}
