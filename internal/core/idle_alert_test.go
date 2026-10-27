package core

import (
	"github.com/djalmajr/soho/internal/platform"
	"testing"
)

func TestIdleAlertConfig(t *testing.T) {
	if !ConfigKeyOk("idle_alert_minutes") {
		t.Fatal("missing scalar key")
	}
	for raw, want := range map[string]bool{"0": true, "20": true, "9223372036854775807": true, "-1": false, "1.5": false, "20m": false, "": false, "+1": false, " 20": false, "9223372036854775808": false} {
		if got := ConfigValueOk("idle_alert_minutes", raw, platform.Env{}, ""); got != want {
			t.Errorf("%q: got %t want %t", raw, got, want)
		}
	}
}
