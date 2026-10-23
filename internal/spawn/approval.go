package spawn

import (
	"github.com/djalmajr/soho/internal/spawn/cursorapproval"
	"github.com/djalmajr/soho/internal/startup"
)

// CursorMCPApproval shares the active-dialog detector with dispatch and wait.
func CursorMCPApproval(screen string) bool { return cursorapproval.CursorMCPApproval(screen) }

// EvaluateCursorStartup shares the startup verdict with dispatch and wait.
func EvaluateCursorStartup(kind, state, screen string, screenOK bool) startup.Evaluation {
	return cursorapproval.Evaluate(kind, state, screen, screenOK)
}
