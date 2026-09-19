# Add a searchable watch history list view

Triage: needs-triage

## Summary

The `/history` route exists only as a placeholder. Make it a real watch-history page: a **list** (one row per entry, standard-sized thumbnail on the left, metadata to the right) rather than the `video-grid`, with a **filter box in a right-hand column** that searches across the metadata of everything watched.

## Layout (as interpreted from the request — confirm at triage)

- Page is two columns, results left, filter box right — the same shape as the watch page (`frontend/src/style.css:1839-1842`, `.watch-page` uses `minmax(0, 1fr) minmax(18rem, 27rem)`).
- Each row: standard-sized thumbnail on the left (not the 4–5 rem micro-thumb used by the existing row lists), then title, channel, duration, watched date and playback progress.
- Not the grid: `.video-grid` (`frontend/src/style.css:1413-1416`, `repeat(auto-fill, minmax(17.5rem, 1fr))`) and `VideoCard` are for the grid surfaces and are not the right primitive here.

## Current state

- **Route is a stub.** `frontend/src/App.svelte:57` defines `utilityRoutes`; `:73-76` is the `/history` entry, whose copy is still future-tense ("Watched videos and progress history will appear here once progress sync is expanded."). It renders through the generic placeholder block at `:3899-3902`, with the page title taken from `:2195`. It is **not** in `sidebarPrimary` (`:19-25`), so it is currently unreachable from the nav.
- **Watched data already exists.** `user_progress` holds `position_seconds`, `duration_seconds`, `watched`, `updated_at`. There is **no per-video `watched_at` column**; `user_progress.updated_at` is the watch-history timestamp and is already treated as such — `videoWatchingSort` (`internal/server/server.go:1971-1978`) orders by `COALESCE(NULLIF(p.updated_at, ''), '')`. `videos` carries a separate boolean `watched`.
- **No watched-only filter or text query on the video list.** `GET /api/videos` (`internal/server/server.go:300`, handler `:1685-1698`) supports `sort`, `hide_watched`, `channel` and `playlist` only. `videoFilters` (`:1891-1907`) has no watched-only case (the existing helper is `homeUnwatchedVideoFilter`, `:1967-1969`, which does the opposite) and no text term. `videoSort` (`:1909-1934`) has no "watched" mode — `videoWatchingSort` covers *in-progress* and explicitly excludes `watched = 1`.
- **Search exists but is not watched-scoped.** `GET /api/search` (`:298`, handler `:1608+`) runs FTS5 over `search_documents` via `internal/search` (`search.Query` is term/limit/offset — `internal/search/search.go:101-105`), with no watched constraint and no way to intersect with one.
- **Row-list precedents exist but with small thumbs.** `.result-list a` (`frontend/src/style.css:2531-2538`) and `.channel-list a` (`:2595-2602`) are already list rows (`4rem` / `5.25rem` thumbnail tracks), used by the search secondary block (`App.svelte:3848-3862`) — a useful skeleton, but the thumbnail size and the two-column shell are new.

## Design questions to settle

1. **What counts as "watched"?** Finished only (`user_progress.watched = 1`), or anything with progress (`position_seconds > 0`), or both with a filter? The two are different sets today and `videoWatchingSort` already distinguishes them.
2. **What does the filter box search?** Options: (a) reuse the FTS5 index (`internal/search`) and intersect with the watched set; (b) a bounded `LOWER(title/channel) LIKE` term added to `videoFilters`; (c) new `q` support in the watch-history endpoint only. (a) gives snippets and ranking but needs a watched constraint threaded through `search.Query`; (b) is smaller but weaker.
3. **Is the box free-text only, or does it also carry facets?** e.g. channel, date range, finished vs in-progress, or a "watched in the last N days" control.
4. **Which ordering?** Most-recently-watched first by `user_progress.updated_at` is the obvious default; confirm whether an alternate sort is wanted.
5. **Per-row actions?** Grid cards carry delete-media overlays and the watch page carries keep-forever / mark-played. Decide whether history rows get any actions or stay read-only links.
6. **Where does it live in navigation?** Add to `sidebarPrimary`, or leave it reachable only by URL/Explore link?

## Acceptance

- `/history` renders a real page (placeholder removed) and is reachable from navigation.
- Entries render as list rows with a standard-sized left thumbnail and title/channel/duration/watched-date, not as `video-grid` cards.
- The filter box sits in a right-hand column and narrows the list by the chosen metadata; the filtered result count and empty state are shown.
- Results are paginated/bounded (the existing `page`/`page_size` convention, WAL-safe, no unbounded query).
- Ordering is most-recently-watched first.
- Backend regression tests cover the watched-only filter, the text filter (whichever option is chosen), pagination bounds and ordering.
- Frontend checks pass (`pnpm check`, build) and a browser smoke covers loading history, filtering, and the empty state.

## Notes

- Reuse rather than reinvent: `user_progress.updated_at` for recency and the `.result-list` row skeleton for the row shape.
- The existing `hide_watched` checkbox (`App.svelte`, `videoFilters`) is the inverse control and is not a substitute for this view.
- Do not sign media URLs in list responses — list endpoints deliberately omit them (`remove-media-urls-from-list-responses.md`); history rows should follow the same rule and let the watch page re-request playback URLs.
