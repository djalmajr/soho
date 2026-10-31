package peer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/communication"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionref"
	"github.com/djalmajr/soho/internal/transport"
)

type AssignmentMessageOptions struct {
	AssignmentID string
	Target       string
	Type         string
	Body         string
	EventID      string
	TimeoutMS    int
	Config       *core.Config
	Env          platform.Env
	Cwd          string
}

// SendAssignmentMessage submits once through Herdr without waiting for the
// receiving worker to finish. Submitted is terminal delivery, not receipt
// by the model or approval of the task.
func SendAssignmentMessage(options AssignmentMessageOptions) (collaboration.DeliveryResult, error) {
	refused := func(err error) (collaboration.DeliveryResult, error) {
		return collaboration.DeliveryResult{Status: "refused", Cause: err.Error()}, err
	}
	if core.Nowrite(options.Env) {
		return refused(errors.New("worker message is a write; NOWRITE refuses"))
	}
	if options.Config == nil || options.AssignmentID == "" || options.Type == "" || strings.TrimSpace(options.Body) == "" {
		return refused(errors.New("worker message requires assignment, type and nonempty body"))
	}
	// The caller-pane check runs after the options are valid and before
	// the delivery acts in the name of the sender pane (DJA-394); under
	// refuse it exits 2, under warn it logs the line and the delivery
	// proceeds.
	core.CallerGate("collaborate", options.Config, options.Env, options.Cwd)
	if platform.Current() == "win32" {
		if executable, ok := platform.FindExecutable("herdr", options.Env, platform.Current()); ok {
			ext := strings.ToLower(filepath.Ext(executable))
			if ext == ".cmd" || ext == ".bat" {
				return refused(errors.New("worker messages on Windows require the native Herdr executable; a batch launcher cannot preserve the complete multiline prompt"))
			}
		}
	}
	budget := options.TimeoutMS
	if budget <= 0 || budget > 30000 {
		budget = 30000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(budget)*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	remaining := func() int { return max(1, int(time.Until(deadline).Milliseconds())) }
	if core.StateInSkill(options.Config, options.Env, options.Cwd) != "" {
		return refused(errors.New("worker messages cannot write state inside the skill"))
	}
	env := options.Env.Clone()
	// The derived workspace id must reach the child process environment and
	// the policy paths unchanged, so it is written into this env copy.
	if env.Get("HERDR_WORKSPACE_ID") == "" {
		env["HERDR_WORKSPACE_ID"] = core.WorkspaceID(options.Config, env, options.Cwd)
	}
	// One transport service for this delivery: the recipient inspection and
	// the submission go through it.
	svc := backend.New(env)
	sd := core.StateDirPath(options.Config, env, options.Cwd)
	a, err := (collaboration.Store{StateDir: sd}).Read(options.AssignmentID)
	if err != nil {
		return refused(err)
	}
	if !a.Active() {
		return refused(errors.New("collaboration is not active for delivery"))
	}
	if platform.StateProjectRoot(env, options.Cwd) != a.Root {
		return refused(errors.New("sender is outside the assigned repository"))
	}
	if err := collaboration.CheckParticipants(a, sd, env, ctx); err != nil {
		return refused(err)
	}
	from, member := a.Member(svc.Runtime.Caller().PaneID)
	if !member {
		return refused(errors.New("sender is not an assigned participant"))
	}
	var pending *collaboration.Event
	if options.Type != "review.question" {
		for i := range a.Events {
			e := &a.Events[i]
			typ := map[string]string{"ready": "review.ready", "corrected": "review.ready", "findings": "review.finding", "approved": "review.result"}[e.Type]
			if options.EventID != "" && e.ID == options.EventID && e.Actor == from.Name && e.Delivery == "pending" && e.Round == a.Round && e.Revision == a.Revision && typ == options.Type {
				pending = e
				break
			}
		}
		if pending == nil {
			return refused(errors.New("review event messages require a persisted pending event for this revision"))
		}
	}
	if a.Phase == collaboration.Escalated && (pending == nil || pending.Type != "findings") {
		return refused(errors.New("escalated collaboration awaits the orchestrator"))
	}
	to := a.Reviewer
	if from.Pane == to.Pane {
		to = a.Author
	}
	if options.Target != to.Name {
		ref := sessionref.ParseRef(options.Target)
		if ref == nil || ref.Machine != sessionref.LocalMachine || ref.PaneID != to.Pane {
			return refused(errors.New("destination is not the assigned local participant"))
		}
	}
	if pending != nil || a.Phase == collaboration.Reviewing || a.Phase == collaboration.AwaitingOrchestrator {
		if err := collaboration.CheckRevision(a); err != nil {
			return refused(err)
		}
	}
	policy, err := communication.Load(options.Config, env)
	if err != nil {
		return refused(err)
	}
	destinationConfig, destinationEnv := inboundConfiguration(to.Cwd, a.Workspace, env)
	for _, layer := range destinationConfig.Layers {
		if layer.ReadError != nil {
			return refused(fmt.Errorf("destination inbound configuration is unreadable: %s: %w", layer.Source, layer.ReadError))
		}
	}
	inbound := core.Cfg(&destinationConfig, "inbound", "auto", destinationEnv)
	if inbound != "auto" && inbound != "off" {
		return refused(errors.New("destination has an invalid inbound policy"))
	}
	err = policy.Authorize(communication.Request{From: communication.Participant{Name: from.Name, Role: from.Role, Lane: from.Lane}, To: communication.Participant{Name: to.Name, Role: to.Role, Lane: to.Lane}, Type: options.Type, AssignmentID: a.ID, Assigned: true, Inbound: inbound})
	if err != nil {
		return refused(err)
	}
	if err := ctx.Err(); err != nil {
		return refused(fmt.Errorf("submission deadline reached before delivery: %w", err))
	}
	screen, cause := readScreen(sessionref.LocalMachine, to.Pane, "visible", 0, env, remaining(), nil)
	if cause != "" {
		return refused(fmt.Errorf("cannot inspect recipient screen: %s", cause))
	}
	state := agentGet(svc, sessionref.LocalMachine, to.Pane, remaining())
	if !state.OK || state.PaneID != to.Pane {
		return refused(errors.New("recipient changed or became unavailable"))
	}
	if isDialogScreen(screen, to.Kind, state.Status) {
		return refused(errors.New("recipient is showing a dialog; nothing was sent"))
	}
	current, err := collaboration.ResolveParticipant(sd, to.Name, env, ctx)
	if err != nil || !collaboration.SameParticipant(to, current) {
		return refused(errors.New("recipient session changed before submission"))
	}
	if time.Now().After(deadline) {
		return refused(errors.New("submission deadline reached before delivery"))
	}
	id := options.EventID
	if id == "" {
		id = RandomPeerID()
	}
	ref := HeaderField(sessionref.FormatRef(sessionref.Ref{Machine: sessionref.LocalMachine, PaneID: from.Pane}))
	header := PeerHeaderAssignment(PeerSender{Ref: ref, Name: HeaderField(from.Name), Kind: HeaderField(from.Kind), Role: HeaderField(from.Role), Model: HeaderField(from.Model)}, from.Name, a.ID, id)
	metadata := fmt.Sprintf("Assignment: %s; type: %s; round: %d; revision: %s.", a.ID, HeaderField(options.Type), a.Round, a.Revision)
	message := header + "\n" + metadata + "\n\n" + QuotePeerBody(LiteralPeerText(options.Body)) + "\n" + PeerEndLine(id)
	// No --wait: neither side waits for the other's turn, report or idle.
	rep := svc.Legacy.AgentPromptRaw(context.Background(), transport.Target{ID: to.Pane}, message, transport.Call{Timeout: time.Duration(remaining()) * time.Millisecond})
	r := rep.Process
	result := collaboration.DeliveryResult{Status: "submitted"}
	if r.Status == nil || *r.Status != 0 || r.TimedOut || r.NotFound {
		code, cause := structuredError(r.Stdout, r.Stderr, "agent prompt", "submission could not be confirmed")
		result = collaboration.DeliveryResult{Status: "uncertain", Cause: cause}
		if code == "agent_blocked" || code == "agent_not_found" || r.NotFound {
			result.Status = "refused"
		}
	}
	appendPeerAssignmentLog(sd, sessionref.FormatRef(sessionref.Ref{Machine: sessionref.LocalMachine, PaneID: from.Pane}), sessionref.FormatRef(sessionref.Ref{Machine: sessionref.LocalMachine, PaneID: to.Pane}), result.Status, utf16Length(options.Body), id, env, a.ID, options.Type, fmt.Sprint(a.Round))
	if result.Status != "submitted" {
		return result, errors.New(result.Cause)
	}
	return result, nil
}
