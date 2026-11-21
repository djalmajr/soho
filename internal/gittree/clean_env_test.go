package gittree

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// CleanGitEnv is shared with review-context: the three repository selectors
// are dropped case-insensitively, optional locks are off, and every other
// variable survives unchanged. This is the extraction from gitOutput, not a
// second filter.
func TestCleanGitEnv(t *testing.T) {
	in := platform.Env{
		"GIT_DIR":            "/hostile/repo.git",
		"git_work_tree":      "/hostile/tree",
		"Git_Index_File":     "/hostile/index",
		"GIT_OPTIONAL_LOCKS": "1",
		"PATH":               "/fixture/bin",
		"SOHO_TRANSPORT":     "hc",
	}
	clean := CleanGitEnv(in)
	for _, key := range []string{"GIT_DIR", "git_work_tree", "Git_Index_File"} {
		for k := range clean {
			if strings.EqualFold(k, key) {
				t.Errorf("selector %s survived cleaning as %s", key, k)
			}
		}
	}
	if clean["GIT_OPTIONAL_LOCKS"] != "0" {
		t.Errorf("optional locks = %q, want 0", clean["GIT_OPTIONAL_LOCKS"])
	}
	if clean["PATH"] != "/fixture/bin" || clean["SOHO_TRANSPORT"] != "hc" {
		t.Errorf("unrelated variables changed: %#v", clean)
	}
	if in["GIT_OPTIONAL_LOCKS"] != "1" || in["GIT_DIR"] != "/hostile/repo.git" {
		t.Errorf("the input environment was mutated: %#v", in)
	}
	if _, ok := clean["GIT_COMMON_DIR"]; ok {
		t.Error("CleanGitEnv must keep the gittree key set, not add selectors")
	}
}
