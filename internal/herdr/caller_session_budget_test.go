package herdr

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// caller_session_budget_test.go covers the budget of the pane suggestion
// of a refusal (DJA-412): the search lists <root>/projects once, reads at
// most suggestMaxReads transcripts the check has not read yet and spends
// at most suggestMaxTime, and once the budget runs out it names no pane.
// The verification of the claimed pane spends no budget.

// budgetSession is the session of the i-th synthetic pane.
func budgetSession(i int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", 0xb0000000+i, i)
}

// countReads counts the listings of projects/ and the transcript reads
// until the test ends; after, when set, runs after each transcript read.
func countReads(t *testing.T, after func(n int)) (listings, reads *int) {
	t.Helper()
	listings, reads = new(int), new(int)
	list, tail := listProjects, readTranscriptTail
	t.Cleanup(func() { listProjects, readTranscriptTail = list, tail })
	listProjects = func(dir string) ([]os.DirEntry, error) {
		*listings++
		return list(dir)
	}
	readTranscriptTail = func(path string, max int64) ([]byte, bool) {
		*reads++
		data, ok := tail(path, max)
		if after != nil {
			after(*reads)
		}
		return data, ok
	}
	return listings, reads
}

// fixedClock pins platform.Now until the test ends and returns the step
// that moves it forward.
func fixedClock(t *testing.T) func(time.Duration) {
	t.Helper()
	now := platform.Now
	t.Cleanup(func() { platform.Now = now })
	clock := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	platform.Now = func() time.Time { return clock }
	return func(d time.Duration) { clock = clock.Add(d) }
}

// manyPanes is 40 Claude panes w5:p00..w5:p39, each with its own session;
// S replaces the session of the pane at index ownAt (-1: none).
func manyPanes(ownAt int) [][2]string {
	panes := make([][2]string, 0, 40)
	for i := 0; i < 40; i++ {
		session := budgetSession(i)
		if i == ownAt {
			session = sessOwn
		}
		panes = append(panes, [2]string{fmt.Sprintf("w5:p%02d", i), session})
	}
	return panes
}

// writeNoTranscripts gives every pane session other than S a transcript
// without continued-in records.
func writeNoTranscripts(t *testing.T, env platform.Env, panes [][2]string) {
	t.Helper()
	for _, p := range panes {
		if p[1] != sessOwn {
			writeSessionTranscript(t, env, "-p", p[1], noise(2)...)
		}
	}
}

func TestCheckCallerPaneSuggestionBudget(t *testing.T) {
	// The claimed pane hosts session sessOther, which has no transcript.
	claimedRule := paneGetRule(sessPane, "claude", "id", sessOther)

	t.Run("40 panes without S: no suggestion, at most 32 reads, one listing", func(t *testing.T) {
		panes := manyPanes(-1)
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule(panes...))
		writeNoTranscripts(t, env, panes)
		listings, reads := countReads(t, nil)
		got := CheckCallerPane(env)
		if got.State != CallerMismatch || got.ShownIn != "" || got.ShellPID != sessShell {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		if *reads == 0 || *reads > suggestMaxReads {
			t.Fatalf("transcript reads=%d want 1..%d", *reads, suggestMaxReads)
		}
		if *listings != 1 {
			t.Fatalf("projects listings=%d want 1", *listings)
		}
	})

	t.Run("40 panes where only the last examined shows S: no suggestion", func(t *testing.T) {
		panes := manyPanes(39)
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule(panes...))
		writeNoTranscripts(t, env, panes)
		if got := CheckCallerPane(env); got.State != CallerMismatch || got.ShownIn != "" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("40 panes where the first examined shows S: no suggestion either", func(t *testing.T) {
		panes := manyPanes(0)
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule(panes...))
		writeNoTranscripts(t, env, panes)
		if got := CheckCallerPane(env); got.State != CallerMismatch || got.ShownIn != "" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
	})

	t.Run("3 panes, one of them leading to S: that pane is named", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule(
			[2]string{"w2:p1", budgetSession(1)}, [2]string{"w2:p2", sessPaneA}, [2]string{"w2:p3", budgetSession(3)}))
		writeSessionTranscript(t, env, "-p", budgetSession(1), noise(2)...)
		writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessOwn))
		writeSessionTranscript(t, env, "-p", budgetSession(3), noise(2)...)
		listings, reads := countReads(t, nil)
		if got := CheckCallerPane(env); got.State != CallerMismatch || got.ShownIn != "w2:p2" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		if *reads != 3 || *listings != 1 {
			t.Fatalf("transcript reads=%d listings=%d want 3 and 1", *reads, *listings)
		}
	})

	t.Run("the clock passing 2 s after the first read leaves no suggestion", func(t *testing.T) {
		env := sessionEnv(t, sessOwn, claimedRule, paneListRule(
			[2]string{"w2:p1", sessPaneA}, [2]string{"w2:p2", budgetSession(2)}, [2]string{"w2:p3", budgetSession(3)}))
		writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessOwn))
		writeSessionTranscript(t, env, "-p", budgetSession(2), noise(2)...)
		writeSessionTranscript(t, env, "-p", budgetSession(3), noise(2)...)
		step := fixedClock(t)
		_, reads := countReads(t, func(n int) {
			if n == 1 {
				step(3 * time.Second)
			}
		})
		if got := CheckCallerPane(env); got.State != CallerMismatch || got.ShownIn != "" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		if *reads != 1 {
			t.Fatalf("transcript reads=%d want 1", *reads)
		}
	})

	t.Run("the claimed pane is still verified two hops away with 40 panes", func(t *testing.T) {
		// Every read moves the clock 3 s: a budget on the verification
		// would refuse the second hop.
		panes := manyPanes(-1)
		env := sessionEnv(t, sessOwn, paneGetRule(sessPane, "claude", "id", sessPaneA), paneListRule(panes...))
		writeNoTranscripts(t, env, panes)
		writeSessionTranscript(t, env, "-p", sessPaneA, continuedIn(sessPaneA, sessMid))
		writeSessionTranscript(t, env, "-p", sessMid, continuedIn(sessMid, sessOwn))
		step := fixedClock(t)
		_, reads := countReads(t, func(int) { step(3 * time.Second) })
		got := CheckCallerPane(env)
		if got.State != CallerVerified || got.Reason != "the pane's Claude Code session continued in this process's session" {
			t.Fatalf("CheckCallerPane=%#v", got)
		}
		if *reads != 2 {
			t.Fatalf("transcript reads=%d want 2", *reads)
		}
	})
}
