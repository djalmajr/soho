package peer_test

import (
	"bytes"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/peerauth"
)

// `send --assignment` signs like the local `send` (DJA-196): with
// peer-keys/local.key when it exists, no `from`, the Assignment line inside
// the MAC; an invalid key refuses the delivery before `agent prompt`.

// assignmentMessage runs the assigned send in a fresh fixture and returns the
// one prompt it delivered. key is written as peer-keys/local.key first.
func assignmentMessage(t *testing.T, withKey bool) (message string, key []byte, assignment string) {
	t.Helper()
	f, _, a := assignedFixture(t, nil)
	// The message id is seeded for this send only: the next fixture of the
	// same test needs the real reader to create its assignment.
	oldReader := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	defer func() { rand.Reader = oldReader }()
	if withKey {
		key = newPeerKey(t, userConfigDir(f), "local")
	}
	code, out, stderr := f.run([]string{"send", "reviewer", "question\nsecond line", "--assignment", a.ID, "--type", "review.question"})
	if code != 0 || !strings.Contains(out, `"status":"submitted"`) {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	prompts := assignmentPrompts(t, f)
	if len(prompts) != 1 || len(prompts[0].Argv) != 4 {
		t.Fatalf("prompts = %+v", prompts)
	}
	return prompts[0].Argv[3], key, a.ID
}

func TestAssignedSendSignsWithTheLocalKey(t *testing.T) {
	message, key, _ := assignmentMessage(t, true)
	canon := peerauth.Canonical(message)
	if !canon.HasSig || canon.HasFrom || !peerauth.Verify(key, canon) {
		t.Fatalf("assignment signature: %+v\n%s", canon, message)
	}
	if line := strings.Split(message, "\n")[1]; !strings.Contains(line, "; sig v1:") || strings.Contains(message, "; from ") {
		t.Fatalf("sender line = %q", line)
	}
	if !strings.Contains(message, "\nAssignment: ") {
		t.Fatalf("the Assignment line is gone: %q", message)
	}

	// The Assignment line is inside the MAC: a changed round breaks it.
	var tampered []string
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, "Assignment: ") {
			line = strings.Replace(line, "round: ", "round: 9", 1)
		}
		tampered = append(tampered, line)
	}
	if peerauth.Verify(key, peerauth.Canonical(strings.Join(tampered, "\n"))) {
		t.Fatal("a changed Assignment line still verifies")
	}
	// A body changed after signing breaks it too.
	if peerauth.Verify(key, peerauth.Canonical(strings.Replace(message, "> question", "> questions", 1))) {
		t.Fatal("a changed body still verifies")
	}

	// A receiver in enforce, with the same local key, accepts the exact text
	// the fake herdr received.
	dir := filepath.Join(t.TempDir(), "soho")
	newPeerKey(t, dir, "local")
	outcome := peerauth.Check(message, peerauth.Source{
		Dir:               dir,
		Policy:            func() (peerauth.Policy, error) { return peerauth.Policy{Mode: peerauth.ModeEnforce}, nil },
		ProjectInboundOff: func() (bool, error) { return false, nil },
		Pane:              "ws:p2",
		Now:               func() time.Time { return peerTestNow },
	})
	if outcome.Block || len(outcome.Verdicts) != 1 || outcome.Verdicts[0].Result != peerauth.Accepted {
		t.Fatalf("enforce did not accept the signed assignment message: %+v", outcome)
	}
}

func TestAssignedSendWithoutAKeyKeepsTodaysBytes(t *testing.T) {
	unsigned, _, unsignedID := assignmentMessage(t, false)
	if strings.Contains(unsigned, "sig v1") || peerauth.Canonical(unsigned).HasSig {
		t.Fatalf("an unsigned send carries a signature: %q", unsigned)
	}
	// The same send with a key differs from it by the signature item only.
	// (the assignment id is random per fixture: it is masked in both).
	signed, _, signedID := assignmentMessage(t, true)
	got := strings.ReplaceAll(peerauth.Canonical(signed).Text, signedID, "<assignment>")
	if unsigned = strings.ReplaceAll(unsigned, unsignedID, "<assignment>"); got != unsigned {
		t.Fatalf("the signature is not the only difference:\n signed (sans sig) %q\n unsigned          %q", got, unsigned)
	}
}

func TestAssignedSendInvalidKeyRefusesBeforePrompt(t *testing.T) {
	for name, body := range map[string]string{
		"wrong size": "abcd\n",
		"not hex":    strings.Repeat("zz", 32) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, _, a := assignedFixture(t, nil)
			path, _ := peerauth.KeyPath(userConfigDir(f), "local")
			mustWrite(t, path, body)
			config := core.LoadConfig(f.env, f.dir)
			result, err := peer.SendAssignmentMessage(peer.AssignmentMessageOptions{AssignmentID: a.ID, Target: "reviewer", Type: "review.question", Body: "question", Config: &config, Env: f.env, Cwd: f.dir})
			if err == nil || result.Status != "refused" || result.Cause != "peer key "+path+" is invalid" {
				t.Fatalf("result = %+v err = %v", result, err)
			}
			if n := len(assignmentPrompts(t, f)); n != 0 {
				t.Fatalf("%d agent prompt calls after an invalid key", n)
			}
		})
	}
}
