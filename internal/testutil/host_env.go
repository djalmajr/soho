package testutil

import "github.com/djalmajr/soho/internal/testutil/testenv"

// HostPath preserves discovery of native tools when a fixture prepends its fakes.
func HostPath() string { return testenv.HostPath() }

// HostSystemRoot preserves Windows loader/tool discovery in explicit child environments.
func HostSystemRoot() string { return testenv.HostSystemRoot() }

// HostRaceOptions preserves the invoking race detector configuration in child binaries.
func HostRaceOptions() string { return testenv.HostRaceOptions() }

// HostGoCache lets native Go fixture builds reuse the configured build cache.
func HostGoCache() string { return testenv.HostGoCache() }

// HostGoRoot locates the native toolchain for relocated Go fixture binaries.
func HostGoRoot() string { return testenv.HostGoRoot() }

// HostRustupHome locates the installed native Cargo toolchain after HOME isolation.
func HostRustupHome() string { return testenv.HostRustupHome() }

// HostTempDir preserves a caller's scratch root when launching a native helper.
func HostTempDir() string { return testenv.HostTempDir() }
