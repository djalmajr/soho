package spawn

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

const (
	ompPolicyAsset    = "omp-managed-v2.yml"
	ompPolicyBytes    = "retry:\n  modelFallback: false\ntask:\n  maxRecursionDepth: 0\n"
	ompPolicyColumn   = 19 // After the optional E2E port, OMP approval and environment policy.
	ompApprovalColumn = 17
	ompPolicyVersion  = "omp-policy:v2:"
	ompReuseReadLimit = time.Second
)

// managedOMPPolicy resolves the stable packaged asset. OMP watches this path,
// so future revisions must use a new filename and retain published versions.
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

func ompPolicyReceipt(path, approvals string, agent ompStartupAgent) string {
	if path == "" || !agent.matches(agent.Name, agent.Pane) || agent.Session.Kind != "path" ||
		agent.Session.Source != "herdr:omp" || agent.Session.Value == "" {
		return ""
	}
	// Quote each field before hashing so embedded whitespace and delimiters
	// cannot create another identity or escape into the roster TSV.
	key := fmt.Sprintf("%q", []string{path, approvals, agent.Name, agent.Kind, agent.Pane,
		agent.Terminal, agent.Session.Kind, agent.Session.Source, agent.Session.Value})
	return fmt.Sprintf("%s%x", ompPolicyVersion, sha256.Sum256([]byte(key)))
}

// Validate the opening approval receipt against the recorded mode. The
// caller's requested approval is a separate reuse compatibility check.
func ompPolicyRecorded(fields []string) bool {
	approvals := fieldAt(fields, 9)
	mode, err := kinds.OMPApprovalArgs(approvals)
	digest, versioned := strings.CutPrefix(fieldAt(fields, ompPolicyColumn), ompPolicyVersion)
	return err == nil && versioned && len(digest) == 2*sha256.Size && fieldAt(fields, 2) == "omp" &&
		fieldAt(fields, 13) == "" &&
		fieldAt(fields, 15) == "inherit" && fieldAt(fields, ompApprovalColumn) == strings.Join(mode, " ")
}

func observeOMPReuse(legacy transport.LegacyTransport, fields []string, policy string) (ompStartupAgent, bool) {
	pane := fieldAt(fields, 1)
	if legacy == nil || pane == "" || policy == "" || !ompPolicyRecorded(fields) {
		return ompStartupAgent{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), ompReuseReadLimit)
	defer cancel()
	agent, ok := readOMPAgent(ctx, legacy, pane, transport.Call{Timeout: ompReuseReadLimit})
	return agent, ok && ctx.Err() == nil && agent.matches(fieldAt(fields, 0), pane) &&
		ompPolicyReceipt(policy, fieldAt(fields, 9), agent) == fieldAt(fields, ompPolicyColumn)
}

func ompReuseReady(legacy transport.LegacyTransport, fields []string, policy string) bool {
	agent, verified := observeOMPReuse(legacy, fields, policy)
	return verified && (agent.State == "idle" || agent.State == "done")
}

func guardOMPReuseName(svc transport.Services, sd string, o spawnOptions) {
	fields := strings.Split(core.RosterLine(sd, o.name), "\t")
	if fieldAt(fields, 2) != "omp" {
		return
	}
	if _, verified := observeOMPReuse(svc.Legacy, fields, o.ompPolicyPath); verified {
		return
	}
	if state, _ := agentGetState(svc.Runtime, o.name); state != "gone" {
		refuseOMPReuse()
	}
}

func refuseOMPReuse() {
	core.DieFriction("spawn: worker has no matching verified managed OMP launch; reopen it or release it before reusing that name", 2, "", "spawn")
}
