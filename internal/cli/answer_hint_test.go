package cli

import (
	"strings"
	"testing"
)

func TestDispatchCursorStartupTrustHintExactAndGenericNoInput(t *testing.T) {
	for _, tc := range []struct{ name, screen, want string }{
		{"trust", cursorTrustScreen(t), `soho: warning: dispatch: 'worker' (cursor) startup is not ready: it is on the cursor workspace trust dialog (state idle); nothing was typed or sent — read the pane (herdr agent read worker --source visible); ask the user before answering; to trust it: herdr agent send-keys worker a (answers "Trust this workspace"); then herdr agent wait worker --timeout 60000` + "\n"},
		{"unknown", "Do you trust the contents of this directory?\nTrust this workspace\nQuit\n", "soho: warning: dispatch: 'worker' (cursor) startup is not ready: it is on the cursor workspace trust dialog (state idle); nothing was typed or sent — read the pane (herdr agent read worker --source visible) and answer it yourself, then dispatch again\n"},
		{"mcp", "MCP Server Approval Required\n[a] Approve all\n", "soho: warning: dispatch: 'worker' (cursor) startup is not ready: Cursor requires approval of MCP servers; the operator must answer in the pane (state idle); nothing was typed or sent — read the pane (herdr agent read worker --source visible) and answer it yourself, then dispatch again\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCursorDispatchFixture(t, "cursor", tc.screen, "0", false)
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
				t.Setenv(key, f.env[key])
			}
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			if code != 7 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q want line=%q", code, out, stderr, tc.want)
			}
			dispatchAssertNoInput(t, dispatchCalls(t, f))
		})
	}
}
