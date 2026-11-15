package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peerauth"
)

func TestHookNativePeerInbound(t *testing.T) {
	for _, flag := range []string{"", "--pi", "--codex"} {
		for _, off := range []bool{false, true} {
			for _, note := range []bool{false, true} {
				h := newHookFix(t)
				if off {
					h.projectOff()
				}
				prompt := `<hc from="omarc/t1 lead">` + hookSecret + `</hc>`
				if note {
					prompt = `<hc>t3 exited (exit 0)</hc>`
				}
				var code int
				var out, stderr string
				if flag == "" {
					code, out, stderr = h.run("inbound", prompt)
				} else {
					code, out, stderr = h.runFlagged("inbound", flag, prompt)
				}
				if code != 0 {
					t.Fatalf("flag=%q off=%v note=%v code=%d stderr=%q", flag, off, note, code, stderr)
				}
				if off && !note {
					var block struct{ Decision, Reason string }
					if err := json.Unmarshal([]byte(out), &block); err != nil || block.Decision != "block" || !strings.HasPrefix(block.Reason, "[soho:peer-refused:hc] omarc/t1 refused-inbound-off\n") || strings.Contains(out, hookSecret) {
						t.Fatalf("block=%q err=%v", out, err)
					}
					fields := logFields(t, h)
					if len(fields) != 7 || fields[1] != "hc" || fields[2] != "omarc/t1" || fields[3] != "" || fields[5] != "refused-inbound-off" {
						t.Fatalf("fields=%q", fields)
					}
					if !strings.Contains(stderr, "refused peer message hc omarc/t1 (refused-inbound-off)") {
						t.Fatalf("friction=%q", stderr)
					}
				} else if out != "" || h.read(peerauth.LogFile) != "" || readIfAny(h.log) != "" {
					t.Fatalf("allow: out=%q log=%q friction=%q", out, h.read(peerauth.LogFile), readIfAny(h.log))
				}
			}
		}
	}
}
