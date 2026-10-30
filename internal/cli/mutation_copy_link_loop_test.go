package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// DJA-567: an explicit --link whose target is the copy, or an ancestor of the
// copy, passed the link validation and left the copy holding a link loop. It
// must be refused like the implicit links: the same wording, exit 2, and the
// created destination removed.
func TestMutationCopyExplicitLinkToCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows without developer mode")
	}
	for _, mode := range []string{"self", "ancestor"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			target := filepath.Join(f.root, "copy")
			if mode == "ancestor" {
				// An ancestor of the copy that contains neither the source nor
				// HOME, so only the copy-containment refusal can fire.
				target = filepath.Join(f.root, "outside")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			dest := target
			if mode == "ancestor" {
				dest = filepath.Join(target, "copy")
			}
			code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest, "--link", "deps="+target)
			if code != 2 || out != "" || !strings.Contains(errOut, "mutation-copy: refusing link 'deps="+target+"'") || !strings.Contains(errOut, "target contains the copy") {
				t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("the created destination was not removed for the refused link: %v", err)
			}
		})
	}
	t.Run("sibling stays accepted", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		sibling := filepath.Join(f.root, "sibling")
		if err := os.Mkdir(sibling, 0o700); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(f.root, "copy")
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest, "--link", "deps="+sibling)
		if code != 0 || !strings.Contains(out, `"path":"deps"`) || !strings.Contains(out, `"target":`) {
			t.Fatalf("the sibling link must stay accepted: code=%d out=%q err=%q", code, out, errOut)
		}
		if target, err := os.Readlink(filepath.Join(dest, "deps")); err != nil || target != sibling {
			t.Fatalf("deps target=%q err=%v; want %q", target, err, sibling)
		}
	})
}
