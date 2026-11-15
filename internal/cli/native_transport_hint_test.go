package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeTransportRefusalHint(t *testing.T) {
	for _, tc := range []struct{ name, id, selector, config, want string }{
		{name: "native-unselected", id: "t2c", want: "soho: not running inside Herdr (HERDR_ENV != 1); HC_TERMINAL_ID is present; select the native transport with SOHO_TRANSPORT=hc or soho config set transport hc\n"},
		{name: "no-native-id", want: "soho: not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside\n"},
		{name: "explicit-legacy", id: "t2c", selector: "herdr", want: "soho: not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside\n"},
		{name: "configured-legacy", id: "t2c", config: "transport=herdr\n", want: "soho: not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, root := commandFixture(t)
			env["HC_TERMINAL_ID"] = tc.id
			env["SOHO_TRANSPORT"] = tc.selector
			if tc.config != "" {
				dir := filepath.Join(root, ".agents")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "soho.conf"), []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, stderr := runIn(t, []string{"init"}, env, root)
			if code != 2 || out != "" || stderr != tc.want {
				t.Fatalf("code=%d stdout=%q stderr=%q want=%q", code, out, stderr, tc.want)
			}
		})
	}
}
