package core

import (
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestCompactTimeoutKeyPlacement(t *testing.T) {
	// The key sits right after dispatch_timeout in the scalar listing, so
	// the frozen config goldens adapt at one stable anchor.
	found := -1
	for i, key := range ConfigScalarKeys {
		if key == "compact_timeout" {
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("compact_timeout is not in ConfigScalarKeys: %#v", ConfigScalarKeys)
	}
	if found == 0 || ConfigScalarKeys[found-1] != "dispatch_timeout" {
		t.Fatalf("compact_timeout at index %d; want right after dispatch_timeout: %#v", found, ConfigScalarKeys)
	}
	if !ConfigKeyOk("compact_timeout") {
		t.Fatal("ConfigKeyOk(compact_timeout) = false; want true")
	}
}

func TestCompactTimeoutConfigValueOk(t *testing.T) {
	// The same grammar as --timeout (core.ParseTimeoutMS): milliseconds or a
	// number with an ms, s, m or h unit; config set refuses invalid or <= 0.
	for raw, ok := range map[string]bool{
		"300000": true, "600000": true, "90s": true, "10m": true, "2h": true, "500ms": true,
		"0": false, "-1": false, "abc": false, "": false, "1.5": false, "90 S": false, "1h30m": false,
	} {
		if got := ConfigValueOk("compact_timeout", raw, platform.Env{}, ""); got != ok {
			t.Fatalf("ConfigValueOk(compact_timeout, %q) = %t; want %t", raw, got, ok)
		}
	}
}

func TestCompactTimeoutMSRead(t *testing.T) {
	ctxWith := func(value string) *Config {
		return &Config{Entries: map[string]ConfigEntry{"compact_timeout": {Value: value, Source: "project"}}}
	}
	empty := &Config{Entries: map[string]ConfigEntry{}}
	envWith := func(value string) platform.Env { return platform.Env{"SOHO_COMPACT_TIMEOUT": value} }

	cases := []struct {
		name string
		ctx  *Config
		env  platform.Env
		want int64
	}{
		{"unset falls back to the default", empty, platform.Env{}, 300000},
		{"file 90s parses to 90000", ctxWith("90s"), platform.Env{}, 90000},
		{"file 600000 stays plain milliseconds", ctxWith("600000"), platform.Env{}, 600000},
		{"file 10m parses to 600000", ctxWith("10m"), platform.Env{}, 600000},
		{"an invalid file value falls back to the default", ctxWith("abc"), platform.Env{}, 300000},
		{"a zero file value falls back to the default", ctxWith("0"), platform.Env{}, 300000},
		{"a negative file value falls back to the default", ctxWith("-1"), platform.Env{}, 300000},
		{"the environment wins over the file", ctxWith("10m"), envWith("90s"), 90000},
		{"an invalid environment value falls back to the default", ctxWith("10m"), envWith("abc"), 300000},
		{"a zero environment value falls back to the default", ctxWith("10m"), envWith("0"), 300000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompactTimeoutMS(tc.ctx, tc.env); got != tc.want {
				t.Fatalf("CompactTimeoutMS = %d; want %d (%s)", got, tc.want, tc.name)
			}
		})
	}
}
