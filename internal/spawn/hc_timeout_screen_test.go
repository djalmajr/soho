package spawn

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestSpawnHcNeverReadyNamesTheScreenBeforeRemoval: a native worker that
// never reports ready ends spawn with exit 4 and a message that carries the
// last visible line of its screen. The screen read is the Core call before
// the terminal's removal (kill, then rm), and spawn sends no input.
func TestSpawnHcNeverReadyNamesTheScreenBeforeRemoval(t *testing.T) {
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "pi")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"Trust project folder?\n  1. Yes, trust\n  2. No, exit\n\n","complete":true}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi", "--timeout", "300"}, ctx, env, cwd, conn)
	want := `agent worker still not ready after 0.3s; last visible line: "2. No, exit"`
	if exit == nil || exit.Code != 4 || exit.Msg != want {
		t.Fatalf("exit=%+v, want exit 4 with %q", exit, want)
	}
	read, kill, rm, reads := -1, -1, -1, 0
	for i, c := range calls {
		switch c.Argv[0] {
		case "read":
			read = i
			reads++
		case "kill":
			kill = i
		case "rm":
			rm = i
		case "send", "keys":
			t.Fatalf("spawn sent input to the terminal: %v", c.Argv)
		}
	}
	if reads != 1 || read < 0 || kill < 0 || rm < 0 || !(read < kill && kill < rm) {
		t.Fatalf("read=%d kill=%d rm=%d reads=%d, want exactly one read before kill before rm: %v", read, kill, rm, reads, calls)
	}
	if strings.Count(exit.Msg, "last visible line") != 1 {
		t.Fatalf("message %q repeats the line", exit.Msg)
	}
}
