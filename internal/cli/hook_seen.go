package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const hookSeenWindow = 24 * time.Hour

// hookSeenWarnings persists only after a successful read and write; otherwise
// callers see all warnings. The project key is independent of the session.
func hookSeenWarnings(env platform.Env, cwd string, warnings []string) ([]string, int) {
	if env.Get("SOHO_NOWRITE") == "1" || len(warnings) == 0 {
		return warnings, 0
	}
	project := platform.ProjectRoot(env, cwd)
	key := fmt.Sprintf("doctor-%x", sha256.Sum256([]byte(project)))
	dir, err := core.PrivateScratchReports(env, key, core.ScratchCreate)
	if err != nil {
		return warnings, 0
	}
	path := filepath.Join(dir, "doctor-seen.tsv")
	data, err := core.ReadScratchReport(path)
	if err != nil && !os.IsNotExist(err) {
		return warnings, 0
	}
	now := platform.Now().UTC()
	seen := make(map[string]time.Time)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, fields[0])
		if err == nil && len(fields[1]) == 64 && now.Sub(when) <= 7*24*time.Hour {
			seen[fields[1]] = when
		}
	}
	var shown []string
	hidden := 0
	for _, warning := range warnings {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(warning)))
		when, ok := seen[hash]
		if ok && now.Sub(when) >= 0 && now.Sub(when) < hookSeenWindow {
			hidden++
			continue
		}
		shown = append(shown, warning)
		seen[hash] = now
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&output, "%s\t%s\n", seen[key].UTC().Format(time.RFC3339Nano), key)
	}
	if err := core.WriteScratchFile(path, output.String()); err != nil {
		return warnings, 0
	}
	return shown, hidden
}
