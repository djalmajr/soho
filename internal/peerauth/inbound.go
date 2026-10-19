package peerauth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// Mode is the receiver's inbound_auth.
type Mode string

const (
	ModeOff     Mode = "off"
	ModeWarn    Mode = "warn"
	ModeEnforce Mode = "enforce"
)

// Result is what the receiver decided about one header.
type Result string

const (
	Accepted           Result = "accepted"
	RefusedInboundOff  Result = "refused-inbound-off"
	RefusedUnsigned    Result = "refused-unsigned"
	RefusedBadSig      Result = "refused-bad-signature"
	RefusedNotAllowed  Result = "refused-not-allowed"
	RefusedNoKey       Result = "refused-no-key"
	RefusedError       Result = "refused-error"
	RefusedStale       Result = "refused-stale"
	RefusedReplay      Result = "refused-replay"
	RefusedWithAnother Result = "refused-with-another"
)

const (
	// RefusalMarker opens the line the hook's block reason (and the sender's
	// proof) carries for each refused header.
	RefusalMarker = "[soho:peer-refused] #"
	// ReasonTail is the last line of the block reason.
	ReasonTail = "The refused text stays in the input; clear it (Ctrl+C) before the next message."

	// InvalidLabel is how a label that failed the name pattern is logged.
	InvalidLabel = "-invalid-"

	// ClockSkew is how far from the receiver's clock a message's send time may
	// be, in both directions.
	ClockSkew = 10 * time.Minute
	// SeenTTL is how long an accepted message stays in the replay cache: the
	// skew on both sides of the clock.
	SeenTTL = 2 * ClockSkew

	SeenFile = "peer-inbound-seen.tsv"
	LogFile  = "peer-inbound.tsv"

	// maxSeenFile bounds a replay cache read.
	maxSeenFile = 4 << 20
)

// Policy is the receiver's inbound configuration, read from the user config
// file only.
type Policy struct {
	Mode Mode
	// ModeInvalid reports that inbound_auth held a value that is not
	// off|warn|enforce: it applies as enforce.
	ModeInvalid bool
	// ModeValue and AllowValue are the raw values as written.
	ModeValue  string
	AllowValue string
	Allow      []string
}

// Allowed reports whether label is in the allowlist.
func (p Policy) Allowed(label string) bool {
	for _, l := range p.Allow {
		if l == label {
			return true
		}
	}
	return false
}

// ParsePolicy reads the two keys out of the config pairs: inbound_auth
// (off by default; any value that is not off|warn|enforce applies as enforce)
// and inbound_allow (labels split by `|`, possibly empty).
func ParsePolicy(pairs []core.ConfigPair) Policy {
	p := Policy{Mode: ModeOff}
	for _, pair := range pairs {
		switch pair.Key {
		case "inbound_auth":
			p.ModeValue = pair.Value
			switch pair.Value {
			case "", "off":
				p.Mode, p.ModeInvalid = ModeOff, false
			case "warn":
				p.Mode, p.ModeInvalid = ModeWarn, false
			case "enforce":
				p.Mode, p.ModeInvalid = ModeEnforce, false
			default:
				p.Mode, p.ModeInvalid = ModeEnforce, true
			}
		case "inbound_allow":
			p.AllowValue = pair.Value
			p.Allow = splitAllow(pair.Value)
		}
	}
	return p
}

func splitAllow(value string) []string {
	var out []string
	for _, label := range strings.Split(value, "|") {
		if label = strings.TrimSpace(label); label != "" {
			out = append(out, label)
		}
	}
	return out
}

// LoadPolicy reads the policy from the user config file. A file that does not
// exist is the default policy; one that cannot be read is an error.
func LoadPolicy(file string) (Policy, error) {
	pairs, err := core.ReadConfigFilePairs(file)
	if err != nil {
		if os.IsNotExist(err) {
			return Policy{Mode: ModeOff}, nil
		}
		return Policy{}, err
	}
	return ParsePolicy(pairs), nil
}

// Source is everything a check reads, behind functions, so a prompt without a
// header reads none of it.
type Source struct {
	// Dir is the user config dir: the key files, the replay cache and the log
	// live under it.
	Dir string
	// Policy loads inbound_auth and inbound_allow from the user config file.
	Policy func() (Policy, error)
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
	// Label is the logged label: `local`, the sender label or InvalidLabel.
	Label string
}

// Outcome is the decision about a prompt.
type Outcome struct {
	// Evaluated is false for a prompt with no header: nothing was read.
	Evaluated bool
	Mode      Mode
	Verdicts  []Verdict
	// Block is the whole prompt's fate.
	Block bool
}

// Reason is the text of a block decision: one line per header, in prompt
// order, and the tail. It carries no body, no signature and no label.
func (o Outcome) Reason() string {
	lines := make([]string, 0, len(o.Verdicts)+1)
	for _, v := range o.Verdicts {
		lines = append(lines, RefusalMarker+v.Header.ID+" "+string(v.Result))
	}
	return strings.Join(append(lines, ReasonTail), "\n")
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
// once, having read no configuration, key, replay cache or log and created no
// file. Otherwise, per header and in this order, the first result wins:
// the project's inbound, the mode, the format, the label, the key, the MAC,
// the send time and the replay.
//
// In enforce a prompt with a header that is not accepted is blocked whole, and
// the headers that would have been accepted become refused-with-another and
// stay out of the replay cache. In warn nothing is blocked. An inbound=off
// project blocks in every mode, warn included. Anything the check cannot read is
// refused-error, which blocks in enforce.
func Check(prompt string, src Source) Outcome {
	headers := FindHeaders(prompt)
	if len(headers) == 0 {
		return Outcome{}
	}
	now := time.Now
	if src.Now != nil {
		now = src.Now
	}
	out := Outcome{Evaluated: true, Mode: ModeEnforce}
	policy, policyErr := src.Policy()
	if policyErr == nil {
		out.Mode = policy.Mode
	}
	projectOff, projectErr := src.ProjectInboundOff()

	verdicts := make([]Verdict, len(headers))
	var pending []int
	var entries []seenEntry
	at := now()
	for i, h := range headers {
		v := Verdict{Header: h, Format: "local", Label: LocalName}
		if h.Remote {
			v.Format = "remote"
		}
		c := Canonical(h.Text)
		if h.Remote {
			v.Label = InvalidLabel
			if ValidName(c.From) {
				v.Label = c.From
			}
		}
		switch {
		case projectErr != nil:
			v.Result = RefusedError
		case projectOff:
			v.Result = RefusedInboundOff
		case policyErr != nil:
			v.Result = RefusedError
		case policy.Mode == ModeOff:
			v.Result = Accepted
		default:
			v.Result = judge(h, c, v.Label, policy, src.Dir, at)
		}
		if v.Result == Accepted && policyErr == nil && policy.Mode != ModeOff && projectErr == nil && !projectOff {
			pending = append(pending, i)
			entries = append(entries, seenEntry{label: v.Label, id: h.ID, sent: h.Sent})
		}
		verdicts[i] = v
	}

	if len(pending) > 0 {
		replayed, err := recordAccepted(src.Dir, entries, verdicts, out.Mode, at)
		for k, i := range pending {
			switch {
			case err != nil:
				verdicts[i].Result = RefusedError
			case replayed[k]:
				verdicts[i].Result = RefusedReplay
			}
		}
	}

	out.Block = blocks(out.Mode, verdicts)
	if out.Block && out.Mode == ModeEnforce {
		for i := range verdicts {
			if verdicts[i].Result == Accepted {
				verdicts[i].Result = RefusedWithAnother
			}
		}
	}
	out.Verdicts = verdicts
	logVerdicts(src, out, at)
	return out
}

// blocks decides the prompt's fate. An inbound=off project blocks in every
// mode: it is the project's policy, apart from authentication. Otherwise warn
// and off block nothing and enforce blocks on any header that is not accepted.
func blocks(mode Mode, verdicts []Verdict) bool {
	for _, v := range verdicts {
		switch {
		case v.Result == RefusedInboundOff:
			return true
		case mode == ModeEnforce && v.Result != Accepted:
			return true
		}
	}
	return false
}

// judge runs the checks that come after the mode, up to the send time. The
// replay check is the caller's, under the cache lock.
func judge(h Header, c Canon, label string, policy Policy, dir string, at time.Time) Result {
	switch {
	case !h.Closed:
		return RefusedBadSig
	case h.Remote && !c.HasFrom:
		return RefusedUnsigned
	case !h.Remote && c.HasFrom:
		return RefusedBadSig
	case !c.HasSig:
		return RefusedUnsigned
	}
	keyName := LocalName
	if h.Remote {
		// local is the reserved name of this machine's own key: no remote
		// sender is it.
		if !ValidName(c.From) || c.From == LocalName || !policy.Allowed(c.From) {
			return RefusedNotAllowed
		}
		keyName = c.From
	}
	key, status := ReadKey(dir, keyName)
	switch status {
	case KeyMissing:
		return RefusedNoKey
	case KeyInvalid:
		return RefusedError
	}
	if !Verify(key, c) {
		return RefusedBadSig
	}
	sent, ok := parseSent(h.Sent)
	if !ok {
		return RefusedStale
	}
	if d := at.Sub(sent); d > ClockSkew || d < -ClockSkew {
		return RefusedStale
	}
	return Accepted
}

// seenEntry is one accepted message in the replay cache: the tuple
// (label, id, sent), and when this receiver saw it.
type seenEntry struct {
	label, id, sent string
	seen            time.Time
}

func (e seenEntry) key() string { return e.label + "\t" + e.id + "\t" + e.sent }

// recordAccepted checks the candidates against the replay cache and, when
// none of the prompt's headers is refused, adds them to it, all under one
// lock: two hooks with the same message accept it exactly once. replayed[k]
// reports candidate k as already seen (in the cache, or earlier in this
// prompt). The cache is pruned on every write.
func recordAccepted(dir string, candidates []seenEntry, verdicts []Verdict, mode Mode, at time.Time) (replayed []bool, err error) {
	replayed = make([]bool, len(candidates))
	err = withCacheLock(dir, func() error {
		seen, readErr := readSeen(filepath.Join(dir, SeenFile), at)
		if readErr != nil {
			return readErr
		}
		index := map[string]bool{}
		for _, e := range seen {
			index[e.key()] = true
		}
		clean := true
		for k, c := range candidates {
			if index[c.key()] {
				replayed[k] = true
				clean = false
				continue
			}
			index[c.key()] = true
		}
		// A header refused for another reason keeps all of the prompt's
		// candidates out of the cache in enforce; warn lets them in. The
		// candidates themselves are still accepted at this point.
		for _, v := range verdicts {
			if v.Result != Accepted {
				clean = false
			}
		}
		if !clean && mode == ModeEnforce {
			return nil
		}
		for k, c := range candidates {
			if !replayed[k] {
				c.seen = at
				seen = append(seen, c)
			}
		}
		return writeSeen(filepath.Join(dir, SeenFile), seen, at)
	})
	return replayed, err
}

// withCacheLock runs fn under the cache lock (core.WithRosterLock, the
// repository's directory lock, taken on the user config dir). A lock that
// cannot be taken is an error, not a crash. The lock is the one the roster
// uses so no second locking scheme exists; it keeps its agents.lock in dir,
// the user config dir, next to peer-keys/.
func withCacheLock(dir string, fn func() error) (err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	defer func() {
		if failure := recover(); failure != nil {
			if exit, ok := failure.(*platform.ExitError); ok {
				err = fmt.Errorf("peer replay cache lock: %s", exit.Msg)
				return
			}
			panic(failure)
		}
	}()
	var inner error
	core.WithRosterLock(dir, func() { inner = fn() })
	return inner
}

// readSeen reads the cache, dropping the entries older than SeenTTL. A file
// that does not exist is an empty cache; malformed lines are skipped.
func readSeen(path string, at time.Time) ([]seenEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if info.Size() > maxSeenFile {
		return nil, fmt.Errorf("peer replay cache is too large")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []seenEntry
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		seen, perr := time.Parse(time.RFC3339, f[3])
		if perr != nil || at.Sub(seen) > SeenTTL {
			continue
		}
		out = append(out, seenEntry{label: f[0], id: f[1], sent: f[2], seen: seen})
	}
	return out, nil
}

// writeSeen replaces the cache with the entries, atomically.
func writeSeen(path string, entries []seenEntry, at time.Time) error {
	var b strings.Builder
	for _, e := range entries {
		if at.Sub(e.seen) > SeenTTL {
			continue
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", e.label, e.id, e.sent, e.seen.UTC().Format(time.RFC3339))
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".peer-inbound-seen-*")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(b.String())
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// logVerdicts appends one line per evaluated header to the receiver's log,
// except in off, where an accepted header leaves no trace. The log is best
// effort: a write that fails changes no verdict.
func logVerdicts(src Source, out Outcome, at time.Time) {
	var b strings.Builder
	pane := tsvField(src.Pane)
	if pane == "" {
		pane = "-"
	}
	for _, v := range out.Verdicts {
		if out.Mode == ModeOff && v.Result == Accepted {
			continue
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", at.UTC().Format(time.RFC3339), out.Mode, v.Format, v.Label, v.Header.ID, pane, v.Result, v.Header.Chars())
	}
	if b.Len() == 0 {
		return
	}
	if os.MkdirAll(src.Dir, 0o700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(src.Dir, LogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(b.String())
	_ = f.Close()
}

func tsvField(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
