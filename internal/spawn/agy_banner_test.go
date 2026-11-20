package spawn

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// agyPromoBannerScreen quotes the two lines of the pdweb 2026-11-18 report.
const agyPromoBannerScreen = "Our best balance of intelligence, cost, and latency. Get building today with Neon.\nesc to dismiss\n"

func agyBannerJSONScreen(t *testing.T, screen string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"text": screen})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestIsAgyPromoBanner(t *testing.T) {
	for _, tc := range []struct {
		name, screen string
		want         bool
	}{
		{"pdweb two lines", agyPromoBannerScreen, true},
		{"padding and uppercase", "   OUR BEST BALANCE OF INTELLIGENCE, COST, AND LATENCY.   \n\t ESC   TO   DISMISS  \n", true},
		{"trust question with footer", "Do you trust the contents of this project?\n" + agyPromoBannerScreen, false},
		{"yes no choice with footer", agyPromoBannerScreen + "(y/n)\n", false},
		{"bracket choice with footer", agyPromoBannerScreen + "[y/n]\n", false},
		{"question line", agyPromoBannerScreen + "Continue anyway?\n", false},
		{"empty screen", "", false},
		{"R581 choice screen without promo text", "Choose how to continue\n> Continue once\n  Cancel\nesc to dismiss\n", false},
		{"R581 wrapped yes no with known text", "Our best balance of intelligence, cost, and latency. Get building today with Neon.\nYes,\n  proceed\nNo,\n  quit\nesc to dismiss\n", false},
		{"unknown promo text with footer", "Sign up now for free credits.\nesc to dismiss\n", false},
		{"known text without footer", "Our best balance of intelligence, cost, and latency. Get building today with Neon.\n", false},
		{"footer in a question sentence", "Press esc to dismiss and tell us: do you trust this folder?\n", false},
		{"confirm hint", agyPromoBannerScreen + "enter to confirm\n", false},
		{"yes choice", agyPromoBannerScreen + "yes, proceed\n", false},
		{"no choice", agyPromoBannerScreen + "no, quit\n", false},
		{"allow choice", agyPromoBannerScreen + "1. Allow once\n", false},
		{"approve choice", agyPromoBannerScreen + "2. Approve and continue\n", false},
		{"other screen", "Our best balance of intelligence, cost, and latency. Get building today with Neon.\n", false},
		{"split footer", "Our best balance of intelligence, cost, and latency. Get building today with Neon. esc to\n dismiss\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAgyPromoBanner(tc.screen); got != tc.want {
				t.Fatalf("got %v want %v for %q", got, tc.want, tc.screen)
			}
		})
	}
}

// TestSpawnHcAgyPromoBanner proves a fresh agy blocked at startup on hc
// keeps its terminal, exits 7 with blocked_at_startup and sends no key. A
// known banner only names itself and the dismiss command in the warning; a
// trust question keeps the ordinary warning. The R582 mixed screen still
// classifies as a banner: the refusal markers do not cover "choose how to
// continue", "continue once" or "cancel", so it gets the sentence too.
// No key is sent on any screen, so the wording is the only difference. A
// ready start with the same banner on screen gets nothing added.
func TestSpawnHcAgyPromoBanner(t *testing.T) {
	const blocked = `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"permission prompt"}}`
	const mixedScreen = "Our best balance of intelligence, cost, and latency. Get building today with Neon.\nChoose how to continue\n> Continue once\n  Cancel\nesc to dismiss\n"
	const bannerLine = "agy 'reviewer' is showing a promotional banner (esc to dismiss); dismiss it with 'hc keys t7 esc', then dispatch"
	get := []string{"get", "t7", "--json"}
	read := []string{"read", "t7", "--lines", "0", "--json"}

	for _, tc := range []struct {
		name, screen, getRecord string
		wantExit                int
		wantBannerLine          bool
	}{
		{"banner blocked", agyPromoBannerScreen, blocked, 7, true},
		{"trust question blocked", piTrustScreen + "esc to dismiss\n", blocked, 7, false},
		{"mixed banner and choice blocked", mixedScreen, blocked, 7, true},
		{"banner on a ready start", agyPromoBannerScreen, hcReadyWorker, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, _, env, cfg := hcSpawnFixture(t, "reviewer", "agy")
			rules := append(hcFixtureRules(), fakecli.Rule{Argv: get, Stdout: tc.getRecord})
			if tc.wantExit == 7 {
				rules = append(rules, fakecli.Rule{Argv: read, Stdout: agyBannerJSONScreen(t, tc.screen)})
			}
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			exit, out, stderr, calls := spawnHcExitWithLaunch(t, bin, []string{"reviewer", "--timeout", "60000"}, cfg, env, cwd, newFakeSpawnRPCConn(), nil)
			code := 0
			if exit != nil {
				code = exit.Code
			}
			if code != tc.wantExit {
				t.Fatalf("exit=%d want=%d stderr=%s out=%s", code, tc.wantExit, stderr, out)
			}
			if tc.wantExit == 7 {
				if !spawnJSONHasField(t, out.String(), `"status": "blocked_at_startup"`) {
					t.Fatalf("missing blocked status: %s", out)
				}
				if !strings.Contains(out.String(), tc.screen) {
					t.Fatalf("screen not printed: %s", out)
				}
			} else if !spawnJSONHasField(t, out.String(), `"status": "ready"`) {
				t.Fatalf("not ready: %s", out)
			}
			if got := strings.Contains(stderr.String(), bannerLine); got != tc.wantBannerLine {
				t.Fatalf("banner line present=%v want=%v stderr=%s", got, tc.wantBannerLine, stderr)
			}
			for _, c := range calls {
				switch c.Argv[0] {
				case "keys":
					t.Fatalf("key sent on an agy start: %v", c.Argv)
				case "kill", "rm", "send":
					if tc.wantExit == 7 {
						t.Fatalf("blocked terminal was touched: %v", c.Argv)
					}
				}
			}
			if strings.Contains(stderr.String(), "fakecli: no rule") {
				t.Fatalf("fake hc call failed: %s", stderr)
			}
		})
	}
}
