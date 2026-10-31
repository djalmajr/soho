package cli

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/inventory"
	"github.com/djalmajr/soho/internal/platform"
)

func cmdInventory(args []string, env platform.Env) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = platform.Stdout.Write([]byte(commandHelp("inventory")))
		return 0
	}
	if len(args) != 1 || args[0] != "--json" {
		platform.Die("usage: soho inventory --json", 2)
	}
	scratch := env.Get("TMPDIR")
	if scratch == "" {
		scratch = os.TempDir()
	}
	doc, err := inventory.Collect(inventory.Request{
		Env:         env,
		Version:     strings.TrimSuffix(strings.TrimPrefix(versionText(version, vcsRevision()), "soho "), "\n"),
		User:        core.UserSelection{},
		GeneratedMS: platform.Now().UTC().UnixMilli(),
		ScratchBase: scratch,
	})
	if err != nil {
		platform.Die("inventory: unavailable", 4)
	}
	body, err := json.Marshal(doc)
	if err != nil {
		platform.Die("inventory: unavailable", 4)
	}
	if _, err := platform.Stdout.Write(append(body, '\n')); err != nil {
		platform.Die("inventory: unavailable", 4)
	}
	return 0
}
