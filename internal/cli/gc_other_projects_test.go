package cli

import (
	"os"
	"strings"
	"testing"
)

func TestGcOtherProjectsSummary(t *testing.T) {
	f := gcEmptyFixture(t)
	other1 := mkOldUnregCopy(t, f.tmp, "soho-mutation-a-other", 40)
	own := mkOldUnregCopy(t, f.tmp, "soho-mutation-b-own", 17)
	other2 := mkOldUnregCopy(t, f.tmp, "soho-mutation-c-other", 60)
	writeTestOriginMarker(t, other1, "/other/a/.git", "/other/a")
	writeTestOriginMarker(t, other2, "/other/b/.git", "/other/b")
	writeTestOriginMarker(t, own, gitCommonDirOf(t, f.repo, f.env), f.repo)
	code, out, errOut := f.run(t, f.env, "gc")
	want := "resource pressure: none (disk 60% free, swap 30% used)\n" + gcUnregisteredHeader + "\n" +
		own + "  17 B  3h  this project\n" +
		"2 unregistered copies of other projects (100 B) are not listed; 'soho gc --verbose' lists them\n" +
		"run 'soho gc --yes --include-unregistered' to remove the unregistered ones\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("code=%d stderr=%q\nstdout=%q\nwant=%q", code, errOut, out, want)
	}
	for _, path := range []string{own, other1, other2} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry run changed %s: %v", path, err)
		}
	}
}

func TestGcOtherProjectSingular(t *testing.T) {
	f := gcEmptyFixture(t)
	other := mkOldUnregCopy(t, f.tmp, "soho-mutation-other", 40)
	writeTestOriginMarker(t, other, "/other/.git", "/other")
	code, out, errOut := f.run(t, f.env, "gc")
	want := "resource pressure: none (disk 60% free, swap 30% used)\n" + gcUnregisteredHeader + "\n" +
		"1 unregistered copy of other projects (40 B) is not listed; 'soho gc --verbose' lists them\n" +
		"the unregistered copies belong to another project or have no origin marker; gc does not remove them (check each one and remove it by hand)\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("code=%d stderr=%q\nstdout=%q\nwant=%q", code, errOut, out, want)
	}
}

func TestGcOtherProjectsVerbose(t *testing.T) {
	for _, yes := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry", true: "yes"}[yes], func(t *testing.T) {
			f := gcEmptyFixture(t)
			other := mkOldUnregCopy(t, f.tmp, "soho-mutation-other", 40)
			own := mkOldUnregCopy(t, f.tmp, "soho-mutation-own", 17)
			writeTestOriginMarker(t, other, "/other/.git", "/other")
			writeTestOriginMarker(t, own, gitCommonDirOf(t, f.repo, f.env), f.repo)
			argv := []string{"gc", "--verbose"}
			if yes {
				argv = append(argv, "--yes")
			}
			code, out, errOut := f.run(t, f.env, argv...)
			wantRows := gcUnregisteredHeader + "\n" + other + "  40 B  3h  another project: /other\n" + own + "  17 B  3h  this project\n"
			if code != 0 || errOut != "" || !strings.Contains(out, wantRows) || strings.Contains(out, "are not listed;") {
				t.Fatalf("argv=%v code=%d stderr=%q stdout=%q; want rows %q and no summary", argv, code, errOut, out, wantRows)
			}
			for _, path := range []string{own, other} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("gc without --include-unregistered changed %s: %v", path, err)
				}
			}
		})
	}
}

func TestGcOtherProjectsYesSummary(t *testing.T) {
	f := gcEmptyFixture(t)
	other := mkOldUnregCopy(t, f.tmp, "soho-mutation-other", 40)
	writeTestOriginMarker(t, other, "/other/.git", "/other")
	code, out, errOut := f.run(t, f.env, "gc", "--yes")
	want := gcUnregisteredHeader + "\n1 unregistered copy of other projects (40 B) is not listed; 'soho gc --verbose' lists them\n"
	if code != 0 || errOut != "" || !strings.Contains(out, want) || strings.Contains(out, other) {
		t.Fatalf("code=%d stderr=%q stdout=%q; want %q and no other-project row", code, errOut, out, want)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("gc without --include-unregistered changed %s: %v", other, err)
	}
}

func TestGcOtherProjectsVerboseRemoval(t *testing.T) {
	f := gcEmptyFixture(t)
	other := mkOldUnregCopy(t, f.tmp, "soho-mutation-other", 40)
	own := mkOldUnregCopy(t, f.tmp, "soho-mutation-own", 17)
	writeTestOriginMarker(t, other, "/other/.git", "/other")
	writeTestOriginMarker(t, own, gitCommonDirOf(t, f.repo, f.env), f.repo)
	code, out, errOut := f.run(t, f.env, "gc", "--yes", "--include-unregistered", "--verbose")
	want := gcUnregisteredHeader + "\nkept " + other + " (another project: /other)\nremoved " + own + "  17 B\n"
	if code != 0 || errOut != "" || !strings.Contains(out, want) || strings.Contains(out, "are not listed;") {
		t.Fatalf("code=%d stderr=%q stdout=%q; want %q and no summary", code, errOut, out, want)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other project's copy changed: %v", err)
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatalf("this project's copy was not removed: %v", err)
	}
}

func TestGcOtherProjectsNowriteVerbose(t *testing.T) {
	nr := newNowriteReads(t, false)
	code, out, errOut := nr.runNowrite(t, "gc", "--verbose")
	if code != 0 || errOut != "" || !strings.Contains(out, "nothing to remove\n") {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, errOut, out)
	}
}
