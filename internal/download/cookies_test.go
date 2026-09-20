package download

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kapsel/internal/jobs"
)

func writeNetscapeCookies(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

const signedInCookieFile = `# Netscape HTTP Cookie File
.youtube.com	TRUE	/	TRUE	1893456000	SID	g.a000hog
.youtube.com	TRUE	/	TRUE	1893456000	__Secure-1PSID	v.1
.youtube.com	TRUE	/	TRUE	1999999999	LOGIN_INFO	AFmmF2sw
.youtube.com	TRUE	/	TRUE	1770000000	SAPISID	sapisid-value
`

func TestCheckCookiesParsesSignedInSession(t *testing.T) {
	t.Parallel()

	status := CheckCookies(writeNetscapeCookies(t, signedInCookieFile))
	if !status.Configured || !status.Readable || !status.AuthCookies {
		t.Fatalf("expected a configured, readable, signed-in status, got %#v", status)
	}
	if status.ParseError != "" || status.Expiry == nil {
		t.Fatalf("unexpected status fields: %#v", status)
	}
	want := time.Unix(1770000000, 0).UTC()
	if status.Expiry == nil || !status.Expiry.Equal(want) {
		t.Fatalf("expected earliest auth expiry %v, got %v", want, status.Expiry)
	}
	if !status.Available() {
		t.Fatalf("expected status to be available: %#v", status)
	}
}

func TestCheckCookiesSetsPath(t *testing.T) {
	t.Parallel()

	path := writeNetscapeCookies(t, signedInCookieFile)
	status := CheckCookies(path)
	if abs, err := filepath.Abs(path); err != nil || status.Path != abs {
		t.Fatalf("expected resolved path %v, got %#v", abs, status)
	}
}

func TestCheckCookiesWithoutAuthCookies(t *testing.T) {
	t.Parallel()

	status := CheckCookies(writeNetscapeCookies(t, ".youtube.com\tTRUE\t/\tTRUE\t1893456000\tVISITOR_INFO1_LIVE	value\n"))
	if !status.Readable || status.AuthCookies || status.Expiry != nil {
		t.Fatalf("expected readable but signed-out status, got %#v", status)
	}
	if status.Available() {
		t.Fatal("expected status to be unavailable without auth cookies")
	}
}

func TestCheckCookiesIgnoresMismatchedDomains(t *testing.T) {
	t.Parallel()

	status := CheckCookies(writeNetscapeCookies(t, ".example.com\tTRUE\t/\tTRUE\t1893456000\tSID	forged\n"))
	if status.AuthCookies {
		t.Fatalf("expected forged cookie domain to be ignored, got %#v", status)
	}
}

func TestCheckCookiesMissingFile(t *testing.T) {
	t.Parallel()

	status := CheckCookies(filepath.Join(t.TempDir(), "missing.txt"))
	if !status.Configured || status.Readable || status.ParseError == "" {
		t.Fatalf("expected configured but unreadable status, got %#v", status)
	}
}

func TestCheckCookiesNotConfigured(t *testing.T) {
	t.Parallel()

	status := CheckCookies("")
	if status.Configured || status.Readable || status.AuthCookies {
		t.Fatalf("expected not-configured status, got %#v", status)
	}
}

func TestCheckCookiesSkipsSessionOnlyExpiry(t *testing.T) {
	t.Parallel()

	status := CheckCookies(writeNetscapeCookies(t, ".youtube.com\tTRUE\t/\tTRUE\t0\tHSID\ts\n.youtube.com\tTRUE\t/\tTRUE\t1770000000\tSAPISID\tsv\n.youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\tlv\n"))
	if !status.AuthCookies || status.Expiry == nil || !status.Expiry.Equal(time.Unix(1770000000, 0).UTC()) {
		t.Fatalf("expected session-only cookie to be skipped for expiry, got %#v", status)
	}
}

func TestCheckCookiesRejectsMalformedLines(t *testing.T) {
	t.Parallel()

	status := CheckCookies(writeNetscapeCookies(t, "not-a-cookie-line\n"))
	if !status.Readable || status.ParseError == "" || status.AuthCookies {
		t.Fatalf("expected parse error for malformed file, got %#v", status)
	}
	if status.Available() {
		t.Fatal("expected malformed cookies to be unavailable")
	}
}

type cookiesProbeRunner struct {
	output []byte
	err    error
}

func (r *cookiesProbeRunner) Run(_ context.Context, command Command) ([]byte, error) {
	if !strings.Contains(strings.Join(command.Args, " "), "--cookies") {
		return nil, errCookiesProbeNoCookies
	}

	return r.output, r.err
}

var errCookiesProbeNoCookies = context.Canceled

func TestCheckCookiesLiveVerifiesSignedInSession(t *testing.T) {
	t.Parallel()

	path := writeNetscapeCookies(t, signedInCookieFile)
	runner := &cookiesProbeRunner{output: []byte("[debug] [youtube] Found YouTube account cookies\n[download] Nothing to download\n")}
	status := CheckCookiesLive(context.Background(), "yt-dlp", path, runner)
	if !status.Verified || status.ProbeError != "" {
		t.Fatalf("expected verified status, got %#v", status)
	}
	if status.LastProbe != nil {
		t.Fatalf("CheckCookiesLive must not stamp LastProbe; the server owns that: %#v", status)
	}
}

func TestCheckCookiesLiveReportsMissingSessionMarker(t *testing.T) {
	t.Parallel()

	path := writeNetscapeCookies(t, signedInCookieFile)
	runner := &cookiesProbeRunner{output: []byte("abc123\n")}
	status := CheckCookiesLive(context.Background(), "yt-dlp", path, runner)
	if status.Verified || !strings.Contains(status.ProbeError, "account session") {
		t.Fatalf("expected a probe error, got %#v", status)
	}
}

func TestCheckCookiesLiveReportsRunnerFailure(t *testing.T) {
	t.Parallel()

	path := writeNetscapeCookies(t, signedInCookieFile)
	runner := &cookiesProbeRunner{err: context.Canceled}
	status := CheckCookiesLive(context.Background(), "yt-dlp", path, runner)
	if status.Verified || status.ProbeError == "" {
		t.Fatalf("expected a probe error, got %#v", status)
	}
}

func TestCheckCookiesLiveSkipsProbeWithoutSession(t *testing.T) {
	t.Parallel()

	runner := &cookiesProbeRunner{}
	status := CheckCookiesLive(context.Background(), "yt-dlp", "", runner)
	if status.Verified || !strings.Contains(status.ProbeError, "no YouTube cookies configured") {
		t.Fatalf("expected not-configured skip, got %#v", status)
	}
	if len(runner.output) != 0 {
		t.Fatalf("expected the runner not to run, got output %#v", runner.output)
	}
}

func TestCookiesProbeStateRateLimits(t *testing.T) {
	t.Parallel()

	state := NewCookiesProbeState()
	now := time.Now().UTC()
	if allowed, _ := state.Allow(now); !allowed {
		t.Fatal("expected first probe to be allowed")
	}
	if allowed, retry := state.Allow(now); allowed || retry <= 0 || retry > cookiesProbeRateLimit {
		t.Fatalf("expected second immediate probe to be rate limited, got allowed=%v retry=%v", allowed, retry)
	}
	if allowed, _ := state.Allow(now.Add(cookiesProbeRateLimit + time.Second)); !allowed {
		t.Fatal("expected probe to be allowed after the cooldown")
	}
	state.Record(CookiesStatus{Verified: true}, now)
	if cached := state.Cached(); !cached.Verified || cached.LastProbe == nil {
		t.Fatalf("expected cached record, got %#v", cached)
	}
}

func TestBuildCommandMarkWatchedGating(t *testing.T) {
	t.Parallel()

	video := "https://www.youtube.com/watch?v=abc123DEF45"

	// No cookies configured: requested but skipped.
	downloader := NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: t.TempDir()}, nil)
	command, err := downloader.BuildCommand(video, true)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(command.Args, "--mark-watched") {
		t.Fatalf("expected --mark-watched to be omitted without cookies: %#v", command.Args)
	}
	command, err = downloader.BuildCommand(video, false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(command.Args, "--mark-watched") {
		t.Fatalf("expected --mark-watched to be omitted when not requested: %#v", command.Args)
	}

	// Signed-in cookies file: the flag is appended when requested.
	mediaRoot := t.TempDir()
	cookies := writeNetscapeCookies(t, signedInCookieFile)
	downloader = NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, YTDLPCookiesFile: cookies}, nil)
	command, err = downloader.BuildCommand(video, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(command.Args, "--mark-watched") {
		t.Fatalf("expected --mark-watched with an active session, got %#v", command.Args)
	}
	if !slices.Contains(command.Args, "--cookies") {
		t.Fatalf("expected the cookies flag alongside --mark-watched: %#v", command.Args)
	}

	// Unreadable file: the flag is dropped again.
	downloader = NewDownloader(nil, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, YTDLPCookiesFile: filepath.Join(t.TempDir(), "missing.txt")}, nil)
	command, err = downloader.BuildCommand(video, true)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(command.Args, "--mark-watched") {
		t.Fatalf("expected --mark-watched to be omitted for an unreadable cookies file: %#v", command.Args)
	}
}

func TestCookiesSkipReasonMatchesStates(t *testing.T) {
	t.Parallel()

	downloader := NewDownloader(nil, Config{}, nil)
	if reason := downloader.cookiesSkipReason(); reason != "no YouTube cookies configured" {
		t.Fatalf("unexpected unset reason: %q", reason)
	}
}

func TestNormalizeDownloadPayloadPreservesMarkWatched(t *testing.T) {
	t.Parallel()

	payload, err := NormalizeDownloadPayload(Payload{URL: "https://www.youtube.com/watch?v=abc123DEF45", MarkWatched: true})
	if err != nil {
		t.Fatal(err)
	}
	if !payload.MarkWatched {
		t.Fatal("expected mark_watched to be preserved through normalization")
	}
	_, body, err := canonicalDownloadPayload(Payload{URL: "https://www.youtube.com/watch?v=abc123DEF45", MarkWatched: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"mark_watched":true`) {
		t.Fatalf("expected the persisted payload to carry mark_watched, got %s", body)
	}
}

const markWatchedMetadataFixture = `{"id":"vid-1","title":"Kapsel Demo","channel_id":"chan-1","channel":"Archive Workshop","duration":120,"thumbnails":[],"__thumbnail_filepath":"vid-1.jpg","requested_downloads":[{"filepath":"vid-1.mp4"}]}`

func TestDownloadJobRecordsMarkWatchedSkipReason(t *testing.T) {
	t.Parallel()

	db := openDownloadDB(t)
	mediaRoot := writeDownloadFiles(t, "vid-1.mp4", "vid-1.jpg")
	store := jobs.NewStore(db)
	job, err := store.Enqueue(context.Background(), jobs.EnqueueParams{
		Type:        JobType,
		PayloadJSON: `{"url":"https://www.youtube.com/watch?v=abc123DEF45","mark_watched":true}`,
		MaxAttempts: 1,
		RunAfter:    time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	cookies := filepath.Join(t.TempDir(), "missing.txt")
	runner := jobs.NewRunner(store, map[string]jobs.Handler{
		JobType: newTestDownloader(db, store, Config{YTDLPPath: "yt-dlp", MediaRoot: mediaRoot, YTDLPCookiesFile: cookies}, fakeRunner{stdout: []byte(markWatchedMetadataFixture)}).Handle,
	})
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	stored, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		MarkWatched           string `json:"mark_watched"`
		MarkWatchedSkipReason string `json:"mark_watched_skip_reason"`
	}
	if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
		t.Fatalf("unexpected result json %q: %v", stored.ResultJSON, err)
	}
	if result.MarkWatched != "skipped" || !strings.Contains(result.MarkWatchedSkipReason, "not readable") {
		t.Fatalf("expected a skipped mark with a reason, got %#v", result)
	}
}

// Regression tests pinning the yt-dlp `_has_auth_cookies` boundary: the gate
// needs LOGIN_INFO AND a SAPISID-family cookie; anything less is rejected.
func TestCheckCookiesGateBoundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		file string
		want bool
	}{
		{name: "sid family only is rejected", file: ".youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tg\n.youtube.com\tTRUE\t/\tTRUE\t1893456000\t__Secure-1PSID\tv\n"},
		{name: "sapisid without login_info is rejected", file: ".youtube.com\tTRUE\t/\tTRUE\t1770000000\tSAPISID\tsv\n"},
		{name: "login_info without sapisid is rejected", file: ".youtube.com\tTRUE\t/\tTRUE\t1999999999\tLOGIN_INFO\tlv\n"},
		{name: "login_info and sapisid passes", file: ".youtube.com\tTRUE\t/\tTRUE\t1770000000\tSAPISID\tsv\n.google.com\tTRUE\t/\tTRUE\t1999999999\tLOGIN_INFO\tlv\n"},
	}
	for _, tableCase := range cases {
		status := CheckCookies(writeNetscapeCookies(t, tableCase.file))
		if available := tableCase.file == cases[len(cases)-1].file; status.AuthCookies != available || status.Available() != available {
			t.Fatalf("%s: unexpected gate result: %#v", tableCase.name, status)
		}
	}
}

func TestCheckCookiesSessionOnlyAuthCookiesLeaveExpiryUnset(t *testing.T) {
	t.Parallel()

	fixture := ".youtube.com\tTRUE\t/\tTRUE\t0\tSAPISID\tsv\n.youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\tlv\n"
	status := CheckCookies(writeNetscapeCookies(t, fixture))
	if !status.AuthCookies {
		t.Fatalf("expected a session-only signed-in session, got %#v", status)
	}
	if status.Expiry != nil {
		t.Fatalf("expected no expiry for session-only cookies, got %#v", status.Expiry)
	}
}
