package spawn

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

const (
	ompPolicyAsset    = "omp-managed-v1.yml"
	ompPolicyBytes    = "retry:\n  modelFallback: false\ntools:\n  approval:\n    task: deny\n"
	ompPolicyColumn   = 19 // After the optional E2E port, OMP approval and environment policy.
	ompApprovalColumn = 17
)

// managedOMPPolicy resolves the stable packaged asset. OMP watches this path,
// so future policy revisions must use a new filename and retain this file.
func managedOMPPolicy(env platform.Env, cwd string) (string, error) {
	skill := platform.SkillDir(env)
	if !filepath.IsAbs(skill) {
		skill = filepath.Join(cwd, skill)
	}
	path, err := filepath.EvalSymlinks(filepath.Join(skill, "templates", ompPolicyAsset))
	if err != nil {
		return "", fmt.Errorf("managed OMP policy %s is unavailable: %w", ompPolicyAsset, err)
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != ompPolicyAsset || strings.ContainsAny(path, "\t\r\n") {
		return "", fmt.Errorf("managed OMP policy %s requires its versioned filename at an absolute path without tabs or line breaks", ompPolicyAsset)
	}
	file, err := platform.OpenRegularFile(path)
	if err != nil {
		return "", fmt.Errorf("managed OMP policy %s is unreadable: %w", ompPolicyAsset, err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, int64(len(ompPolicyBytes)+1)))
	if err != nil {
		return "", fmt.Errorf("managed OMP policy %s is unreadable: %w", ompPolicyAsset, err)
	}
	if string(body) != ompPolicyBytes {
		return "", fmt.Errorf("managed OMP policy %s is invalid; reinstall the matching Soho skill", ompPolicyAsset)
	}
	return path, nil
}

func ompPolicyReceipt(path string) string { return "omp-policy:" + path }

// The recorded launch options are required even when historical approval and
// MCP labels match. Reuse also requires the same verified on-disk asset.
func ompReuseVerified(fields []string, approvals, policy string) bool {
	mode, err := kinds.OMPApprovalArgs(approvals)
	return err == nil && policy != "" && fieldAt(fields, 2) == "omp" &&
		fieldAt(fields, 9) == approvals && fieldAt(fields, 13) == "" &&
		fieldAt(fields, 15) == "inherit" && fieldAt(fields, ompApprovalColumn) == strings.Join(mode, " ") &&
		fieldAt(fields, ompPolicyColumn) == ompPolicyReceipt(policy)
}

func guardOMPReuseName(rt transport.Transport, sd string, o spawnOptions) {
	fields := strings.Split(core.RosterLine(sd, o.name), "\t")
	if fieldAt(fields, 2) == "omp" && !ompReuseVerified(fields, o.approvals, o.ompPolicyPath) {
		if state, _ := agentGetState(rt, o.name); state != "gone" {
			refuseOMPReuse()
		}
	}
}

func refuseOMPReuse() {
	core.DieFriction("spawn: worker has no matching verified managed OMP launch; reopen it or release it before reusing that name", 2, "", "spawn")
}
