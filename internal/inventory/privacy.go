package inventory

import (
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// Mode is the privacy mode of one project in `soho inventory --json`.
type Mode string

// The modes of the contract's "Privacy" section.
const (
	ModeOff    Mode = "off"
	ModeHidden Mode = "hidden"
	ModeShow   Mode = "show"
)

// modeRank orders the modes by how much of the project leaves the
// machine: off < hidden < show. A restrictive source may only move a
// mode toward off, never toward show.
func modeRank(m Mode) int {
	switch m {
	case ModeShow:
		return 2
	case ModeHidden:
		return 1
	default:
		return 0
	}
}

// modeOf validates one raw inventory value. Only the contract's three
// modes are valid; anything else, including an empty value, is invalid.
func modeOf(value string) (Mode, bool) {
	switch value {
	case string(ModeShow), string(ModeHidden), string(ModeOff):
		return Mode(value), true
	default:
		return ModeOff, false
	}
}

// ResolveMode resolves the privacy mode of the project the config was
// loaded for. Only a project layer's inventory entry opens show or
// hidden; the SOHO_INVENTORY map entry and the user and session layers
// may only lower the result, never raise it. Defaults never opt a
// project in and never lower it: the defaults layer is ignored. A
// missing config, a missing project layer or key, an invalid or empty
// project value, a project read error, or a read error of any
// restrictive layer fails closed to off, because a restriction that
// cannot be established may still be there.
//
// The resolution is pure: it reads only the layers core already loaded
// and the entries of the passed env map. It never reads merged Cfg or
// Entries, never touches the process environment, the file system, the
// terminal server or a state directory, and writes nothing. The caller
// loads the config from the canonical project root; the resolver does
// not second-guess which project it describes.
func ResolveMode(cfg *core.Config, env platform.Env) Mode {
	if cfg == nil {
		return ModeOff
	}
	// Only the project's own configuration opens a project. The project
	// layers are processed in the recorded order: a later valid value
	// replaces an earlier one, and a later read error or invalid value
	// fails closed. No project layer or key leaves the mode closed.
	project := ModeOff
	for _, layer := range cfg.Layers {
		if layer.Source != "project" {
			continue
		}
		if layer.ReadError != nil {
			return ModeOff
		}
		value, ok := layer.Entries["inventory"]
		if !ok {
			continue
		}
		mode, valid := modeOf(value.Value)
		if !valid {
			return ModeOff
		}
		project = mode
	}
	result := project
	// The SOHO_INVENTORY variable is one of the restrictive sources: a
	// present entry (the map says it is set) may only lower the mode.
	if value, ok := env["SOHO_INVENTORY"]; ok {
		mode, valid := modeOf(value)
		if !valid {
			return ModeOff
		}
		if modeRank(mode) < modeRank(result) {
			result = mode
		}
	}
	// The user and session layers are the other restrictive sources. A
	// read error of either fails closed: its possible restriction cannot
	// be established.
	for _, layer := range cfg.Layers {
		if layer.Source != "user" && layer.Source != "session" {
			continue
		}
		if layer.ReadError != nil {
			return ModeOff
		}
		value, ok := layer.Entries["inventory"]
		if !ok {
			continue
		}
		mode, valid := modeOf(value.Value)
		if !valid {
			return ModeOff
		}
		if modeRank(mode) < modeRank(result) {
			result = mode
		}
	}
	return result
}
