package dispatch

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestUnsandboxedApprovalPromptNotes(t *testing.T) {
	withPlatform(t, "win32")
	role := writeRole(t)
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	for _, mode := range []string{"full", "unsandboxed"} {
		for name, prompt := range map[string]string{
			"brief":     ComposePrompt(role, "implementer", "worker", "Brief", "/report.md", ctx, composeEnv(t), "codex", "", mode, false),
			"amendment": ComposeAmendment("Amendment", "/report.md", ctx, composeEnv(t), "codex", "", mode, false),
		} {
			for _, note := range []string{sandboxGitNote, sandboxNetNote, sandboxWinRuntimeNote} {
				if strings.Contains(prompt, note) != (mode == "full") {
					t.Errorf("%s/%s note presence incorrect: %q", mode, name, note)
				}
			}
		}
	}
}
