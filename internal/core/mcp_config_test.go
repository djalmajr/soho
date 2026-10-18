package core

import (
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestMcpDottedKeysAreConfiguredLikeTheOtherEnumeratedKeys(t *testing.T) {
	t.Run("the role and lane mcp attributes are accepted keys", func(t *testing.T) {
		for _, key := range []string{"role.reviewer.mcp", "lane.build.mcp", "role.security-reviewer.mcp"} {
			if !ConfigKeyOk(key) {
				t.Fatalf("key %q refused", key)
			}
		}
		if ConfigKeyOk("role.reviewer.bogus") || ConfigKeyOk("lane.build.bogus") {
			t.Fatal("unknown attributes must stay refused")
		}
	})

	env := platform.Env{}
	t.Run("inherit and none are the only accepted values", func(t *testing.T) {
		for _, key := range []string{"role.reviewer.mcp", "lane.build.mcp"} {
			for _, value := range []string{"inherit", "none"} {
				if !ConfigValueOk(key, value, env, "/tmp") {
					t.Fatalf("%s=%s refused", key, value)
				}
			}
			for _, value := range []string{"off", "on", "true", "inherit ", "NONE"} {
				if ConfigValueOk(key, value, env, "/tmp") {
					t.Fatalf("%s=%s accepted, want the enumerated refusal", key, value)
				}
			}
		}
	})
}
