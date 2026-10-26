package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

// doctorPeerInbound warns about the receiver side of signed peer messages
// (peerauth). With the default configuration (no inbound_auth, no
// inbound_allow anywhere) it says nothing, so the doctor's output is the one
// it always had.
func doctorPeerInbound(ctx *core.Config, env platform.Env, cwd string, s *Say) {
	userFile := core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{})
	for _, layer := range ctx.Layers {
		if layer.Source != "project" && layer.Source != "session" {
			continue
		}
		for _, key := range []string{"inbound_auth", "inbound_allow"} {
			if _, set := layer.Entries[key]; set {
				s.Warning(fmt.Sprintf("config: %s in the %s file %s is ignored; only the user file %s sets it", key, layer.Source, layer.File, userFile))
			}
		}
	}
	for _, key := range []string{"inbound_auth", "inbound_allow"} {
		if env.Get("SOHO_"+strings.ToUpper(key)) != "" {
			s.Warning(fmt.Sprintf("config: SOHO_%s is ignored; only the user file %s sets %s", strings.ToUpper(key), userFile, key))
		}
	}
	policy, err := peerauth.LoadPolicy(userFile)
	if err != nil {
		return
	}
	if policy.ModeInvalid {
		s.Warning(fmt.Sprintf("config: inbound_auth='%s' is not off|warn|enforce (it applies as enforce)", policy.ModeValue))
	}
	if policy.Mode == peerauth.ModeOff {
		return
	}
	dir := filepath.Dir(userFile)
	if line, corrupt := peerauth.SeenCorrupt(dir); corrupt {
		s.Warning(fmt.Sprintf("peer replay cache %s is corrupt (line %d); messages are refused until it is fixed: delete it", filepath.Join(dir, peerauth.SeenFile), line))
	}
	checked := map[string]bool{}
	checkKey := func(name string) {
		if checked[name] {
			return
		}
		checked[name] = true
		path, ok := peerauth.KeyPath(dir, name)
		if !ok {
			s.Warning(fmt.Sprintf("inbound_allow: label '%s' is not a valid machine name (letters, digits, '.', '_' and '-', at most 64)", name))
			return
		}
		switch _, status := peerauth.ReadKey(dir, name); status {
		case peerauth.KeyMissing:
			s.Warning(fmt.Sprintf("inbound_allow: no key file for '%s' (%s); create it with: soho peer-key new %s", name, path, name))
			return
		case peerauth.KeyInvalid:
			s.Warning(fmt.Sprintf("peer key %s is invalid (it must hold 64 hex characters, 32 bytes); replace it with: soho peer-key new %s", path, name))
			return
		}
		if runtime.GOOS != "windows" {
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm() != 0o600 {
				s.Warning(fmt.Sprintf("peer key %s has mode %04o; chmod 600 it", path, info.Mode().Perm()))
			}
		}
	}
	for _, label := range policy.Allow {
		checkKey(label)
	}
	if _, status := peerauth.ReadKey(dir, peerauth.LocalName); status == peerauth.KeyMissing {
		s.Warning("peer key local is missing: peer messages to this machine's own panes go out unsigned and, with inbound_auth=enforce, are refused; create it with: soho peer-key new local")
	} else {
		checkKey(peerauth.LocalName)
	}
	if runtime.GOOS == "windows" {
		s.Warning("peer key permissions are not checked on Windows; keep the peer-keys directory readable by your user only")
	}
	settings := filepath.Join(platform.ProjectRoot(env, cwd), ".claude", "settings.json")
	localSettings := filepath.Join(platform.ProjectRoot(env, cwd), ".claude", "settings.local.json")
	if !userPromptSubmitProtected(settings) && !userPromptSubmitProtected(localSettings) {
		s.Warning(fmt.Sprintf("%s and %s have no 'soho hook reminder' or 'soho hook inbound' in UserPromptSubmit: the Claude Code panes of this project do not check peer messages (run 'soho setup')", settings, localSettings))
	}
	if policy.Mode == peerauth.ModeEnforce {
		doctorUnprotectedAgents(ctx, env, cwd, s)
	}
}

// userPromptSubmitProtected reports whether the settings file carries a
// UserPromptSubmit hook that runs the peer check (`soho hook reminder` or
// `soho hook inbound`).
func userPromptSubmitProtected(file string) bool {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		return false
	}
	v, err := jsonjs.Parse([]byte(raw))
	if err != nil {
		return false
	}
	root, ok := v.(*jsonjs.Object)
	if !ok {
		return false
	}
	hooks, _ := root.Get("hooks")
	hooksObject, ok := hooks.(*jsonjs.Object)
	if !ok {
		return false
	}
	event, _ := hooksObject.Get("UserPromptSubmit")
	rows, ok := event.([]any)
	if !ok {
		return false
	}
	for _, row := range rows {
		rowObject, ok := row.(*jsonjs.Object)
		if !ok {
			continue
		}
		list, _ := rowObject.Get("hooks")
		entries, ok := list.([]any)
		if !ok {
			continue
		}
		for _, entry := range entries {
			entryObject, ok := entry.(*jsonjs.Object)
			if !ok {
				continue
			}
			command, _ := entryObject.Get("command")
			if command == setuptext.SetupHookReminder() || command == "soho hook inbound" {
				return true
			}
		}
	}
	return false
}

// doctorUnprotectedAgents warns about each live agent of the roster that is
// not protected by this project's supported inbound hooks.
func doctorUnprotectedAgents(ctx *core.Config, env platform.Env, cwd string, s *Say) {
	if env.Get("HERDR_ENV") != "1" {
		return
	}
	codexHooks, codexReadError := platform.ReadTextFile(filepath.Join(platform.ProjectRoot(env, cwd), ".codex", "hooks.json"))
	codexProtected := codexReadError == nil && setuptext.CodexHooksProtected(codexHooks)
	piExtension, piReadError := platform.ReadTextFile(filepath.Join(platform.ProjectRoot(env, cwd), ".pi", "extensions", "soho-inbound.ts"))
	piProtected := piReadError == nil && setuptext.PiInboundProtected(piExtension)
	// A managed extension that differs from this soho's text (an older
	// release wrote it, or it was edited) is reported as such, not as a
	// missing hook: setup rewrites it either way.
	piStale := !piProtected && piReadError == nil && setuptext.PiInboundManaged(piExtension)
	var lines []string
	func() {
		defer func() {
			if value := recover(); value != nil {
				if _, ok := value.(*platform.ExitError); !ok {
					panic(value)
				}
			}
		}()
		agents := herdr.LiveAgents(env, herdr.Timeout)
		seen := map[string]bool{}
		for _, row := range core.RosterRows(core.StateDirPath(ctx, env, cwd)) {
			f := strings.Split(row, "\t")
			if len(f) < 3 || f[0] == "" || seen[f[0]] {
				continue
			}
			for _, agent := range agents {
				object, ok := agent.(*jsonjs.Object)
				if !ok {
					continue
				}
				name, _ := object.Get("name")
				pane, _ := object.Get("pane_id")
				if name == f[0] && (f[1] == "" || pane == f[1]) {
					seen[f[0]] = true
					if f[2] == "codex" {
						if !codexProtected {
							lines = append(lines, fmt.Sprintf("%s (codex) is not protected: no 'soho hook inbound --codex' in UserPromptSubmit (run 'soho setup')", f[0]))
						}
					} else if f[2] == "pi" {
						if piStale {
							lines = append(lines, fmt.Sprintf("%s (pi) may not be protected: .pi/extensions/soho-inbound.ts differs from this soho's inbound extension (run 'soho setup')", f[0]))
						} else if !piProtected {
							lines = append(lines, fmt.Sprintf("%s (pi) is not protected: no inbound hook for pi yet (run 'soho setup')", f[0]))
						}
					} else if f[2] != "claude" {
						lines = append(lines, fmt.Sprintf("%s (%s) is not protected: no inbound hook for %s yet", f[0], f[2], f[2]))
					}
					break
				}
			}
		}
	}()
	for _, line := range lines {
		s.Warning(line)
	}
}
