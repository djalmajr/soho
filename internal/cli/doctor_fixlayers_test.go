package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestDoctorFixDestinationLayers(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	user := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
	tm6Write(t, user, "panes=2\npane_mode=flex\nlane.review.panes=3\n")
	tm6Write(t, filepath.Join(cwd, ".agents", "soho.conf"), "pane_mode=strict\n")
	code, out, stderr := runIn(t, []string{"doctor", "--fix", "--user", "--panes", "2"}, env, cwd)
	data, err := os.ReadFile(user)
	if err != nil || code != 0 || !strings.Contains(string(data), "lane.review.panes=3\n") || !strings.Contains(string(data), "max_workers=5\n") {
		t.Fatalf("code=%d out=%q stderr=%q config=%q err=%v", code, out, stderr, data, err)
	}
}

func TestDoctorFixUserMigratesKindThroughDestination(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	user := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
	tm6Write(t, user, "panes=2\npane_mode=flex\nrole.reviewer.kind=claude\nrole.security-reviewer.kind=claude\nrole.ui-reviewer.kind=claude\nrole.inspector.kind=claude\nrole.validator.kind=claude\n")
	tm6Write(t, filepath.Join(cwd, ".agents", "soho.conf"), "pane_mode=strict\nlane.review.kind=codex\n")
	code, out, stderr := runIn(t, []string{"doctor", "--fix", "--user", "--panes", "2"}, env, cwd)
	data, err := os.ReadFile(user)
	if err != nil || code != 0 || !strings.Contains(string(data), "lane.review.kind=claude\n") {
		t.Fatalf("code=%d out=%q stderr=%q config=%q err=%v", code, out, stderr, data, err)
	}
}

func TestDoctorFixUserPreservesNormalizedLaneModel(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	user := filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config")
	input := "panes=2\npane_mode=flex\nlane-review-model=old\nlane_review_model=opus\n"
	for _, role := range []string{"reviewer", "security-reviewer", "ui-reviewer", "inspector", "validator"} {
		input += "role." + role + ".kind=claude\nrole." + role + ".model=sonnet\n"
	}
	tm6Write(t, user, input)
	tm6Write(t, filepath.Join(cwd, ".agents", "soho.conf"), "pane_mode=strict\nlane.review.kind=codex\n")
	code, out, stderr := runIn(t, []string{"doctor", "--fix", "--user", "--panes", "2"}, env, cwd)
	ctx := core.LoadConfigThrough(env, cwd, "user")
	if got := core.LaneAttr(&ctx, "review", "model", nil, env); code != 0 || got != "opus" {
		t.Fatalf("code=%d out=%q stderr=%q model=%q", code, out, stderr, got)
	}
}

func TestDoctorFixLocalUnlimited(t *testing.T) {
	for _, mode := range []string{"strict", "flex"} {
		for _, layout := range []string{"preset", "custom"} {
			for _, zero := range []string{"0", `"0"`, "0 # unlimited", "0# unlimited", "4\nmax_workers=0"} {
				t.Run(mode+"/"+layout+"/"+zero, func(t *testing.T) {
					env, cwd := tm6CLIEnv(t)
					conf := filepath.Join(cwd, ".agents", "soho.conf")
					panes := "2"
					input := "pane_mode=" + mode + "\nmax_workers=" + zero + "\n"
					if layout == "custom" {
						panes = "4"
						input += "lane.build.roles=implementer\nlane.build.panes=2\n"
					}
					input += "panes=" + panes + "\n"
					tm6Write(t, conf, input)
					code, out, stderr := runIn(t, []string{"doctor", "--fix", "--panes", panes}, env, cwd)
					data, err := os.ReadFile(conf)
					want := "split_max_panes=2\n"
					if mode == "flex" {
						want = "split_max_panes=3\n"
					}
					if layout == "custom" {
						want = "split_max_panes=4\n"
						if mode == "flex" {
							want = "split_max_panes=5\n"
						}
					}
					if err != nil || code != 0 || !strings.Contains(string(data), "max_workers="+zero+"\n") || !strings.Contains(string(data), want) {
						t.Fatalf("code=%d out=%q stderr=%q config=%q err=%v", code, out, stderr, data, err)
					}
				})
			}
		}
	}
}

func TestDoctorFixUnlimitedSplitReport(t *testing.T) {
	for _, old := range []string{"4", "3", ""} {
		t.Run("old="+old, func(t *testing.T) {
			env, cwd := tm6CLIEnv(t)
			conf := filepath.Join(cwd, ".agents", "soho.conf")
			input := "panes=4\npane_mode=strict\nmax_workers=0\nreuse_workers=on\n"
			if old != "" {
				input += "split_max_panes=" + old + "\n"
			}
			tm6Write(t, conf, input)
			code, out, stderr := runIn(t, []string{"doctor", "--fix", "--panes", "4"}, env, cwd)
			if code != 0 || strings.Contains(out, "removed split_max_panes=") || strings.Contains(out, "set split_max_panes=4") != (old != "4") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			code, out, stderr = runIn(t, []string{"doctor", "--fix", "--panes", "4"}, env, cwd)
			if code != 0 || !strings.Contains(out, "no changes in ") || strings.Contains(out, "removed split_max_panes=") || strings.Contains(out, "set split_max_panes=") {
				t.Fatalf("rerun code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

func TestDoctorFixInheritedUnlimited(t *testing.T) {
	env, cwd := tm6CLIEnv(t)
	tm6Write(t, filepath.Join(env.Get("XDG_CONFIG_HOME"), "soho", "config"), "max_workers=0\n")
	conf := filepath.Join(cwd, ".agents", "soho.conf")
	tm6Write(t, conf, "")
	code, out, stderr := runIn(t, []string{"doctor", "--fix", "--panes", "4"}, env, cwd)
	data, err := os.ReadFile(conf)
	if err != nil || code != 0 || strings.Contains(string(data), "max_workers=") {
		t.Fatalf("code=%d out=%q stderr=%q config=%q err=%v", code, out, stderr, data, err)
	}
}
