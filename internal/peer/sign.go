package peer

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/job"
	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionref"
)

// notSignedNote is what a send says when it has a key but no valid label to
// put in the `from` item.
const notSignedNote = "send: not signed: machine_label is empty or invalid"

// noHostnameNote is what a send says to a remote target when this machine has
// no hostname: the header then has no ` on <host>` part and the receiver would
// read it as a local one, so the message goes out unsigned.
const noHostnameNote = "send: not signed: this machine has no hostname"

// signingPlan is how one send signs its message. The zero value signs
// nothing: the message goes out with the bytes it had before signatures.
type signingPlan struct {
	key []byte
	// from is the sender machine's label for the `from` item; "" on a local
	// message.
	from string
}

func (p signingPlan) signs() bool { return p.key != nil }

// userConfigDir is the directory of the user config file: the key files, the
// replay cache and the receiver's log live under it.
func userConfigDir(env platform.Env) string {
	return filepath.Dir(core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{}))
}

// planSigning decides how a message to the machine is signed. A remote target
// `<m>/<pane>` signs with peer-keys/<m>.key and names this machine's
// machine_label (from the user config file only) in `from`; a local one signs
// with peer-keys/local.key and has no `from`. Without the key the message goes
// out as it always did, and so does one to a remote target when this machine
// has no hostname. refusal is non-empty when the key exists but is not valid:
// the send must stop before it types anything, never send unsigned in its
// place. note is a line for stderr.
func planSigning(machine string, env platform.Env) (plan signingPlan, note, refusal string) {
	dir := userConfigDir(env)
	remote := machine != "" && machine != sessionref.LocalMachine
	name := peerauth.LocalName
	if remote {
		name = machine
	}
	path, ok := peerauth.KeyPath(dir, name)
	if !ok {
		return signingPlan{}, "", ""
	}
	key, status := peerauth.ReadKey(dir, name)
	switch status {
	case peerauth.KeyMissing:
		return signingPlan{}, "", ""
	case peerauth.KeyInvalid:
		return signingPlan{}, "", fmt.Sprintf("peer key %s is invalid", path)
	}
	if !remote {
		return signingPlan{key: key}, "", ""
	}
	label := ""
	if values, err := job.MachineValues(platform.Current(), env, core.UserSelection{}); err == nil {
		label = values["machine_label"]
	}
	if !peerauth.ValidName(label) {
		return signingPlan{}, notSignedNote, ""
	}
	if HeaderField(senderHostname()) == "" {
		return signingPlan{}, noHostnameNote, ""
	}
	return signingPlan{key: key, from: label}, "", ""
}

// signMessage signs an assembled message (the opening line to the closing
// line) under the plan. A plan that signs nothing returns the message as it is.
func signMessage(plan signingPlan, message string) (string, error) {
	if !plan.signs() {
		return message, nil
	}
	return peerauth.Sign(plan.key, message)
}

// SignLocalMessage signs a message for a local target with the plan of the
// local `send`: peer-keys/local.key when it exists, no `from`. Without the key
// the message comes back as it is. A key that exists but is not valid is an
// error and nothing may be sent in its place.
func SignLocalMessage(env platform.Env, message string) (string, error) {
	plan, _, refusal := planSigning(sessionref.LocalMachine, env)
	if refusal != "" {
		return "", errors.New(refusal)
	}
	return signMessage(plan, message)
}
