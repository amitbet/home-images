# Photo library and frame controls

Open Home Images at `http://192.168.1.81:8372/`. The UI is embedded in the Go binary;
there is no frontend build, CDN dependency, or additional server.

## Thumbnails and loading

Gallery and album cards use `GET /api/images/IMAGE_ID/thumbnail`: an oriented JPEG
fitting within 480 × 360 at quality 72. People portraits are 160 × 160. Both are
persisted in `data_dir/thumbnails-v1`, reused across restarts, and invalidated when
the source size or modification time changes. Concurrent requests for the same
missing preview share one decode. Viewer and Echo image requests retain their
requested resolution; thumbnail sizing does not reduce full-size delivery.

Small previews are also generated in the background, and ML preview decoding
seeds the gallery cache. Warming uses a single low-priority decoder, yields to
foreground activity, and pauses with indexing. The cache has an approximately
4 GiB budget with periodic eviction of older generated files. Its contents are
disposable; originals and annotations are never removed by cache cleanup.

Cold foreground decodes run in separate, CPU-limited processes. Only visible
and nearby cards request thumbnails. Viewer navigation and closing remain usable
while images or face labels are loading. Photo/person cards are real links:
middle-click and Ctrl/Cmd-click open the corresponding photo/person in a new tab.

## Library

- Browse all photos or a folder, with paginated previews and a keyboard-accessible viewer.
- Favorite photos, hide them from normal browsing/playback, or restore them in **Hidden**.
- Select multiple photos with the corner checkboxes to favorite, hide/restore, or add to an album.
- Create virtual albums, rename/remove them, and add/remove membership without moving originals.
- Filter by dates, manual place labels, tags, favorites, album, or person. **On this day**
  selects matching month/day across years. Dates prefer camera EXIF, then file creation time.
- Use **Files & metadata** to find filenames, paths, EXIF, titles, descriptions, place labels,
  and tags. Text searches treat SQL wildcard characters literally.
- Use **Content search** for ranked visual matches. It searches indexed photos and calls
  the Mac worker for text embeddings; it does not produce verified object labels.
- Add titles, descriptions, tags and place labels in the viewer's **Details** panel.
  Place filtering uses these labels, not automatic GPS reverse geocoding. GPS remains
  available in file details. No coordinates are sent to a geocoding provider.

## People and indexing

The People overview uses summaries warmed at startup and refreshed every 30 seconds.
New background indexing can take one refresh to appear. Renames, merges, face moves,
and curation edits invalidate the overview immediately. **Refresh results** forces a
fresh summary. Name searches and filtered API requests continue to read current data.

People appear as the ML worker processes photos. Open a group to see its photos, then
combine that person with content, folder, date, and annotation filters. Give groups names
and search those names in **People**. Use **Edit name / merge** to combine groups; the
target selector can search names so it is not limited to the first page of people.

**Name / review matches** shows the current portrait and up to 12 similar groups,
with portraits, photo counts, and links to inspect their photos in another tab.
Choose **Review merge**, then confirm **Merge groups** to keep the selected target.
All retained group representatives remain available for future matching. Suggestions
never merge automatically. **Not the same person** persistently dismisses the pair
in both directions. Groups already appearing together in a photo are excluded from
suggestions. Similarity uses retained group representatives, not every individual
face; age and appearance changes may still require a manual search.

The Name field offers the same existing-name autocomplete as **Tag this face**.
Saving an existing name merges the current group into that person; saving a new
name renames the group. Name matching is resolved during the save, even when
autocomplete has not finished. Both dialogs use a lightweight name lookup without
photo-count or capture-year aggregation.

Open a photo and click a face name to access **Move this face**. Select the correct
person below and confirm; only that face moves. The correction survives reindexing of the unchanged photo with
the same model and face box. **Undo latest merge** restores the
most recent merge's original face assignments and group representatives, including
the source name. Photos indexed after the merge stay with their current group.
A subsequent merge replaces the undo; moving a face or dismissing a suggestion clears
it so undo cannot overwrite later corrections.

Gallery thumbnails show the same face outlines when you hover a card (always shown for
named faces on touch screens); click one to tag it without opening the viewer. Renames,
merges and face retags update the open gallery, viewer and People cards in place.
Saving a face tag uses one request and shows progress immediately. When applying
to the whole group, an existing name combines the groups as explained inline;
there is no extra confirmation popup. Name matching happens during the save, so
tagging does not wait for autocomplete. **Only this face** keeps other faces in
their current group. A failed save keeps the editor open for retry.

In the photo viewer, detected faces are outlined on the image with their names (unnamed
faces are dashed). **Faces** toggles the outlines; they are hidden during playback. Click a
face, or its chip under the title, to tag it. **Every photo of this person** renames the
group; if the name already belongs to another group, saving offers to merge into it.
**Only this face** moves just that face, to the group with that name or to a new person,
for a face grouped with the wrong person. Reindexing a changed photo re-detects its
faces, so a per-face correction on that photo is redone by matching.

The status card shows indexed/pending/failed counts, worker availability, pause/resume,
and retry controls. Browsing and annotations work independently of the Mac's availability.
On small screens, the navigation scrolls horizontally; **Saved** opens saved selections
and annotation export links.

## Playback for the Echo or another client

Choose an album or search, then use **Frame & API links** to copy the selection's API
or browser slideshow URL. Saved selections also have a playback-link button. Open the
UI using the server's LAN hostname/IP when copying links for another device.

The API returns the same photo fields as the existing whole-library API, including `id`,
`image_url`, and `original_url`. Image URLs are relative to the server origin. Each
display keeps its own cursor by passing the previous photo's `id` as `after`.

```text
# Existing API: all photos, or an album, in random/sequential path order
GET /api/next?mode=random
GET /api/next?mode=sequential&album=1&after=IMAGE_ID

# New API: albums, people, metadata searches, and content searches
GET /api/playback/next?album=1&order=random&after=IMAGE_ID
GET /api/playback/next?mode=metadata&q=holiday&order=sequential&after=IMAGE_ID
GET /api/playback/next?mode=semantic&q=beach&person=1&order=random&after=IMAGE_ID
GET /api/playback/next?collection=1&order=sequential&after=IMAGE_ID
```

Both orders wrap as needed. Random avoids an immediate repeat if more than one eligible
photo exists; it does not promise a full no-repeat cycle. Sequential metadata/album
playback follows alphabetical paths. Sequential content playback follows similarity rank.
Content playback uses the best 500 candidates by default (`pool_limit`, up to 2000) and a
120-second selection cache. Metadata/album playback covers the entire selection. Hidden
photos are excluded by default, and selection changes are rechecked before returning a photo.
Explicit URL filters override a saved selection's values. See the README playback API
section for the complete endpoint contract.

The browser slideshow uses this same playback API, supports timing and random/sequential
order, hides controls after inactivity, and offers fullscreen. Add `frame=true` to a UI
selection URL to start playback automatically, and `order=random` for random playback.
Fullscreen and screen wake lock depend on the browser; the frame works without either.

## Curation API

Mutation requests use POST with JSON and `X-Home-Images: 1`. Browser writes must be
same-origin. This is a trusted-LAN service, not an authenticated public service. Hidden
is a display preference, not an access-control boundary; a known original URL remains readable.

```text
GET  /api/curation/IMAGE_ID
POST /api/curation/IMAGE_ID
     {"favorite":true,"hidden":false,"title":"A summer afternoon",
      "description":"...","location":"Barcelona, Spain","tags":["family","holiday"]}
POST /api/curation
     {"ids":["IMAGE_ID", "OTHER_ID"]}  # Up to 60; batch-read annotations

GET  /api/albums
POST /api/albums                         {"name":"Family holidays"}
POST /api/albums/1                       {"action":"add","image_id":"IMAGE_ID"}
POST /api/albums/1                       {"action":"remove","image_id":"IMAGE_ID"}
POST /api/albums/1                       {"action":"rename","name":"Summer"}
POST /api/albums/1                       {"action":"delete"}

GET  /api/collections
POST /api/collections                    {"name":"Beach favorites","query":{"favorite":"true","q":"beach","mode":"semantic"}}
POST /api/collections                    {"action":"delete","id":1}
GET  /api/curation/export
```

Search/playback filters: `album`, `favorite=true`, `hidden=only|all` (excluded by default),
`from=YYYY-MM-DD`, `to=YYYY-MM-DD`, `on_this_day=true`, `place`, and `tag`, in addition
to existing folder, person, and query filters. Place is a literal substring; tag is a
case-insensitive exact match. Text limits: title/place 150 characters, description 1000,
up to 20 tags of 50 characters each, album/selection names 100 characters.

## Persistence and backup

Annotations, album membership, and saved rules are stored in `data_dir/curation.sqlite3`,
independently of image metadata and ML embeddings. Scans do not delete them. They are
scoped to the photo root, survive service restarts, and never modify originals. **Export
annotations** downloads a JSON snapshot for inspection/backup. There is no JSON import
endpoint yet. Stop the service before copying the SQLite file for a complete backup.

People names/merges and face embeddings are in `ai.sqlite3`; back that up separately.
Photo IDs currently depend on paths: renaming/moving a photo does not transfer its
annotations automatically. Changing roots hides prior-root annotations rather than
deleting them. Content-based identity and automatic GPS place indexing remain future work.

## Verification

`go test ./...` covers persistence, scan behavior, root isolation, filters, album playback,
saved selections, literal wildcard handling, validation, and same-origin writes alongside
the ML/protocol/playback tests. `scripts/ui-smoke.js` runs through actual browser actions
against the isolated sample server on `127.0.0.1:18374` and refuses to run against the live
library. Its setup uses 18 sample previews and empty curation state; it is not a production
health check. Desktop, phone, and Echo-sized layouts are inspected with the collaborative
browser. Actual Echo device behavior still requires an on-device check.

### Viewer preview performance

The viewer shows a gallery thumbnail immediately while the 1920?1440 JPEG loads.
Standard viewer previews persist alongside thumbnails in the shared 4 GiB disk cache;
source size/modification changes invalidate them. Cache eviction runs periodically
on writes, so the limit can be exceeded briefly during generation. Arbitrary large
API variants remain uncached. After the current preview loads, the UI preloads one
next photo at low priority; playback reuses the prefetched selection, including random
playback. Closing or navigating the viewer cancels obsolete image requests.

Grid thumbnails use a per-tab 32 MiB/512-entry memory cache with five-minute
freshness, backed by the browser HTTP cache. At most two thumbnail fetches run
at once; page navigation aborts obsolete fetches and discards queued work.
People thumbnail ETags allow unchanged portraits to revalidate without decoding.

### Merge several face groups

Open a person's review dialog, check matching groups in suggestions or name search,
then choose **Review selected groups**. Selections persist across searches. The review
dialog shows every selected portrait and the person being kept before **Confirm merge**.
The batch is atomic; undo restores its sources one at a time in reverse order.
Merge, undo, and face-move confirmations use application dialogs.


Photo results offer a Grid/List toggle and ascending/descending sorting by filename,
person name, distinct people count, location label, time taken, file creation date,
and file size. Sorting applies to the full filtered selection before pagination;
missing labels/dates appear last. Person sorting uses the first named person
alphabetically. Location sorting uses the editable location label. Content queries
retain visual similarity ranking and disable metadata sort controls. Layout and
sort choices are retained in the page URL.

`GET /api/search` accepts `sort=filename|person|people_count|location|taken_at|created_at|size`
and `direction=asc|desc` for metadata results (including an empty content query).
