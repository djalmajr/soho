package peer_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Exercise CmdSend itself, including its native transport and ExitError contract.
// Restore the output streams even when an assertion or unexpected panic fires.
func dja651StaleHistorySend(t *testing.T, f *fixture) (code int, stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() {
		platform.Stdout, platform.Stderr = oldOut, oldErr
		stdout, stderr = out.String(), errOut.String()
		if value := recover(); value != nil {
			exitErr, ok := value.(*platform.ExitError)
			if !ok {
				panic(value)
			}
			code, stderr = exitErr.Code, stderr+exitErr.Msg
		}
	}()
	cwd := f.env.Get("HOME")
	ctx := core.LoadConfig(f.env, cwd)
	code = peer.CmdSend([]string{"w0test:p0a", "--now", "--timeout", "1s", "hello"}, &ctx, f.env, cwd)
	return
}

func TestDJA651StalledHistoryLatestFrame(t *testing.T) {
	for _, scenario := range []string{"pending_composer", "delivered_history", "foreign_history"} {
		t.Run(scenario, func(t *testing.T) {
			id, prompt := stalledID(t)
			pre := codexPreScreen(t)
			clear := strings.Replace(pre, "35m 51s", "35m 52s", 1)
			const privateBody = "DJA651-LATEST-FRAME-BODY"
			latest := privateBody + "\n› " + peer.PeerOpenTag(id) + "\n> hello\n" + peer.PeerEndLine(id) + "\n" + clear
			if scenario == "pending_composer" {
				latest = privateBody + "\n" + strings.Replace(clear, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+"\n> hello\n"+peer.PeerEndLine(id), 1)
			} else if scenario == "foreign_history" {
				// A longer ID sharing the complete own-ID prefix is not a receipt.
				latest = strings.ReplaceAll(latest, id+"]", id+"0]")
			}
			region, recognized := provider.ComposerRegion("codex", latest)
			if !recognized || clear == pre {
				t.Fatal("fixture requires supported frames and an earlier changed clear screen")
			}
			composer := strings.Join(region, "\n")
			if scenario == "pending_composer" {
				if !strings.Contains(composer, peer.PeerOpenTag(id)) || !slices.Contains(region, peer.PeerEndLine(id)) {
					t.Fatal("fixture must keep the exact own opening and complete closing line inside the composer")
				}
			} else if strings.Contains(composer, peer.PeerOpenTag(id)) {
				t.Fatal("history control must leave the composer empty")
			}
			ownReceipt := slices.Contains(strings.Split(latest, "\n"), peer.PeerEndLine(id))
			if ownReceipt != (scenario != "foreign_history") {
				t.Fatal("fixture closing-line identity does not match its scenario")
			}

			get := []string{"agent", "get", "w0test:p0a"}
			visible := []string{"agent", "read", "w0test:p0a", "--source", "visible"}
			recent := []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped", "--lines", strconv.Itoa(strings.Count(prompt, "\n") + 61)}
			p := dja651PromptRule(prompt)
			p.Code, p.Stderr = 1, stalledPromptErr
			f := newFixture(t, []fakecli.Rule{
				// No session binding/journal receipt, and every get retains seq=1.
				{Argv: get, Stdout: dja651Agent(false)},
				{Argv: visible, Call: 1, Stdout: pre},
				{Argv: visible, Call: 2, Stdout: clear}, // immediate stalled box check
				{Argv: visible, Call: 3, Stdout: clear}, // stalledTaken's local visible
				{Argv: visible, Stdout: latest},         // actual recent -> visible fallback and later polls
				{Argv: recent, Code: 1, Stderr: notIdleErr}, p,
			})
			dja651Isolate(f)
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1000", "50"
			code, out, stderr := dja651StaleHistorySend(t, f)

			// Establish the real consumer branch before inspecting delivery. A
			// missing fake rule or an earlier conservative exit cannot pass here.
			calls := dja651ReviewCalls(t, f)
			promptAt, immediateAt := -1, -1
			clearAt, getAt, recentAt, fallbackAt := -1, -1, -1, -1
			prompts, keys, reads := 0, 0, 0
			for i, call := range calls {
				switch {
				case slices.Equal(call.Argv, get):
					if clearAt >= 0 && recentAt < 0 {
						getAt = i
					}
				case slices.Equal(call.Argv, visible):
					reads++
					switch reads {
					case 2:
						immediateAt = i
					case 3:
						clearAt = i
					case 4:
						fallbackAt = i
					}
				case slices.Equal(call.Argv, recent):
					if recentAt < 0 {
						recentAt = i
					}
				case slices.Equal(call.Argv, p.Argv):
					prompts++
					promptAt = i
				case len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "send-keys":
					keys++
				default:
					t.Fatalf("unexpected native call at index %d; fixture cannot prove the history branch", i)
				}
			}
			if prompts != 1 || keys != 0 {
				t.Fatalf("submission invariant: prompts=%d keys=%d; want one exact original prompt and no key/resend", prompts, keys)
			}
			if promptAt < 0 || immediateAt != promptAt+1 || clearAt != immediateAt+1 || getAt != clearAt+1 || recentAt != getAt+1 || fallbackAt != recentAt+1 {
				t.Fatalf("stalled history fallback not reached in order: prompt=%d immediate=%d clear=%d unchanged_get=%d recent=%d fallback=%d", promptAt, immediateAt, clearAt, getAt, recentAt, fallbackAt)
			}
			if _, err := os.Stat(filepath.Join(f.env.Get("CODEX_HOME"), "sessions")); !os.IsNotExist(err) {
				t.Fatal("fixture must have no journal receipt")
			}
			t.Logf("native stalled history: prompt=%d immediate=%d clear=%d unchanged_get=%d recent_not_idle=%d visible_fallback=%d; seq=1, no journal, prompts=%d keys=%d, own_closing_line=%t composer_pending=%t", promptAt, immediateAt, clearAt, getAt, recentAt, fallbackAt, prompts, keys, ownReceipt, scenario == "pending_composer")

			// Check any artifact before the result assertion, without printing
			// screen bytes even on failure. Its body stays local to the file.
			stateDir := dja651StateDir(f)
			screenPath := filepath.Join(stateDir, "wait", "send-"+id+".screen")
			raw, screenErr := os.ReadFile(screenPath)
			if screenErr == nil {
				if string(raw) != latest {
					t.Fatal("saved artifact differs from the latest successful visible fallback; body withheld")
				}
				if strings.Count(stderr, screenPath) != 1 || strings.Contains(out, screenPath) {
					t.Fatal("saved artifact path must appear exactly once on stderr only")
				}
			} else if !os.IsNotExist(screenErr) {
				t.Fatalf("cannot inspect screen artifact: %v", screenErr)
			}
			if strings.Contains(out+stderr, privateBody) {
				t.Fatal("visible fallback body leaked to command output; body withheld")
			}
			for _, name := range []string{peer.PeerLogFile, "friction.log"} {
				log, err := os.ReadFile(filepath.Join(stateDir, name))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if strings.Contains(string(log), privateBody) {
					t.Fatalf("visible fallback body leaked to %s; body withheld", name)
				}
			}

			if scenario == "delivered_history" {
				if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
					t.Errorf("valid delivered-history fallback: code=%d out=%q stderr=%q; want sent", code, out, stderr)
				}
				if screenErr == nil {
					t.Error("confirmed delivery must not create an uncertain-send artifact")
				}
				dja651Log(t, stateDir, "sent", id, "local/-", "local/w0test:p0a")
			} else {
				if code != 15 || out != "" || !strings.Contains(stderr, "delivery is uncertain") || !strings.Contains(stderr, "agent_prompt_stalled") {
					t.Errorf("latest %s must remain uncertain/stalled: code=%d out=%q stderr=%q; want exit15 and no sent stdout", scenario, code, out, stderr)
				}
				if screenErr != nil {
					t.Error("uncertain send must save its latest successfully observed fallback frame")
				}
				dja651Log(t, stateDir, "stalled", id, "local/-", "local/w0test:p0a")
			}
		})
	}
}
