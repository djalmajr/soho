package wait

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	refusalCybersecurity = "We take extra care with some cybersecurity requests"
	refusalCurly         = "This content can’t be shown"
	refusalStraight      = "This content can't be shown"
)

func TestProviderRefusalMarkerMatchesOnlyTheFixedTextInTheLastFortyLines(t *testing.T) {
	for _, tc := range []struct {
		name, screen, want string
	}{
		{"cybersecurity", "x\nWE TAKE EXTRA CARE WITH SOME CYBERSECURITY REQUESTS. More text", refusalCybersecurity},
		{"curly apostrophe", "■ This content can’t be shown", refusalCurly},
		{"straight apostrophe", "■ This content can't be shown", refusalStraight},
		{"surrounding hostile text is not echoed", "ATTACKER {secret} " + refusalCybersecurity + " <token>", refusalCybersecurity},
		{"split across lines is not a substring", "We take extra care with some cybersecurity\nrequests", ""},
		{"no marker", "all quiet\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProviderRefusalMarker(tc.screen); got != tc.want {
				t.Fatalf("ProviderRefusalMarker(%q) = %q, want %q", tc.screen, got, tc.want)
			}
		})
	}
	older := strings.Repeat("older line\n", 40) + refusalCybersecurity
	if got := ProviderRefusalMarker(older); got != refusalCybersecurity {
		t.Fatalf("the 41st line from the bottom must still match, got %q", got)
	}
	outside := refusalCybersecurity + "\n" + strings.Repeat("newer line\n", 40)
	if got := ProviderRefusalMarker(outside); got != "" {
		t.Fatalf("a marker outside the last 40 lines matched: %q", got)
	}
}

func refusalHcRule(screen string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"read", "worker", "--lines", "40", "--json"}, Stdout: `{"text":` + fmt.Sprintf("%q", screen) + `}`}
}

func TestFallbackCommandEmptyAndInvalidValues(t *testing.T) {
	if got, want := fallbackCommand("worker", "codex", ""), "soho reopen worker --kind claude --redispatch"; got != want {
		t.Fatalf("empty fallback for codex = %q, want %q", got, want)
	}
	if got, want := fallbackCommand("worker", "claude", ""), "soho reopen worker --kind codex --redispatch"; got != want {
		t.Fatalf("empty fallback for claude = %q, want %q", got, want)
	}
	if got, want := fallbackCommand("worker", "codex", "broken value"), "soho reopen worker --kind claude --redispatch"; got != want {
		t.Fatalf("malformed fallback = %q, want %q", got, want)
	}
	if got, want := fallbackCommand("worker", "claude", "codex gpt-5 high"), "soho reopen worker --kind codex --model gpt-5 --effort high --redispatch"; got != want {
		t.Fatalf("configured fallback = %q, want %q", got, want)
	}
}

func TestWaitProviderErrorRefusalPrintsTheFallbackCommand(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{
		"settled_grace":   {Value: "0"},
		"fallback_edit":   {Value: "codex gpt-5 high"},
		"fallback_review": {Value: "claude sonnet low"},
	}}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeError5),
		refusalHcRule("■ WE TAKE EXTRA CARE WITH SOME CYBERSECURITY REQUESTS"),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 14 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	wantNext := `"next":"soho reopen worker --kind codex --model gpt-5 --effort high --redispatch"`
	for _, want := range []string{
		`"status":"provider-error"`,
		`"cause":"the provider refused this task: ` + refusalCybersecurity + `"`,
		`"refused":true`, wantNext,
		"soho: warning: the provider refused the task of 'worker' (" + refusalCybersecurity + "); run: soho reopen worker --kind codex --model gpt-5 --effort high --redispatch",
	} {
		if !strings.Contains(out+stderr, want) {
			t.Fatalf("missing %q in out=%q stderr=%q", want, out, stderr)
		}
	}
	if strings.Contains(out, "fallback_review") || strings.Contains(out, "sonnet") || strings.Contains(stderr, "sonnet") {
		t.Fatalf("an edit role used the review fallback: out=%q stderr=%q", out, stderr)
	}
	reads := 0
	for _, call := range f.hcCalls(t) {
		if strings.Join(call.Argv, " ") == "read worker --lines 40 --json" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("the refusal path read the worker screen %d times, want once: %#v", reads, f.hcCalls(t))
	}
	f.assertNoHerdr(t)
}

func TestWaitProviderErrorWithoutAMarkerKeepsTheOldLine(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{
		"panes":           {Value: "4"},
		"settled_grace":   {Value: "0"},
		"fallback_edit":   {Value: "codex gpt-5 high"},
		"fallback_review": {Value: "claude sonnet low"},
	}}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeError5),
		refusalHcRule("Server connection could not be restored"),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 14 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	want := `{"agent":"worker","status":"provider-error","report":"","lane":"builders","kind":"claude","model":"","cause":"the assistant reported an error"}` + "\n"
	if out != want {
		t.Fatalf("out=%q\nwant %q", out, want)
	}
	if strings.Contains(stderr, "refused") || strings.Contains(stderr, "soho reopen") {
		t.Fatalf("a quiet screen warned about a refusal: %q", stderr)
	}
}

func TestWaitRefusalEmptyFallbackNamesAConcreteKind(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{"settled_grace": {Value: "0"}}}
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("worker\tp0a\tcodex\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeError5),
		refusalHcRule("■ "+refusalCybersecurity),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 14 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	want := "soho reopen worker --kind claude --redispatch"
	if !strings.Contains(out, `"next":"`+want+`"`) || !strings.Contains(stderr, "; run: "+want) {
		t.Fatalf("out=%q stderr=%q, want %q", out, stderr, want)
	}
	if strings.Contains(out+stderr, "another assistant") {
		t.Fatalf("out=%q stderr=%q, want a concrete kind", out, stderr)
	}
	f.assertNoHerdr(t)
}

func TestWaitSettledNoReportRefusalUsesTheReviewFallback(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{
		"settled_grace":   {Value: "0"},
		"fallback_edit":   {Value: "codex gpt-5 high"},
		"fallback_review": {Value: "claude sonnet medium"},
	}}
	f.env["SOHO_ROLES"] = ""
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("worker\tp0a\tclaude\treviewer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeIdle7),
		refusalHcRule("■ This content can't be shown"),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 6 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	for _, want := range []string{
		`"status":"settled-no-report"`,
		`"cause":"the provider refused this task: ` + refusalStraight + `"`,
		`"refused":true`,
		`"next":"soho reopen worker --kind claude --model sonnet --effort medium --redispatch"`,
		"soho: warning: the provider refused the task of 'worker' (" + refusalStraight + "); run: soho reopen worker --kind claude --model sonnet --effort medium --redispatch",
	} {
		if !strings.Contains(out+stderr, want) {
			t.Fatalf("missing %q in out=%q stderr=%q", want, out, stderr)
		}
	}
	if strings.Contains(out+stderr, "gpt-5") {
		t.Fatalf("a review role used the edit fallback: out=%q stderr=%q", out, stderr)
	}
	f.assertNoHerdr(t)
}

func TestWaitSettledNoReportWithoutAMarkerKeepsTheOldLine(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeIdle7),
		refusalHcRule("all quiet"),
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 6 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !strings.Contains(out, `"status":"settled-no-report"`) {
		t.Fatalf("out=%q", out)
	}
	for _, key := range []string{"refused", "next", "the provider refused"} {
		if strings.Contains(out+stderr, key) {
			t.Fatalf("a quiet screen added %q: out=%q stderr=%q", key, out, stderr)
		}
	}
	f.assertNoHerdr(t)
}

func TestWaitRefusalWithAnUnreadableScreenKeepsTheOldLine(t *testing.T) {
	f := newHcWakeFixture(t, "1", "none")
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{
		"settled_grace": {Value: "0"},
		"fallback_edit": {Value: "codex gpt-5 high"},
	}}
	f.installHc(t,
		fakecliRuleGetWorker(hcWakeError5),
		fakecli.Rule{Argv: []string{"read", "worker", "--lines", "40", "--json"}, Stderr: `{"error":{"code":"no_terminal"}}`, Code: 1},
	)
	code, out, stderr := f.runWait(t, true, 0)
	if code != 14 || !strings.Contains(out, `"status":"provider-error"`) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	for _, key := range []string{"refused", "next", "the provider refused"} {
		if strings.Contains(out+stderr, key) {
			t.Fatalf("a failed screen read added %q: out=%q stderr=%q", key, out, stderr)
		}
	}
}
