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
- Optional Android backups through a managed Syncthing receiver and Syncthing-Fork QR pairing.
- Optional remote Immich ML inference; no Docker or ML runtime required on this server.
- Native JPEG, PNG, GIF, BMP, TIFF, and WebP decoding. Optional **FFmpeg 8.1+** for HEIC/HEIF previews.
- Bounded rendering concurrency/pixel counts, root-confined file handles, graceful shutdown, and rotating logs.

## Android phone backup

Open **Phone backup** in the library sidebar (`/settings` or `/phone-setup`)
and choose **Enable phone backup**. Home Images downloads a pinned Syncthing
v2.1.0 release from upstream, verifies its SHA-256 against a checksum included
in our code, and runs it as a supervised child process. No separate OS service
or manual Syncthing configuration is needed. First setup requires Internet
access to GitHub. Linux, macOS and Windows on amd64/arm64 are supported.

1. Install the free [Syncthing-Fork Android app](https://f-droid.org/packages/com.github.catfriend1.syncthingfork/)
   ([upstream APK releases](https://github.com/researchxxl/syncthing-android/releases)).
2. In its **Devices** tab, add a device and scan the receiver QR shown by Home
   Images. This is a Syncthing device-ID QR, scanned inside the app. On the same
   phone, expand **Connecting from this phone?** to copy the receiver ID instead.
3. Match the requesting device ID to the phone's **Show device ID**, name the
   phone and approve it in Home Images.
4. Share the phone's `DCIM/Camera` folder with Home Images, choosing **Send Only**
   on the phone. Approve the folder offer with **Back up this folder** in Home
   Images. The receiver configures a private **Receive Only** staging folder.
5. Allow the app to run in the background; configure Wi-Fi-only run conditions
   in the phone app if desired. Both devices must be online for transfers.

Existing Syncthing installations are left alone: we never reuse their home
directory, device identity, database, service, startup configuration or API key.
Our executable and its upstream notices live in
`data_dir/syncthing/bin/v2.1.0/`, private instance state in
`data_dir/syncthing/instance/`, pairing/settings in
`data_dir/syncthing/home-images.json`, and staging files in
`data_dir/syncthing/incoming/`. Our TCP transfer port defaults to 22001 and falls
back to an available port if occupied; the private management API binds only to
a free loopback port. Normal Syncthing discovery and encrypted relays are
available. Our instance's automatic updater is disabled; Home Images controls
the pinned version. The service starts the receiver automatically after restart
if phone backup was enabled, retries failed starts/crashes, and shuts it down
before stopping.

Completed media files are copied from idle, fully synchronized staging folders
into `root/Phone backups/<phone>/<folder>/`. Content hashes in filenames preserve
changed versions without overwriting earlier copies. Files are published only
after a complete copy; an SQLite manifest makes repeated imports idempotent.
Phone deletions affect staging only; they never delete archive files. Check the
phone's last backup and wait for sync/import completion before deleting originals.
This retains **both staging and archive copies**, so allow disk space for both.
Removing a phone or disabling backup preserves its archive, staging files and
identity; it does not uninstall or stop any other Syncthing installation.
Videos and camera sidecars are archived even where the gallery cannot display them.

The service needs write access to `root/Phone backups` only. Updated installers
create that directory and grant its service account access, while retaining
read-only access to existing originals. For an existing installation, rerun the
installer or create/grant that subfolder manually. Keep `data_dir/syncthing`
separate from the photo library. Windows installation adds a local-subnet TCP
firewall exception scoped to our pinned executable; on other firewalled hosts,
allow that executable or its displayed transfer port on the home network.

Phone administration follows the existing trusted-LAN settings model: it requires
same-origin requests and the `X-Home-Images: 1` header, and does not expose our
Syncthing API key. Do not expose the Home Images administration UI directly to
the Internet. The QR is a public device identity, not an upload credential;
both device and folder offers require explicit approval.

To exercise real pairing, transfer, deletion retention, crash recovery and
restart behavior locally, run
`HOMEIMAGES_TEST_SYNCTHING_DOWNLOAD=1 go test -run TestSyncthingRealTransferLifecycle -count=1 -timeout=5m`.
Alternatively set `HOMEIMAGES_TEST_SYNCTHING_EXE` to an already downloaded
Syncthing v2.1.0 executable. CI runs this check on every supported build platform.

## Build and run

### Download and install a release

The `v*` tag workflow publishes six complete bundles: Windows, Linux and macOS,
each for amd64 and arm64. Every bundle includes the application, the matching
libjpeg-turbo 3.2.0 native library, its license notices, and a local installer.
These download commands become available once that workflow publishes a release:

Linux (systemd) or macOS (launchd):

```sh
curl -fL https://github.com/amitbet/home-images/releases/latest/download/install.sh -o install.sh
sudo bash install.sh --root /srv/photos
# On macOS, use your own path, e.g. --root '/Volumes/Photos'
# Pin a release: sudo bash install.sh --version v1.5.0 --root /srv/photos
```

The Unix installer needs Bash, curl, tar and Python 3 (installer only). If needed,
install these with `apt install curl python3` or `brew install python`. Linux
release bundles target glibc-based systems; native codec loading on musl/Alpine is
not covered by this CI matrix. The default listener is `127.0.0.1:8372`; supply
`--host 0.0.0.0` for LAN access. Configure the firewall separately.

Windows, from an Administrator PowerShell:

```powershell
Invoke-WebRequest https://github.com/amitbet/home-images/releases/latest/download/install.ps1 -OutFile install.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Root 'D:\Photos'
# Add -Version v1.5.0 to select a particular release.
```

Both downloaders detect the OS/CPU and verify the release archive against its
`SHA256SUMS` before invoking the bundled installer. Existing configuration,
indexes, annotations and photos are preserved. Program/codec upgrades keep a
backup and restore it on a failed health check. Unix installations default to
`/usr/local/lib/home-images`, with Linux config/data in `/etc/home-images` and
`/var/lib/home-images`, or macOS config/data under
`/Library/Application Support/HomeImages`. Unix flags include `--prefix`,
`--config`, `--data-dir`, `--user`, `--host` and `--port`; Windows offers
`-Destination`, `-DataDirectory` and `-Port`. Photo access must be granted to the
Unix service account; the Unix installer does not change photo-tree permissions.
Unmanaged Unix services must be migrated/uninstalled before using this installer.
Existing install/config/data locations are retained when upgrading managed bundles.

For offline installation, extract the matching archive and run
`sudo bash install-unix.sh --root /srv/photos`, or run the bundled
`install-windows.ps1 -Root 'D:\Photos'` from Administrator PowerShell.

### Build from source

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

On Windows, the server also watches the photo root recursively and updates changed files and folders. It scans at startup and every `scan_interval_seconds` (minimum 30) to reconcile missed changes; for a large library, a weekly interval (604800 seconds) avoids repeated disk activity. Other platforms currently rely on periodic scans. Watcher errors or notification overflow trigger an additional reconciliation. It serves
results while indexing, skips unreadable/corrupt/oversized images, and logs errors.
Failed directory traversal prevents stale-entry pruning for that scan. A missing EXIF
block does not exclude otherwise valid images. GIF/TIFF previews use the first frame.
Restart to rescan immediately or apply configuration changes.

The SQLite index is `data_dir/index-go.sqlite3`. Changing the root resets its entries.

Exact-directory searches use a covering `(directory,id,signature)` index. Person
filters start from the person's face index and validate model/signature matches
through covering indexes. Bounded SQLite planner maintenance runs after schema
setup, after the initial scan, and daily; it does not require a manual `ANALYZE`.
It is disposable: stop the daemon before removing it to force a complete rebuild.
Moving a photo changes its existing path-based ID. The additive content-identity
index below links matching originals across paths without replacing those IDs.
Logs rotate at 10 MiB with one retained previous file.

### Content identity and duplicate candidates

Existing image IDs, URLs, albums, annotations, face associations, AI results and
thumbnail caches remain intact. Startup adds nullable `content_id` and `file_hash`
columns and indexes without rewriting catalog metadata or AI signatures. A single
background worker hashes up to 16 files per batch, pauses for foreground rendering,
yields within large files and persists each result. Restarting resumes pending
work. Temporary read failures retry after an hour; a changed scan signature clears
stale hashes automatically. Backfilling does not require a full rescan or repeat AI
inference. It reads existing originals, so disk activity will increase temporarily.

- `file_hash`: `sha256:<full 64-character hex digest>` of every original byte.
- `content_id`: `imgdata-sha256-v1:<format>:<full hex digest>` for JPEG, PNG and
  WebP. Format-aware parsing excludes EXIF, XMP, IPTC, comments and color-profile
  metadata while retaining encoded image data, transparency and animation data.
  Recompression can produce a different content ID. Preview and AI invalidation
  continue to use the existing source signature, including for orientation and
  color-profile edits.
- Other formats, including HEIC/HEIF, TIFF, GIF and BMP, initially use
  `file-sha256-v1:<full hex digest>` for exact-file matching. **Metadata-independent
  matching is not yet supported for these formats.** Malformed JPEG/PNG/WebP files
  retain their file checksum with a null content ID and a recorded parsing error.

Phone imports compute both hashes while streaming the staging file into the
archive. The catalog reuses these saved hashes when its source signature matches,
avoiding another read. Every metadata version is retained, and deleting a staging
copy does not remove its archived original.

`GET /api/images/ID/identity` returns `image_id`, nullable `content_id` and
`file_hash`, `pending`, nullable `error`, and up to 100 `potential_duplicates`
(`id`, `path`). Matching content IDs link candidates only: no automatic merging,
file deletion, ID changes or cross-image reuse of face results occurs. Hidden
photos are excluded unless `hidden=all` or `hidden=only` is requested. This first
migration deliberately keeps each existing catalog record independent.

## Windows service

Build, then run the installer in an **elevated PowerShell**:

```powershell
go build -trimpath -ldflags '-s -w' -o dist/home-images-windows-amd64.exe .
# Optional Windows x64 JPEG acceleration (requires 7-Zip):
./scripts/package-turbojpeg.ps1
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
  "concurrency": 2,
  "face_similarity": 0.6
}
```

The Mac receives upright previews, returning CLIP content embeddings, face boxes
and face embeddings. Pin its container version: `/predict` is an internal Immich
protocol, not a versioned public API. The container downloads models on first use.
Keep its unauthenticated port restricted to your trusted network.

Indexing uses a separate `ai.sqlite3` WAL database, a queue of at most two prepared
images, and concurrent remote inference requests (`ml.concurrency`, default 2,
maximum 4). Preview preparation overlaps with inference; saving and face clustering
remain serialized. `interval_ms` is accepted for older configurations but adds no
delay. Preview decoding runs in a separate single-core process with a soft 256 MiB
Go memory target and below-normal CPU priority on Windows. The worker waits while
foreground rendering is active, never takes a foreground rendering slot, and holds
no catalog connection while contacting the Mac. CPU, RAM and disk are still shared.

Progress survives restarts. New or changed photos are queued automatically; changing
models makes old results ineligible until recomputed. A Mac outage backs off for 30
seconds. Undecodable files and invalid results are retried after an hour, or via the
retry control. Pause/resume is persistent; requests already in flight finish before pause.
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

### Optional native JPEG acceleration

Native JPEG acceleration supports Windows/Linux/macOS on both amd64 and arm64.
`scripts/package-turbojpeg.ps1 -Architecture amd64` (or `arm64`) packages the
Windows codec; `bash scripts/package-turbojpeg.sh amd64 dist/package` packages
the Linux/macOS codec. Downloads use pinned official libjpeg-turbo 3.2.0 SHA-256
hashes. The codec is loaded only by absolute path beside the real executable
(symlinked executable paths are resolved), never from PATH or the working folder.
Windows uses `turbojpeg.dll`, Linux `libturbojpeg.so`, and macOS
`libturbojpeg.dylib`. Install the matching library and `turbojpeg-licenses/`
alongside the executable. The CI release bundles and installers do this together.
Unix bindings use purego; application builds still use `CGO_ENABLED=0` without
requiring a C compiler. They are not claimed to be fully static Linux binaries.

With `"jpeg_decoder": "auto"` (the default), an available library enables SIMD JPEG
decode/encode and reduced-resolution decoding inside isolated preview workers.
EXIF orientation, final Lanczos resize, face crops and existing caches are preserved.
Native input/dimension/working-set bounds are checked; unsupported colorspaces,
codec errors or missing/unloadable libraries fall back to Go on supported systems. A failed native JPEG worker is retried
once with Go within the original request deadline. Set `"jpeg_decoder": "go"` and
restart to disable acceleration. `/health` reports `jpeg_decoder` as `turbojpeg` or
`go`; individual files can still fall back. The HTTP process never decodes through
the native library.

For native tests, set `HOMEIMAGES_TEST_TURBO_LIBRARY` to the absolute library path and
`HOMEIMAGES_TEST_PREVIEW_EXE` to a freshly built application executable, then run
`go test ./...`. Tests cover all eight orientations, progressive JPEG, dimensions,
pixel limits, cancellation, isolated execution, symlinked executable discovery
and missing/unloadable library fallback. `HOMEIMAGES_TEST_TURBO_DLL` remains a
backward-compatible test alias. CI runs native tests on all six OS/CPU combinations,
plus Unix race tests, daemon lifecycle tests, download/checksum rejection tests,
and real install/upgrade/restart tests that verify config/annotation preservation.

### Indexing throughput

Indexing prepares previews with one isolated background decoder, queues at most two
prepared images, runs two remote ML requests concurrently by default, and saves
results sequentially. `ml.concurrency` can be set from 1 to 4. The old `interval_ms`
setting is accepted but no longer inserts an artificial delay. Cached image reads
do not pause indexing; actual foreground decoding takes priority. Pause stops new
work while allowing requests already in flight to finish. Outages cancel the current
batch and retry pending work after 30 seconds without marking the library as failed.
`/api/indexing` includes in-flight count, session throughput and mean stage timings.


## Automatic photo locations

On startup, Home Images downloads `cities500.zip`, `admin1CodesASCII.txt` and
`countryInfo.txt` from GeoNames into `<data_dir>/geonames` if missing (about 14 MB
compressed city data plus region/country tables). Subsequent lookups run entirely
locally: GPS coordinates are never sent to a geocoding API. For offline setup,
copy these three files from https://download.geonames.org/export/dump/ into that
folder before starting. Failed downloads retry after ten minutes; browsing and ML
indexing continue independently.

A background worker backfills all indexed images in bounded batches and catches
new or changed photos automatically. `index-go.sqlite3` stores disposable,
signature-validated results in `locations`; originals and manual annotations are
untouched. `GET /api/curation/{id}` and batch curation responses expose
`auto_location` separately from the editable `location`. Gallery, viewer and Echo
captions prefer the manual label, falling back to the automatic label. Location
sorting uses the same fallback. No GPS or no settlement within 50 km produces an
empty automatic caption. These are approximate nearest-settlement labels, not
street addresses or administrative boundary determinations.

Data attribution: [GeoNames](https://www.geonames.org/), licensed under
[Creative Commons Attribution 4.0](https://creativecommons.org/licenses/by/4.0/).
The local lookup formats city, region and country names from these source files.


Offline backfill on another machine can use a consistent, stopped-service SQLite
snapshot without the original photos:

```sh
home-images --geocode-catalog snapshot.sqlite3 --geocode-output locations.sqlite3 --geonames-dir ./geonames
```

The output must be a new file and contains only derived labels, IDs, signatures
and source root identity. The snapshot is opened read-only/immutable; do not use a
live WAL database for this command. On the original server, stop HomeImages and run:

```sh
home-images --config config.json --import-locations locations.sqlite3
```

Then restart the service. Import is transactional, requires the same photo root,
and skips IDs/signatures that no longer match. It does not overwrite manual labels
or original photo metadata. Use the updated executable to serve `auto_location`.


## Catalog storage and migration

Catalog version **1** stores lossless JSON metadata as zlib-compressed BLOBs with
an `HIM` + version-byte header. SQLite's `PRAGMA user_version` identifies the
catalog schema; the original unversioned catalog is version **0**. This version
applies to `index-go.sqlite3`, independently of the AI and curation databases.
`/health` reports `catalog_version`, and the following command inspects an existing
catalog read-only (including a live catalog's WAL):

```sh
./home-images --db-info /path/to/index-go.sqlite3
```

Startup automatically migrates version 0 before serving requests or starting
workers. The schema, compressed metadata, smaller search text and version marker
commit in one transaction. Failure rolls back the entire migration; retrying is
safe. Unknown newer versions are rejected. IDs, signatures, metadata JSON, AI
embeddings, annotations and location results are preserved. Existing Python index
imports and new filesystem scans also use compressed storage.

For a planned upgrade, stop the service and back up the catalog first. Allow
additional free disk space for SQLite's transaction journal/WAL and compaction.
The first upgrade can take several minutes for a large library. To migrate a
stopped catalog without accessing photo originals and reclaim unused pages:

```sh
./home-images --migrate-catalog /path/to/index-go.sqlite3 --compact
./home-images --db-info /path/to/index-go.sqlite3
```

Compaction is optional and is not run automatically at startup. Older binaries
cannot read the compressed catalog; restore the pre-upgrade backup to downgrade.
With a generic SQLite client, metadata requires decompression before JSON queries
(strip the four-byte header, then zlib-decompress). The application registers
`metadata_json(metadata)` on its own SQLite connections for this purpose.

The open `search` column contains deduplicated lowercase readable string values:
paths, filenames, dates, camera names, descriptions, captions, keywords and genuine
text comments, including Unicode. JSON property names, numeric values/arrays,
API URLs and strings containing binary control bytes or invalid-text replacement
characters are excluded. Their original data remains in compressed metadata.
Curation titles/descriptions/tags/locations continue to be searched separately;
semantic object/event search (for example, dogs or birthday scenes) continues to
use CLIP embeddings in the AI database. Date and size filters/sorts use separate
columns maintained when metadata changes, without decompressing the catalog.

Offline location packages can be imported with the service stopped:
`home-images.exe --config config.json --import-locations locations.sqlite3`.
The import checks the package photo root and matches each image ID and file
signature; it updates only derived locations. Manual labels are preserved.
New and changed images are reverse-geocoded automatically by the background
worker using the local GeoNames datasets, including after the offline import.
Photos without GPS or a settlement within 50 km receive an empty result.

People portraits use the isolated preview worker to crop oriented pixels and
encode the final 160?160 JPEG directly. JPEG originals use TurboJPEG when
available. New ML results seed face portraits from the existing inference preview;
a single background worker also warms existing group covers, largest groups
first, sharing the background decoder and yielding to foreground rendering.
Cached portraits retain source-signature invalidation and visibility checks.

The image viewer chooses cached 1280px, 1920px or 2560px square bounding boxes
from the target photo's aspect ratio, display size, pixel density and zoom. The
1280px variant is shared with ML indexing. Viewer and speculative next-photo
requests share the bounded client blob cache and server cache; navigation aborts
obsolete requests. Visible images take priority over queued grid/face previews,
which take priority over speculative prefetch, within the same two-render limit.
A queued prefetch is promoted if its photo becomes visible; aging prevents other
requests from starving. Grid fetching pauses while the viewer is open.
Run `node --test scripts/viewer-regression.test.js` for viewer regression tests.
Use +/minus to zoom, the percentage button to reset, and drag to pan while zoomed.
Fullscreen targets the document rather than the modal dialog.

The grid/list supports `group_by=path|year|people|tag` on `/api/search`.
Grouping covers the full filtered selection before pagination. People and tag
memberships can repeat a photo; responses include `unique_total` and membership
`total`. Unassigned photos appear in explicit fallback groups. Content searches
also support all four groupings: the top 1,200 matches are selected first, then
grouped with similarity ranking preserved within each group. Responses report
`ranked_limit`, `unique_total`, membership `total`, and `indexed_candidates`.
Group direction changes group order while photo order remains most similar first.
Grouped pagination covers every membership, including offsets above 1,000.
`GET /api/people?summary=true` serves the unfiltered People overview from an
in-memory summary warmed at startup and refreshed every 30 seconds. It retains
exact distinct-photo counts and capture years for the snapshot, and returns
`summary_age_ms`. API mutations invalidate it immediately; `refresh=1` forces a
rebuild. Name searches, curation filters, and requests without `summary=true`
continue to query current data. This cache contains metadata, not embeddings.

`GET /api/people/names?q=...&limit=50` returns `{items:[{id,name}]}` for
name autocomplete in both face-tagging and group-review dialogs. It returns only
named, unmerged people with current visible faces, supports literal substring
matching and curation filters, and avoids photo-count/date aggregation. Saving an
existing name in either dialog merges the group into that person.

`GET /api/filter-suggestions?kind=tag|place|folder&q=...` supplies up to 30
literal substring suggestions from visible indexed photos. Place suggestions
and filtering include local GeoNames labels, with manual location overrides.

People/tag group counts are cached for at most 15 seconds across pages (up to
eight filter selections); manual edits invalidate them immediately. Page contents
still apply current visibility and source-signature checks. `refresh=1` rebuilds
counts. Folder and year grouping use covering indexes and unpack only page metadata.

People merge review uses a wide workspace with 25 candidates per batch. A ranked
snapshot of up to 1,000 candidates is fetched once, so selections persist and pages
do not shift during review. Additional batches reveal lower-similarity candidates;
users must review matches before confirming. Up to 500 groups can be selected per batch. Stale groups are skipped and reported;
valid groups commit together, and already-completed sources are safe to retry. The workspace stays open after merging; the People grid refreshes
when review is finished. Names can also be searched in the expandable section.

Face-tag visibility is controlled from both the grid toolbar and image viewer,
with a shared saved preference. Viewer overlays are recalculated after zoom/reset.
Merge review and confirmation show the number of distinct visible source images,
so overlapping group memberships are counted once.

Bulk merging indexes alias lookups and writes the complete undo chain once per
batch. Completion reports identify skipped source groups and their current person
where available. A storage error still rolls back all newly merged valid sources.


### Stable content identities and duplicate cleanup

`photo_assets` stores content-addressed identities and `photo_files` links each
indexed file ID to its asset. Existing file IDs and annotations are retained.
Normal scans still compare path, size and modification time; new files are hashed
in the background. When a new path could be a missing file, an exact full-file
hash verifies a move and reconnects its existing ID. Removed catalog rows retain
recovery information so a later move can reconnect them. Ambiguous matches remain
separate; they are not silently merged. Metadata-only variants share a content
identity for supported formats but retain their individual metadata and annotations.

Open **Manage duplicates** (`/duplicates`) to review all matching file locations.
Choose a retained copy and quarantine an exact duplicate. The server hashes both
originals again before moving the selected copy into `.homeimages-quarantine`
inside the photo root. It rechecks the moved and retained bytes, and records a
recovery token before moving. This directory is excluded from scans and watcher
updates. Restore is available on the same screen; occupied original paths are
never overwritten. Favorites, tags and album membership are carried to the retained copy. Conflicting
text and face information for quarantined copies remain in recovery records.
Restore uses an atomic non-replacing hard link before unlinking quarantine; the
library volume must support hard links (NTFS does). The service needs filesystem permission to move selected files;
permission errors are reported without changing their catalog entries. Quarantine itself frees no disk space. The separate **Permanently delete** action
requires confirmation and verifies the retained original and quarantined copy
again before deleting the quarantined file. It refuses deletion if either copy
has changed or the retained file is missing. Permanent deletion cannot be undone.

To relocate the entire library, stop the service, move the directory, update
`root`, and set `relocate_from` to the exact previous absolute root path in the
configuration. Startup verifies a sample of up to ten previously hashed originals
at their relative paths before reconnecting the catalog, AI data and root-scoped
curation. An unverified root change is rejected rather than clearing existing data.
Remove `relocate_from` after successful relocation. Use a separate data directory
when configuring an unrelated library.

### Folder overlap cleanup

The default `/duplicates` view compares directory trees at a selected depth,
using indexed content IDs and exact file checksums. For example, depth 2 compares
`ALBUM/Album A` with `ALBUM/Album B`, including all their descendants. A scope such
as `ALBUM` with depth 1 compares the same trees. Files shallower than the selected
depth are not grouped. Results show directional full/partial photo coverage,
metadata variants, pending hashes and potential duplicate bytes, ordered by space.
These are logical photo-byte estimates; coverage never authorizes removal of an
entire filesystem directory or of unindexed/non-photo files.

Review creates a persisted, immutable plan containing the source/retained IDs,
paths and expected exact checksums. Starting moves only these matching photos to
Trash. The worker verifies bytes and path membership again, skips changed or
unavailable files, and leaves unique photos and metadata variants in place. Parent
and child trees cannot be chosen as source and retained trees. One cleanup job
runs at a time, independently of the browser. Progress, per-file errors and journals
are persisted; cancellation preserves completed work, and a restart marks unfinished
jobs interrupted for explicit resume. Recorded moves can be reconnected if a
process stopped between the filesystem move and the progress update.

Completed groups offer **Restore this group** and **Permanently delete this group**.
These operate on successful copies still in Trash and reuse the single-copy
verification and non-overwriting restoration rules. Permanent deletion requires
confirmation and rechecks that a retained exact original remains available.
The previous individual-photo interface is available under **Individual photos &
Trash**. Folder comparisons use a short-lived in-memory snapshot for pagination;
Refresh and cleanup completion invalidate it. Creating a plan always reads the
current catalog, and actual cleanup always reads the originals for verification.
