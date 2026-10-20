package platform

import (
	"reflect"
	"testing"
)

// TestSelectOrphansFilters pins the two filters of the orphan scan on a
// synthetic table: an entry of another uid and an entry whose parent is
// not a reaper are left out, as are pid 1 and this process.
func TestSelectOrphansFilters(t *testing.T) {
	entries := []orphanEntry{
		{pid: 1, ppid: 0, uid: 501},
		{pid: 10, ppid: 1, uid: 501, name: "kept"},
		{pid: 11, ppid: 1, uid: 0, name: "other uid"},
		{pid: 12, ppid: 77, uid: 501, name: "child of a live parent"},
		{pid: 13, ppid: 900, uid: 501, name: "user manager child"},
		{pid: 42, ppid: 1, uid: 501, name: "self"},
	}
	reaper := func(ppid int) bool { return ppid == 1 || ppid == 900 }
	got := []int{}
	for _, entry := range selectOrphans(entries, 501, 42, reaper) {
		got = append(got, entry.pid)
	}
	if want := []int{10, 13}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selectOrphans = %v; want %v (same uid, reaper parent, not pid 1, not self)", got, want)
	}
}

// TestProcCwdRefusesANonPositivePid keeps the pid guard of ProcCwd.
func TestProcCwdRefusesANonPositivePid(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if cwd, err := ProcCwd(pid); err == nil || cwd != "" {
			t.Fatalf("ProcCwd(%d) = %q, %v; want an error", pid, cwd, err)
		}
	}
}
