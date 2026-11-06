package peer_test

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDJA651Exit15UnverifiedSites(t *testing.T) {
	for _, site := range []string{"recent", "pre_enter_visible", "pre_enter_status", "after_enter_recent"} {
		t.Run(site, func(t *testing.T) {
			id, prompt := stalledID(t)
			pre := codexPreScreen(t)
			last := strings.Replace(pre, "35m 51s", "35m 52s", 1)
			get := []string{"agent", "get", "w0test:p0a"}
			visible := []string{"agent", "read", "w0test:p0a", "--source", "visible"}
			recent := []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}
			rules := []fakecli.Rule{}
			for n := 1; n <= 4; n++ {
				r := fakecli.Rule{Argv: get, Call: n, Stdout: agentJSONKind("codex", "working", "1")}
				if n == 4 {
					r.Delay = 5
				}
				rules = append(rules, r)
			}
			postGet := fakecli.Rule{Argv: get, Stdout: agentJSONKind("codex", "working", "1"), Delay: 5}
			if site == "pre_enter_status" {
				postGet.Stdout, postGet.Stderr, postGet.Code = "", "fixture status unavailable", 1
			}
			rules = append(rules, postGet, fakecli.Rule{Argv: visible, Call: 1, Stdout: pre})
			postVisible := fakecli.Rule{Argv: visible, Stdout: last}
			if site == "pre_enter_visible" {
				postVisible.Stdout, postVisible.Stderr, postVisible.Code = "", "fixture visible unavailable", 1
				last = pre
			}
			if site == "after_enter_recent" {
				last = strings.Replace(last, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+" pending", 1)
				postVisible.Stdout = last
			}
			rules = append(rules, postVisible)
			if site == "after_enter_recent" {
				rules = append(rules, fakecli.Rule{Argv: recent, ArgvPrefix: true, Call: 1, Stdout: "history without receipt\n"})
			}
			read := fakecli.Rule{Argv: recent, ArgvPrefix: true, Stdout: "history without receipt\n"}
			if site == "recent" || site == "after_enter_recent" {
				read.Stdout, read.Stderr, read.Code = "", "fixture history unavailable", 1
				if site == "recent" {
					last = pre
				}
			}
			rules = append(rules, read, dja651PromptRule(prompt), fakecli.Rule{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}})
			f := newFixture(t, rules)
			dja651Isolate(f)
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
			if code != 15 || out != "" || !strings.Contains(stderr, "fixture ") {
				t.Errorf("unverified %s: code=%d out=%q stderr=%q", site, code, out, stderr)
			}
			dja651Log(t, dja651StateDir(f), "unverified", id, "local/-", "local/w0test:p0a")
			dja651Screen(t, dja651StateDir(f), id, last, out, stderr, true)
			enters := 0
			if site == "after_enter_recent" {
				enters = 1
			}
			dja651Submission(t, f, prompt, enters)
		})
	}
}
