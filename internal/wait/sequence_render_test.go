package wait

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestSeqTextKeepsTheDecimalRendering: the transported sequence renders as
// jsString rendered herdr.AgentState's value, never in exponent form.
func TestSeqTextKeepsTheDecimalRendering(t *testing.T) {
	for raw, want := range map[string]string{
		"":                    "",
		"42":                  "42",
		"-7":                  "-7",
		"1e+21":               "1000000000000000000000",
		"9223372036854776000": "9223372036854776000",
		// An unsigned hc turn above int64 keeps its exact digits, the
		// rendering dispatch writes into the marker.
		"9223372036854775809":  "9223372036854775809",
		"18446744073709551615": "18446744073709551615",
	} {
		if got := seqText(json.RawMessage(raw)); got != want {
			t.Errorf("seqText(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestStatusKeepsNotReceivedForAnUnchangedLargeSequence: a not-received
// marker written with the adapter's rendering of a sequence beyond int64
// still matches the same sequence read through the transport, so status
// keeps not-received and exit 15 (DJA-594 slice 3 review).
func TestStatusKeepsNotReceivedForAnUnchangedLargeSequence(t *testing.T) {
	f := newNotInRosterFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1e21}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
	})
	old := herdr.AgentState("worker", f.env, herdr.Timeout, nil)
	if next := readState(herdr.New(f.env), "worker"); next.Seq != jsString(old.Seq) {
		t.Fatalf("transport sequence %q, adapter sequence %q", next.Seq, jsString(old.Seq))
	}
	marker := "123 " + jsString(old.Seq) + " /tmp/prompt.md\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "wait", "worker.not-received"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := f.run(t, []string{"worker"}); code != 15 {
		t.Fatalf("unchanged sequence must keep not-received and exit 15: code=%d stdout=%q stderr=%q", code, out, stderr)
	}
}
