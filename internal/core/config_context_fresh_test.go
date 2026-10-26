package core

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestConfigContextFresh(t *testing.T) {
	if !ConfigKeyOk("context_fresh_percent") {
		t.Fatal("unknown context_fresh_percent")
	}
	count := 0
	for _, key := range ConfigScalarKeys {
		if key == "context_fresh_percent" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("scalar key occurrences=%d", count)
	}
	for raw, want := range map[string]bool{
		"0": true, "70": true, "100": true, "101": true, "9223372036854775807": true,
		"": false, "-1": false, "+1": false, "1.5": false, "70%": false, " 70": false, "70 ": false, "abc": false, "0x10": false,
		"9223372036854775808": false, strings.Repeat("9", 1000): false,
	} {
		t.Run(raw, func(t *testing.T) {
			if got := ConfigValueOk("context_fresh_percent", raw, platform.Env{}, ""); got != want {
				t.Fatalf("value=%q got=%t want=%t", raw, got, want)
			}
		})
	}
}
