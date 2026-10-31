package cli

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// TestCapabilitiesReportsTheConfiguredTransport pins the transport field of
// capabilities --json (DJA-603): herdr by default and for an invalid value,
// hc when the environment or the project file selects it, with the fixed
// fields unchanged. No case has a skill directory, and the one without
// SOHO_NOWRITE checks that nothing was written: capabilities must neither
// fail without a skill nor materialize the bundled one.
func TestCapabilitiesReportsTheConfiguredTransport(t *testing.T) {
	fixed := `{"schema":1,"worker_collaboration":1,"ephemeral_job":1,"job_events":1,"inventory":1,`
	for _, tc := range []struct {
		name, env, file, want string
		nowrite               bool
	}{
		{name: "absent", want: "herdr", nowrite: true},
		{name: "environment hc", env: "hc", want: "hc", nowrite: true},
		{name: "invalid value", env: "other", want: "herdr", nowrite: true},
		{name: "project file hc", file: "transport=hc\n", want: "hc", nowrite: true},
		{name: "absent, writes nothing", want: "herdr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.file != "" {
				if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".agents", "soho.conf"), []byte(tc.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(root)
			env := platform.Env{"HOME": root, "USERPROFILE": root, "SOHO_DIR": filepath.Join(root, "state"), "TMPDIR": root}
			if tc.nowrite {
				env["SOHO_NOWRITE"] = "1"
			}
			if tc.env != "" {
				env["SOHO_TRANSPORT"] = tc.env
			}
			before := capabilitiesTree(t, root)
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			code := Run([]string{"capabilities", "--json"}, env)
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if want := fixed + `"transport":"` + tc.want + "\"}\n"; code != 0 || out.String() != want || stderr.Len() != 0 {
				t.Fatalf("code=%d out=%q stderr=%q, want %q", code, out.String(), stderr.String(), want)
			}
			if after := capabilitiesTree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("capabilities wrote files: before %v, after %v", before, after)
			}
		})
	}
}

// capabilitiesTree lists every path under root, relative to it.
func capabilitiesTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}
