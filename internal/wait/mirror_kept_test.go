package wait

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMirrorKeptOncePerDestinationAndContent(t *testing.T) {
	ctx, env, sd, _, stem := privateMirror(t)
	writeFiles(t, map[string]string{stem + ".md": "first\n", stem + ".brief.md": "brief\n", stem + ".dispatch.json": "{}\n"})
	stderr := captureStderr(t)
	mirror := func() { mirrorReport(sd, "worker", stem+".md", ctx, env, "") }
	mirror()
	dst := filepath.Join(sd, "reports", filepath.Base(stem)+".md")
	writeFiles(t, map[string]string{dst: "operator copy\n"})
	want := "kept " + dst + ": it differs from " + stem + ".md, which was not copied over it; the state dir keeps its first copy. To take the new one, compare the two and delete " + dst + ": the next wait or status copies it again"
	for _, body := range []string{"first\n", "second\n", "first\n"} {
		writeFiles(t, map[string]string{stem + ".md": body})
		mirror()
		mirror()
	}
	if n := strings.Count(stderr.String(), want); n != 2 {
		t.Fatalf("warning count=%d, want 2; stderr=%q", n, stderr.String())
	}
	marker := filepath.Join(sd, "wait", "worker.mirror-kept")
	data, err := os.ReadFile(marker)
	wantMarker := fmt.Sprintf("%s\t%x\n%s\t%x\n", dst, sha256.Sum256([]byte("first\n")), dst, sha256.Sum256([]byte("second\n")))
	if err != nil || string(data) != wantMarker {
		t.Fatalf("marker=%q err=%v want=%q", data, err, wantMarker)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "operator copy\n" {
		t.Fatalf("destination overwritten: %q err=%v", got, err)
	}
	stderr.Reset()
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	mirror()
	mirror()
	if got, err := os.ReadFile(dst); err != nil || string(got) != "first\n" || stderr.Len() != 0 {
		t.Fatalf("recopy=%q err=%v stderr=%q", got, err, stderr.String())
	}
	// Equal bytes at a different destination must still warn independently.
	briefDst := filepath.Join(sd, "briefs", filepath.Base(stem)+".md")
	writeFiles(t, map[string]string{stem + ".brief.md": "first\n", briefDst: "operator brief\n"})
	mirror()
	mirror()
	if strings.Count(stderr.String(), "kept "+briefDst+":") != 1 {
		t.Fatalf("second destination stderr=%q", stderr.String())
	}
	t.Log("same pair warns once; changed source warns once more; prior content stays suppressed; destination preserved; deleted destination recopied silently; identical destination silent; second destination independent")
}

func TestMirrorKeptNowrite(t *testing.T) {
	ctx, env, sd, _, stem := privateMirror(t)
	writeFiles(t, map[string]string{stem + ".md": "report\n", stem + ".brief.md": "brief\n"})
	env["SOHO_NOWRITE"] = "1"
	stderr := captureStderr(t)
	mirrorReport(sd, "worker", stem+".md", ctx, env, "")
	if _, err := os.Stat(sd); !os.IsNotExist(err) || stderr.Len() != 0 {
		t.Fatalf("Nowrite state err=%v stderr=%q", err, stderr.String())
	}
}

func TestMirrorKeptMarkerWriteFailureStaysBestEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory mode does not deny file creation on Windows")
	}
	ctx, env, sd, _, stem := privateMirror(t)
	writeFiles(t, map[string]string{stem + ".md": "report\n", stem + ".brief.md": "brief\n"})
	mirrorReport(sd, "worker", stem+".md", ctx, env, "")
	dst := filepath.Join(sd, "reports", filepath.Base(stem)+".md")
	writeFiles(t, map[string]string{dst: "operator copy\n"})
	waitDir := filepath.Join(sd, "wait")
	if err := os.MkdirAll(waitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(waitDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(waitDir, 0o700) })
	if f, err := os.OpenFile(filepath.Join(waitDir, "probe"), os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Close()
		t.Skip("this user can still create files in a 0500 directory")
	}
	stderr := captureStderr(t)
	for i := 0; i < 2; i++ {
		mirrorReport(sd, "worker", stem+".md", ctx, env, "")
	}
	if n := strings.Count(stderr.String(), "kept "+dst+":"); n != 2 {
		t.Fatalf("warning count=%d, want 2 with no marker; stderr=%q", n, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(waitDir, "worker.mirror-kept")); !os.IsNotExist(err) {
		t.Fatalf("marker err=%v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "operator copy\n" {
		t.Fatalf("destination=%q err=%v", got, err)
	}
}
