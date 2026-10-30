package peerauth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Result is what the receiver decided about one header.
type Result string

const (
	Accepted          Result = "accepted"
	RefusedInboundOff Result = "refused-inbound-off"
	RefusedError      Result = "refused-error"
	// RefusedTooLarge is the logged result of a prompt over the hook's input
	// ceiling, which was not interpreted.
	RefusedTooLarge Result = "refused-too-large"
)

const (
	// ReasonTail is the last line of the block reason.
	ReasonTail = "The refused text stays in the input; clear it (Ctrl+C) before the next message."

	// TooLargeLine and TooLargeReason are the two lines of the block reason
	// for a prompt over the hook's input ceiling.
	TooLargeLine   = "[soho:peer-refused] prompt-too-large"
	TooLargeReason = "The prompt is larger than 4 MiB, so its peer messages could not be checked; send it in smaller parts."

	LogFile = "peer-inbound.tsv"
)

// Source is everything a check reads, behind functions, so a prompt without a
// header reads none of it.
type Source struct {
	// Dir is the user config dir: the receiver's log lives under it.
	Dir string
	// ProjectInboundOff reports whether the target project's `inbound` is off.
	ProjectInboundOff func() (bool, error)
	// Pane is the receiving pane (HERDR_PANE_ID), logged; "" is logged as "-".
	Pane string
	// Now is the receiver's clock.
	Now func() time.Time
}

// Verdict is the decision about one header.
type Verdict struct {
	Header Header
	Result Result
	// Format is "local" or "remote".
	Format string
	// Label is the logged label: `local` for a local header, "-" for a remote
	// one (the check does not read the Sender line).
	Label string
}

// Outcome is the decision about a prompt.
type Outcome struct {
	// Evaluated is false for a prompt with no header: nothing was read.
	Evaluated bool
	Verdicts  []Verdict
	// Block is the whole prompt's fate.
	Block bool
	// TooLarge marks the outcome of a prompt over the hook's input ceiling:
	// it was not interpreted, so it has no verdicts.
	TooLarge bool
}

// RefusalLine is the hook's block-reason line for one refused header:
// `[soho:peer-refused:<id>] <result>`. The size refusal names no message id
// and keeps its own line (TooLargeLine). The sender accepts this line and
// the previous generation's `[soho:peer-refused] #<id>` line.
func RefusalLine(id string, result Result) string {
	return "[soho:peer-refused:" + id + "] " + string(result)
}

// Reason is the text of a block decision: one line per header, in prompt
// order, and the tail. It carries no body and no label.
func (o Outcome) Reason() string {
	if o.TooLarge {
		return TooLargeLine + "\n" + TooLargeReason
	}
	lines := make([]string, 0, len(o.Verdicts)+1)
	for _, v := range o.Verdicts {
		lines = append(lines, RefusalLine(v.Header.ID, v.Result))
	}
	return strings.Join(append(lines, ReasonTail), "\n")
}

// CodexReason keeps the refusal lines and describes Codex's blocked delivery.
func (o Outcome) CodexReason() string {
	const tail = "The message did not reach the model; nothing needs clearing."
	if o.TooLarge {
		return o.Reason() + "\n" + tail
	}
	return strings.TrimSuffix(o.Reason(), ReasonTail) + tail
}

// Refused lists the verdicts that are not accepted.
func (o Outcome) Refused() []Verdict {
	var out []Verdict
	for _, v := range o.Verdicts {
		if v.Result != Accepted {
			out = append(out, v)
		}
	}
	return out
}

// Check decides every header of a prompt. A prompt with no header returns at
// once, having read no configuration or log and created no file. Otherwise the
// project's inbound decides each header: a project with inbound=off refuses
// (the project's policy), a project configuration that cannot be read is
// refused-error, and the message otherwise passes. The check reads nothing
// else and verifies nothing: peer messages travel only between the owner's
// own machines, and a message from an older sender may still carry a
// `from`/`sig v1:` item on the Sender line, which is plain text to the check.
func Check(prompt string, src Source) Outcome {
	headers := FindHeaders(prompt)
	if len(headers) == 0 {
		return Outcome{}
	}
	now := time.Now
	if src.Now != nil {
		now = src.Now
	}
	projectOff, projectErr := src.ProjectInboundOff()

	verdicts := make([]Verdict, len(headers))
	for i, h := range headers {
		v := Verdict{Header: h, Format: "local", Label: "local"}
		if h.Remote {
			v.Format, v.Label = "remote", "-"
		}
		switch {
		case projectErr != nil:
			v.Result = RefusedError
		case projectOff:
			v.Result = RefusedInboundOff
		default:
			v.Result = Accepted
		}
		verdicts[i] = v
	}
	out := Outcome{Evaluated: true, Block: projectOff, Verdicts: verdicts}
	logVerdicts(src, out, now())
	return out
}

// logVerdicts appends one line per evaluated header to the receiver's log,
// except for accepted headers, which leave no trace. The log is best effort:
// a write that fails changes no verdict.
func logVerdicts(src Source, out Outcome, at time.Time) {
	var b strings.Builder
	pane := logPane(src)
	for _, v := range out.Verdicts {
		if v.Result == Accepted {
			continue
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n", at.UTC().Format(time.RFC3339), v.Format, v.Label, v.Header.ID, pane, v.Result, v.Header.Chars())
	}
	appendLog(src, b.String())
}

func logPane(src Source) string {
	if pane := tsvField(src.Pane); pane != "" {
		return pane
	}
	return "-"
}

// appendLog appends lines to the receiver's log, best effort.
func appendLog(src Source, lines string) {
	if lines == "" {
		return
	}
	if os.MkdirAll(src.Dir, 0o700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(src.Dir, LogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(lines)
	_ = f.Close()
}

// CheckTooLarge decides a prompt the hook did not interpret because its input
// is over the ceiling: nothing in it can be known, so the prompt is blocked
// whole with the too-large refusal. Only the log is touched: one line with `-`
// for the format, label and id and a `-` for the character count, which is
// unknown past the ceiling.
func CheckTooLarge(src Source) Outcome {
	now := time.Now
	if src.Now != nil {
		now = src.Now
	}
	out := Outcome{Evaluated: true, Block: true, TooLarge: true}
	appendLog(src, fmt.Sprintf("%s\t-\t-\t-\t%s\t%s\t-\n", now().UTC().Format(time.RFC3339), logPane(src), RefusedTooLarge))
	return out
}

func tsvField(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
