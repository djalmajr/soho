package inventory

import (
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestInventoryCollectInvalidCwdNeverQueriesGit(t *testing.T) {
	for _, cwd := range []string{"", ".", "..", "relative/project"} {
		t.Run("cwd="+cwd, func(t *testing.T) {
			env, _, _ := inventoryFixture(t)
			fakeEnv, log := withFakeHerdr(t, env, []fakecli.Rule{
				herdrList(t, herdrAgentEntry("worker", "p1", "claude", "ws", cwd)),
			})
			if _, err := fakecli.Install(t, filepath.Dir(log), "git", nil); err != nil {
				t.Fatal(err)
			}
			doc, err := collect(t, fakeEnv, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Projects) != 0 {
				t.Fatal("an invalid observed cwd produced a project")
			}
			if calls := readCalls(t, filepath.Join(filepath.Dir(log), "git.calls.jsonl")); len(calls) != 0 {
				t.Fatalf("an invalid observed cwd queried Git: %+v", calls)
			}
			if calls := readCalls(t, log); len(calls) != 1 {
				t.Fatalf("wanted only the initial listing, got %+v", calls)
			}
		})
	}
}
