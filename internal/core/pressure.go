package core

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/platform"
)

// Resource pressure for `spawn`, `wait`, and `status`: one line on stderr
// (never stdout, never the friction log) when the state dir's disk or the
// swap passed the configured limits, and — for the spawn only, above the
// harder refusal limits — the refusal to open a new pane (exit 25, skipped
// by --force). Every measurement failure is silent (the command's stdout
// and exit code never change), and under SOHO_NOWRITE nothing is written,
// no warning goes out and the spawn is never refused.

const (
	pressureDiskKey      = "pressure_disk_free_percent"
	pressureDiskFloorKey = "pressure_disk_free_min_gb"
	pressureSwapKey      = "pressure_swap_percent"

	// The harder spawn-only limits: above them the spawn refuses to open a
	// new pane (exit 25) unless it gets --force. They measure the same
	// things as the warning keys (platform.DiskFree / platform.SwapUsage);
	// on Windows the swap is not measured, so only the disk part applies.
	pressureRefuseDiskKey          = "pressure_refuse_disk_free_percent"
	pressureRefuseDiskFloorKey     = "pressure_refuse_disk_free_min_gb"
	pressureRefuseDiskFloorDefault = 5
	pressureRefuseSwapKey          = "pressure_refuse_swap_percent"
	pressureRefuseDiskDefault      = 5
	pressureRefuseSwapDefault      = 95

	pressureDiskDefault      = 15
	pressureDiskFloorDefault = 20
	pressureSwapDefault      = 80
	pressureGiB              = int64(1024 * 1024 * 1024)

	// wait/status warn at most once per 10 minutes per state dir; the
	// stamp is the mtime of <state>/wait/pressure-warned.
	pressureWarnInterval = 10 * time.Minute
	pressureStampName    = "pressure-warned"
)

// PressureWarn is the spawn entry point: it warns whenever the machine is
// under pressure (no throttle), before the pane is created. The spawn
// proceeds as usual.
func PressureWarn(ctx *Config, env platform.Env, cwd string) {
	pressureCheck(ctx, env, cwd, false)
}

// PressureWarnThrottled is the wait/status entry point: at most one
// warning per 10 minutes per state dir.
func PressureWarnThrottled(ctx *Config, env platform.Env, cwd string) {
	pressureCheck(ctx, env, cwd, true)
}

func pressureCheck(ctx *Config, env platform.Env, cwd string, throttle bool) {
	if Nowrite(env) {
		return
	}
	stateDir := pressureStateDir(ctx, env, cwd)
	if stateDir == "" {
		return
	}
	parts := pressureParts(ctx, env, stateDir)
	if len(parts) == 0 {
		return
	}
	if throttle && recentlyWarned(stateDir, platform.Now()) {
		return
	}
	_, _ = fmt.Fprintf(platform.Stderr, "soho: resource pressure: %s; run 'soho gc' and close idle panes before opening more\n", strings.Join(parts, "; "))
	if throttle {
		stampPressure(stateDir)
	}
}

// PressureRefuseParts is the spawn refusal entry point (DJA-392): it holds
// only the parts that passed the harder refusal limits
// (pressure_refuse_disk_free_percent, pressure_refuse_swap_percent), with
// the same text as the warning parts. It runs after the reuse decision and
// before any herdr call that creates a pane or agent; the caller refuses
// (exit 25) when it returns a part, unless --force was passed. A limit of 0
// disables its part, a failed measurement yields no part (a failure never
// refuses), and under SOHO_NOWRITE nothing refuses.
func PressureRefuseParts(ctx *Config, env platform.Env, cwd string) []string {
	if Nowrite(env) {
		return nil
	}
	stateDir := pressureStateDir(ctx, env, cwd)
	if stateDir == "" {
		return nil
	}
	return pressureMeasure(ctx, env, stateDir,
		pressureThreshold(ctx, pressureRefuseDiskKey, pressureRefuseDiskDefault, env),
		PressureDiskFloor(ctx, env, true),
		pressureThreshold(ctx, pressureRefuseSwapKey, pressureRefuseSwapDefault, env))
}

// pressureStateDir resolves <state dir> without dying or spending a herdr
// call: the workspace id comes from HERDR_WORKSPACE_ID; without it there is
// no pressure check (a command that cannot resolve its state dir cannot be
// opened more work for). Any failure means silent skip.
func pressureStateDir(ctx *Config, env platform.Env, cwd string) string {
	workspace := backend.New(env).Runtime.Caller().WorkspaceID
	if workspace == "" {
		return ""
	}
	return filepath.Join(StateRootPath(ctx, env, cwd), workspace)
}

// pressureParts holds only the parts that passed the warning limits.
func pressureParts(ctx *Config, env platform.Env, stateDir string) []string {
	return pressureMeasure(ctx, env, stateDir,
		pressureThreshold(ctx, pressureDiskKey, pressureDiskDefault, env),
		PressureDiskFloor(ctx, env, false),
		pressureThreshold(ctx, pressureSwapKey, pressureSwapDefault, env))
}

// pressureMeasure holds only the parts that passed the given limits: the
// disk part when free percent is below diskLimit and free bytes below the
// floor (0 disables the percent, or turns the floor off), and the swap part
// when used percent is above swapLimit. Measurement failure yields no part.
func pressureMeasure(ctx *Config, env platform.Env, stateDir string, diskLimit int, diskFloor int64, swapLimit int) []string {
	var parts []string
	if diskLimit > 0 {
		if free, total, ok := platform.DiskFree(stateDir); ok && total > 0 {
			pct := free * 100 / total
			if DiskUnderPressure(free, pct, diskLimit, diskFloor) {
				parts = append(parts, fmt.Sprintf("disk %s has %d%% free (%s of %s)", stateDir, pct, platform.HumanSize(free), platform.HumanSize(total)))
			}
		}
	}
	if part, _ := CopiesPressure(ctx, env, stateDir, diskLimit, diskFloor); part != "" {
		parts = append(parts, part)
	}
	if swapLimit > 0 {
		if used, total, ok := platform.SwapUsage(env); ok && total > 0 {
			pct := used * 100 / total
			if pct > int64(swapLimit) {
				parts = append(parts, fmt.Sprintf("swap %d%% used (%s of %s)", pct, platform.HumanSize(used), platform.HumanSize(total)))
			}
		}
	}
	return parts
}

// CopiesPressure measures the copy parent only when its device is known and distinct.
func CopiesPressure(ctx *Config, env platform.Env, stateDir string, limit int, floor int64) (string, string) {
	if limit <= 0 {
		return "", ""
	}
	parent, _, err := CopiesParent(ctx, env, "")
	if err != nil {
		return "", ""
	}
	stateDev, stateOK := CopiesDevice(stateDir)
	copyDev, copyOK := CopiesDevice(parent)
	if !stateOK || !copyOK || stateDev == copyDev {
		return "", ""
	}
	return copiesDiskPart(parent, limit, floor), parent
}

func copiesDiskPart(parent string, limit int, floor int64) string {
	free, total, ok := platform.DiskFree(parent)
	if !ok || total <= 0 {
		return ""
	}
	pct := free * 100 / total
	if !DiskUnderPressure(free, pct, limit, floor) {
		return ""
	}
	return fmt.Sprintf("disk %s (mutation copies) has %d%% free (%s of %s)", parent, pct, platform.HumanSize(free), platform.HumanSize(total))
}

// CopiesDevice resolves missing directories to the filesystem of their nearest existing ancestor.
func CopiesDevice(path string) (string, bool) {
	for {
		if dev, ok := platform.DiskDevice(path); ok {
			return dev, true
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false
		}
		path = parent
	}
}

// CopiesWarningPressure applies the configured warning threshold.
func CopiesWarningPressure(ctx *Config, env platform.Env, stateDir string) (string, string) {
	parent, _, err := CopiesParent(ctx, env, "")
	if err != nil {
		return "", ""
	}
	return copiesDiskPart(parent, pressureThreshold(ctx, pressureDiskKey, pressureDiskDefault, env), PressureDiskFloor(ctx, env, false)), parent
}

// DiskUnderPressure applies the shared warning/refusal disk rule. floor is
// in bytes; zero leaves the percent as the sole limit.
func DiskUnderPressure(free, pct int64, limit int, floor int64) bool {
	return limit > 0 && pct < int64(limit) && (floor == 0 || free < floor)
}

func pressureDiskFloorValue(raw string) (int64, bool) {
	if !decimalRE.MatchString(raw) {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value <= math.MaxInt64/pressureGiB
}

// PressureDiskFloor reads the warning or refusal floor, returning bytes.
// Invalid values use the corresponding default and emit the normal warning.
func PressureDiskFloor(ctx *Config, env platform.Env, refuse bool) int64 {
	key, fallback := pressureDiskFloorKey, pressureDiskFloorDefault
	if refuse {
		key, fallback = pressureRefuseDiskFloorKey, pressureRefuseDiskFloorDefault
	}
	raw := Cfg(ctx, key, strconv.Itoa(fallback), env)
	if value, ok := pressureDiskFloorValue(raw); ok {
		return value * pressureGiB
	}
	_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: invalid value '%s' for %s; using the default %d\n", raw, key, fallback)
	return int64(fallback) * pressureGiB
}

// pressureThreshold reads the key through the usual layers (env override
// included). Only a non-negative decimal integer is valid, the domain that
// config set accepts; anything else (a negative value included) falls back
// to the default with the project's warning line, so only 0 turns it off.
func pressureThreshold(ctx *Config, key string, limit int, env platform.Env) int {
	raw := Cfg(ctx, key, strconv.Itoa(limit), env)
	if decimalRE.MatchString(raw) {
		if value, err := strconv.Atoi(raw); err == nil {
			return value
		}
	}
	_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: invalid value '%s' for %s; using the default %d\n", raw, key, limit)
	return limit
}

func recentlyWarned(stateDir string, now time.Time) bool {
	info, err := os.Stat(filepath.Join(stateDir, "wait", pressureStampName))
	return err == nil && now.Sub(info.ModTime()) < pressureWarnInterval
}

// stampPressure touches <state>/wait/pressure-warned (best effort: a
// failed stamp still warned once, and the next run retries the stamp).
func stampPressure(stateDir string) {
	dir := filepath.Join(stateDir, "wait")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, pressureStampName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		return
	}
	_ = f.Close()
}
