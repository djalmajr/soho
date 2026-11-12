package kinds

import (
	"github.com/djalmajr/soho/internal/launchprofile"
	"github.com/djalmajr/soho/internal/transport"
)

// LaunchProfile is the shared launch resolution for one kind, model, effort
// and transport combination: the profile name a backend may admit, the model
// CLI args the caller passes, the extra native args the profile adds, and the
// explicit per-child environment overrides the backend merges into the
// child. The one struct definition now lives in the neutral launchprofile
// package; every non-native combination resolves to an empty Name, no
// ExtraArgs and no EnvOverrides, while ModelArgs carries exactly the
// existing KindModelArgs output, so zero-value callers and all current
// KindModelArgs consumers keep their exact behavior.
type LaunchProfile = launchprofile.Profile

// OpenCodePrimaryProfile is the launch profile of the native hc+opencode
// combination: the shared resolver's profile name for it, the only profile
// the shared native status policy admits for the kind, the label the hc
// backend's start request carries and the record field its readiness
// qualification requires.
const OpenCodePrimaryProfile = launchprofile.OpenCodePrimaryProfile

// The value-free refusal errors the resolver returns. They are aliases of
// the neutral launchprofile sentinels, so the existing private names and
// their identity semantics stay exactly the same: they never carry the
// caller's model or inline configuration text, so a bad reference or config
// cannot be echoed back through the error channel.
var (
	errInvalidModelRef      = launchprofile.ErrInvalidModelRef
	errInvalidInlineCfg     = launchprofile.ErrInvalidInlineCfg
	errInvalidAgentsBuild   = launchprofile.ErrInvalidAgentsBuild
	errInvalidConfigDir     = launchprofile.ErrInvalidConfigDir
	errInvalidNativeProfile = launchprofile.ErrInvalidNativeProfile
)

// ResolveLaunchProfile resolves the shared launch profile for one kind,
// model, effort, transport and optional inline configuration. For the native
// hc+opencode combination it delegates to the neutral launchprofile
// resolver with no explicit configuration directory and returns the inert
// primary profile: the --standalone extra arg, no generated model CLI arg,
// and exactly two per-child environment overrides
// (OPENCODE_DISABLE_AUTOUPDATE=1 and OPENCODE_CONFIG_CONTENT carrying the
// merged configuration that pins the exact requested provider/model at
// root model, default_agent and agents.build.model). Malformed UTF-8 in the
// model or the inline configuration is refused with the fixed value-free
// errors before any grammar or JSON decoding, because encoding/json would
// silently rewrite the invalid bytes as U+FFFD and change the explicit
// input; valid Unicode and a literal U+FFFD character are valid UTF-8 and
// are preserved. Every other combination resolves to the existing
// KindModelArgs output with the same warn behavior and no new profile,
// extra arg or env override, without inspecting the inline configuration;
// KindModelArgs itself is unchanged.
//
// The resolver is pure: it reads no files, process environment, config,
// auth or provider endpoints, and it mutates no caller input.
func ResolveLaunchProfile(kind, model, effort, transportName, inlineConfig string, warn WarnFunc) (LaunchProfile, error) {
	if kind == "opencode" && transportName == "hc" {
		return launchprofile.ResolveOpenCode(model, inlineConfig, "")
	}
	return LaunchProfile{ModelArgs: KindModelArgs(kind, model, effort, warn)}, nil
}

// ResolveLaunchProfileWithConfigDir resolves the shared launch profile
// exactly as ResolveLaunchProfile does, delegating the native hc+opencode
// combination to the neutral launchprofile resolver with the caller's
// explicit configuration directory: OPENCODE_CONFIG_DIR set to that
// directory when it is nonempty. A configuration directory that holds a NUL
// or invalid UTF-8 is refused with the fixed value-free error before a
// profile is returned, and a malformed model or inline configuration
// refuses before the directory is examined; an empty directory adds
// nothing, so the native profile keeps exactly the resolver's two overrides
// and no default is invented. Every other kind or transport combination
// ignores the configuration directory and the inline configuration and
// resolves exactly as the resolver, malformed directory included.
//
// Like the resolver, the function is pure: it reads no files, process
// environment, config, auth or provider endpoints, and it mutates no
// caller input.
func ResolveLaunchProfileWithConfigDir(kind, model, effort, transportName, inlineConfig, configDir string, warn WarnFunc) (LaunchProfile, error) {
	if kind == "opencode" && transportName == "hc" {
		return launchprofile.ResolveOpenCode(model, inlineConfig, configDir)
	}
	return LaunchProfile{ModelArgs: KindModelArgs(kind, model, effort, warn)}, nil
}

// NativeStatusSupported reports whether a launch profile is supported for
// native status under transport=hc: the five kinds whose executable
// reports the 7501 status the hc backend observes carry the empty
// (pass-through) profile, and opencode carries exactly the primary
// profile. Every other combination is unsupported.
func NativeStatusSupported(kind, profile string) bool {
	return launchprofile.NativeStatusSupported(kind, profile)
}

// ValidateNativeLaunchProfile refuses a launch profile the native hc
// backend would not have generated. An unsupported kind/profile
// combination refuses outright, and the five empty-profile kinds pass
// through unchanged. The opencode primary profile additionally admits
// only the exact native argv and the exact canonical resolver
// environment: exactly one --standalone and at most one --auto, in
// either order, and no other token; OPENCODE_DISABLE_AUTOUPDATE=1 and
// the generated OPENCODE_CONFIG_CONTENT, plus the optional explicit
// nonempty OPENCODE_CONFIG_DIR, and no other override. The generated
// configuration must decode, with the resolver's UseNumber helper, to
// the resolver's own pinned shape for an explicit valid provider/model:
// the model at root model, default_agent build, agents.build.model
// equal to the root model, hidden or disabled only bool false and mode
// only primary or all, and a canonical re-resolution of the decoded
// model, configuration and optional directory must return exactly the
// candidate environment map. Every refusal is the one fixed value-free
// error, so no argument, configuration or directory text reaches the
// error channel.
func ValidateNativeLaunchProfile(kind, profile string, args []string, overrides map[string]string) error {
	return launchprofile.ValidateNativeLaunchProfile(kind, profile, args, overrides)
}

// OpenCodePrimaryObserved reports whether a terminal observation is
// exactly the one the native hc+opencode primary profile makes: the
// exact primary profile on the record's launch profile label, the
// record's own reported flag true, the reporter application opencode,
// and the native lifecycle live.
//
// Reported and ReporterApp are the transport.Terminal fields the
// hc backend's Get populates from the terminal record; this policy
// expands the fields into the neutral launchprofile primitive predicate
// and keeps no second projection or classifier of its own.
func OpenCodePrimaryObserved(t transport.Terminal) bool {
	return launchprofile.OpenCodePrimaryObserved(t.LaunchProfile, t.Reported, t.ReporterApp, t.NativeState)
}

// OpenCodePrimaryReady reports whether the observed primary terminal is
// ready: its reported agent activity is idle or done.
func OpenCodePrimaryReady(t transport.Terminal) bool {
	return launchprofile.OpenCodePrimaryReady(t.LaunchProfile, t.Reported, t.ReporterApp, t.NativeState, t.State)
}
