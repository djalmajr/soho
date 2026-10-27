package cli

import (
	"encoding/json"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestDispatchContextPostCompact replaces the compact phase so the footer the
// context check reads changes at the phase boundary, as a real compaction does.
func TestDispatchContextPostCompact(t *testing.T) {
	for _, kind := range []string{"pi", "opencode"} {
		for _, mode := range []string{"low", "high", "unknown", "override", "compact-fails"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				screen := func(pct string) string {
					if pct == "unknown" {
						return "clipped footer"
					}
					if kind == "pi" {
						return contextPiScreen("", "", pct+"%/262k")
					}
					return "200K (" + pct + "%) ctrl+p commands"
				}
				post := "40"
				if mode == "high" || mode == "override" {
					post = "85"
				}
				if mode == "unknown" {
					post = "unknown"
				}
				f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen("95")})
				f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
				f.env["SOHO_CONTEXT_FRESH_PERCENT"] = "70"
				f.env["SOHO_CONTEXT_WARN_PERCENT"] = "60"
				roster := filepath.Join(f.state, "ws", "agents.tsv")
				writeRoleFixture(t, roster, strings.Replace(mustRead(t, roster), "\tcodex\t", "\t"+kind+"\t", 1))
				before := contextStateFiles(t, f.state)
				phaseCalls := 0
				saved := dispatchCompactPhase
				dispatchCompactPhase = func(o compactPhaseDispatchOptions) compactStepResult {
					phaseCalls++
					calls, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
					if e != nil && !os.IsNotExist(e) {
						t.Fatal(e)
					}
					for _, c := range calls {
						if len(c.Argv) > 1 && c.Argv[0] == "agent" && c.Argv[1] == "read" {
							t.Fatalf("screen read before compact boundary: %v", c.Argv)
						}
					}
					if mode == "compact-fails" {
						return compactStepResult{Code: 9}
					}
					p := filepath.Join(f.bin, "herdr.json")
					raw, e := os.ReadFile(p)
					if e != nil {
						t.Fatal(e)
					}
					var cfg map[string]json.RawMessage
					if e = json.Unmarshal(raw, &cfg); e != nil {
						t.Fatal(e)
					}
					var rules []fakecli.Rule
					if e = json.Unmarshal(cfg["rules"], &rules); e != nil {
						t.Fatal(e)
					}
					for i := range rules {
						if strings.Join(rules[i].Argv, " ") == "agent read worker --source visible" {
							rules[i].Stdout = screen(post)
						}
					}
					cfg["rules"], e = json.Marshal(rules)
					if e != nil {
						t.Fatal(e)
					}
					raw, e = json.Marshal(cfg)
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(p, raw, 0600); e != nil {
						t.Fatal(e)
					}
					return compactStepResult{Code: 0}
				}
				t.Cleanup(func() { dispatchCompactPhase = saved })
				args := []string{"worker", f.brief, "--no-wait", "--compact"}
				if mode == "override" || mode == "compact-fails" {
					args = append(args, "--allow-full-context")
				}
				code, out, msg := f.run(t, args...)
				want := 0
				if mode == "high" {
					want = 10
				}
				if mode == "compact-fails" {
					want = 9
				}
				if code != want || phaseCalls != 1 {
					t.Fatalf("code=%d want=%d phaseCalls=%d out=%s err=%s", code, want, phaseCalls, out, msg)
				}
				calls, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
				if e != nil && !os.IsNotExist(e) {
					t.Fatal(e)
				}
				prompts := 0
				reads := 0
				for _, c := range calls {
					if len(c.Argv) > 1 && c.Argv[0] == "agent" {
						if c.Argv[1] == "prompt" {
							prompts++
						}
						if c.Argv[1] == "read" {
							reads++
						}
					}
				}
				if want == 0 && (prompts != 1 || dispatchOutputStatus(t, out) != "submitted") {
					t.Fatalf("send missing: prompts=%d out=%s", prompts, out)
				}
				if want == 0 && reads == 0 {
					t.Fatal("post-compaction footer was never read")
				}
				warning := strings.Contains(msg, "a long context can end its turn empty ("+kind+")")
				if warning != (mode == "override") {
					t.Fatalf("warning=%t mode=%s stderr=%q", warning, mode, msg)
				}
				if strings.Contains(msg, "fakecli: no rule") {
					t.Fatalf("unmatched fake: %s", msg)
				}
				if want != 0 {
					if prompts != 0 || !reflect.DeepEqual(before, contextStateFiles(t, f.state)) {
						t.Fatal("refusal wrote task state or sent prompt")
					}
				}
				t.Logf("controlled compact boundary: before=95 post=%s kind=%s phase_calls=%d reads_after=%d exit=%d prompts=%d stderr=%q", post, kind, phaseCalls, reads, code, prompts, msg)
			})
		}
	}
}
