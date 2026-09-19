# Bound job error output and summarize non-failed job errors

Triage: needs-triage

## Summary

A single failed download stores ~3.4 MB of yt-dlp stdout in `jobs.error`. The jobs API returns that field verbatim, so one bad job makes `GET /api/jobs` a ~3.9 MB response, and the UI renders it into the job card. Because the UI's error summariser only runs for `status === 'failed'`, **cancelling** that job renders `Previous error: <3.4 MB of JSON>` — thousands of lines of format metadata on the downloads page, with the real cause (`ERROR: unable to download video data: HTTP Error 403: Forbidden`) buried at the very end of the string.

Reported by the user after cancelling job `45aa0fc6-d5db-4219-ad29-3b5396929476`, which is the exact string that opened the report.

## Current state

Three independent layers each contribute; fixing only one leaves the symptom.

**1. The error message is ordered wrong.** `internal/download/diagnostics.go:67-87` (`ytdlpCommandError`) sets `detail` to the whole captured output — for downloads that is `stdout`, which holds the `--dump-single-json` info dump — and appends `(<exit status 1>)` after it. `internal/download/downloader.go:104-141` (`ExecRunner.Run`) returns *uncapped* stdout on error when `command.MaxStdoutBytes <= 0` (true for download commands), which is deliberate: the info JSON carries `requested_downloads`, needed to reconstruct a download that actually succeeded but exited non-zero (see `fix-ytdlp-download-json-failure.md`). The consequence is that the actionable line is the last one, and every front-truncating surface drops it.

**2. Storage and serving are unbounded.** `sanitizeStoredDiagnosticText` is `redact.Text(text, 0)` (`internal/download/diagnostics.go:141-143`), and `redact.Text` treats `maxLength <= 0` as "no cap" (`internal/redact/redact.go:30-34`), so the whole dump is persisted. `internal/server/job_dto.go:23-41` (`publicJobResponse`) and `:43-61` (`publicJobListItem`) copy `job.Error` straight through with no cap, reaching the wire via `getJob`, `cancelJob`/`retryJob` (`internal/server/job_handlers.go:14,31,43-70`) and `listJobs` (`:93`). Only `diagnosticErrors` (`:120`) truncates, using `download.SanitizeDiagnosticText` at `maxDiagnosticLength = 1200` (`internal/download/diagnostics.go:16`); `internal/jobs/store.go:35` defines an equivalent `MaxDiagnosticErrorLength = 1200`.

**3. The UI skips summarisation for every status except `failed`.** `frontend/src/App.svelte:2434-2441` (`jobSummary`) calls `jobErrorSummary` only when `status === 'failed'`, then falls through to `if (job.error) return \`Previous error: ${job.error}\`` (`:2439`). A cancelled job therefore bypasses `jobErrorSummary` (`:2443-2453`, which correctly picks the first `ERROR:`-ish line and caps at 180 chars) and renders the raw string. The bounded raw-log `<textarea>` at `:3982` is gated on `job.status === 'failed' && job.error` (`:3975`), so for cancelled jobs the raw text spills into the summary paragraph at `:3974` instead.

## Measured impact (2026-09-19, live instance)

| Item | Value |
|---|---|
| Job `45aa0fc6-…` (`cancelled`) `jobs.error` | 3,485,608 bytes |
| `GET /api/jobs/45aa0fc6-…` | 3,923,232 bytes |
| `GET /api/jobs?page_size=50` | 3,944,705 bytes |
| All 48 `jobs.error` rows in the DB | 3,507,236 bytes (this one row = 99.4%) |
| Every other failed job | 565 bytes |

## Options

1. **Reorder the message, keep the data.** In `ytdlpCommandError`, when the output contains a line starting `ERROR:`, lead with those lines and demote the info dump (or omit it from the message while keeping it in the process result). Fixes readability; does not shrink the payload.
2. **Cap the public projection.** Apply `jobs.MaxDiagnosticErrorLength` (1200) in `publicJobResponse`/`publicJobListItem`, matching what `diagnosticErrors` already does. `fix-ytdlp-download-json-failure.md` explicitly permits this ("Public diagnostic summary endpoints may truncate for UI safety, but the database row preserves the full redacted error"), so it does not reverse that decision.
3. **Cap the stored row.** Change `sanitizeStoredDiagnosticText` to pass a limit. Directly contradicts `fix-ytdlp-download-json-failure.md`'s "store full redacted stdout/stderr diagnostics in `jobs.error`" — only take this if the DB-preserves-everything guarantee is deliberately given up. Note the 3.4 MB stdout here carries no diagnostic value: it is `--dump-single-json` output, not stderr.
4. **Route every status through `jobErrorSummary`.** Remove or cap the raw `Previous error:` fallback in `App.svelte:2439`, and show the bounded raw-log `<textarea>` for `cancelled` too so the full text stays copyable.

## Recommended scope

Options 1, 2 and 4. Together they make the failure legible, bound the API without reversing the earlier DB guarantee, and fix the cancel path that actually produced the report. Option 3 is a separate decision about the storage guarantee and should stay out of this change.

## Acceptance

- A cancelled job whose `error` is megabytes long renders a one-line summary on the downloads page, not the raw text.
- `jobSummary` returns a bounded summary for every status that carries an error; the raw log remains available in bounded form (textarea or equivalent) and stays copyable.
- `GET /api/jobs` and `GET /api/jobs/{id}` cap the `error` field; a job with a multi-megabyte error cannot inflate the list response beyond the cap.
- The `ERROR:` line of a failed yt-dlp command is the leading content of the error message.
- Regression tests cover: a job error containing a large JSON blob plus a trailing `ERROR:` line; the jobs-list/detail DTO cap; and the UI summary for a `cancelled` job.
- The succeeded-download reconstruction path (`requested_downloads` from stdout) still passes its existing tests.

## References

- `internal/download/diagnostics.go` — `ytdlpCommandError`, `sanitizeStoredDiagnosticText`, `sanitizeDiagnosticText`
- `internal/download/downloader.go` — `ExecRunner.Run` uncapped-on-error behaviour
- `internal/server/job_dto.go`, `internal/server/job_handlers.go` — job DTO projection
- `frontend/src/App.svelte` — `jobSummary`, `jobErrorSummary`, raw-log textarea
- `meta/issues/fix-ytdlp-download-json-failure.md` — why uncapped diagnostics were introduced, and the sanction for truncating the public projection
- `meta/issues/improve-job-and-diagnostics-readability.md` — prior intent to summarize long job errors and bound raw logs
- Observed failure context: `ERROR: unable to download video data: HTTP Error 403: Forbidden` on video `Knkkdu9w6kY` (the 403 is the known intermittent YouTube issue, yt-dlp #17395 — unrelated to this bug)
