package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/hcbackend"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
)

// refuseOrchestratorChild runs before command dispatch and does not initialize state.
func refuseOrchestratorChild(args []string, env platform.Env) {
	if len(args) == 0 || args[0] == "help" || args[0] == "version" || args[0] == "--version" || args[0] == "--help" || args[0] == "-h" || (len(args) > 1 && (args[1] == "--help" || args[1] == "-h")) {
		return
	}
	child := env.Get("HC_TERMINAL_ID")
	if child == "" || env.Get("SOHO_AGENT") != "" || env.Get("SOHO_DIR") != "" || env.Get("SOHO_STATE_DIR") != "" || !sessionmeta.ValidSessionID("hc-"+child) {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	cfg := core.ChildGuardConfig(env, cwd)
	if core.TransportMode(&cfg, env) != "hc" {
		return
	}
	if entry, ok := cfg.Entries["state_dir"]; ok && entry.Source != "defaults" && entry.Value != "" {
		return
	}
	root := core.StateRootPath(&cfg, env, cwd)
	if _, err := os.Lstat(filepath.Join(root, "hc-"+child)); !os.IsNotExist(err) {
		return
	}
	parent, err := hcbackend.Parent(env)
	if err != nil || parent == "" || parent == child || !sessionmeta.ValidSessionID("hc-"+parent) {
		return
	}
	record, found, err := (sessionmeta.Store{StateBase: root, SessionID: "hc-" + parent}).Read()
	if err != nil || !found || record.Terminal != parent {
		return
	}
	platform.Die(fmt.Sprintf("%s: this terminal (%s) is a child of the orchestrator %s; run soho from the orchestrator's terminal (builds and tests can stay in children)", args[0], child, parent), 2)
}
