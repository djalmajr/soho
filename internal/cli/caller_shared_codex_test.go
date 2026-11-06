package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestInitSharedCodexRefusesBeforeNaming(t *testing.T) {
	herdr.ResetCallerCheck()
	t.Cleanup(herdr.ResetCallerCheck)
	env, cwd := commandFixture(t)
	bin := t.TempDir()
	shell, _ := callerShellPIDs()
	const id = "66bf36b5-9b48-4b58-88a3-5da128ff288b"
	rules := []fakecli.Rule{
		{Argv: []string{"pane", "process-info", "--pane", "w1:p9"}, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, shell)},
		{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[{"pane_id":"w2:p8","agent_session":{"agent":"codex","kind":"id","value":"` + id + `"}}]}}`},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env["PATH"], env["SOHO_FAKECLI_CONFIG"] = bin, bin
	env["HERDR_ENV"], env["HERDR_PANE_ID"], env["CODEX_THREAD_ID"] = "1", "w1:p9", id
	scriptCallerTree(t, false)
	t.Cleanup(platform.OverrideProcExecutableRunner(func(pid int) (string, error) {
		if pid == os.Getpid() || pid == shell {
			return filepath.Join(bin, "codex"), nil
		}
		return "", errors.New("synthetic missing executable")
	}))
	state := core.StateDir(&core.Config{}, env, cwd)
	snapshot := func() map[string]string {
		out := map[string]string{}
		err := filepath.WalkDir(state, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || d.Name() == "friction.log" {
				return nil
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			rel, e := filepath.Rel(state, path)
			if e != nil {
				return e
			}
			out[rel] = string(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	code, out, stderr := runIn(t, []string{"init"}, env, cwd)
	want := "soho: init: HERDR_PANE_ID='w1:p9' cannot identify this Codex executor: its complete ancestry contains at least two Codex executables, so it may carry another pane's identity. This Codex session is shown in pane 'w2:p8'. Resume this agent in the intended pane with 'codex resume --no-daemon " + id + "'. Nothing was changed. For intentional nested codex exec, caller_check=warn is available.\n"
	if code != 2 || out != "" || stderr != want {
		t.Fatalf("init result: code=%d stdout=%q stderr=%q want=%q", code, out, stderr, want)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("refusal mutated state: before=%v after=%v", before, after)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := [][]string{{"pane", "process-info", "--pane", "w1:p9"}, {"pane", "list"}}
	var actual [][]string
	for _, c := range calls {
		actual = append(actual, c.Argv)
	}
	if !reflect.DeepEqual(actual, wantCalls) {
		t.Fatalf("init named, titled or mutated a pane: calls=%v", actual)
	}
	friction, err := os.ReadFile(filepath.Join(state, "friction.log"))
	if err != nil || strings.Count(string(friction), "\n") != 1 || !strings.Contains(string(friction), "codex resume --no-daemon "+id) {
		t.Fatalf("friction=%q err=%v", friction, err)
	}
}
