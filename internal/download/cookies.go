package download

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"kapsel/internal/sandbox"
)

// Auth-cookie gating mirrors yt-dlp's own _has_auth_cookies (youtube
// _base.py): a LOGIN_INFO cookie AND at least one SAPISID-family cookie.
// SID/1PSID-family cookies alone are ignored by yt-dlp, so they must not
// satisfy the gate here either.
var youtubeAuthLoginCookieName = "LOGIN_INFO"

var youtubeAuthSAPISIDCookieNames = []string{
	"SAPISID",
	"__Secure-1PAPISID",
	"__Secure-3PAPISID",
}

// youtubeAuthCookieDomains are the domains an auth cookie must belong to.
var youtubeAuthCookieDomains = []string{".youtube.com", ".google.com"}

// CookiesExpiryWarnWindow is how far from cookie expiry the readiness check
// starts warning. Chosen so a monthly re-export routine stays ahead of the
// warning; documented in macos-local.env.example.
const CookiesExpiryWarnWindow = 14 * 24 * time.Hour

// cookiesProbeRateLimit is the minimum interval between live cookie probes.
const cookiesProbeRateLimit = 120 * time.Second

// CookiesStatus reports what the static cookies-file check observed. It is a
// cheap, network-free read of the configured cookies file; Verified is only
// ever set by CheckCookiesLive.
type CookiesStatus struct {
	Configured  bool       `json:"configured"`
	Path        string     `json:"path,omitempty"`
	Readable    bool       `json:"readable"`
	ParseError  string     `json:"parse_error,omitempty"`
	AuthCookies bool       `json:"auth_cookies"`
	Expiry      *time.Time `json:"expiry,omitempty"`
	Verified    bool       `json:"verified"`
	LastProbe   *time.Time `json:"last_probe,omitempty"`
	ProbeError  string     `json:"probe_error,omitempty"`
}

// Available reports whether the static check is satisfied enough to use the
// cookies for authenticated yt-dlp requests such as --mark-watched.
func (s CookiesStatus) Available() bool {
	return s.Readable && s.AuthCookies
}

// UnavailableReason is the single source of truth for why the static check
// failed: probes and settings checks reuse it so wording cannot drift.
func (s CookiesStatus) UnavailableReason() string {
	switch {
	case !s.Configured:
		return "no YouTube cookies configured"
	case !s.Readable:
		return s.ParseError
	case !s.AuthCookies:
		return "cookies file has no signed-in session"
	}

	return ""
}

type youtubeCookie struct {
	domain  string
	name    string
	expires int64
}

// CheckCookies inspects the configured cookies file: configured → readable →
// contains a signed-in session, with the earliest auth-cookie expiry. It never
// touches the network and is safe to run per request. An empty path yields a
// plain "not configured" status.
func CheckCookies(path string) CookiesStatus {
	status := CookiesStatus{Path: path}
	resolved, err := commandPath(path)
	if err != nil || resolved == "" {
		return status
	}
	status.Configured = true
	status.Path = resolved
	file, err := os.Open(resolved)
	if err != nil {
		status.ParseError = SanitizeDiagnosticText(fmt.Sprintf("cookies file is not readable: %v", err))
		return status
	}
	defer file.Close()
	status.Readable = true

	cookies, parseErr := parseNetscapeCookies(file)
	if parseErr != nil {
		status.ParseError = SanitizeDiagnosticText(parseErr.Error())
		return status
	}

	expiring := time.Time{}
	expiringFound := false
	loginInfo := false
	sapisid := false
	for _, cookie := range cookies {
		if !isYouTubeAuthDomain(cookie.domain) {
			continue
		}
		authRelevant := false
		if cookie.name == youtubeAuthLoginCookieName {
			loginInfo = true
			authRelevant = true
		}
		for _, name := range youtubeAuthSAPISIDCookieNames {
			if cookie.name == name {
				sapisid = true
				authRelevant = true
			}
		}
		if authRelevant && cookie.expires > 0 {
			candidate := time.Unix(cookie.expires, 0)
			if !expiringFound || candidate.Before(expiring) {
				expiring = candidate
				expiringFound = true
			}
		}
	}
	status.AuthCookies = loginInfo && sapisid
	if expiringFound {
		status.Expiry = &expiring
	}

	return status
}

// parseNetscapeCookies reads the Netscape cookies.txt format: comment lines
// start with #, and data rows are tab-separated
// domain \t includeSubdomains \t path \t secure \t expiry \t name \t value.
func parseNetscapeCookies(file *os.File) ([]youtubeCookie, error) {
	var cookies []youtubeCookie
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			return nil, fmt.Errorf("cookies file line %d has %d fields, expected 7", lineNumber, len(fields))
		}
		expires, err := parseInt64Field(fields[4])
		if err != nil {
			return nil, fmt.Errorf("cookies file line %d has an invalid expiry", lineNumber)
		}
		cookies = append(cookies, youtubeCookie{domain: strings.ToLower(strings.TrimSpace(fields[0])), name: strings.TrimSpace(fields[5]), expires: expires})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cookies file could not be read: %w", err)
	}

	return cookies, nil
}

func parseInt64Field(value string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", value)
	}

	return parsed, nil
}

func isYouTubeAuthDomain(domain string) bool {
	for _, granted := range youtubeAuthCookieDomains {
		if domain == granted || strings.HasSuffix(domain, granted) {
			return true
		}
	}

	return false
}

// cookiesProbeState caches the most recent live probe result in-process so
// GET /api/settings can surface it without re-probing.
type CookiesProbeState struct {
	mu       sync.Mutex
	last     CookiesStatus
	probedAt time.Time
}

func NewCookiesProbeState() *CookiesProbeState {
	return &CookiesProbeState{}
}

// Allow reports whether a new probe may run, and the time a blocked caller
// should wait. Mirrors the loginLimiter pattern. A successful Allow claims the
// slot immediately, so concurrent probe requests are limited too.
func (s *CookiesProbeState) Allow(now time.Time) (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.probedAt.IsZero() {
		remaining := cookiesProbeRateLimit - now.Sub(s.probedAt)
		if remaining > 0 {
			return false, remaining
		}
	}
	s.probedAt = now

	return true, 0
}

// Record stores the probe result and returns the stamped copy (LastProbe set),
// which callers should use as the endpoint response.
func (s *CookiesProbeState) Record(status CookiesStatus, now time.Time) CookiesStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status.LastProbe = &now
	s.last = status
	s.probedAt = now

	return status
}

func (s *CookiesProbeState) Cached() CookiesStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.last
}

// CheckCookiesLive runs one yt-dlp invocation against a watch URL: pass =
// exit 0 and yt-dlp's debug output confirming the parsed cookies carry an
// account session ("Found YouTube account cookies"). A real request also
// proves the sandbox can read the file. The feed/subscriptions URL is avoided:
// with current yt-dlp nightly builds it stalls even with a valid session. The
// static result is carried over so the response always includes
// configured/readable/auth state.
const cookiesProbeURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
const cookiesProbeMarker = "Found YouTube account cookies"

func CheckCookiesLive(ctx context.Context, ytdlpPath string, cookiesPath string, runner Runner) CookiesStatus {
	static := CheckCookies(cookiesPath)
	static.Verified = false

	if !static.Available() {
		static.ProbeError = static.UnavailableReason()
		return static
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	if strings.TrimSpace(ytdlpPath) == "" {
		ytdlpPath = defaultYTDLPPath
	}
	command := Command{
		Name:           ytdlpPath,
		Args:           []string{"--ignore-config", "--cookies", static.Path, "--verbose", "--simulate", "--print", "%(id)s", cookiesProbeURL},
		Kind:           sandbox.KindYTDLP,
		Access:         sandbox.Access{ReadOnly: []sandbox.PathGrant{{Path: static.Path, Kind: sandbox.PathLiteral}}},
		Network:        sandbox.NetworkAllow,
		MaxStdoutBytes: maxDiagnosticOutput,
		CaptureStderr:  true,
	}
	output, err := runner.Run(ctx, command)
	text := string(output)
	if err != nil {
		static.ProbeError = SanitizeDiagnosticText(ytdlpCommandError(command, output, err).Error())
		return static
	}
	if !strings.Contains(text, cookiesProbeMarker) {
		static.ProbeError = SanitizeDiagnosticText("yt-dlp did not confirm an account session; the cookies file may be stale")
		return static
	}
	static.Verified = true
	static.ProbeError = ""

	return static
}
