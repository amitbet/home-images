package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"howett.net/plist"
)

func backupContext(done <-chan struct{}) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-done; cancel() }()
	return ctx
}
func validateBackupURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("sftp.public_url must be the phone-reachable HTTP(S) origin of Home Images, e.g. http://192.168.1.81:8372")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsUnspecified() || ip.IsLoopback()) {
		return fmt.Errorf("sftp.public_url must be reachable from your phone")
	}
	if u.Hostname() == "localhost" {
		return fmt.Errorf("sftp.public_url cannot be localhost")
	}
	return nil
}
func (l *Library) backupRoutes(mux *http.ServeMux) {
	for _, p := range []string{"/settings", "/phone-backup", "/phone-setup"} {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, r *http.Request) {
			backupHeaders(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data, _ := webFiles.ReadFile("web/phone-backup.html")
			w.Write(data)
		})
	}
	mux.HandleFunc("/api/phone-backup/", func(w http.ResponseWriter, r *http.Request) {
		backupHeaders(w)
		b := l.backup
		if b == nil {
			http.Error(w, "Phone backup is disabled. Enable sftp in the service configuration and restart.", 503)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.URL.Path == "/api/phone-backup/pair" || r.URL.Path == "/api/phone-backup/settings" {
			if r.Method != "GET" {
				http.Error(w, "Use GET", 405)
				return
			}
			b.mu.Lock()
			pair, ok := b.pairs[token]
			if !ok || time.Now().After(pair.Expires) {
				delete(b.pairs, token)
				b.mu.Unlock()
				http.Error(w, "Setup link expired. Create a new link from Phone backup.", 401)
				return
			}
			_, ok = b.accounts[pair.Account.ID]
			b.mu.Unlock()
			if !ok {
				http.Error(w, "Phone was revoked", 401)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/settings") {
				data, e := photoSyncSettings(l.config.SFTP, pair)
				if e != nil {
					http.Error(w, "Cannot generate settings", 500)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", `attachment; filename="HomeImages.photosync"`)
				w.Write(data)
				return
			}
			u, _ := url.Parse(l.config.SFTP.PublicURL)
			link := strings.TrimRight(l.config.SFTP.PublicURL, "/") + "/phone-setup#" + token
			png, err := qrcode.Encode(link, qrcode.Medium, 280)
			if err != nil {
				http.Error(w, "Cannot generate QR", 500)
				return
			}
			writeJSON(w, 200, map[string]any{"name": pair.Account.Name, "username": pair.Account.ID, "password": pair.Password, "host": u.Hostname(), "port": l.config.SFTP.Port, "path": "/", "fingerprint": ssh.FingerprintSHA256(b.signer.PublicKey()), "expires": pair.Expires, "url": link, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
			return
		}
		// Management is available directly on the trusted LAN. A custom header
		// and same-origin check keep third-party websites from changing phones.
		if r.Header.Get("X-Home-Images") != "1" {
			http.Error(w, "Missing request header", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if err != nil || u.Host != r.Host || u.Scheme != scheme {
				http.Error(w, "Same-origin request required", 403)
				return
			}
		}
		w.Header().Del("Access-Control-Allow-Origin")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/phone-backup/phones":
			b.mu.Lock()
			phones := []map[string]string{}
			for _, a := range b.accounts {
				phones = append(phones, map[string]string{"id": a.ID, "name": a.Name})
			}
			b.mu.Unlock()
			writeJSON(w, 200, map[string]any{"phones": phones, "port": l.config.SFTP.Port, "public_url": l.config.SFTP.PublicURL, "fingerprint": ssh.FingerprintSHA256(b.signer.PublicKey())})
		case r.Method == "POST" && r.URL.Path == "/api/phone-backup/phones":
			var in struct {
				Name string `json:"name"`
				ID   string `json:"id"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&in) != nil {
				http.Error(w, "Invalid request", 400)
				return
			}
			in.Name = strings.TrimSpace(in.Name)
			if in.Name == "" || len(in.Name) > 80 {
				http.Error(w, "Enter a phone name (up to 80 characters)", 400)
				return
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			for t, p := range b.pairs {
				if time.Now().After(p.Expires) {
					delete(b.pairs, t)
				}
			}
			if len(b.pairs) >= 100 || len(b.accounts) >= 100 {
				http.Error(w, "Phone/setup limit reached", 409)
				return
			}
			a, exists := b.accounts[in.ID]
			if in.ID != "" && !exists {
				http.Error(w, "Phone not found", 404)
				return
			}
			if !exists {
				a = phoneAccount{ID: "phone-" + backupSecret()[:12], Name: in.Name}
			}
			password := backupSecret()
			hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if e != nil {
				http.Error(w, "Cannot create credentials", 500)
				return
			}
			a.Hash = string(hash)
			if e = b.lib.root.MkdirAll(filepath.Join("Phone uploads", a.ID), 0750); e != nil {
				http.Error(w, "Cannot create upload folder", 500)
				return
			}
			old := b.accounts[a.ID]
			b.accounts[a.ID] = a
			if e = b.saveLocked(); e != nil {
				if exists {
					b.accounts[a.ID] = old
				} else {
					delete(b.accounts, a.ID)
				}
				http.Error(w, "Cannot save phone", 500)
				return
			}
			// Re-pairing rotates the password and closes old sessions immediately.
			for c, id := range b.connections {
				if id == a.ID {
					c.Close()
				}
			}
			for t, p := range b.pairs {
				if p.Account.ID == a.ID {
					delete(b.pairs, t)
				}
			}
			pair := phonePair{Account: a, Password: password, Expires: time.Now().Add(15 * time.Minute)}
			t := backupSecret()
			b.pairs[t] = pair
			link := strings.TrimRight(l.config.SFTP.PublicURL, "/") + "/phone-setup#" + t
			png, e := qrcode.Encode(link, qrcode.Medium, 280)
			if e != nil {
				http.Error(w, "Cannot generate QR", 500)
				return
			}
			writeJSON(w, 201, map[string]any{"url": link, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), "expires": pair.Expires})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/phone-backup/phones/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/phone-backup/phones/")
			b.mu.Lock()
			defer b.mu.Unlock()
			a, ok := b.accounts[id]
			if !ok {
				http.Error(w, "Phone not found", 404)
				return
			}
			delete(b.accounts, id)
			if e := b.saveLocked(); e != nil {
				b.accounts[id] = a
				http.Error(w, "Cannot revoke phone", 500)
				return
			}
			for c, p := range b.connections {
				if p == id {
					c.Close()
				}
			}
			for t, p := range b.pairs {
				if p.Account.ID == id {
					delete(b.pairs, t)
				}
			}
			w.WriteHeader(204)
		default:
			http.Error(w, "Not found", 404)
		}
	})
}
func backupHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Del("Access-Control-Allow-Origin")
}

// Keys and numeric enums follow PhotoSync's published AppConfig specification:
// https://download.photosync-app.com/xml/mdm_specfile.zip (4.9.5).
func photoSyncSettings(c SFTPConfig, p phonePair) ([]byte, error) {
	u, e := url.Parse(c.PublicURL)
	if e != nil {
		return nil, e
	}
	values := map[string]any{
		"1_service_title":                "Home Images - " + p.Account.Name,
		"1_service_type":                 2,
		"1_service_ftptype":              2,
		"1_service_server":               u.Hostname(),
		"1_service_port":                 c.Port,
		"1_service_login":                p.Account.ID,
		"1_service_password":             p.Password,
		"1_service_path":                 "/",
		"1_service_delete":               0,
		"1_service_overwrite":            false,
		"1_service_temporary_write":      false,
		"1_service_autocreate_dirs":      true,
		"1_service_target_select":        false,
		"1_service_create_subdirs":       "%YR/%mR",
		"1_service_filename_type":        0,
		"1_service_photos_wifi":          true,
		"1_service_videos_wifi":          true,
		"1_service_livephoto_wifi":       true,
		"1_service_quality_wifi":         -1,
		"1_service_video_resize_wifi":    -1,
		"1_service_heic_conversion_wifi": 0,
		"1_service_hevc_conversion_wifi": 0,
	}
	return plist.Marshal(values, plist.XMLFormat)
}
