# Add episodes to a new playlist from the UI

## Summary

Playlists can currently only be created via CSV upload or YouTube URL import
(both on the `/playlists` page). There is no way to create a playlist from
scratch or add individual videos — whether downloaded or catalog-only — to a
playlist directly from the video card, watch page, or any browse view.

Add the ability to create a new playlist and add episodes to it from anywhere
videos are displayed: library, home, channel page, search results, and the
watch page. Every video card gains an "Add to playlist" action that opens a
playlist picker (with a "Create new playlist" option), and the backend gets
the CRUD endpoints to support it.

## Current state

- **Schema:** `playlists` table (id, title, description, source, external_id,
  channel_id, subscribed) and `playlist_entries` table (playlist_id, video_id,
  position) — both exist and are used by the import paths.
- **API:** `GET /api/playlists`, `GET /api/playlists/{id}`,
  `GET /api/playlists/{id}/videos`, `DELETE /api/playlists/{id}`,
  `POST /api/playlists/import` (CSV), `POST /api/playlists/import-url`.
- **Frontend:** `/playlists` route shows a list of imported playlists;
  `/playlists/{id}` shows the playlist's videos in a grid. No create,
  add-video, or remove-video UI exists.
- **No backend endpoint exists** to create a playlist, add a single video,
  or remove a single video from a playlist.

## Design

### Backend

#### New endpoints

| Method | Path | Purpose |
|--------|------|---------|
| `POST /api/playlists` | Create a new playlist with a title (and optional description). Returns the new playlist id. |
| `POST /api/playlists/{id}/videos` | Add one or more videos to a playlist (append at the next position). Accepts `{video_ids: string[]}`. |
| `DELETE /api/playlists/{id}/videos/{videoId}` | Remove a single video from a playlist. Re-numbers remaining positions. |

#### `POST /api/playlists`

Request:
```json
{ "title": "My New Playlist", "description": "Optional description" }
```

Response (201):
```json
{ "id": "local-abc123", "title": "My New Playlist", "description": "...", "video_count": 0 }
```

- Playlist id is deterministic: `local-<slugified-title>` (or a hash), matching
  the existing `slugify` pattern in `playlistimport`. If the id already exists,
  return 409 Conflict (or 200 if the caller wants to reuse it — decide at
  implementation).
- `source` = `"local"`, `external_id` = the slug/id.
- No `channel_id` — user-created playlists are not tied to a channel.
- Auth-gated.

#### `POST /api/playlists/{id}/videos`

Request:
```json
{ "video_ids": ["abc123DEF45", "xyz789GHI01"] }
```

- Appends each video at the next available position.
- Ignores duplicates (already in the playlist) silently — idempotent.
- Returns 404 if the playlist does not exist.
- Returns 400 if any `video_id` does not exist in the `videos` table.
- Also upserts a search document so the video becomes discoverable under the
  playlist's title (matching the existing denorm sync in `playlistimport`).
- Auth-gated.

#### `DELETE /api/playlists/{id}/videos/{videoId}`

- Removes the playlist entry.
- Re-numbers remaining entries so positions stay contiguous (or keep gaps —
  decide at implementation; contiguous is friendlier for position-based
  ordering).
- Returns 204 No Content.

### Frontend

#### "Add to playlist" action on video cards

Every video card (library, home, channel page, search results, playlist page,
watch page's Up next) gains a small "Add to playlist" button/icon — a
`+` icon or a bookmark-style icon, visible on hover or always present.

Clicking it shows a **playlist picker overlay**:

- A searchable/filterable list of existing playlists (fetched from
  `GET /api/playlists`).
- A "Create new playlist" option at the top or bottom of the list.
- Each playlist row shows the title and video count.
- Clicking a playlist adds the video and closes the picker with a brief
  confirmation toast/animation.
- The picker is a lightweight popover (not a full-page modal), positioned
  near the clicked button.

#### Create new playlist flow

When the user clicks "Create new playlist" in the picker:

1. A small inline form appears (title input, optional description).
2. On submit, `POST /api/playlists` creates the playlist.
3. The video is then added to it via `POST /api/playlists/{id}/videos`.
4. The picker closes with a confirmation.
5. The new playlist appears in the picker immediately on next open.

#### Watch page action

The watch page also gets an "Add to playlist" button in the video metadata
area (next to the title or in the action bar), with the same picker flow.

### Position ordering

New videos are appended at the end (max position + 1). The playlist video list
(`GET /api/playlists/{id}/videos`) already orders by `position ASC`, so new
entries appear last. This matches the import behavior.

### Search document sync

When a video is added to a playlist, the playlist's search document (title +
description) should be updated to reflect the new entry. The existing
`denorm.DeleteSearchDocumentsForOwner` / `denorm.UpsertSearchDocuments` pattern
used by `playlistimport` should be followed.

## Acceptance Criteria

- A user can create a new playlist from the `/playlists` page with a title and
  optional description. It appears in the playlist list immediately.
- Every video card in the library, home, channel, search, and playlist views
  has an "Add to playlist" action.
- Clicking "Add to playlist" shows a picker of existing playlists plus a
  "Create new" option.
- Adding a video to a playlist appends it; the video appears in the playlist
  detail view.
- Adding the same video twice is a no-op (idempotent).
- Removing a video from a playlist removes it from the playlist detail view.
- User-created playlists have `source = "local"` and are visually distinct from
  imported YouTube playlists (e.g. a badge or label).
- Deleting a user-created playlist removes it and its entries.
- Backend endpoints are covered by server handler tests.
- Frontend interactions are covered by e2e smoke tests (create playlist, add
  video, verify it appears in the playlist detail).

## Out of scope

- Reordering playlist entries (drag-and-drop) — follow-up.
- Sharing playlists or exporting them.
- Auto-download all videos in a playlist.
- Collaborative playlists.
- Nested/grouped playlists.