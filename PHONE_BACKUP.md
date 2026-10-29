# Android phone backup with Syncthing-Fork

Open **Settings & phone backup** in Home Images, then choose **Enable phone
backup**. The first start downloads, verifies and configures a dedicated
Syncthing executable. Home Images manages its process and restarts it after
failures. This replaces the previous phone-upload integration; existing
Syncthing installations and previously uploaded originals are preserved.

1. Install [Syncthing-Fork from F-Droid](https://f-droid.org/packages/com.github.catfriend1.syncthingfork/)
   or the [upstream Android APK](https://github.com/researchxxl/syncthing-android/releases).
   Grant access to photos and videos.
2. In the app's **Devices** tab, add a device and scan the receiver QR on the
   Home Images page. Name the receiver **Home Images** and save. Scan inside
   Syncthing-Fork, rather than the phone camera app. If viewing the page on the
   same phone, expand **Connecting from this phone?** and copy the receiver ID.
3. Match the device ID shown in the new phone request to your phone's
   **Show device ID**, enter a name, and choose **Approve phone**.
4. In Syncthing-Fork, add or edit the `DCIM/Camera` folder, choose **Send Only**,
   and share it with **Home Images**.
5. When the folder offer appears in Home Images, choose **Back up this folder**.
   The server configures a private receive-only destination automatically.

Allow the phone app to run in the background. Wi-Fi-only and other run
conditions are controlled in the app. Both devices need to be online to
transfer. Home Images shows connection state, remaining transfer bytes,
archived file count and the last successful backup.

Completed photos, videos and camera sidecars are copied to **Phone backups**
in your library. Phone deletions affect only the private sync staging copy.
Removing a phone or disabling backup preserves archived files. Wait for
synchronization and archive import to complete and check the last backup
before deleting originals from your phone. Budget disk space for both staging
and archive copies. Videos are retained even where the gallery cannot display them.

For installed services, rerun the updated installer to grant the service write
access to the **Phone backups** subfolder and, on Windows, configure the
firewall rule for our private executable. Existing originals retain their
previous permissions. The private receiver uses separate configuration,
identity and database paths, and avoids occupied ports. It never takes over
or upgrades an existing Syncthing installation.

See [Android phone backup in the README](README.md#android-phone-backup) for
storage paths, supported platforms, network details and integration tests.
