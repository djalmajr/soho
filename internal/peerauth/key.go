// Package peerauth signs and checks the peer messages `soho send` types into
// another agent's pane. The `[soho:peer]` header is plain text anyone who
// reaches the target's Herdr can type, so the sender adds an HMAC-SHA256 over
// the whole message with a key shared by the pair of machines, and the
// receiver's UserPromptSubmit hook checks the signature, the allowlist and
// the replay before the agent sees the prompt.
//
// A signature proves the machine, not an approval: a request to merge, deploy
// or hand over a credential still needs a verifiable approval.
package peerauth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// LocalName is the reserved key name of the machine itself.
	LocalName = "local"
	// KeyDirName is the directory of the key files, inside the user config dir.
	KeyDirName = "peer-keys"
	keyExt     = ".key"
	// keyBytes is the decoded size of a key.
	keyBytes = 32
	// maxKeyFile bounds what a key read takes from disk: 64 hex characters and
	// a line break are far below it.
	maxKeyFile = 1024
)

// namePattern is the sessionref machine pattern with a ceiling of 64.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether name can name a key. Every name that came from a
// header passes through it before a path is built.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// KeyStatus is the outcome of reading a key.
type KeyStatus int

const (
	// KeyOK: the file holds a valid key.
	KeyOK KeyStatus = iota
	// KeyMissing: the file does not exist.
	KeyMissing
	// KeyInvalid: the name is not valid, or the file is unreadable or does not
	// hold exactly 32 hex-encoded bytes.
	KeyInvalid
)

// KeyDir is the directory of the key files under the user config dir.
func KeyDir(dir string) string { return filepath.Join(dir, KeyDirName) }

// KeyPath is the key file of name. ok is false when name is not valid: no path
// is built from it.
func KeyPath(dir, name string) (string, bool) {
	if !ValidName(name) {
		return "", false
	}
	return filepath.Join(KeyDir(dir), name+keyExt), true
}

// ReadKey reads the key of name. An invalid name does not touch the disk.
func ReadKey(dir, name string) ([]byte, KeyStatus) {
	path, ok := KeyPath(dir, name)
	if !ok {
		return nil, KeyInvalid
	}
	return readKeyFile(path)
}

func readKeyFile(path string) ([]byte, KeyStatus) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, KeyMissing
		}
		return nil, KeyInvalid
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil || len(raw) > maxKeyFile {
		return nil, KeyInvalid
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != keyBytes {
		return nil, KeyInvalid
	}
	return key, KeyOK
}

// ErrKeyExists is NewKey's refusal to overwrite a key.
var ErrKeyExists = errors.New("peer key already exists")

// NewKey creates the key file of name: the directory with 0700, the file with
// O_EXCL and 0600, 32 bytes from crypto/rand as 64 lowercase hex characters
// and a line break. It returns the absolute path and never the value.
func NewKey(dir, name string) (string, error) {
	path, ok := KeyPath(dir, name)
	if !ok {
		return "", fmt.Errorf("invalid peer key name")
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return path, err
	}
	raw := make([]byte, keyBytes)
	if _, err := rand.Read(raw); err != nil {
		return path, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return path, ErrKeyExists
		}
		return path, err
	}
	_, err = f.WriteString(hex.EncodeToString(raw) + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return path, err
	}
	return path, nil
}
