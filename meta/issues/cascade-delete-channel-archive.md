# Delete a channel subscription and its archived content

## Summary

`DELETE /api/channels/{id}` currently refuses whenever a channel has downloaded media or any playlist. That guard exists for good reason, but it means a channel that has ever archived an episode can never be removed — the only escape is deleting each video's media by hand. Add an explicit, confirmed cascade: removing a channel with content deletes the channel row, all of its video rows, their denormalized metadata and search documents, and the media/thumbnail/subtitle/preview files on disk.

## Current behavior

- Route: `internal/server/server.go:293`; handler `deleteChannel` at `internal/server/server.go:2587`.
- Guard at `internal/server/server.go:2620-2632` returns `409` when any `videos.media_path <> ''` or any `playlists.channel_id = ?` row exists.
- The catalog-only path deletes video rows and the channel row (`internal/server/server.go:2660`, `:2665`) and cleans `search_documents` / `media_assets` for the catalog videos, but never touches files.
- `videos.channel_id` and `playlists.channel_id` are `ON DELETE SET NULL` (`internal/database/migrations/001_initial.sql`), so deleting only the channel row would leave channel-less videos behind. The explicit `DELETE FROM videos` is load-bearing.
- Video children (`playlist_entries`, `user_progress`, `subtitles`, `comments`, `video_previews`, `sponsorblock_segments`) cascade via foreign keys; `downloads.video_id` is `ON DELETE SET NULL`. `media_assets` and `search_documents` have no foreign keys and must be deleted explicitly.
- `media_assets.byte_size` is never populated (`internal/denorm/denormalized.go:100-111`), so any size reporting must stat files.

## Problem

Deleting a channel with content is impossible, and doing it naively is unsafe in three ways:

1. **Files.** Media, thumbnails, subtitles, preview sprites, and the channel thumbnail live under `KAPSEL_MEDIA_ROOT`. Deleting only rows leaves the archive silently leaking disk.
2. **Resurrection.** Every job that touches a channel calls `upsertChannel` (`internal/download/catalog.go:641-653`, `INSERT ... ON CONFLICT(id) DO UPDATE`) from channel-first (`catalog.go:299`), channel scan (`catalog.go:319`), download ingest (`internal/download/ingest.go:212`) and direct download (`internal/download/downloader.go:326`, `:423`). A scan or download already in flight when the channel is deleted recreates the channel row and its videos after the delete commits.
3. **Irreversibility.** A channel can hold hundreds of gigabytes. The delete must be explicit, previewed, and confirmed, never a single mis-click.

## Design

### 1. Deletion impact (read-only preview)

`GET /api/channels/{id}/deletion-impact` → `200`:

```json
{
  "channel": { "id": "chan-1", "name": "Example Channel" },
  "videos": { "total": 412, "with_media": 410, "keep_forever": 3, "watched": 88 },
  "files": {
    "media": 410, "thumbnails": 412, "subtitles": 38, "previews": 400,
    "channel_thumbnail": 1, "media_bytes": 183746592
  },
  "playlists": [{ "id": "pl-1", "title": "Imported playlist" }],
  "other_playlist_entries": 17,
  "pending_jobs": [{ "id": "job-1", "type": "channel_scan", "status": "running" }],
  "requires_confirmation": true
}
```

- `404` when the channel does not exist.
- Counts come from `videos`, `subtitles`, `video_previews`, and `media_assets` for the channel and its videos. `other_playlist_entries` counts `playlist_entries` rows in playlists whose `channel_id` is *not* the deleted channel and that reference this channel's videos — deleting those videos cascades the entries away with no refusal (F3). `media_bytes` stats `videos.media_path` files only (the large ones); a stat error contributes `0` and never fails the request.
- Read-only and safe to call from the UI dialog.

### 2. Cascade delete

`DELETE /api/channels/{id}?delete_content=true&confirm=<channel name>`

- No query params: unchanged behavior. `204` for a metadata-only channel; `409` when content or playlists exist.
- `delete_content=true` with `confirm` equal to the channel `name` (trimmed, exact match): full cascade, `200` with a removal summary.
- `409` `{"error":"channel has archived content; pass delete_content=true and confirm=<channel name>"}` when content exists and `delete_content` is absent.
- `409` `{"error":"confirmation required: pass confirm=<channel name>"}` when `delete_content=true` but `confirm` is empty.
- `409` `{"error":"confirmation does not match channel name"}` on mismatch.
- `409` `{"error":"channel has playlists: <titles>"}` whenever `playlists.channel_id = ?` — unchanged, and checked before anything is deleted.
- `404` when the channel does not exist (also when the id is already deleted, which keeps retries idempotent).

Order of operations, in one transaction:

1. Load the channel; `404` if absent.
2. Refuse on playlists; refuse without valid confirmation.
3. Collect every file path and its pre-delete `FileInfo` into a **map keyed by normalized relative path** so each file is removed exactly once: `videos.media_path`, `videos.thumbnail_path`, `subtitles.path`, `video_previews.sprite_path`, `media_assets.path` for `owner_type='video'` on the channel's videos, and `media_assets.path` for `owner_type='channel'`. These sources are *not* disjoint — `videos.media_path`/`videos.thumbnail_path` are mirrored 1:1 as `media_assets` rows with `kind='media'`/`'thumbnail'` (`internal/download/ingest.go:230`, `:233`; `internal/download/retention.go:284` deletes both in one tx). Dedup on the first `FileInfo` wins; the removal loop and the `files_removed`/`bytes_reclaimed` counters operate on the unique set. This mirrors the canonical list in `internal/storage/storage.go:203-211` + `appendMediaAssetRefs`.
4. Collect the video ids.
5. Request cancellation (`jobs.Store.Cancel`, `internal/jobs/store.go:778`) for queued/running `channel_scan` and `channel_auto_download` jobs whose payload `channel_id` matches (both carry `ChannelID` + `URL`, `internal/download/enqueue.go:189`, `internal/download/schedule.go:135`). Cancellation here is **best-effort** — report whatever ids were cancelled in `cancelled_job_ids`. `channel_first_download` and direct `download` jobs are *not* cancelled by id: their payload is channel-URL / video-URL (`internal/download/enqueue.go:194`, `:12-16`), and `NormalizeChannelURL` preserves the caller's input form (`internal/download/urls.go:136`), so a handle-form (`@{handle}`) first-download payload cannot reliably be matched to a `UC…` channel. Those are left to the tombstone guard (§4), which blocks regardless.
6. Delete `search_documents` and `media_assets` for each video and for the channel (reuse `denorm.DeleteSearchDocumentsForOwner` / `denorm.DeleteMediaAssetsForOwner`; FTS triggers in `002_search_fts.sql` keep `search_documents_fts` in sync).
7. `DELETE FROM videos WHERE channel_id = ?` (FK cascades handle the children). This also cascades `playlist_entries` out of **other, surviving playlists** that reference these videos — report the count (F3) so the dialog can warn the user.
8. `DELETE FROM channels WHERE id = ?`.
9. Insert the deleted-channel tombstone (see §4) with `deleted_at = now`.
10. Commit.

Then, after the commit and outside the transaction, remove the collected files (see §3). File failures never roll back or fail the request.

`200` summary:

```json
{
  "channel_id": "chan-1",
  "videos_removed": 412,
  "files_removed": 1261,
  "bytes_reclaimed": 183746592,
  "cancelled_job_ids": ["job-1"],
  "file_errors": [{ "path": "videos/abc.mp4", "error": "permission denied" }]
}
```

#### 204 (metadata-only) path — also remove local files

The unchanged `204` path (`no query params`) deletes catalog-video rows, `search_documents`, and `media_assets`, but that leaves the corresponding **local files** on disk: a TA-imported, media-less channel owns a local channel-thumbnail file (`media_assets` `owner_type='channel'`, `kind='thumbnail'`; `internal/taimport/importer.go:578`, `importer_test.go:53`) and possibly local catalog video thumbnails. Leaving this path row-only would silently drop those files into `storage-cleanup`'s orphan set.

Decision (F4): **also collect and remove these files** under the same §3 guards, reusing the deduped path set from above computed for owner-`channel` assets and the catalog videos' `thumbnail_path`. Run the file removal *outside* the delete transaction so an unremovable file still lets the request return `204`. The `204` response body is empty by convention, so there is no `file_errors` payload — log failures instead so the user can point `storage-cleanup` at them. The impact endpoint (§1) is the arbiter and already counts `channel_thumbnail` even when `requires_confirmation` is `false`; if a row-only `204` is preferred, document the residual leak explicitly rather than leaving it implicit.

#### Additional refusal nuance (F3)

The playlist refusal only guards `playlists.channel_id = ?` (channel-bound playlists, §5). It does *not* guard **other, surviving playlists** whose `playlist_entries` reference this channel's videos — `DELETE FROM videos` cascades those entries away with no refusal. That is intentional (a channel can legitimately appear in curated cross-channel playlists), but it must be **reported**: the impact endpoint returns `other_playlist_entries`, and the dialog displays it before the user confirms (§6).

### 3. File removal

- Reuse the guarded helpers: `assetpath.Lstat` / `assetpath.RemoveRegularMatching` (`internal/assetpath/assetpath.go:135`), following the pattern in `internal/download/retention.go:250-295` and the existing `deleteVideoMedia` handler.
- Use the `FileInfo` captured before the transaction so `ErrChanged` detects a file swapped underneath the delete.
- `ErrSymlink` / `ErrInvalid` are recorded as `file_errors` and never followed; nothing outside `KAPSEL_MEDIA_ROOT` is touched.
- Missing files (`os.ErrNotExist`) are skipped and counted as already gone, not as errors.
- Run file removal under `context.WithoutCancel(ctx)` with a bounded timeout so a client disconnect does not abort cleanup mid-way (same pattern as `deleteVideoMedia`).
- DB-first ordering is deliberate: a crash between commit and file removal leaves orphan files that `kapsel storage-cleanup --delete --confirm` already reclaims (`internal/storage/storage.go:156`, README `:187`). Record the surviving paths in `file_errors` so the user can point maintenance at them.

### 4. Job/race safety: no resurrection

Add a tombstone so a job enqueued before the deletion cannot recreate the channel:

```sql
-- internal/database/migrations/018_deleted_channels.sql
CREATE TABLE deleted_channels (
  id          TEXT PRIMARY KEY,
  source      TEXT NOT NULL DEFAULT 'youtube',
  external_id TEXT NOT NULL,
  deleted_at  TEXT NOT NULL
);
CREATE INDEX idx_deleted_channels_deleted_at ON deleted_channels(deleted_at);
```

- `upsertChannel` gains a guard, inside the same transaction as the upsert, comparing the caller job's `CreatedAt` against the tombstone: block when `deleted_at >= job.created_at` (compare with `COLLATE RFC3339_NANO`, `internal/database/collation.go:19`). Return a sentinel `ErrChannelDeleted`; handlers surface it as a clear terminal job failure (`"channel was deleted; job stopped"`) instead of a generic FK error.
- Thread the job timestamp through the channel upsert path. `jobs.Job.CreatedAt` (`internal/jobs/store.go:59`) is available in every handler; `ingest` already receives a job id (`internal/download/ingest.go:180`).
- The high-water mark means a **later** explicit re-add is allowed without clearing anything: `POST /api/channels` enqueues a fresh `channel_first_download` job whose `created_at` is newer than the tombstone.
- A fresh auto-download job cannot be created for a deleted channel because `EnsureChannelAutoDownloadJobs` selects from `channels` (`internal/download/schedule.go:106`).
- On delete, request cancellation (`jobs.Store.Cancel`, `internal/jobs/store.go:778`) for queued/running `channel_scan` and `channel_auto_download` jobs whose payload `channel_id` matches. Report the ids in `cancelled_job_ids`; cancellation is **best-effort** (see F2). `channel_first_download` and direct `download` jobs are *not* cancelled by id — their payload is URL-only and a handle-form first-download URL cannot be reliably matched to the `UC…` channel — so they are left to this tombstone guard, which blocks them regardless.
- Prune tombstones when a tombstone is written (opportunistic) **and at server/CLI startup**, keeping only rows that can still block a live job:

```sql
DELETE FROM deleted_channels
WHERE NOT EXISTS (
  SELECT 1 FROM jobs
  WHERE status IN ('queued','running')
    AND created_at COLLATE RFC3339_NANO <= deleted_channels.deleted_at
);
```

### 5. Playlists

Out of scope to cascade. Keep the existing refusal, but make the message name the blocking playlists and have the UI link to them. Rationale: channel-bound playlists can be curated and may contain cross-channel entries; silently destroying them is a different, riskier decision. Delete the playlists first (playlist delete already keeps videos), then delete the channel.

### 6. UI

`frontend/src/App.svelte:1480` (`deleteChannel`) and the two buttons at `:3663` (channel list) and `:3683` (channel detail page), plus the status blocks at `:3651` / `:3686`.

- Replace the inline `window.confirm` with a dialog that first fetches `deletion-impact`.
- Metadata-only, no playlists: keep the current simple confirm and the plain `DELETE` (`204`).
- Content present: show video count, media count and size, watched count, and the keep-forever count, then require the exact channel name in a text input to enable the delete button. Send `delete_content=true&confirm=<name>`.
- When `other_playlist_entries > 0`: show a line like "removing this channel also removes N entries from your other playlists" ahead of confirmation — those entries are lost and are *not* refused.
- Playlists present: list them with links and disable deletion.
- On success show the summary (`Removed 412 videos and 1,261 files (183 MB).`); if `file_errors` is non-empty, show a warning pointing at Settings → storage cleanup.
- The current copy ("Channels with downloaded media or playlists cannot be removed") is no longer true and must change.
- Add stable `data-testid` hooks for the dialog, input, and summary.

## Requirements

1. Add `GET /api/channels/{id}/deletion-impact` returning the preview shape above.
2. Extend `DELETE /api/channels/{id}` with `delete_content` + `confirm`, preserving the current `204`/`409` behavior when they are absent.
3. Cascade the DB deletes and clean `search_documents`, `media_assets`, and the FTS index.
4. Remove media, thumbnail, subtitle, preview-sprite, and channel-thumbnail files with `assetpath` guards (deduped across `videos.*` and `media_assets`); never follow symlinks or escape the media root.
5. DB commit before file removal; file failures are reported, not fatal; orphans remain reclaimable by `storage-cleanup`.
6. Add the deleted-channel tombstone, guard `upsertChannel`, cancel matching queued/running `channel_scan`/`channel_auto_download` jobs, and prune tombstones when none of the deleted channel's queued/running jobs predate them — pruned at write time and at startup so `deleted_channels` does not accumulate in archives that rarely delete channels.
7. Keep the playlist refusal and name the blocking playlists; report `other_playlist_entries` in the impact preview and warn the dialog.
8. Implement the confirmed dialog, name-match gate, and success/partial-failure reporting in the frontend.
9. Update the README feature line and document that removing a channel deletes its archive and that `storage-cleanup` reclaims any leftovers.

## Acceptance Criteria

- Deleting a metadata-only channel still returns `204` and behaves exactly as before.
- Deleting a channel with media and `delete_content=true&confirm=<name>` returns `200` and removes the channel, all its video rows, their `user_progress`, `playlist_entries`, `subtitles`, `comments`, `video_previews`, `sponsorblock_segments`, `search_documents` (including FTS), `media_assets`, and the media/thumbnail/subtitle/preview/channel-thumbnail files under the media root.
- The same request without `delete_content` returns `409` and changes nothing; a wrong `confirm` returns `409` and changes nothing; a missing channel returns `404`.
- A channel with playlists returns `409` and changes nothing, and the message names the playlists.
- The impact preview reports `other_playlist_entries` (entries lost from other, surviving playlists via the video cascade) and the dialog surfaces it before the user confirms.
- A symlinked or escaping media path is never removed; it is reported in `file_errors` and the DB rows are still deleted.
- A file that cannot be removed (e.g. permission denied) leaves the DB deleted, returns `200`, and reports the path in `file_errors`; a subsequent `storage-cleanup --delete --confirm` reclaims it.
- `keep_forever` does not block the delete, but `deletion-impact` reports the count and the dialog states it.
- Queued/running `channel_scan`/`channel_auto_download` jobs for the deleted channel are cancelled and their ids returned (best-effort). A `channel_first_download` or direct `download` job enqueued before the deletion cannot recreate the channel or its videos after the delete commits; it fails terminally with a "channel was deleted" message via the tombstone. A re-add enqueued after the deletion succeeds.
- `downloads` rows for the channel's videos are kept with `video_id` set to `NULL` (audit history preserved).

## Testing

- `internal/server/server_test.go`: cascade happy path (rows + files), confirmation gating, wrong/missing `confirm`, `404`, playlist refusal, metadata-only `204` unchanged, impact endpoint counts, symlink/unsafe path, file-removal failure reporting, `keep_forever` counting. Extend the existing `TestDeleteChannelRemovesCatalogOnlyVideos` / `TestDeleteChannelRefusesWhenVideosHaveMedia` coverage (`server_test.go:5965`, `:6002`).
- `internal/download`: tombstone guard on `upsertChannel` (pre-deletion job blocked, post-deletion re-add allowed), tombstone prune condition, job cancellation matching.
- `frontend/e2e/smoke.spec.js`: mocked-route spec for the impact dialog, name-match gating, playlist blocker, and the success summary (follow the existing `page.route('**/api/channels/…')` pattern).
- Manual: rebuild and restart the local instance per AGENTS, delete a small test channel with content, verify files and rows are gone and the archive stays consistent.

## Non-goals

- Cascading channel-bound playlist deletion (still refused; delete playlists first).
- Async/job-based channel deletion. The delete is synchronous with a bounded cleanup timeout; revisit if large archives make the request too slow.
- Removing `downloads` audit rows, or any bulk "delete everything" operation.
- Cross-process coordination beyond the app lock every mutating entry point takes at startup: `runServer` and the `import-ta`/`import-subscriptions`/`import-playlists` CLIs all go through `app.New` → `applock.Acquire` (`internal/app/app.go:43`; called from `cmd/kapsel/main.go:239`, `:277`, `:314`, `:344`).

## Notes

- `videos.channel_id` is `ON DELETE SET NULL` (`001_initial.sql`), so the explicit `DELETE FROM videos` remains required; deleting the channel row alone would orphan videos.
- The retention cleaner can run concurrently; both the cascade and the `204` path guard writes with `WHERE media_path = ?` / the deduped remove set and tolerate `os.ErrNotExist`, so there is no double-delete hazard.
- Tombstones are pruned at write time **and at startup** (F6); the high-water-mark guard means pruning never unguards a still-blockable job.
- This issue is the follow-on to **`delete-channel-and-content.md`** ("Allow deleting channels that only have catalog metadata"), which added the `204` catalog-only path. This file is named `cascade-delete-channel-archive.md` to distinguish the two near-identical titles in `meta/issues.md`; keep the cross-references explicit (F7).
- Alternative considered for §4: refuse the delete while any relevant job is active. Rejected because the `download` payload carries only a URL (`internal/download/enqueue.go:12-16`), so an in-flight direct download cannot be attributed to a channel — the tombstone covers it instead.
- `media_bytes` stats only media files to keep the impact endpoint cheap; thumbnail/subtitle/preview sizes are reported as counts.
- Migration number is `018` (latest is `017_video_channel_search_docs.sql`).

## Review references

- `internal/server/server.go:293`, `:2587`, `:2620-2632`, `:2660`, `:2665`
- `internal/server/server_test.go:5335`, `:5965`, `:6002`
- `internal/download/catalog.go:641-653`, `:299`, `:319`; `internal/download/ingest.go:180`, `:212`, `:230`, `:233`; `internal/download/downloader.go:326`, `:423`; `internal/download/enqueue.go:12-16`, `:189`, `:194`; `internal/download/urls.go:136`
- `internal/download/retention.go:250-295`; `internal/assetpath/assetpath.go:135`
- `internal/storage/storage.go:156`, `:203-211`; `internal/denorm/denormalized.go:36`, `:100`, `:119`
- `internal/database/migrations/001_initial.sql`, `002_search_fts.sql`, `017_video_channel_search_docs.sql`; `internal/database/collation.go:19`
- `internal/jobs/store.go:59`, `:778`; `internal/download/schedule.go:106`
- `frontend/src/App.svelte:1480`, `:3651`, `:3663`, `:3683`, `:3686`
- `internal/taimport/importer.go:578` (channel thumbnail `media_assets`), `importer_test.go:53`
- `meta/archive_integrity.md`; `meta/issues/delete-channel-and-content.md`; `meta/issues/add-delete-video-media-action.md`; `meta/issues/lock-destructive-storage-cleanup.md`
