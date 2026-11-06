package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// dispatch_git_policy_test.go drives the real dispatch command (no provider,
// no network: the fake herdr of the arrival fixture) and reads the prompt it
// actually composed, to pin the Git instruction the worker receives per
// dispatcher-resolved role.

const (
	workerGitBan   = "- Do not commit, push, tag, or open pull requests.\n"
	jobGitRulePart = "commit integrated changes only inside this job's own worktree when the job brief requires them"
)

// gitPolicyFixture is the arrival fixture with the role files and a roster row
// chosen by the test, and a clock that gives every dispatch its own stamp.
func gitPolicyFixture(t *testing.T, rosterRole string) *dispatchArrivalFixture {
	t.Helper()
	f := newDispatchArrivalFixture(t, "working", 5, 5, "$CURRENT_PATHS", "20")
	roles := f.env["SOHO_ROLES"]
	for _, name := range []string{"job-orchestrator", "reviewer", "sub-orchestrator", "mystery"} {
		body := "---\nname: " + name + "\nmode: edit\n---\nBody of " + name + ".\n"
		if err := os.WriteFile(filepath.Join(roles, name+".md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A frontmatter display name that claims the job role must not select it.
	spoof := "---\nname: job-orchestrator\nmode: edit\n---\nSpoofing display name.\n"
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte(spoof), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"worker\tw0test:p0a\tcodex\t" + rosterRole + "\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n" +
		"job-orchestrator\tw0test:p0b\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	oldNow := platform.Now
	clock := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	platform.Now = func() time.Time {
		value := clock
		clock = clock.Add(2 * time.Second)
		return value
	}
	t.Cleanup(func() { platform.Now = oldNow })
	return f
}

// composedFor returns the composed prompts written so far for an agent, oldest
// first.
func composedFor(t *testing.T, f *dispatchArrivalFixture, agent string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(f.state, "ws", "briefs", agent+"-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(matches)
	out := make([]string, 0, len(matches))
	for _, path := range matches {
		out = append(out, mustRead(t, path))
	}
	return out
}

// dispatchBoth dispatches an initial brief and then an amendment for the
// agent and returns the two composed prompts the worker received.
func dispatchBoth(t *testing.T, f *dispatchArrivalFixture, agent string, roleArgs ...string) (string, string) {
	t.Helper()
	args := append([]string{agent, f.brief, "--no-wait"}, roleArgs...)
	if code, out, errText := f.run(t, args...); code != 0 {
		t.Fatalf("dispatch code=%d out=%s stderr=%s", code, out, errText)
	}
	amend := append([]string{agent, f.brief, "--no-wait", "--amend"}, roleArgs...)
	if code, out, errText := f.run(t, amend...); code != 0 {
		t.Fatalf("amend code=%d out=%s stderr=%s", code, out, errText)
	}
	prompts := composedFor(t, f, agent)
	if len(prompts) != 2 {
		t.Fatalf("composed prompts = %d, want the initial brief and the amendment", len(prompts))
	}
	if strings.Contains(prompts[0], "# Amendment to your current brief") || !strings.HasPrefix(prompts[1], "# Amendment to your current brief") {
		t.Fatalf("composed order is not initial then amendment")
	}
	return prompts[0], prompts[1]
}

// The canonical job-orchestrator may commit inside its own job worktree; the
// supervisor owns publication. Both composed prompts carry that rule and not
// the blanket ban that contradicts the role.
func TestDispatchJobOrchestratorGetsTheJobLocalGitRule(t *testing.T) {
	f := gitPolicyFixture(t, "implementer")
	initial, amended := dispatchBoth(t, f, "worker", "--role", "job-orchestrator")
	for name, prompt := range map[string]string{"initial": initial, "amendment": amended} {
		if strings.Contains(prompt, workerGitBan) {
			t.Errorf("%s prompt still carries the blanket ban for the job-orchestrator role", name)
		}
		if !strings.Contains(prompt, jobGitRulePart) {
			t.Errorf("%s prompt lacks the job-local commit rule", name)
		}
		for _, forbidden := range []string{"Do not push, tag, merge, or open pull requests", "the job supervisor owns publication"} {
			if !strings.Contains(prompt, forbidden) {
				t.Errorf("%s prompt lacks %q", name, forbidden)
			}
		}
		// The other report rules and their order stay.
		if strings.Index(prompt, "- Stop only processes you started") > strings.Index(prompt, "As job-orchestrator") ||
			!strings.HasSuffix(strings.TrimSpace(prompt), "reply in the terminal with exactly the report path and nothing else.") {
			t.Errorf("%s prompt reorders the standing rules", name)
		}
	}
}

// Every other role keeps the exact blanket ban in both prompts, whatever the
// agent is called or the role file's display name says.
func TestDispatchOtherRolesKeepTheBlanketGitBan(t *testing.T) {
	cases := []struct {
		name, agent, role string
		rosterRole        string
	}{
		{"implementer", "worker", "implementer", "implementer"},
		{"reviewer", "worker", "reviewer", "implementer"},
		{"sub-orchestrator", "worker", "sub-orchestrator", "implementer"},
		{"unknown role", "worker", "mystery", "implementer"},
		{"roster role implementer whose display name claims the job role", "worker", "", "implementer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := gitPolicyFixture(t, tc.rosterRole)
			var roleArgs []string
			if tc.role != "" {
				roleArgs = []string{"--role", tc.role}
			}
			initial, amended := dispatchBoth(t, f, tc.agent, roleArgs...)
			for name, prompt := range map[string]string{"initial": initial, "amendment": amended} {
				if strings.Count(prompt, workerGitBan) != 1 {
					t.Errorf("%s prompt: blanket ban count = %d, want exactly 1", name, strings.Count(prompt, workerGitBan))
				}
				if strings.Contains(prompt, jobGitRulePart) {
					t.Errorf("%s prompt grants the job-local commit rule to %s", name, tc.name)
				}
			}
		})
	}
}

// An explicit prohibition in the caller's raw brief stays verbatim, next to
// the job rule, and nothing is stripped to force a pass.
func TestDispatchJobOrchestratorKeepsAnExplicitBriefProhibition(t *testing.T) {
	f := gitPolicyFixture(t, "implementer")
	brief := filepath.Join(f.root, "brief-prohibit.md")
	body := "# Goal\n\nDo the work.\n# Forbidden\n\nNo commit or push by you in this slice.\n"
	if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errText := f.run(t, "worker", brief, "--no-wait", "--role", "job-orchestrator"); code != 0 {
		t.Fatalf("dispatch code=%d out=%s stderr=%s", code, out, errText)
	}
	prompts := composedFor(t, f, "worker")
	if len(prompts) != 1 || !strings.Contains(prompts[0], "No commit or push by you in this slice.") {
		t.Fatalf("the brief's own prohibition was not kept verbatim")
	}
}
