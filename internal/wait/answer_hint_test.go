package wait

import "testing"

func TestWaitCursorTrustHintExactAndGenericNoInput(t *testing.T) {
	for _, tc := range []struct{ name, screen, want string }{
		{"trust", waitTrustScreen(t), `soho: warning: agent 'worker' is on the cursor workspace trust dialog; no approval key is sent — read the pane; ask the user before answering; to trust it: herdr agent send-keys worker a (answers "Trust this workspace"); then herdr agent wait worker --timeout 60000` + "\n"},
		{"unknown", "Do you trust the contents of this directory?\nTrust this workspace\nQuit\n", "soho: warning: agent 'worker' is on the cursor workspace trust dialog; no approval key is sent — read the pane and answer it yourself (herdr agent send-keys worker <keys>)\n"},
		{"mcp", "MCP Server Approval Required\n[a] Approve all\n", "soho: warning: agent 'worker' requires approval of MCP servers; no input is sent — the operator must answer in the pane\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, stderr := queuedRetryProbe(t, tc.screen, nil)
			root := t.TempDir()
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
				f.env[key] = root
			}
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
				t.Setenv(key, f.env[key])
			}
			f.cursorRoster(t, "cursor")
			if got := f.probe(""); got != "blocked" || stderr.String() != tc.want {
				t.Fatalf("probe=%q stderr=%q want=%q", got, stderr.String(), tc.want)
			}
			assertNoSendKeys(t, f)
		})
	}
}
