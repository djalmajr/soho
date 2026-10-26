package setup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

// UnifiedDiff renders the setup plan's three-context, labeled unified diff.
func UnifiedDiff(before, after, label string) string {
	if before == after {
		return ""
	}
	a, b := diffLines(before), diffLines(after)
	n, m := len(a), len(b)
	// LCS ties prefer insertion, matching the reference's Myers tie rule for the plan fixtures.
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = 1 + dp[i+1][j+1]
			} else if dp[i+1][j] > dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n || j < m {
		if i < n && j < m && a[i] == b[j] {
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		} else if i < n && (j == m || dp[i+1][j] >= dp[i][j+1]) {
			ops = append(ops, diffOp{'-', a[i]})
			i++
		} else {
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	changes := []int{}
	for x, o := range ops {
		if o.kind != ' ' {
			changes = append(changes, x)
		}
	}
	if len(changes) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", label, label)
	start := 0
	for start < len(changes) {
		first := changes[start]
		lo := max(0, first-3)
		hi := min(len(ops), first+4)
		end := start + 1
		for end < len(changes) && changes[end]-hi < 3 {
			hi = min(len(ops), changes[end]+4)
			end++
		}
		old0, new0 := 0, 0
		for x := 0; x < lo; x++ {
			if ops[x].kind != '+' {
				old0++
			}
			if ops[x].kind != '-' {
				new0++
			}
		}
		oldCount, newCount := 0, 0
		for x := lo; x < hi; x++ {
			if ops[x].kind != '+' {
				oldCount++
			}
			if ops[x].kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(old0, oldCount), hunkRange(new0, newCount))
		for _, o := range ops[lo:hi] {
			out.WriteByte(o.kind)
			out.WriteString(o.line.text)
			if o.line.nl {
				out.WriteByte('\n')
			} else {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
		start = end
	}
	return out.String()
}

type diffOp struct {
	kind byte
	line planLine
}

type planLine struct {
	text string
	nl   bool
}

func diffLines(s string) []planLine {
	if s == "" {
		return nil
	}
	ended := strings.HasSuffix(s, "\n")
	if ended {
		s = strings.TrimSuffix(s, "\n")
	}
	parts := strings.Split(s, "\n")
	out := make([]planLine, 0, len(parts))
	for i, p := range parts {
		out = append(out, planLine{text: p, nl: ended || i < len(parts)-1})
	}
	return out
}
func hunkRange(start, count int) string {
	if count == 0 {
		return "0,0"
	}
	if count == 1 {
		return fmt.Sprint(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}

var planCommentRE = regexp.MustCompile(`[ \t]#.*$`)

func confKeys(content string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		line = planCommentRE.ReplaceAllString(line, "")
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		i := strings.IndexByte(s, '=')
		if i < 0 {
			continue
		}
		k := strings.TrimSpace(s[:i])
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}
func valueFor(content, key string) string {
	v := ""
	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		line = planCommentRE.ReplaceAllString(line, "")
		s := strings.TrimSpace(line)
		i := strings.IndexByte(s, '=')
		if i >= 0 && strings.TrimSpace(s[:i]) == key {
			v = strings.TrimSpace(s[i+1:])
		}
	}
	return v
}
func planConfigDiff(before, after string) string {
	keys := append(confKeys(before), confKeys(after)...)
	seen := map[string]bool{}
	var out strings.Builder
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		b, a := valueFor(before, k), valueFor(after, k)
		if b == a {
			continue
		}
		if b == "" {
			b = "(unset)"
		}
		if a == "" {
			a = "(removed)"
		}
		fmt.Fprintf(&out, "  %-20s %s → %s\n", k, b, a)
	}
	return out.String()
}
func planFileDiff(file, before, after string) string {
	d := UnifiedDiff(before, after, file)
	if d == "" {
		d = "  (no change)\n"
	}
	return file + "\n" + d + "\n"
}
func readOrEmpty(file string) string {
	s, e := platform.ReadTextFile(file)
	if e != nil {
		return ""
	}
	return s
}
func writePair(raw, key, value string) string {
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	out := make([]string, 0, len(lines)+1)
	found := false
	for _, line := range lines {
		without := planCommentRE.ReplaceAllString(line, "")
		i := strings.IndexByte(strings.TrimSpace(without), '=')
		if i >= 0 && strings.TrimSpace(strings.TrimSpace(without)[:i]) == key {
			if !found {
				comment := ""
				cut := len(without)
				if cut < len(line) {
					comment = line[cut:]
				}
				out = append(out, key+"="+value+comment)
			}
			found = true
		} else {
			out = append(out, line)
		}
	}
	if !found {
		out = append(out, key+"="+value)
	}
	return strings.Join(out, "\n") + "\n"
}

// CmdPlan simulates config/session writes in memory and emits the instruction/hook diffs; it never mutates the checkout.
func CmdPlan(args []string, ctx *core.Config, env platform.Env, cwd string) {
	// The plan shows what the migration would move, without moving it.
	// Inside the skill the previews never run: they would call herdr, and
	// the plan's own writes keep the refusal that follows.
	if core.StatePathInSkill(env, platform.ProjectRoot(env, cwd)) == "" {
		for _, line := range migrateNames(ctx, env, cwd, true) {
			_, _ = fmt.Fprintln(platform.Stdout, line)
		}
		for _, line := range migratePlugin(env, BuildVersion, true) {
			_, _ = fmt.Fprintln(platform.Stdout, line)
		}
	}
	var panes, target string
	local, hooks := false, true
	var proj, user, sess [][2]string
	var laneSpecs []core.LaneSpec
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--panes", "--lane", "--target":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				platform.DieFriction("setup --plan: "+a+" expects a value", 2)
			}
			i++
			if a == "--panes" {
				panes = args[i]
			} else if a == "--target" {
				target = args[i]
			} else {
				spec, err := core.SetupLaneSpec(args[i])
				if err != nil {
					panic(err)
				}
				laneSpecs = append(laneSpecs, spec)
			}
		case "--set", "--user-set", "--session-set":
			if i+2 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				platform.DieFriction("setup --plan: "+a+" expects a value", 2)
			}
			k, v := args[i+1], args[i+2]
			i += 2
			if !core.ConfigKeyOk(k) {
				platform.DieFriction(fmt.Sprintf("setup --plan: unknown key '%s'", k), 2)
			}
			if !core.ConfigValueOk(k, v, env, cwd) {
				platform.DieFriction(fmt.Sprintf("setup --plan: invalid value '%s' for %s", v, k), 2)
			}
			switch a {
			case "--set":
				proj = append(proj, [2]string{k, v})
			case "--user-set":
				user = append(user, [2]string{k, v})
			default:
				sess = append(sess, [2]string{k, v})
			}
		case "--no-hooks":
			hooks = false
		case "--local":
			local = true
		default:
			platform.DieFriction(fmt.Sprintf("setup --plan: unknown option '%s'", a), 2)
		}
	}
	if panes != "" && panes != "2" && panes != "3" && panes != "4" {
		platform.DieFriction("setup --plan: --panes must be 2, 3 or 4", 2)
	}
	root := platform.ProjectRoot(env, cwd)
	projectFile := core.ConfigFileFor("project", env, cwd)
	userFile := core.ConfigFileFor("user", env, cwd)
	sessionFile := core.SessionConfPath(ctx, env, cwd)
	if len(sess) > 0 && sessionFile == "" {
		platform.DieFriction("setup --plan: --session-set needs a Herdr workspace (none resolvable here)", 2)
	}
	modeLocal := resolveSetupMode(local, target, ctx, env, "setup --plan")
	var localPlan *localExcludePlan
	if modeLocal {
		refuseUnignorableStateDir(root, ctx, env, cwd, "setup --plan")
		refuseTrackedLocal(root, env, "setup --plan")
		localPlan = planLocalExcludes(root, ctx, env, cwd, "setup --plan")
	}
	if target == "" && modeLocal {
		target = filepath.Join(root, localInstructionFile)
	}
	if target == "" {
		for _, p := range []string{filepath.Join(root, "AGENTS.md"), filepath.Join(root, "CLAUDE.md")} {
			s := readOrEmpty(p)
			if strings.Contains(s, setuptext.SetupStart) || strings.Contains(s, setuptext.LegacySetupStart) {
				target = p
				break
			}
		}
		if target == "" {
			agents := filepath.Join(root, "AGENTS.md")
			claude := filepath.Join(root, "CLAUDE.md")
			if info, err := os.Stat(agents); err == nil && info.Mode().IsRegular() {
				target = agents
			} else if info, err := os.Stat(claude); err == nil && info.Mode().IsRegular() && !setupIsSymlink(claude) {
				target = claude
			} else {
				target = agents
			}
		}
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	tmpBase := env.Get("TMPDIR")
	if tmpBase == "" {
		tmpBase = os.TempDir()
	}
	tmpd, err := os.MkdirTemp(tmpBase, "soho-plan.")
	if err != nil {
		platform.DieFriction("setup --plan: could not create simulation directory", 4)
	}
	defer os.RemoveAll(tmpd)
	sections := []struct {
		file, source string
		pairs        [][2]string
		touched      bool
	}{{file: projectFile, source: projectFile, pairs: proj, touched: panes != "" || len(laneSpecs) > 0 || len(proj) > 0}, {file: userFile, source: userFile, pairs: user, touched: len(user) > 0}, {file: sessionFile, source: sessionFile, pairs: sess, touched: len(sess) > 0}}
	sectionAfter := make([]string, len(sections))
	tempNames := []string{"proj.conf", "user.conf", "session.conf"}
	for i, section := range sections {
		if !section.touched {
			continue
		}
		tmp := filepath.Join(tmpd, tempNames[i])
		before := readOrEmpty(section.source)
		if err = os.WriteFile(tmp, []byte(before), 0600); err != nil {
			platform.DieFriction("setup --plan: could not seed simulation file", 4)
		}
		if i == 0 && panes != "" {
			core.ApplyLaneFile(tmp, panes, env, cwd)
		}
		if i == 0 {
			for _, s := range laneSpecs {
				core.ConfigWritePair(tmp, "lane."+s.Name+".kind", s.Kind, env, cwd)
				if s.Model != "" {
					core.ConfigWritePair(tmp, "lane."+s.Name+".model", s.Model, env, cwd)
				}
				if s.Effort != "" {
					core.ConfigWritePair(tmp, "lane."+s.Name+".effort", s.Effort, env, cwd)
				}
			}
		}
		for _, p := range section.pairs {
			core.ConfigWritePair(tmp, p[0], p[1], env, cwd)
		}
		sectionAfter[i] = readOrEmpty(tmp)
	}
	var instrBefore *string
	instrRaw, e := platform.ReadTextFile(target)
	if e == nil {
		instrBefore = &instrRaw
	}
	instrAfter := setuptext.SetupBlockResult(instrBefore)
	if instrAfter == nil {
		platform.DieFriction(fmt.Sprintf("setup --plan: could not produce the instruction block for %s (setup would refuse it and leave the file untouched)", target), 4)
	}
	settingsFile := claudeSettingsPath(root, modeLocal)
	var settingsBefore *string
	settingsRaw, e := platform.ReadTextFile(settingsFile)
	if e == nil {
		settingsBefore = &settingsRaw
	}
	var settingsAfter *string
	settingsFailed := false
	if hooks {
		settingsAfter = setuptext.SettingsHooksResult(settingsBefore)
		if settingsAfter == nil {
			settingsFailed = true
		}
	}
	_, _ = io.WriteString(platform.Stdout, "plan (nothing is written):\n\n")
	for i, section := range sections {
		if !section.touched {
			continue
		}
		_, _ = io.WriteString(platform.Stdout, section.file+"\n"+planConfigDiff(readOrEmpty(section.file), sectionAfter[i])+"\n")
	}
	_, _ = io.WriteString(platform.Stdout, planFileDiff(target, stringValue(instrBefore), *instrAfter))
	if settingsFailed {
		platform.DieFriction("setup --plan: could not merge hooks into "+settingsFile+" (setup would refuse it and leave the file untouched)", 4)
	}
	if hooks {
		_, _ = io.WriteString(platform.Stdout, planFileDiff(settingsFile, stringValue(settingsBefore), *settingsAfter))
		if modeLocal {
			shared, after := sharedHooksMigration(root)
			if after != nil {
				if warning := sharedHooksTrackingWarning(trackingStatus(root, ".claude/settings.json", env)); warning != "" {
					_, _ = io.WriteString(platform.Stdout, "# "+warning+"\n")
				} else {
					_, _ = io.WriteString(platform.Stdout, planFileDiff(shared, readOrEmpty(shared), *after))
				}
			}
		}
	}
	if modeLocal {
		if localPlan != nil && localPlan.path != "" && localPlan.after != localPlan.before {
			_, _ = io.WriteString(platform.Stdout, planFileDiff(localPlan.path, localPlan.before, localPlan.after))
		}
	} else {
		state := core.StateRootPath(ctx, env, cwd)
		rel := core.StateGitignoreRel(root, state)
		if rel != "" {
			cmd := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
			cmd.Env = env.List()
			if cmd.Run() == nil && core.GitignoreNeeds(root, rel, env) {
				file := filepath.Join(root, ".gitignore")
				before := readOrEmpty(file)
				_, _ = io.WriteString(platform.Stdout, planFileDiff(file, before, core.GitignoreAfter(before, rel)))
			}
		}
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
