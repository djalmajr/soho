package testenv

import "os"

// HostPath preserves discovery of native tools when a fixture prepends its fakes.
func HostPath() string { return os.Getenv("PATH") }

// HostSystemRoot preserves Windows loader/tool discovery in explicit child environments.
func HostSystemRoot() string { return os.Getenv("SystemRoot") }

// HostRaceOptions preserves the invoking race detector configuration in child binaries.
func HostRaceOptions() string { return os.Getenv("GORACE") }

// HostGoCache lets native Go fixture builds reuse the configured build cache.
func HostGoCache() string { return os.Getenv("GOCACHE") }

// HostGoRoot locates the native toolchain for relocated Go fixture binaries.
func HostGoRoot() string { return os.Getenv("GOROOT") }

// HostRustupHome locates the installed native Cargo toolchain after HOME isolation.
func HostRustupHome() string { return os.Getenv("RUSTUP_HOME") }

// HostTempDir preserves a caller's scratch root when launching a native helper.
func HostTempDir() string { return os.Getenv("TMPDIR") }
