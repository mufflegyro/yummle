# Client capabilities endpoint for the save userscript

## Summary

A cheap, auth-gated `GET /api/capabilities` so the Yummle save userscript can
learn whether the server can mark YouTube videos watched on save — without
paying the cost of `GET /api/settings`, which runs a full media-root
`storage.Scan` on every call (`internal/server/server.go:767`). The script
fetches it once per page load for its save-button copy; the `mark_watched`
flag itself is always sent and the server enforces the cookie prerequisite
(see `mark-youtube-videos-watched-on-save`).

## Current state

- The userscript lives in the `userscript` worktree
  (`~/Src/pi-working-folder/mytube-userscript`), file
  `scripts/yummle-save.user.js`. It knows the server URL and calls
  `GET /api/videos/<id>` and `POST /api/downloads` today (`queueVideo`,
  about `scripts/yummle-save.user.js:210`).
- `GET /api/settings` is heavy (storage scan + diskspace check), so it is not
  a viable per-page call for the script.
- `GET /api/session` (`internal/server/server.go:414`) is cheap but is the
  auth-state surface; capabilities are a separate concern and a separate
  endpoint keeps both responses stable.

## Design

### `GET /api/capabilities` (main branch)

- Auth-gated via the existing `requireAuth` wrapper (the script already sends
  the session cookie through `GM_xmlhttpRequest`).
- Computes the static cookie status only (`download.CheckCookies` from
  `surface-youtube-cookies-status-in-settings`) — no probe, no storage scan,
  no diskspace walk.
- Response shape:

```json
{
  "mark_watched": {
    "available": true,
    "reason": null
  }
}
```

  `available` is true only when the cookies file is configured, readable, and
  contains a signed-in session. `reason` carries the shortfall for the UI
  ("no YouTube cookies configured", "cookies file has no signed-in session",
  ...). Unknown capabilities are simply absent — an older server implies no
  mark-watched support and the script treats that as unavailable.
- The web UI can reuse this later (e.g. the topbar "Queue a video" panel
  offering "also mark watched"); no UI change in this issue.

### Userscript alignment (userscript branch)

- Merge order: bring `main` into the `userscript` worktree first (currently
  61 commits behind); the endpoint must exist before the script reads it.
- On page load, after the server URL is known, fetch `GET /api/capabilities`
  once per page (in-memory cache, no GM storage). On failure (auth or
  network), remember "unknown": the save button still works, `mark_watched`
  is still sent, and the server enforces the prerequisite.
- Save-button copy:
  - `mark_watched.available === true` → title "Save to Yummle (marks watched
    on YouTube)".
  - false with a reason → append the reason to the title.
  - unknown → default copy.
- Expose the fetched capabilities in `__yummleSave.debug()`.

## Acceptance

- `GET /api/capabilities` returns mark-watched availability consistent with
  the static cookie check; cost is bounded (no scan, no probe, no diskspace).
- Userscript fetches capabilities once per page, never per thumbnail, and
  degrades to "unknown" on error.
- Save-button copy reflects availability; diagnostics include it.

## Blocked by

- `surface-youtube-cookies-status-in-settings`