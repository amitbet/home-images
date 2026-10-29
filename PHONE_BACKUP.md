# Phone backup with PhotoSync

Home Images can run an optional embedded **SFTP-only** server. Each phone has a
random username/password and its own directory under `Phone uploads/<phone-id>`
in the configured photo root. No system SSH account or separate server is needed.

## Enable the server

Build/install the updated binary, then add this section to your existing config:

```json
"sftp": {
  "enabled": true,
  "host": "0.0.0.0",
  "port": 2222,
  "public_url": "http://192.168.1.81:8372",
  "max_file_bytes": 21474836480
}
```

`public_url` must be the phone-reachable origin of this service, without a path.
It supplies the QR link and SFTP hostname. If disabled/omitted, no SFTP listener,
key, account file or upload directory is created. An enabled server without a
valid public URL fails startup. The default SFTP bind address is
loopback; explicitly use a LAN address or `0.0.0.0` to accept phone connections.
The HTTP listener must also be reachable from the phone.

Restart Home Images. Allow inbound TCP 2222 from your LAN in the host firewall.
The service account needs write permission on the photo root, its phone upload
folders and data directory. Atomic upload publication uses hard links: use NTFS
on Windows or a filesystem supporting hard links on Linux/macOS.

Use this on your trusted home LAN or through your VPN. SFTP encrypts transfers;
the setup page inherits HTTP/HTTPS from the existing service. HTTP setup exposes
credentials to network observers, so use HTTPS for setup on an untrusted network.
Do not publish the existing unauthenticated photo browser to the internet.

## Connect a phone

1. Open **Settings & phone backup** in the library sidebar, or `/settings`.
2. Phone management opens directly; no admin password is required.
3. Add a phone name and scan the QR with the phone's camera on the same network.
4. Install PhotoSync. On iPhone, download `HomeImages.photosync` and open it in
   PhotoSync using the Share menu. On Android, use Configure → FTP → Add → SFTP
   and copy the displayed server, port, username, password and `/` destination.
5. Compare the displayed SHA256 SSH host fingerprint before accepting it.
6. Send one photo with Send → New → FTP, then configure PhotoSync Autotransfer
   with that target. The OS controls background scheduling; paid PhotoSync
   features may be needed.

The `.photosync` download is an XML property list using PhotoSync's published
AppConfig keys and service enums. **Actual settings-file import has not yet been
verified on an iPhone.** If the app does not accept it, the manual settings work
with any SFTP client; no QR-based PhotoSync deep link is assumed.

Generated settings select SFTP, year/month folders, photos/videos over Wi-Fi,
no overwrite, no deletion after transfer, and no client temporary-file renaming.
On manual setup select original quality, disable **Allow overwrite**, **Delete
after transfer**, and **Transfer to Temporary File**. Server-side staging already
keeps incomplete files out of the photo index. Timestamp-setting requests are
acknowledged without changing filesystem times; original EXIF capture dates stay
unchanged. Video uploads are stored, but only supported image formats are indexed.

## Credentials and recovery

QR links expire after 15 minutes and contain an unguessable setup token in a URL
fragment. The fragment is removed from the address bar after opening and retained in that tab's session storage so reload works; credentials
are fetched using an Authorization header and responses are not cached. Anyone
holding the link can obtain that phone's credentials before expiration. The phone setup page also displays its QR code and expiry. Expired links show a
link back to Settings. Do not share screenshots of the QR or downloaded settings files.

**New setup link** rotates the existing phone password and disconnects active
sessions; reconfigure that phone with the new credentials. **Revoke access**
disconnects the phone immediately and invalidates its setup links. It leaves
uploaded originals in place. Restarting the service expires all pending setup
links but preserves established phone credentials and the SSH host key.

Settings is available to users on the trusted home network without an admin login.

The data directory contains `phones.json` (bcrypt password hashes) and
`sftp-host-key.pem` (persistent Ed25519 private key). Protect and back up these
files and the config with OS permissions. Raw phone passwords are held in memory
only during pairing and included in the downloadable settings file.

## Upload behavior

- Phone clients can list/stat their own folder, create directories, and upload
  new files. Downloads, shells, port forwarding, deletion, renaming, symlinks,
  arbitrary metadata changes and overwriting originals are denied.
- Files are staged as `.upload-<random>.part` in the destination directory. A
  successful SFTP close flushes and atomically publishes the original without
  replacing an existing file. Disconnects and write/size errors discard staging.
- Interrupted uploads must restart; resumable writes are not supported. A process
  crash can leave hidden `.part` files; remove these only while the service is
  stopped. They are never indexed as photos.
- Completed uploads request a background scan, coalesced over two seconds. The
  regular library scan remains the fallback if another scan is already running.
- Up to 16 connections and one SFTP session per connection are allowed. Handshakes
  have a 20-second deadline, connections a 24-hour lifetime. `max_file_bytes`
  bounds each file (including sparse offsets); it is not a total disk quota.
- New setup links and phone accounts are capped at 100 each.

Reference: [PhotoSync SFTP setup](https://www.photosync-app.com/support/basics/answers/how-to-transfer-to-a-linux-device),
[settings import](https://config.photosync-app.com/),
[AppConfig specification](https://download.photosync-app.com/xml/mdm_specfile.zip).
