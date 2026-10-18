package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestTM11RegridFailureParity(t *testing.T) {
	t.Run(`JS: "parity: regrid exits 4 on a failed move, with the bash message and friction entry"`, func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("..", "testdata", "legacy", "parity-regrid.json"))
		if err != nil {
			t.Fatal(err)
		}
		var goldens map[string]struct {
			Err   string             `json:"err"`
			Files map[string]*string `json:"files"`
			Out   string             `json:"out"`
			RC    int                `json:"rc"`
		}
		if err := json.Unmarshal(data, &goldens); err != nil {
			t.Fatal(err)
		}
		want, ok := goldens["fail"]
		if !ok {
			t.Fatal("parity-regrid golden fail is missing")
		}
		rules := []fakecli.Rule{
			{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 1, Stdout: `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"}]}}`},
			{Argv: []string{"agent", "get", "w1"}, Stdout: `{"result":{"agent":{"name":"w1","agent_status":"idle"}}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Call: 2, Stdout: `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"p1","tab_id":"t0"}]}}`},
			{Argv: []string{"pane", "move", "p1", "--new-tab", "--label", "herd-park", "--no-focus"}, Code: 1},
		}
		env, cwd, state := regridCommandFixture(t, rules)
		env["HERDR_TAB_ID"], env["HERDR_PANE_ID"] = "t0", "C"
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\nw1\tp1\tgrok\timplementer\txai\t1\t/tmp\t\n\n"
		if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "herd-tab"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"regrid"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != want.RC || out.String() != want.Out || normalizeParityError(stderr.String()) != want.Err {
			t.Fatalf("code=%d want=%d stdout=%q want=%q stderr=%q want=%q", code, want.RC, out.String(), want.Out, normalizeParityError(stderr.String()), want.Err)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		var log strings.Builder
		for _, call := range calls {
			log.WriteString(strings.Join(call.Argv, " "))
			log.WriteByte('\n')
		}
		// DJA-394: the caller has HERDR_PANE_ID, so the regrid reads its
		// pane's shell pid first (the fake has no rule for it: unknown,
		// and the step's output and files are unchanged).
		expectedLog := testutil.RewriteCallerCheckLog(want.Files["herdr.log"], "C")
		if expectedLog != nil && log.String() != *expectedLog {
			t.Fatalf("herdr log=%q want=%q", log.String(), *expectedLog)
		}
		if actual, err := os.ReadFile(filepath.Join(state, "agents.tsv")); err != nil || string(actual) != *want.Files["state/ws/agents.tsv"] {
			t.Fatalf("roster=%q err=%v want=%q", actual, err, *want.Files["state/ws/agents.tsv"])
		}
		friction, err := os.ReadFile(filepath.Join(state, "friction.log"))
		if err != nil || len(friction) == 0 || !strings.Contains(string(friction), "error(exit 4)\tregrid\tregrid: could not park the workers of tab t0 in a temporary tab") {
			t.Fatalf("friction=%q err=%v", friction, err)
		}
		_ = cwd
	})
}
