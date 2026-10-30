package peer

import (
	"strings"
	"testing"
)

func TestSohoPeerMarkers(t *testing.T) {
	if PeerPrefix != "[soho:peer]" {
		t.Fatalf("PeerPrefix=%q, want the soho opening marker", PeerPrefix)
	}
	if PeerEndPrefix != "[/soho:peer]" {
		t.Fatalf("PeerEndPrefix=%q, want the soho closing marker", PeerEndPrefix)
	}
	id := "01020304"
	end := PeerEndLine(id)
	if end != "[/soho:peer:01020304]" {
		t.Fatalf("PeerEndLine(%q)=%q", id, end)
	}
	if open := PeerOpenTag(id); open != "[soho:peer:01020304]" {
		t.Fatalf("PeerOpenTag(%q)=%q", id, open)
	}
	header := PeerHeaderFor(PeerSender{Ref: "local/w1:p1"}, id)
	if !strings.HasPrefix(header, "[soho:peer:01020304]\n") {
		t.Fatalf("sent header does not open with the soho opening line:\n%s", header)
	}
	if !strings.Contains(header, "soho send local/w1:p1") {
		t.Fatalf("sent header reply line is not the soho send command:\n%s", header)
	}
}

// DJA-318: the herdr-soho generation's markers are no longer read or
// accepted. A message that opens with the legacy marker is not a peer
// message: every recognition point treats it the same as ordinary text,
// while the soho marker keeps proving delivery.
func TestSohoPeerLegacyMarkersNotAccepted(t *testing.T) {
	id := "01020304"
	legacyEnd := "[/herdr-soho:peer] #" + id + " end of message"
	legacyOpen := "[herdr-soho:peer] #" + id + " Message from another agent — local/w1:p1."
	plainEnd := "Some other text #" + id + " end of message"
	plainOpen := "Some other text #" + id + " Message from another agent — local/w1:p1."
	currentEnd := "[/soho:peer:" + id + "]"
	currentOpen := "[soho:peer:" + id + "]"
	// The previous format's lines are not the current closing or opening
	// line of a new-format send: the receipt checks do not read them.
	previousEnd := "[/soho:peer] #" + id + " end of message"
	previousOpen := "[soho:peer] #" + id + " Message from another agent — local/w1:p1."
	legacyScreen := "› " + legacyOpen + "\n• working\n" + legacyEnd + "\n• done\n"
	plainScreen := "› " + plainOpen + "\n• working\n" + plainEnd + "\n• done\n"
	currentScreen := "› " + currentOpen + "\n• working\n" + currentEnd + "\n• done\n"
	t.Run("the legacy closing line is recognized the way an ordinary line is", func(t *testing.T) {
		if got, plain := peerEndLineIn(legacyEnd, id), peerEndLineIn(plainEnd, id); got || got != plain {
			t.Fatalf("peerEndLineIn: legacy %q=%v, ordinary %q=%v; want both false", legacyEnd, got, plainEnd, plain)
		}
		if peerEndLineIn("> "+legacyEnd, id) {
			t.Fatal("a quoted legacy closing line counted as an end line")
		}
	})
	t.Run("the legacy opening header is recognized the way an ordinary line is", func(t *testing.T) {
		for name, fn := range map[string]func(string, string) bool{"receiptLineIn": receiptLineIn, "composerHeaderLineIn": composerHeaderLineIn} {
			if got, plain := fn(legacyOpen, id), fn(plainOpen, id); got || got != plain {
				t.Fatalf("%s: legacy %q=%v, ordinary %q=%v; want both false", name, legacyOpen, got, plainOpen, plain)
			}
		}
		if receiptLineIn("> "+legacyOpen, id) || composerHeaderLineIn("> "+legacyOpen, id) {
			t.Fatal("a quoted legacy opening header counted as a marker")
		}
	})
	t.Run("a legacy codex history turn is read the way an ordinary turn is", func(t *testing.T) {
		if got, plain := peerMessageInHistory(legacyScreen, id), peerMessageInHistory(plainScreen, id); got || got != plain {
			t.Fatalf("peerMessageInHistory: legacy screen=%v, ordinary screen=%v; want both false", got, plain)
		}
	})
	t.Run("the soho marker still proves delivery (control)", func(t *testing.T) {
		if !peerEndLineIn(currentEnd, id) || !receiptLineIn(currentEnd, id) || !composerHeaderLineIn(currentOpen, id) || !peerMessageInHistory(currentScreen, id) {
			t.Fatalf("the soho marker stopped proving delivery: end=%v receipt=%v composer=%v history=%v",
				peerEndLineIn(currentEnd, id), receiptLineIn(currentEnd, id), composerHeaderLineIn(currentOpen, id), peerMessageInHistory(currentScreen, id))
		}
	})
	t.Run("the previous format's lines are not proof of a new-format send", func(t *testing.T) {
		if peerEndLineIn(previousEnd, id) || receiptLineIn(previousEnd, id) || receiptLineIn(previousOpen, id) || composerHeaderLineIn(previousOpen, id) {
			t.Fatalf("the previous format's lines proved a new-format send: end=%q header=%q", previousEnd, previousOpen)
		}
	})
}
