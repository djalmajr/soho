package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestOMPDispatchObservationCallFloor(t *testing.T) {
	now := time.Unix(1, 0)
	oldClock := dispatchCeilingNow
	t.Cleanup(func() { dispatchCeilingNow = oldClock })
	dispatchCeilingNow = func() time.Time { return now }
	for _, tc := range []struct {
		remaining time.Duration
		want      bool
	}{
		{time.Millisecond - time.Nanosecond, false},
		{time.Millisecond, true},
	} {
		call, ok := ompDispatchObservationCall(&dispatchWaitBudget{deadline: now.Add(tc.remaining)})
		if ok != tc.want || call.Timeout != tc.remaining {
			t.Errorf("remaining=%s call=%+v allowed=%t want %t", tc.remaining, call, ok, tc.want)
		}
	}
}

func TestDispatchOMPTranscriptArrival(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "no-follow")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := platform.OpenRegularFileNoFollow(probe)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip("OMP journal receipt is unavailable without an atomic no-follow open")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	const pane = "ws:p0a"
	for _, tc := range []struct {
		name, sessionKind, changeFrom, changeTo, probeEvent, fileKind string
		compact                                                       string
		add                                                           []string
		wantCode, wantPaneGets                                        int
		noCheck, rejectAtStart                                        bool
	}{
		{name: "one fresh user line", add: []string{"metadata", "user"}, wantPaneGets: 2},
		{name: "one user line after large retained history", fileKind: "large", add: []string{"user"}, wantPaneGets: 2},
		{name: "one fresh user line after compaction", compact: "success", add: []string{"metadata", "user"}, wantPaneGets: 2},
		{name: "failed compaction never starts receipt", compact: "failed", wantCode: 9},
		{name: "metadata only", add: []string{"metadata"}, wantCode: 15, wantPaneGets: 1},
		{name: "oversized append", add: []string{"oversize", "user"}, wantCode: 15, wantPaneGets: 1},
		{name: "incomplete appended user line", add: []string{"user"}, probeEvent: "partial", wantCode: 15, wantPaneGets: 1},
		{name: "budget exhausted by journal preflight", probeEvent: "preflight", wantCode: 10, wantPaneGets: 1},
		{name: "assistant only", add: []string{"assistant"}, wantCode: 15, wantPaneGets: 1},
		{name: "baseline only", wantCode: 15, wantPaneGets: 1},
		{name: "duplicate user lines", add: []string{"user", "user"}, wantCode: 15, wantPaneGets: 1},
		{name: "duplicate during verification", add: []string{"user"}, probeEvent: "duplicate", wantCode: 15, wantPaneGets: 2},
		{name: "journal replaced at same path", add: []string{"user"}, probeEvent: "replace", wantCode: 15, wantPaneGets: 2},
		{name: "changed terminal", add: []string{"user"}, changeFrom: `"terminal_id":"term-start"`, changeTo: `"terminal_id":"term-changed"`, wantCode: 15, wantPaneGets: 2},
		{name: "changed pane", add: []string{"user"}, changeFrom: `"pane_id":"ws:p0a"`, changeTo: `"pane_id":"ws:p0b"`, wantCode: 15, wantPaneGets: 2},
		{name: "changed workspace", add: []string{"user"}, changeFrom: `"workspace_id":"ws"`, changeTo: `"workspace_id":"other"`, wantCode: 15, wantPaneGets: 2},
		{name: "changed agent name", add: []string{"user"}, changeFrom: `"name":"worker"`, changeTo: `"name":"replacement"`, wantCode: 15, wantPaneGets: 2},
		{name: "changed kind", add: []string{"user"}, changeFrom: `"agent":"omp","agent_status"`, changeTo: `"agent":"pi","agent_status"`, wantCode: 15, wantPaneGets: 2},
		{name: "changed journal path", add: []string{"user"}, changeFrom: "session.jsonl", changeTo: "replacement.jsonl", wantCode: 15, wantPaneGets: 2},
		{name: "missing terminal", add: []string{"user"}, changeFrom: `"terminal_id":"term-start"`, changeTo: `"terminal_id":""`, rejectAtStart: true, wantCode: 15, wantPaneGets: 1},
		{name: "wrong workspace at start", add: []string{"user"}, changeFrom: `"workspace_id":"ws"`, changeTo: `"workspace_id":"other"`, rejectAtStart: true, wantCode: 15, wantPaneGets: 1},
		{name: "nonlocal session", sessionKind: "id", add: []string{"user"}, wantCode: 15, wantPaneGets: 1},
		{name: "missing session", sessionKind: "missing", add: []string{"user"}, wantCode: 15, wantPaneGets: 1},
		{name: "relative session path", fileKind: "relative", add: []string{"user"}, wantCode: 15, wantPaneGets: 1},
		{name: "missing session file", fileKind: "absent", wantCode: 15, wantPaneGets: 1},
		{name: "symlink journal", fileKind: "symlink", add: []string{"user"}, wantCode: 15, wantPaneGets: 1},
		{name: "budget exhausted before verification", add: []string{"user"}, probeEvent: "before", wantCode: 15, wantPaneGets: 1},
		{name: "budget exhausted during verification", add: []string{"user"}, probeEvent: "during", wantCode: 15, wantPaneGets: 2},
		{name: "late working state does not prove arrival", probeEvent: "late-state", wantCode: 15, wantPaneGets: 1},
		{name: "late report does not prove arrival", probeEvent: "late-report", wantCode: 15, wantPaneGets: 1},
		{name: "transient state kill cannot retry beyond ceiling", probeEvent: "retry", wantCode: 15, wantPaneGets: 1},
		{name: "submillisecond state budget skips launch", probeEvent: "sub-ms", wantCode: 15, wantPaneGets: 1},
		{name: "submillisecond raw identity budget skips launch", add: []string{"user"}, probeEvent: "sub-ms-identity", wantCode: 15, wantPaneGets: 1},
		{name: "submillisecond diagnostic budget keeps proved receipt", add: []string{"user"}, probeEvent: "sub-ms-diagnostic", wantPaneGets: 2},
		{name: "confirmation disabled", add: []string{"user"}, noCheck: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			session, gate := filepath.Join(tmp, "session.jsonl"), filepath.Join(tmp, "prompt-gate")
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			kind := tc.sessionKind
			if kind == "" {
				kind = "path"
			}
			reportedPath := session
			if tc.fileKind == "relative" {
				reportedPath = filepath.Join("..", filepath.Base(tmp), filepath.Base(session))
			}
			sessionRecord := fmt.Sprintf(`{"kind":%q,"source":"herdr:omp","value":%q}`, kind, reportedPath)
			if kind == "missing" {
				sessionRecord = "null"
			}
			raw := fmt.Sprintf(`{"result":{"agent":{"name":"worker","pane_id":%q,"workspace_id":"ws","terminal_id":"term-start","agent":"omp","agent_status":"idle","state_change_seq":1,"agent_session":%s}}}`, pane, sessionRecord)
			recheck := raw
			if tc.changeFrom != "" {
				recheck = strings.Replace(raw, tc.changeFrom, tc.changeTo, 1)
				if recheck == raw {
					t.Fatal("fixture did not change the worker record")
				}
				if tc.rejectAtStart {
					raw = recheck
				}
			}
			paneGet := []string{"agent", "get", pane}
			rules := []fakecli.Rule{
				{Argv: paneGet, Call: 1, Stdout: raw},
				{Argv: paneGet, Stdout: recheck},
				{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, WaitFile: gate, Stdout: `{"result":{"submitted":true}}`},
			}
			stateGet := []string{"agent", "get", "worker"}
			switch tc.probeEvent {
			case "late-state", "sub-ms":
				rules = append(rules, fakecli.Rule{Argv: stateGet, Call: 2, Stdout: compactStateJSON("working", 2)})
			case "retry":
				rules = append(rules,
					fakecli.Rule{Argv: stateGet, Call: 2, Code: 137},
					fakecli.Rule{Argv: stateGet, Call: 3, Stdout: compactStateJSON("working", 2)},
				)
			}
			if tc.compact != "" {
				after := ompCompactBefore + "\n" + ompCompactDivider + "\n"
				if tc.compact == "failed" {
					after = ompCompactFailed + "\n"
				}
				rules = append(rules,
					fakecli.Rule{Argv: compactReadArgv("worker"), Call: 1, Stdout: ompCompactBefore},
					fakecli.Rule{Argv: compactReadArgv("worker"), Stdout: after},
					fakecli.Rule{Argv: []string{"pane", "send-text", pane, "/compact"}, Stdout: `{"result":{}}`},
					fakecli.Rule{Argv: []string{"pane", "send-keys", pane, "Enter"}, Stdout: `{"result":{}}`},
				)
			}
			base := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0", rules...)
			roster := filepath.Join(base.state, "ws", "agents.tsv")
			body := mustRead(t, roster)
			body = strings.Replace(body, "w0test:p0a\tcodex\t", pane+"\tomp\t", 1)
			if err := os.WriteFile(roster, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.noCheck {
				base.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			}
			f := &piDispatchFixture{
				dispatchArrivalFixture: base,
				composed:               filepath.Join(base.state, "ws", "briefs", "worker-"+piTranscriptStamp+".md"),
				session:                session, promptGate: gate,
			}
			metadata := `{"type":"model_usage","completion":` + fmt.Sprintf("%q", f.composed) + `}`
			if tc.fileKind == "symlink" {
				if err := os.Symlink(filepath.Join(tmp, "target.jsonl"), session); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			if tc.fileKind != "absent" {
				f.seedSession(t, piTranscriptLine("user", f.composed), metadata)
			}
			if tc.fileKind == "large" {
				file, err := os.OpenFile(session, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				// The retained extent exceeds the old line scanner's cap.
				_, writeErr := file.WriteAt([]byte{'\n'}, (128<<20)-1)
				closeErr := file.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("write retained history: %v, close: %v", writeErr, closeErr)
				}
			}
			if len(tc.add) != 0 {
				lines := make([]string, 0, len(tc.add))
				for _, role := range tc.add {
					if role == "oversize" {
						lines = append(lines, fmt.Sprintf(`{"type":"model_usage","completion":%q}`, strings.Repeat("x", 1<<20)))
					} else if role == "metadata" {
						lines = append(lines, metadata)
					} else {
						lines = append(lines, piTranscriptLine(role, f.composed))
					}
				}
				appendSessionLine(t, f, strings.Join(lines, "\n"))
			}

			readCalls := func() []fakecli.Call {
				calls, err := fakecli.ReadCalls(filepath.Join(base.bin, "herdr.calls.jsonl"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				return calls
			}
			postSendGet := func(calls []fakecli.Call) bool {
				sent := false
				for _, call := range calls {
					if sent && slices.Equal(call.Argv, []string{"agent", "get", "worker"}) {
						return true
					}
					if len(call.Argv) >= 3 && slices.Equal(call.Argv[:3], []string{"agent", "prompt", "worker"}) {
						sent = true
					}
				}
				return false
			}
			start := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
			// The fixture config sets a one-second check and zero settle.
			ceilingEnd := start.Add(11 * time.Second)
			oldNow, oldCeilingNow := platform.Now, dispatchCeilingNow
			t.Cleanup(func() { platform.Now, dispatchCeilingNow = oldNow, oldCeilingNow })
			platform.Now = func() time.Time {
				if postSendGet(readCalls()) {
					return start.Add(time.Second)
				}
				return start
			}
			verified, preflightChecks, postSendChecks := false, 0, 0
			var postSendStarted time.Time
			dispatchCeilingNow = func() time.Time {
				switch tc.probeEvent {
				case "retry":
					if countDispatchCalls(readCalls(), "prompt") != 0 {
						if postSendStarted.IsZero() {
							postSendStarted = time.Now()
						}
						return ceilingEnd.Add(time.Since(postSendStarted) - 750*time.Millisecond)
					}
				case "late-report", "sub-ms":
					if countDispatchCalls(readCalls(), "prompt") != 0 {
						postSendChecks++
						if postSendChecks >= 4 {
							return ceilingEnd
						}
						if tc.probeEvent == "sub-ms" {
							return ceilingEnd.Add(-500 * time.Microsecond)
						}
						if postSendChecks == 3 {
							report := filepath.Join(base.state, "ws", "reports", "worker-"+piTranscriptStamp+".md")
							if err := os.WriteFile(report, []byte("late report\n"), 0o600); err != nil {
								t.Fatal(err)
							}
						}
						return ceilingEnd.Add(-750 * time.Millisecond)
					}
				case "late-state":
					if postSendGet(readCalls()) {
						return ceilingEnd
					}
				case "sub-ms-identity", "sub-ms-diagnostic":
					calls := readCalls()
					atProbe := tc.probeEvent == "sub-ms-identity" && postSendGet(calls) ||
						tc.probeEvent == "sub-ms-diagnostic" && countArgv(calls, paneGet) == 2
					if atProbe {
						postSendChecks++
						if postSendChecks >= 5 {
							return ceilingEnd
						}
						if postSendChecks == 4 {
							return ceilingEnd.Add(-500 * time.Microsecond)
						}
					}
				case "preflight":
					if countArgv(readCalls(), paneGet) == 1 {
						preflightChecks++
						if preflightChecks >= 3 {
							return start.Add(time.Minute)
						}
					}
				case "partial":
					if !verified && postSendGet(readCalls()) {
						verified = true
						data := mustRead(t, session)
						if err := os.Truncate(session, int64(len(data)-1)); err != nil {
							t.Fatal(err)
						}
					}
				case "before":
					if postSendGet(readCalls()) {
						return start.Add(time.Minute)
					}
				case "during", "duplicate", "replace":
					gets := 0
					for _, call := range readCalls() {
						if slices.Equal(call.Argv, paneGet) {
							gets++
						}
					}
					if gets == 2 && tc.probeEvent == "during" {
						return start.Add(time.Minute)
					}
					if gets == 2 && !verified {
						verified = true
						// The second raw call has finished. The journal may
						// change before the receipt is consumed.
						data := mustRead(t, session)
						if tc.probeEvent == "duplicate" {
							if err := os.WriteFile(session, []byte(data+piTranscriptLine("user", f.composed)+"\n"), 0o600); err != nil {
								t.Fatal(err)
							}
						} else {
							replacement := filepath.Join(tmp, "replacement.jsonl")
							if err := os.WriteFile(replacement, []byte(data), 0o600); err != nil {
								t.Fatal(err)
							}
							if err := os.Rename(replacement, session); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				return start
			}
			args := []string{"worker", base.brief, "--no-wait"}
			if tc.compact != "" {
				base.env["SOHO_COMPACT_TIMEOUT"] = "5s"
				args = append(args, "--compact")
			}
			code, out, stderr := base.run(t, args...)
			calls := readCalls()
			if tc.wantCode == 10 {
				sd := filepath.Join(base.state, "ws")
				if code != 10 || out != "" || strings.Count(stderr, "--no-wait gave up after") != 1 ||
					countDispatchCalls(calls, "prompt") != 0 || countArgv(calls, paneGet) != tc.wantPaneGets {
					t.Fatalf("expected one unsent ceiling outcome, exit=%d stdout=%q stderr=%q calls=%#v", code, out, stderr, calls)
				}
				for _, path := range []string{taskreport.TaskReportPointerPath(sd, "worker"), filepath.Join(sd, "last-report-worker")} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("pre-send cleanup left %s: %v", path, err)
					}
				}
				requireNoEnter(t, base.bin)
				return
			}
			if tc.compact != "" {
				success := tc.compact == "success"
				assertOMPCompactCalls(t, calls, pane, success, success)
				if !success {
					if code != tc.wantCode || compactJSON(t, out)["status"] != "failed" ||
						!strings.Contains(stderr, "the brief was not sent") || countArgv(calls, paneGet) != 0 {
						t.Fatalf("failed compaction reached receipt or send: exit=%d stdout=%q stderr=%q", code, out, stderr)
					}
					compactPhaseNoTaskStateFor(t, filepath.Join(base.state, "ws"), base.root)
					return
				}
			}
			wantStatus := "submitted"
			if tc.wantCode == 15 {
				wantStatus = "not-received"
			}
			if code != tc.wantCode || dispatchOutputStatus(t, out) != wantStatus {
				t.Errorf("exit=%d want=%d status=%q want=%q stderr=%q", code, tc.wantCode, dispatchOutputStatus(t, out), wantStatus, stderr)
			}
			seenPrompt, paneGets, postSendStateReads := false, 0, 0
			for _, call := range calls {
				if len(call.Argv) >= 3 && slices.Equal(call.Argv[:3], []string{"agent", "prompt", "worker"}) {
					seenPrompt = true
				}
				if seenPrompt && slices.Equal(call.Argv, []string{"agent", "get", "worker"}) {
					postSendStateReads++
				}
				if slices.Equal(call.Argv, paneGet) {
					paneGets++
					if seenPrompt != (paneGets == 2) {
						t.Fatalf("journal identity read on wrong side of send: %q", call.Argv)
					}
				}
			}
			if sends := countDispatchCalls(calls, "prompt"); sends != 1 || paneGets != tc.wantPaneGets {
				t.Errorf("prompt calls=%d want=1 journal identity calls=%d want=%d", sends, paneGets, tc.wantPaneGets)
			}
			if (tc.probeEvent == "before" || tc.probeEvent == "during") && postSendStateReads != 1 {
				t.Errorf("state reads after send=%d want=1 with no state reads past expired receipt ceiling", postSendStateReads)
			}
			switch tc.probeEvent {
			case "late-report", "sub-ms", "late-state", "retry", "sub-ms-identity", "sub-ms-diagnostic":
				wantReads := 1
				if tc.probeEvent == "late-report" || tc.probeEvent == "sub-ms" {
					wantReads = 0
				}
				if postSendStateReads != wantReads {
					t.Errorf("state reads after send=%d want=%d", postSendStateReads, wantReads)
				}
				if code == 15 {
					marker := filepath.Join(base.state, "ws", "wait", "worker.not-received")
					fields := strings.Fields(mustRead(t, marker))
					if len(fields) != 2 || fields[1] != "1" {
						t.Errorf("not-received marker=%q want last timely sequence 1", fields)
					}
				}
			}
			if tc.compact == "" {
				requireNoEnter(t, base.bin)
			} else if countArgvPrefix(calls, []string{"agent", "send-keys"}) != 0 {
				t.Error("dispatch sent a key after compaction")
			}
		})
	}
}
