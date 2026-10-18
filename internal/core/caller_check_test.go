package core

import (
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestConfigValueOkCallerCheck(t *testing.T) {
	for _, value := range []string{"refuse", "warn", "off"} {
		if !ConfigValueOk("caller_check", value, nil, "") {
			t.Fatalf("caller_check=%s must be accepted", value)
		}
	}
	for _, value := range []string{"on", "abc", "", "REFUSE", "refuse "} {
		if ConfigValueOk("caller_check", value, nil, "") {
			t.Fatalf("caller_check=%q must be rejected", value)
		}
	}
}

func TestCallerCheckMode(t *testing.T) {
	env := platform.Env{}
	// Absent key: the default refuse.
	if got := CallerCheckMode(&Config{Entries: map[string]ConfigEntry{}}, env); got != "refuse" {
		t.Fatalf("default=%q", got)
	}
	// A file value.
	fileWarn := &Config{Entries: map[string]ConfigEntry{"caller_check": {Value: "warn", Source: "project"}}}
	if got := CallerCheckMode(fileWarn, env); got != "warn" {
		t.Fatalf("file warn=%q", got)
	}
	fileOff := &Config{Entries: map[string]ConfigEntry{"caller_check": {Value: "off", Source: "user"}}}
	if got := CallerCheckMode(fileOff, env); got != "off" {
		t.Fatalf("file off=%q", got)
	}
	// An invalid file value counts as refuse.
	fileBad := &Config{Entries: map[string]ConfigEntry{"caller_check": {Value: "on", Source: "project"}}}
	if got := CallerCheckMode(fileBad, env); got != "refuse" {
		t.Fatalf("invalid file value=%q", got)
	}
	// The SOHO_CALLER_CHECK environment layer wins over the file.
	envWarn := platform.Env{"SOHO_CALLER_CHECK": "warn"}
	if got := CallerCheckMode(&Config{Entries: map[string]ConfigEntry{}}, envWarn); got != "warn" {
		t.Fatalf("env warn=%q", got)
	}
	envOverride := platform.Env{"SOHO_CALLER_CHECK": "off"}
	if got := CallerCheckMode(fileWarn, envOverride); got != "off" {
		t.Fatalf("env off over file warn=%q", got)
	}
	// An invalid environment value counts as refuse.
	envBad := platform.Env{"SOHO_CALLER_CHECK": "loud"}
	if got := CallerCheckMode(fileWarn, envBad); got != "refuse" {
		t.Fatalf("invalid env value=%q", got)
	}
}
