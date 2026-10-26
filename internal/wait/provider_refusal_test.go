package wait

import (
	"bytes"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderRefusalWait(t *testing.T) {
	for _, tc := range []struct{ name, state, screen, status, cause string }{
		{"refusal", "idle", "■ We take extra care with some cybersecurity requests", "provider-refusal", "We take extra care with some cybersecurity requests"},
		{"refusal blocked", "blocked", "■ We take extra care with some cybersecurity requests", "provider-refusal", "We take extra care with some cybersecurity requests"},
		{"lost working", "working", "■ Server connection could not be restored", "provider-error", provider.LostConnectionCause},
		{"lost idle", "idle", "■ Automatic reconnect could not restore this session. Your draft is", "provider-error", provider.LostConnectionCause},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQueuedProbeFixture(t, tc.state, "5", tc.screen, nil)
			isolateProviderPlanFixture(t, f)
			var out bytes.Buffer
			old := platform.Stdout
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = old })
			start := time.Now()
			rc := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, 3000, false, t.TempDir())
			if rc != 14 || !strings.Contains(out.String(), `"status":"`+tc.status+`"`) || !strings.Contains(out.String(), tc.cause) {
				t.Fatalf("exit=%d output=%s", rc, out.String())
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("terminal detection waited %s", time.Since(start))
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			gets := 0
			for _, c := range calls {
				if len(c.Argv) > 1 {
					if c.Argv[1] == "prompt" || c.Argv[1] == "send-keys" {
						t.Fatalf("automatic retry: %#v", c)
					}
					if c.Argv[1] == "get" {
						gets++
					}
				}
			}
			if gets != 1 {
				t.Fatalf("observations=%d want first observation", gets)
			}
		})
	}
}
