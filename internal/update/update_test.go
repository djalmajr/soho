package update

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// releasesServer serves a fixed release list body and counts every request.
func releasesServer(t *testing.T, body string) (*httptest.Server, *int32) {
	t.Helper()
	var reqs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqs, 1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

// releaseJSON renders one item of the GitHub releases list.
func releaseJSON(tag string, prerelease bool, published string) string {
	draft := false
	return fmt.Sprintf(`{"tag_name":%q,"draft":%t,"prerelease":%t,"published_at":%q}`,
		tag, draft, prerelease, published)
}

func TestParseVersionAndCompare(t *testing.T) {
	for _, tc := range []struct {
		name, tag string
		major     string
		pre       []string
		wantErr   bool
	}{
		{"a stable release", "v0.3.1", "0", nil, false},
		{"a release candidate", "v0.3.1-rc.18", "0", []string{"rc", "18"}, false},
		{"a large major", "v12.0.4", "12", nil, false},
		{"a major past uint64", "v18446744073709551617.0.0", "18446744073709551617", nil, false},
		{"empty", "", "0", nil, true},
		{"without the v prefix", "0.3.1", "0", nil, true},
		{"only two fields", "v0.3", "0", nil, true},
		{"a leading zero", "v0.03.1", "0", nil, true},
		{"an empty pre-release", "v0.3.1-", "0", nil, true},
		{"a path escape", "v0.3.1-../x", "0", nil, true},
		{"a leading zero in a pre-release number", "v0.3.1-rc.01", "0", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := ParseVersion(tc.tag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q) = %+v, want an error", tc.tag, v)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q): %v", tc.tag, err)
			}
			if v.Major != tc.major || strings.Join(v.Pre, ".") != strings.Join(tc.pre, ".") {
				t.Fatalf("ParseVersion(%q) = %+v, want major %s pre %v", tc.tag, v, tc.major, tc.pre)
			}
		})
	}

	// Semantic-versioning order, including the pre-release rules.
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v0.3.1", "v0.3.1", 0},
		{"v0.3.1", "v0.3.2", -1},
		{"v0.4.0", "v0.3.9", 1},
		{"v0.3.1-rc.18", "v0.3.1-rc.19", -1},
		{"v0.3.1-rc.9", "v0.3.1-rc.10", -1},
		{"v0.3.1-rc.19", "v0.3.1", -1},
		{"v0.3.1-rc.18", "v0.3.0", 1},
		{"v0.3.1-alpha", "v0.3.1-beta", -1},
		{"v0.3.1-alpha.1", "v0.3.1-alpha", 1},
		{"v0.3.1-1", "v0.3.1-alpha", -1},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},
	} {
		a, err := ParseVersion(tc.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := ParseVersion(tc.b)
		if err != nil {
			t.Fatal(err)
		}
		if got := Compare(a, b); got != tc.want {
			t.Fatalf("Compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestCompareKeepsTheOrderOfLargeNumericIdentifiers is the review's overflow
// pair (SOH-447 F6): two distinct identifiers past uint64 must not compare
// equal, in a pre-release and in a core field.
func TestCompareKeepsTheOrderOfLargeNumericIdentifiers(t *testing.T) {
	const (
		lower   = "v0.3.1-rc.18446744073709551616"
		higher  = "v0.3.1-rc.18446744073709551617"
		lowerC  = "v20000000000000000000.0.0"
		higherC = "v20000000000000000001.0.0"
	)
	for _, pair := range [][2]string{{lower, higher}, {lowerC, higherC}, {"v0.3.1-rc.9", "v0.3.1-rc.10"}} {
		a, err := ParseVersion(pair[0])
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", pair[0], err)
		}
		b, err := ParseVersion(pair[1])
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", pair[1], err)
		}
		if got := Compare(a, b); got != -1 {
			t.Fatalf("Compare(%s, %s) = %d, want -1", pair[0], pair[1], got)
		}
		if got := Compare(b, a); got != 1 {
			t.Fatalf("Compare(%s, %s) = %d, want 1", pair[1], pair[0], got)
		}
		if !Newer(pair[1], pair[0]) || !Lower(pair[0], pair[1]) {
			t.Fatalf("Newer/Lower disagree for %s vs %s", pair[0], pair[1])
		}
		if Same(pair[0], pair[1]) {
			t.Fatalf("Same(%s, %s) = true for distinct identifiers", pair[0], pair[1])
		}
	}
	best, ok := Newest([]Release{{Tag: lower}, {Tag: higher}}, ChannelRC)
	if !ok || best.Tag != higher {
		t.Fatalf("Newest = %+v, %t; want %s", best, ok, higher)
	}
}

func TestChannelForRunningVersion(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"v0.3.1-rc.18", ChannelRC},
		{"v0.3.1", ChannelStable},
		{"v1.0.0", ChannelStable},
		{"dev", ChannelStable},
		{"dev+abc1234", ChannelStable},
		{"", ChannelStable},
		{"not a version", ChannelStable},
	} {
		if got := ChannelFor(tc.version); got != tc.want {
			t.Fatalf("ChannelFor(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}

func TestParseChannel(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{ChannelStable, ChannelStable, false},
		{ChannelRC, ChannelRC, false},
		{"rc.18", "", true},
		{"latest", "", true},
		{"STABLE", "", true},
	} {
		got, err := ParseChannel(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Fatalf("ParseChannel(%q) = %q, %v; want %q, err=%t", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
	if _, err := ParseChannel("bogus"); err == nil || !strings.Contains(err.Error(), "stable") || !strings.Contains(err.Error(), "rc") {
		t.Fatalf("the refusal does not name both channels: %v", err)
	}
}

func TestNewestPerChannel(t *testing.T) {
	releases := []Release{
		{Tag: "v0.2.0", Published: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{Tag: "v0.3.0", Published: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)},
		{Tag: "v0.3.1-rc.18", Prerelease: true, Published: time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)},
		{Tag: "v0.3.1-rc.19", Prerelease: true, Published: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)},
	}
	t.Run("rc takes every release, pre-releases included", func(t *testing.T) {
		best, ok := Newest(releases, ChannelRC)
		if !ok || best.Tag != "v0.3.1-rc.19" {
			t.Fatalf("Newest(rc) = %+v, %t; want v0.3.1-rc.19 (it must beat the stable v0.3.0)", best, ok)
		}
	})
	t.Run("stable excludes pre-releases", func(t *testing.T) {
		best, ok := Newest(releases, ChannelStable)
		if !ok || best.Tag != "v0.3.0" {
			t.Fatalf("Newest(stable) = %+v, %t; want v0.3.0", best, ok)
		}
	})
	t.Run("a newer stable release beats an older rc", func(t *testing.T) {
		withNewerStable := append([]Release{{Tag: "v0.4.0", Published: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)}}, releases...)
		best, ok := Newest(withNewerStable, ChannelRC)
		if !ok || best.Tag != "v0.4.0" {
			t.Fatalf("Newest(rc) = %+v, %t; want v0.4.0", best, ok)
		}
	})
	t.Run("a release flagged prerelease with a plain tag is not stable", func(t *testing.T) {
		best, ok := Newest([]Release{{Tag: "v0.5.0", Prerelease: true}, {Tag: "v0.4.0"}}, ChannelStable)
		if !ok || best.Tag != "v0.4.0" {
			t.Fatalf("Newest(stable) = %+v, %t; want v0.4.0", best, ok)
		}
	})
	t.Run("ties go to the later published_at", func(t *testing.T) {
		early := Release{Tag: "v1.0.0", Published: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		late := Release{Tag: "v1.0.0", Published: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}
		if best, _ := Newest([]Release{late, early}, ChannelStable); !best.Published.Equal(late.Published) {
			t.Fatalf("tie broke the wrong way: %+v", best)
		}
	})
	t.Run("an empty list has no newest", func(t *testing.T) {
		for _, channel := range []string{ChannelStable, ChannelRC} {
			if best, ok := Newest(nil, channel); ok {
				t.Fatalf("Newest(nil, %s) = %+v, true; want false", channel, best)
			}
		}
	})
	t.Run("a tag that does not parse is never picked", func(t *testing.T) {
		best, ok := Newest([]Release{{Tag: "nightly"}, {Tag: "v1.0.0"}}, ChannelStable)
		if !ok || best.Tag != "v1.0.0" {
			t.Fatalf("Newest = %+v, %t; want v1.0.0", best, ok)
		}
	})
}

func TestClientReleasesReadsTheList(t *testing.T) {
	body := "[" + strings.Join([]string{
		releaseJSON("v0.3.1-rc.19", true, "2026-11-20T10:00:00Z"),
		`{"tag_name":"v9.9.9","draft":true,"prerelease":false,"published_at":"2026-11-21T10:00:00Z"}`,
		releaseJSON("v0.3.0", false, "2026-10-20T10:00:00Z"),
	}, ",") + "]"
	srv, reqs := releasesServer(t, body)
	client := Client{API: srv.URL + "/repos/djalmajr/soho/releases?per_page=50"}
	releases, err := client.Releases(context.Background())
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	if len(releases) != 2 {
		t.Fatalf("releases = %+v, want the draft left out", releases)
	}
	if releases[0].Tag != "v0.3.1-rc.19" || !releases[0].Prerelease || releases[0].PublishedDate() != "2026-11-20" {
		t.Fatalf("first release = %+v", releases[0])
	}
	if releases[1].Tag != "v0.3.0" || releases[1].Prerelease || releases[1].PublishedDate() != "2026-10-20" {
		t.Fatalf("second release = %+v", releases[1])
	}
}

func TestClientReleasesSendsTheDocumentedRequest(t *testing.T) {
	var got struct {
		method, ua, accept, path string
		body                     int
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path = r.Method, r.URL.Path
		got.ua = r.Header.Get("User-Agent")
		got.accept = r.Header.Get("Accept")
		got.body = int(r.ContentLength)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	if _, err := (Client{API: srv.URL + "/repos/djalmajr/soho/releases?per_page=50"}).Releases(context.Background()); err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if got.method != http.MethodGet || got.path != "/repos/djalmajr/soho/releases" {
		t.Fatalf("request = %+v, want GET /repos/djalmajr/soho/releases", got)
	}
	if got.ua != UserAgent {
		t.Fatalf("User-Agent = %q, want %q", got.ua, UserAgent)
	}
	if got.accept == "" {
		t.Fatalf("no Accept header was sent")
	}
	if got.body > 0 {
		t.Fatalf("the request carried %d bytes of body; the check sends no user data", got.body)
	}
}

func TestClientReleasesRefusesBadAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"a non-2xx status", http.StatusInternalServerError, `[]`, "answered 500"},
		{"a JSON object instead of an array", http.StatusOK, `{"tag_name":"v1.0.0"}`, "malformed releases response"},
		{"a body that is not JSON", http.StatusOK, `<html>`, "malformed releases response"},
		{"trailing data after the array", http.StatusOK, `[] {}`, "trailing data"},
		{"a non-draft release with an unusable tag", http.StatusOK, `[{"tag_name":"nightly","draft":false}]`, "unusable tag"},
		{"a non-draft release with no tag", http.StatusOK, `[{"draft":false}]`, "unusable tag"},
		{"a non-draft release with a path escape", http.StatusOK, `[{"tag_name":"v0.1.0-../x","draft":false}]`, "unusable tag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := (Client{API: srv.URL}).Releases(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Releases error = %v, want it to name %q", err, tc.want)
			}
		})
	}
	t.Run("a body over the size bound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("[" + strings.Repeat(" ", maxResponseBytes) + "]"))
		}))
		defer srv.Close()
		if _, err := (Client{API: srv.URL}).Releases(context.Background()); err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("Releases error = %v, want the size-bound refusal", err)
		}
	})
	t.Run("a draft with an unusable tag is ignored", func(t *testing.T) {
		srv, _ := releasesServer(t, `[{"tag_name":"nightly","draft":true},{"tag_name":"v1.0.0","draft":false}]`)
		releases, err := (Client{API: srv.URL}).Releases(context.Background())
		if err != nil {
			t.Fatalf("Releases: %v", err)
		}
		if len(releases) != 1 || releases[0].Tag != "v1.0.0" {
			t.Fatalf("releases = %+v, want only v1.0.0", releases)
		}
	})
}

func TestClientReleasesRefusesBadEndpointsBeforeTheNetwork(t *testing.T) {
	for _, api := range []string{
		"ftp://example.test/releases",
		"https://",
		"https://user:secret@example.test/releases",
		"https://example.test/releases#fragment",
		"https://example.test/releases?per_page=5 5",
	} {
		if _, err := (Client{API: api}).Releases(context.Background()); err == nil {
			t.Fatalf("Client{API:%q}.Releases accepted the endpoint", api)
		}
	}
}

func TestProductionConstants(t *testing.T) {
	// The production endpoint and the opt-out variable are the documented
	// ones; every test points API at an httptest server instead.
	if DefaultAPI != "https://api.github.com/repos/djalmajr/soho/releases?per_page=50" {
		t.Fatalf("DefaultAPI = %q", DefaultAPI)
	}
	if UserAgent != "soho-update" {
		t.Fatalf("UserAgent = %q", UserAgent)
	}
	if EnvNoUpdateCheck != "SOHO_NO_UPDATE_CHECK" {
		t.Fatalf("EnvNoUpdateCheck = %q", EnvNoUpdateCheck)
	}
	if RequestTimeout != 5*time.Second {
		t.Fatalf("RequestTimeout = %s, want 5s", RequestTimeout)
	}
	if NoticeTimeout != 2*time.Second {
		t.Fatalf("NoticeTimeout = %s, want 2s", NoticeTimeout)
	}
}

// closedPortURL is an address nothing listens on: the check must report a
// failure, never "up to date".
func closedPortURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr + "/releases"
}

func TestCheckChannelPicksTheRunningChannelsRelease(t *testing.T) {
	body := "[" + strings.Join([]string{
		releaseJSON("v0.3.0", false, "2026-10-20T10:00:00Z"),
		releaseJSON("v0.3.1-rc.19", true, "2026-11-20T10:00:00Z"),
	}, ",") + "]"
	srv, reqs := releasesServer(t, body)

	for _, tc := range []struct {
		name     string
		current  string
		channel  string
		want     string
		wantChan string
		update   bool
	}{
		{"an rc build follows rc, pre-releases included", "v0.3.1-rc.18", "", "v0.3.1-rc.19", ChannelRC, true},
		{"a stable build ignores the pre-release", "v0.3.0", "", "v0.3.0", ChannelStable, false},
		{"--channel overrides an rc build to stable", "v0.3.1-rc.18", ChannelStable, "v0.3.0", ChannelStable, false},
		{"--channel overrides a stable build to rc", "v0.2.0", ChannelRC, "v0.3.1-rc.19", ChannelRC, true},
		{"a dev build follows stable", DevVersion, "", "v0.3.0", ChannelStable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := CheckChannel(context.Background(), CheckOptions{Current: tc.current, Channel: tc.channel, API: srv.URL})
			if err != nil {
				t.Fatalf("CheckChannel: %v", err)
			}
			if res.Channel != tc.wantChan || res.Newest.Tag != tc.want || res.UpdateAvailable != tc.update {
				t.Fatalf("CheckChannel = %+v, want channel %q newest %q update %t", res, tc.wantChan, tc.want, tc.update)
			}
		})
	}
	if n := atomic.LoadInt32(reqs); n != 5 {
		t.Fatalf("requests = %d, want one per check (5)", n)
	}
}

func TestCheckChannelRefusesWhatItCannotProve(t *testing.T) {
	t.Run("a network failure is not up to date", func(t *testing.T) {
		_, err := CheckChannel(context.Background(), CheckOptions{Current: "v0.3.1-rc.18", API: closedPortURL(t)})
		if err == nil {
			t.Fatal("CheckChannel succeeded against a closed port")
		}
	})
	t.Run("an empty release list", func(t *testing.T) {
		srv, _ := releasesServer(t, `[]`)
		_, err := CheckChannel(context.Background(), CheckOptions{Current: "v0.3.1-rc.18", API: srv.URL})
		if err == nil || !strings.Contains(err.Error(), "no release in the rc channel") {
			t.Fatalf("error = %v, want the empty-channel refusal", err)
		}
	})
	t.Run("the stable channel with only pre-releases", func(t *testing.T) {
		srv, _ := releasesServer(t, `[{"tag_name":"v0.3.1-rc.19","prerelease":true}]`)
		_, err := CheckChannel(context.Background(), CheckOptions{Current: "v0.3.0", API: srv.URL})
		if err == nil || !strings.Contains(err.Error(), "no release in the stable channel") {
			t.Fatalf("error = %v, want the empty-channel refusal", err)
		}
	})
	t.Run("an unknown channel", func(t *testing.T) {
		if _, err := CheckChannel(context.Background(), CheckOptions{Channel: "beta"}); err == nil {
			t.Fatal("CheckChannel accepted an unknown channel")
		}
	})
}

func TestSame(t *testing.T) {
	for _, tc := range []struct {
		tag, current string
		want         bool
	}{
		{"v0.3.1-rc.18", "v0.3.1-rc.18", true},
		{"v0.3.1", "v0.3.1-rc.18", false},
		{"v0.3.1-rc.19", "v0.3.1-rc.18", false},
		{"v0.3.1", DevVersion, false},
		{"nightly", "nightly", false},
	} {
		if got := Same(tc.tag, tc.current); got != tc.want {
			t.Fatalf("Same(%q, %q) = %t, want %t", tc.tag, tc.current, got, tc.want)
		}
	}
}

func TestNewerAndLower(t *testing.T) {
	for _, tc := range []struct {
		name         string
		tag, current string
		newer, lower bool
	}{
		{"a higher rc", "v0.3.1-rc.19", "v0.3.1-rc.18", true, false},
		{"the same version", "v0.3.1-rc.18", "v0.3.1-rc.18", false, false},
		{"a lower rc", "v0.3.1-rc.17", "v0.3.1-rc.18", false, true},
		{"the stable release of an rc", "v0.3.1", "v0.3.1-rc.18", true, false},
		{"a dev build is never refused", "v0.3.1-rc.19", DevVersion, true, false},
		{"an installed rc is lower than a newer stable", "v0.3.0", "v0.3.1-rc.18", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Newer(tc.tag, tc.current); got != tc.newer {
				t.Fatalf("Newer(%q, %q) = %t, want %t", tc.tag, tc.current, got, tc.newer)
			}
			if got := Lower(tc.tag, tc.current); got != tc.lower {
				t.Fatalf("Lower(%q, %q) = %t, want %t", tc.tag, tc.current, got, tc.lower)
			}
		})
	}
}
