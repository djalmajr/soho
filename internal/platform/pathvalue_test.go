package platform

import (
	"runtime"
	"testing"
)

func TestPathValueFor(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			for _, tc := range []struct {
				name          string
				env           Env
				windows, unix string
			}{
				{"exact", Env{"PATH": "exact"}, "exact", "exact"},
				{"Path-only", Env{"Path": "mixed"}, "mixed", ""},
				{"path-only", Env{"path": "lower"}, "lower", ""},
				{"exact-wins", Env{"PATH": "exact", "Path": "mixed"}, "exact", "exact"},
				{"lexical-tie", Env{"Path": "mixed", "path": "lower"}, "mixed", ""},
				{"exact-empty-wins", Env{"PATH": "", "Path": "mixed"}, "", ""},
				{"lexical-empty-wins", Env{"Path": "", "path": "lower"}, "", ""},
				{"unrelated", Env{"PATH_SUFFIX": "other"}, "", ""},
				{"missing", Env{}, "", ""},
				{"nil", nil, "", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					want := tc.unix
					if goos == "windows" {
						want = tc.windows
					}
					if got := pathValueFor(tc.env, goos); got != want {
						t.Fatalf("goos=%s env=%v got=%q want=%q", goos, tc.env, got, want)
					}
				})
			}
		})
	}
}

func TestPathValue(t *testing.T) {
	if got := PathValue(Env{"PATH": "exact", "Path": "mixed"}); got != "exact" {
		t.Fatalf("native goos=%s exact PATH got=%q", runtime.GOOS, got)
	}
	want := ""
	if runtime.GOOS == "windows" {
		want = "mixed"
	}
	if got := PathValue(Env{"Path": "mixed"}); got != want {
		t.Fatalf("native goos=%s Path-only got=%q want=%q", runtime.GOOS, got, want)
	}
}
