package core

import (
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EPortConfig(t *testing.T) {
	if !ConfigKeyOk("e2e_port_base") {
		t.Fatal("unknown key")
	}
	count := 0
	for _, k := range ConfigScalarKeys {
		if k == "e2e_port_base" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("key count=%d", count)
	}
	for value, want := range map[string]bool{"": true, "1024": true, "47000": true, "65000": true, "0": false, "1023": false, "65001": false, "-1": false, "+47000": false, "47.0": false, "47000 ": false, " 47000": false, "abc": false, strings.Repeat("9", 1000): false} {
		t.Run(value, func(t *testing.T) {
			if got := ConfigValueOk("e2e_port_base", value, platform.Env{}, ""); got != want {
				t.Fatalf("value=%q got=%t want=%t", value, got, want)
			}
		})
	}
}

func TestE2EPortRosterAppendCompatibility(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agents.tsv")
	original := "# original header\nold\tp-old\tgrok\tworker\txai\t1\t/work\tnow\n"
	if err := os.WriteFile(file, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	row := []string{"new", "p-new", "grok", "worker", "xai", "1", "/work", "now", "model", "ask", "worker", "", "", "", "high", "inherit", "47000"}
	RosterAppend(dir, row)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), original) {
		t.Fatalf("old bytes changed: %q", raw)
	}
	if !strings.Contains(string(raw), "\tmcp\te2e_port\n") {
		t.Fatalf("no trailing header: %q", raw)
	}
	old := strings.Split(RosterLine(dir, "old"), "\t")
	if RosterE2EPort(old) != "" {
		t.Fatal("old row has port")
	}
	if got := RosterE2EPort(strings.Split(RosterLine(dir, "new"), "\t")); got != "47000" {
		t.Fatalf("port=%q", got)
	}
	before := string(raw)
	off := row[:16]
	off[0] = "off"
	RosterAppend(dir, off)
	raw, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != before+strings.Join(off, "\t")+"\n" {
		t.Fatalf("off append altered bytes: %q", raw)
	}
	if got := len(strings.Split(RosterLine(dir, "off"), "\t")); got != 16 {
		t.Fatalf("off columns=%d", got)
	}
	RosterRemove(dir, "new")
	if RosterLine(dir, "new") != "" || RosterLine(dir, "old") == "" {
		t.Fatal("release row removal changed other worker")
	}
}
