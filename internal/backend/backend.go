// Package backend selects the worker transport backend soho runs on
// (DJA-604 stage 2b): the herdr backend by default, or the herdr-core
// (hc) backend when the resolved transport mode is hc. The mode travels in
// the environment (SOHO_TRANSPORT), which cli.Run sets to the resolved
// mode after loading the command's configuration, so every downstream
// backend.New(env) sees the same mode; an empty or invalid value keeps the
// herdr default. This package is the only place outside the two backends
// that calls their constructors (internal/archtest enforces it).
package backend

import (
	"github.com/djalmajr/soho/internal/hcbackend"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

const hcMode = "hc"

// New returns the Services for the transport the environment selects:
// the hc backend when SOHO_TRANSPORT is hc, the herdr backend otherwise —
// including when the variable is empty or holds an invalid value.
func New(env platform.Env) transport.Services {
	if env.Get("SOHO_TRANSPORT") == hcMode {
		return hcbackend.New(env)
	}
	return herdr.New(env)
}
