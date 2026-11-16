package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestHcNowriteSessionSnapshot(t *testing.T) {
	f := newSnapshotFixture(t)
	bin := filepath.Join(f.root, "bin")
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"}}`}}); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_TRANSPORT"], f.env["HC_TERMINAL_ID"], f.env["SOHO_FAKECLI_CONFIG"] = "hc", "t1", bin
	if code, out, stderr := f.run(t, "config"); code != 0 {
		t.Fatalf("warm config exit=%d out=%q err=%q", code, out, stderr)
	}
	before := fileSnapshot(t, f.sd)
	f.env["SOHO_NOWRITE"] = "1"
	for _, args := range [][]string{{"review-snapshot", f.src}, {"review-snapshot", "--help"}, {"session", "set", "max_workers", "9"}} {
		code, out, stderr := f.run(t, args...)
		if code != 2 || out != "" || !strings.Contains(stderr, "SOHO_NOWRITE=1 is read-only") {
			t.Fatalf("%v: exit=%d out=%q err=%q", args, code, out, stderr)
		}
	}
	if after := fileSnapshot(t, f.sd); !reflect.DeepEqual(before, after) {
		t.Fatalf("state changed: before=%v after=%v", before, after)
	}
}

func TestHcSnapshotRegistrantIdentity(t *testing.T) {
	for _, tc := range []struct{ name, terminal, agent, owner string }{
		{"caller worker", "t7", "", "worker"},
		{"missing caller uses agent", "", "worker", "worker"},
		{"caller outside roster ignores agent", "t1", "worker", ""},
		{"missing caller without roster agent", "", "missing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSnapshotFixture(t)
			bin := filepath.Join(f.root, "bin")
			if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
				{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"}}`},
				{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","labels":{"soho.session":"elsewhere"}}`},
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# header\nworker\tt7\tcodex\timplementer\topenai\t1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_TRANSPORT"], f.env["SOHO_FAKECLI_CONFIG"], f.env["SOHO_REGISTRY_DIR"] = "hc", bin, f.sd
			f.env["HC_TERMINAL_ID"], f.env["SOHO_AGENT"] = tc.terminal, tc.agent
			code, out, stderr := f.run(t, "review-snapshot", f.src)
			if code != 0 {
				t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
			}
			var obj map[string]string
			if err := json.Unmarshal([]byte(out), &obj); err != nil {
				t.Fatal(err)
			}
			rows, err := readReviewSnapshots(f.sd)
			if err != nil || len(rows) != 1 || rows[0][0] != tc.owner || obj["owner"] != tc.owner {
				t.Fatalf("owner want=%q rows=%v json=%v err=%v", tc.owner, rows, obj, err)
			}
		})
	}
}
