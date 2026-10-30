package core

import (
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// TestTransportConfigValueOk is the `config set` acceptance for the
// transport key: herdr and hc are accepted, everything else is refused
// (config set dies 2 on a value ConfigValueOk rejects).
func TestTransportConfigValueOk(t *testing.T) {
	env := platform.Env{}
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"hc", true},
		{"herdr", true},
		{"other", false},
		{"HC", false},
		{"", false},
	} {
		if got := ConfigValueOk("transport", tc.value, env, t.TempDir()); got != tc.ok {
			t.Errorf("ConfigValueOk(transport, %q) = %v, want %v", tc.value, got, tc.ok)
		}
	}
	if !ConfigKeyOk("transport") {
		t.Error("ConfigKeyOk(transport) = false, want true (config set knows the key)")
	}
	if TransportValueOk("hc") != true || TransportValueOk("herdr") != true || TransportValueOk("other") != false {
		t.Error("TransportValueOk does not accept exactly herdr and hc")
	}
}

// TestTransportMode is the resolver contract: herdr when the key is absent,
// herdr when it holds an invalid value, hc when it is hc; the
// SOHO_TRANSPORT environment layer wins over the file layers.
func TestTransportMode(t *testing.T) {
	env := platform.Env{}
	if got := TransportMode(&Config{Entries: map[string]ConfigEntry{}}, env); got != "herdr" {
		t.Fatalf("absent key: got %q, want herdr", got)
	}
	if got := TransportMode(nil, env); got != "herdr" {
		t.Fatalf("nil config: got %q, want herdr", got)
	}
	invalid := &Config{Entries: map[string]ConfigEntry{"transport": {Value: "other"}}}
	if got := TransportMode(invalid, env); got != "herdr" {
		t.Fatalf("invalid value: got %q, want herdr", got)
	}
	herdr := &Config{Entries: map[string]ConfigEntry{"transport": {Value: "herdr"}}}
	if got := TransportMode(herdr, env); got != "herdr" {
		t.Fatalf("herdr value: got %q, want herdr", got)
	}
	hc := &Config{Entries: map[string]ConfigEntry{"transport": {Value: "hc"}}}
	if got := TransportMode(hc, env); got != "hc" {
		t.Fatalf("hc value: got %q, want hc", got)
	}
	// The environment layer wins over the file layers.
	envHC := platform.Env{"SOHO_TRANSPORT": "hc"}
	if got := TransportMode(herdr, envHC); got != "hc" {
		t.Fatalf("SOHO_TRANSPORT=hc over herdr file: got %q, want hc", got)
	}
	envInvalid := platform.Env{"SOHO_TRANSPORT": "bogus"}
	if got := TransportMode(nil, envInvalid); got != "herdr" {
		t.Fatalf("SOHO_TRANSPORT=bogus: got %q, want herdr", got)
	}
	// The environment value beats an invalid file value, too: Cfg reads the
	// env layer first, and the resolver accepts only the exact hc.
	envBoth := platform.Env{"SOHO_TRANSPORT": "hc"}
	if got := TransportMode(invalid, envBoth); got != "hc" {
		t.Fatalf("SOHO_TRANSPORT=hc over an invalid file value: got %q, want hc", got)
	}
}
