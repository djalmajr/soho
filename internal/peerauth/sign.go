package peerauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	// macDomain starts the MAC input, before the canonical message text.
	macDomain = "soho-peer-v1\n"
	// sigItemPrefix opens the signature item of the Sender line.
	sigItemPrefix = "sig v1:"
	// fromItemPrefix opens the item that names the sender machine's label.
	fromItemPrefix = "from "
	senderPrefix   = "Sender:"
	// sigHexLen is the size of the signature as written: 16 bytes in hex.
	sigHexLen = 32
)

// Canon is a message reduced to what the MAC covers, plus what the Sender
// line says about it.
type Canon struct {
	// Text is the canonical message text: CRLF as LF, trailing spaces and tabs
	// of every line gone, and the `sig v1:<hex>` item of the Sender line
	// removed (the whole Sender line when it held nothing else).
	Text string
	// Sig is the signature as written ("" when HasSig is false).
	Sig    string
	HasSig bool
	// From is the label of the `from` item that closes the Sender line, before
	// the signature.
	From    string
	HasFrom bool
}

// Canonical canonicalizes a message text, from its opening line to its
// closing line. Only the Sender line, the line after the opening one, is read
// for the signature, and only its last item counts as one: a `sig v1:` inside
// a name or anywhere else is plain text the MAC covers.
func Canonical(text string) Canon {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	c := Canon{}
	if len(lines) > 1 && strings.HasPrefix(lines[1], senderPrefix) {
		body := strings.TrimPrefix(strings.TrimPrefix(lines[1], senderPrefix), " ")
		if strings.HasSuffix(body, ".") {
			var items []string
			if inner := strings.TrimSuffix(body, "."); inner != "" {
				items = strings.Split(inner, "; ")
			}
			if n := len(items); n > 0 && strings.HasPrefix(items[n-1], sigItemPrefix) {
				c.Sig, c.HasSig = strings.TrimPrefix(items[n-1], sigItemPrefix), true
				items = items[:n-1]
			}
			if n := len(items); n > 0 && strings.HasPrefix(items[n-1], fromItemPrefix) {
				c.From, c.HasFrom = strings.TrimPrefix(items[n-1], fromItemPrefix), true
			}
			if c.HasSig {
				if len(items) == 0 {
					lines = append(lines[:1], lines[2:]...)
				} else {
					lines[1] = senderPrefix + " " + strings.Join(items, "; ") + "."
				}
			}
		}
	}
	c.Text = strings.Join(lines, "\n")
	return c
}

// mac is the signature of a canonical text: HMAC-SHA256 over the domain and
// the text, cut to 16 bytes, in lowercase hex.
func mac(key []byte, canonical string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(macDomain))
	h.Write([]byte(canonical))
	return hex.EncodeToString(h.Sum(nil)[:sigHexLen/2])
}

// Verify reports whether the signature the message carries is the MAC of its
// canonical text under key. The comparison is hmac.Equal.
func Verify(key []byte, c Canon) bool {
	if !c.HasSig {
		return false
	}
	return hmac.Equal([]byte(mac(key, c.Text)), []byte(c.Sig))
}

// ErrNotSignable is Sign's refusal of a message it cannot place a signature in.
var ErrNotSignable = errors.New("message cannot carry a signature")

// Sign adds the signature to an unsigned message (the opening line to the
// closing line, the Sender line already carrying the `from` item when there is
// one). The signature goes at the end of the Sender line, before its period;
// a message with no Sender line gets `Sender: sig v1:<hex>.` after the opening
// line, so a signed message always has one.
func Sign(key []byte, message string) (string, error) {
	c := Canonical(message)
	if c.HasSig {
		return "", ErrNotSignable
	}
	sig := sigItemPrefix + mac(key, c.Text)
	lines := strings.Split(message, "\n")
	if len(lines) < 2 {
		return "", ErrNotSignable
	}
	if strings.HasPrefix(lines[1], senderPrefix) {
		if !strings.HasSuffix(lines[1], ".") {
			return "", ErrNotSignable
		}
		lines[1] = strings.TrimSuffix(lines[1], ".") + "; " + sig + "."
	} else {
		lines = append(lines[:1], append([]string{senderPrefix + " " + sig + "."}, lines[1:]...)...)
	}
	return strings.Join(lines, "\n"), nil
}
