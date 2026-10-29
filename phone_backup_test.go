package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncthingDownloadRejectsTamperingAndExtractsOnlyRegularFiles(t *testing.T) {
	if err := verifySyncthingArchive([]byte("tampered"), strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted a checksum mismatch")
	}
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for _, name := range []string{"upstream/" + syncthingExe(), "upstream/LICENSE.txt", "../../outside", "upstream/config.xml", "upstream/etc/service/" + syncthingExe()} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(name))
	}
	h := &zip.FileHeader{Name: "symlink/README.txt"}
	h.SetMode(os.ModeSymlink | 0777)
	w, _ := z.CreateHeader(h)
	_, _ = w.Write([]byte("../../outside"))
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := extractSyncthing(data.Bytes(), true, dir); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("unexpected extracted files: %v", files)
	}
}

func TestSyncthingConfigurationPreservesIdentityAndFolders(t *testing.T) {
	original := []byte(`<configuration version="52"><device id="existing" name="my device"/><folder id="camera" path="/private/incoming"/><gui enabled="false" tls="true"><address>127.0.0.1:8384</address><apikey>old</apikey></gui><options><listenAddress>default</listenAddress><listenAddress>quic://:22000</listenAddress><maxRecvKbps>1024</maxRecvKbps></options></configuration>`)
	data, err := configureSyncthingXML(original, "127.0.0.1:9999", "private-key", 22001)
	if err != nil {
		t.Fatal(err)
	}
	var root syncthingXML
	if err = xml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`id="existing"`, `id="camera"`, `path="/private/incoming"`, "tcp://0.0.0.0:22001", "127.0.0.1:9999", "<maxRecvKbps>1024</maxRecvKbps>"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("missing preserved/managed value %s: %s", want, data)
		}
	}
	for _, unwanted := range []string{"quic://:22000", "<listenAddress>default</listenAddress>", "<apikey>old</apikey>"} {
		if bytes.Contains(data, []byte(unwanted)) {
			t.Fatalf("retained %s", unwanted)
		}
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	occupied := listener.Addr().(*net.TCPAddr).Port
	port, err := availableSyncPort(occupied)
	if err != nil || port == occupied {
		t.Fatalf("did not avoid occupied port: %d %v", port, err)
	}
}

func TestPhoneBackupArchivePreservesDeletedAndChangedOriginals(t *testing.T) {
	l, _, _ := fixture(t)
	b := l.backup
	phone := backupPhone{ID: "phone", Directory: "My-Android"}
	folder := backupFolder{ID: "camera", Directory: "camera"}
	dir := filepath.Join(b.base, "incoming", folder.Directory)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "photo.jpg")
	put := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("original")
	if err := os.WriteFile(filepath.Join(dir, ".syncthing.partial.jpg"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	_ = os.WriteFile(outside, []byte("outside"), 0600)
	// Windows may require developer mode or elevation to create a symlink.
	_ = os.Symlink(outside, filepath.Join(dir, "escape.jpg"))
	check := func(want int) {
		t.Helper()
		n, err := b.importFolder(context.Background(), phone, folder)
		if err != nil || n != want {
			t.Fatalf("import: n=%d want=%d err=%v", n, want, err)
		}
	}
	check(1)
	check(0)
	put("changed photo")
	check(1)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check(0)
	archive := filepath.Join(l.rootPath, "Phone backups", phone.Directory, folderKey(folder.ID))
	files, err := os.ReadDir(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("want two preserved versions; got %v", files)
	}
	contents := map[string]bool{}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(archive, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		contents[string(data)] = true
	}
	if !contents["original"] || !contents["changed photo"] {
		t.Fatalf("wrong archived contents: %v", contents)
	}
	// Restarting the manager keeps both the manifest and pairing settings.
	b.mu.Lock()
	previous := b.settings
	b.settings.Phones = []backupPhone{phone}
	err = b.saveLocked(previous)
	b.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := newPhoneBackup(l)
	if err != nil || len(reloaded.settings.Phones) != 1 {
		t.Fatalf("settings did not survive restart: %v", err)
	}
}

func TestPhoneBackupSkipsUnchangedSyncthingSequence(t *testing.T) {
	l, _, _ := fixture(t)
	b := l.backup
	phone := backupPhone{ID: "phone", Directory: "Phone", Folders: []backupFolder{{ID: "camera", Directory: "camera"}}}
	b.settings.Phones = []backupPhone{phone}
	dir := filepath.Join(b.base, "incoming", "camera")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte("photo"), 0600); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = writeJSON(w, 200, map[string]any{"state": "idle", "sequence": 1, "needTotalItems": 0, "pullErrors": 0})
	}))
	defer remote.Close()
	b.connection = &syncthingConnection{url: remote.URL, key: "private", client: remote.Client()}
	if err := b.importFolders(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := b.importFolders(context.Background()); err != nil {
		t.Fatalf("unchanged folder was walked again: %v", err)
	}
}

func TestPhoneBackupAPIRequiresSameOriginAndPendingApproval(t *testing.T) {
	l, server, _ := fixture(t)
	for _, path := range []string{"/settings", "/phone-setup", "/phone-backup"} {
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || !bytes.Contains(data, []byte("Syncthing-Fork")) || bytes.Contains(data, []byte("PhotoSync")) {
			t.Fatalf("wrong phone page %s: %d", path, response.StatusCode)
		}
	}
	for _, test := range []struct {
		header, origin string
		want           int
	}{{"", "", 403}, {"1", "https://evil.example", 403}, {"1", server.URL, 200}} {
		req, _ := http.NewRequest("GET", server.URL+"/api/phone-backup/status", nil)
		req.Header.Set("X-Home-Images", test.header)
		req.Header.Set("Origin", test.origin)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.want {
			t.Fatalf("origin/header check: got %d want %d", response.StatusCode, test.want)
		}
		if response.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("admin API granted CORS")
		}
	}
	posted := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "private" {
			t.Error("missing private API key")
		}
		if r.Method == "POST" {
			posted = true
		}
		_ = writeJSON(w, 200, map[string]any{})
	}))
	defer remote.Close()
	l.backup.connection = &syncthingConnection{url: remote.URL, key: "private", client: remote.Client()}
	req, _ := http.NewRequest("POST", server.URL+"/api/phone-backup/accept-phone", strings.NewReader(`{"device":"unknown","name":"My phone"}`))
	req.Header.Set("X-Home-Images", "1")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 409 || posted {
		t.Fatalf("approved a device which was not pending: %d", response.StatusCode)
	}
}

// Runs against the pinned upstream binary, with no external devices or network
// discovery needed. CI downloads and verifies that binary before enabling this.
func TestSyncthingRealTransferLifecycle(t *testing.T) {
	binary := os.Getenv("HOMEIMAGES_TEST_SYNCTHING_EXE")
	if binary == "" && os.Getenv("HOMEIMAGES_TEST_SYNCTHING_DOWNLOAD") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		var err error
		binary, err = installSyncthing(ctx, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
	}
	if binary == "" {
		t.Skip("set HOMEIMAGES_TEST_SYNCTHING_EXE to test real Syncthing processes")
	}
	l, server, _ := fixture(t)
	peer, _, _ := fixture(t)
	for _, lib := range []*Library{l, peer} {
		dest := filepath.Join(lib.backup.base, "bin", syncthingVersion, syncthingExe())
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, data, 0700); err != nil {
			t.Fatal(err)
		}
		if err = lib.backup.setEnabled(true); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(label string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if predicate() {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		for _, lib := range []*Library{l, peer} {
			lib.backup.mu.Lock()
			t.Logf("status=%s error=%s", lib.backup.status, lib.backup.lastError)
			lib.backup.mu.Unlock()
		}
		t.Fatal("timeout: " + label)
	}
	wait("both receivers ready", func() bool {
		l.backup.mu.Lock()
		a := l.backup.connection != nil
		l.backup.mu.Unlock()
		peer.backup.mu.Lock()
		b := peer.backup.connection != nil
		peer.backup.mu.Unlock()
		return a && b
	})
	l.backup.mu.Lock()
	id, port := l.backup.deviceID, l.backup.settings.Port
	l.backup.mu.Unlock()
	peer.backup.mu.Lock()
	peerID, peerPort := peer.backup.deviceID, peer.backup.settings.Port
	peer.backup.mu.Unlock()
	if id == peerID || port == peerPort {
		t.Fatal("instances did not get independent identities/ports")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var device map[string]any
	if err := peer.backup.call(ctx, "GET", "/rest/config/defaults/device", nil, &device); err != nil {
		t.Fatal(err)
	}
	device["deviceID"], device["name"], device["addresses"] = id, "Home Images", []string{fmt.Sprintf("tcp://127.0.0.1:%d", port)}
	if err := peer.backup.call(ctx, "POST", "/rest/config/devices", device, nil); err != nil {
		t.Fatal(err)
	}
	wait("phone pairing request", func() bool {
		var pending map[string]pendingBackupDevice
		return l.backup.call(ctx, "GET", "/rest/cluster/pending/devices", nil, &pending) == nil && pending[peerID].Name != ""
	})
	post := func(action string, body any) {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", server.URL+"/api/phone-backup/"+action, bytes.NewReader(data))
		req.Header.Set("X-Home-Images", "1")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("%s: %d %s", action, response.StatusCode, data)
		}
	}
	post("accept-phone", map[string]string{"device": peerID, "name": "Test Android"})
	camera := filepath.Join(t.TempDir(), "DCIM", "Camera")
	if err := os.MkdirAll(camera, 0700); err != nil {
		t.Fatal(err)
	}
	photo, err := os.ReadFile("testdata/orientation6.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(camera, "phone.jpg"), photo, 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Mkdir(filepath.Join(camera, ".stfolder"), 0700)
	var folder map[string]any
	if err = peer.backup.call(ctx, "GET", "/rest/config/defaults/folder", nil, &folder); err != nil {
		t.Fatal(err)
	}
	folder["id"], folder["label"], folder["path"], folder["type"] = "test-camera", "Camera", camera, "sendonly"
	folder["rescanIntervalS"] = 1
	folder["devices"] = []map[string]string{{"deviceID": peerID}, {"deviceID": id}}
	if err = peer.backup.call(ctx, "POST", "/rest/config/folders", folder, nil); err != nil {
		t.Fatal(err)
	}
	wait("camera folder offered", func() bool {
		var pending map[string]pendingBackupFolder
		return l.backup.call(ctx, "GET", "/rest/cluster/pending/folders", nil, &pending) == nil && len(pending["test-camera"].OfferedBy) > 0
	})
	post("accept-folder", map[string]string{"device": peerID, "folder": "test-camera"})
	var archived string
	wait("photo transferred and archived", func() bool {
		return l.db.QueryRow("SELECT archive FROM phone_backup_files WHERE source LIKE '%/phone.jpg'").Scan(&archived) == nil
	})
	data, err := os.ReadFile(filepath.Join(l.rootPath, filepath.FromSlash(archived)))
	if err != nil || !bytes.Equal(data, photo) {
		t.Fatalf("archive differs: %v", err)
	}
	if err = os.Remove(filepath.Join(camera, "phone.jpg")); err != nil {
		t.Fatal(err)
	}
	if err = peer.backup.call(ctx, "POST", "/rest/db/scan?folder=test-camera", nil, nil); err != nil {
		t.Fatal(err)
	}
	l.backup.mu.Lock()
	incoming := filepath.Join(l.backup.base, "incoming", l.backup.settings.Phones[0].Folders[0].Directory, "phone.jpg")
	l.backup.mu.Unlock()
	wait("phone deletion propagated only to staging", func() bool { _, err := os.Stat(incoming); return os.IsNotExist(err) })
	if _, err = os.Stat(filepath.Join(l.rootPath, filepath.FromSlash(archived))); err != nil {
		t.Fatal("phone deletion removed archive", err)
	}
	// Force a crash and check that our supervisor restarts only its own process.
	l.backup.mu.Lock()
	crashed := l.backup.process
	l.backup.mu.Unlock()
	if crashed == nil {
		t.Fatal("no managed process")
	}
	if err = crashed.Kill(); err != nil {
		t.Fatal(err)
	}
	wait("crash recovery", func() bool {
		l.backup.mu.Lock()
		defer l.backup.mu.Unlock()
		return l.backup.connection != nil && l.backup.process != nil && l.backup.process.Pid != crashed.Pid
	})
	// Disable fully stops the process. Starting again preserves the device ID,
	// paired phone and archive manifest, without disturbing the other receiver.
	l.backup.mu.Lock()
	port = l.backup.settings.Port
	l.backup.mu.Unlock()
	if err = l.backup.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		conn.Close()
		t.Fatal("disabled receiver is still listening")
	}
	if err = l.backup.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	wait("receiver restarted", func() bool { l.backup.mu.Lock(); defer l.backup.mu.Unlock(); return l.backup.connection != nil })
	l.backup.mu.Lock()
	same := l.backup.deviceID == id && len(l.backup.settings.Phones) == 1
	l.backup.mu.Unlock()
	if !same {
		t.Fatal("restart lost identity/pairing")
	}
	post("remove-phone", map[string]string{"device": peerID})
	if _, err = os.Stat(filepath.Join(l.rootPath, filepath.FromSlash(archived))); err != nil {
		t.Fatal("removing phone removed archive", err)
	}
}
