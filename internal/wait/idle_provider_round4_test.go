package wait

import (
	"fmt"
	"github.com/djalmajr/soho/internal/provider"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReview546ProviderSpecificBoundary(t *testing.T) {
	raw, e := os.ReadFile("../peer/testdata/evidence-rc11-codex-history.txt")
	if e != nil {
		t.Fatal(e)
	}
	capture := string(raw)
	if !strings.Contains(capture, "• Working") {
		t.Fatal("capture anchor")
	}
	for _, withBorders := range []bool{true, false} {
		t.Run(fmt.Sprint(withBorders), func(t *testing.T) {
			frame := func(i int) string {
				history := "Result header\n"
				if withBorders {
					history += strings.Repeat("─", 30) + "\n"
				}
				history += "prior section\n"
				if withBorders {
					history += strings.Repeat("─", 30) + "\n"
				}
				history += fmt.Sprintf("processed fixtures/item%d.json\n", i)
				return strings.Replace(capture, "• Working", history+"• Working", 1)
			}
			a, b := frame(1), frame(2)
			ps, pe, pok := provider.ComposerRegionBounds("pi", a)
			cs, ce, cok := provider.ComposerRegionBounds("codex", a)
			t.Logf("with_history_borders=%t pi=[%d,%d)/%t codex=[%d,%d)/%t equal=%t", withBorders, ps, pe, pok, cs, ce, cok, idleFingerprint(a, "codex") == idleFingerprint(b, "codex"))
			if !cok {
				t.Fatal("actual Codex composer not recognized")
			}
			_, alerts, _, _ := review546Run(t, "codex", 27, time.Minute, frame)
			if idleFingerprint(a, "codex") == idleFingerprint(b, "codex") || alerts != 0 {
				t.Errorf("substantive progress below history separators lost: alerts=%d", alerts)
			}
		})
	}
}

func TestIdleProviderFallbackRound4(t *testing.T) {
	for _, kind := range []string{"", "unknown", "claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			a := "Result header\n" + strings.Repeat("─", 30) + "\nprior section\n" + strings.Repeat("─", 30) + "\nprocessed item 1\n› draft 1\nfooter\n"
			if idleFingerprint(a, kind) == idleFingerprint(strings.Replace(a, "processed item 1", "processed item 2", 1), kind) {
				t.Fatal("provider fallback discarded substantive history")
			}
			same := idleFingerprint(a, kind) == idleFingerprint(strings.Replace(a, "draft 1", "draft 2", 1), kind)
			if same != (kind == "codex") {
				t.Fatalf("composer recognized for wrong kind %q: equal=%t", kind, same)
			}
			plain := "command output\nprocessed item 1\n125 tokens\n"
			if idleFingerprint(plain, kind) != idleFingerprint(strings.Replace(plain, "125 tokens", "126 tokens", 1), kind) {
				t.Fatal("no-composer fallback retained bottom status")
			}
			if idleFingerprint(plain, kind) == idleFingerprint(strings.Replace(plain, "item 1", "item 2", 1), kind) {
				t.Fatal("no-composer fallback lost substantive output")
			}
		})
	}
}
