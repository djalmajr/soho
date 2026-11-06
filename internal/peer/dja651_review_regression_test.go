package peer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

var dja651ReviewGet = []string{"agent", "get", "w0test:p0a"}
var dja651ReviewVisible = []string{"agent", "read", "w0test:p0a", "--source", "visible"}
var dja651ReviewRecent = []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}

func dja651ReviewCalls(t *testing.T, f *fixture) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func dja651ReviewIndex(calls []fakecli.Call, argv []string, n int) int {
	for i, call := range calls {
		if len(call.Argv) >= len(argv) && slices.Equal(call.Argv[:len(argv)], argv) {
			n--
			if n == 0 {
				return i
			}
		}
	}
	return -1
}

// The prompt fake waits for a file written only after the real invocation is
// logged and its typed journal append has completed. No scheduler-dependent
// prompt delay decides whether the reader can see the new record.
func dja651ReviewAppend(t *testing.T, f *fixture, journal, id, record, release string) func() {
	t.Helper()
	appended := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), journal, id, record)
	done := make(chan error, 1)
	go func() {
		ok := <-appended
		err := os.WriteFile(release, nil, 0o600)
		if !ok {
			err = fmt.Errorf("typed append did not follow the actual prompt (release error: %v)", err)
		}
		done <- err
	}()
	var once sync.Once
	join := func() {
		once.Do(func() {
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(join)
	return join
}

func TestDJA651ReviewStalledQueueRefusal(t *testing.T) {
	for _, route := range []string{"immediate", "window"} {
		for _, kind := range []string{"codex", "claude", "pi"} {
			for _, refused := range []bool{false, true} {
				name := "clean"
				if refused {
					name = "history_refusal"
				}
				t.Run(route+"/"+kind+"/"+name, func(t *testing.T) {
					id, prompt := stalledID(t)
					pre := codexPreScreen(t)
					queue := "↳ " + peer.PeerOpenTag(id) + " pending\n" + pre
					switch kind {
					case "claude":
						pre = "✻ Working…\n" + claudeBoxSep + "\n❯\n" + claudeBoxSep + "\n"
						queue = peer.PeerOpenTag(id) + " pending\nPress up to edit queued messages\n" + pre
					case "pi":
						pre = "working without a peer marker\n"
						queue = "Steering: " + peer.PeerOpenTag(id) + " pending\n" + pre
					}
					recent := "history without receipt\n"
					if refused {
						recent = "[soho:peer-refused:" + id + "] refused-inbound-off\n"
					}
					p := dja651PromptRule(prompt)
					p.Code, p.Stderr = 1, stalledPromptErr
					rules := []fakecli.Rule{
						{Argv: dja651ReviewGet, Stdout: agentJSONKind(kind, "working", "1")},
						{Argv: dja651ReviewVisible, Call: 1, Stdout: pre},
						{Argv: dja651ReviewRecent, ArgvPrefix: true, Stdout: recent}, p,
					}
					queueRead := 2
					if route == "window" {
						rules = append(rules, fakecli.Rule{Argv: dja651ReviewVisible, Call: 2, Stdout: pre})
						queueRead = 3
					}
					rules = append(rules, fakecli.Rule{Argv: dja651ReviewVisible, Stdout: queue})
					f := newFixture(t, rules)
					dja651Isolate(f)
					code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
					calls := dja651ReviewCalls(t, f)
					pAt := dja651ReviewIndex(calls, []string{"agent", "prompt"}, 1)
					qAt := dja651ReviewIndex(calls, dja651ReviewVisible, queueRead)
					rAt := dja651ReviewIndex(calls, dja651ReviewRecent, 1)
					t.Logf("route=%s provider=%s prompt_index=%d queue_read=%d queue_index=%d history_index=%d code=%d", route, kind, pAt, queueRead, qAt, rAt, code)
					if pAt < 0 || qAt <= pAt || route == "window" && dja651ReviewIndex(calls, dja651ReviewVisible, 2) >= qAt {
						t.Fatal("intended stalled queue route was not reached after submission")
					}
					status := "queued"
					switch {
					case refused:
						status = "refused"
						if rAt <= qAt {
							t.Error("stalled queue returned without checking available off-screen refusal history")
						}
						if code != 18 || out != "" || !strings.Contains(stderr, "refused-inbound-off") {
							t.Errorf("off-screen refusal must win over stalled queue: code=%d out=%q stderr=%q; want exit18 and empty stdout", code, out, stderr)
						}
					case kind != "codex":
						status = "stalled"
						if code != 15 || out != "" || !strings.Contains(stderr, "delivery is uncertain") {
							t.Errorf("legacy stalled queue policy changed: provider=%s code=%d out=%q stderr=%q; want uncertain exit15", kind, code, out, stderr)
						}
					default:
						if code != 0 || out != "queued for local/w0test:p0a: it takes the message when its current turn ends\n" || stderr != "" {
							t.Errorf("clean Codex stalled queue control: code=%d out=%q stderr=%q", code, out, stderr)
						}
					}
					dja651Log(t, dja651StateDir(f), status, id, "local/-", "local/w0test:p0a")
					dja651Submission(t, f, prompt, 0)
				})
			}
		}
	}
}

func TestDJA651ReviewStalledReceiptCallBudget(t *testing.T) {
	for _, delayed := range []string{"control", "binding", "history", "visible"} {
		t.Run(delayed, func(t *testing.T) {
			id, prompt := stalledID(t)
			pre := codexPreScreen(t)
			dir := t.TempDir()
			release := filepath.Join(dir, "typed-append-complete")
			p := dja651PromptRule(prompt)
			p.Code, p.Stderr, p.WaitFile = 1, stalledPromptErr, release
			binding := fakecli.Rule{Argv: dja651ReviewGet, Call: 4, Stdout: dja651Agent(true)}
			history := fakecli.Rule{Argv: dja651ReviewRecent, ArgvPrefix: true, Call: 1, Stdout: "no screen receipt\n"}
			visible := fakecli.Rule{Argv: dja651ReviewVisible, Call: 4, Stdout: pre}
			const delay = 1700 // ms, above the existing one-second call floor
			switch delayed {
			case "binding":
				binding.Delay = delay
				binding.Stdout = strings.Replace(binding.Stdout, dja651Session, "aaaaaaaa-2222-4333-8444-555555555555", 1)
			case "history":
				history.Delay = delay
				visible.Stdout = "Unsupported final renderer\n"
			case "visible":
				visible.Delay = delay
				visible.Stdout = "Unsupported final renderer\n"
			}
			// Delayed cases cannot prove delivery even if the call finishes:
			// binding is different, or the final renderer is unsupported. This
			// keeps exit15 fixed while isolating the transport-time budget from
			// the separate policy for an unreadable refusal-history lookup.
			f := newFixtureAt(t, dir, []fakecli.Rule{
				binding, {Argv: dja651ReviewGet, Stdout: dja651Agent(true)},
				visible, {Argv: dja651ReviewVisible, Stdout: pre},
				history, {Argv: dja651ReviewRecent, ArgvPrefix: true, Stdout: "no screen receipt\n"}, p,
			})
			dja651Isolate(f)
			journal := dja651Journal(t, f, `{"type":"session_meta","payload":{"id":"`+dja651Session+`"}}`+"\n")
			join := dja651ReviewAppend(t, f, journal, id, dja651Record(t, "user", prompt), release)
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
			end := time.Now()
			join()
			calls := dja651ReviewCalls(t, f)
			starts, err := fakecli.ReadCallStarts(filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"))
			if err != nil || len(starts) != len(calls) {
				t.Fatalf("native call timestamp evidence missing: calls=%d starts=%d err=%v", len(calls), len(starts), err)
			}
			phase := dja651ReviewIndex(calls, dja651ReviewVisible, 3)
			stage := dja651ReviewIndex(calls, dja651ReviewGet, 4)
			if delayed == "history" {
				stage = dja651ReviewIndex(calls, dja651ReviewRecent, 1)
			} else if delayed == "visible" {
				stage = dja651ReviewIndex(calls, dja651ReviewVisible, 4)
			}
			if phase < 0 || stage <= phase {
				t.Fatalf("armed stalled validation did not reach %s: phase=%d stage=%d", delayed, phase, stage)
			}
			elapsed := end.Sub(starts[phase])
			t.Logf("validation=%s phase_start=%s call_start=%s elapsed=%s delay_ms=%d window=1ms floor=1s slack=450ms code=%d", delayed, starts[phase].Format(time.RFC3339Nano), starts[stage].Format(time.RFC3339Nano), elapsed, delay, code)
			// Start at the native in-window visible read, excluding preparation,
			// baseline arming and the prompt/append handshake entirely.
			if elapsed > time.Millisecond+time.Second+450*time.Millisecond {
				t.Errorf("stalled journal validation exceeded remaining window plus call floor: validation=%s elapsed=%s", delayed, elapsed)
			}
			status := "stalled"
			if delayed == "control" {
				status = "sent"
				if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
					t.Errorf("valid armed stalled journal control: code=%d out=%q stderr=%q", code, out, stderr)
				}
				if dja651ReviewIndex(calls, dja651ReviewRecent, 1) <= stage || dja651ReviewIndex(calls, dja651ReviewVisible, 4) <= stage {
					t.Error("clean control did not complete binding/history/final-visible receipt validation")
				}
			} else if code != 15 || out != "" || !strings.Contains(stderr, "delivery is uncertain") {
				t.Errorf("over-budget final validation cannot prove receipt: validation=%s code=%d out=%q stderr=%q; want uncertain exit15", delayed, code, out, stderr)
			}
			dja651Log(t, dja651StateDir(f), status, id, "local/-", "local/w0test:p0a")
			dja651Submission(t, f, prompt, 0)
		})
	}
}

func TestDJA651ReviewFinalFrameInsideReceiptValidation(t *testing.T) {
	for _, frame := range []string{"control", "queue", "composer", "dialog", "unknown", "unreadable"} {
		t.Run(frame, func(t *testing.T) {
			id, prompt := stalledID(t)
			pre := codexPreScreen(t)
			post := pre
			switch frame {
			case "queue":
				post = "↳ " + peer.PeerOpenTag(id) + " pending\n" + pre
			case "composer":
				post = strings.Replace(pre, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+" pending", 1)
			case "dialog":
				post = "Do you trust this workspace? [y/N]\n" + pre
			case "unknown":
				post = "Unsupported renderer\n› Ask Codex to do anything\n"
			}
			dir := t.TempDir()
			release := filepath.Join(dir, "typed-append-complete")
			p := dja651PromptRule(prompt)
			p.WaitFile = release
			final := fakecli.Rule{Argv: dja651ReviewVisible, Stdout: post}
			if frame == "unreadable" {
				final.Stdout, final.Stderr, final.Code = "", "fixture final visible unavailable", 1
			}
			f := newFixtureAt(t, dir, []fakecli.Rule{
				{Argv: dja651ReviewGet, Stdout: dja651Agent(true)},
				{Argv: dja651ReviewVisible, Call: 1, Stdout: pre},
				{Argv: dja651ReviewVisible, Call: 2, Stdout: pre}, final,
				{Argv: dja651ReviewRecent, ArgvPrefix: true, Stdout: "no independent screen receipt\n"}, p,
				{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
			})
			dja651Isolate(f)
			journal := dja651Journal(t, f, `{"type":"session_meta","payload":{"id":"`+dja651Session+`"}}`+"\n")
			join := dja651ReviewAppend(t, f, journal, id, dja651Record(t, "user", prompt), release)
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
			join()
			calls := dja651ReviewCalls(t, f)
			pAt := dja651ReviewIndex(calls, []string{"agent", "prompt"}, 1)
			// The immediate post-prompt sequence uniquely identifies Receipt's
			// callback: poll get, initial frame, binding get, history, final frame.
			want := [][]string{dja651ReviewGet, dja651ReviewVisible, dja651ReviewGet, dja651ReviewRecent, dja651ReviewVisible}
			for n, argv := range want {
				i := pAt + 1 + n
				if pAt < 0 || i >= len(calls) || len(calls[i].Argv) < len(argv) || !slices.Equal(calls[i].Argv[:len(argv)], argv) {
					t.Fatalf("final frame did not occur inside actual receipt validation: step=%d prompt_index=%d calls=%d", n, pAt, len(calls))
				}
			}
			t.Logf("frame=%s receipt_callback_indices=%d,%d,%d code=%d", frame, pAt+3, pAt+4, pAt+5, code)
			status, enters := "uncertain", 0
			switch frame {
			case "control":
				status = "sent"
				if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
					t.Errorf("receipt callback control: code=%d out=%q stderr=%q", code, out, stderr)
				}
			case "queue":
				status = "queued"
				if code != 0 || out != "queued for local/w0test:p0a: it takes the message when its current turn ends\n" || stderr != "" {
					t.Errorf("queue inside receipt validation became sent: code=%d out=%q stderr=%q", code, out, stderr)
				}
			case "dialog":
				status = "dialog"
				if code != 17 || out != "" {
					t.Errorf("dialog inside receipt validation became sent: code=%d out=%q stderr=%q", code, out, stderr)
				}
			default:
				if frame == "composer" {
					enters = 1
				} else if frame == "unreadable" {
					status = "unverified"
				}
				if code != 15 || out != "" {
					t.Errorf("unsafe final frame inside receipt validation became sent: frame=%s code=%d out=%q stderr=%q", frame, code, out, stderr)
				}
			}
			dja651Log(t, dja651StateDir(f), status, id, "local/-", "local/w0test:p0a")
			dja651Submission(t, f, prompt, enters)
		})
	}
}

func TestDJA651ReviewMovedSequenceWithoutJournalReceipt(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		route := "normal"
		if stalled {
			route = "stalled"
		}
		for _, mode := range []string{"moved", "unchanged", "history_refusal"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				id, prompt := stalledID(t)
				pre := codexPreScreen(t)
				before := strings.Replace(dja651Agent(true), `"working"`, `"idle"`, 1)
				after := strings.Replace(before, `"state_change_seq":"1"`, `"state_change_seq":"2"`, 1)
				if mode == "unchanged" {
					after = before
				}
				recent := "history without receipt\n"
				if mode == "history_refusal" {
					recent = "[soho:peer-refused:" + id + "] refused-inbound-off\n"
				}
				p := dja651PromptRule(prompt)
				if stalled {
					p.Code, p.Stderr = 1, stalledPromptErr
				}
				rules := []fakecli.Rule{}
				for n := 1; n <= 3; n++ {
					rules = append(rules, fakecli.Rule{Argv: dja651ReviewGet, Call: n, Stdout: before})
				}
				rules = append(rules,
					fakecli.Rule{Argv: dja651ReviewGet, Stdout: after},
					fakecli.Rule{Argv: dja651ReviewVisible, Stdout: pre},
					fakecli.Rule{Argv: dja651ReviewRecent, ArgvPrefix: true, Stdout: recent}, p)
				f := newFixture(t, rules)
				dja651Isolate(f)
				// A valid baseline can arm, but never supplies a new user record.
				// No append goroutine exists in this sequence-only proof.
				baseline := `{"type":"session_meta","payload":{"id":"` + dja651Session + `"}}` + "\n"
				journal := dja651Journal(t, f, baseline)
				code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
				raw, err := os.ReadFile(journal)
				if err != nil || string(raw) != baseline {
					t.Fatalf("sequence proof acquired a journal receipt: err=%v", err)
				}
				calls := dja651ReviewCalls(t, f)
				get := dja651ReviewIndex(calls, dja651ReviewGet, 4)
				history := dja651ReviewIndex(calls, dja651ReviewRecent, 1)
				if get < 0 || history <= get {
					t.Fatalf("sequence stage/history not reached independently: get=%d history=%d", get, history)
				}
				t.Logf("route=%s mode=%s sequence_get=%d history=%d journal_user_records=0 code=%d", route, mode, get, history, code)
				status := "sent"
				switch mode {
				case "moved":
					if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
						t.Errorf("independent moved sequence failed: code=%d out=%q stderr=%q", code, out, stderr)
					}
				case "unchanged":
					status = "uncertain"
					if stalled {
						status = "stalled"
					}
					if code != 15 || out != "" {
						t.Errorf("unchanged sequence without journal growth proved receipt: code=%d out=%q stderr=%q", code, out, stderr)
					}
				case "history_refusal":
					status = "refused"
					if code != 18 || out != "" || !strings.Contains(stderr, "refused-inbound-off") {
						t.Errorf("sequence-only proof bypassed refusal: code=%d out=%q stderr=%q", code, out, stderr)
					}
				}
				dja651Log(t, dja651StateDir(f), status, id, "local/-", "local/w0test:p0a")
				dja651Submission(t, f, prompt, 0)
			})
		}
	}
}
