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
	"strconv"
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
// <destination>.old-<n>), and empty otherwise. PreviousMarker is the
// provenance marker written next to it (<Previous>.soho-install), which
// lets the next install remove that backup; it is empty when the marker
// could not be written, and the backup then stays until removed by hand.
// Stranded lists the check names (<backup>.soho-check-<pid>) of earlier
// backups the cleanup renamed to check them and left there, next to their
// markers: the bytes did not match the marker, or the file could not be
// removed.
type Result struct {
	Artifact       string
	Destination    string
	Digest         string
	Previous       string
	PreviousMarker string
	ReleaseURL     string
	Stranded       []string
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
	previous, marker, stranded, err := replaceDestination(binPath, destination)
	if err != nil {
		if len(stranded) > 0 {
			return Result{}, fmt.Errorf("%w (an earlier backup did not match its marker or could not be removed, and is at %s)", err, strings.Join(stranded, ", "))
		}
		return Result{}, err
	}
	return Result{
		Artifact:       artifact,
		Destination:    destination,
		Digest:         digest,
		Previous:       previous,
		PreviousMarker: marker,
		ReleaseURL:     releaseURL,
		Stranded:       stranded,
	}, nil
}

// Seams for the replacement step: moveAsideOnRefusal turns on the Windows
// fallback (tests turn it on elsewhere to exercise it portably), and
// renameFile, removeFile and writeFile are os.Rename, os.Remove and
// writeNewFile except in tests that simulate a refusal.
var (
	moveAsideOnRefusal = runtime.GOOS == "windows"
	renameFile         = os.Rename
	removeFile         = os.Remove
	writeFile          = writeNewFile
)

// writeNewFile creates path with data, and fails when path exists, a
// symlink included, dangling or not: O_CREATE|O_EXCL never follows a link
// (open(2) fails with EEXIST; on Windows it is CreateFile with CREATE_NEW
// and FILE_FLAG_OPEN_REPARSE_POINT, see syscall.Open). A file it created
// and could not fill is removed; an object that was already at path is
// never touched.
func writeNewFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// maxAsideSuffix bounds the <destination>.old-<n> names tried when
// <destination>.old is taken.
const maxAsideSuffix = 100

// asideMarkerSuffix names the provenance marker of a backup:
// <backup>.soho-install holds the sha256 of the bytes the install moved to
// <backup>. Only a backup whose marker matches its bytes is removed later.
const asideMarkerSuffix = ".soho-install"

// asideCheckSuffix names the check name a backup is renamed to while the
// cleanup hashes and removes it: <backup>.soho-check-<pid>.
const asideCheckSuffix = ".soho-check-"

// replaceDestination renames the verified binPath onto destination and
// returns the path the previous destination was moved to and the path of
// that backup's marker, both "" when the direct rename replaced it; the
// marker is "" also when it could not be written. stranded lists the
// earlier backups left under a check name (see removeEarlierAside).
//
// Same-directory rename: one atomic operation on POSIX, and the only step
// off Windows. On Windows os.Rename is MoveFileEx with
// MOVEFILE_REPLACE_EXISTING (see internal/syscall/windows.Rename), which
// replaces a normal destination but is refused for an executable that is
// running (for example, the soho.exe running this install). Windows still
// lets a running executable be read and renamed, so when the direct rename
// fails and the destination exists, the sha256 of the destination is read,
// the destination is moved to the first free name among <destination>.old
// and <destination>.old-<n>, then binPath is renamed onto destination; if
// that second rename fails, the first one is undone and the error is
// returned, so the old destination stays in place. Once the new binary is
// in place, <backup>.soho-install is created with the sha256 read before
// the move, only if nothing is at that name (see writeNewFile); a failure
// to read or create it is not an error, and leaves a backup that no later
// install removes. Backups an earlier install marked are removed at the
// start, before the direct rename (see removeEarlierAside); a failure to
// remove one is not an error.
func replaceDestination(binPath, destination string) (string, string, []string, error) {
	if !moveAsideOnRefusal {
		if err := renameFile(binPath, destination); err != nil {
			return "", "", nil, fmt.Errorf("install: replacing %s: %w", destination, err)
		}
		return "", "", nil, nil
	}
	stranded := removeEarlierAside(destination)
	err := renameFile(binPath, destination)
	if err == nil {
		return "", "", stranded, nil
	}
	if _, statErr := os.Lstat(destination); statErr != nil {
		return "", "", stranded, fmt.Errorf("install: replacing %s: %w", destination, err)
	}
	aside, asideErr := freeAsidePath(destination)
	if asideErr != nil {
		return "", "", stranded, fmt.Errorf("install: replacing %s: %w (%v)", destination, err, asideErr)
	}
	previousDigest, digestErr := sha256File(destination)
	if moveErr := renameFile(destination, aside); moveErr != nil {
		return "", "", stranded, fmt.Errorf("install: replacing %s: %w (moving it to %s: %v)", destination, err, aside, moveErr)
	}
	if err := renameFile(binPath, destination); err != nil {
		if undoErr := renameFile(aside, destination); undoErr != nil {
			return "", "", stranded, fmt.Errorf("install: replacing %s: %w (the previous binary is at %s: %v)", destination, err, aside, undoErr)
		}
		return "", "", stranded, fmt.Errorf("install: replacing %s after moving it aside: %w", destination, err)
	}
	if digestErr != nil {
		return aside, "", stranded, nil
	}
	marker := aside + asideMarkerSuffix
	if err := writeFile(marker, []byte(previousDigest+"\n"), 0o644); err != nil {
		return aside, "", stranded, nil
	}
	return aside, marker, stranded, nil
}

// asideNames lists the backup names of destination: <destination>.old,
// then <destination>.old-1 to <destination>.old-<maxAsideSuffix>.
func asideNames(destination string) []string {
	names := []string{destination + ".old"}
	for n := 1; n <= maxAsideSuffix; n++ {
		names = append(names, fmt.Sprintf("%s.old-%d", destination, n))
	}
	return names
}

// removeEarlierAside removes the backups an earlier install made and
// returns the check names of those it left in place. Only a
// regular-file marker (Lstat: not a symlink) is read; any other marker
// authorizes nothing, and the backup and the marker stay, as they do when
// the marker does not hold a sha256. A marker holding one whose backup is
// gone is removed. Otherwise the backup is renamed to its
// check name (<backup>.soho-check-<pid>, skipped when that name is taken),
// and the renamed file is removed, then the marker, only when it is a
// regular file whose sha256 is the one the marker holds; a file put at
// <backup> after the rename is not the one hashed and removed. A renamed
// file that is not a regular file, does not match, or cannot be removed
// (its process may still run) stays under the check name, next to its
// marker, and that check name is returned: it is never renamed back, which
// would overwrite a file put at <backup> meanwhile. The rename to the
// check name is the only rename here; a file put at the check name between
// its Lstat and that rename is replaced, a window left open. A process that
// already had the backup open for writing can still change it after the
// hash; that window is left open too. No failure here is an error.
func removeEarlierAside(destination string) []string {
	var stranded []string
	for _, backup := range asideNames(destination) {
		marker := backup + asideMarkerSuffix
		info, err := os.Lstat(marker)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			continue
		}
		want := strings.ToLower(strings.TrimSpace(string(data)))
		if !isHex64(want) {
			continue
		}
		if _, err := os.Lstat(backup); errors.Is(err, os.ErrNotExist) {
			_ = removeFile(marker)
			continue
		}
		check := backup + asideCheckSuffix + strconv.Itoa(os.Getpid())
		if _, err := os.Lstat(check); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if renameFile(backup, check) != nil {
			continue
		}
		if checkedRemove(check, want) {
			_ = removeFile(marker)
			continue
		}
		stranded = append(stranded, check)
	}
	return stranded
}

// checkedRemove removes check and reports true only when it is a regular
// file (Lstat) whose sha256 is want.
func checkedRemove(check, want string) bool {
	info, err := os.Lstat(check)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	got, err := sha256File(check)
	if err != nil || got != want {
		return false
	}
	return removeFile(check) == nil
}

// freeAsidePath returns the first backup name (see asideNames) that is
// free: neither the backup nor its marker (<backup>.soho-install) exists.
// A name whose backup or marker is held by anything, a user's file or a
// symlink included, is skipped.
func freeAsidePath(destination string) (string, error) {
	for _, candidate := range asideNames(destination) {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := os.Lstat(candidate + asideMarkerSuffix); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free name from %s.old to %s.old-%d", destination, destination, maxAsideSuffix)
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
