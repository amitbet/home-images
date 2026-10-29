# Gallery image loading, 2026-10-29

Five random library photos were requested at both 960px and 1920px bounding boxes, quality 80, over localhost on the running Windows service. A second pass reused the same photos with alternating size order to reduce first-read bias.

| Bounding box | Median response | Mean payload |
| --- | ---: | ---: |
| 960 x 960 | 208 ms | 93,128 bytes |
| 1920 x 1920 | 729 ms | 299,522 bytes |

This small warm-file sample reduced median response time by 71% and payload by 69%. Cold-file timings varied substantially and are not represented by those figures. The initial pass measured averages of 3150 ms and 2187 ms respectively; its fixed small-first ordering confounded size with filesystem warming. Raw results are in ignored dist/gallery-image-benchmark*.json.

The viewer fills its display area using the existing thumbnail immediately, requests a preview sized to its CSS display dimensions, then requests more pixels (including device pixel ratio, capped at 2560px) after zoom/fullscreen. Zoom requests are debounced by 160 ms. This does not stream or change original files.

Directory groups use /api/search?group_by=directory and return paginated exact-directory summaries with counts and cover IDs. group_kind and group_key scope search and playback to a selected directory/path/year/person/tag. Default photo sorting is taken_at ascending, with undated photos last.

## Group folder/grid performance, 2026-10-29

On the live 175,552-photo library, a year-group list took about 6.5 seconds and
year 2003's first 60-photo grid data took 3.2 seconds before optimization.
After correcting the year predicate's index collation, adding covering scope/time
indexes and avoiding materialization of the whole filtered catalog, a local HTTP
sample measured 776 ms for the year cards and 133 ms for the first grid page.
These are individual observations, not latency percentiles; original-image reads
and uncached thumbnail generation are separate and still vary with disk/decoding.

Browser checks verified directory-like group cards, exact-scope drilldown,
60-to-120 automatic scroll append with the first DOM card preserved, and loading
all 324 photos in the tested group without duplicates. Changing groups during an
append cancels the old result. The client caches recent pages for 30 seconds and
fetches thumbnails lazily with four concurrent requests (server render limit two).
