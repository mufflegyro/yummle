# Mark YouTube videos watched on save (server-side yt-dlp)

## Summary

When a video is saved to the archive from the userscript, also mark it
watched in the user's YouTube account, using yt-dlp's existing
`--mark-watched` during the download. The marking is always on for saves,
but only ever runs when the YouTube session is active: no `--mark-watched`
is added to a download command unless the cookies status check passes.
Bulk flows (channel scans, playlist imports) never mark. A skipped mark is
recorded on the job so the UI can say why.

## Current state

- `POST /api/downloads` accepts `download.Payload` (`URL`, `Origin`,
  `ScanOnly`) at `internal/download/enqueue.go:12`; handler
  `createDownload` at `internal/server/server.go:1012`.
- The worker builds the yt-dlp command in `internal/download/ytdlp.go:295`
  (`BuildCommand`), called from `internal/download/ingest.go:38` with just
  the URL. `--cookies` is appended when configured (`ytdlpArgs`, `:509`);
  threading a per-job mark flag means extending `BuildCommand` and the job
  payload.
- yt-dlp: `--mark-watched` fires during extraction (before media download;
  zero extra requests), gated on `mark_watched` + cookies-passed
  (`yt_dlp/extractor/common.py`, `mark_watched`); the ping itself is
  `_mark_watched` (`yt_dlp/extractor/youtube/_video.py:2263`) hitting the
  playtime stats URLs (`videostatsPlaybackUrl` + `videostatsWatchtimeUrl`).
  Without a real session it is a silent no-op — hence the prerequisite.
- Known partial-progress behavior: the video lands in watch history but
  progress can read below 100% (yt-dlp issues #8236, #11532). Accepted
  tradeoff; do not re-try or attempt repair.

## Decisions (user-approved)

1. **Mark during the download** (in-flight, during extraction). Accepted
   cost: a download that later fails can still leave the video "watched".
2. **Always on, gated by cookies** — the userscript sends
   `mark_watched: true` on every save; the server only adds `--mark-watched`
   when the cookies check is active at command-build time.
3. **Prerequisite enforced server-side** — `mark_watched` with inactive
   cookies: the download proceeds, the mark is skipped, and the skip reason
   is recorded on the job (no 4xx that would block archiving).

## Design

- `download.Payload` gains `MarkWatched bool json:"mark_watched,omitempty"`;
  `NormalizeDownloadPayload` passes it through. It is only ever set by the
  direct-video flow (userscript body `{"url": ..., "mark_watched": true}`);
  channel-auto and playlist flows never set it, so bulk imports are
  untouched.
- Job: persist `mark_watched` in the job payload/result JSON used by the
  worker; the download result records
  `mark_watched: "applied" | "skipped"` plus the skip reason when the cookie
  check fails at run time, surfaced on the job row/result on the Downloads
  page.
- `BuildCommand(rawURL string)` becomes `BuildCommand(rawURL string,
  markWatched bool)` (update the call site at
  `internal/download/ingest.go:38`): append `--mark-watched` only when
  `markWatched && cookiesActive(config.YTDLPCookiesFile)`.
  `cookiesActive` = configured + readable + auth cookies from the static
  `download.CheckCookies` (Issue 1); cheap, no probe, evaluated at command
  build time.
- README/API docs: document the optional `mark_watched` field on
  `POST /api/downloads`.
- Web UI: out of scope for v1 (the topbar queue does not mark); noted as a
  follow-up.

## Acceptance

- Userscript save with active cookies → job runs with `--mark-watched`;
  video appears in the account's watch history.
- Userscript save with no or invalid cookies → job still downloads; job
  result says the mark was skipped and why; nothing fires at YouTube.
- Channel/playlist/auto downloads never include `--mark-watched`.
- Unit tests: `BuildCommand` includes the flag exactly when
  `markWatched && active` and omits it otherwise; payload normalization
  preserves `mark_watched`; job result records the skip reason (injected
  runner).

## Blocked by

- `surface-youtube-cookies-status-in-settings`
- `add-client-capabilities-endpoint`
## Implementation notes (2026-09-20, pending sign-off)

- `download.Payload` gained `MarkWatched bool json:"mark_watched,omitempty"`
  (only the direct-video flow sets it; `NormalizeDownloadPayload` passes it
  through untouched, so channel/playlist/auto flows are untouched).
- `BuildCommand(rawURL string, markWatched bool)`: appends `--mark-watched`
  only when requested and `d.cookiesActive()` (static
  `CheckCookies().Available()` — configured + readable + signature cookies).
  Nowhere else builds download commands this way, so bulk flows can't leak
  the flag.
- Skip handling: `handlePayload` decides once at command-build time — applied
  or skipped with `d.cookiesSkipReason()` — and both statuses flow through
  `ingest()` (including the subtitle-fallback path) into the job result:
  `mark_watched: "applied"|"skipped"` + `mark_watched_skip_reason` on the
  ingest result JSON, surfaced on the job row (Downloads page reads job
  results as-is).
- README documents the flag and the accepted costs (failed download can still
  mark; no retry/repair of partial progress).
- Tests: `TestBuildCommandMarkWatchedGating` (flag exactly when requested &&
  active; cookies omitted otherwise), `TestNormalizeDownloadPayloadPreservesMarkWatched`,
  `TestDownloadJobRecordsMarkWatchedSkipReason` (injected runner, missing
  cookies file → job succeeds and result says skipped with reason).
  `TestBuildCommand` etc. updated for the new signature. `go test ./...` green.
