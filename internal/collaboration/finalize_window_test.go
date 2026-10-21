package collaboration

import "testing"

func TestFinalizeRejectsGenerationChangeBeforeUpdate(t *testing.T) {
	s, a := assignmentFixture(t)
	escalated, err := s.Stop(a.ID)
	if err != nil {
		t.Fatal(err)
	}

	previousHook := finalizeBeforeUpdateHook
	t.Cleanup(func() { finalizeBeforeUpdateHook = previousHook })
	calls := 0
	var external Assignment
	finalizeBeforeUpdateHook = func(id string) {
		calls++
		if id != a.ID || calls != 1 {
			t.Fatalf("finalize hook: id=%q calls=%d, want id=%q calls=1", id, calls, a.ID)
		}
		// Finalize has already read its generation. This external transition
		// must make that exact read stale before Finalize calls Update.
		var err error
		external, err = s.Stop(id)
		if err != nil {
			t.Fatal(err)
		}
	}

	_, err = s.Finalize(a.ID, "reject")
	const conflict = "collaboration generation changed; inspect status before retrying"
	if err == nil || err.Error() != conflict {
		t.Fatalf("finalize conflict = %v, want %q", err, conflict)
	}
	if calls != 1 || external.Generation != escalated.Generation+1 {
		t.Fatalf("external transition: calls=%d generation=%d, want calls=1 generation=%d", calls, external.Generation, escalated.Generation+1)
	}
	current, err := s.Read(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Phase != Escalated || current.Verdict != "" || current.Generation != external.Generation {
		t.Fatalf("after conflict: phase=%s verdict=%q generation=%d, want escalated/empty generation=%d", current.Phase, current.Verdict, current.Generation, external.Generation)
	}

	// The same public API succeeds after removing the competing transition.
	finalizeBeforeUpdateHook = nil
	finalized, err := s.Finalize(a.ID, "reject")
	if err != nil {
		t.Fatalf("finalize without concurrent transition: %v", err)
	}
	current, err = s.Read(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []Assignment{finalized, current} {
		if result.Phase != Finished || result.Verdict != "reject" || result.Generation != external.Generation+1 {
			t.Fatalf("after retry: phase=%s verdict=%q generation=%d, want finished/reject generation=%d", result.Phase, result.Verdict, result.Generation, external.Generation+1)
		}
	}
	if calls != 1 {
		t.Fatalf("hook calls after retry = %d, want 1", calls)
	}
}
