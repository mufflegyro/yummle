# Add video delete button to thumbnails in sort views

## Summary

The watch page has a "Delete video" button that removes local media while preserving metadata, but there is no way to delete a video's media from grid views. Users who want to reclaim disk space must navigate to each video's watch page. Add a delete button overlay on thumbnails in the library, channel, and playlist grids so media can be removed inline.

## Current behavior

- `frontend/src/components/VideoCard.svelte` renders a thumbnail, metadata, and an optional catalog-download button for metadata-only items. There is no delete action for downloaded items.
- `frontend/src/App.svelte` has three `<div class="video-grid">` sections that use `<VideoCard>`:
  - **Library** (home, sort toolbar: For You / Newest / Recently Downloaded / Oldest / Length / Popularity) at `App.svelte:3611`
  - **Channel page** at `App.svelte:3696`
  - **Playlist page** at `App.svelte:3781`
- Search results at `App.svelte:3822` use a different, custom `<a class="episode-tile">` layout, **not** `<VideoCard>`.
- The existing delete action lives on the watch page only (`App.svelte:1573` `deleteVideoMedia`, `WatchRoute.svelte:94` "Delete video" button, revealed only when `video.item.media_url` is truthy). It calls `DELETE /api/videos/{id}/media` which:
  1. Clears `media_path`, `media_origin`, `media_downloaded_at` from the video row
  2. Removes the media file from disk (`assetpath.RemoveRegularMatching`)
  3. Deletes the `media_assets` record for `kind='media'`
  4. Keeps thumbnail, subtitle, comments, metadata, and the video record intact
  5. Returns `204`

## Design

### 1. What changes

**VideoCard.svelte** — add a delete button overlay on the thumbnail:

- Visible only when the item has downloaded media (`item.media_url` is truthy; equivalently `archive_state !== 'catalog-only'`).
- Not rendered when `item.archive_state === 'catalog-only'` (no media to delete).
- The button is a small, secondary overlay on the thumbnail (similar position to the duration badge but on the opposite corner) with a trash/dismiss icon or a "×" label.
- Optional: reveal on hover (`:hover .thumb-delete`) for a cleaner default look, matching common video-player thumbnail-delete patterns. On touch devices the button is always visible.

The component already accepts extensible props. Add two new optional props:

```svelte
let {
  onDelete = () => {},
  deleteDisabled = () => true,
} = $props();
```

**App.svelte** — wire the delete handler:

- Add a `deleteVideoMediaFromCard` async function (or reuse `deleteVideoMedia` with the card item passed as an argument) that:
  1. Confirms with the user (`window.confirm("Delete the local video file for "…")`)
  2. Calls `DELETE /api/videos/${id}/media`
  3. Refreshes the enclosing view — `loadLibrary()`, `loadChannel(channelID)`, or `loadPlaylist(playlistID)` depending on which view is active
  4. Reports errors inline (a brief notification/message, mirroring the pattern used by `channelDeleteAction`)

- Pass `onDelete` to `<VideoCard>` in each grid:
  - Library: `onDelete={(item) => deleteVideoMediaFromCard(item)}`
  - Channel page: same
  - Playlist page: same
  - Search results: **not wired** — search uses a different tile layout that is out of scope

- Add a per-card delete-action state (simple object `{ videoID, loading, error }` or reuse the existing `deleteVideoMediaAction` with an `itemID` check, similar to the request-token pattern at `App.svelte:1579`). Only one delete can be in flight across all cards; additional clicks while loading are ignored.

### 2. What stays the same

- The existing `DELETE /api/videos/{id}/media` handler at `server.go:2129` — **no backend changes needed**. It already clears the media path, removes the file, and cleans the `media_assets` row. After a successful delete the video becomes catalog-only and the thumbnail delete button disappears.
- The watch page "Delete video" button stays as-is.
- The catalog-download button and its logic (`enableCatalogDownload`, `showDownloadAction`) are unchanged.

### 3. Refreshing the parent view

The existing `deleteVideoMedia` at `App.svelte:1573` refreshes all visible views after deletion:

```js
loadLibrary({ showLoading: false });
if (channelID) loadChannel(channelID);
if (playlistID) loadPlaylist(playlistID, { showLoading: false });
```

The card-level handler should do the same, plus clear the video's `media_url` from local state so the grid reflects the change immediately without waiting for the full reload:

- Update the enclosing `library.videos`, `channelPage.videos`, or `playlistPage.videos` array: set `archive_state` to `'catalog-only'` and `media_url` to `''` for the affected video id.
- Then trigger the same set of refreshes.

### 4. Confirmation

Reuse `window.confirm` with copy matching the watch page:

```
Delete the local video file for "{title}"?

Metadata, comments, thumbnails, and catalog information will stay.
```

## Requirements

1. Add a delete button overlay to `<VideoCard>` thumbnails that appears when the card has downloaded media.
2. Exclude catalog-only (metadata-only) items — no media to delete.
3. Exclude search results — search uses a non-`VideoCard` tile layout; a separate issue could add it there.
4. Wire the button to the existing `DELETE /api/videos/{id}/media` endpoint.
5. Confirm before sending the mutation.
6. Refresh the enclosing grid view after a successful delete so the card reflects the catalog-only state.
7. Error reporting: show a brief inline message on failure, same as the channel/playlist delete pattern.

## Acceptance Criteria

- A downloaded video's thumbnail in the library grid, channel page, and playlist page shows a delete overlay.
- Clicking the overlay prompts for confirmation.
- Confirming calls `DELETE /api/videos/{id}/media` and returns `204`.
- After success the video appears as catalog-only (no `media_url`, no delete button, no player link) in the grid without a page refresh.
- Attempting to delete while another is in flight is ignored (loading guard).
- Catalog-only items (`archive_state === 'catalog-only'`) show no delete overlay in any grid.
- Search results show no delete overlay.
- The watch page delete button and its behavior are unchanged.
- Frontend regression tests cover the delete overlay rendering, confirmation gate, and the refresh flow on each grid type.

## Notes

- No backend changes are needed — the existing `DELETE /api/videos/{id}/media` handler already correctly handles the delete-and-keep-metadata semantics.
- The delete button replaces the thumbnail-link navigation temporarily when clicked (the thumbnail is normally a link to the watch page). Use `event.preventDefault()` or `event.stopPropagation()` on the button click to prevent navigation.
- The existing `deleteVideoMediaAction` state and request-token pattern at `App.svelte:1579` is a proven pattern to reuse; consider extracting a reusable per-item delete state rather than adding a new global one.
- Touch devices: the delete button must be always visible, not only on hover, since there is no hover state.

## Review references

- `frontend/src/components/VideoCard.svelte` — full component (thumbnail, metadata, download action)
- `frontend/src/App.svelte:3611` — library `video-grid` with `<VideoCard>`
- `frontend/src/App.svelte:3696` — channel page `video-grid` with `<VideoCard>`
- `frontend/src/App.svelte:3781` — playlist page `video-grid` with `<VideoCard>`
- `frontend/src/App.svelte:3822` — search results `video-grid` (excluded — uses `<a class="episode-tile">`)
- `frontend/src/App.svelte:1573` — `deleteVideoMedia` function (reference for confirmation, API call, refresh pattern)
- `frontend/src/App.svelte:169` — `deleteVideoMediaAction` state
- `frontend/src/App.svelte:1480` — `deleteChannel` (reference for per-button state pattern)
- `frontend/src/display.js` — `isCatalogOnly` (determines whether to show the button)
- `internal/server/server.go:2129` — `deleteVideoMedia` handler (no changes needed)
- `internal/server/server.go:304` — route `DELETE /api/videos/{id}/media`
- `internal/server/server_test.go` — existing `TestDeleteVideoMedia*` coverage
- `meta/issues/add-delete-video-media-action.md` — the original issue that added the watch-page button

## Resolution

Implemented exactly as scoped, with zero backend changes (the existing `DELETE /api/videos/{id}/media` handler already had the delete-and-keep-metadata semantics):

- `VideoCard.svelte` gains optional `onDelete` / `deleteDisabled` props and a `.thumb-delete` trash-icon overlay rendered inside a new `.thumb-wrap` container, only when the item has downloaded media (`!isCatalogOnly(item) && !!item.id`). The click handler `preventDefault()` + `stopPropagation()`s so the overlay can never act as the thumbnail navigation link. The catalog-download overlay and its behavior are unchanged.
- `App.svelte` adds `deleteVideoMediaFromCard(item)`: `window.confirm` with the watch-page copy ("Delete the local video file for … Metadata, comments, thumbnails, and catalog information will stay."), then `DELETE /api/videos/{id}/media`, then an optimistic `markCatalogOnly` pass over `library.videos`, `channelPage.videos`, and `playlistPage.videos` so the overlay and player link vanish immediately, followed by the standard `loadLibrary` / `loadChannel` / `loadPlaylist` background refreshes.
- One delete in flight across all cards, via a dedicated `deleteVideoMediaCardAction` state + `deleteVideoMediaCardRequestToken` token guard (mirroring the existing pattern): while a delete runs, every card's delete button is disabled and extra clicks are ignored. Success (`role=status`) and failure (`role=alert`) banners render inline above the grid with `data-testid=video-card-delete-status`; the success banner self-clears after 4 seconds.
- `style.css` positions the overlay opposite the duration badge; desktop reveals it on hover, touch devices keep it permanently visible (no hover state).
- Search-result episode tiles are intentionally not wired (different tile layout, out of scope per the issue); catalog-only cards show no overlay in any grid.

Tests: six new smoke tests — library and playlist grid deletion end-to-end, double-click guard, inline failure reporting, search-tile exemption, and hover/touch visibility — run across the desktop and mobile Playwright projects (suite: 131 passed, 1 skipped). `svelte-check`: 0 errors, 0 warnings. Status: **landed 2026-09-09**.