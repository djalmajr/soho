package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
)

var authorDispatchName = regexp.MustCompile(`^(.+)-(\d{8}T\d{6})(?:-(\d+))?\.dispatch\.json$`)
var authorRole = regexp.MustCompile("You are running as the `([^`]+)` role")
var authorFields = []string{"author_agent", "author_kind", "author_model", "author_family", "author_type"}

type dispatchAuthorContext struct {
	stateDir string
	env      platform.Env
	cwd      string
}

func authorObject(path string) *jsonjs.Object {
	b, err := core.ReadScratchReport(path)
	if err != nil {
		return nil
	}
	v, err := jsonjs.Parse(b)
	if err != nil {
		return nil
	}
	o, _ := v.(*jsonjs.Object)
	return o
}

func authorString(o *jsonjs.Object, key string) string {
	if o == nil {
		return ""
	}
	v, _ := o.Get(key)
	s, _ := v.(string)
	return s
}

// Freeze attribution before the accepted write, then preserve it through
// arrival updates. State history authorizes scratch-routed candidate paths.
func withDispatchAuthor(path, role string, write func() error, contexts ...dispatchAuthorContext) error {
	var fields map[string]string
	if core.IsReviewRole(role) {
		fields = lookupDispatchAuthor(path, contexts...)
	}
	if err := write(); err != nil {
		return err
	}
	if len(fields) == 0 {
		return nil
	}
	o := authorObject(path)
	if o == nil {
		return nil
	}
	for k, v := range fields {
		o.Set(k, v)
	}
	return core.WriteScratchFile(path, jsonjs.Stringify(o)+"\n")
}

// Mutual containment is identity: PathWithin resolves existing symlinks,
// cleans paths and applies SamePath's Windows case/separator semantics.
func sameAuthorWorktree(left, right string) bool {
	return dispatch.PathWithin(left, right, platform.Current()) && dispatch.PathWithin(right, left, platform.Current())
}

func authorBriefPath(sidecar, stateDir string) string {
	stem := strings.TrimSuffix(sidecar, ".dispatch.json")
	extensions := []string{".brief.md"}
	// Only legacy state briefs use .md; beside a scratch sidecar it is
	// the report body, which must never supply author metadata.
	if stateDir != "" && sameAuthorWorktree(filepath.Dir(sidecar), filepath.Join(stateDir, "briefs")) {
		extensions = append(extensions, ".md")
	}
	for _, ext := range extensions {
		if info, err := os.Stat(stem + ext); err == nil && info.Mode().IsRegular() {
			return stem + ext
		}
	}
	return ""
}

func authorCandidatePaths(sd string) map[string]string {
	candidates := map[string]string{}
	// Keep pre-pointer dispatch history readable without scanning any worker
	// worktree or an unreferenced scratch directory.
	dir := filepath.Join(sd, "briefs")
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if authorDispatchName.MatchString(entry.Name()) {
			sidecar := filepath.Join(dir, entry.Name())
			candidates[sidecar] = authorBriefPath(sidecar, sd)
		}
	}
	entries, _ = os.ReadDir(sd)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "task-report-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		agent := strings.TrimSuffix(strings.TrimPrefix(name, "task-report-"), ".json")
		pointer := taskreport.ReadTaskReportPointer(sd, agent)
		if pointer == nil {
			continue
		}
		members := []string{authorString(pointer, "current")}
		if value, ok := pointer.Get("history"); ok {
			if history, ok := value.([]any); ok {
				for _, value := range history {
					if member, ok := value.(string); ok {
						members = append(members, member)
					}
				}
			}
		}
		for _, member := range members {
			if member == "" {
				continue
			}
			memberDir := filepath.Dir(member)
			if sameAuthorWorktree(memberDir, filepath.Join(sd, "reports")) {
				memberDir = dir
			}
			stem := strings.TrimSuffix(filepath.Base(member), ".md")
			sidecar := filepath.Join(memberDir, stem+".dispatch.json")
			candidates[sidecar] = authorBriefPath(sidecar, sd)
		}
	}
	return candidates
}

func lookupDispatchAuthor(path string, contexts ...dispatchAuthorContext) map[string]string {
	if old := authorObject(path); authorString(old, "author_model") != "" {
		fields := map[string]string{}
		for _, k := range authorFields {
			if v := authorString(old, k); v != "" {
				fields[k] = v
			}
		}
		return fields
	}
	ctx := dispatchAuthorContext{}
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	if ctx.stateDir == "" && filepath.Base(filepath.Dir(path)) == "briefs" {
		ctx.stateDir = filepath.Dir(filepath.Dir(path))
	}
	if ctx.stateDir == "" {
		return nil
	}
	if ctx.env == nil {
		ctx.env = platform.Env{}
	}
	if ctx.cwd == "" {
		ctx.cwd, _ = os.Getwd()
	}
	currentPath := authorBriefPath(path, ctx.stateDir)
	body, err := core.ReadScratchReport(currentPath)
	if err != nil {
		return nil
	}
	tree := dispatch.DeclaredWorktree(string(body))
	if tree == "" {
		return nil
	}
	currentBrief, err := os.Stat(currentPath)
	if err != nil {
		return nil
	}
	current := authorDispatchName.FindStringSubmatch(filepath.Base(path))
	candidates := authorCandidatePaths(ctx.stateDir)
	paths := make([]string, 0, len(candidates))
	for sidecar := range candidates {
		paths = append(paths, sidecar)
	}
	sort.Strings(paths)
	latestStamp, latestSuffix := "", 0
	var latestModified time.Time
	var result map[string]string
	for _, sidecar := range paths {
		m := authorDispatchName.FindStringSubmatch(filepath.Base(sidecar))
		if m == nil || sidecar == path {
			continue
		}
		if len(current) > 0 && m[2] > current[2] {
			continue
		}
		suffix := 1
		if m[3] != "" {
			suffix, err = strconv.Atoi(m[3])
			if err != nil {
				continue
			}
		}
		candidate := authorObject(sidecar)
		if authorString(candidate, "submission") != "accepted" || authorString(candidate, "model") == "" || authorString(candidate, "kind") == "" {
			continue
		}
		briefPath := candidates[sidecar]
		brief, err := core.ReadScratchReport(briefPath)
		if err != nil || !sameAuthorWorktree(dispatch.DeclaredWorktree(string(brief)), tree) {
			continue
		}
		info, err := os.Stat(briefPath)
		if err != nil || info.ModTime().After(currentBrief.ModTime()) {
			continue
		}
		if !latestModified.IsZero() && (info.ModTime().Before(latestModified) || (info.ModTime().Equal(latestModified) && (m[2] < latestStamp || (m[2] == latestStamp && suffix <= latestSuffix)))) {
			continue
		}
		candidateRole := authorString(candidate, "role")
		if candidateRole == "" {
			if match := authorRole.FindStringSubmatch(string(brief)); match != nil {
				candidateRole = match[1]
			}
		}
		if candidateRole == "" || !authorRoleIsEdit(candidateRole, ctx.env, ctx.cwd) {
			continue
		}
		result = map[string]string{"author_agent": m[1], "author_kind": authorString(candidate, "kind"), "author_model": authorString(candidate, "model"), "author_type": dispatch.BriefType(string(brief))}
		if family := authorString(candidate, "family"); family != "" {
			result["author_family"] = family
		}
		latestStamp, latestSuffix, latestModified = m[2], suffix, info.ModTime()
	}
	return result
}

// Attribution is optional: unavailable role/skill context must not prevent
// writing the sidecar. Built-in edit roles remain classifiable without it.
func authorRoleIsEdit(role string, env platform.Env, cwd string) (edit bool) {
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(*platform.ExitError); !ok {
				panic(value)
			}
			edit = false
		}
	}()
	return core.RoleIsEdit(role, env, cwd)
}
