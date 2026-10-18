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
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{30: 20, 20: 10, 10: 1, 1: 0}))
		if err != nil || !reflect.DeepEqual(chain, []int{30, 20, 10, 1}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a parent that already exited ends the chain there", func(t *testing.T) {
		// 20 is gone from the snapshot, but 30 still records it as its parent.
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{30: 20}))
		if err != nil || !reflect.DeepEqual(chain, []int{30, 20}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a start pid missing from the snapshot is a read failure", func(t *testing.T) {
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{20: 10}))
		if !errors.Is(err, errAncestorRead) || !reflect.DeepEqual(chain, []int{30}) {
			t.Fatalf("chain=%v err=%v", chain, err)
		}
	})
	t.Run("a cycle stops the walk with an error", func(t *testing.T) {
		chain, err := walkAncestors(30, snapshotParentReader(map[int]int{30: 20, 20: 10, 10: 20}))
		if !errors.Is(err, errAncestorCycle) || !reflect.DeepEqual(chain, []int{30, 20, 10}) {
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
