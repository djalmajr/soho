package cli

import (
	"bytes"
	"fmt"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReview544BlockingDispatchKeepsReferenceParity(t *testing.T) {
	repo, sha, git := cliReferenceRepo(t, "old\n")
	f := roleDispatchFixture(t)
	f.env["PATH"] += string(os.PathListSeparator) + filepath.Dir(git)
	target := t.TempDir()
	writeRoleFixture(t, filepath.Join(target, "seed.sql"), "old\n")
	writeRoleFixture(t, f.brief, fmt.Sprintf("# Goal\nReview.\n# Expected result\nReview.\n# Forbidden\nNo commit/push.\n# Report\nReview.\nType: review\nWorktree: `%s`\nReference: %s@%s:seed.sql\n", target, repo, sha))
	idle := t.TempDir()
	if _, e := fakecli.Install(t, idle, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":6}}}`}}); e != nil {
		t.Fatal(e)
	}
	realWait := dispatchWaitFor
	defer func() { dispatchWaitFor = realWait }()
	producer := ""
	dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, quiet bool, cwd string) int {
		report := core.LastReport(sd, "worker")
		if e := os.WriteFile(report, []byte("<!-- report: completed -->\n## Reference parity\n- [same] seed.sql\n"), 0600); e != nil {
			t.Fatal(e)
		}
		waitEnv := env.Clone()
		waitEnv["PATH"], waitEnv["SOHO_FAKECLI_CONFIG"] = idle, idle
		old := platform.Stdout
		var b bytes.Buffer
		platform.Stdout = &b
		code := realWait(agents, sd, ctx, waitEnv, timeout, quiet, cwd)
		platform.Stdout = old
		producer = b.String()
		fmt.Fprint(platform.Stdout, producer)
		return code
	}
	code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--timeout", "5000")
	t.Logf("producer_wait_json=%s dispatch_exit=%d dispatch_json=%s stderr=%q", producer, code, out, msg)
	if code != 0 || !strings.Contains(producer, `"reference_parity":{"same":1,"diverges":0,"na":0,"unmarked":0}`) {
		t.Fatal("producer control did not settle with real parity metadata")
	}
	if !strings.Contains(out, `"reference_parity":{"same":1,"diverges":0,"na":0,"unmarked":0}`) {
		t.Fatal("blocking dispatch dropped parity metadata from the actual WaitFor result")
	}
}

func TestReferenceParityDuplicateDispatchResult(t *testing.T) {
	root := t.TempDir()
	report := filepath.Join(root, "report.md")
	writeRoleFixture(t, report, "<!-- report: completed -->\n")
	parity := jsonjs.O("same", 1, "diverges", 2, "na", 3, "unmarked", 4)
	for _, present := range []bool{true, false} {
		last := jsonjs.O("status", "done", "report", report)
		if present {
			last.Set("reference_parity", parity)
		}
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		code := emitDuplicateDispatchResult("worker", "reviewer", "review", &dispatchDuplicate{report: report, composed: "brief.md", duplicateOf: "original.md"}, last, 0, root)
		platform.Stdout = old
		want := `"reference_parity":{"same":1,"diverges":2,"na":3,"unmarked":4}`
		if code != 0 || strings.Contains(out.String(), want) != present {
			t.Fatalf("present=%t code=%d output=%s", present, code, out.String())
		}
	}
}
