package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func gcUnchangedVolume(path string) string {
	return "volume free: 60.0 GB -> 60.0 GB (+0 B) on " + filepath.Dir(path)
}

func TestGcVolumeFree(t *testing.T) {
	for _, tc := range []struct {
		name                                                      string
		devices                                                   int
		delta                                                     int64
		failBefore, failAfter, failDevice, noCopies, missing, dry bool
	}{
		{name: "one device before all removals and after all removals", devices: 1, delta: 6 << 30},
		{name: "two devices", devices: 2, delta: 6 << 30},
		{name: "negative difference", devices: 1, delta: -6 << 30},
		{name: "before fails", devices: 1, failBefore: true},
		{name: "after fails", devices: 1, failAfter: true},
		{name: "device fails", devices: 1, failDevice: true},
		{name: "no removals", noCopies: true},
		{name: "missing registry entry only", noCopies: true, missing: true},
		{name: "dry run unchanged", devices: 1, dry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := gcEmptyFixture(t)
			paths := []string{}
			rows := []core.CopyRow{}
			devices := map[string]string{}
			for i := 0; i < 3 && !tc.noCopies; i++ {
				path := filepath.Join(f.tmp, fmt.Sprintf("parent-%d", i), "copy")
				writeFileAt(t, filepath.Join(path, "data"), "payload", platform.Now().Add(-3*time.Hour))
				paths = append(paths, path)
				devices[path] = fmt.Sprintf("device-%d", i%tc.devices)
				rows = append(rows, core.CopyRow{Path: path, Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour)), Source: "-", Origin: "add"})
			}
			if tc.missing {
				rows = append(rows, core.CopyRow{Path: filepath.Join(f.tmp, "missing"), Owner: "ghost", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour))})
			}
			f.writeCopies(t, rows)
			oldDevice, oldDisk := platform.DiskDevice, platform.DiskFree
			t.Cleanup(func() { platform.DiskDevice, platform.DiskFree = oldDevice, oldDisk })
			platform.DiskDevice = func(path string) (string, bool) { return devices[path], !tc.failDevice }
			calls := map[string]int{}
			platform.DiskFree = func(path string) (int64, int64, bool) {
				if path == f.stateDir(t) {
					return 60 << 30, 100 << 30, true // the existing pressure read
				}
				device := devices[filepath.Join(path, "copy")]
				call := calls[device]
				calls[device]++
				if !tc.failDevice {
					for _, copy := range paths {
						_, err := os.Stat(copy)
						if (call == 0 && err != nil) || (call == 1 && !os.IsNotExist(err)) {
							t.Fatalf("sample=%d device=%s path=%s copy=%s stat=%v: before must precede every removal; after must follow every removal", call, device, path, copy, err)
						}
					}
				}
				if call == 0 && !tc.failDevice {
					return 100 << 30, 200 << 30, !tc.failBefore
				}
				return (100 << 30) + tc.delta, 200 << 30, !tc.failAfter
			}
			argv := []string{"gc", "--yes"}
			if tc.dry {
				argv = []string{"gc"}
			}
			code, out, errOut := f.run(t, f.env, argv...)
			if code != 0 || errOut != "" {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
			if tc.noCopies || tc.dry {
				if strings.Contains(out, "volume free:") || len(calls) != 0 {
					t.Fatalf("unexpected volume sample or output: calls=%v out=%q", calls, out)
				}
				// A missing line alone leaves nothing on disk: the freed
				// line says so and points at no volume line.
				if tc.missing && (!strings.Contains(out, "freed: 0 B (the copies were already gone from disk)\n") || strings.Contains(out, "see volume free below")) {
					t.Fatalf("freed line for a missing entry only: %q", out)
				}
				return
			}
			count := tc.devices
			if tc.failDevice {
				count = len(paths)
			}
			if strings.Count(out, "volume free:") != count {
				t.Fatalf("volume count=%d want=%d out=%q", strings.Count(out, "volume free:"), count, out)
			}
			wantFreed := "freed: 21 B (logical size; see volume free below)\n"
			index := strings.Index(out, wantFreed)
			if index < 0 || !strings.HasPrefix(out[index+len(wantFreed):], "volume free:") {
				t.Fatalf("volume lines must follow freed: %q", out)
			}
			for i := 0; i < count; i++ {
				parent := filepath.Dir(paths[i])
				want := "volume free: not measured on " + parent + "\n"
				if !tc.failBefore && !tc.failAfter && !tc.failDevice {
					sign, delta := "+", tc.delta
					if delta < 0 {
						sign, delta = "-", -delta
					}
					want = fmt.Sprintf("volume free: 100.0 GB -> %s (%s%s) on %s\n", humanSize((100<<30)+tc.delta), sign, humanSize(delta), parent)
				}
				if !strings.Contains(out, want) {
					t.Fatalf("missing %q in %q", want, out)
				}
			}
			if !tc.failDevice {
				for device, count := range calls {
					if count != 2 {
						t.Fatalf("%s sampled %d times; want 2", device, count)
					}
				}
			}
		})
	}
}
