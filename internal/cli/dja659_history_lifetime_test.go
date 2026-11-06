package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDJA659HistoryLifetime(t *testing.T) {
	for _, scenario := range []string{"repeated", "cycle", "released", "released-source-name", "removed-if", "replaced-pane", "replaced-row", "replaced-same-pane", "recreated-source-released", "recreated-destination-released", "clock-rollback", "stale-winning-row", "stale-started"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			sd := filepath.Join(f.state, "ws")
			write := func(raw string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(dja659RosterHeader + dja659RosterLine("old", "p"))
			core.RosterRename(sd, "old", "middle")
			core.RosterRename(sd, "middle", "new")
			want := ""
			switch scenario {
			case "repeated":
				want = "new"
			case "cycle":
				core.RosterRename(sd, "new", "old")
				core.RosterRemove(sd, "old")
			case "released":
				core.RosterRemove(sd, "new")
			case "released-source-name":
				core.RosterRemove(sd, "old")
			case "removed-if":
				removed, err := core.RosterRemoveIf(sd, "p", core.RosterLine(sd, "new"))
				if err != nil || !removed {
					t.Fatalf("remove=%v err=%v", removed, err)
				}
			case "replaced-pane":
				core.RosterReplacePane(sd, "p", "replacement")
				core.RosterReplacePane(sd, "replacement", "p")
			case "replaced-same-pane":
				core.RosterAppend(sd, strings.Split(strings.TrimSuffix(dja659RosterLine("other", "p"), "\n"), "\t"))
			case "replaced-row":
				core.RosterAppend(sd, strings.Split(strings.TrimSuffix(dja659RosterLine("new", "replacement"), "\n"), "\t"))
				core.RosterRemove(sd, "new")
				core.RosterAppend(sd, strings.Split(strings.TrimSuffix(dja659RosterLine("new", "p"), "\n"), "\t"))
			case "recreated-source-released":
				core.RosterAppend(sd, strings.Split(strings.TrimSuffix(dja659RosterLine("old", "other"), "\n"), "\t"))
				core.RosterRemove(sd, "old")
			case "recreated-destination-released":
				core.RosterAppend(sd, strings.Split(strings.TrimSuffix(dja659RosterLine("new", "p"), "\n"), "\t"))
				core.RosterRemove(sd, "new")
				core.RosterAppend(sd, strings.Split(strings.TrimSuffix(dja659RosterLine("new", "p"), "\n"), "\t"))
			case "clock-rollback":
				core.RosterRemove(sd, "new")
				core.RosterAppend(sd, strings.Split(strings.ReplaceAll(strings.TrimSuffix(dja659RosterLine("new", "p"), "\n"), "\tnow\t", "\tearlier\t"), "\t"))
			case "stale-winning-row":
				b, err := os.ReadFile(filepath.Join(sd, "agents.tsv"))
				if err != nil {
					t.Fatal(err)
				}
				write(string(b) + dja659RosterLine("new", "wrong-pane"))
			case "stale-started":
				b, err := os.ReadFile(filepath.Join(sd, "agents.tsv"))
				if err != nil {
					t.Fatal(err)
				}
				write(strings.ReplaceAll(string(b), "\tnow\t", "\tearlier-clock\t"))
			}
			code, out, stderr := f.run(t, "old", f.brief, "--no-wait")
			if code != 3 || out != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			generic := "soho: agent 'old' is not in this skill's roster (spawn it first, or pass a name you spawned)\n"
			if want == "" && stderr != generic {
				t.Fatalf("invalidated hint: %q", stderr)
			}
			if want != "" && !strings.Contains(stderr, "renamed to '"+want+"'") {
				t.Fatalf("missing flattened hint: %q", stderr)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(calls) != 0 {
				t.Fatalf("diagnostic delivered: %#v", calls)
			}
		})
	}
}

func TestDJA659HistoryActiveNameWins(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	sd := filepath.Join(f.state, "ws")
	core.RosterRename(sd, "worker", "new")
	core.RosterAppend(sd, strings.Split(strings.TrimSuffix(dja659RosterLine("worker", "w0test:p0a"), "\n"), "\t"))
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || !strings.Contains(out, `"wait_status":"submitted"`) || strings.Contains(stderr, "renamed to") {
		t.Fatalf("active recreation: exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestDJA659HistoryHostileMetadata(t *testing.T) {
	valid := `{"v":1,"from":"old","to":"worker","pane":"w0test:p0a","started":"now"}`
	prefix := "# soho-rename "
	cases := map[string]string{
		"valid":             valid,
		"unsupported":       strings.Replace(valid, `"v":1`, `"v":2`, 1),
		"duplicate-key":     strings.Replace(valid, `"from":"old"`, `"from":"other","from":"old"`, 1),
		"noncanonical-key":  strings.Replace(valid, `"from":"old"`, `"FROM":"old"`, 1),
		"invalid-utf8":      strings.Replace(valid, "now", string([]byte{0xff}), 1),
		"malformed":         valid[:len(valid)-1],
		"trailing-document": valid + valid,
		"unknown-field":     strings.Replace(valid, `"v":1`, `"v":1,"path":"private"`, 1),
		"missing-identity":  strings.Replace(valid, `"started":"now"`, `"started":""`, 1),
		"stale":             strings.Replace(valid, `"pane":"w0test:p0a"`, `"pane":"other"`, 1),
		"self":              strings.Replace(valid, `"to":"worker"`, `"to":"old"`, 1),
		"ambiguous":         valid + "\n" + prefix + valid,
		"cycle":             strings.Replace(valid, `"to":"worker"`, `"to":"other"`, 1) + "\n" + prefix + strings.ReplaceAll(strings.Replace(valid, `"to":"worker"`, `"to":"old"`, 1), `"from":"old"`, `"from":"other"`),
		"oversized":         valid + strings.Repeat(" ", 64<<10),
	}
	count := valid
	for i := 0; i < 64; i++ {
		count += "\n" + prefix + strings.Replace(valid, `"from":"old"`, fmt.Sprintf(`"from":"n%d"`, i), 1)
	}
	cases["too-many"] = count
	cases["at-count-limit"] = strings.TrimSuffix(count, "\n"+prefix+strings.Replace(valid, `"from":"old"`, `"from":"n63"`, 1))
	cases["at-byte-limit"] = valid + strings.Repeat(" ", (64<<10)-len(prefix)-len(valid)-1)
	for name, metadata := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			sd := filepath.Join(f.state, "ws")
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(dja659RosterHeader+dja659RosterLine("worker", "w0test:p0a")+prefix+metadata+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, "old", f.brief, "--no-wait")
			if code != 3 || out != "" {
				t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
			}
			if name == "valid" || name == "at-count-limit" || name == "at-byte-limit" {
				if !strings.Contains(stderr, "renamed to 'worker'") {
					t.Fatalf("valid hint missing: %q", stderr)
				}
			} else if stderr != "soho: agent 'old' is not in this skill's roster (spawn it first, or pass a name you spawned)\n" {
				t.Fatalf("hostile hint: %q", stderr)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(calls) != 0 {
				t.Fatalf("native calls: %#v", calls)
			}
		})
	}
}
