package setup

// migratePlugin replaces the former djalmajr.herdr-soho Herdr plugin with
// djalmajr.soho. It returns one line per step ("migrate: plugin: ...") and
// runs nothing that writes when dry is true.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// herdrPluginTimeoutMs matches the doctor's herdr CLI contract (a bounded
// call, never an unattended hang while setup or doctor runs).
const herdrPluginTimeoutMs = 30000

// pluginCall is the bounded call every herdr plugin step of the migration
// runs with (DJA-596).
var pluginCall = transport.Call{Timeout: herdrPluginTimeoutMs * time.Millisecond}

const (
	formerPluginID = "djalmajr.herdr-soho"
	sohoPluginID   = "djalmajr.soho"

	formerShortcutPrefix = "djalmajr.herdr-soho."
	sohoShortcutPrefix   = "djalmajr.soho."
)

var (
	// releaseVersionRef accepts release tags (v1.2.3, v0.3.0-rc.1) that
	// 'herdr plugin install --ref' can resolve on GitHub; anything else is a
	// development build that must be linked from the checkout by hand.
	releaseVersionRef = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
	// formerShortcutLine anchors the rename to real Herdr shortcut lines: a
	// command key whose value starts with the former plugin id. Comments and
	// other keys keep the former id untouched.
	formerShortcutLine = regexp.MustCompile(`^\s*command\s*=\s*"djalmajr\.herdr-soho\.`)
	// sohoShortcutLine matches a shortcut bound to a djalmajr.soho action.
	sohoShortcutLine = regexp.MustCompile(`^\s*command\s*=\s*"djalmajr\.soho\.`)
)

// herdrPluginRef is one row of 'herdr plugin list --json' that the migration
// needs: the id and the source kind (local vs installed).
type herdrPluginRef struct {
	ID   string
	Kind string
}

// migratePlugin replaces the former djalmajr.herdr-soho plugin with
// djalmajr.soho. It returns one line per step ("migrate: plugin: ...")
// and runs nothing that writes when dry is true.
func migratePlugin(env platform.Env, version string, dry bool) []string {
	// One backend for this invocation: every herdr plugin step of the
	// migration runs through it (DJA-596).
	svc := backend.New(env)
	list := svc.Legacy.PluginListRaw(context.Background(), pluginCall)
	if list.Process.NotFound {
		// Without Herdr there is no plugin registration to migrate.
		return nil
	}
	if !herdrCallOK(list.Process) {
		return []string{fmt.Sprintf("migrate: plugin: skipped (herdr plugin list failed: %s)", herdrCallReason(list.Process))}
	}
	if strings.TrimSpace(list.Process.Stdout) == "" {
		// A list that succeeds with no output registers nothing to migrate.
		return nil
	}
	plugins, ok := parsePluginList(list.Process.Stdout)
	if !ok {
		return []string{"migrate: plugin: skipped (herdr plugin list failed: invalid JSON)"}
	}
	former, found := findHerdrPlugin(plugins, formerPluginID)
	if !found {
		return nil
	}
	sohoInstalled := pluginPresent(plugins, sohoPluginID)
	lines := []string{}
	if !sohoInstalled {
		if !releaseVersionRef.MatchString(version) {
			return []string{fmt.Sprintf("migrate: plugin: link djalmajr.soho from your checkout first: herdr plugin link %s", checkoutPluginDir(env))}
		}
		if dry {
			lines = append(lines, fmt.Sprintf("migrate: plugin: would run: herdr plugin install djalmajr/soho/plugin --ref %s --yes", version))
		} else if install := svc.Legacy.PluginInstallRefRaw(context.Background(), version, pluginCall); !herdrCallOK(install.Process) {
			return []string{fmt.Sprintf("migrate: plugin: install of djalmajr.soho failed: %s; the former plugin is kept", herdrCallReason(install.Process))}
		}
	}
	// The former plugin is removed only once its shortcuts are known to be
	// moved: an unknown config, an unreadable one or a reload that fails
	// keeps it, so no shortcut is left pointing at a missing plugin.
	config := herdrConfigToml(svc, env)
	if config == "" {
		return append(lines, "migrate: plugin: could not locate the Herdr config (herdr plugin config-dir failed); the former plugin is kept")
	}
	data, readErr := os.ReadFile(config)
	if readErr != nil && !os.IsNotExist(readErr) {
		return append(lines, fmt.Sprintf("migrate: plugin: could not read %s; the former plugin is kept", config))
	}
	// Without a config file Herdr has no custom shortcut to move.
	if readErr == nil {
		content := string(data)
		updated, count := replaceFormerShortcuts(content)
		if count > 0 {
			if dry {
				lines = append(lines, fmt.Sprintf("migrate: plugin: would replace %d shortcut(s) in %s", count, config))
			} else if backup := writeFormerConfigBackup(config, data); backup == "" || platform.AtomicWrite(config, updated) != nil {
				return append(lines, fmt.Sprintf("migrate: plugin: could not update %s; the former plugin is kept", config))
			} else {
				lines = append(lines, fmt.Sprintf("migrate: plugin: %d shortcut(s) now call djalmajr.soho (backup %s)", count, backup))
			}
			content = updated
		}
		// A shortcut on djalmajr.soho, written now or by an earlier run,
		// works only once Herdr reloads its config: the former plugin goes
		// only after a successful reload.
		if hasSohoShortcut(content) {
			if dry {
				lines = append(lines, "migrate: plugin: would run: herdr server reload-config")
			} else if reload := svc.Legacy.ServerReloadConfigRaw(context.Background(), pluginCall); !herdrCallOK(reload.Process) {
				return append(lines, fmt.Sprintf("migrate: plugin: herdr server reload-config failed: %s; the former plugin is kept until Herdr loads the djalmajr.soho shortcuts: restart Herdr and run soho setup again", herdrCallReason(reload.Process)))
			}
		}
	}
	verb := "uninstall"
	if former.Kind == "local" {
		verb = "unlink"
	}
	if dry {
		lines = append(lines, fmt.Sprintf("migrate: plugin: would run: herdr plugin %s %s", verb, formerPluginID))
		return lines
	}
	if remove := svc.Legacy.PluginRemoveRaw(context.Background(), verb, formerPluginID, pluginCall); herdrCallOK(remove.Process) {
		lines = append(lines, fmt.Sprintf("migrate: plugin: removed the former %s (%s)", formerPluginID, verb))
	} else {
		lines = append(lines, fmt.Sprintf("migrate: plugin: could not remove %s: %s", formerPluginID, herdrCallReason(remove.Process)))
	}
	return lines
}

func herdrCallOK(result transport.ProcessResult) bool {
	return !result.NotFound && result.Error == "" && result.Status != nil && *result.Status == 0
}

// herdrCallReason is the short, deterministic reason a failed herdr call
// ends up on the migration lines.
func herdrCallReason(result transport.ProcessResult) string {
	switch {
	case result.NotFound:
		return "herdr not found"
	case result.TimedOut:
		return "timed out"
	case result.Status != nil:
		return "exit " + strconv.Itoa(*result.Status)
	case result.Error != "":
		return "failed to start"
	default:
		return "failed"
	}
}

// parsePluginList decodes the 'herdr plugin list --json' envelope
// ({"id":"cli:plugin","result":{"plugins":[...]}}). It rejects a response
// whose result is missing or malformed rather than reading a partial list.
func parsePluginList(stdout string) ([]herdrPluginRef, bool) {
	var doc struct {
		Result *struct {
			Plugins []struct {
				PluginID string `json:"plugin_id"`
				Source   *struct {
					Kind string `json:"kind"`
				} `json:"source"`
			} `json:"plugins"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || doc.Result == nil {
		return nil, false
	}
	refs := make([]herdrPluginRef, 0, len(doc.Result.Plugins))
	for _, plugin := range doc.Result.Plugins {
		kind := ""
		if plugin.Source != nil {
			kind = plugin.Source.Kind
		}
		refs = append(refs, herdrPluginRef{ID: plugin.PluginID, Kind: kind})
	}
	return refs, true
}

func findHerdrPlugin(plugins []herdrPluginRef, id string) (herdrPluginRef, bool) {
	for _, plugin := range plugins {
		if plugin.ID == id {
			return plugin, true
		}
	}
	return herdrPluginRef{}, false
}

func pluginPresent(plugins []herdrPluginRef, id string) bool {
	_, found := findHerdrPlugin(plugins, id)
	return found
}

// herdrConfigToml finds the Herdr config.toml through
// 'herdr plugin config-dir djalmajr.soho': three levels above the printed
// plugin config dir. It returns "" when the path cannot be resolved.
func herdrConfigToml(svc transport.Services, env platform.Env) string {
	called := svc.Legacy.PluginConfigDirRaw(context.Background(), sohoPluginID, pluginCall)
	if !herdrCallOK(called.Process) {
		return ""
	}
	dir := strings.TrimSpace(called.Process.Stdout)
	if dir == "" {
		return ""
	}
	if strings.HasPrefix(dir, "~"+string(filepath.Separator)) || dir == "~" {
		home := ""
		if runtime.GOOS == "windows" {
			home = env.Get("USERPROFILE")
		} else {
			home = env.Get("HOME")
		}
		if home == "" {
			return ""
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	for i := 0; i < 3; i++ {
		dir = filepath.Dir(dir)
	}
	return filepath.Join(dir, "config.toml")
}

// hasSohoShortcut reports whether a Herdr config binds a shortcut to a
// djalmajr.soho action (a command = "djalmajr.soho.*" line).
func hasSohoShortcut(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if sohoShortcutLine.MatchString(line) {
			return true
		}
	}
	return false
}

// replaceFormerShortcuts rewrites the former plugin prefix to the new one on
// the Herdr shortcut lines only (the command = "djalmajr.herdr-soho.*"
// lines); comments and other lines keep the former id.
func replaceFormerShortcuts(content string) (string, int) {
	lines := strings.Split(content, "\n")
	changed := 0
	for i, line := range lines {
		if !formerShortcutLine.MatchString(line) {
			continue
		}
		lines[i] = strings.Replace(line, formerShortcutPrefix, sohoShortcutPrefix, 1)
		changed++
	}
	return strings.Join(lines, "\n"), changed
}

// writeFormerConfigBackup copies the Herdr config beside itself under the
// dated config.toml.bak-soho-<UTC> name and returns the backup path; it
// reports "" when the copy cannot be written.
func writeFormerConfigBackup(config string, data []byte) string {
	backup := config + ".bak-soho-" + time.Now().UTC().Format("20060102T150405Z")
	perm := os.FileMode(0o600)
	if info, err := os.Stat(config); err == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(backup, data, perm); err != nil {
		return ""
	}
	return backup
}

// checkoutPluginDir is the plugin directory of the checkout that holds the
// skill named by SOHO_SKILL_DIR (<checkout>/skills/soho beside
// <checkout>/plugin); the development build links it by hand. When the env
// does not name the skill the brief's own placeholder stays in the line.
func checkoutPluginDir(env platform.Env) string {
	skillDir := env.Get("SOHO_SKILL_DIR")
	if skillDir == "" {
		return "<checkout>/plugin"
	}
	return filepath.Join(filepath.Dir(filepath.Dir(skillDir)), "plugin")
}
