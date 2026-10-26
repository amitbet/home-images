# Home Images

A small **Go image and metadata server** for home dashboards, digital photo frames,
and older browser displays such as an Echo Show. Runs as a native Windows service,
Linux systemd daemon, or macOS launchd daemon. One binary; no Python, WinSW, or CGO.

Features:

- Read-only photo access under one configurable directory, with nested folder browsing.
- Random selection without immediate repeats, or alphabetical sequential playback with wraparound.
- Stable path-based image IDs and independent client cursors.
- Filesystem dates, original camera date/offset, EXIF properties, and GPS metadata.
- Upright, resized JPEG previews, transparency composited over white, ETags, and original-file range requests.
- Persistent SQLite index, background incremental scans, and literal text search across paths/metadata.
- Embedded photo browser with content search and editable named face groups.
- Optional remote Immich ML inference; no Docker or ML runtime required on this server.
- Native JPEG, PNG, GIF, BMP, TIFF, and WebP decoding. Optional **FFmpeg 8.1+** for HEIC/HEIF previews.
- Bounded rendering concurrency/pixel counts, root-confined file handles, graceful shutdown, and rotating logs.

## Build and run

Requires Go 1.26.3 or newer to build. No Go installation is needed on the target host.

```sh
go build -trimpath -ldflags "-s -w" -o home-images .
cp config.example.json config.json
# Edit root and data_dir to absolute paths for your host.
./home-images --config /absolute/path/config.json
```

Open `http://127.0.0.1:8372/` for the photo library, or `/docs` for the API reference.
See [the web UI guide](UI.md) for browsing, curation, named people and frame/API links.

Configuration:

```json
{
  "root": "/srv/photos",
  "data_dir": "/var/lib/home-images",
  "host": "127.0.0.1",
  "port": 8372,
  "scan_interval_seconds": 900,
  "cors_origins": ["http://dashboard.local"],
  "ffmpeg": "ffmpeg",
  "max_pixels": 80000000
}
```

On Windows, use JSON paths such as `"D:\\Photos"` and `"C:\\ProgramData\\HomeImages"`.
`ffmpeg` can be an absolute executable path. Leave it empty to disable external HEIC
decoding. HEIC metadata and primary image dimensions are extracted in Go; unusual
containers that cannot be parsed may need `ffprobe` alongside FFmpeg as a fallback.

The server scans at startup and every `scan_interval_seconds` (minimum 30). It serves
results while indexing, skips unreadable/corrupt/oversized images, and logs errors.
Failed directory traversal prevents stale-entry pruning for that scan. A missing EXIF
block does not exclude otherwise valid images. GIF/TIFF previews use the first frame.
Restart to rescan immediately or apply configuration changes.

The SQLite index is `data_dir/index-go.sqlite3`. Changing the root resets its entries.
It is disposable: stop the daemon before removing it to force a complete rebuild.
Moving a photo changes its ID. Logs rotate at 10 MiB with one retained previous file.

## Windows service

Build, then run the installer in an **elevated PowerShell**:

```powershell
go build -trimpath -ldflags '-s -w' -o dist/home-images-windows-amd64.exe .
.\install-windows.ps1 -Root 'D:\Photos'
Get-Service HomeImages
Invoke-RestMethod http://localhost:8372/health
```

The installer uses `C:\Program Files\HomeImages\home-images.exe`, configuration/data
in `C:\ProgramData\HomeImages`, LocalService, delayed automatic startup, and automatic
restart after failure. It grants read access to the media/program directories and write
access to the data directory. It opens the selected port to **LocalSubnet only**.
Non-inheriting child-folder ACLs may require separate read grants.

An existing `HomeImages` Python/WinSW service is replaced in place, keeping its name,
configuration and HTTP port. The entire previous program directory, including Python,
is archived under `data_dir/backups/before-go-TIMESTAMP/program`. The Python SQLite
index remains untouched; the installer imports its metadata read-only into Go's
separate index so photos are available immediately, then refreshes metadata in the
background. The installer checks health and
restores the old executable/service on failure. Existing configuration wins over
installer defaults. Review/update its CORS origins and optional FFmpeg path as needed.

Native lifecycle commands (elevated PowerShell):

```powershell
$exe = 'C:\Program Files\HomeImages\home-images.exe'
$config = 'C:\ProgramData\HomeImages\config.json'
& $exe --config $config --service restart
& $exe --config $config --service stop
& $exe --config $config --service uninstall
Remove-NetFirewallRule -Name HomeImages-LAN
```

Uninstall leaves photos, program, configuration, index, and logs in place.

To roll back a successful Python migration, stop the Go service, move the current
program directory aside, restore the archived `program` directory at the original
installation path, then restore the service command/account recorded in the adjacent
`service.json` with `sc.exe config` and start it. Do not run both implementations on
the same port. Keep the original photo folder and data directory in place.

## Linux systemd daemon

Install the binary and create a service account. These commands assume the media root
is `/srv/photos` and that the account already has permission to read that directory:

```sh
sudo install -m 755 home-images /usr/local/bin/home-images
sudo useradd --system --home /var/lib/home-images --shell /usr/sbin/nologin homeimages
sudo install -d -o homeimages -g homeimages /var/lib/home-images
sudo install -d /etc/home-images
sudo install -m 644 config.example.json /etc/home-images/config.json
# Edit /etc/home-images/config.json for your root, data dir, and listener.
sudo home-images --config /etc/home-images/config.json --user homeimages --service install
sudo home-images --config /etc/home-images/config.json --service start
sudo systemctl status HomeImages
```

The generated unit enables startup at boot and restarts failed processes. Commands:
`--service stop`, `restart`, `status`, and `uninstall`. A user-level systemd service is
also supported: supply `--user-service` to each lifecycle command; it requires a user
systemd session and, for startup without login, administrator-configured lingering.
No firewall rules are created by the binary on Linux/macOS.

## macOS launchd daemon

Use a permanent binary/config location, with media/data permissions for the selected
account. For example, create `~/Library/Application Support/HomeImages/config.json`
with absolute paths and a data directory writable by your user:

```sh
sudo install -m 755 home-images /usr/local/bin/home-images
config="$HOME/Library/Application Support/HomeImages/config.json"
sudo /usr/local/bin/home-images --config "$config" --user "$USER" --service install
sudo /usr/local/bin/home-images --config "$config" --service start
sudo launchctl list HomeImages
```

This installs a system LaunchDaemon with RunAtLoad/KeepAlive. Use the same config with
`--service stop`, `restart`, or `uninstall`. Alternatively use `--user-service` without
sudo for a login-scoped LaunchAgent, including on all subsequent lifecycle commands.
macOS privacy permissions can block access to Photos/Documents/removable volumes;
grant the required permission or use a media directory accessible to the daemon.

## API

All query paths use **forward slashes relative to the configured root** and must be URL
encoded. Absolute paths, `..`, drive letters/ADS syntax and out-of-root symlinks or
junctions are rejected. Indexing does not follow symlinks/junctions. GET/HEAD only;
there are no upload, edit, or delete endpoints.

- `GET /health` — implementation/version, image count, root availability, scan status/errors.
- `GET /api/directories?directory=Trips&offset=0&limit=100` — immediate subdirectories.
- `GET /api/images?directory=Trips&recursive=true&q=Paris&offset=0&limit=50` — paginated metadata.
- `GET /api/next?mode=random&directory=Trips&after=PREVIOUS_ID` — random photo; avoids the previous ID if possible.
- `GET /api/next?mode=sequential&directory=Trips&after=PREVIOUS_ID` — next alphabetical path, wrapping at the end.
  Both modes accept `recursive` and `q`; omit `after` to start a sequential slideshow.
- `GET /api/images/ID` — one photo's metadata.
- `GET /api/images/ID/image?width=1280&height=800&quality=85` — orientation-correct JPEG fitting inside the box without upscaling/cropping.
  Dimensions range 32–2560; quality 40–95. HEIC JPEG quality uses FFmpeg's nearest equivalent.
- `GET /api/images/ID/original` — unchanged original with HTTP range support.

Selection and listing return `id`, `path`, `absolute_path`, `filename`, `directory`,
`size_bytes`, oriented `width`/`height`, `format`, `created_at`, `modified_at`,
`taken_at`, `taken_at_offset`, `exif`, `image_url`, and `original_url`.
URLs are relative to the API's origin. Creation time is filesystem birth time or null
when unavailable; it can reflect when a photo was copied. `taken_at` is the camera
timestamp in `YYYY:MM:DD HH:mm:ss` format; timezone offset is separate and nullable.
EXIF properties may differ by decoder/format; `exif.Photo` holds camera fields and
`exif.GPS` holds normalized coordinates when present. This is not exhaustive ExifTool
parity. Preview JPEGs omit metadata; original bytes retain it.

Minimal dashboard integration:

```javascript
var api = 'http://your-server:8372';
var previous = null;
function nextPhoto() {
  var query = '?mode=sequential&directory=' + encodeURIComponent('Trips') +
    (previous ? '&after=' + previous : '');
  return fetch(api + '/api/next' + query).then(function (response) {
    if (!response.ok) throw new Error('Photo API: ' + response.status);
    return response.json();
  }).then(function (photo) {
    var preload = new Image();
    preload.onload = function () {
      document.getElementById('photo').src = preload.src;
      document.getElementById('caption').textContent = photo.taken_at || photo.filename;
      previous = photo.id;
    };
    preload.src = api + photo.image_url + '?width=1280&height=800';
  });
}
nextPhoto().catch(console.error);
setInterval(function () { nextPhoto().catch(console.error); }, 30000);
```

## Network access

There is **no authentication**: reachable clients can retrieve photos, paths and GPS.
The example binds to loopback. Set `host` to a LAN IP or `0.0.0.0` when needed and
restrict the port using your firewall. Do not expose directly to the internet.
`cors_origins` controls browser access, not authentication. `"*"` allows any origin.
For an HTTPS dashboard, put the API behind an HTTPS or same-origin reverse proxy to
avoid mixed-content blocking. An HTTP LAN dashboard can call the HTTP API directly.

## Faces and content indexing

The native Go service can call an existing **Immich machine-learning container**
directly over HTTP. The full Immich application, PostgreSQL, Docker and WSL are not
needed on the image server. Add this section to `config.json`, then restart:

```json
"ml": {
  "enabled": true,
  "url": "http://192.168.1.20:3003",
  "clip_model": "ViT-B-32__openai",
  "face_model": "buffalo_l",
  "interval_ms": 2000,
  "face_similarity": 0.6
}
```

The Mac receives upright previews, returning CLIP content embeddings, face boxes
and face embeddings. Pin its container version: `/predict` is an internal Immich
protocol, not a versioned public API. The container downloads models on first use.
Keep its unauthenticated port restricted to your trusted network.

Indexing uses a separate `ai.sqlite3` WAL database and one throttled background
worker. Preview decoding runs in a separate single-core process with a soft 256 MiB
Go memory target and below-normal CPU priority on Windows. The worker waits while
foreground rendering is active, never takes a foreground rendering slot, and holds
no catalog connection while contacting the Mac. CPU, RAM and disk are still shared;
this is bounded background work, not a guarantee of zero resource contention.

Progress survives restarts. New or changed photos are queued automatically; changing
models makes old results ineligible until recomputed. A Mac outage backs off for 30
seconds. Undecodable files and invalid results are retried after an hour, or via the
retry control. Pause/resume is persistent; an in-flight image finishes before pause.
Do not delete `ai.sqlite3`: it also contains user-assigned people names and merges.
Back up the entire data directory with the service stopped, including curation data.

People are grouped conservatively using representative face-vector similarity.
Name groups and merge duplicates in the People page. A higher `face_similarity`
reduces automatic merging; changes apply to future indexing and do not recluster
existing photos. Recognition can make mistakes, particularly with children, twins,
blur, or unusual angles. Renaming changes a label; it does not retrain the model.

Content search ranks photos by text/image similarity, e.g. "dog", "beach", or
"birthday party". It **does not return verified object labels or object boxes**.
Only indexed photos participate; even weak matches may appear in ranked results.
An uncached query needs the Mac to encode the text. Previously cached queries,
people filters, and ordinary browsing remain usable without the Mac.

- `GET /api/search?mode=semantic&q=beach&person=12&limit=60&offset=0` returns ranked
  photos, with optional person/directory filters. `mode=metadata` searches literal
  path and EXIF text; an empty query browses the library. Offset is bounded at 1000.
- `GET /api/people?q=Dana&limit=48&offset=0` lists groups, names and photo counts.
- `POST /api/people/12` accepts `{"name":"Dana"}` or `{"merge_into":34}`.
  Merges retain both representatives for future recognition and cannot be undone
  through the UI. Separate people with the same name are still separate groups.
- `GET /api/people/12/image` serves a face thumbnail.
- `GET /api/images/IMAGE_ID/faces` returns groups and face boxes for that photo.
- `POST /api/faces` with `{"ids":["IMAGE_ID", ...]}` (up to 60) returns face boxes keyed by
  image ID, for tagging thumbnails. Accepts `hidden=only|all` like other reads.
- `POST /api/faces/FACE_ID` retags one face: `{"name":"Dana"}` moves it to the existing
  person with that name (case-insensitive) or creates one; `{"person_id":34}` moves it to
  that group. Returns 409 if the person is already tagged in the photo. Faces indexed from
  this version on store their embedding, so a corrected face also steers future matches.
- `GET /api/indexing` reports indexed/pending/failed counts and worker status.
- `POST /api/indexing/control` accepts `{"action":"pause"}`, `resume`, or `retry`.

### Playback of albums and search selections

`GET /api/playback/next` serves the same top-level photo fields as `/api/next`,
including `id`, `image_url`, `original_url`, dates and EXIF. It adds a `playback`
object; existing photo clients can ignore the additional fields. Use the returned
`id` as `after` for the next request. Each client maintains its own cursor.

```text
/api/playback/next?album=3&order=random
/api/playback/next?album=3&order=sequential&after=PREVIOUS_IMAGE_ID
/api/playback/next?mode=semantic&q=beach&person=12&order=sequential
/api/playback/next?mode=metadata&q=Paris&favorite=true&order=random
/api/playback/next?collection=7&order=sequential
```

- `order=random|sequential` defaults to `random`. Sequential wraps at the end;
  random avoids the previous ID whenever another current match is available.
- `mode=semantic|metadata` defaults to `semantic`. An empty `q` browses all photos
  matching the filters, regardless of indexing progress. Metadata mode searches
  paths/EXIF and annotation title, description, location and tags with literal
  substring matching (`%` and `_` are not wildcards).
- Filters: `person`, `album`, `favorite=true`, `hidden=only|all`, `directory`,
  `recursive`, `from=YYYY-MM-DD`, `to=YYYY-MM-DD`, `place`, `tag`, and
  `on_this_day=true`. Hidden photos are excluded by default. Person filters cover
  indexed faces; ordinary album/favorite/folder playback requires no ML worker.
- `collection=ID` loads a saved selection belonging to this photo root. Explicit
  URL query keys override saved rules, including empty values to clear a filter.
  The collection name and ID are never interpreted as SQL or file paths.
- Metadata/empty-query playback covers the entire matching library in alphabetical
  path order or uniform random order, with no candidate cap.
- Semantic playback uses the top `pool_limit` matches (default **500**, range
  **1–2000**). Sequential follows descending similarity; random samples from that
  pool. These are ranked suggestions, with no claim that every match contains the
  queried object. Only currently indexed photos participate.
- Up to eight playback selections (metadata and semantic) are cached and refreshed
  in the background after **120 seconds**, keyed by root, models, normalized filters
  and API curation revision. Completed catalog scans also trigger refresh. Random
  and sequential playback share a snapshot. Metadata snapshots keep every matching ID;
  semantic snapshots retain the bounded ranked pool. Newly indexed matches join when it is
  refreshed. Frames continue from the previous snapshot during refresh. A first-time
  metadata query may scan the catalog; large libraries can take around 15 seconds.
  Subsequent frames reuse IDs instead of repeating that scan. Every cached candidate is rechecked for current index signature,
  filters, visibility and file availability. Curation changes invalidate the cache
  immediately. `refresh=1` forces a rebuild for that request; do not send it on
  every frame if you want caching.
- `playback` always contains `mode` and `order`. Semantic responses also contain
  top-level `similarity` and `playback.{pool_size,pool_limit,indexed_candidates,
  cached_at,cache_seconds}`. IDs remain the cursor, not a mutable server session.
- An empty selection returns HTTP **404**. A busy semantic search returns **429**;
  retry after a short delay. An uncached text query with an unavailable Mac returns
  **503**. Cached text embeddings allow local reranking without the Mac.

The existing `/api/next?mode=random|sequential` remains compatible and now also
accepts curation filters such as `album` and `favorite`. Use `/api/playback/next`
for person filters, semantic search, or saved selections.

Write requests require `Content-Type: application/json` and `X-Home-Images: 1`.
Browser writes must be same-origin. The service still has no user authentication:
all trusted-LAN clients able to reach it can view photos and edit labels.
Existing `/api/images` and `/api/next` keep their literal metadata `q` behavior,
now including user annotations.
Automatic GPS-to-city reverse geocoding is not implemented.

## Verification and portability

```sh
go test ./...
go vet ./...
go test -race ./...  # requires a supported C compiler for Go's race detector
HOMEIMAGES_TEST_HEIC=1 go test ./...  # optional, FFmpeg 8.1+ on PATH
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o home-images-linux-arm64 .
```

CI tests Windows/Linux/macOS, including native service installation, image delivery,
stop/start, and uninstall. It also builds CGO-free amd64/arm64 binaries for all three
systems. Tests cover all eight orientations, dates/GPS, original bytes/ranges, ETags,
Unicode/wildcard search, folder scope, traversal/junction escape, rescan behavior,
formats/alpha, pixel limits, CORS, and shutdown. HEIC preview tests are opt-in because
FFmpeg versions differ across runner images.

The included test images are synthetic. No personal photos, local index, configuration,
logs, or credentials belong in the repository. External libraries retain their own
licenses; FFmpeg is optional and distributed separately.
