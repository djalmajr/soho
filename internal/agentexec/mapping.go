package agentexec

// Executable returns the canonical executable name for a given logical kind.
func Executable(kind string) string {
	if kind == "cursor" {
		return "cursor-agent"
	}
	return kind
}
