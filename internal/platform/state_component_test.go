package platform

import "testing"

func TestValidStateComponent(t *testing.T) {
	for _, value := range []string{"w6X", "hc-t8", "session_2", "phase.2", "sessão", "team session", "COM0", "COM10", "console", "nul-value"} {
		t.Run("accept_"+value, func(t *testing.T) {
			if !ValidStateComponent(value) {
				t.Fatalf("rejected safe component %q", value)
			}
		})
	}
	for _, value := range []string{
		"", ".", "..", "../session", "session/child", `..\session`, `session\child`, "/session", `C:\session`, "C:session", "session:stream",
		"session.", "session ", "session\x00", "session\n", "session\t", "session\x7f", "session\u0085", string([]byte{0xff}),
		"s<1", "s>1", `s"1`, "s|1", "s?1", "s*1", "CON", "con", "PRN", "AUX", "NUL", "nul.txt", "CON .txt", "CONIN$", "conout$.txt",
		"COM1", "com9", "LPT1", "lpt9.txt", "COM¹", "LPT²", "COM³.txt",
	} {
		t.Run("reject_"+value, func(t *testing.T) {
			if ValidStateComponent(value) {
				t.Fatalf("accepted unsafe state component %q", value)
			}
		})
	}
}
