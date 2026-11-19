// Package update resolves which release channel a running soho build follows
// and answers whether a newer release exists in it.
//
// The channel comes from the running binary's own version string, never from
// the environment: a pre-release suffix (v0.3.1-rc.18) follows the rc channel,
// a version without one follows stable, and a build that is not a released
// version ("dev") counts as stable for the check. The release list is the
// public GitHub REST endpoint, which is untrusted input: every tag is
// validated with install.ValidateVersion and parsed as a version before it is
// used, and a malformed response is an error, never an empty list.
//
// A network or parse failure is "not checked": the callers print that, never
// "up to date".
package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/djalmajr/soho/internal/install"
)

const (
	// ChannelStable is the channel of a version without a pre-release suffix.
	ChannelStable = "stable"
	// ChannelRC is the channel of a version with a pre-release suffix.
	ChannelRC = "rc"

	// DevVersion is the version of a build that is not a release. It counts
	// as stable for the check and is never refused as a downgrade.
	DevVersion = "dev"

	// DefaultAPI is the public release list: no token, no user data. Drafts
	// in it are ignored.
	DefaultAPI = "https://api.github.com/repos/djalmajr/soho/releases?per_page=50"
	// UserAgent identifies the request; the API requires one.
	UserAgent = "soho-update"

	// RequestTimeout bounds one releases request when Client.HTTP is nil. The
	// update command passes its own bound; the passive notice uses
	// NoticeTimeout.
	RequestTimeout = 5 * time.Second
	// NoticeTimeout bounds the passive check of doctor and init.
	NoticeTimeout = 2 * time.Second

	// EnvNoUpdateCheck turns the passive notice off ("1").
	EnvNoUpdateCheck = "SOHO_NO_UPDATE_CHECK"

	// maxResponseBytes bounds the release list read from the network.
	maxResponseBytes = 1 << 20
)

// Release is one published release of the project.
type Release struct {
	Tag        string
	Published  time.Time
	Prerelease bool
}

// PublishedDate is the release date as YYYY-MM-DD (UTC), or "" when the
// response carried no usable published_at.
func (r Release) PublishedDate() string {
	if r.Published.IsZero() {
		return ""
	}
	return r.Published.UTC().Format("2006-01-02")
}

// Version is a parsed release tag: three numeric fields and, for a
// pre-release, its dot-separated identifiers. The core fields and the numeric
// pre-release identifiers stay validated decimal strings: a release of any
// magnitude keeps its SemVer order, where a uint64 conversion would wrap and
// make two distinct identifiers compare equal.
type Version struct {
	Major, Minor, Patch string
	Pre                 []string
}

// ParseVersion parses a release tag (vMAJOR.MINOR.PATCH[-pre]). It applies the
// install tag rule first, so a tag can never name a path outside /download/.
func ParseVersion(raw string) (Version, error) {
	if err := install.ValidateVersion(raw); err != nil {
		return Version{}, err
	}
	if raw == "" {
		return Version{}, errors.New("update: empty version")
	}
	rest := strings.TrimPrefix(raw, "v")
	core, pre := rest, ""
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		core, pre = rest[:i], rest[i+1:]
		if pre == "" {
			return Version{}, fmt.Errorf("update: version %q has an empty pre-release", raw)
		}
	}
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return Version{}, fmt.Errorf("update: version %q is not MAJOR.MINOR.PATCH", raw)
	}
	for _, field := range fields {
		if err := validateNumber(field); err != nil {
			return Version{}, fmt.Errorf("update: version %q: %w", raw, err)
		}
	}
	v := Version{Major: fields[0], Minor: fields[1], Patch: fields[2]}
	if pre != "" {
		for _, ident := range strings.Split(pre, ".") {
			if ident == "" {
				return Version{}, fmt.Errorf("update: version %q has an empty pre-release identifier", raw)
			}
			for i := 0; i < len(ident); i++ {
				c := ident[i]
				if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
					return Version{}, fmt.Errorf("update: version %q has an invalid pre-release identifier %q", raw, ident)
				}
			}
			if isNumber(ident) && len(ident) > 1 && ident[0] == '0' {
				return Version{}, fmt.Errorf("update: version %q has a pre-release number with a leading zero %q", raw, ident)
			}
			v.Pre = append(v.Pre, ident)
		}
	}
	return v, nil
}

// validateNumber accepts a SemVer numeric identifier: decimal digits only, no
// leading zero unless the identifier is "0". The identifier is never
// converted to an integer, so its magnitude is unbounded.
func validateNumber(field string) error {
	if !isNumber(field) {
		return fmt.Errorf("%q is not a number", field)
	}
	if len(field) > 1 && field[0] == '0' {
		return fmt.Errorf("%q has a leading zero", field)
	}
	return nil
}

// compareNumeric orders two decimal identifiers by value: leading zeros are
// stripped first (SemVer numeric identifiers do not carry them; this keeps a
// caller that did not come through ParseVersion ordered too), then the longer
// identifier is the larger number, then the bytes decide. No integer
// conversion happens, so an identifier past the uint64 range keeps its order.
func compareNumeric(a, b string) int {
	a, b = trimNumericZeros(a), trimNumericZeros(b)
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func trimNumericZeros(n string) string {
	i := 0
	for i < len(n)-1 && n[i] == '0' {
		i++
	}
	return n[i:]
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Compare orders two parsed versions: -1, 0 or 1. A pre-release is lower than
// the release of the same MAJOR.MINOR.PATCH, and pre-release identifiers follow
// the semantic-versioning rules (numeric before alphanumeric, fewer fields
// lower on a common prefix). Numeric identifiers compare as validated decimal
// strings, so no magnitude overflows.
func Compare(a, b Version) int {
	for _, pair := range [][2]string{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if cmp := compareNumeric(pair[0], pair[1]); cmp != 0 {
			return cmp
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		xn, yn := isNumber(x), isNumber(y)
		switch {
		case xn && yn:
			if cmp := compareNumeric(x, y); cmp != 0 {
				return cmp
			}
		case xn != yn:
			if xn {
				return -1
			}
			return 1
		default:
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// ChannelFor is the channel of a running version: rc when it carries a
// pre-release suffix, stable otherwise (a dev build included).
func ChannelFor(version string) string {
	v, err := ParseVersion(version)
	if err != nil {
		return ChannelStable
	}
	if len(v.Pre) > 0 {
		return ChannelRC
	}
	return ChannelStable
}

// ParseChannel validates the --channel value. Empty means "the running
// version's channel" and is returned as "".
func ParseChannel(channel string) (string, error) {
	switch channel {
	case "":
		return "", nil
	case ChannelStable, ChannelRC:
		return channel, nil
	}
	return "", fmt.Errorf("update: unknown channel %q; use %s or %s", channel, ChannelStable, ChannelRC)
}

// Newer reports whether tag is a release the running version does not have. A
// running version that is not a released build (dev) has no position in the
// channel, so any release is newer than it.
func Newer(tag, current string) bool {
	next, err := ParseVersion(tag)
	if err != nil {
		return false
	}
	have, err := ParseVersion(current)
	if err != nil {
		return true
	}
	return Compare(next, have) > 0
}

// Same reports whether tag names the running release. Two tags that do not
// parse are never the same (a dev build is not "the same" as any release).
func Same(tag, current string) bool {
	next, err := ParseVersion(tag)
	if err != nil {
		return false
	}
	have, err := ParseVersion(current)
	if err != nil {
		return false
	}
	return Compare(next, have) == 0
}

// Lower reports whether tag is lower than the running version. A build that is
// not a released version (dev) is never lower, so it is never refused as a
// downgrade.
func Lower(tag, current string) bool {
	next, err := ParseVersion(tag)
	if err != nil {
		return false
	}
	have, err := ParseVersion(current)
	if err != nil {
		return false
	}
	return Compare(next, have) < 0
}

// Newest picks the newest release of channel. The rc channel takes every
// release (pre-releases included); stable takes only releases without a
// pre-release suffix. Ties on the version go to the later published_at. A tag
// that does not parse is skipped here: Client.Releases already refused a
// response that carries one.
func Newest(releases []Release, channel string) (Release, bool) {
	var best Release
	var bestVersion Version
	found := false
	for _, release := range releases {
		v, err := ParseVersion(release.Tag)
		if err != nil {
			continue
		}
		if channel == ChannelStable && (release.Prerelease || len(v.Pre) > 0) {
			continue
		}
		if !found {
			best, bestVersion, found = release, v, true
			continue
		}
		switch cmp := Compare(v, bestVersion); {
		case cmp > 0:
			best, bestVersion = release, v
		case cmp == 0 && release.Published.After(best.Published):
			best = release
		}
	}
	return best, found
}

// Client reads the release list. API overrides DefaultAPI (tests point it at
// an httptest server through SOHO_RELEASES_API); HTTP overrides the bounded
// production client.
type Client struct {
	API  string
	HTTP *http.Client
}

// apiRelease is one item of the GitHub releases response. Unknown fields are
// ignored (the API adds them); the fields read here are the ones that decide.
type apiRelease struct {
	TagName     string `json:"tag_name"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
}

// Releases fetches the release list. A non-2xx status, a body that is not a
// JSON array, a body over the size bound, and a non-draft release whose tag is
// not a valid version are errors: the whole response is refused rather than
// partially used. Drafts are ignored.
func (c Client) Releases(ctx context.Context) ([]Release, error) {
	api, err := normalizeAPI(c.API)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, fmt.Errorf("update: building the releases request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: RequestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: reading the releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("update: the releases API answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("update: reading the releases: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("update: the releases response is larger than %d bytes", maxResponseBytes)
	}
	var items []apiRelease
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("update: malformed releases response: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("update: malformed releases response: trailing data after the array")
	}
	releases := make([]Release, 0, len(items))
	for _, item := range items {
		if item.Draft {
			continue
		}
		if _, err := ParseVersion(item.TagName); err != nil {
			return nil, fmt.Errorf("update: the releases response carries an unusable tag %q: %w", item.TagName, err)
		}
		var published time.Time
		if item.PublishedAt != "" {
			if parsed, err := time.Parse(time.RFC3339, item.PublishedAt); err == nil {
				published = parsed
			}
		}
		releases = append(releases, Release{Tag: item.TagName, Published: published, Prerelease: item.Prerelease})
	}
	return releases, nil
}

// normalizeAPI applies the default and validates the endpoint before any
// request: whitespace, credentials and a fragment are refused, and only
// http/https with a host is accepted. A query is allowed (the default API
// carries per_page).
func normalizeAPI(api string) (string, error) {
	if api == "" {
		api = DefaultAPI
	}
	if idx := strings.IndexFunc(api, unicode.IsSpace); idx >= 0 {
		return "", errors.New("update: releases API URL contains whitespace")
	}
	u, err := url.Parse(api)
	if err != nil {
		return "", fmt.Errorf("update: invalid releases API URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("update: releases API URL must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("update: releases API URL has no host")
	}
	if u.User != nil {
		return "", errors.New("update: releases API URL contains credentials")
	}
	if u.Fragment != "" {
		return "", errors.New("update: releases API URL contains a fragment")
	}
	return api, nil
}

// CheckOptions configures one check. Channel empty means the running
// version's channel.
type CheckOptions struct {
	Current string
	Channel string
	API     string
	HTTP    *http.Client
}

// Check is the outcome of a successful check: which release is the newest of
// the channel and whether the running version should move to it.
type Check struct {
	Current         string
	Channel         string
	Newest          Release
	Found           bool
	UpdateAvailable bool
}

// CheckChannel resolves the channel, reads the release list and picks its
// newest release. A network, parse or empty-channel outcome is an error: the
// caller reports "could not check", never "up to date".
func CheckChannel(ctx context.Context, opts CheckOptions) (Check, error) {
	channel, err := ParseChannel(opts.Channel)
	if err != nil {
		return Check{}, err
	}
	if channel == "" {
		channel = ChannelFor(opts.Current)
	}
	releases, err := Client{API: opts.API, HTTP: opts.HTTP}.Releases(ctx)
	if err != nil {
		return Check{}, err
	}
	best, found := Newest(releases, channel)
	if !found {
		return Check{}, fmt.Errorf("update: no release in the %s channel", channel)
	}
	return Check{
		Current:         opts.Current,
		Channel:         channel,
		Newest:          best,
		Found:           true,
		UpdateAvailable: Newer(best.Tag, opts.Current),
	}, nil
}
