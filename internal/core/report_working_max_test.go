package core

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestReportWorkingMaxKeyPlacement(t *testing.T) {
	found := -1
	for i, key := range ConfigScalarKeys {
		if key == "report_working_max" {
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("report_working_max is not in ConfigScalarKeys: %#v", ConfigScalarKeys)
	}
	if found == 0 || ConfigScalarKeys[found-1] != "settled_grace" {
		t.Fatalf("report_working_max at index %d; want right after settled_grace: %#v", found, ConfigScalarKeys)
	}
	if !ConfigKeyOk("report_working_max") {
		t.Fatal("ConfigKeyOk(report_working_max) = false; want true")
	}
}

func TestReportWorkingMaxConfigValueOk(t *testing.T) {
	for raw, want := range map[string]bool{
		"0": true, "600": true, "7800": true,
		"-1": false, "1.5": false, "10m": false, "abc": false, "": false,
		"+1": false, "600 ": false, " 600": false, "0x10": false,
		"9223372036854775807": true, "9223372036854775808": false,
		strings.Repeat("9", 1000): false,
	} {
		t.Run(raw, func(t *testing.T) {
			if got := ConfigValueOk("report_working_max", raw, platform.Env{}, ""); got != want {
				t.Fatalf("ConfigValueOk(report_working_max, %q) = %t; want %t", raw, got, want)
			}
		})
	}
}
