// Package install is the native Go core of the release installers
// (install.sh and install.ps1). It downloads the soho artifact for a
// requested platform from a release base URL, verifies it against
// SHA256SUMS, and installs the soho binary inside the requested directory
// using only the Go standard library.
//
// The core never invokes a shell, curl, awk, shasum, PowerShell, Node, or
// Bun, never changes PATH or the registry, and never executes the
// downloaded bytes. The root CLI owns platform defaults and environment
// resolution, PATH handling, and running the installed binary.
package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// defaultReleaseBase is the verified production release location. It stays
// HTTPS even though tests may point ReleaseBase at an httptest server.
const defaultReleaseBase = "https://github.com/djalmajr/soho/releases"

// defaultHTTPTimeout bounds the whole request (headers and body) when
// Options.Client is nil.
const defaultHTTPTimeout = 5 * time.Minute

const (
	// binaryBaseName is the installed binary's base name (the .exe suffix
	// is added on windows).
	binaryBaseName = "soho"
	sumsFileName   = "SHA256SUMS"
)

// Options configures a single install run.
//
// OS and Architecture default to runtime.GOOS and runtime.GOARCH when
// empty and must then name a supported platform. InstallDir is always
// explicit and nonempty: the core does not resolve platform defaults or
// environment variables. ReleaseBase defaults to defaultReleaseBase. An
// empty Version selects the latest release.
type Options struct {
	Architecture string
	Client       *http.Client
	InstallDir   string
	OS           string
	ReleaseBase  string
	Version      string
}

// Result describes a successful installation. Digest is the verified
// lowercase sha256 of the installed bytes. ReleaseURL is the download base
// (without the artifact or SHA256SUMS suffix) that was requested.
// Destination is the freshly installed soho binary (what SOHO_BIN points
// at). Previous is where the running destination was moved on Windows
// when it could not be replaced directly (<destination>.old or
// <destination>.old-<n>), and empty otherwise.
type Result struct {
	Artifact    string
	Destination string
	Digest      string
	Previous    string
	ReleaseURL  string
}

// supportedOS mirrors the platforms the old installers accepted, in
// canonical Go names.
var supportedOS = map[string]bool{"darwin": true, "linux": true, "windows": true}

// Run downloads the release artifact for the requested platform, verifies
// it against SHA256SUMS, and replaces the soho destination in
// Options.InstallDir with the verified bytes. Every input is validated
// before any network or filesystem mutation. Exclusive temp files are
// created in the requested directory and removed on every failure and
// cancellation path, and the existing destination is preserved when the
// verification fails: a failure never leaves the file at the new version
// and never touches a file that is not the install destination.
func Run(ctx context.Context, opts Options) (Result, error) {
	osName, err := normalizeOS(opts.OS)
	if err != nil {
		return Result{}, err
	}
	arch, err := normalizeArch(opts.Architecture)
	if err != nil {
		return Result{}, err
	}
	if opts.InstallDir == "" {
		return Result{}, errors.New("install: InstallDir must be explicit and nonempty")
	}
	base, err := normalizeReleaseBase(opts.ReleaseBase)
	if err != nil {
		return Result{}, err
	}
	if err := validateVersion(opts.Version); err != nil {
		return Result{}, err
	}
	artifact := artifactName(osName, arch)
	destination := installDest(opts.InstallDir, binaryBaseName, osName)
	releaseURL := base + "/latest/download"
	if opts.Version != "" {
		releaseURL = base + "/download/" + opts.Version
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("install: %w", err)
	}

	if err := os.MkdirAll(opts.InstallDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("install: creating install dir: %w", err)
	}
	binTmp, err := os.CreateTemp(opts.InstallDir, "."+binaryBaseName+".*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("install: creating temp file: %w", err)
	}
	sumsTmp, err := os.CreateTemp(opts.InstallDir, "."+binaryBaseName+".*.sums")
	if err != nil {
		_ = binTmp.Close()
		os.Remove(binTmp.Name())
		return Result{}, fmt.Errorf("install: creating temp file: %w", err)
	}
	binPath, sumsPath := binTmp.Name(), sumsTmp.Name()
	// removeTemp cleans every temp file on every path. After a successful
	// rename it is still correct: the missing file fails silently, and the
	// remaining temp is removed, mirroring the old installers.
	removeTemp := func() {
		_ = binTmp.Close()
		_ = os.Remove(binPath)
		_ = sumsTmp.Close()
		_ = os.Remove(sumsPath)
	}
	defer removeTemp()

	if err := download(ctx, client, releaseURL+"/"+artifact, binTmp); err != nil {
		return Result{}, fmt.Errorf("install: failed to download %s: %w", artifact, err)
	}
	if err := download(ctx, client, releaseURL+"/"+sumsFileName, sumsTmp); err != nil {
		return Result{}, fmt.Errorf("install: failed to download %s: %w", sumsFileName, err)
	}
	if err := binTmp.Close(); err != nil {
		return Result{}, fmt.Errorf("install: closing temp file: %w", err)
	}
	if err := sumsTmp.Close(); err != nil {
		return Result{}, fmt.Errorf("install: closing temp file: %w", err)
	}

	expected, err := expectedDigest(sumsPath, artifact)
	if err != nil {
		return Result{}, fmt.Errorf("install: %w", err)
	}
	digest, err := sha256File(binPath)
	if err != nil {
		return Result{}, fmt.Errorf("install: hashing %s: %w", artifact, err)
	}
	// Both sides are lowercase, so an uppercase SHA256SUMS line still
	// verifies.
	if digest != expected {
		return Result{}, fmt.Errorf("install: sha256 mismatch for %s", artifact)
	}

	// Executable mode on POSIX; the rename below carries the mode to the
	// destination. Windows needs no mode change. The temp file was closed
	// before this point: Windows refuses to rename a file that is still
	// open.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(binPath, 0o755); err != nil {
			return Result{}, fmt.Errorf("install: setting executable mode: %w", err)
		}
	}
	previous, err := replaceDestination(binPath, destination)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Artifact:    artifact,
		Destination: destination,
		Digest:      digest,
		Previous:    previous,
		ReleaseURL:  releaseURL,
	}, nil
}

// Seams for the replacement step: moveAsideOnRefusal turns on the Windows
// fallback (tests turn it on elsewhere to exercise it portably), and
// renameFile and removeFile are os.Rename and os.Remove except in tests
// that simulate a refusal.
var (
	moveAsideOnRefusal = runtime.GOOS == "windows"
	renameFile         = os.Rename
	removeFile         = os.Remove
)

// maxAsideSuffix bounds the <destination>.old-<n> names tried when an
// earlier <destination>.old cannot be removed.
const maxAsideSuffix = 100

// replaceDestination renames the verified binPath onto destination and
// returns the path the previous destination was moved to, or "" when the
// direct rename replaced it.
//
// Same-directory rename: one atomic operation on POSIX, and the only step
// off Windows. On Windows os.Rename is MoveFileEx with
// MOVEFILE_REPLACE_EXISTING (see internal/syscall/windows.Rename), which
// replaces a normal destination but is refused for an executable that is
// running (for example, the soho.exe running this install). Windows still
// lets a running executable be renamed, so when the direct rename fails and
// the destination exists, the destination is moved to <destination>.old
// (or the first free <destination>.old-<n> when an earlier .old cannot be
// removed), then binPath is renamed onto destination; if that second
// rename fails, the first one is undone and the error is returned, so the
// old destination stays in place. A <destination>.old left by an earlier
// install (and any <destination>.old-<n>) is removed at the start, before
// the direct rename; a failure to remove it is not an error.
func replaceDestination(binPath, destination string) (string, error) {
	if !moveAsideOnRefusal {
		if err := renameFile(binPath, destination); err != nil {
			return "", fmt.Errorf("install: replacing %s: %w", destination, err)
		}
		return "", nil
	}
	removeEarlierAside(destination)
	err := renameFile(binPath, destination)
	if err == nil {
		return "", nil
	}
	if _, statErr := os.Lstat(destination); statErr != nil {
		return "", fmt.Errorf("install: replacing %s: %w", destination, err)
	}
	aside, asideErr := freeAsidePath(destination)
	if asideErr != nil {
		return "", fmt.Errorf("install: replacing %s: %w (%v)", destination, err, asideErr)
	}
	if moveErr := renameFile(destination, aside); moveErr != nil {
		return "", fmt.Errorf("install: replacing %s: %w (moving it to %s: %v)", destination, err, aside, moveErr)
	}
	if err := renameFile(binPath, destination); err != nil {
		if undoErr := renameFile(aside, destination); undoErr != nil {
			return "", fmt.Errorf("install: replacing %s: %w (the previous binary is at %s: %v)", destination, err, aside, undoErr)
		}
		return "", fmt.Errorf("install: replacing %s after moving it aside: %w", destination, err)
	}
	return aside, nil
}

// removeEarlierAside tries to remove the <destination>.old and the
// <destination>.old-<n> an earlier install left; one that cannot be
// removed (its process may still run) stays, and is not an error.
func removeEarlierAside(destination string) {
	_ = removeFile(destination + ".old")
	for n := 1; n <= maxAsideSuffix; n++ {
		_ = removeFile(fmt.Sprintf("%s.old-%d", destination, n))
	}
}

// freeAsidePath returns <destination>.old when no such file exists,
// otherwise the first <destination>.old-<n> (n from 1 to maxAsideSuffix)
// that does not exist.
func freeAsidePath(destination string) (string, error) {
	candidate := destination + ".old"
	for n := 1; ; n++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if n > maxAsideSuffix {
			return "", fmt.Errorf("no free name from %s.old-1 to %s.old-%d", destination, destination, maxAsideSuffix)
		}
		candidate = fmt.Sprintf("%s.old-%d", destination, n)
	}
}

// installDest joins the install directory with the binary's file name for
// the target OS (the .exe suffix on windows).
func installDest(dir, baseName, osName string) string {
	if osName == "windows" {
		return filepath.Join(dir, baseName+".exe")
	}
	return filepath.Join(dir, baseName)
}

// normalizeOS applies the host default and the supported-platform rule.
func normalizeOS(osName string) (string, error) {
	if osName == "" {
		osName = runtime.GOOS
	}
	if !supportedOS[osName] {
		return "", fmt.Errorf("install: unsupported operating system: %s", osName)
	}
	return osName, nil
}

// normalizeArch applies the host default and the supported-architecture
// rule.
func normalizeArch(arch string) (string, error) {
	if arch == "" {
		arch = runtime.GOARCH
	}
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("install: unsupported architecture: %s", arch)
	}
	return arch, nil
}

// artifactName keeps the release asset naming:
// soho_<os>_<arch>, with the .exe suffix on windows.
func artifactName(osName, arch string) string {
	name := binaryBaseName + "_" + osName + "_" + arch
	if osName == "windows" {
		name += ".exe"
	}
	return name
}

// normalizeReleaseBase applies the default, trims a trailing slash, and
// validates the base before any network or filesystem mutation: whitespace
// anywhere, credentials, a query, or a fragment are rejected, and only
// http/https with a host is accepted.
func normalizeReleaseBase(base string) (string, error) {
	if base == "" {
		base = defaultReleaseBase
	}
	// The raw string must not carry whitespace before it is parsed: the old
	// installers rejected space, newline, and tab because such bases made
	// the shell and PowerShell URL handling unsafe.
	if idx := strings.IndexFunc(base, unicode.IsSpace); idx >= 0 {
		return "", errors.New("install: release base URL contains whitespace")
	}
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("install: invalid release base URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("install: release base URL must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("install: release base URL has no host")
	}
	if u.User != nil {
		return "", errors.New("install: release base URL contains credentials")
	}
	if u.RawQuery != "" {
		return "", errors.New("install: release base URL contains a query")
	}
	if u.Fragment != "" {
		return "", errors.New("install: release base URL contains a fragment")
	}
	return base, nil
}

// versionChars is the release character set the old install.sh accepted:
// v, digits, letters, dot, underscore, and dash.
const versionChars = "v0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz._-"

// validateVersion keeps the old tag rule: it must begin with v and use
// only release characters, and it must not contain ".." so a tag can never
// escape the /download/ path. Empty means the latest release and is valid.
func validateVersion(version string) error {
	if version == "" {
		return nil
	}
	if !strings.HasPrefix(version, "v") {
		return fmt.Errorf("install: version %q must begin with v", version)
	}
	for i := 0; i < len(version); i++ {
		if !strings.ContainsRune(versionChars, rune(version[i])) {
			return fmt.Errorf("install: version %q contains invalid characters", version)
		}
	}
	if strings.Contains(version, "..") {
		return fmt.Errorf("install: version %q must not contain %q", version, "..")
	}
	return nil
}

// download stores the response body in dst. Non-2xx statuses are failures,
// mirroring curl -f and Invoke-WebRequest.
func download(ctx context.Context, client *http.Client, rawURL string, dst *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		status := resp.Status
		if status == "" {
			status = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("unexpected status %s", status)
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

// expectedDigest reads SHA256SUMS and requires exactly one valid
// 64-hex-digest row for the artifact name. The optional '*' marker is
// accepted, lines for other files are ignored, and the returned digest is
// lowercase.
func expectedDigest(sumsPath, artifact string) (string, error) {
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", fmt.Errorf("reading SHA256SUMS: %w", err)
	}
	var found int
	var digest string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sum, name := fields[0], fields[1]
		if strings.HasPrefix(name, "*") {
			name = name[1:]
		}
		if name != artifact || !isHex64(sum) {
			continue
		}
		found++
		if found == 1 {
			digest = strings.ToLower(sum)
		}
	}
	if found != 1 {
		return "", fmt.Errorf("SHA256SUMS has no unique entry for %s", artifact)
	}
	return digest, nil
}

// isHex64 reports whether s is exactly 64 hexadecimal characters.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// sha256File returns the lowercase hex sha256 of the file contents.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
