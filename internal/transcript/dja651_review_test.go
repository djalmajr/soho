package transcript

import (
	"testing"
	"time"
)

// All cases first arm a real, valid journal. Only this reader's limits and
// hook vary; neither the production clock nor global limits are replaced.
func TestDJA651ReviewReaderWorkAccounting(t *testing.T) {
	for _, wait := range []string{"control", "idle", "external_validation"} {
		t.Run(wait, func(t *testing.T) {
			home, path := receiptFixture(t, receiptHeader())
			limits := codexReceiptLimits
			limits.work = 200 * time.Millisecond
			r := newCodexReceipt(home, receiptUUID, receiptClosing, limits, nil)
			if r == nil || r.disabled || r.previous.count != 0 {
				t.Fatal("valid baseline did not arm before work-accounting probe")
			}
			t.Cleanup(r.Close)
			before := r.work
			reads, verify, callbacks := 0, 0, 0
			r.hook = func(stage string) {
				if stage == "after-read" {
					reads++
				}
				if stage == "before-verify" {
					verify++
				}
			}
			// An old poll must read the baseline but cannot reach validation.
			if r.Receipt(func() bool { callbacks++; return true }) || r.disabled || reads != 1 || callbacks != 0 {
				t.Fatal("armed old poll did not remain eligible without validating")
			}
			if wait == "idle" {
				time.Sleep(limits.work + 100*time.Millisecond)
			}
			receiptAppend(t, path, receiptMessage(t, "user", receiptClosing))
			taken := r.Receipt(func() bool {
				callbacks++
				if reads != 2 || verify != 1 {
					t.Errorf("validation stage: reads=%d before-verify=%d", reads, verify)
				}
				if wait == "external_validation" {
					time.Sleep(limits.work + 100*time.Millisecond)
				}
				return true
			})
			t.Logf("wait=%s callbacks=%d reads=%d before-verify=%d work_before=%s work_after=%s disabled=%v", wait, callbacks, reads, verify, before, r.work, r.disabled)
			if callbacks != 1 || verify != 1 {
				t.Fatalf("receipt validation was not reached exactly once: callbacks=%d verify=%d", callbacks, verify)
			}
			if !taken || r.disabled || reads != 3 {
				t.Errorf("idle/external waiting consumed reader work: wait=%s taken=%v disabled=%v reads=%d; want receipt and final reread", wait, taken, r.disabled, reads)
			}
		})
	}
}

func TestDJA651ReviewCumulativeReaderWorkDisablesReceipt(t *testing.T) {
	home, path := receiptFixture(t, receiptHeader())
	limits := codexReceiptLimits
	limits.work = 300 * time.Millisecond
	r := newCodexReceipt(home, receiptUUID, receiptClosing, limits, nil)
	if r == nil || r.disabled || r.previous.count != 0 {
		t.Fatal("valid baseline did not arm before cumulative-work probe")
	}
	t.Cleanup(r.Close)
	// Each hook is bounded below the budget. Four reads, spread over three
	// polls, exceed it; no individual Receipt call does so on its own.
	const work = 80 * time.Millisecond
	reads, callbacks, verify := 0, 0, 0
	r.hook = func(stage string) {
		switch stage {
		case "after-read":
			reads++
			time.Sleep(work)
		case "before-verify":
			verify++
		}
	}
	for poll := 1; poll <= 2; poll++ {
		before := r.work
		if r.Receipt(func() bool { callbacks++; return true }) || r.disabled {
			t.Fatalf("valid old poll %d failed before cumulative exhaustion: disabled=%v work=%s", poll, r.disabled, r.work)
		}
		if reads != poll || callbacks != 0 || before-r.work < work {
			t.Errorf("poll work was not charged cumulatively: poll=%d reads=%d callbacks=%d spent=%s", poll, reads, callbacks, before-r.work)
		}
	}
	receiptAppend(t, path, receiptMessage(t, "user", receiptClosing))
	taken := r.Receipt(func() bool { callbacks++; return true })
	t.Logf("reads=%d callbacks=%d before-verify=%d work=%s disabled=%v", reads, callbacks, verify, r.work, r.disabled)
	if callbacks != 1 || verify != 1 || reads != 4 {
		t.Fatalf("cumulative proof did not reach validation and final read: callbacks=%d verify=%d reads=%d", callbacks, verify, reads)
	}
	if taken || !r.disabled {
		t.Error("actual cumulative reader work did not permanently disable receipt")
	}
	r.hook = nil
	receiptAppend(t, path, receiptMessage(t, "user", receiptClosing))
	if r.Receipt(func() bool { callbacks++; return true }) || callbacks != 1 || !r.disabled {
		t.Error("work-exhausted reader revived after more matching growth")
	}
}
