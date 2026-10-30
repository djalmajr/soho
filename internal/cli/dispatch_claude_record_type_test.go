package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestDispatchClaudeArrivalNeedsAUserRootRecord: a fresh assistant record
// that carries the brief marker and nests a user-typed object is not the
// target receiving the brief; a genuine user record is.
func TestDispatchClaudeArrivalNeedsAUserRootRecord(t *testing.T) {
	for _, shape := range []string{"none", "user", "assistant-nested-user"} {
		t.Run(shape, func(t *testing.T) {
			pinDispatchClock(t)
			f := newClaudeDispatchFixture(t, "working", 5, "id", claudeTranscriptSession, "0")
			f.seedTranscript(t, claudeTranscriptLine("user", "earlier conversation"))
			withEarlierReport(t, f)
			done := make(chan bool, 1)
			if shape == "none" {
				done <- true
			} else {
				role := "user"
				if shape != "user" {
					role = "assistant"
				}
				raw, err := json.Marshal(map[string]any{"type": role, "message": map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": claudeDispatchText(f.composed, f.report, true)}, map[string]any{"type": "tool_use", "input": map[string]any{"type": "user"}}}}})
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					deadline := time.Now().Add(15 * time.Second)
					for time.Now().Before(deadline) {
						calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
						for _, call := range calls {
							if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
								file, err := os.OpenFile(f.transcript, os.O_APPEND|os.O_WRONLY, 0600)
								if err != nil {
									done <- false
									return
								}
								_, err = file.Write(append(raw, '\n'))
								closeErr := file.Close()
								done <- err == nil && closeErr == nil
								return
							}
						}
						time.Sleep(time.Millisecond)
					}
					done <- false
				}()
			}
			code, out, _ := f.run(t, "worker", f.brief, "--amend", "--no-wait")
			if !<-done {
				t.Fatal("fixture append failed")
			}
			requireNoEnter(t, f.bin)
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			prompts := countDispatchCalls(calls, "prompt")
			status := dispatchOutputStatus(t, out)
			t.Logf("shape=%s exit=%d status=%s prompt_attempts=%d extra_enter=0", shape, code, status, prompts)
			if prompts != 1 {
				t.Error("unexpected resend")
			}
			wantCode, wantStatus := 15, "not-received"
			if shape == "user" {
				wantCode, wantStatus = 0, "queued"
			}
			if code != wantCode || status != wantStatus {
				t.Errorf("want exit=%d status=%s", wantCode, wantStatus)
			}
		})
	}
}
