package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/platform"
)

// The receiver's hook: `soho hook reminder` and `soho hook inbound` check the
// peer messages of a UserPromptSubmit prompt before the agent sees it. There
// is no authentication: the project's `inbound=off` and the size refusal are
// what the check refuses.

const hookSecret = "SECRET-DJA196-HOOK-BODY"

type hookFix struct {
	t    *testing.T
	env  platform.Env
	cwd  string
	conf string // the user config dir
	log  string // the project's friction log
}

func newHookFix(t *testing.T) *hookFix {
	t.Helper()
	env, cwd := commandFixture(t)
	env["HERDR_ENV"], env["HERDR_PANE_ID"], env["PATH"] = "1", "w6X:p2", t.TempDir()
	h := &hookFix{t: t, env: env, cwd: cwd, conf: filepath.Join(env["XDG_CONFIG_HOME"], "soho")}
	if err := os.MkdirAll(filepath.Join(env["SOHO_DIR"], "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.log = filepath.Join(env["SOHO_DIR"], "ws", "friction.log")
	return h
}

// projectOff sets the target project's inbound=off in its project file.
func (h *hookFix) projectOff() {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Join(h.cwd, ".agents"), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cwd, ".agents", "soho.conf"), []byte("inbound=off\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

var idCounter int

func nextID() string {
	idCounter++
	return fmt.Sprintf("%08x", 0x1a2b0000+idCounter)
}

// localMessage is a peer message as the sender assembles it today: unsigned.
func localMessage(id string) string {
	return peer.PeerHeaderFor(peer.PeerSender{Ref: "local/w6X:p1", Name: "orchestrator-7", Kind: "claude"}, id) + "\n\n> " + hookSecret + "\n" + peer.PeerEndLine(id)
}

func remoteMessage(id string) string {
	return peer.PeerHeaderRemoteFor(peer.PeerSender{Name: "orchestrator-7", Kind: "claude"}, "w6X:p1", "mac.local", id) + "\n\n> " + hookSecret + "\n" + peer.PeerEndLine(id)
}

// oldSenderMessage is a message from an older soho: its Sender line still
// ends with the signature item before the period.
func oldSenderMessage(id string) string {
	tail := "sig v1:0123456789abcdef0123456789abcdef."
	return strings.Replace(localMessage(id), "; kind claude.", "; kind claude; "+tail, 1)
}

func wrapped(message string) string {
	return "\n\n<pasted_content id=\"c967\">\n" + message + "\n</pasted_content id=\"c967\">\n"
}

func promptInput(prompt, cwd string) string {
	raw, err := json.Marshal(map[string]any{
		"session_id": "40889d6b-9ee0-4d2b-8b6f-2992662e9e82", "transcript_path": "/nonexistent/t.jsonl", "cwd": cwd,
		"prompt_id": "39564f26-8fd1-4e5e-be0a-27d190106883", "permission_mode": "auto", "hook_event_name": "UserPromptSubmit", "prompt": prompt,
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// run drives `soho hook <sub>` with the given stdin text.
func (h *hookFix) runStdin(sub, stdin string) (int, string, string) {
	h.t.Helper()
	old := hookStdin
	hookStdin = strings.NewReader(stdin)
	defer func() { hookStdin = old }()
	return runIn(h.t, []string{"hook", sub}, h.env, h.cwd)
}

func (h *hookFix) run(sub, prompt string) (int, string, string) {
	h.t.Helper()
	return h.runStdin(sub, promptInput(prompt, h.cwd))
}

func blockJSON(lines ...string) string {
	reason := strings.Join(append(lines, peerauth.ReasonTail), `\n`)
	return `{"decision":"block","reason":"` + reason + `","hookSpecificOutput":{"hookEventName":"UserPromptSubmit","suppressOriginalPrompt":true}}` + "\n"
}

func refusedLine(id string, result peerauth.Result) string {
	return "[soho:peer-refused] #" + id + " " + string(result)
}

func (h *hookFix) read(name string) string {
	raw, err := os.ReadFile(filepath.Join(h.conf, name))
	if err != nil && !os.IsNotExist(err) {
		h.t.Fatal(err)
	}
	return string(raw)
}

func readIfAny(path string) string {
	raw, _ := os.ReadFile(path)
	return string(raw)
}

// logFields splits the receiver's log into its line fields.
func logFields(t *testing.T, h *hookFix) []string {
	t.Helper()
	log := h.read(peerauth.LogFile)
	if strings.TrimSpace(log) == "" {
		t.Fatalf("the receiver log is empty")
	}
	return strings.Split(strings.TrimSpace(log), "\t")
}

// The project's inbound=off is its policy: it blocks, in the Claude JSON
// shape, with the one result that blocks, in the reminder and the inbound
// subcommand, wrapped and bare.
func TestHookInboundProjectOffBlocks(t *testing.T) {
	ids := map[string]string{"local": nextID(), "remote": nextID()}
	for name, build := range map[string]func() string{
		"local":  func() string { return localMessage(ids["local"]) },
		"remote": func() string { return remoteMessage(ids["remote"]) },
	} {
		for _, shape := range []struct {
			name string
			wrap func(string) string
		}{{"wrapped", wrapped}, {"bare", func(m string) string { return m }}} {
			t.Run(name+", "+shape.name, func(t *testing.T) {
				h := newHookFix(t)
				h.projectOff()
				id := ids[name]
				for _, sub := range []string{"reminder", "inbound"} {
					code, out, stderr := h.run(sub, shape.wrap(build()))
					if code != 0 {
						t.Fatalf("%s exit %d, must be 0 (never 2): %q", sub, code, stderr)
					}
					if out != blockJSON(refusedLine(id, peerauth.RefusedInboundOff)) {
						t.Fatalf("%s stdout = %q\nwant %q", sub, out, blockJSON(refusedLine(id, peerauth.RefusedInboundOff)))
					}
					if strings.Contains(out, hookReminder) {
						t.Fatalf("%s printed the reminder with the block", sub)
					}
					if !strings.Contains(stderr, "refused peer message #"+id+" (refused-inbound-off)") {
						t.Fatalf("%s stderr = %q", sub, stderr)
					}
					// No body anywhere a person or a log can read it.
					if strings.Contains(out+stderr+h.read(peerauth.LogFile)+readIfAny(h.log), hookSecret) {
						t.Fatalf("%s leaked the body", sub)
					}
				}
				// Each run logged the refusal once, with the line fields.
				log := strings.Split(strings.TrimSpace(h.read(peerauth.LogFile)), "\n")
				wantFormat, wantLabel := "local", "local"
				if name == "remote" {
					wantFormat, wantLabel = "remote", "-"
				}
				if len(log) != 2 {
					t.Fatalf("two refusals logged once each: %q", log)
				}
				for _, line := range log {
					fields := strings.Split(line, "\t")
					if len(fields) != 7 || fields[1] != wantFormat || fields[2] != wantLabel || fields[3] != id || fields[4] != "w6X:p2" || fields[5] != "refused-inbound-off" || fields[6] == "" {
						t.Fatalf("log line = %q", fields)
					}
				}
			})
		}
	}
}

// Two messages in one prompt, in a project with inbound=off: each gets its
// refusal line, in prompt order, and the prompt is blocked.
func TestHookInboundProjectOffRefusesEveryMessage(t *testing.T) {
	h := newHookFix(t)
	h.projectOff()
	first, second := nextID(), nextID()
	code, out, _ := h.run("inbound", wrapped(localMessage(first)+"\n"+remoteMessage(second)))
	want := blockJSON(refusedLine(first, peerauth.RefusedInboundOff), refusedLine(second, peerauth.RefusedInboundOff))
	if code != 0 || out != want {
		t.Fatalf("code=%d out=%q\nwant %q", code, out, want)
	}
	log := h.read(peerauth.LogFile)
	if n := strings.Count(log, "\n"); n != 2 {
		t.Fatalf("two refusals logged once each: %q", log)
	}
}

// A target project whose configuration cannot be read is refused-error:
// logged, with a friction line, but not a block (the check cannot tell
// whether the project refuses peer messages).
func TestHookInboundProjectUnreadableIsRefusedError(t *testing.T) {
	h := newHookFix(t)
	// A directory in the place of the project file: it cannot be read.
	if err := os.MkdirAll(filepath.Join(h.cwd, ".agents", "soho.conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	id := nextID()
	code, out, stderr := h.run("inbound", wrapped(remoteMessage(id)))
	if code != 0 || out != "" {
		t.Fatalf("refused-error passes the prompt: code=%d out=%q", code, out)
	}
	fields := logFields(t, h)
	if len(fields) != 7 || fields[5] != "refused-error" {
		t.Fatalf("log = %q", fields)
	}
	if !strings.Contains(stderr, "refused peer message #"+id+" (refused-error)") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// An accepted message passes with no block and leaves no trace: no receiver
// log line and no friction line.
func TestHookInboundAcceptedLeavesNoTrace(t *testing.T) {
	for name, build := range map[string]func() string{
		"local":  func() string { return localMessage(nextID()) },
		"remote": func() string { return remoteMessage(nextID()) },
	} {
		t.Run(name, func(t *testing.T) {
			for _, shape := range []func(string) string{wrapped, func(m string) string { return m }} {
				h := newHookFix(t)
				code, out, stderr := h.run("reminder", shape(build()))
				if code != 0 || out != hookReminder+"\n" || stderr != "" {
					t.Fatalf("reminder: code=%d out=%q stderr=%q", code, out, stderr)
				}
				code, out, stderr = h.run("inbound", shape(build()))
				if code != 0 || out != "" || stderr != "" {
					t.Fatalf("inbound: code=%d out=%q stderr=%q", code, out, stderr)
				}
				if h.read(peerauth.LogFile) != "" || readIfAny(h.log) != "" {
					t.Fatalf("an accepted message left a trace: log %q friction %q", h.read(peerauth.LogFile), readIfAny(h.log))
				}
			}
		})
	}
}

// An old sender: a message whose Sender line still ends with the signature
// item is not refused; it is a plain accepted message and gets the reminder.
func TestHookInboundOldSenderIsNotRefused(t *testing.T) {
	h := newHookFix(t)
	id := nextID()
	code, out, stderr := h.run("reminder", wrapped(oldSenderMessage(id)))
	if code != 0 || out != hookReminder+"\n" || stderr != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if h.read(peerauth.LogFile) != "" {
		t.Fatalf("the old sender's signature item was refused: %q", h.read(peerauth.LogFile))
	}
}

// A prompt with no header reads nothing and writes nothing.
func TestHookInboundPlainPromptTouchesNothing(t *testing.T) {
	prompts := []string{"fix the failing test", "", "see [soho:peer] in the docs", wrapped("plain text in a paste"), "> [soho:peer] #0f23aac6 quoted\n> [/soho:peer] #0f23aac6 end of message"}
	h := newHookFix(t)
	before := snapshot(t, h.conf, h.cwd, h.env["SOHO_DIR"])
	for _, prompt := range prompts {
		for _, sub := range []string{"reminder", "inbound"} {
			code, out, stderr := h.run(sub, prompt)
			wantOut := ""
			if sub == "reminder" {
				wantOut = hookReminder + "\n"
			}
			if code != 0 || out != wantOut || stderr != "" {
				t.Fatalf("%s %q: code=%d out=%q stderr=%q", sub, prompt, code, out, stderr)
			}
		}
	}
	if after := snapshot(t, h.conf, h.cwd, h.env["SOHO_DIR"]); after != before {
		t.Fatalf("a prompt without a header changed the disk:\nbefore %s\nafter  %s", before, after)
	}
}

// snapshot lists every path under the roots with its size, mode and mtime.
func snapshot(t *testing.T, roots ...string) string {
	t.Helper()
	var lines []string
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				lines = append(lines, fmt.Sprintf("%s %d %v %d", path, info.Size(), info.Mode(), info.ModTime().UnixNano()))
			}
			return nil
		})
	}
	return strings.Join(lines, "\n")
}

func TestHookInboundInvalidStdinPassesWithAFrictionLine(t *testing.T) {
	inputs := map[string]string{
		"not json":          "this is not json",
		"empty":             "",
		"another event":     `{"hook_event_name":"SessionStart","cwd":"/tmp"}`,
		"no prompt":         `{"hook_event_name":"UserPromptSubmit"}`,
		"prompt not a text": `{"hook_event_name":"UserPromptSubmit","prompt":5}`,
		"an array":          `[1,2]`,
	}
	for name, stdin := range inputs {
		t.Run(name, func(t *testing.T) {
			h := newHookFix(t)
			code, out, stderr := h.runStdin("reminder", stdin)
			if code != 0 || out != hookReminder+"\n" {
				t.Fatalf("code=%d out=%q", code, out)
			}
			if !strings.Contains(stderr, "hook inbound: stdin is not the JSON of a UserPromptSubmit; nothing was checked") {
				t.Fatalf("stderr = %q", stderr)
			}
			if lines := strings.Count(readIfAny(h.log), "\n"); lines != 1 {
				t.Fatalf("friction has %d lines: %q", lines, readIfAny(h.log))
			}
			if h.read(peerauth.LogFile) != "" {
				t.Fatal("an invalid stdin was logged as a peer message")
			}
		})
	}
	t.Run("a stdin up to 4 MiB that is not a prompt", func(t *testing.T) {
		h := newHookFix(t)
		code, out, _ := h.runStdin("inbound", strings.Repeat("x", 4<<20))
		if code != 0 || out != "" {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if !strings.Contains(readIfAny(h.log), "nothing was checked") {
			t.Fatalf("friction = %q", readIfAny(h.log))
		}
		if h.read(peerauth.LogFile) != "" {
			t.Fatal("a stdin that is not a prompt was logged as a refused prompt")
		}
	})
}

const (
	tooLargeLine   = "[soho:peer-refused] prompt-too-large"
	tooLargeReason = "The prompt is larger than 4 MiB, so its peer messages could not be checked; send it in smaller parts."
	// tooLargeBlock is the decision a too-large prompt prints: the two lines
	// are the whole reason, without the refusal tail.
	tooLargeBlock = `{"decision":"block","reason":"` + tooLargeLine + `\n` + tooLargeReason + `","hookSpecificOutput":{"hookEventName":"UserPromptSubmit","suppressOriginalPrompt":true}}` + "\n"
)

// padTo pads a prompt with text to exactly n bytes of the hook's JSON input.
func padTo(t *testing.T, h *hookFix, prompt string, n int) string {
	t.Helper()
	pad := n - len(promptInput(prompt, h.cwd))
	if pad < 0 {
		t.Fatalf("the prompt is already %d bytes", len(promptInput(prompt, h.cwd)))
	}
	out := prompt + " " + strings.Repeat("x", pad-1)
	if got := len(promptInput(out, h.cwd)); got != n {
		t.Fatalf("input is %d bytes, want %d", got, n)
	}
	return out
}

// A stdin over 4 MiB is not interpreted, so nothing in it can be known: the
// prompt is blocked whole with the too-large refusal, whatever else the
// configuration holds.
func TestHookInboundOversizedPromptBlocks(t *testing.T) {
	const limit = 4 << 20
	prompts := map[string]func(t *testing.T, h *hookFix) string{
		"a forged header": func(t *testing.T, h *hookFix) string { return localMessage(nextID()) },
		"no header":       func(t *testing.T, h *hookFix) string { return "just text" },
	}
	for pname, build := range prompts {
		for _, size := range []int{limit + 1, limit + 1<<20} {
			t.Run(fmt.Sprintf("%s, %d bytes", pname, size), func(t *testing.T) {
				h := newHookFix(t)
				prompt := padTo(t, h, build(t, h), size)
				code, out, stderr := h.runStdin("inbound", promptInput(prompt, h.cwd))
				if code != 0 {
					t.Fatalf("exit %d, must be 0 (never 2): %q", code, stderr)
				}
				want := tooLargeBlock
				if out != want {
					t.Fatalf("stdout = %q\nwant     %q", out, want)
				}
				fields := logFields(t, h)
				if len(fields) != 7 || fields[1] != "-" || fields[2] != "-" || fields[3] != "-" || fields[4] != "w6X:p2" || fields[5] != "refused-too-large" || fields[6] != "-" {
					t.Fatalf("log = %q", fields)
				}
				// Nothing of the body leaves the hook.
				all := out + stderr + h.read(peerauth.LogFile) + readIfAny(h.log)
				for _, secret := range []string{hookSecret, "orchestrator-7"} {
					if strings.Contains(all, secret) {
						t.Fatalf("leaked %q: %q", secret, all)
					}
				}
			})
		}
	}
	// The reminder subcommand blocks too, and the too-large line is the one
	// logged.
	h := newHookFix(t)
	code, out, _ := h.runStdin("reminder", promptInput(padTo(t, h, "just text", limit+1), h.cwd))
	if code != 0 || out != tooLargeBlock {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

// A prompt with a header runs the check outside Herdr too; only the reminder
// stays Herdr-only.
func TestHookInboundOutsideHerdr(t *testing.T) {
	h := newHookFix(t)
	h.env["HERDR_ENV"] = ""
	h.projectOff()
	id := nextID()
	code, out, stderr := h.run("inbound", wrapped(remoteMessage(id)))
	if code != 0 || out != blockJSON(refusedLine(id, peerauth.RefusedInboundOff)) {
		t.Fatalf("blocked outside Herdr: code=%d out=%q stderr=%q", code, out, stderr)
	}
	g := newHookFix(t)
	g.env["HERDR_ENV"] = ""
	code, out, stderr = g.run("reminder", wrapped(localMessage(nextID())))
	if code != 0 || out != "" || stderr != "" {
		t.Fatalf("accepted outside Herdr: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

// The hook reads at most 4 MiB.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestHookInboundReadsAtMost4MiB(t *testing.T) {
	h := newHookFix(t)
	counter := &countingReader{r: strings.NewReader(strings.Repeat("x", 16<<20))}
	old := hookStdin
	hookStdin = counter
	defer func() { hookStdin = old }()
	if code, _, _ := runIn(t, []string{"hook", "inbound"}, h.env, h.cwd); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if counter.n > 4<<20+1 {
		t.Fatalf("the hook read %d bytes, limit is 4 MiB", counter.n)
	}
}

func TestHookUsageNamesInbound(t *testing.T) {
	env, cwd := commandFixture(t)
	code, _, stderr := runIn(t, []string{"hook", "other"}, env, cwd)
	if code != 2 || !strings.Contains(stderr, "usage: hook reminder|doctor|inbound") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}
