# Surface YouTube cookie status in settings

## Summary

Make `KAPSEL_YTDLP_COOKIES_FILE` a first-class, observable setting: a static
check on the Settings page (configured → readable → contains a signed-in
session, with expiry), surfaced as a readiness check with warnings, plus an
on-demand "Test YouTube cookies" button that runs one real yt-dlp request to
prove the session is live. This is the prerequisite for "mark YouTube videos
watched on save" (`mark-youtube-videos-watched-on-save`): without a verified
session that feature must stay disabled.

## Current state

- `KAPSEL_YTDLP_COOKIES_FILE` is parsed at `internal/config/config.go:130`
  (`EnvYTDLPCookiesFile` at `:34`, field at `:79`) and handed to the
  downloader (`internal/download/downloader.go:79`, `internal/app/app.go:66`).
- The cookies path is resolved only at command-build time:
  `commandPath()` (`internal/download/ytdlp.go:485`) does path resolution but
  never checks existence or readability; `ytdlpArgs(...)` (`:509`) appends
  `--cookies` whenever the path is non-empty — regardless of whether the file
  exists, is readable by the sandboxed child, or contains any session.
- `SettingsDiagnostics` (`internal/server/server.go:162`) carries no cookie
  fields. `GET /api/settings` (`:753`) already exposes `yt_dlp` status via
  `CheckYTDLP` with an injected runner (`WithYTDLPDiagnostics`, `:220`) — the
  pattern to mirror — but nothing about cookies.
- The frontend renders rows from `/api/settings` in
  `frontend/src/App.svelte:914` and `:3429`, displayed by
  `frontend/src/routes/SettingsDiagnosticsPanel.svelte`; there is no cookie
  awareness anywhere in the UI.
- yt-dlp gates `--mark-watched` on "cookies passed", not "valid session"
  (`yt_dlp/extractor/common.py`, `mark_watched`): with a stale or anonymous
  cookie file it fires the pings and YouTube silently ignores them. A
  configured path is therefore not evidence of a working session.

Result: a misconfigured cookies setup is invisible until a download fails
with "Sign in to confirm you're not a bot" deep in a job log.

## Design

### Static check (`download.CheckCookies`)

New `internal/download` function and status type:

```go
type CookiesStatus struct {
    Configured  bool      `json:"configured"`
    Path        string    `json:"path,omitempty"`
    Readable    bool      `json:"readable"`
    ParseError  string    `json:"parse_error,omitempty"`
    AuthCookies bool      `json:"auth_cookies"`         // signed-in session present
    Expiry      time.Time `json:"expiry,omitempty"`     // earliest auth-cookie expiry
    Verified    bool      `json:"verified"`             // true only from a live probe
    LastProbe   time.Time `json:"last_probe,omitempty"`
    ProbeError  string    `json:"probe_error,omitempty"`
}
```

`CheckCookies(path string) CookiesStatus`:

- empty path → `Configured=false`.
- resolve with `commandPath`; `os.Stat` + read → missing/unreadable →
  `ParseError`.
- parse the Netscape format
  (`domain \t includeSubdomains \t path \t secure \t expiry \t name \t value`).
- signed-in detection: auth cookie names `SID`, `__Secure-1PSID`,
  `__Secure-3PSID`, `SAPISID`, `__Secure-1PAPISID`, `__Secure-3PAPISID` whose
  domain is `.youtube.com` or `.google.com` — matches what
  `scripts/export-cookies.sh` writes. `Expiry` = earliest `expires` epoch
  among those (skip `0` = session cookie → no expiry).
- no network I/O; safe to run per request.

### Settings surface

- Add `YTDLPCookiesFile` to `SettingsDiagnostics` (`internal/server/server.go:162`)
  and populate it in the `WithSettingsDiagnostics` block at
  `internal/app/app.go:105`.
- New option `WithCookiesDiagnostics(path string, runner download.Runner)`
  mirroring `WithYTDLPDiagnostics`.
- `settingsResponse` (`server.go:718`) gains `Cookies *download.CookiesStatus`;
  `getSettings` computes it with the static check only — no probe on page
  load.
- New readiness check `youtube_cookies` in `settingsChecks` (`:808`):

  | State | Condition |
  |-------|-----------|
  | warn  | not configured — "mark watched on save" unavailable; some videos fail signed-out |
  | error | configured but missing / unreadable / unparseable |
  | error | readable but no signed-in cookies found |
  | warn  | auth cookies present but earliest expiry < 14 days |
  | pass  | signed-in cookies present (plus `Verified` once probed) |

- Frontend: render the row + detail in `SettingsDiagnosticsPanel.svelte`
  (state, expiry date, `ProbeError`) and the "Test YouTube cookies" button.

### Live probe (`POST /api/settings/test-youtube-cookies`)

- Auth-gated. Rate-limited: minimum 120s between probes (in-process limiter
  like `loginLimiter`); button disabled with a countdown while cooling down.
- Runs `CheckCookiesLive`: one yt-dlp invocation that requires a signed-in
  session, e.g.
  `yt-dlp --cookies <file> --verbose --simulate --flat-playlist --print "%(id)s" "https://www.youtube.com/feed/subscriptions"`.
  Pass = exit 0 and stderr contains the debug line
  `[youtube] Extracted SAPISID cookie`. The exact command may need iteration
  during implementation (poToken changes); on failure, record yt-dlp's
  output into `ProbeError` so the UI can explain.
- Uses the injected-runner pattern; tests inject a fake runner, never a real
  probe.
- Result cached in-process (`Verified`, `LastProbe`, `ProbeError`) and
  surfaced by `GET /api/settings` and `GET /api/capabilities`.
- Note: the static check reads the file as the *server* process sees it; the
  sandboxed yt-dlp child can still fail on permissions. Only the live probe
  catches that — it is the authoritative check.

## Acceptance

- `GET /api/settings` returns `cookies` with configured/readable/auth/expiry,
  and never fires a probe by being called.
- Fixture cookie files: signed-in session → pass with expiry; no auth
  cookies → error; missing file → error; env unset → warn.
- `POST /api/settings/test-youtube-cookies` runs one yt-dlp probe, enforces
  the rate limit, stores the result, and it shows up on the next
  `GET /api/settings`.
- Settings page shows the check, the expiry, and a working test button with
  surfaced failure text.
- Unit tests for `CheckCookies` parsing (auth present/absent, expired,
  missing file, session-only expiry) and handler tests for the probe with an
  injected runner.

## Notes

- The 14-day expiry warning threshold is a soft number; pick one and document
  it in the env example.
- `Path` is exposed like the other roots (auth-gated settings response) —
  the path, never the file contents.