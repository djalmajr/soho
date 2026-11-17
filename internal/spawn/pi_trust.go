package spawn

import "strings"

func isPiTrustDialog(screen string) bool {
	title, option := false, false
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "→"))
		if line == "Trust project folder?" {
			title = true
		}
		if line == "Trust" || line == "Trust (this session only)" || line == "Do not trust" || line == "Do not trust (this session only)" || (strings.HasPrefix(line, "Trust parent folder (") && strings.HasSuffix(line, ")")) {
			option = true
		}
	}
	return title && option
}
