package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

func ResolveParticipant(stateDir, name string, env platform.Env, contexts ...context.Context) (Participant, error) {
	row := strings.Split(core.RosterLine(stateDir, name), "\t")
	if len(row) < 12 || row[0] != name || row[1] == "" || row[7] == "" {
		return Participant{}, fmt.Errorf("%s has no complete worker registration", name)
	}
	ctx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	svc := backend.New(env)
	rep := svc.Legacy.AgentGetRaw(ctx, transport.Target{ID: name}, transport.Call{Timeout: 5 * time.Second})
	if rep.Process.Status == nil || *rep.Process.Status != 0 || rep.Process.TimedOut || rep.Process.NotFound {
		return Participant{}, fmt.Errorf("cannot verify live collaboration participant %s", name)
	}
	var response struct {
		Result struct {
			Agent struct {
				Name      string `json:"name"`
				Pane      string `json:"pane_id"`
				Kind      string `json:"agent"`
				Workspace string `json:"workspace_id"`
				Cwd       string `json:"cwd"`
				Session   struct {
					Kind  string `json:"kind"`
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(rep.Value), &response); err != nil {
		return Participant{}, err
	}
	ag := response.Result.Agent
	if ag.Name != name || ag.Pane != row[1] || ag.Kind != row[2] || ag.Workspace != svc.Runtime.Caller().WorkspaceID || ag.Workspace == "" || ag.Session.Kind == "" || ag.Session.Value == "" || !filepath.IsAbs(ag.Cwd) {
		return Participant{}, errors.New("participant pane, workspace or session no longer matches its registration")
	}
	cwd, err := filepath.EvalSymlinks(ag.Cwd)
	if err != nil {
		return Participant{}, err
	}
	registeredCwd, err := filepath.EvalSymlinks(row[6])
	if err != nil || cwd != registeredCwd {
		return Participant{}, errors.New("participant working directory changed")
	}
	family := kinds.AgentFamily(row[2], row[8])
	return Participant{Name: name, Pane: row[1], Kind: row[2], Role: row[3], Family: family, Started: row[7], Model: row[8], Lane: row[11], Session: Hash([]byte(ag.Session.Kind + "\x00" + ag.Session.Value)), Cwd: cwd}, nil
}

func CheckParticipants(a Assignment, stateDir string, env platform.Env, contexts ...context.Context) error {
	if backend.New(env).Runtime.Caller().WorkspaceID != a.Workspace {
		return errors.New("collaboration belongs to another workspace")
	}
	for _, expected := range []Participant{a.Author, a.Reviewer} {
		live, err := ResolveParticipant(stateDir, expected.Name, env, contexts...)
		if err != nil {
			return err
		}
		if !SameParticipant(expected, live) {
			return errors.New("collaboration participant identity changed; ask the orchestrator to inspect")
		}
		if platform.StateProjectRoot(env, live.Cwd) != a.Root {
			return errors.New("collaboration participant repository changed")
		}
	}
	return nil
}
