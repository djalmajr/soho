package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
	"github.com/djalmajr/soho/internal/transport"
)

// doctorPeerInbound checks the receiver side of peer messages: that every
// agent that could receive one has a UserPromptSubmit hook, and that the
// signed peer settings of the previous design are gone. The hook reads no key
// and checks no signature, so there is nothing else to check.
func doctorPeerInbound(ctx *core.Config, env platform.Env, cwd string, s *Say) {
	// The signed peer settings are retired: a file that still sets one is
	// no longer read by anything.
	for _, layer := range ctx.Layers {
		for _, key := range []string{"inbound_auth", "inbound_allow"} {
			if _, present := layer.Entries[key]; present {
				s.Warning(fmt.Sprintf("config: %s is retired (%s); delete the %s line from %s", key, layer.Source, key, layer.File))
			}
		}
	}
	userDir := filepath.Dir(core.EffectiveUserConfigFile(platform.Current(), env, core.UserSelection{}))
	if info, err := os.Stat(filepath.Join(userDir, "peer-keys")); err == nil && info.IsDir() {
		s.Warning("peer keys in " + filepath.Join(userDir, "peer-keys") + " are no longer used; delete the directory")
	}
	if _, err := os.Stat(filepath.Join(userDir, "peer-inbound-seen.tsv")); err == nil {
		s.Warning("peer replay cache " + filepath.Join(userDir, "peer-inbound-seen.tsv") + " is no longer used; delete it")
	}
	settings := filepath.Join(platform.ProjectRoot(env, cwd), ".claude", "settings.json")
	localSettings := filepath.Join(platform.ProjectRoot(env, cwd), ".claude", "settings.local.json")
	if !userPromptSubmitProtected(settings) && !userPromptSubmitProtected(localSettings) {
		s.Warning(fmt.Sprintf("%s and %s have no 'soho hook reminder' or 'soho hook inbound' in UserPromptSubmit: the Claude Code panes of this project do not check peer messages (run 'soho setup')", settings, localSettings))
	}
	// The hook is the protection: every live agent without a UserPromptSubmit
	// hook receives a peer message as-is. This check no longer depends on a
	// setting; it runs whenever the doctor runs in Herdr.
	doctorUnprotectedAgents(ctx, env, cwd, s)
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
	// One backend for this invocation of the probe (DJA-596).
	svc := herdr.New(env)
	if !svc.Runtime.Caller().Inside {
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
		rep := svc.Legacy.AgentListRaw(context.Background(), transport.Call{Timeout: herdr.Timeout})
		seen := map[string]bool{}
		for _, row := range core.RosterRows(core.StateDirPath(ctx, env, cwd)) {
			f := strings.Split(row, "\t")
			if len(f) < 3 || f[0] == "" || seen[f[0]] {
				continue
			}
			for _, rawAgent := range rep.Value {
				parsed, err := jsonjs.Parse([]byte(rawAgent))
				object, ok := parsed.(*jsonjs.Object)
				if err != nil || !ok {
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
