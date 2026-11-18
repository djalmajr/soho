package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestDoctorFixLocalUnlimited(t *testing.T) {
	for _, mode := range []string{"strict", "flex"} {
		for _, layout := range []string{"preset", "custom"} {
			for _, zero := range []string{"0", `"0"`, "0 # unlimited", "0# unlimited", "4\nmax_workers=0"} {
				t.Run(mode+"/"+layout+"/"+zero, func(t *testing.T) {
					env, cwd := tm6CLIEnv(t)
					conf := filepath.Join(cwd, ".agents", "soho.conf")
					input := "panes=2\npane_mode=" + mode + "\nmax_workers=" + zero + "\n"
					if layout == "custom" {
						input += "lane.build.roles=implementer\nlane.build.panes=1\n"
					}
					tm6Write(t, conf, input)
					code, out, stderr := runIn(t, []string{"doctor", "--fix", "--panes", "2"}, env, cwd)
					data, err := os.ReadFile(conf)
					want := "split_max_panes=2\n"
					if mode == "flex" {
						want = "split_max_panes=3\n"
					}
					if err != nil || code != 0 || !strings.Contains(string(data), "max_workers="+zero+"\n") || !strings.Contains(string(data), want) {
						t.Fatalf("code=%d out=%q stderr=%q config=%q err=%v", code, out, stderr, data, err)
					}
				})
			}
		}
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
