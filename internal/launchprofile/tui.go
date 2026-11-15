package launchprofile

import "errors"

// ErrInvalidTUIProfile never includes the caller's configuration value.
var ErrInvalidTUIProfile = errors.New("launch profile: OpenCode TUI profile is invalid")

// ResolveTUIProfile resolves an explicit, inert selector without reading or
// changing configuration, process environment or runtime state.
func ResolveTUIProfile(raw string) (string, error) {
	switch raw {
	case "":
		return "legacy", nil
	case "legacy", "v2":
		return raw, nil
	default:
		return "", ErrInvalidTUIProfile
	}
}
