package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kapsel/internal/download"
)

func writeServerCookiesFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

const serverSignedInCookies = `.youtube.com	TRUE	/	TRUE	1893456000	SID	g.a000hog
.youtube.com	TRUE	/	TRUE	1999999999	LOGIN_INFO	lv
.youtube.com	TRUE	/	TRUE	1999999999	SAPISID	sapisid-value
`

type serverCookieRunner struct {
	output []byte
	err    error
	calls  int
}

func (r *serverCookieRunner) Run(_ context.Context, command download.Command) ([]byte, error) {
	r.calls++

	return r.output, r.err
}

func newCookiesTestHandler(t *testing.T, body string, runner *serverCookieRunner) http.Handler {
	t.Helper()
	path := writeServerCookiesFile(t, body)

	return NewHandler(
		WithSettingsDiagnostics(SettingsDiagnostics{YTDLPCookiesFile: path}),
		WithCookiesDiagnostics(path, runner),
	)
}

func TestSettingsEndpointWarnsWhenCookiesNotConfigured(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()

	NewHandler(
		WithSettingsDiagnostics(SettingsDiagnostics{}),
		WithCookiesDiagnostics("", nil),
	).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	response := struct {
		Cookies download.CookiesStatus `json:"cookies"`
		Checks  []settingsCheckFixture `json:"checks"`
	}{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Cookies.Configured {
		t.Fatalf("expected unconfigured cookies status, got %#v", response.Cookies)
	}
	if state := settingsCheckState(response.Checks, "youtube_cookies"); state != "warn" {
		t.Fatalf("expected unset cookies to warn, got %q", state)
	}
}

func TestSettingsEndpointReportsSignedInCookieStatus(t *testing.T) {
	t.Parallel()

	handler := newCookiesTestHandler(t, serverSignedInCookies, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	response := struct {
		Cookies download.CookiesStatus `json:"cookies"`
		Checks  []settingsCheckFixture `json:"checks"`
	}{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.Cookies.Available() || response.Cookies.Configured != true || response.Cookies.Path == "" {
		t.Fatalf("unexpected cookies status: %#v", response.Cookies)
	}
	if state := settingsCheckState(response.Checks, "youtube_cookies"); state != "pass" {
		t.Fatalf("expected signed-in cookies to pass, got %q in %#v", state, response.Checks)
	}
}

func TestSettingsEndpointErrorsOnMissingCookieFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.txt")
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()

	NewHandler(
		WithSettingsDiagnostics(SettingsDiagnostics{YTDLPCookiesFile: path}),
		WithCookiesDiagnostics(path, nil),
	).ServeHTTP(rec, req)

	response := struct {
		Checks []settingsCheckFixture `json:"checks"`
	}{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if state := settingsCheckState(response.Checks, "youtube_cookies"); state != "error" {
		t.Fatalf("expected missing cookies file to error, got %q", state)
	}
	if detail := settingsCheckDetail(response.Checks, "youtube_cookies"); detail == "" {
		t.Fatal("expected the parse error in the check detail")
	}
}

func TestSettingsEndpointErrorsOnSignedOutCookieFile(t *testing.T) {
	t.Parallel()

	handler := newCookiesTestHandler(t, ".youtube.com	TRUE	/	TRUE	1893456000	VISITOR_INFO1_LIVE	v\n", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	response := struct {
		Checks []settingsCheckFixture `json:"checks"`
	}{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if state := settingsCheckState(response.Checks, "youtube_cookies"); state != "error" {
		t.Fatalf("expected signed-out cookies to error, got %q", state)
	}
	if detail := settingsCheckDetail(response.Checks, "youtube_cookies"); !strings.Contains(detail, "signed-in") {
		t.Fatalf("expected the signed-out explanation in the check detail, got %q", detail)
	}
}

func TestCookiesProbeVerifiesAndCaches(t *testing.T) {
	t.Parallel()

	runner := &serverCookieRunner{output: []byte("[debug] [youtube] Found YouTube account cookies\n")}
	handler := newCookiesTestHandler(t, serverSignedInCookies, runner)

	probeReq := httptest.NewRequest(http.MethodPost, "/api/settings/test-youtube-cookies", nil)
	probeRec := httptest.NewRecorder()
	handler.ServeHTTP(probeRec, probeReq)
	if probeRec.Code != http.StatusOK {
		t.Fatalf("expected probe status %d, got %d: %s", http.StatusOK, probeRec.Code, probeRec.Body.String())
	}
	probe := download.CookiesStatus{}
	if err := json.NewDecoder(probeRec.Body).Decode(&probe); err != nil {
		t.Fatal(err)
	}
	if !probe.Verified || probe.LastProbe.IsZero() {
		t.Fatalf("expected a verified probed status, got %#v", probe)
	}
	if probe.Expiry.IsZero() {
		t.Fatalf("expected static expiry in probe response, got %#v", probe)
	}

	// Immediately re-probing is rate limited.
	rateRec := httptest.NewRecorder()
	handler.ServeHTTP(rateRec, probeReq)
	if rateRec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected rate limited probe, got %d", rateRec.Code)
	}
	if retry := rateRec.Header().Get("Retry-After"); retry == "" {
		t.Fatal("expected a Retry-After header on the rate limited probe")
	}

	// The cached probe result is surfaced by GET /api/settings without re-probing.
	settingsRec := httptest.NewRecorder()
	handler.ServeHTTP(settingsRec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if runner.calls != 1 {
		t.Fatalf("expected a single probe invocation, got %d", runner.calls)
	}
	settings := struct {
		Cookies download.CookiesStatus `json:"cookies"`
	}{}
	if err := json.NewDecoder(settingsRec.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Cookies.Verified || settings.Cookies.LastProbe.IsZero() {
		t.Fatalf("expected cached probe verification in settings, got %#v", settings.Cookies)
	}
}

func TestCookiesProbeRecordsFailure(t *testing.T) {
	t.Parallel()

	runner := &serverCookieRunner{err: context.Canceled}
	handler := newCookiesTestHandler(t, serverSignedInCookies, runner)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/settings/test-youtube-cookies", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected probe status %d, got %d", http.StatusOK, rec.Code)
	}
	probe := download.CookiesStatus{}
	if err := json.NewDecoder(rec.Body).Decode(&probe); err != nil {
		t.Fatal(err)
	}
	if probe.Verified || probe.ProbeError == "" {
		t.Fatalf("expected surfaced probe error, got %#v", probe)
	}

	settingsRec := httptest.NewRecorder()
	handler.ServeHTTP(settingsRec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	settings := struct {
		Checks []settingsCheckFixture `json:"checks"`
	}{}
	if err := json.NewDecoder(settingsRec.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if state := settingsCheckState(settings.Checks, "youtube_cookies"); state != "pass" {
		t.Fatalf("expected the auth-cookie static state to pass despite probe failure, got %q", state)
	}
	if detail := settingsCheckDetail(settings.Checks, "youtube_cookies"); !strings.Contains(detail, "did not pass") {
		t.Fatalf("expected the failed probe in the check detail, got %q", detail)
	}
}

func TestCapabilitiesEndpointReflectsCookieStatus(t *testing.T) {
	t.Parallel()

	// Signed-in file: available.
	available := newCookiesTestHandler(t, serverSignedInCookies, nil)
	rec := httptest.NewRecorder()
	available.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	response := struct {
		MarkWatched struct {
			Available bool    `json:"available"`
			Reason    *string `json:"reason"`
		} `json:"mark_watched"`
	}{}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.MarkWatched.Available || response.MarkWatched.Reason != nil {
		t.Fatalf("expected mark_watched available, got %#v", response.MarkWatched)
	}

	// Unconfigured: unavailable with a reason.
	unconfigured := NewHandler(WithCookiesDiagnostics("", nil))
	rec = httptest.NewRecorder()
	unconfigured.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.MarkWatched.Available || response.MarkWatched.Reason == nil || !strings.Contains(*response.MarkWatched.Reason, "not configured") && !strings.Contains(*response.MarkWatched.Reason, "no YouTube cookies") {
		t.Fatalf("expected unavailable with reason, got %#v", response.MarkWatched)
	}
}
