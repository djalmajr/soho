// gitsync.go keeps the job worktree in sync with the bare remote and with at
// most one draft pull request: it reads the head, the remote head, and the
// commits against the base, pushes new commits without ever forcing, opens a
// draft pull request once (looking up an existing one first), commits
// leftovers as a wip checkpoint, and reports the clean and in-sync facts the
// report derives limpeza.removivel from. It never removes a worktree, a
// branch, or a pull request. Every subprocess runs with a bounded timeout,
// and, when the optional Context is set, under it: a cancel of that context
// ends only the subprocesses this sync opened and the step reports
// errCancelled, not the step error. The other errors are fixed strings that
// name the step — subprocess output, URLs, and environment values never
// appear in them. What a failed step knew (how the subprocess ended and the
// last lines of its stderr, credentials redacted) travels apart, in the
// ExitError's Detail, for the job's friction log only.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

const (
	defaultLocalSyncTimeout = 60 * time.Second  // local git commands
	defaultNetSyncTimeout   = 300 * time.Second // ls-remote and push
	defaultGhSyncTimeout    = 60 * time.Second  // gh pr list and create
)

// GitSync is one job's git and draft-pull-request state. Dir is the job
// worktree, Branch is job/<id>, Base the base branch name, and Repo is
// <org>/<repo>. Zero timeouts select the defaults: LocalTimeout 60 s,
// NetTimeout 300 s, GhTimeout 60 s.
type GitSync struct {
	Env          platform.Env
	Dir          string
	Branch       string
	Base         string
	Repo         string
	LocalTimeout time.Duration
	NetTimeout   time.Duration
	GhTimeout    time.Duration
	// Limit, when set, is consulted by every timeout below with the
	// original default or configured value and returns the timeout to use:
	// the supervisor's terminal procedure clamps each subprocess to the
	// remaining time of its step (never to zero, which would mean none).
	Limit func(time.Duration) time.Duration
	// Context, when non-nil, is the platform context every subprocess runs
	// under (the same pattern as runStepInCtx): a cancel of it ends only the
	// subprocesses GitSync itself opened — the supervisor's pending cancel
	// cutting the in-flight periodic sync — and a step whose subprocess
	// ended that way reports errCancelled, not the fixed step error, so the
	// cut is not mistaken for a failure.
	Context context.Context
}

// limited applies Limit to a timeout.
func (g *GitSync) limited(d time.Duration) time.Duration {
	if g.Limit != nil {
		return g.Limit(d)
	}
	return d
}

// Commit is one commit of the job branch ahead of the base, oldest first.
// Push is left empty here; the caller fills it in.
type Commit struct {
	SHA    string `json:"sha"`
	Titulo string `json:"titulo"`
	Push   string `json:"push"`
}

// PushResult reports one Push. Pushed means this call pushed the local head
// to the remote job branch; Rejected means the remote refused a
// fast-forward and the caller turns it into a blocked event; Head is the
// local head. An up-to-date remote leaves both flags false.
type PushResult struct {
	Pushed   bool
	Rejected bool
	Head     string
}

// gitSyncMsgPrefix starts the message of every git sync step error.
const gitSyncMsgPrefix = "job: git sync failed: "

// unexpectedOutput is the detail of a step whose subprocess succeeded but
// printed something the step cannot read.
const unexpectedOutput = "unexpected output"

// gitSyncErrorDetail is the fixed step error: code 19, a message that names
// the step and nothing else, plus the step's diagnostic in Detail, which
// stays out of the message.
func gitSyncErrorDetail(step, detail string) error {
	return &ExitError{Code: ExitFailed, Msg: gitSyncMsgPrefix + step, Detail: detail}
}

// errCancelled is the fixed error of a step whose subprocess ended because
// the GitSync context was cancelled: the supervisor's pending cancel cut
// the in-flight periodic sync, and the cut is not a failure (no friction,
// no refused-push block). A nil context (every caller without one, such as
// the terminal procedure) is never cancelled, so the terminal path keeps
// its own errors.
var errCancelled = errors.New("job: git sync cut by the cancel")

// cancelled reports whether this GitSync's context was cancelled: the
// step's subprocess is the one the cancel ended.
func (g *GitSync) cancelled() bool {
	return g.Context != nil && g.Context.Err() != nil
}

func (g *GitSync) localTimeout() time.Duration {
	if g.LocalTimeout <= 0 {
		return g.limited(defaultLocalSyncTimeout)
	}
	return g.limited(g.LocalTimeout)
}

func (g *GitSync) netTimeout() time.Duration {
	if g.NetTimeout <= 0 {
		return g.limited(defaultNetSyncTimeout)
	}
	return g.limited(g.NetTimeout)
}

func (g *GitSync) ghTimeout() time.Duration {
	if g.GhTimeout <= 0 {
		return g.limited(defaultGhSyncTimeout)
	}
	return g.limited(g.GhTimeout)
}

// gitEnv is the subprocess environment for git steps: GIT_TERMINAL_PROMPT=0
// so a push that would ask for credentials fails instead of hanging.
func (g *GitSync) gitEnv() platform.Env {
	env := g.Env.Clone()
	env["GIT_TERMINAL_PROMPT"] = "0"
	return env
}

// ghEnv is the subprocess environment for gh steps: GH_PROMPT_DISABLED=1.
func (g *GitSync) ghEnv() platform.Env {
	env := g.Env.Clone()
	env["GH_PROMPT_DISABLED"] = "1"
	return env
}

// Head is the worktree's HEAD sha.
func (g *GitSync) Head() (string, error) {
	out, detail, err := runStepDetailInCtx("", "git", []string{"-C", g.Dir, "rev-parse", "HEAD"}, g.localTimeout(), g.gitEnv(), g.Context)
	if err != nil {
		if g.cancelled() {
			return "", errCancelled
		}
		return "", gitSyncErrorDetail("head", detail)
	}
	return strings.TrimSpace(out), nil
}

// RemoteHead is the sha of the remote job branch; "" (no error) when the
// remote branch does not exist yet.
func (g *GitSync) RemoteHead() (string, error) {
	out, detail, err := runStepDetailInCtx("", "git", []string{"-C", g.Dir, "ls-remote", "origin", "refs/heads/" + g.Branch}, g.netTimeout(), g.gitEnv(), g.Context)
	if err != nil {
		if g.cancelled() {
			return "", errCancelled
		}
		return "", gitSyncErrorDetail("remote head", detail)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 {
		return "", gitSyncErrorDetail("remote head", unexpectedOutput)
	}
	return fields[0], nil
}

// Commits lists the job branch's commits ahead of the base, oldest first,
// with the Push field left empty for the caller. Zero commits is an empty
// non-nil slice.
func (g *GitSync) Commits() ([]Commit, error) {
	commits := []Commit{}
	out, detail, err := runStepDetailInCtx("", "git", []string{"-C", g.Dir, "log", "--reverse", "--format=%H%x09%s", "refs/remotes/origin/" + g.Base + "..HEAD"}, g.localTimeout(), g.gitEnv(), g.Context)
	if err != nil {
		if g.cancelled() {
			return nil, errCancelled
		}
		return nil, gitSyncErrorDetail("commits", detail)
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		sha, titulo, ok := strings.Cut(line, "\t")
		if !ok || sha == "" {
			return nil, gitSyncErrorDetail("commits", unexpectedOutput)
		}
		commits = append(commits, Commit{SHA: sha, Titulo: titulo})
	}
	return commits, nil
}

// Facts snapshots the worktree for the report: Clean when
// `git status --porcelain` prints nothing, Head from Head(), HeadRemoto from
// RemoteHead(). Resumo stays empty; the report builder derives removivel and
// sincronizado from these fields.
func (g *GitSync) Facts() (ReportFacts, error) {
	out, detail, err := runStepDetailInCtx("", "git", []string{"-C", g.Dir, "status", "--porcelain"}, g.localTimeout(), g.gitEnv(), g.Context)
	if err != nil {
		if g.cancelled() {
			return ReportFacts{}, errCancelled
		}
		return ReportFacts{}, gitSyncErrorDetail("status", detail)
	}
	head, err := g.Head()
	if err != nil {
		return ReportFacts{}, err
	}
	remote, err := g.RemoteHead()
	if err != nil {
		return ReportFacts{}, err
	}
	return ReportFacts{Clean: strings.TrimSpace(out) == "", Head: head, HeadRemoto: remote}, nil
}

// Push pushes the local head to the remote job branch. An up-to-date remote
// runs no git push at all. A push the remote refused (for example a
// non-fast-forward after another clone advanced the branch, or a hook that
// declined it) is Rejected with a nil error so the caller can turn it into a
// blocked event: it exits non-zero and --porcelain prints a ref line that
// starts with "!". Any other failure (the network, the credentials, a missing
// repository, a timeout, a missing git) is an error code 19 and the caller
// tries again on the next cycle. It never forces — no --force, no --force-with-lease, no
// + refspec, no --no-verify — and never retries.
func (g *GitSync) Push() (PushResult, error) {
	gitEnv := g.gitEnv()
	if _, ok := platform.FindExecutable("git", gitEnv, platform.Current()); !ok {
		return PushResult{}, gitSyncErrorDetail("push", stepFailureDetail(platform.RunResult{NotFound: true}))
	}
	head, err := g.Head()
	if err != nil {
		return PushResult{}, err
	}
	remote, err := g.RemoteHead()
	if err != nil {
		return PushResult{}, err
	}
	if head == remote {
		return PushResult{Head: head}, nil
	}
	// RunCli directly (deadline and WaitDelay, argv only) so a timeout, a
	// missing executable or a killed process stays apart from a refused push:
	// only a non-zero exit with a rejected ref line is reported as Rejected
	// for the blocked event, never the stderr text (it changes with the git
	// version and the language). The
	// optional context runs under the supervisor's cancel observer: a cancel
	// of it kills this push and reports the cut, not a refusal.
	result := platform.RunCli("git", []string{"-C", g.Dir, "push", "--porcelain", "origin", "HEAD:refs/heads/" + g.Branch}, platform.RunOptions{Context: g.Context, Env: gitEnv, TimeoutMs: timeoutMs(g.netTimeout())})
	switch {
	case !result.NotFound && !result.TimedOut && result.Status != nil && *result.Status == 0:
		return PushResult{Pushed: true, Head: head}, nil
	case g.cancelled():
		// The cancel cut this push: whatever the exit (a git killed on
		// Windows exits 1), it is the cut, never a refusal.
		return PushResult{}, errCancelled
	case result.NotFound || result.TimedOut || result.Status == nil:
		return PushResult{Head: head}, gitSyncErrorDetail("push", stepFailureDetail(result))
	case rejectedRefLine(result.Stdout):
		return PushResult{Rejected: true, Head: head}, nil
	default:
		return PushResult{Head: head}, gitSyncErrorDetail("push", stepFailureDetail(result))
	}
}

// rejectedRefLine reports whether the `git push --porcelain` output has a
// ref line the remote refused: the status flag "!" starts the line, followed
// by a tab ("!\tHEAD:refs/heads/x\t[rejected] (fetch first)").
func rejectedRefLine(stdout string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "!") {
			return true
		}
	}
	return false
}

// Checkpoint stages the tracked and untracked non-ignored files (.gitignore
// respected) and commits them as exactly "wip(job): checkpoint" when
// anything is staged; nothing staged commits nothing. It does not push; the
// caller pushes after it.
func (g *GitSync) Checkpoint() (bool, error) {
	env := g.gitEnv()
	// The timeout is read immediately before each subprocess: under a Limit
	// that means "what is left of the step", a later command must not get the
	// remainder the first one saw.
	if _, detail, err := runStepDetailInCtx("", "git", []string{"-C", g.Dir, "add", "-A"}, g.localTimeout(), env, g.Context); err != nil {
		if g.cancelled() {
			return false, errCancelled
		}
		return false, gitSyncErrorDetail("checkpoint", detail)
	}
	// diff --cached --quiet exits 0 when nothing is staged and 1 when the
	// index differs from HEAD. runStep reports every non-zero exit as the
	// same error, so a genuine diff failure falls through to the commit,
	// which fails on its own when nothing was staged and surfaces as the
	// checkpoint error instead of committing an empty commit. The diff's own
	// diagnostic is kept for that error unless it is the plain exit 1 of a
	// staged change.
	_, diffDetail, err := runStepDetailInCtx("", "git", []string{"-C", g.Dir, "diff", "--cached", "--quiet"}, g.localTimeout(), env, g.Context)
	if err == nil {
		return false, nil
	}
	if _, detail, err := runStepDetailInCtx("", "git", []string{"-C", g.Dir, "commit", "--no-verify", "-m", "wip(job): checkpoint"}, g.localTimeout(), env, g.Context); err != nil {
		if g.cancelled() {
			return false, errCancelled
		}
		if diffDetail != "exit 1" {
			detail = "diff " + diffDetail + "; commit " + detail
			if len(detail) > 400 {
				detail = detail[:400]
			}
		}
		return false, gitSyncErrorDetail("checkpoint", detail)
	}
	return true, nil
}

// EnsureDraftPR returns the existing pull request of the job branch when
// one exists, and opens exactly one draft pull request against the base when
// none does. The bool reports whether this call created the pull request.
// It never marks a pull request ready, merges it, closes it, edits it, or
// deletes a branch.
func (g *GitSync) EnsureDraftPR(title, body string) (PullRequest, bool, error) {
	ghEnv := g.ghEnv()
	list, detail, err := runStepDetailInCtx("", "gh", []string{"pr", "list", "--repo", g.Repo, "--head", g.Branch, "--state", "all", "--json", "number,url,isDraft", "--limit", "1"}, g.ghTimeout(), ghEnv, g.Context)
	if err != nil {
		if g.cancelled() {
			return PullRequest{}, false, errCancelled
		}
		return PullRequest{}, false, gitSyncErrorDetail("pr", detail)
	}
	// Only a JSON array of zero or one element is accepted; anything else
	// (garbage, null, more than one) is a malformed response and refuses the
	// whole call instead of guessing.
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(list), &items); err != nil || items == nil {
		return PullRequest{}, false, gitSyncErrorDetail("pr", unexpectedOutput)
	}
	if len(items) == 1 {
		var item struct {
			Numero  int    `json:"number"`
			URL     string `json:"url"`
			IsDraft bool   `json:"isDraft"`
		}
		if err := json.Unmarshal(items[0], &item); err != nil {
			return PullRequest{}, false, gitSyncErrorDetail("pr", unexpectedOutput)
		}
		estado := "open"
		if item.IsDraft {
			estado = "draft"
		}
		return PullRequest{Numero: item.Numero, URL: item.URL, Estado: estado}, false, nil
	}
	if len(items) != 0 {
		return PullRequest{}, false, gitSyncErrorDetail("pr", unexpectedOutput)
	}
	create, detail, err := runStepDetailInCtx("", "gh", []string{"pr", "create", "--repo", g.Repo, "--draft", "--base", g.Base, "--head", g.Branch, "--title", title, "--body", body}, g.ghTimeout(), ghEnv, g.Context)
	if err != nil {
		if g.cancelled() {
			return PullRequest{}, false, errCancelled
		}
		return PullRequest{}, false, gitSyncErrorDetail("pr", detail)
	}
	url := lastNonEmptyLine(create)
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return PullRequest{}, false, gitSyncErrorDetail("pr", unexpectedOutput)
	}
	numero, err := strconv.Atoi(url[idx+len("/pull/"):])
	if err != nil {
		return PullRequest{}, false, gitSyncErrorDetail("pr", unexpectedOutput)
	}
	return PullRequest{Numero: numero, URL: url, Estado: "draft"}, true, nil
}

// lastNonEmptyLine is the last non-empty line of the output; gh prints
// progress lines before the pull request URL.
func lastNonEmptyLine(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
