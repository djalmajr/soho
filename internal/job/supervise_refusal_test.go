package job

import (
	"testing"
	"time"
)

func TestReview370RefusalResetsGoneStreak(t *testing.T) {
	for _, alive := range []string{"provider-error", "provider-refusal"} {
		t.Run(alive, func(t *testing.T) {
			var friction []string
			store, _, _, team, sup := nsSup(t, time.Second, &friction)
			if _, e := store.Record("job-1", func(st *State) { st.Orchestrator = "orch-1" }); e != nil {
				t.Fatal(e)
			}
			team.statusScript = []fakeStatus{{State: "gone"}, {State: "gone"}, {State: alive}, {State: "gone"}}
			for i := 0; i < 4; i++ {
				code, done, e := sup.checkOrchestrator()
				st := supState(t, store)
				t.Logf("observation=%d code=%d done=%t err=%v gone_streak=%d job_status=%s motivo=%v", i+1, code, done, e, sup.goneStreak, st.Status, st.Motivo)
				if e != nil {
					t.Fatal(e)
				}
				if i == 2 && (st.Status != StatusBlocked || st.Motivo == nil || *st.Motivo != "provider_error") {
					t.Fatalf("refusal did not enter provider_error block: %+v", st)
				}
				if done {
					t.Fatalf("job terminated on one gone observation after an alive %s; expected consecutive-gone count to restart", alive)
				}
			}
			if sup.goneStreak != 1 {
				t.Fatalf("gone_streak=%d want 1", sup.goneStreak)
			}
		})
	}
}
