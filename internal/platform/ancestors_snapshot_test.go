package platform

import (
	"errors"
	"reflect"
	"testing"
)

// The snapshot reader runs on every system (the Windows provider is its
// only production user, but it is a pure function over a map).
func TestSnapshotParentReader(t *testing.T) {
	t.Run("a present parent continues the chain up to the root", func(t *testing.T) {
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{30: 20, 20: 10, 10: 1, 1: 0}, nil))
		if err != nil || !reflect.DeepEqual(chain, []int{30, 20, 10, 1}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a parent that already exited ends the chain there", func(t *testing.T) {
		// 20 is gone from the snapshot, but 30 still records it as its parent.
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{30: 20}, nil))
		if err != nil || !reflect.DeepEqual(chain, []int{30, 20}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a start pid missing from the snapshot is a read failure", func(t *testing.T) {
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{20: 10}, nil))
		if !errors.Is(err, errAncestorRead) || !reflect.DeepEqual(chain, []int{30}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a cycle stops the walk with an error", func(t *testing.T) {
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{30: 20, 20: 10, 10: 20}, nil))
		if !errors.Is(err, errAncestorCycle) || !reflect.DeepEqual(chain, []int{30, 20, 10}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
}

// createdFrom is the injected creation-time read: a pid outside times is
// unreadable.
func createdFrom(times map[int]int64) func(pid int) (int64, bool) {
	return func(pid int) (int64, bool) {
		at, ok := times[pid]
		return at, ok
	}
}

// With the creation-time read injected, a parent present in the snapshot
// is accepted only when it was created at the same instant as its child
// or before it (Windows reuses the pid of a dead parent).
func TestSnapshotParentReaderCreationTime(t *testing.T) {
	t.Run("a parent newer than its child ends the chain at the child", func(t *testing.T) {
		// 20 is a reused pid: created after 30, its "child".
		parents := map[int]int{30: 20, 20: 10, 10: 1, 1: 0}
		times := map[int]int64{30: 500, 20: 900, 10: 100, 1: 1}
		chain, err := walkAncestors(30, snapshotParentReader(parents, createdFrom(times)))
		if err != nil || !reflect.DeepEqual(chain, []int{30}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a newer parent midway keeps the legitimate part of the chain", func(t *testing.T) {
		parents := map[int]int{40: 30, 30: 20, 20: 10, 10: 1, 1: 0}
		times := map[int]int64{40: 600, 30: 500, 20: 900, 10: 100, 1: 1}
		chain, err := walkAncestors(40, snapshotParentReader(parents, createdFrom(times)))
		if err != nil || !reflect.DeepEqual(chain, []int{40, 30}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a parent with an unreadable creation time ends the chain at the child", func(t *testing.T) {
		parents := map[int]int{30: 20, 20: 10, 10: 1, 1: 0}
		times := map[int]int64{30: 500, 10: 100, 1: 1} // 20 unreadable
		chain, err := walkAncestors(30, snapshotParentReader(parents, createdFrom(times)))
		if err != nil || !reflect.DeepEqual(chain, []int{30}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a parent created at the same instant as its child is accepted", func(t *testing.T) {
		parents := map[int]int{30: 20, 20: 1, 1: 0}
		times := map[int]int64{30: 500, 20: 500, 1: 1}
		chain, err := walkAncestors(30, snapshotParentReader(parents, createdFrom(times)))
		if err != nil || !reflect.DeepEqual(chain, []int{30, 20, 1}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a long chain of legitimate parents reaches the root", func(t *testing.T) {
		parents := map[int]int{1: 0}
		times := map[int]int64{1: 1}
		want := []int{}
		for pid := 1040; pid > 1000; pid-- {
			parents[pid] = pid - 1
			times[pid] = int64(pid)
			want = append(want, pid)
		}
		parents[1000] = 1
		times[1000] = 1000
		want = append(want, 1000, 1)
		chain, err := walkAncestors(1040, snapshotParentReader(parents, createdFrom(times)))
		if err != nil || !reflect.DeepEqual(chain, want) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a start pid with an unreadable creation time is a read failure", func(t *testing.T) {
		parents := map[int]int{30: 20, 20: 1, 1: 0}
		times := map[int]int64{20: 100, 1: 1} // 30 unreadable
		chain, err := walkAncestors(30, snapshotParentReader(parents, createdFrom(times)))
		if !errors.Is(err, errAncestorRead) || !reflect.DeepEqual(chain, []int{30}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a parentless start pid with an unreadable creation time is a read failure", func(t *testing.T) {
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{30: 0}, createdFrom(nil)))
		if !errors.Is(err, errAncestorRead) || !reflect.DeepEqual(chain, []int{30}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a parent missing from the snapshot still joins the chain and ends it", func(t *testing.T) {
		// 20 already exited: no creation time to read, nothing to compare.
		parents := map[int]int{40: 30, 30: 20}
		times := map[int]int64{40: 600, 30: 500}
		chain, err := walkAncestors(40, snapshotParentReader(parents, createdFrom(times)))
		if err != nil || !reflect.DeepEqual(chain, []int{40, 30, 20}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("reused pids that overflowed the hop limit now stop at the first newer parent", func(t *testing.T) {
		// 2000 <- 2001 is legitimate; from 2002 on every pid was reused by
		// a process created later, each one the "parent" of the previous.
		parents := map[int]int{}
		times := map[int]int64{2000: 500, 2001: 400}
		for pid := 2000; pid < 2000+3*ancestorHopLimit; pid++ {
			parents[pid] = pid + 1
			if pid >= 2002 {
				times[pid] = int64(pid)
			}
		}
		// The control: without the creation time the same snapshot
		// overflows the hop limit.
		if _, err := walkAncestors(2000, snapshotParentReader(parents, nil)); !errors.Is(err, errAncestorLimit) {
			t.Fatalf("control: err=%v, want the hop limit", err)
		}
		chain, err := walkAncestors(2000, snapshotParentReader(parents, createdFrom(times)))
		if err != nil || !reflect.DeepEqual(chain, []int{2000, 2001}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
}

// OverrideAncestorParents drives ProcessAncestors on every system and
// restores the provider; a pid outside the map is a read failure.
func TestOverrideAncestorParents(t *testing.T) {
	restore := OverrideAncestorParents(map[int]int{500: 400, 400: 1})
	chain, err := ProcessAncestors(500, Env{})
	if err != nil || !reflect.DeepEqual(chain, []int{500, 400, 1}) {
		t.Fatalf("chain=%v err=%v", chain, err)
	}
	if _, err := ProcessAncestors(501, Env{}); !errors.Is(err, errAncestorRead) {
		t.Fatalf("a pid outside the map: err=%v", err)
	}
	// Unlike a snapshot, a parent outside the map is a failed read here.
	restore2 := OverrideAncestorParents(map[int]int{500: 400})
	if chain, err := ProcessAncestors(500, Env{}); !errors.Is(err, errAncestorRead) || !reflect.DeepEqual(chain, []int{500, 400}) {
		t.Fatalf("chain=%v err=%v", chain, err)
	}
	restore2()
	if chain, err := ProcessAncestors(500, Env{}); err != nil || !reflect.DeepEqual(chain, []int{500, 400, 1}) {
		t.Fatalf("nested restore: chain=%v err=%v", chain, err)
	}
	restore()
	if overriddenAncestorReader() != nil {
		t.Fatal("the override is still active after restore")
	}
}
