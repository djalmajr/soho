package main

// Built-in rule tokens in names, paths and error channels: with or
// without a deny file, a name that matches a built-in content rule is
// detected, printed under its ordinal and masked out of every output
// and error line. The canaries are synthetic tokens generated at run
// time (tok* helpers); no valid-format token literal lives in this
// file.

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNameTokenContextName covers the context walk without a deny file:
// a canary file name with neutral content gives exit 1 and a @name
// finding under the ordinal; the same name with matching content keeps
// the content finding's offset, length and sha. The canary never
// reaches stdout or stderr.
func TestNameTokenContextName(t *testing.T) {
	t.Run("neutral-content", func(t *testing.T) {
		tok := tokGitHub()
		dir := t.TempDir()
		writeContextFile(t, dir, "src/"+tok+".txt", "hello\n")
		code, out, errb := runContextCLI(t, dir, "--allow", "src")
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "finding\tgithub-token\tentry#1\t@name") {
			t.Fatalf("@name finding missing in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("matching-content", func(t *testing.T) {
		tok := tokNPM()
		content := "k=" + tok + "\n"
		dir := t.TempDir()
		writeContextFile(t, dir, "src/"+tok+".txt", content)
		code, out, errb := runContextCLI(t, dir, "--allow", "src")
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "finding\tnpm-token\tentry#1\t@name") {
			t.Fatalf("@name finding missing in:\n%s", out)
		}
		want := contentLine("finding", "npm-token", "entry#1", 2, len(tok), sumOf(content))
		if !hasLine(out, want) {
			t.Fatalf("content finding under the ordinal missing in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
}

// TestNameTokenImageMemberRawName: a layer member whose raw name holds
// a canary is found at @name, printed under the ordinal and never
// printed with the canary.
func TestNameTokenImageMemberRawName(t *testing.T) {
	tok := tokGitHub()
	dir := t.TempDir()
	archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
		{
			{h: &tar.Header{Name: "app/" + tok + ".bin", Mode: 0o644}, body: []byte("x")},
		},
	}, nil, nil)
	code, out, errb := runImageCLI(t, archive)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
	}
	if !hasLine(out, "finding\tgithub-token\tlayer0:entry#0\t@name") {
		t.Fatalf("@name finding missing in:\n%s", out)
	}
	assertNoToken(t, out, errb, tok)
}

// TestNameTokenImageMemberNormalizedOnly: a synthetic PEM in the member
// name whose body run only forms after normalizeArchiveName collapses
// the "/./" segment: the raw name does not match private-key, the
// normalized name does. The member is found at @name and printed under
// the ordinal; the PEM never reaches the output.
func TestNameTokenImageMemberNormalizedOnly(t *testing.T) {
	raw := "-----BEGIN PRIVATE KEY-----\n" +
		strings.Repeat("A", 30) + "/./" + strings.Repeat("B", 33)
	dir := t.TempDir()
	archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
		{
			{h: &tar.Header{Name: raw, Mode: 0o644}, body: []byte("x")},
		},
	}, nil, nil)
	code, out, errb := runImageCLI(t, archive)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
	}
	if !hasLine(out, "finding\tprivate-key\tlayer0:entry#0\t@name") {
		t.Fatalf("@name finding missing in:\n%s", out)
	}
	assertNoLiteral(t, out, errb, "-----BEGIN PRIVATE KEY-----")
}

// TestNameTokenImageIndexFileName: an archive index file whose name
// holds a canary is found at @name under its ordinal and never printed
// with the canary.
func TestNameTokenImageIndexFileName(t *testing.T) {
	tok := tokXAI()
	dir := t.TempDir()
	archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, nil, nil,
		[]outerFile{{Name: "extra/" + tok, Data: []byte("{}")}})
	code, out, errb := runImageCLI(t, archive)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
	}
	if !hasLine(out, "finding\txai-key\tarchive:entry#1\t@name") {
		t.Fatalf("@name finding missing in:\n%s", out)
	}
	assertNoToken(t, out, errb, tok)
}

// TestNameTokenAdjacentChannels covers the channels beside the entry
// name, without a deny file: link targets (raw and resolved), owner
// names, PAX values and the secret-looking environment variable name in
// the config. The finding is present and the canary never reaches the
// output.
func TestNameTokenAdjacentChannelsNoDeny(t *testing.T) {
	t.Run("link-raw", func(t *testing.T) {
		tok := tokXAI()
		dir := t.TempDir()
		archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
			{
				{h: &tar.Header{Name: "app/link", Mode: 0o777, Typeflag: tar.TypeSymlink,
					Linkname: "/secrets/" + tok}},
			},
		}, nil, nil)
		code, out, errb := runImageCLI(t, archive)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "finding\txai-key\tlayer0:app/link\t@link") {
			t.Fatalf("@link finding missing in:\n%s", out)
		}
		// The raw and the resolved target both match the same rule at
		// the same display and field: addMeta keeps one finding.
		if n := strings.Count(out, "@link"); n != 1 {
			t.Fatalf("want exactly one @link finding, got %d:\n%s", n, out)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("link-resolved", func(t *testing.T) {
		tok := tokXAI()
		dir := t.TempDir()
		// The relative target resolves to the canary at the archive
		// root; the raw target text also carries it.
		archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
			{
				{h: &tar.Header{Name: "app/link", Mode: 0o777, Typeflag: tar.TypeSymlink,
					Linkname: "../" + tok}},
			},
		}, nil, nil)
		code, out, errb := runImageCLI(t, archive)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "finding\txai-key\tlayer0:app/link\t@link") {
			t.Fatalf("@link finding missing in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("uname", func(t *testing.T) {
		tok := tokSlack()
		dir := t.TempDir()
		archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
			{
				{h: &tar.Header{Name: "app/u.txt", Mode: 0o644, Uname: tok}, body: []byte("x")},
			},
		}, nil, nil)
		code, out, errb := runImageCLI(t, archive)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "finding\tslack-token\tlayer0:app/u.txt\t@uname") {
			t.Fatalf("@uname finding missing in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("gname", func(t *testing.T) {
		tok := tokSlack()
		dir := t.TempDir()
		archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
			{
				{h: &tar.Header{Name: "app/g.txt", Mode: 0o644, Gname: tok}, body: []byte("x")},
			},
		}, nil, nil)
		code, out, errb := runImageCLI(t, archive)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "finding\tslack-token\tlayer0:app/g.txt\t@gname") {
			t.Fatalf("@gname finding missing in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("pax", func(t *testing.T) {
		tok := tokSlack()
		dir := t.TempDir()
		archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
			{
				{h: &tar.Header{Name: "app/p.txt", Mode: 0o644,
					PAXRecords: map[string]string{"comment": tok}}, body: []byte("x")},
			},
		}, nil, nil)
		code, out, errb := runImageCLI(t, archive)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "finding\tslack-token\tlayer0:app/p.txt\t@pax") {
			t.Fatalf("@pax finding missing in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("config-env-name", func(t *testing.T) {
		tok := tokGitHub()
		dir := t.TempDir()
		cfg := map[string]any{
			"config": map[string]any{
				"Env": []string{"TOKEN_" + tok + "=v"},
			},
		}
		archive := writeSavedArchive(t, dir, "img.tar", cfg, nil, false)
		code, out, errb := runImageCLI(t, archive)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		line := lineWith(out, "finding\tconfig-env-secret:TOKEN_")
		if line == "" || !strings.HasSuffix(line, "\tconfig\t-") {
			t.Fatalf("config-env-secret finding missing or malformed in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
}

// TestNameTokenErrorChannels covers the error paths without a deny
// file: the canary sits in a command argument, a flag, a file path, an
// outer entry name or a compared member name. Every case keeps today's
// exit code and the canary never reaches stdout or stderr.
func TestNameTokenErrorChannelsNoDeny(t *testing.T) {
	tok := tokGitHub()
	t.Run("unknown-command", func(t *testing.T) {
		code, out, errb := runCLI([]string{tok})
		if code != 2 {
			t.Fatalf("code = %d, want 2; stderr=%s", code, errb)
		}
		if !strings.Contains(errb, "unknown command") {
			t.Fatalf("stderr = %q, want the unknown-command message", errb)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("unknown-flag", func(t *testing.T) {
		dir := t.TempDir()
		code, out, errb := runContextCLI(t, dir, "--allow", "src", "--"+tok)
		if code != 2 {
			t.Fatalf("code = %d, want 2; stderr=%s", code, errb)
		}
		if !strings.Contains(errb, "unknown flag at argument") {
			t.Fatalf("stderr = %q, want the unknown-flag message", errb)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("deny-file-path", func(t *testing.T) {
		dir := t.TempDir()
		writeContextFile(t, dir, "src/a.txt", "x\n")
		code, out, errb := runContextCLI(t, dir, "--allow", "src",
			"--deny-file", filepath.Join(t.TempDir(), tok, "missing.txt"))
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		if !strings.Contains(errb, "deny-file: not found") {
			t.Fatalf("stderr = %q, want the static deny-file message", errb)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("accept-file-path", func(t *testing.T) {
		archDir := t.TempDir()
		archive := filepath.Join(archDir, "a.tar")
		if err := os.WriteFile(archive, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		tokX := tokXAI()
		code, out, errb := runImageCLI(t, archive, "--accept-file",
			filepath.Join(t.TempDir(), tokX, "accept.txt"))
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		// The error cites the path; the rule mask replaces the canary
		// with the marker.
		if !strings.Contains(errb, "[deny-token:xai-key]") {
			t.Fatalf("stderr = %q, want the masked path", errb)
		}
		assertNoToken(t, out, errb, tokX)
	})
	t.Run("context-stat-path", func(t *testing.T) {
		code, out, errb := runContextCLI(t, filepath.Join(t.TempDir(), tok), "--allow", "src")
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		if !strings.HasPrefix(errb, "imagecheck: ") {
			t.Fatalf("stderr = %q, want the stat error", errb)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("archive-stat-path", func(t *testing.T) {
		tokN := tokNPM()
		code, out, errb := runImageCLI(t, filepath.Join(t.TempDir(), tokN+".tar"))
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		assertNoToken(t, out, errb, tokN)
	})
	t.Run("openSaved-duplicate-outer", func(t *testing.T) {
		dir := t.TempDir()
		archive := writeOuterArchive(t, dir, "dup.tar", []outerFile{
			{Name: tok, Data: []byte("{}")},
			{Name: tok, Data: []byte("{}")},
		})
		code, out, errb := runImageCLI(t, archive)
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		if !strings.Contains(errb, "duplicate outer entry") {
			t.Fatalf("stderr = %q, want the duplicate-entry message", errb)
		}
		if !strings.Contains(errb, "[deny-token:github-token]") {
			t.Fatalf("stderr = %q, want the masked entry name", errb)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("compare-path", func(t *testing.T) {
		tokX := tokXAI()
		code, out, errb := runCLI([]string{"compare",
			filepath.Join(t.TempDir(), tokX+".tar"), filepath.Join(t.TempDir(), "b.tar")})
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		assertNoToken(t, out, errb, tokX)
	})
	t.Run("compare-entry-diff", func(t *testing.T) {
		dir := t.TempDir()
		a := writeSavedArchive(t, dir, "a.tar", map[string]any{},
			[][]testEntry{{
				{Name: "app/" + tok, Body: "a"},
			}}, false)
		b := writeSavedArchive(t, dir, "b.tar", map[string]any{},
			[][]testEntry{{
				{Name: "app/other", Body: "b"},
			}}, false)
		code, out, errb := runCLI([]string{"compare", a, b})
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "entry\t0\tapp/[deny-token:github-token]\tremoved") {
			t.Fatalf("masked entry diff line missing in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
}

// TestNameTokenDenyFileControl: with a deny file holding the same
// canary, the deny finding keeps its line and marker behaviour of
// today, the rule finding rides on the same ordinal and the canary
// stays out.
func TestNameTokenDenyFileControl(t *testing.T) {
	tok := tokGitHub()
	dir := t.TempDir()
	writeContextFile(t, dir, "src/"+tok+".txt", "hello\n")
	auxDir := t.TempDir()
	deny := writeAuxFile(t, auxDir, "deny.txt", tok+"\n")
	code, out, errb := runContextCLI(t, dir, "--allow", "src", "--deny-file", deny)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
	}
	if !hasLine(out, "finding\tdeny-token:1\tentry#1\t@name") {
		t.Fatalf("deny finding missing in:\n%s", out)
	}
	if !hasLine(out, "finding\tgithub-token\tentry#1\t@name") {
		t.Fatalf("rule finding under the same ordinal missing in:\n%s", out)
	}
	assertNoToken(t, out, errb, tok)
}

// TestNameTokenCommonNameLineUnchanged: a common name with matching
// content prints the exact historical content line, byte for byte.
func TestNameTokenCommonNameLineUnchanged(t *testing.T) {
	tok := tokGitHub()
	content := "k=" + tok + "\n"
	dir := t.TempDir()
	writeContextFile(t, dir, "src/a.txt", content)
	code, out, errb := runContextCLI(t, dir, "--allow", "src")
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
	}
	want := contentLine("finding", "github-token", "src/a.txt", 2, len(tok), sumOf(content))
	if !hasLine(out, want) {
		t.Fatalf("common-name content line changed, want exactly:\n%s\ngot:\n%s", want, out)
	}
	if strings.Contains(out, "[deny-token:") {
		t.Fatalf("no mask marker is expected on this output:\n%s", out)
	}
	assertNoToken(t, out, errb, tok)
}

// TestNameTokenAcceptCannotClear: an accept entry naming the canary
// path does not clear the finding (the key is the ordinal), the @name
// finding is never acceptable, and the content finding is acceptable
// only under the entry#N key, like with a deny file.
func TestNameTokenAcceptCannotClear(t *testing.T) {
	tok := tokGitHub()
	content := "k=" + tok + "\n"
	dir := t.TempDir()
	writeContextFile(t, dir, "src/"+tok+".txt", content)
	t.Run("canary-path-accept-stays-finding", func(t *testing.T) {
		entry := makeEntry("github-token", "src/"+tok+".txt", content, tok, 2, len(tok))
		code, out, errb := runContextCLI(t, dir, "--allow", "src", "--accept", entry)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		want := contentLine("finding", "github-token", "entry#1", 2, len(tok), sumOf(content))
		if !hasLine(out, want) {
			t.Fatalf("content finding must stay a finding in:\n%s", out)
		}
		if !strings.Contains(out, "unmatched-accept") {
			t.Fatalf("unmatched-accept line missing in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
	t.Run("entry-key-accepts-content-only", func(t *testing.T) {
		entry := makeEntry("github-token", "entry#1", content, tok, 2, len(tok))
		code, out, errb := runContextCLI(t, dir, "--allow", "src", "--accept", entry)
		if code != 1 {
			t.Fatalf("code = %d, want 1: the @name finding is never acceptable; stderr=%s", code, errb)
		}
		want := contentLine("accepted", "github-token", "entry#1", 2, len(tok), sumOf(content))
		if !hasLine(out, want) {
			t.Fatalf("content finding must be accepted under entry#1 in:\n%s", out)
		}
		if !hasLine(out, "finding\tgithub-token\tentry#1\t@name") {
			t.Fatalf("@name finding must stay a finding in:\n%s", out)
		}
		assertNoToken(t, out, errb, tok)
	})
}

// TestRuleMaskWriterSplitWrite: a token split across several writes is
// still masked, because the writer buffers to the line break.
func TestRuleMaskWriterSplitWrite(t *testing.T) {
	tok := tokGitHub()
	var buf bytes.Buffer
	w := &ruleMaskWriter{w: &buf}
	if _, err := w.Write([]byte("head " + tok[:12])); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(tok[12:] + "\n")); err != nil {
		t.Fatal(err)
	}
	if want := "head [deny-token:github-token]\n"; buf.String() != want {
		t.Fatalf("split write = %q, want %q", buf.String(), want)
	}
}

// TestRuleMaskWriterFlushPartialLine: a final line without a newline
// stays buffered until Flush, which writes it masked.
func TestRuleMaskWriterFlushPartialLine(t *testing.T) {
	tok := tokNPM()
	var buf bytes.Buffer
	w := &ruleMaskWriter{w: &buf}
	if _, err := w.Write([]byte("tail " + tok)); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "" {
		t.Fatalf("the partial line must stay buffered, got %q", buf.String())
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if want := "tail [deny-token:npm-token]"; buf.String() != want {
		t.Fatalf("flush = %q, want %q", buf.String(), want)
	}
}

// TestMaskRuleTokensNoMatchByteIdentical: a line with no occurrence is
// returned byte for byte unchanged, the AWS documentation example is
// exempt exactly as in detection, and a real key is masked.
func TestMaskRuleTokensNoMatchByteIdentical(t *testing.T) {
	in := "plain line, nothing to mask\n"
	if got := maskRuleTokens(in); !bytes.Equal([]byte(got), []byte(in)) {
		t.Fatalf("no-match line changed: %q", got)
	}
	ex := tokAWSDocsExample() + "\n"
	if got := maskRuleTokens(ex); !bytes.Equal([]byte(got), []byte(ex)) {
		t.Fatalf("the docs example must stay as written: %q", got)
	}
	if got := maskRuleTokens(tokAWS() + "\n"); got != "[deny-token:aws-access-key]\n" {
		t.Fatalf("real key = %q, want the masked line", got)
	}
}

// TestMaskRuleTokensMarkerRecomposition: no marker joined to the text
// around it recomposes a rule match, and the while-a-rule-still-matches
// loop replaces such a recomposition with "?" (proved with a synthetic
// rule whose marker spells its own token).
func TestMaskRuleTokensMarkerRecomposition(t *testing.T) {
	for _, r := range scanRules {
		marker := "[deny-token:" + r.name + "]"
		out := maskRuleTokens(marker + strings.Repeat("A", 70) + "\n")
		if hits := contentRuleHits(out, scanRules); len(hits) != 0 {
			t.Fatalf("marker %s recomposed a match %v in %q", marker, hits, out)
		}
	}
	saved := scanRules
	t.Cleanup(func() { scanRules = saved })
	scanRules = []scanRule{{name: "ab", pattern: regexp.MustCompile("ab")}}
	if got := maskRuleTokens("xab\n"); got != "x[deny-token:?]\n" {
		t.Fatalf("marker loop = %q, want %q", got, "x[deny-token:?]\n")
	}
}

// syntheticPEMBlock builds a synthetic private-key block with real line
// breaks: a header line and two base64 body lines whose joined run
// satisfies the private-key rule's body. Every line of the block sits
// inside the match, so a correctly masked output holds none of them.
// (A trailing -----END----- footer would fall outside the rule's body
// run, whose character set does not include "-", so it is not part of
// the block under test.)
func syntheticPEMBlock() string {
	return "-----BEGIN PRIVATE KEY-----\n" +
		strings.Repeat("A", 40) + "\n" +
		strings.Repeat("B", 40) + "\n"
}

// redactionMarkerRe matches a redaction marker of either family: the
// deny marker [deny-token:<line>] and the rule marker
// [deny-token:<rule>].
var redactionMarkerRe = regexp.MustCompile(`\[deny-token:[A-Za-z0-9-]+\]`)

// assertDenyLiteralsGone asserts that literal appears nowhere in out
// or errb except inside redaction markers: after removing every
// marker, no occurrence of the literal may remain.
func assertDenyLiteralsGone(t *testing.T, out, errb, literal string) {
	t.Helper()
	for name, got := range map[string]string{"stdout": out, "stderr": errb} {
		cleaned := redactionMarkerRe.ReplaceAllString(got, "")
		if strings.Contains(cleaned, literal) {
			t.Fatalf("deny literal %q survives in %s outside the redaction markers", literal, name)
		}
	}
}

// assertPEMGone asserts that no line of block appears in out or errb:
// every block line (header, body, final line) is checked on its own.
func assertPEMGone(t *testing.T, out, errb, block string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		for name, got := range map[string]string{"stdout": out, "stderr": errb} {
			if strings.Contains(got, line) {
				t.Fatalf("block line %q survives in %s:\n%s", line, name, got)
			}
		}
	}
}

// TestDenyFileLiteralNotReintroduced covers F1: the rule mask runs
// before the deny pass inside redactingWriter, so a deny literal that
// fits the rule marker (\"github\" and \"deny\" inside
// [deny-token:github-token]) does not survive the mask: the intact
// rule marker never reaches the output, because the deny pass redacts
// the literals it holds. A late --accept-file error with a rule canary
// in its path, and a config holding a secret-looking environment
// variable name with a canary, keep today's exit code, and neither
// the canary nor a deny literal reaches stdout or stderr.
func TestDenyFileLiteralNotReintroduced(t *testing.T) {
	tok := tokGitHub()
	deny := "github\ndeny\n"
	assertMarkerBroken := func(t *testing.T, out, errb string) {
		t.Helper()
		for name, got := range map[string]string{"stdout": out, "stderr": errb} {
			// The deny pass runs after the rule mask, so the literals
			// inside the marker are redacted and the marker does not
			// survive intact.
			if strings.Contains(got, "[deny-token:github-token]") {
				t.Fatalf("the intact rule marker survives in %s: the deny literals inside it must be redacted", name)
			}
		}
		assertDenyLiteralsGone(t, out, errb, "github")
		assertDenyLiteralsGone(t, out, errb, "deny")
	}
	t.Run("context", func(t *testing.T) {
		dir := t.TempDir()
		writeContextFile(t, dir, "src/a.txt", "hello\n")
		d := writeAuxFile(t, t.TempDir(), "deny.txt", deny)
		code, out, errb := runContextCLI(t, dir, "--allow", "src",
			"--deny-file", d, "--accept-file",
			filepath.Join(t.TempDir(), tok, "accept.txt"))
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		if !strings.Contains(errb, "accept.txt") {
			t.Fatalf("stderr = %q, want the late accept-file error", errb)
		}
		assertMarkerBroken(t, out, errb)
		assertNoToken(t, out, errb, tok)
	})
	t.Run("image", func(t *testing.T) {
		dir := t.TempDir()
		cfg := map[string]any{"config": map[string]any{"Env": []string{"TOKEN_" + tok + "=v"}}}
		archive := writeSavedArchive(t, dir, "img.tar", cfg, nil, false)
		d := writeAuxFile(t, t.TempDir(), "deny.txt", deny)
		code, out, errb := runImageCLI(t, archive, "--deny-file", d,
			"--accept-file", filepath.Join(t.TempDir(), tok, "accept.txt"))
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		if !strings.Contains(errb, "accept.txt") {
			t.Fatalf("stderr = %q, want the late accept-file error", errb)
		}
		assertMarkerBroken(t, out, errb)
		assertNoToken(t, out, errb, tok)
	})
}

// TestPEMBlockInErrorChannels covers F2: a complete synthetic PEM block
// with real line breaks in an entry name arrives at the error channel
// and the compare diff as a single message; the whole block is masked
// by the writers' unit (the buffer up to the last line break), no
// block line survives, and the exit code is unchanged.
func TestPEMBlockInErrorChannels(t *testing.T) {
	block := syntheticPEMBlock()
	if hits := contentRuleHits(block, scanRules); len(hits) != 1 || hits[0] != "private-key" {
		t.Fatalf("fixture hits = %v, want exactly [private-key]", hits)
	}
	t.Run("image-duplicate-outer", func(t *testing.T) {
		dir := t.TempDir()
		archive := writeOuterArchive(t, dir, "dup.tar", []outerFile{
			{Name: block, Data: []byte("{}")},
			{Name: block, Data: []byte("{}")},
		})
		code, out, errb := runImageCLI(t, archive)
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		if !strings.Contains(errb, "duplicate outer entry") {
			t.Fatalf("stderr = %q, want the duplicate-entry message", errb)
		}
		if !strings.Contains(errb, "[deny-token:private-key]") {
			t.Fatalf("stderr = %q, want the masked block", errb)
		}
		assertPEMGone(t, out, errb, block)
	})
	t.Run("image-duplicate-outer-with-deny", func(t *testing.T) {
		dir := t.TempDir()
		archive := writeOuterArchive(t, dir, "dup.tar", []outerFile{
			{Name: block, Data: []byte("{}")},
			{Name: block, Data: []byte("{}")},
		})
		d := writeAuxFile(t, t.TempDir(), "deny.txt", "sekritliteral\n")
		code, out, errb := runImageCLI(t, archive, "--deny-file", d)
		if code != 4 {
			t.Fatalf("code = %d, want 4; stderr=%s", code, errb)
		}
		if !strings.Contains(errb, "duplicate outer entry") {
			t.Fatalf("stderr = %q, want the duplicate-entry message", errb)
		}
		if !strings.Contains(errb, "[deny-token:private-key]") {
			t.Fatalf("stderr = %q, want the masked block", errb)
		}
		assertPEMGone(t, out, errb, block)
	})
	t.Run("compare-member-diff", func(t *testing.T) {
		dir := t.TempDir()
		a := writeSavedArchive(t, dir, "a.tar", map[string]any{},
			[][]testEntry{{
				{Name: block, Body: "a"},
				{Name: "app/keep", Body: "same"},
			}}, false)
		b := writeSavedArchive(t, dir, "b.tar", map[string]any{},
			[][]testEntry{{
				{Name: "app/keep", Body: "same"},
			}}, false)
		code, out, errb := runCLI([]string{"compare", a, b})
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "entry\t0\t[deny-token:private-key]\tremoved") {
			t.Fatalf("masked diff line missing in:\n%s", out)
		}
		assertPEMGone(t, out, errb, block)
	})
}

// TestNameTokenResolvedLinkTargetNormalizedOnly covers F3: a PEM whose
// body run only forms after normalization collapses the \"/./\" segment
// is detected on the resolved link target when the raw target does not
// match. Context goes through checkContextLink directly, so it runs
// unchanged on every OS; the layer goes through the CLI. In both, the
// finding is at @link and no PEM line reaches the output.
func TestNameTokenResolvedLinkTargetNormalizedOnly(t *testing.T) {
	// The raw target's body run stops at the \".\" of \"/./\" (31
	// characters, below the rule's 64); normalized, the run is 64
	// characters (the remaining \"/\" counts in the body class).
	raw := "-----BEGIN PRIVATE KEY-----\n" +
		strings.Repeat("A", 30) + "/./" + strings.Repeat("B", 33)
	if hits := contentRuleHits(raw, scanRules); len(hits) != 0 {
		t.Fatalf("fixture raw target must not match: %v", hits)
	}
	t.Run("context", func(t *testing.T) {
		resolved := normalizeArchiveName(path.Join(path.Dir("src/lnk"), raw))
		if hits := contentRuleHits(resolved, scanRules); len(hits) != 1 || hits[0] != "private-key" {
			t.Fatalf("fixture resolved target must match: %v", hits)
		}
		f := newFindings()
		checkContextLink("src/lnk", raw, '/', "src/lnk", nil, f)
		out := printFindings(t, f)
		if !hasLine(out, "finding\tprivate-key\tsrc/lnk\t@link") {
			t.Fatalf("@link finding missing in:\n%s", out)
		}
		if n := strings.Count(out, "finding\t"); n != 1 {
			t.Fatalf("findings = %d, want exactly 1:\n%s", n, out)
		}
		assertNoLiteral(t, out, "", "-----BEGIN PRIVATE KEY-----")
	})
	t.Run("layer", func(t *testing.T) {
		dir := t.TempDir()
		archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
			{
				{h: &tar.Header{Name: "app/link", Mode: 0o777, Typeflag: tar.TypeSymlink,
					Linkname: raw}},
			},
		}, nil, nil)
		code, out, errb := runImageCLI(t, archive)
		if code != 1 {
			t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
		}
		if !hasLine(out, "finding\tprivate-key\tlayer0:app/link\t@link") {
			t.Fatalf("@link finding missing in:\n%s", out)
		}
		assertNoLiteral(t, out, errb, "-----BEGIN PRIVATE KEY-----")
	})
}

// TestNameTokenOrdinalKeyAccept covers F3: a layer member and an
// archive index file whose names hold a canary and whose content
// matches are keyed under their ordinals. Under the ordinal key the
// content finding is accepted (index, where the key form is
// acceptable); accepting under the original name never matches the
// key (layer and index); a wrong sha never accepts (index). The layer
// ordinal key form (layer0:entry#N) is rejected by the pinned
// accept-entry validation ("old layer-index form"), so there the key
// is proven through the original-name non-acceptance.
func TestNameTokenOrdinalKeyAccept(t *testing.T) {
	nameTok := tokGitHub()
	contentTok := tokNPM()
	content := "k=" + contentTok + "\n"
	t.Run("layer", func(t *testing.T) {
		dir := t.TempDir()
		archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, [][]metaEntry{
			{{h: &tar.Header{Name: "app/" + nameTok + ".bin", Mode: 0o644}, body: []byte(content)}},
		}, nil, nil)
		t.Run("findings-under-ordinal", func(t *testing.T) {
			code, out, errb := runImageCLI(t, archive)
			if code != 1 {
				t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
			}
			want := contentLine("finding", "npm-token", "layer0:entry#0", 2, len(contentTok), sumOf(content))
			if !hasLine(out, want) {
				t.Fatalf("content finding under the ordinal missing in:\n%s", out)
			}
			if !hasLine(out, "finding\tgithub-token\tlayer0:entry#0\t@name") {
				t.Fatalf("@name finding under the ordinal missing in:\n%s", out)
			}
			assertNoToken(t, out, errb, nameTok)
			assertNoToken(t, out, errb, contentTok)
		})
		t.Run("original-name-not-accepted", func(t *testing.T) {
			entry := makeEntry("npm-token", "app/"+nameTok+".bin", content, contentTok, 2, len(contentTok))
			code, out, errb := runImageCLI(t, archive, "--accept", entry)
			if code != 1 {
				t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
			}
			want := contentLine("finding", "npm-token", "layer0:entry#0", 2, len(contentTok), sumOf(content))
			if !hasLine(out, want) {
				t.Fatalf("content finding must stay a finding in:\n%s", out)
			}
			if !strings.Contains(out, "unmatched-accept") {
				t.Fatalf("unmatched-accept line missing in:\n%s", out)
			}
			assertNoToken(t, out, errb, nameTok)
		})
		t.Run("ordinal-key-rejected-by-parser", func(t *testing.T) {
			entry := makeEntry("npm-token", "layer0:entry#0", content, contentTok, 2, len(contentTok))
			code, out, errb := runImageCLI(t, archive, "--accept", entry)
			if code != 2 {
				t.Fatalf("code = %d, want 2; stderr=%s", code, errb)
			}
			if !strings.Contains(errb, "old layer-index form") {
				t.Fatalf("stderr = %q, want the pinned layer-index rejection", errb)
			}
			assertNoToken(t, out, errb, nameTok)
		})
	})
	t.Run("index", func(t *testing.T) {
		dir := t.TempDir()
		archive, _ := metaArchive(t, dir, "img.tar", map[string]any{}, nil, nil,
			[]outerFile{{Name: "extra/" + nameTok, Data: []byte(content)}})
		t.Run("ordinal-key-accepts-content", func(t *testing.T) {
			entry := makeEntry("npm-token", "archive:entry#1", content, contentTok, 2, len(contentTok))
			code, out, errb := runImageCLI(t, archive, "--accept", entry)
			if code != 1 {
				t.Fatalf("code = %d, want 1 (the @name finding is never acceptable); stderr=%s", code, errb)
			}
			want := contentLine("accepted", "npm-token", "archive:entry#1", 2, len(contentTok), sumOf(content))
			if !hasLine(out, want) {
				t.Fatalf("content finding must be accepted under the ordinal key in:\n%s", out)
			}
			if !hasLine(out, "finding\tgithub-token\tarchive:entry#1\t@name") {
				t.Fatalf("@name finding must stay a finding in:\n%s", out)
			}
			assertNoToken(t, out, errb, nameTok)
			assertNoToken(t, out, errb, contentTok)
		})
		t.Run("original-name-not-accepted", func(t *testing.T) {
			entry := makeEntry("npm-token", "extra/"+nameTok, content, contentTok, 2, len(contentTok))
			code, out, errb := runImageCLI(t, archive, "--accept", entry)
			if code != 1 {
				t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
			}
			want := contentLine("finding", "npm-token", "archive:entry#1", 2, len(contentTok), sumOf(content))
			if !hasLine(out, want) {
				t.Fatalf("content finding must stay a finding in:\n%s", out)
			}
			if !strings.Contains(out, "unmatched-accept") {
				t.Fatalf("unmatched-accept line missing in:\n%s", out)
			}
			assertNoToken(t, out, errb, nameTok)
		})
		t.Run("wrong-sha-not-accepted", func(t *testing.T) {
			entry := makeEntry("npm-token", "archive:entry#1", content+"\n", contentTok, 2, len(contentTok))
			code, out, errb := runImageCLI(t, archive, "--accept", entry)
			if code != 1 {
				t.Fatalf("code = %d, want 1; stderr=%s", code, errb)
			}
			want := contentLine("finding", "npm-token", "archive:entry#1", 2, len(contentTok), sumOf(content))
			if !hasLine(out, want) {
				t.Fatalf("content finding must stay a finding in:\n%s", out)
			}
			if !strings.Contains(out, "unmatched-accept") {
				t.Fatalf("unmatched-accept line missing in:\n%s", out)
			}
			assertNoToken(t, out, errb, nameTok)
		})
	})
}

// TestMaskedOutputFlush covers F3: withMaskedOutput flushes both
// wrapped writers before returning fn's result, so a final message
// without a newline is masked and not lost.
func TestMaskedOutputFlush(t *testing.T) {
	tok := tokGitHub()
	var outBuf, errBuf bytes.Buffer
	code := withMaskedOutput(&outBuf, &errBuf, func(out, errOut io.Writer) int {
		io.WriteString(out, "o:"+tok)
		io.WriteString(errOut, "e:"+tok)
		return 7
	})
	if code != 7 {
		t.Fatalf("code = %d, want 7 (fn's result)", code)
	}
	if want := "o:[deny-token:github-token]"; outBuf.String() != want {
		t.Fatalf("stdout = %q, want %q", outBuf.String(), want)
	}
	if want := "e:[deny-token:github-token]"; errBuf.String() != want {
		t.Fatalf("stderr = %q, want %q", errBuf.String(), want)
	}
}

// TestRuleMaskWriterPEMUnits covers F2 on ruleMaskWriter: the masked
// unit runs to the last line break, so a block delivered in a single
// Write is masked whole, two blocks in one message are both masked,
// and a tail without a break stays buffered until Flush.
func TestRuleMaskWriterPEMUnits(t *testing.T) {
	block := syntheticPEMBlock()
	t.Run("single-write-block", func(t *testing.T) {
		var buf bytes.Buffer
		w := &ruleMaskWriter{w: &buf}
		if _, err := w.Write([]byte("x " + block + " #\n")); err != nil {
			t.Fatal(err)
		}
		if want := "x [deny-token:private-key] #\n"; buf.String() != want {
			t.Fatalf("output = %q, want %q", buf.String(), want)
		}
	})
	t.Run("two-blocks", func(t *testing.T) {
		var buf bytes.Buffer
		w := &ruleMaskWriter{w: &buf}
		if _, err := w.Write([]byte(block + block)); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(buf.String(), "[deny-token:private-key]"); n != 2 {
			t.Fatalf("markers = %d, want 2 in:\n%s", n, buf.String())
		}
		assertPEMGone(t, buf.String(), "", block)
	})
	t.Run("tail-kept", func(t *testing.T) {
		tok := tokGitHub()
		var buf bytes.Buffer
		w := &ruleMaskWriter{w: &buf}
		if _, err := w.Write([]byte("t " + tok)); err != nil {
			t.Fatal(err)
		}
		if buf.String() != "" {
			t.Fatalf("the tail without a break must stay buffered, got %q", buf.String())
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if want := "t [deny-token:github-token]"; buf.String() != want {
			t.Fatalf("flush = %q, want %q", buf.String(), want)
		}
	})
}

// TestRedactingWriterPEMUnits covers F2 on redactingWriter: with a
// deny file loaded, the unit runs to the last line break and the rule
// mask inside the deny pass masks a block delivered in a single Write;
// a tail without a break stays buffered, never written unmasked. This
// writer has no Flush, so such a tail is lost rather than leaked; every
// message of the package ends in a line break and leaves none.
func TestRedactingWriterPEMUnits(t *testing.T) {
	block := syntheticPEMBlock()
	toks := []denyToken{{line: 1, needle: asciiLower([]byte("sekritliteral"))}}
	t.Run("single-write-block", func(t *testing.T) {
		var buf bytes.Buffer
		w := &redactingWriter{w: &buf, toks: toks}
		if _, err := w.Write([]byte("x " + block + " #\n")); err != nil {
			t.Fatal(err)
		}
		if want := "x [deny-token:private-key] #\n"; buf.String() != want {
			t.Fatalf("output = %q, want %q", buf.String(), want)
		}
	})
	t.Run("two-blocks", func(t *testing.T) {
		var buf bytes.Buffer
		w := &redactingWriter{w: &buf, toks: toks}
		if _, err := w.Write([]byte(block + block)); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(buf.String(), "[deny-token:private-key]"); n != 2 {
			t.Fatalf("markers = %d, want 2 in:\n%s", n, buf.String())
		}
		assertPEMGone(t, buf.String(), "", block)
	})
	t.Run("tail-kept", func(t *testing.T) {
		tok := tokGitHub()
		var buf bytes.Buffer
		w := &redactingWriter{w: &buf, toks: toks}
		if _, err := w.Write([]byte("t " + tok)); err != nil {
			t.Fatal(err)
		}
		if buf.String() != "" {
			t.Fatalf("the tail without a break must stay buffered, got %q", buf.String())
		}
	})
}
