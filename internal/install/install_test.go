// Tests for the install core run on every platform against real temp files
// and httptest servers: no sh, no PowerShell, no host-only build tags. The
// Windows-specific locking and replacement proofs live in
// install_windows_test.go.
package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBinary is the static payload served as the release artifact. It is
// arbitrary bytes, not a program: the core installs these bytes exactly and
// has no path to execute them (the core never launches the downloaded
// file, and the tests never do either).
const fakeBinary = "soho fake release payload\n"

// hermeticTransport refuses every host other than the exact httptest
// listener. Environment proxies do not constrain the httptest clients, so
// the gate lives in the transport itself: even a broken core validation
// guard cannot push a test onto a real network.
type hermeticTransport struct {
	allowed string
	base    http.RoundTripper
}

type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("test network refused")
}

func noNetworkClient() *http.Client {
	return &http.Client{Transport: refusingTransport{}}
}

func (t *hermeticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != t.allowed {
		return nil, fmt.Errorf("hermetic test transport: refusing host %q (only %q)", req.URL.Host, t.allowed)
	}
	return t.base.RoundTrip(req)
}

// hermeticClient returns a client that only reaches the exact httptest
// listener; Proxy is nil so environment proxies are never consulted.
func hermeticClient(server *httptest.Server) *http.Client {
	return &http.Client{
		Transport: &hermeticTransport{
			allowed: server.Listener.Addr().String(),
			base:    &http.Transport{Proxy: nil},
		},
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func artifactNameFor(osName, arch string) string {
	name := "soho_" + osName + "_" + arch
	if osName == "windows" {
		name += ".exe"
	}
	return name
}

func destinationName(osName string) string {
	if osName == "windows" {
		return "soho.exe"
	}
	return "soho"
}

type fixtureOptions struct {
	// Target platform for the download. Empty os/arch let the core default
	// to the test host, mirroring runtime.GOOS/GOARCH.
	os   string
	arch string

	version    string
	installDir string
	// base overrides the release base entirely (for example to exercise
	// credentials or a missing scheme); otherwise the httptest server URL
	// plus baseSuffix is used.
	base       string
	baseSuffix string

	// payload overrides the served artifact bytes; defaults to fakeBinary.
	payload []byte
	// sumsBody overrides the served SHA256SUMS content verbatim.
	sumsBody      string
	missingAsset  bool
	missingSums   bool
	missingEntry  bool
	badChecksum   bool
	upperChecksum bool
}

type coreResult struct {
	res        Result
	err        error
	requests   []string
	installDir string
	base       string
	artifact   string
}

func runCore(t *testing.T, options fixtureOptions) coreResult {
	t.Helper()
	targetOS := options.os
	if targetOS == "" {
		targetOS = runtime.GOOS
	}
	arch := options.arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	installDir := options.installDir
	if installDir == "" {
		installDir = filepath.Join(t.TempDir(), "bin")
	}
	artifact := artifactNameFor(targetOS, arch)
	payload := options.payload
	if payload == nil {
		payload = []byte(fakeBinary)
	}
	digest := sha256Hex(payload)
	sums := options.sumsBody
	if sums == "" {
		switch {
		case options.badChecksum:
			sums = strings.Repeat("0", 64) + "  " + artifact + "\n"
		case options.upperChecksum:
			sums = strings.ToUpper(digest) + "  " + artifact + "\n"
		case options.missingEntry:
			sums = ""
		default:
			sums = digest + "  " + artifact + "\n"
		}
	}
	var requestsMu sync.Mutex
	var requests []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests = append(requests, r.URL.Path)
		requestsMu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			if options.missingSums {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, sums)
		case strings.HasSuffix(r.URL.Path, "/"+artifact):
			if options.missingAsset {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base := options.base
	if base == "" {
		base = server.URL + options.baseSuffix
	}
	res, err := Run(context.Background(), Options{
		Architecture: options.arch,
		Client:       hermeticClient(server),
		InstallDir:   installDir,
		OS:           options.os,
		ReleaseBase:  base,
		Version:      options.version,
	})
	requestsMu.Lock()
	defer requestsMu.Unlock()
	return coreResult{
		res:        res,
		err:        err,
		requests:   append([]string(nil), requests...),
		installDir: installDir,
		base:       base,
		artifact:   artifact,
	}
}

// assertNoTempFiles fails if the installer left a .soho.* temp file in dir
// (a missing dir is fine: nothing was created at all).
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".soho.") {
			t.Fatalf("installer left temporary file %q in %s", entry.Name(), dir)
		}
	}
}

// assertDirAbsent fails if dir was created before the validation error.
func assertDirAbsent(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("install dir %s was created before validation: %v", dir, err)
	}
}

// assertDirExact fails if dir holds entries other than the given names.
func assertDirExact(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("dir %s entries=%v want %v", dir, got, names)
	}
}

// Port of TestInstallSHappyPathAndInstallDir: the core installs the exact
// served bytes into the requested directory under the latest release path
// and records the resulting path, digest, and URL. Empty OS/Architecture
// prove the host defaults.
func TestInstallHappyPathInstallsLatestRelease(t *testing.T) {
	got := runCore(t, fixtureOptions{})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	destination := filepath.Join(got.installDir, destinationName(runtime.GOOS))
	wantPaths := []string{"/latest/download/" + got.artifact, "/latest/download/SHA256SUMS"}
	if len(got.requests) != len(wantPaths) || got.requests[0] != wantPaths[0] || got.requests[1] != wantPaths[1] {
		t.Fatalf("download paths=%v want %v", got.requests, wantPaths)
	}
	installed, err := os.ReadFile(destination)
	if err != nil || string(installed) != fakeBinary {
		t.Fatalf("installed binary=%q err=%v", installed, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(destination)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode=%v want 0755", destination, fi.Mode().Perm())
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file: %v", destination, fi.Mode())
		}
	}
	if got.res.Artifact != got.artifact {
		t.Errorf("result artifact=%q want %q", got.res.Artifact, got.artifact)
	}
	if got.res.Destination != destination {
		t.Errorf("result destination=%q want %q", got.res.Destination, destination)
	}
	if got.res.Digest != sha256Hex([]byte(fakeBinary)) {
		t.Errorf("result digest=%q want %q", got.res.Digest, sha256Hex([]byte(fakeBinary)))
	}
	if got.res.ReleaseURL != got.base+"/latest/download" {
		t.Errorf("result release URL=%q", got.res.ReleaseURL)
	}
	assertDirExact(t, got.installDir, destinationName(runtime.GOOS))
}

// DJA-318 N2 (negative): a successful install publishes the soho binary
// only — no herdr-soho (or herdr-soho.exe) file ever lands in the
// destination directory, for any target platform.
func TestInstallSuccessLeavesNoLegacyBinary(t *testing.T) {
	for _, osName := range []string{runtime.GOOS, "linux", "windows"} {
		got := runCore(t, fixtureOptions{os: osName, arch: "amd64"})
		if got.err != nil {
			t.Fatalf("install: %v", got.err)
		}
		for _, legacy := range []string{"herdr-soho", "herdr-soho.exe"} {
			if _, err := os.Stat(filepath.Join(got.installDir, legacy)); !os.IsNotExist(err) {
				t.Fatalf("%s found in %s, want only %s", legacy, got.installDir, destinationName(osName))
			}
		}
		assertDirExact(t, got.installDir, destinationName(osName))
	}
}

// Port of TestInstallSExplicitVersionUsesMatchingRelease: an explicit
// version downloads from /download/<version>, like the old installers.
func TestInstallExplicitVersionUsesMatchingRelease(t *testing.T) {
	got := runCore(t, fixtureOptions{version: "v1.2.3"})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	want := []string{"/download/v1.2.3/" + got.artifact, "/download/v1.2.3/SHA256SUMS"}
	if len(got.requests) != len(want) || got.requests[0] != want[0] || got.requests[1] != want[1] {
		t.Fatalf("download paths=%v want %v", got.requests, want)
	}
	if got.res.ReleaseURL != got.base+"/download/v1.2.3" {
		t.Errorf("result release URL=%q", got.res.ReleaseURL)
	}
}

// Port of TestInstallSRejectsWrongShaWithoutReplacingExistingBinary: a
// wrong digest must not touch the installed binary and must clean up the
// temp files.
func TestInstallRejectsWrongShaWithoutReplacingExistingBinary(t *testing.T) {
	dir := t.TempDir()
	old := []byte("existing binary")
	destination := filepath.Join(dir, destinationName(runtime.GOOS))
	if err := os.WriteFile(destination, old, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	got := runCore(t, fixtureOptions{installDir: dir, badChecksum: true})
	if got.err == nil || !strings.Contains(got.err.Error(), "sha256 mismatch") {
		t.Fatalf("install err=%v", got.err)
	}
	after, err := os.ReadFile(destination)
	if err != nil || string(after) != string(old) {
		t.Fatalf("existing binary changed: %q err=%v", after, err)
	}
	if fi, statErr := os.Stat(destination); statErr != nil {
		t.Fatal(statErr)
	} else if fi.Mode() != beforeInfo.Mode() {
		t.Errorf("existing mode changed: %v; before %v", fi.Mode(), beforeInfo.Mode())
	}
	assertNoTempFiles(t, dir)
}

// Port of TestInstallSRejectsMissingArtifactAndMissingChecksumEntry, plus
// the missing SHA256SUMS case: every failed download leaves no destination
// and no temp files, and the SHA256SUMS is only requested after the
// artifact.
func TestInstallRejectsMissingDownloads(t *testing.T) {
	hostOS := runtime.GOOS
	for _, test := range []struct {
		name    string
		options fixtureOptions
		want    string
		reqs    int
	}{
		{name: "asset missing", options: fixtureOptions{missingAsset: true}, want: "failed to download", reqs: 1},
		{name: "checksum entry missing", options: fixtureOptions{missingEntry: true}, want: "no unique entry", reqs: 2},
		{name: "checksum file missing", options: fixtureOptions{missingSums: true}, want: "failed to download SHA256SUMS", reqs: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := runCore(t, test.options)
			if got.err == nil || !strings.Contains(got.err.Error(), test.want) {
				t.Fatalf("install err=%v", got.err)
			}
			if len(got.requests) != test.reqs {
				t.Fatalf("requests=%v want %d", got.requests, test.reqs)
			}
			if _, err := os.Stat(filepath.Join(got.installDir, destinationName(hostOS))); !os.IsNotExist(err) {
				t.Fatalf("failed install left destination: %v", err)
			}
			assertNoTempFiles(t, got.installDir)
		})
	}
}

// Port of TestInstallSAcceptsAnUppercaseChecksum: an uppercase SHA256SUMS
// line verifies, and the recorded digest is canonical lowercase.
func TestInstallAcceptsAnUppercaseChecksum(t *testing.T) {
	got := runCore(t, fixtureOptions{upperChecksum: true})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	if got.res.Digest != sha256Hex([]byte(fakeBinary)) {
		t.Errorf("result digest=%q want canonical lowercase %q", got.res.Digest, sha256Hex([]byte(fakeBinary)))
	}
}

// Digest row rules: exactly one valid 64-hex row for the artifact, the
// optional '*' marker, CRLF tolerance, and malformed or duplicate rows
// rejected without touching the destination.
func TestInstallDigestEntryRules(t *testing.T) {
	hostOS := runtime.GOOS
	artifact := artifactNameFor(hostOS, runtime.GOARCH)
	digest := sha256Hex([]byte(fakeBinary))
	for _, test := range []struct {
		name string
		sums string
		want string // error substring; empty means success
	}{
		{name: "star marker", sums: digest + " *" + artifact + "\n"},
		{name: "crlf line", sums: digest + "  " + artifact + "\r\n"},
		{name: "duplicate", sums: digest + "  " + artifact + "\n" + digest + "  " + artifact + "\n", want: "no unique entry"},
		{name: "duplicate with marker", sums: digest + "  " + artifact + "\n" + digest + " *" + artifact + "\n", want: "no unique entry"},
		{name: "digest too short", sums: digest[:63] + "  " + artifact + "\n", want: "no unique entry"},
		{name: "digest too long", sums: digest + "a" + "  " + artifact + "\n", want: "no unique entry"},
		{name: "non-hex digest", sums: "z" + digest[1:] + "  " + artifact + "\n", want: "no unique entry"},
		{name: "extra field", sums: digest + "  " + artifact + " extra\n", want: "no unique entry"},
		{name: "wrong filename", sums: digest + "  soho_other\n", want: "no unique entry"},
		{name: "other file lines ignored", sums: "not-a-digest  soho_other\n" + digest + "  " + artifact + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := runCore(t, fixtureOptions{sumsBody: test.sums})
			if test.want == "" {
				if got.err != nil {
					t.Fatalf("install: %v", got.err)
				}
				if got.res.Digest != digest {
					t.Errorf("result digest=%q want %q", got.res.Digest, digest)
				}
				return
			}
			if got.err == nil || !strings.Contains(got.err.Error(), test.want) {
				t.Fatalf("install err=%v", got.err)
			}
			if _, err := os.Stat(filepath.Join(got.installDir, destinationName(hostOS))); !os.IsNotExist(err) {
				t.Fatalf("failed install left destination: %v", err)
			}
			assertNoTempFiles(t, got.installDir)
		})
	}
}

// Invalid tags are rejected before any network or filesystem mutation.
func TestInstallRejectsInvalidVersion(t *testing.T) {
	for _, version := range []string{
		"1.2.3",    // no leading v
		"V1.2.3",   // uppercase V
		"v..",      // path traversal dots
		"v1..2",    // dots in the middle
		"v1.2/3",   // slash
		"v1.2.3 ",  // trailing space
		"v1.2.3\n", // newline
		"vé1.2.3",  // non-ASCII
		"v1.2.3?x", // query character
	} {
		t.Run(version, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "bin")
			got := runCore(t, fixtureOptions{version: version, installDir: dir})
			if got.err == nil {
				t.Fatalf("version %q accepted", version)
			}
			if len(got.requests) != 0 {
				t.Fatalf("version %q reached the network: %v", version, got.requests)
			}
			assertDirAbsent(t, dir)
		})
	}
}

// Valid tag edges: release characters only, and no ".." anywhere.
func TestInstallAcceptsReleaseCharacterVersions(t *testing.T) {
	// "v" alone passes the rule stated for the core (begin with v, release
	// characters only, no ".."); install.ps1 additionally required at least
	// one character after v.
	for _, version := range []string{"v", "v1.2.3-rc.1_2"} {
		t.Run(version, func(t *testing.T) {
			got := runCore(t, fixtureOptions{version: version})
			if got.err != nil {
				t.Fatalf("version %q rejected: %v", version, got.err)
			}
			if got.res.ReleaseURL != got.base+"/download/"+version {
				t.Errorf("result release URL=%q", got.res.ReleaseURL)
			}
		})
	}
}

// Invalid release bases are rejected before any network or filesystem
// mutation: whitespace anywhere, credentials, a query, a fragment, and a
// non-http(s) or missing scheme.
func TestInstallRejectsInvalidReleaseBase(t *testing.T) {
	for _, test := range []struct{ name, base, want string }{
		{"space", "https://example.com/releases/a b", "contains whitespace"},
		{"newline", "https://example.com/releases/a\nb", "contains whitespace"},
		{"tab", "https://example.com/releases/a\tb", "contains whitespace"},
		{"credentials", "http://user:pass@example.com/releases", "contains credentials"},
		{"query", "https://example.com/releases?tag=v1", "contains a query"},
		{"fragment", "https://example.com/releases#sha256", "contains a fragment"},
		{"ftp scheme", "ftp://example.com/releases", "must be http or https"},
		{"missing scheme", "example.com/releases", "must be http or https"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "bin")
			got := runCore(t, fixtureOptions{base: test.base, installDir: dir})
			if got.err == nil || !strings.Contains(got.err.Error(), test.want) {
				t.Fatalf("base %q: install err=%v", test.base, got.err)
			}
			if len(got.requests) != 0 {
				t.Fatalf("base %q reached the network: %v", test.base, got.requests)
			}
			assertDirAbsent(t, dir)
		})
	}
}

// A trailing slash in the base is trimmed so the download paths match the
// old installers exactly.
func TestInstallTrimsTrailingSlashFromBase(t *testing.T) {
	got := runCore(t, fixtureOptions{baseSuffix: "/"})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	trimmed := strings.TrimRight(got.base, "/")
	if got.res.ReleaseURL != trimmed+"/latest/download" {
		t.Errorf("result release URL=%q want %q", got.res.ReleaseURL, trimmed+"/latest/download")
	}
	if want := "/latest/download/" + got.artifact; got.requests[0] != want {
		t.Fatalf("download paths=%v want %v", got.requests, want)
	}
}

// The core requires an explicit InstallDir; platform defaults and
// environment resolution belong to the root CLI.
func TestInstallRequiresExplicitInstallDir(t *testing.T) {
	_, err := Run(context.Background(), Options{OS: "linux", Architecture: "amd64", Client: noNetworkClient()})
	if err == nil || !strings.Contains(err.Error(), "InstallDir") {
		t.Fatalf("err=%v", err)
	}
}

// Unsupported OS/arch values are rejected before any network or filesystem
// mutation.
func TestInstallRejectsUnsupportedPlatform(t *testing.T) {
	for _, test := range []struct{ os, arch, want string }{
		{"freebsd", "amd64", "unsupported operating system: freebsd"},
		{"darwin", "riscv64", "unsupported architecture: riscv64"},
	} {
		t.Run(test.os+"/"+test.arch, func(t *testing.T) {
			_, err := Run(context.Background(), Options{
				Client:       noNetworkClient(),
				OS:           test.os,
				Architecture: test.arch,
				InstallDir:   t.TempDir(),
				ReleaseBase:  "https://example.com/releases",
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// The artifact follows the requested platform, not the test host: this
// proves the core downloads for the target and never needs the host to be
// that platform.
func TestInstallTargetsRequestedPlatform(t *testing.T) {
	for _, test := range []struct{ os, arch string }{
		{"windows", "amd64"},
		{"linux", "arm64"},
		{"darwin", "amd64"},
	} {
		t.Run(test.os+"/"+test.arch, func(t *testing.T) {
			got := runCore(t, fixtureOptions{os: test.os, arch: test.arch})
			if got.err != nil {
				t.Fatalf("install: %v", got.err)
			}
			artifact := artifactNameFor(test.os, test.arch)
			if want := "/latest/download/" + artifact; got.requests[0] != want {
				t.Fatalf("download paths=%v want %v", got.requests, want)
			}
			installed, err := os.ReadFile(filepath.Join(got.installDir, destinationName(test.os)))
			if err != nil || string(installed) != fakeBinary {
				t.Fatalf("installed %s=%q err=%v", destinationName(test.os), installed, err)
			}
			if got.res.Artifact != artifact {
				t.Errorf("result artifact=%q want %q", got.res.Artifact, artifact)
			}
		})
	}
}

// The old ps1 fixture installed into a directory with a space; the core
// must not assume shell quoting.
func TestInstallInstallDirMayContainSpaces(t *testing.T) {
	got := runCore(t, fixtureOptions{installDir: filepath.Join(t.TempDir(), "custom bin")})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	installed, err := os.ReadFile(got.res.Destination)
	if err != nil || string(installed) != fakeBinary {
		t.Fatalf("installed=%q err=%v", installed, err)
	}
	assertNoTempFiles(t, got.installDir)
}

// Replacement of a pre-existing destination: the old installers replaced
// the installed binary in place (mv -f / File.Replace); the core must do
// the same and carry the POSIX executable mode over.
func TestInstallReplacesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	old := []byte("old installed binary")
	destination := filepath.Join(dir, destinationName(runtime.GOOS))
	if err := os.WriteFile(destination, old, 0o644); err != nil {
		t.Fatal(err)
	}
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	installed, err := os.ReadFile(destination)
	if err != nil || string(installed) != fakeBinary {
		t.Fatalf("installed %s=%q err=%v", destination, installed, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(destination)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode=%v want 0755", destination, fi.Mode().Perm())
		}
	}
	assertNoTempFiles(t, dir)
}

// A cancelled context aborts the download and every temp file is removed;
// an already-cancelled context fails before any filesystem or network
// mutation.
func TestInstallCancellationCleansUpTempFiles(t *testing.T) {
	t.Run("mid-download cancel", func(t *testing.T) {
		hostOS := runtime.GOOS
		artifact := artifactNameFor(hostOS, runtime.GOARCH)
		digest := sha256Hex([]byte(fakeBinary))
		started := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
				_, _ = io.WriteString(w, digest+"  "+artifact+"\n")
			case strings.HasSuffix(r.URL.Path, "/"+artifact):
				select {
				case <-started:
				default:
					close(started)
				}
				_, _ = io.WriteString(w, "partial-bytes")
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				<-r.Context().Done()
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		installDir := filepath.Join(t.TempDir(), "bin")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := Run(ctx, Options{Client: hermeticClient(server), InstallDir: installDir, ReleaseBase: server.URL})
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("artifact download did not start")
		}
		time.Sleep(100 * time.Millisecond) // let the client read the partial body
		cancel()
		err := <-done
		if err == nil {
			t.Fatal("cancel did not fail the install")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err=%v want context.Canceled in the chain", err)
		}
		if _, statErr := os.Stat(filepath.Join(installDir, destinationName(hostOS))); !os.IsNotExist(statErr) {
			t.Fatalf("cancel left destination: %v", statErr)
		}
		assertNoTempFiles(t, installDir)
	})
	t.Run("already cancelled", func(t *testing.T) {
		installDir := filepath.Join(t.TempDir(), "bin")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Run(ctx, Options{InstallDir: installDir, Client: noNetworkClient()})
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		assertDirAbsent(t, installDir)
	})
}

// useReplaceSeams sets the replacement seams for one test and restores the
// defaults in its cleanup (the package's tests do not run in parallel). A
// nil rename or remove keeps os.Rename or os.Remove.
func useReplaceSeams(t *testing.T, moveAside bool, rename func(string, string) error, remove func(string) error) {
	t.Helper()
	savedMoveAside, savedRename, savedRemove, savedWrite := moveAsideOnRefusal, renameFile, removeFile, writeFile
	t.Cleanup(func() {
		moveAsideOnRefusal, renameFile, removeFile, writeFile = savedMoveAside, savedRename, savedRemove, savedWrite
	})
	moveAsideOnRefusal = moveAside
	if rename != nil {
		renameFile = rename
	}
	if remove != nil {
		removeFile = remove
	}
}

// refusingRename is os.Rename except for the renames of an install temp
// file (.soho.*) onto destination: the first `refusals` of them fail as a
// running Windows executable makes them fail (a negative count refuses
// every one). calls records every rename as "src-base>dst-base", in order.
func refusingRename(destination string, refusals int, calls *[]string) func(string, string) error {
	return func(src, dst string) error {
		*calls = append(*calls, filepath.Base(src)+">"+filepath.Base(dst))
		if dst == destination && strings.HasPrefix(filepath.Base(src), ".soho.") && refusals != 0 {
			refusals--
			return &os.LinkError{Op: "rename", Old: src, New: dst, Err: errors.New("simulated: the file is in use")}
		}
		return os.Rename(src, dst)
	}
}

// writeFiles creates each name in dir with its content.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// markerFor is the content of the provenance marker of a backup holding
// content: its sha256 and a newline.
func markerFor(content string) string {
	return sha256Hex([]byte(content)) + "\n"
}

// assertFile fails unless path holds exactly want.
func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q (err %v); want %q", path, got, err, want)
	}
}

// A refused direct rename with the fallback on (what Windows does for a
// running destination): the destination moves to .old, the verified bytes
// take its place, the .old gets its marker with the sha256 of the bytes
// moved, and Result names both. The order of the renames is the contract:
// direct try, move aside, then the new binary.
func TestInstallMoveAsideReplacesARefusedDestination(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{name: "running binary"})
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, destination, fakeBinary)
	assertFile(t, destination+".old", "running binary")
	assertFile(t, destination+".old.soho-install", markerFor("running binary"))
	if got.res.Previous != destination+".old" || got.res.PreviousMarker != destination+".old.soho-install" {
		t.Fatalf("Previous = %q, PreviousMarker = %q; want %q and its marker", got.res.Previous, got.res.PreviousMarker, destination+".old")
	}
	if len(calls) != 3 || !strings.HasSuffix(calls[0], ">"+name) || calls[1] != name+">"+name+".old" || !strings.HasSuffix(calls[2], ">"+name) || calls[0] != calls[2] {
		t.Fatalf("renames = %v; want temp>%s, %s>%s.old, temp>%s", calls, name, name, name, name)
	}
	assertDirExact(t, dir, name, name+".old", name+".old.soho-install")
}

// When the new binary cannot be renamed onto the destination even after
// the move aside, the move aside is undone: the old destination is back
// with its bytes, no .old and no marker are left, the error surfaces, and
// the temp files are removed.
func TestInstallMoveAsideUndoesWhenTheSecondRenameFails(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{name: "running binary"})
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, -1, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err == nil || !strings.Contains(got.err.Error(), "after moving it aside") {
		t.Fatalf("install err = %v; want the second rename's failure", got.err)
	}
	assertFile(t, destination, "running binary")
	if len(calls) != 4 || calls[3] != name+".old>"+name {
		t.Fatalf("renames = %v; want the last one to undo the move aside (%s.old>%s)", calls, name, name)
	}
	assertDirExact(t, dir, name)
}

// Backups an earlier install marked (.old and a .old-<n>, each with a
// marker holding its sha256) are removed with their markers at the start
// of the replacement, also when the direct rename then succeeds; that
// direct rename creates no .old and leaves Previous empty.
func TestInstallMoveAsideRemovesAnEarlierOld(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{
		name:                         "previous binary",
		name + ".old":                "older binary",
		name + ".old.soho-install":   markerFor("older binary"),
		name + ".old-2":              "oldest binary",
		name + ".old-2.soho-install": markerFor("oldest binary"),
	})
	useReplaceSeams(t, true, nil, nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, destination, fakeBinary)
	if got.res.Previous != "" || got.res.PreviousMarker != "" {
		t.Fatalf("Previous = %q, PreviousMarker = %q; want empty after a direct rename", got.res.Previous, got.res.PreviousMarker)
	}
	assertDirExact(t, dir, name)
}

// A user's file with a backup name and no marker (the reviewer's probe: a
// soho.old-7 with data) is never removed: it keeps its bytes after a
// direct replacement, and after a move aside, which takes the free .old.
func TestInstallMoveAsideKeepsAnUnmarkedFile(t *testing.T) {
	for _, refusals := range []int{0, 1} {
		t.Run(fmt.Sprintf("refusals=%d", refusals), func(t *testing.T) {
			dir := t.TempDir()
			name := destinationName(runtime.GOOS)
			destination := filepath.Join(dir, name)
			writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old-7": "USER DATA"})
			var calls []string
			useReplaceSeams(t, true, refusingRename(destination, refusals, &calls), nil)
			got := runCore(t, fixtureOptions{installDir: dir})
			if got.err != nil {
				t.Fatalf("install: %v", got.err)
			}
			assertFile(t, destination, fakeBinary)
			assertFile(t, destination+".old-7", "USER DATA")
			if refusals == 0 {
				assertDirExact(t, dir, name, name+".old-7")
				return
			}
			if got.res.Previous != destination+".old" {
				t.Fatalf("Previous = %q; want %q", got.res.Previous, destination+".old")
			}
			assertDirExact(t, dir, name, name+".old", name+".old-7", name+".old.soho-install")
		})
	}
}

// A marked backup whose bytes no longer match its marker (the file was
// replaced) stays under its check name with its marker, and Stranded names
// it; a marker whose content is not a sha256 leaves its backup at its own
// name. The move aside skips both names, whose markers stay.
func TestInstallMoveAsideKeepsABackupWhoseShaDiffers(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{
		name:                         "running binary",
		name + ".old":                "USER DATA",
		name + ".old.soho-install":   markerFor("older binary"),
		name + ".old-1":              "older binary",
		name + ".old-1.soho-install": "not a digest\n",
	})
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	check := checkName(destination + ".old")
	assertFile(t, check, "USER DATA")
	assertFile(t, destination+".old.soho-install", markerFor("older binary"))
	assertFile(t, destination+".old-1", "older binary")
	assertFile(t, destination+".old-1.soho-install", "not a digest\n")
	assertFile(t, destination+".old-2", "running binary")
	if got.res.Previous != destination+".old-2" {
		t.Fatalf("Previous = %q; want %q", got.res.Previous, destination+".old-2")
	}
	if len(got.res.Stranded) != 1 || got.res.Stranded[0] != check {
		t.Fatalf("Stranded = %q; want [%q]", got.res.Stranded, check)
	}
	assertDirExact(t, dir, name, name+".old-1", name+".old-1.soho-install", name+".old-2", name+".old-2.soho-install", filepath.Base(check), name+".old.soho-install")
}

// A marker whose backup is gone is removed, and nothing else changes.
func TestInstallMoveAsideRemovesAnOrphanMarker(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old-3.soho-install": markerFor("gone")})
	useReplaceSeams(t, true, nil, nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertDirExact(t, dir, name)
}

// A marker that cannot be written does not fail the install: the backup
// is in place without a marker, Result.PreviousMarker is empty, and the
// next install keeps that backup.
func TestInstallMoveAsideWithoutAMarkerSucceedsAndKeepsTheBackup(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{name: "running binary"})
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), nil)
	writeFile = func(string, []byte, os.FileMode) error { return errors.New("simulated: marker not written") }
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	if got.res.Previous != destination+".old" || got.res.PreviousMarker != "" {
		t.Fatalf("Previous = %q, PreviousMarker = %q; want the .old without a marker", got.res.Previous, got.res.PreviousMarker)
	}
	assertFile(t, destination+".old", "running binary")
	assertDirExact(t, dir, name, name+".old")
	writeFile = writeNewFile
	again := runCore(t, fixtureOptions{installDir: dir})
	if again.err != nil {
		t.Fatalf("next install: %v", again.err)
	}
	assertFile(t, destination+".old", "running binary")
	assertDirExact(t, dir, name, name+".old")
}

// A destination the direct rename replaces (not running) is never moved
// aside, with the fallback on: one rename, no .old, Previous empty.
func TestInstallMoveAsideIsNotUsedWhenTheDirectRenameWorks(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{name: "previous binary"})
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 0, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, destination, fakeBinary)
	if got.res.Previous != "" || len(calls) != 1 {
		t.Fatalf("Previous = %q, renames = %v; want one direct rename", got.res.Previous, calls)
	}
	assertDirExact(t, dir, name)
}

// A marked .old and .old-1 that cannot be removed (their processes still
// run: Windows lets the cleanup rename them to their check names, but not
// remove them) do not fail the install: they stay under their check names
// with their markers, Stranded names both, and the destination moves to
// the first free .old-<n>, past the names their markers still hold.
func TestInstallMoveAsideUsesAFreeSuffixWhenTheOldStays(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{
		name:                         "running binary",
		name + ".old":                "older binary",
		name + ".old.soho-install":   markerFor("older binary"),
		name + ".old-1":              "oldest binary",
		name + ".old-1.soho-install": markerFor("oldest binary"),
	})
	inUse := map[string]bool{destination + ".old": true, destination + ".old-1": true}
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), func(path string) error {
		if inUse[strings.SplitN(path, asideCheckSuffix, 2)[0]] {
			return &os.PathError{Op: "remove", Path: path, Err: errors.New("simulated: the file is in use")}
		}
		return os.Remove(path)
	})
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, destination, fakeBinary)
	checkOld, checkOld1 := checkName(destination+".old"), checkName(destination+".old-1")
	assertFile(t, checkOld, "older binary")
	assertFile(t, checkOld1, "oldest binary")
	assertFile(t, destination+".old-2", "running binary")
	if got.res.Previous != destination+".old-2" {
		t.Fatalf("Previous = %q; want %q", got.res.Previous, destination+".old-2")
	}
	if strings.Join(got.res.Stranded, " ") != checkOld+" "+checkOld1 {
		t.Fatalf("Stranded = %q; want [%q %q]", got.res.Stranded, checkOld, checkOld1)
	}
	assertDirExact(t, dir, name, filepath.Base(checkOld1), name+".old-1.soho-install", name+".old-2", name+".old-2.soho-install", filepath.Base(checkOld), name+".old.soho-install")
}

// No free name from .old to .old-<maxAsideSuffix> (all held by unmarked
// files): the install fails, the destination keeps its bytes and nothing
// new is created.
func TestInstallMoveAsideFailsWhenNoAsideNameIsFree(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	files := map[string]string{name: "running binary", name + ".old": "x"}
	for n := 1; n <= maxAsideSuffix; n++ {
		files[fmt.Sprintf("%s.old-%d", name, n)] = "x"
	}
	writeFiles(t, dir, files)
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err == nil || !strings.Contains(got.err.Error(), "no free name") {
		t.Fatalf("install err = %v; want no free name", got.err)
	}
	assertFile(t, destination, "running binary")
	if len(calls) != 1 {
		t.Fatalf("renames = %v; want only the refused direct one", calls)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != len(files) {
		t.Fatalf("dir holds %d entries (err %v); want the %d files written", len(entries), err, len(files))
	}
}

// symlinkOrSkip makes link point at target, and skips the test where the
// platform refuses symlinks (Windows without the privilege).
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
}

// checkName is the name the cleanup renames backup to while it checks it.
func checkName(backup string) string {
	return fmt.Sprintf("%s%s%d", backup, asideCheckSuffix, os.Getpid())
}

// The reviewer's dangling-marker probe: a soho.old.soho-install symlink to
// a path that does not exist takes the .old name, so the move aside uses
// .old-1, nothing is created at the link's target, and the link stays.
func TestInstallMoveAsideNeverWritesThroughADanglingMarkerLink(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	outside := filepath.Join(t.TempDir(), "user-file")
	writeFiles(t, dir, map[string]string{name: "running binary"})
	symlinkOrSkip(t, outside, destination+".old.soho-install")
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	if _, err := os.Lstat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(%s) = %v; want nothing written through the link", outside, err)
	}
	if got.res.Previous != destination+".old-1" || got.res.PreviousMarker != destination+".old-1.soho-install" {
		t.Fatalf("Previous = %q, PreviousMarker = %q; want .old-1 and its marker", got.res.Previous, got.res.PreviousMarker)
	}
	assertFile(t, destination+".old-1", "running binary")
	assertFile(t, destination+".old-1.soho-install", markerFor("running binary"))
	if target, err := os.Readlink(destination + ".old.soho-install"); err != nil || target != outside {
		t.Fatalf("Readlink = %q (err %v); want the link kept on %s", target, err, outside)
	}
	assertDirExact(t, dir, name, name+".old-1", name+".old-1.soho-install", name+".old.soho-install")
}

// The reviewer's write-only probe: a marker symlink to a user's file that
// can be written but not read never has that file overwritten.
func TestInstallMoveAsideKeepsAWriteOnlyMarkerTarget(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	target := filepath.Join(t.TempDir(), "user-file")
	if err := os.WriteFile(target, []byte("USER DATA"), 0o200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o600) })
	if _, err := os.ReadFile(target); err == nil {
		t.Skip("read permission is not enforced here")
	}
	writeFiles(t, dir, map[string]string{name: "running binary"})
	symlinkOrSkip(t, target, destination+".old.soho-install")
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	assertFile(t, target, "USER DATA")
	if got.res.Previous != destination+".old-1" {
		t.Fatalf("Previous = %q; want %q", got.res.Previous, destination+".old-1")
	}
}

// A user's file named like a marker (soho.old.soho-install, not a sha256)
// with no soho.old takes the .old name: it keeps its bytes, and the move
// aside uses .old-1.
func TestInstallMoveAsideTreatsAnExistingMarkerAsTaken(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{name: "running binary", name + ".old.soho-install": "USER DATA"})
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, destination+".old.soho-install", "USER DATA")
	if got.res.Previous != destination+".old-1" || got.res.PreviousMarker != destination+".old-1.soho-install" {
		t.Fatalf("Previous = %q, PreviousMarker = %q; want .old-1 and its marker", got.res.Previous, got.res.PreviousMarker)
	}
	assertDirExact(t, dir, name, name+".old-1", name+".old-1.soho-install", name+".old.soho-install")
}

// A marker that appears at the free name between the choice of that name
// and the marker's creation (a racer's file) is neither overwritten nor
// removed: the backup stays without a marker.
func TestInstallMoveAsideNeverOverwritesAMarkerThatAppears(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	writeFiles(t, dir, map[string]string{name: "running binary"})
	var calls []string
	refusing := refusingRename(destination, 1, &calls)
	useReplaceSeams(t, true, func(src, dst string) error {
		err := refusing(src, dst)
		if err == nil && src == destination && dst == destination+".old" {
			writeFiles(t, dir, map[string]string{name + ".old.soho-install": "USER DATA"})
		}
		return err
	}, nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, destination+".old.soho-install", "USER DATA")
	if got.res.Previous != destination+".old" || got.res.PreviousMarker != "" {
		t.Fatalf("Previous = %q, PreviousMarker = %q; want the .old without a marker", got.res.Previous, got.res.PreviousMarker)
	}
	assertDirExact(t, dir, name, name+".old", name+".old.soho-install")
}

// A marker that is a symlink, even to a file holding the backup's sha256,
// authorizes nothing: the backup, the link and its target stay.
func TestInstallMoveAsideKeepsABackupWhoseMarkerIsALink(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	outside := filepath.Join(t.TempDir(), "digest")
	if err := os.WriteFile(outside, []byte(markerFor("older binary")), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old": "older binary"})
	symlinkOrSkip(t, outside, destination+".old.soho-install")
	useReplaceSeams(t, true, nil, nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, destination+".old", "older binary")
	assertFile(t, outside, markerFor("older binary"))
	assertDirExact(t, dir, name, name+".old", name+".old.soho-install")
}

// A backup that is a symlink to a file whose sha256 the marker holds is
// kept under its check name, with its marker, and Stranded names it: only
// a regular file is removed.
func TestInstallMoveAsideKeepsABackupThatIsALink(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	outside := filepath.Join(t.TempDir(), "older")
	if err := os.WriteFile(outside, []byte("older binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old.soho-install": markerFor("older binary")})
	symlinkOrSkip(t, outside, destination+".old")
	useReplaceSeams(t, true, nil, nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	check := checkName(destination + ".old")
	if target, err := os.Readlink(check); err != nil || target != outside {
		t.Fatalf("Readlink = %q (err %v); want the link kept on %s", target, err, outside)
	}
	assertFile(t, outside, "older binary")
	if len(got.res.Stranded) != 1 || got.res.Stranded[0] != check {
		t.Fatalf("Stranded = %q; want [%q]", got.res.Stranded, check)
	}
	assertDirExact(t, dir, name, filepath.Base(check), name+".old.soho-install")
}

// The reviewer's cleanup-race probe: a file put at soho.old after the
// cleanup checked the backup and before it removed it is not the file
// removed. The checked one (under its check name) and the marker go; the
// new file stays with its bytes.
func TestInstallMoveAsideCleanupRemovesOnlyTheCheckedFile(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	backup := destination + ".old"
	writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old": "older binary", name + ".old.soho-install": markerFor("older binary")})
	var removed []string
	useReplaceSeams(t, true, nil, func(path string) error {
		if path == backup || path == checkName(backup) {
			writeFiles(t, dir, map[string]string{name + ".old": "NEW USER DATA"})
		}
		removed = append(removed, filepath.Base(path))
		return os.Remove(path)
	})
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, backup, "NEW USER DATA")
	want := []string{filepath.Base(checkName(backup)), name + ".old.soho-install"}
	if strings.Join(removed, " ") != strings.Join(want, " ") {
		t.Fatalf("removed %q; want %q", removed, want)
	}
	assertDirExact(t, dir, name, name+".old")
}

// A backup whose sha256 does not match its marker is renamed to its check
// name and left there with its bytes, next to its marker: that rename is
// the only one the cleanup makes, nothing is renamed back to soho.old, and
// Stranded names the check name.
func TestInstallMoveAsideLeavesAMismatchUnderItsCheckName(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	backup := destination + ".old"
	check := checkName(backup)
	writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old": "USER DATA", name + ".old.soho-install": markerFor("older binary")})
	var calls []string
	useReplaceSeams(t, true, func(src, dst string) error {
		calls = append(calls, filepath.Base(src)+">"+filepath.Base(dst))
		return os.Rename(src, dst)
	}, nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	if len(calls) != 2 || calls[0] != name+".old>"+filepath.Base(check) || !strings.HasSuffix(calls[1], ">"+name) {
		t.Fatalf("renames %q; want %s.old>%s, then the direct temp>%s", calls, name, filepath.Base(check), name)
	}
	assertFile(t, check, "USER DATA")
	assertFile(t, backup+".soho-install", markerFor("older binary"))
	if len(got.res.Stranded) != 1 || got.res.Stranded[0] != check {
		t.Fatalf("Stranded = %q; want [%q]", got.res.Stranded, check)
	}
	assertDirExact(t, dir, name, filepath.Base(check), name+".old.soho-install")
}

// The reviewer's rollback-collision probe: a file put at soho.old while
// the cleanup checks the renamed backup (right after the rename to the
// check name) keeps its bytes when that backup does not match; the backup
// stays under its check name and Stranded names it.
func TestInstallMoveAsideKeepsAFileThatAppearsDuringTheCheck(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	backup := destination + ".old"
	check := checkName(backup)
	writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old": "MISMATCHED BACKUP", name + ".old.soho-install": markerFor("older binary")})
	useReplaceSeams(t, true, func(src, dst string) error {
		err := os.Rename(src, dst)
		if err == nil && src == backup && dst == check {
			writeFiles(t, dir, map[string]string{name + ".old": "NEW USER DATA"})
		}
		return err
	}, nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, backup, "NEW USER DATA")
	assertFile(t, check, "MISMATCHED BACKUP")
	assertFile(t, backup+".soho-install", markerFor("older binary"))
	if len(got.res.Stranded) != 1 || got.res.Stranded[0] != check {
		t.Fatalf("Stranded = %q; want [%q]", got.res.Stranded, check)
	}
	assertDirExact(t, dir, name, name+".old", filepath.Base(check), name+".old.soho-install")
}

// A file already at a backup's check name is never replaced by the
// cleanup's rename: that backup is skipped and stays with its marker.
func TestInstallMoveAsideKeepsAFileAtTheCheckName(t *testing.T) {
	dir := t.TempDir()
	name := destinationName(runtime.GOOS)
	destination := filepath.Join(dir, name)
	check := checkName(destination + ".old")
	writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old": "older binary", name + ".old.soho-install": markerFor("older binary"), filepath.Base(check): "USER DATA"})
	useReplaceSeams(t, true, nil, nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err != nil {
		t.Fatalf("install: %v", got.err)
	}
	assertFile(t, check, "USER DATA")
	assertFile(t, destination+".old", "older binary")
	assertDirExact(t, dir, name, name+".old", filepath.Base(check), name+".old.soho-install")
}

// Off Windows nothing changes: the fallback is off by default, a .old next
// to the destination is neither removed nor created (a marked one
// included), and a refused rename is the error it always was, with no
// move aside.
func TestInstallOffWindowsNeverMovesAside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fallback is on by default on Windows")
	}
	if moveAsideOnRefusal {
		t.Fatal("moveAsideOnRefusal is on off Windows")
	}
	t.Run("a direct replace keeps a .old", func(t *testing.T) {
		dir := t.TempDir()
		name := destinationName(runtime.GOOS)
		writeFiles(t, dir, map[string]string{name: "previous binary", name + ".old": "user file", name + ".old.soho-install": markerFor("user file")})
		got := runCore(t, fixtureOptions{installDir: dir})
		if got.err != nil || got.res.Previous != "" {
			t.Fatalf("install: %v, Previous %q", got.err, got.res.Previous)
		}
		assertFile(t, filepath.Join(dir, name+".old"), "user file")
		assertDirExact(t, dir, name, name+".old", name+".old.soho-install")
	})
	t.Run("a refused rename fails without a move aside", func(t *testing.T) {
		dir := t.TempDir()
		name := destinationName(runtime.GOOS)
		destination := filepath.Join(dir, name)
		writeFiles(t, dir, map[string]string{name: "previous binary"})
		var calls []string
		useReplaceSeams(t, false, refusingRename(destination, 1, &calls), nil)
		got := runCore(t, fixtureOptions{installDir: dir})
		if got.err == nil || len(calls) != 1 {
			t.Fatalf("install err = %v, renames = %v; want the refused rename's error only", got.err, calls)
		}
		assertFile(t, destination, "previous binary")
		assertDirExact(t, dir, name)
	})
}

// A refused rename with no destination is not a running binary: the error
// surfaces and nothing is moved aside.
func TestInstallMoveAsideNeedsAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, destinationName(runtime.GOOS))
	var calls []string
	useReplaceSeams(t, true, refusingRename(destination, 1, &calls), nil)
	got := runCore(t, fixtureOptions{installDir: dir})
	if got.err == nil || len(calls) != 1 {
		t.Fatalf("install err = %v, renames = %v; want the refused rename's error only", got.err, calls)
	}
	assertDirExact(t, dir)
}
