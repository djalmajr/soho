package cli

import (
	"errors"
	"fmt"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/job"
	"github.com/djalmajr/soho/internal/platform"
)

// configGetKeyOk is the `config get` key criterion: exactly the keys
// config set accepts (core.ConfigKeyOk on the raw argument).
func configGetKeyOk(raw string) bool {
	return core.ConfigKeyOk(raw)
}

func configGetScalarKey(key string) bool {
	for _, scalar := range core.ConfigScalarKeys {
		if scalar == key {
			return true
		}
	}
	return false
}

// cmdConfigGet implements `soho config get <key>`: it prints the effective
// value of the key, exactly as its consumer reads it, plus a newline, and
// exits 0 (an empty value prints only the newline). The seven machine keys
// the job reads print the raw string job.MachineValues resolves (the job's
// defaults over the user file, no ~ expansion, zero user selection like
// LoadMachine); job_auto_cleanup and job_human_input_minutes print the
// autoCloseSettings resolution over the cwd; every other key prints the
// `soho config` listing's VALUE column for the key.
func cmdConfigGet(argv []string, configCtx *core.Config, env platform.Env, cwd string) {
	if len(argv) != 1 {
		platform.Die("usage: config get <key>", 2)
	}
	raw := argv[0]
	if !configGetKeyOk(raw) {
		platform.Die("config get: unknown key '"+raw+"'", 2)
	}
	key := core.NormalizeKey(raw)
	var value string
	switch {
	case key == "machine_label" || key == "job_orgs" || key == "job_repos_root" || key == "job_timeout" || key == "job_blocked_timeout" || key == "job_finishing_timeout" || key == "job_wake_cmd":
		values, err := job.MachineValues(platform.Current(), env, core.UserSelection{})
		if err != nil {
			var platformExit *platform.ExitError
			if errors.As(err, &platformExit) && platformExit != nil {
				platform.Die(platformExit.Msg, platformExit.Code)
			}
			platform.Die("config get: "+err.Error(), 2)
		}
		value = values[key]
	case key == "job_auto_cleanup", key == "job_human_input_minutes":
		// The same resolution the job's autoCloseSettings makes over the
		// cwd's layered configuration.
		fallback := job.DefaultJobAutoCleanup
		if key == "job_human_input_minutes" {
			fallback = job.DefaultJobHumanInputMinutes
		}
		ctx := core.LoadConfig(env, cwd)
		value = core.Cfg(&ctx, key, fallback, env)
	case configGetScalarKey(key):
		value = core.ConfigScalarValue(configCtx, key, env, cwd)
	default:
		value = core.Cfg(configCtx, key, "", env)
	}
	_, _ = fmt.Fprintf(platform.Stdout, "%s\n", value)
}
