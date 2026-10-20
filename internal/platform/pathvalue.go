package platform

import (
	"runtime"
	"sort"
	"strings"
)

// PathValue reads PATH with Windows variable-name casing rules. An exact
// PATH key always wins, including an explicitly empty value.
func PathValue(env Env) string {
	return pathValueFor(env, runtime.GOOS)
}

func pathValueFor(env Env, goos string) string {
	if value, ok := env["PATH"]; ok {
		return value
	}
	if goos != "windows" {
		return ""
	}
	names := []string{}
	for name := range env {
		if strings.EqualFold(name, "PATH") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return env[names[0]]
}
