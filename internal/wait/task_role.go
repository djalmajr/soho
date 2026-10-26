package wait

import (
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/taskreport"
)

// RecordedTaskRole returns the pointer role, then the current member's sidecar
// role. An empty result preserves legacy dispatches without recorded roles.
func RecordedTaskRole(sd, agent string) string {
	current := core.LastReport(sd, agent)
	if pointer := taskreport.ReadTaskReportPointer(sd, agent); pointer != nil {
		if role, _ := sidecarString(pointer, "role"); role != "" {
			return role
		}
		if report, ok := sidecarString(pointer, "current"); ok {
			current = report
		}
	}
	if current == "" {
		return ""
	}
	base := strings.TrimSuffix(filepath.Base(current), ".md") + ".dispatch.json"
	for _, path := range []string{filepath.Join(sd, "briefs", base), filepath.Join(filepath.Dir(current), base)} {
		raw, err := core.ReadScratchReport(path)
		if err != nil {
			continue
		}
		value, err := jsonjs.Parse(raw)
		obj, _ := value.(*jsonjs.Object)
		if err != nil || obj == nil {
			return ""
		}
		role, _ := sidecarString(obj, "role")
		return role
	}
	return ""
}

// TaskRole falls back to the roster only for tasks without recorded roles.
func TaskRole(sd, agent string) string {
	if role := RecordedTaskRole(sd, agent); role != "" {
		return role
	}
	return field(strings.Split(core.RosterLine(sd, agent), "\t"), 3)
}
