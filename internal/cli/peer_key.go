package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/platform"
)

// cmdPeerKey implements `soho peer-key new <name>`: it creates the key file
// peer-keys/<name>.key under the user config dir and prints its absolute path
// and nothing else, never the key. An existing file is never overwritten.
func cmdPeerKey(args []string, env platform.Env) int {
	if len(args) != 2 || args[0] != "new" {
		platform.Die("usage: peer-key new <name>", 2)
	}
	name := args[1]
	if !peerauth.ValidName(name) {
		platform.Die(fmt.Sprintf("peer-key: invalid name '%s' (letters, digits, '.', '_' and '-', at most 64, starting with a letter or digit)", name), 2)
	}
	dir := filepath.Dir(core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{}))
	path, err := peerauth.NewKey(dir, name)
	if errors.Is(err, peerauth.ErrKeyExists) {
		platform.Die(fmt.Sprintf("peer-key: %s already exists; delete it to rotate the key", path), 1)
	}
	if err != nil {
		platform.Die(fmt.Sprintf("peer-key: cannot create %s", path), 1)
	}
	fmt.Fprintln(platform.Stdout, path)
	return 0
}
