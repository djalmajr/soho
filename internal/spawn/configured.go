package spawn

import (
	"context"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

func resolveSpawnLaunchProfile(kind, model, effort string, ctx *core.Config, env platform.Env) (kinds.LaunchProfile, error) {
	if kind != "opencode" {
		return kinds.LaunchProfile{}, nil
	}
	selector := ""
	if env.Get("SOHO_TRANSPORT") != "hc" {
		var err error
		selector, err = core.OpenCodeTUIProfile(ctx, env)
		if err != nil {
			return kinds.LaunchProfile{}, err
		}
	}
	return kinds.ResolveLaunch(kinds.LaunchRequest{Kind: kind, Model: model, Effort: effort, Transport: env.Get("SOHO_TRANSPORT"), TUIProfile: selector, InlineConfig: env.Get("OPENCODE_CONFIG_CONTENT"), ConfigDir: env.Get("OPENCODE_CONFIG_DIR")}, func(string) {})
}

func configuredSpawnRequest(o spawnOptions, kind string, args []string) transport.Start {
	return transport.Start{Name: o.name, PaneID: o.pane, Kind: kind, Timeout: o.timeout, Args: args, LaunchProfile: o.launchProfile.Name, EnvOverrides: o.launchProfile.EnvOverrides}
}

func configuredSpawnCall(o spawnOptions) transport.Call {
	return transport.Call{Timeout: time.Duration(startWrapperTimeoutMs(o.timeout)) * time.Millisecond}
}

func preflightConfiguredSpawn(rt transport.Runtime, o spawnOptions, kind string, args []string) {
	preflight, ok := rt.(transport.ConfiguredLaunchPreflight)
	if ok {
		r := preflight.PreflightConfiguredStart(context.Background(), configuredSpawnRequest(o, kind, args), configuredSpawnCall(o))
		if r.Valid && r.Err == nil && r.Value {
			return
		}
	}
	core.DieFriction("spawn: configured launch preflight refused", 4, "", "")
}

// Configured starts never enter the shell-clearing or busy-retry loop. Only
// classified pre-dispatch failures permit closing an exact owned allocation.
func startConfiguredSpawn(svc transport.Services, o spawnOptions, kind string, created bool, args []string) bool {
	r := svc.Terminal.Start(context.Background(), configuredSpawnRequest(o, kind, args), configuredSpawnCall(o))
	p := r.Process
	if r.Valid && r.Err == nil && p.Status != nil && *p.Status == 0 && !p.NotFound && !p.TimedOut {
		return false
	}
	if r.Err != nil && r.Err.Kind == transport.FailureCommand && !p.NotFound && !p.TimedOut {
		if r.Err.Code == "agent_not_ready" {
			return true
		}
		if r.Err.Code == "agent_pane_busy" {
			core.DieFriction("spawn: configured launch busy; target preserved", 4, "", "")
		}
	}
	safe := r.Err != nil && !p.TimedOut && (r.Err.Kind == transport.FailureProtocol || r.Err.Kind == transport.FailureUnsupported || r.Err.Kind == transport.FailureUnavailable || (r.Err.Kind == transport.FailureCommand && p.NotFound))
	if safe && created {
		closed := svc.Runtime.Close(context.Background(), transport.Target{ID: o.pane}, transport.Call{})
		if closed.Valid && closed.Err == nil && closed.Value {
			core.DieFriction("spawn: configured launch refused; owned pane closed", 4, "", "")
		}
		core.DieFriction("spawn: configured launch refused; pane close unconfirmed", 4, "", "")
	}
	core.DieFriction("spawn: configured launch failed; target preserved", 4, "", "")
	return false
}
