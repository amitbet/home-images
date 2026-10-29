package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"howett.net/plist"
)

func backupFixture(t *testing.T) (*Library, *phoneBackup, *httptest.Server) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "photos")
	os.Mkdir(root, 0750)
	l, e := newLibrary(Config{Root: root, DataDir: filepath.Join(base, "data"), ScanInterval: 30, MaxPixels: 80000000})
	if e != nil {
		t.Fatal(e)
	}
	l.config.SFTP = SFTPConfig{Enabled: true, Host: "127.0.0.1", PublicURL: "http://192.0.2.1:8372", MaxFileBytes: 1 << 20}
	b, e := startPhoneBackup(l)
	if e != nil {
		l.Close()
		t.Fatal(e)
	}
	l.backup = b
	srv := httptest.NewServer(l.Handler())
	t.Cleanup(func() { srv.Close(); b.Close(); l.Close() })
	return l, b, srv
}
func backupRequest(t *testing.T, s *httptest.Server, method, path, token, body string) (int, []byte) {
	t.Helper()
	r, e := http.NewRequest(method, s.URL+"/api/phone-backup/"+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Home-Images", "1")
	resp, e := s.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("credentials must not be cached")
	}
	return resp.StatusCode, data
}
func addBackupPhone(t *testing.T, l *Library, s *httptest.Server) (string, map[string]any) {
	t.Helper()
	code, data := backupRequest(t, s, "POST", "phones", "", `{"name":"Test phone"}`)
	if code != 201 {
		t.Fatalf("create %d %s", code, data)
	}
	var created map[string]any
	json.Unmarshal(data, &created)
	link := created["url"].(string)
	token := strings.Split(link, "#")[1]
	code, data = backupRequest(t, s, "GET", "pair", token, "")
	if code != 200 {
		t.Fatalf("pair %d %s", code, data)
	}
	var settings map[string]any
	json.Unmarshal(data, &settings)
	return token, settings
}
func dialBackup(t *testing.T, b *phoneBackup, p map[string]any) (*ssh.Client, *sftp.Client) {
	t.Helper()
	conn, e := ssh.Dial("tcp", b.listener.Addr().String(), &ssh.ClientConfig{User: p["username"].(string), Auth: []ssh.AuthMethod{ssh.Password(p["password"].(string))}, HostKeyCallback: ssh.FixedHostKey(b.signer.PublicKey()), Timeout: time.Second * 5})
	if e != nil {
		t.Fatal(e)
	}
	client, e := sftp.NewClient(conn)
	if e != nil {
		conn.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() { client.Close(); conn.Close() })
	return conn, client
}
func TestPhoneBackupUploadAndIsolation(t *testing.T) {
	l, b, s := backupFixture(t)
	_, p := addBackupPhone(t, l, s)
	conn, c := dialBackup(t, b, p)
	if e := c.Mkdir("2026"); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile("testdata/orientation6.jpg")
	if e != nil {
		t.Fatal(e)
	}
	f, e := c.OpenFile("2026/photo.jpg", os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write(data); e != nil {
		t.Fatal(e)
	}
	local := filepath.Join(l.rootPath, "Phone uploads", p["username"].(string), "2026", "photo.jpg")
	if _, e = os.Stat(local); !os.IsNotExist(e) {
		t.Fatal("upload published before close")
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(local)
	if e != nil || !bytes.Equal(got, data) {
		t.Fatal("original differs", e)
	}
	if f, e = c.OpenFile("2026/photo.jpg", os.O_WRONLY|os.O_CREATE|os.O_TRUNC); e == nil {
		f.Close()
		t.Fatal("overwrite allowed")
	}
	if e = c.Remove("2026/photo.jpg"); e == nil {
		t.Fatal("delete allowed")
	}
	if e = c.Rename("2026/photo.jpg", "changed.jpg"); e == nil {
		t.Fatal("rename allowed")
	}
	if e = c.Symlink("../../a.jpg", "escape.jpg"); e == nil {
		t.Fatal("symlink allowed")
	}
	// The SFTP library normalizes virtual absolute paths before dispatch. Even
	// normalized parent components must never access anything outside this phone.
	if escaped, err := c.Create("/../../escape.jpg"); err == nil {
		escaped.Write(data)
		escaped.Close()
	}
	for _, outside := range []string{filepath.Join(l.rootPath, "escape.jpg"), filepath.Join(l.rootPath, "Phone uploads", "escape.jpg")} {
		if _, err := os.Stat(outside); !os.IsNotExist(err) {
			t.Fatal("upload escaped phone root")
		}
	}
	if _, e = c.Open("2026/photo.jpg"); e == nil {
		t.Fatal("download allowed")
	}
	session, e := conn.NewSession()
	if e == nil {
		if e = session.Run("whoami"); e == nil {
			t.Fatal("shell allowed")
		}
		session.Close()
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		l.read.QueryRow("SELECT count(*) FROM images WHERE path LIKE 'Phone uploads/%'").Scan(&n)
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed upload not indexed")
		}
		time.Sleep(30 * time.Millisecond)
	}
	// A dropped connection must discard its still-open upload.
	f, e = c.Create("interrupted.jpg")
	if e != nil {
		t.Fatal(e)
	}
	f.Write([]byte("partial"))
	conn.Close()
	time.Sleep(100 * time.Millisecond)
	if _, e = os.Stat(filepath.Join(filepath.Dir(filepath.Dir(local)), "interrupted.jpg")); !os.IsNotExist(e) {
		t.Fatal("interrupted file published")
	}
}
func TestPhoneBackupPairingAndRevocation(t *testing.T) {
	l, b, s := backupFixture(t)

	token, p := addBackupPhone(t, l, s)
	qr, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(p["qr"].(string), "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = png.DecodeConfig(bytes.NewReader(qr)); err != nil {
		t.Fatal("pairing page QR is not a valid PNG", err)
	}
	if !strings.HasSuffix(p["url"].(string), "#"+token) {
		t.Fatal("pairing QR URL uses wrong token")
	}
	conn, c := dialBackup(t, b, p)
	code, data := backupRequest(t, s, "GET", "settings", token, "")
	if code != 200 {
		t.Fatal(code, string(data))
	}
	var values map[string]any
	if _, e := plist.Unmarshal(data, &values); e != nil {
		t.Fatal(e)
	}
	if values["1_service_server"] != "192.0.2.1" || values["1_service_password"] != p["password"] || values["1_service_overwrite"] != false || values["1_service_temporary_write"] != false {
		t.Fatal("incorrect PhotoSync settings")
	}
	raw, _ := os.ReadFile(filepath.Join(l.config.DataDir, "phones.json"))
	if bytes.Contains(raw, []byte(p["password"].(string))) {
		t.Fatal("plaintext password persisted")
	}
	if code, _ = backupRequest(t, s, "DELETE", "phones/"+p["username"].(string), "", ""); code != 204 {
		t.Fatal(code)
	}
	if _, e := c.Stat("/"); e == nil {
		t.Fatal("revoked connection still usable")
	}
	conn.Close()
	if code, _ = backupRequest(t, s, "GET", "pair", token, ""); code != 401 {
		t.Fatal("revoked pairing still valid", code)
	}
	token, _ = addBackupPhone(t, l, s)
	b.mu.Lock()
	pair := b.pairs[token]
	pair.Expires = time.Now().Add(-time.Second)
	b.pairs[token] = pair
	b.mu.Unlock()
	if code, _ = backupRequest(t, s, "GET", "pair", token, ""); code != 401 {
		t.Fatal("expired pairing valid")
	}
}
func TestPhoneBackupFileLimitsAndPaths(t *testing.T) {
	l, b, s := backupFixture(t)
	_, p := addBackupPhone(t, l, s)
	_, c := dialBackup(t, b, p)
	f, e := c.Create("large.jpg")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.WriteAt([]byte("too big"), l.config.SFTP.MaxFileBytes); e == nil {
		t.Fatal("size limit bypassed")
	}
	if e = f.Close(); e == nil {
		t.Fatal("failed upload committed")
	}
	for _, name := range []string{"/../x", "a/../x", "a\\x", "C:x", ".hidden", "x/./y", "x.", "x/ y"} {
		if _, e := phonePath(name); e == nil {
			t.Errorf("unsafe path accepted: %q", name)
		}
	}
	if _, e := net.DialTimeout("tcp", b.listener.Addr().String(), time.Second); e != nil {
		t.Fatal(e)
	}
}
func TestPhoneBackupPersistentIdentity(t *testing.T) {
	l, b, s := backupFixture(t)
	_, p := addBackupPhone(t, l, s)
	fingerprint := ssh.FingerprintSHA256(b.signer.PublicKey())
	b.Close()
	next, e := startPhoneBackup(l)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if ssh.FingerprintSHA256(next.signer.PublicKey()) != fingerprint {
		t.Fatal("host key changed")
	}
	_, c := dialBackup(t, next, p)
	if _, e = c.Stat("/"); e != nil {
		t.Fatal(e)
	}
}
func TestPhoneBackupConfiguration(t *testing.T) {
	for _, raw := range []string{"", "http://localhost:8372", "http://127.0.0.1:8372", "http://0.0.0.0:8372", "https://host/path", "https://user:pass@host", "https://host?secret=1"} {
		c := SFTPConfig{Enabled: true, PublicURL: raw}
		if c.validate() == nil {
			t.Errorf("invalid public URL accepted: %s", raw)
		}
	}
	c := SFTPConfig{Enabled: true, PublicURL: "http://192.168.1.81:8372"}
	if err := c.validate(); err != nil {
		t.Fatal("management must not require an admin password", err)
	}
	if e := c.validate(); e != nil {
		t.Fatal(e)
	}
	if c.Port != 2222 || c.Host != "127.0.0.1" || c.MaxFileBytes != 20<<30 {
		t.Fatal("unexpected defaults")
	}
	l, _, _ := fixture(t)
	s := httptest.NewServer(l.Handler())
	defer s.Close()
	code, _ := backupRequest(t, s, "GET", "phones", "", "")
	if code != 503 {
		t.Fatal("disabled backup not reported", code)
	}
}
func TestPhoneBackupManagementWithoutLogin(t *testing.T) {
	_, _, s := backupFixture(t)
	code, _ := backupRequest(t, s, "GET", "phones", "", "")
	if code != 200 {
		t.Fatal("phone management requires login", code)
	}
	for _, custom := range []bool{false, true} {
		req, _ := http.NewRequest("POST", s.URL+"/api/phone-backup/phones", strings.NewReader(`{"name":"Cross-site phone"}`))
		req.Header.Set("Origin", "https://unrelated.example")
		if custom {
			req.Header.Set("X-Home-Images", "1")
		}
		resp, err := s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatal("cross-origin management permitted", resp.StatusCode)
		}
	}
}
