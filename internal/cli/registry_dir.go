package cli

import (
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/platform"
)

// registryDestination is used only by resource registration commands. Invalid
// hints fall back to the caller's usual state resolution without a new warning.
func registryDestination(env platform.Env) string {
	dir := env.Get("SOHO_REGISTRY_DIR")
	if !filepath.IsAbs(dir) {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	return dir
}
