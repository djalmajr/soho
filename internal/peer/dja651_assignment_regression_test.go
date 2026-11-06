package peer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func dja651AssignmentRow(t *testing.T, f *fixture, sd string, a collaboration.Assignment, status string) string {
	t.Helper()
	prompts := assignmentPrompts(t, f)
	if len(prompts) != 1 || len(prompts[0].Argv) != 4 || prompts[0].Argv[2] != a.Reviewer.Pane {
		t.Fatalf("assignment must submit once without waiting: %+v", prompts)
	}
	message := prompts[0].Argv[3]
	first, _, _ := strings.Cut(message, "\n")
	id := strings.TrimSuffix(strings.TrimPrefix(first, "[soho:peer:"), "]")
	if len(id) != 8 || !strings.HasSuffix(message, "> question\n"+peer.PeerEndLine(id)) {
		t.Fatalf("unexpected assignment prompt: %q", message)
	}
	cols := dja651Log(t, sd, status, id, "local/"+a.Author.Pane, "local/"+a.Reviewer.Pane)
	if len(cols) != 9 || cols[4] != "8" || cols[6] != a.ID || cols[7] != "review.question" || cols[8] != fmt.Sprint(a.Round) {
		t.Errorf("assignment TSV metadata=%q; want assignment=%s type=review.question round=%d", cols, a.ID, a.Round)
	}
	dja651Submission(t, f, message, 0)
	return id
}

func TestDJA651AssignmentUncertainSavesPreSubmitScreen(t *testing.T) {
	screen := "PRIVATE-PRE-SUBMIT-SCREEN\nWorker is editing files\n"
	f, s, a := assignedFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: screen},
		{Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Code: 1, Stderr: "connection disappeared"},
	})
	dja651Isolate(f)
	code, out, stderr := f.run([]string{"send", "reviewer", "question", "--assignment", a.ID, "--type", "review.question", "--timeout", "1s"})
	id := dja651AssignmentRow(t, f, s.StateDir, a, "uncertain")
	if code != 15 || out != "" || !strings.HasPrefix(stderr, "soho: send: connection disappeared") || !strings.HasSuffix(stderr, "\n") {
		t.Errorf("uncertain assignment must retain its transport cause: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !strings.Contains(stderr, "pre-submit") && !strings.Contains(stderr, "before submission") && !strings.Contains(stderr, "at submission time") {
		t.Errorf("assignment snapshot must be labeled as pre-submit, never a post-submit receipt: %q", stderr)
	}
	dja651Screen(t, s.StateDir, id, screen, out, stderr, true)
	// The only visible read was before the one prompt; saving must not invent
	// a post-submit read or change the assignment's one-submit policy.
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	reads, prompted := 0, false
	for _, c := range calls {
		if len(c.Argv) > 1 && c.Argv[1] == "prompt" {
			prompted = true
		}
		if len(c.Argv) > 1 && c.Argv[1] == "read" {
			reads++
			if prompted {
				t.Error("assignment diagnostic read after submission instead of saving the available pre-submit screen")
			}
		}
	}
	if reads != 1 {
		t.Errorf("visible reads=%d; want one pre-submit read", reads)
	}
}

func TestDJA651AssignmentNoInventedScreenControls(t *testing.T) {
	t.Run("save_failure", func(t *testing.T) {
		screen := "PRIVATE-SAVE-FAILURE-SCREEN\n"
		f, s, a := assignedFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: screen},
			{Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Code: 1, Stderr: "connection disappeared"},
		})
		dja651Isolate(f)
		if err := os.WriteFile(filepath.Join(s.StateDir, "wait"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.run([]string{"send", "reviewer", "question", "--assignment", a.ID, "--type", "review.question", "--timeout", "1s"})
		id := dja651AssignmentRow(t, f, s.StateDir, a, "uncertain")
		if code != 15 || out != "" || stderr != "soho: send: connection disappeared\n" {
			t.Errorf("save failure must retain only the real cause: code=%d out=%q stderr=%q", code, out, stderr)
		}
		dja651Screen(t, s.StateDir, id, screen, out, stderr, false)
	})
	t.Run("unavailable", func(t *testing.T) {
		f, s, a := assignedFixture(t, []fakecli.Rule{{Argv: []string{"agent", "read"}, ArgvPrefix: true, Code: 1, Stderr: "fixture screen unavailable"}})
		dja651Isolate(f)
		code, out, stderr := f.run([]string{"send", "reviewer", "question", "--assignment", a.ID, "--type", "review.question", "--timeout", "1s"})
		if code != 2 || out != "" || stderr != "soho: send: cannot inspect recipient screen: fixture screen unavailable\n" {
			t.Errorf("unavailable screen must preserve refusal/cause: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if len(assignmentPrompts(t, f)) != 0 || countSendKeyEnters(t, f) != 0 {
			t.Error("unavailable screen must refuse before any prompt or Enter")
		}
		if _, err := os.Stat(filepath.Join(s.StateDir, peer.PeerLogFile)); !os.IsNotExist(err) {
			t.Errorf("pre-submit screen refusal unexpectedly wrote a delivery row: %v", err)
		}
		dja651Screen(t, s.StateDir, "not-submitted", "", out, stderr, false)
	})
	t.Run("hc_has_no_visible_screen", func(t *testing.T) {
		f, s, a := assignedFixture(t, nil)
		dja651Isolate(f)
		// Reuse the assignment fixture, rebinding only its synthetic transport
		// identities as the existing hc assignment fixtures do.
		var err error
		a, err = s.Update(a.ID, a.Generation, func(current *collaboration.Assignment) error {
			current.Author.Pane, current.Reviewer.Pane = "t7", "t8"
			current.Author.Session = collaboration.Hash([]byte("hc\x00t7"))
			current.Reviewer.Session = collaboration.Hash([]byte("hc\x00t8"))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		roster := fmt.Sprintf("author\tt7\tcodex\timplementer\topenai\t\t%s\tfirst\tgpt-5.4\thigh\tmain\tbuild\nreviewer\tt8\tclaude\treviewer\tanthropic\t\t%s\tsecond\tclaude-opus-5-5\tmedium\tmain\treview\n", f.dir, f.dir)
		if err := os.WriteFile(filepath.Join(s.StateDir, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		record := func(name, address string) string {
			return fmt.Sprintf(`{"address":%q,"name":%q,"state":"live","labels":{"soho.session":"ws-test"},"cwd":%q,"status":{"state":"working","turn":1}}`, address, name, f.dir)
		}
		rules := []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Stdout: record("author", "t7")},
			{Argv: []string{"get", "author", "--json"}, Stdout: record("author", "t7")},
			{Argv: []string{"get", "reviewer", "--json"}, Stdout: record("reviewer", "t8")},
			{Argv: []string{"get", "t8", "--json"}, Stdout: record("reviewer", "t8")},
			{Argv: []string{"send"}, ArgvPrefix: true, Code: 1, Stderr: "connection disappeared"},
		}
		if _, err := fakecli.Install(t, filepath.Dir(f.bin), "hc", rules); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_TRANSPORT"], f.env["HC_TERMINAL_ID"] = "hc", "t7"
		delete(f.env, "HERDR_PANE_ID")
		before, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		// CLI send deliberately rejects hc; the public assignment consumer
		// supports it and returns delivery status/error rather than an exit.
		cfg := core.LoadConfig(f.env, f.dir)
		result, deliveryErr := peer.SendAssignmentMessage(peer.AssignmentMessageOptions{
			AssignmentID: a.ID,
			Target:       "reviewer",
			Type:         "review.question",
			Body:         "question",
			TimeoutMS:    1000,
			Config:       &cfg,
			Env:          f.env,
			Cwd:          f.dir,
		})
		const cause = "hc exited 1: connection disappeared"
		if result.Status != "uncertain" || result.Cause != cause || deliveryErr == nil || deliveryErr.Error() != cause {
			t.Errorf("hc assignment must return uncertainty and its actual cause without a screen: result=%+v err=%v", result, deliveryErr)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(filepath.Dir(f.bin), "hc.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		sends, id := 0, ""
		for _, c := range calls {
			if len(c.Argv) > 0 && c.Argv[0] == "send" {
				sends++
				if len(c.Argv) != 5 || strings.Join(c.Argv[:4], " ") != "send --json -- t8" {
					t.Fatalf("hc must queue exactly one prompt without a key: %q", c.Argv)
				}
				first, _, _ := strings.Cut(c.Argv[4], "\n")
				id = strings.TrimSuffix(strings.TrimPrefix(first, "[soho:peer:"), "]")
				if !strings.HasSuffix(c.Argv[4], "> question\n"+peer.PeerEndLine(id)) {
					t.Errorf("hc assignment body changed: %q", c.Argv[4])
				}
			} else if len(c.Argv) > 0 && c.Argv[0] != "get" {
				t.Errorf("hc uncertainty must send no keys or other commands: %q", c.Argv)
			}
		}
		if sends != 1 {
			t.Fatalf("hc sends=%d; want exactly one", sends)
		}
		after, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil || len(after) != len(before) {
			t.Errorf("hc assignment unexpectedly called Herdr: before=%d after=%d err=%v", len(before), len(after), err)
		}
		cols := dja651Log(t, s.StateDir, "uncertain", id, "local/t7", "local/t8")
		if len(cols) != 9 || cols[4] != "8" || cols[6] != a.ID || cols[7] != "review.question" || cols[8] != fmt.Sprint(a.Round) {
			t.Errorf("hc assignment TSV metadata=%q; want assignment=%s type=review.question round=%d", cols, a.ID, a.Round)
		}
		dja651Screen(t, s.StateDir, id, "", "", result.Cause, false)
	})
}
