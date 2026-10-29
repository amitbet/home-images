package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

type pendingBackupDevice struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}
type pendingBackupFolder struct {
	OfferedBy map[string]struct {
		Label            string `json:"label"`
		ReceiveEncrypted bool   `json:"receiveEncrypted"`
		RemoteEncrypted  bool   `json:"remoteEncrypted"`
	} `json:"offeredBy"`
}

func (b *phoneBackup) routes(mux *http.ServeMux, wrap func(endpoint) http.HandlerFunc) {
	page := wrap(func(w http.ResponseWriter, r *http.Request) error {
		data, err := webFiles.ReadFile("web/phone-backup.html")
		if err != nil {
			return err
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "phone-backup.html", time.Time{}, bytes.NewReader(data))
		return nil
	})
	for _, path := range []string{"/settings", "/phone-setup", "/phone-backup"} {
		mux.HandleFunc(path, page)
	}
	mux.HandleFunc("/api/phone-backup/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Del("Access-Control-Allow-Origin")
		err := b.handle(w, r)
		if err != nil {
			code, msg := 500, "Phone backup request failed"
			if e, ok := err.(*apiError); ok {
				code, msg = e.code, e.text
			} else {
				msg = err.Error()
			}
			_ = writeJSON(w, code, map[string]string{"detail": msg})
		}
	})
}

func (b *phoneBackup) handle(w http.ResponseWriter, r *http.Request) error {
	action := strings.TrimPrefix(r.URL.Path, "/api/phone-backup/")
	if action == "qr" && r.Method == "GET" {
		b.mu.Lock()
		id, ready := b.deviceID, b.connection != nil
		b.mu.Unlock()
		if !ready || id == "" {
			return bad(503, "Phone backup receiver is not ready")
		}
		png, err := qrcode.Encode(id, qrcode.Medium, 320)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "image/png")
		_, err = w.Write(png)
		return err
	}
	if r.Header.Get("X-Home-Images") != "1" {
		return bad(403, "Missing request header")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if err != nil || u.Host != r.Host || u.Scheme != scheme {
			return bad(403, "Same-origin request required")
		}
	}
	if action == "status" && r.Method == "GET" {
		return b.backupStatus(w, r)
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		return bad(405, "Use POST")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		Enabled bool   `json:"enabled"`
		Device  string `json:"device"`
		Name    string `json:"name"`
		Folder  string `json:"folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return bad(400, "Invalid request")
	}
	if action == "enable" {
		if err := b.setEnabled(input.Enabled); err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]bool{"enabled": input.Enabled})
	}
	b.opMu.Lock()
	defer b.opMu.Unlock()
	switch action {
	case "accept-phone":
		return b.acceptPhone(w, r, input.Device, input.Name)
	case "accept-folder":
		return b.acceptFolder(w, r, input.Device, input.Folder)
	case "remove-phone":
		return b.removePhone(w, r, input.Device)
	default:
		return bad(404, "Not found")
	}
}

func (b *phoneBackup) backupStatus(w http.ResponseWriter, r *http.Request) error {
	b.mu.Lock()
	settings := b.settings
	status, lastError, id, ready := b.status, b.lastError, b.deviceID, b.connection != nil
	b.mu.Unlock()
	result := map[string]any{"enabled": settings.Enabled, "status": status, "last_error": lastError, "device_id": id, "ready": ready, "port": settings.Port, "version": syncthingVersion, "phones": []any{}, "pending_devices": map[string]pendingBackupDevice{}, "pending_folders": map[string]pendingBackupFolder{}}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var connections struct {
		Connections map[string]struct {
			Connected bool `json:"connected"`
		} `json:"connections"`
	}
	if ready {
		var pending map[string]pendingBackupDevice
		var folders map[string]pendingBackupFolder
		for _, call := range []struct {
			path string
			out  any
		}{{"/rest/cluster/pending/devices", &pending}, {"/rest/cluster/pending/folders", &folders}, {"/rest/system/connections", &connections}} {
			if err := b.call(ctx, "GET", call.path, nil, call.out); err != nil {
				result["last_error"] = err.Error()
				break
			}
		}
		// Only offers from explicitly approved phones appear in our setup page.
		known := map[string]bool{}
		for _, phone := range settings.Phones {
			known[phone.ID] = true
		}
		for device := range pending {
			if known[device] {
				delete(pending, device)
			}
		}
		for folder, offer := range folders {
			for device := range offer.OfferedBy {
				if !known[device] {
					delete(offer.OfferedBy, device)
				}
			}
			if len(offer.OfferedBy) == 0 {
				delete(folders, folder)
			}
		}
		result["pending_devices"], result["pending_folders"] = pending, folders
	}
	phones := []any{}
	for _, phone := range settings.Phones {
		var imported int
		var last string
		if err := b.lib.db.QueryRowContext(ctx, `SELECT count(*),coalesce(max(imported_at),'') FROM phone_backup_files WHERE source>=? AND source<?`, phone.ID+"/", phone.ID+"0").Scan(&imported, &last); err != nil {
			return err
		}
		folders := []any{}
		for _, folder := range phone.Folders {
			var state struct {
				State       string `json:"state"`
				NeedBytes   int64  `json:"needBytes"`
				GlobalBytes int64  `json:"globalBytes"`
				PullErrors  int    `json:"pullErrors"`
			}
			if ready {
				if err := b.call(ctx, "GET", "/rest/db/status?folder="+url.QueryEscape(folder.ID), nil, &state); err != nil {
					result["last_error"] = err.Error()
				}
			}
			folders = append(folders, map[string]any{"id": folder.ID, "label": folder.Label, "state": state.State, "need_bytes": state.NeedBytes, "global_bytes": state.GlobalBytes, "errors": state.PullErrors})
		}
		phones = append(phones, map[string]any{"id": phone.ID, "name": phone.Name, "connected": connections.Connections[phone.ID].Connected, "folders": folders, "imported": imported, "last_backup": last, "directory": filepath.ToSlash(filepath.Join("Phone backups", phone.Directory))})
	}
	result["phones"] = phones
	return writeJSON(w, 200, result)
}

func (b *phoneBackup) acceptPhone(w http.ResponseWriter, r *http.Request, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return bad(400, "Enter a phone name (up to 80 characters)")
	}
	b.mu.Lock()
	previous := b.settings
	b.mu.Unlock()
	if len(previous.Phones) >= 32 {
		return bad(409, "Phone limit reached")
	}
	for _, p := range previous.Phones {
		if p.ID == id {
			return bad(409, "Phone is already approved")
		}
	}
	var pending map[string]pendingBackupDevice
	if err := b.call(r.Context(), "GET", "/rest/cluster/pending/devices", nil, &pending); err != nil {
		return err
	}
	if _, ok := pending[id]; !ok || id == "" {
		return bad(409, "This phone is no longer waiting for approval")
	}
	var device map[string]any
	if err := b.call(r.Context(), "GET", "/rest/config/defaults/device", nil, &device); err != nil {
		return err
	}
	device["deviceID"], device["name"], device["addresses"] = id, name, []string{"dynamic"}
	device["introducer"], device["autoAcceptFolders"], device["paused"] = false, false, false
	if err := b.call(r.Context(), "POST", "/rest/config/devices", device, nil); err != nil {
		return err
	}
	phone := backupPhone{ID: id, Name: name, Directory: backupName(name) + "-" + folderKey(id)[:8], Folders: []backupFolder{}}
	b.mu.Lock()
	b.settings.Phones = append(append([]backupPhone(nil), previous.Phones...), phone)
	err := b.saveLocked(previous)
	b.mu.Unlock()
	if err != nil {
		_ = b.call(r.Context(), "DELETE", "/rest/config/devices/"+url.PathEscape(id), nil, nil)
		return err
	}
	return writeJSON(w, 200, phone)
}

func (b *phoneBackup) acceptFolder(w http.ResponseWriter, r *http.Request, device, id string) error {
	if id == "" || len(id) > 256 {
		return bad(400, "Invalid folder")
	}
	b.mu.Lock()
	previous := b.settings
	b.mu.Unlock()
	index := -1
	for i, p := range previous.Phones {
		if p.ID == device {
			index = i
		}
		for _, f := range p.Folders {
			if f.ID == id {
				return bad(409, "Folder is already configured")
			}
		}
	}
	if index < 0 {
		return bad(404, "Approve the phone first")
	}
	if len(previous.Phones[index].Folders) >= 32 {
		return bad(409, "Folder limit reached")
	}
	var pending map[string]pendingBackupFolder
	if err := b.call(r.Context(), "GET", "/rest/cluster/pending/folders", nil, &pending); err != nil {
		return err
	}
	offer, ok := pending[id].OfferedBy[device]
	if !ok {
		return bad(409, "This folder is no longer waiting for approval")
	}
	if offer.ReceiveEncrypted || offer.RemoteEncrypted {
		return bad(400, "Share the folder without an encryption password")
	}
	folder := backupFolder{ID: id, Label: offer.Label, Directory: folderKey(device + "/" + id)}
	incoming := filepath.Join(b.base, "incoming", folder.Directory)
	if err := os.MkdirAll(incoming, 0700); err != nil {
		return err
	}
	var config map[string]any
	if err := b.call(r.Context(), "GET", "/rest/config/defaults/folder", nil, &config); err != nil {
		return err
	}
	b.mu.Lock()
	self := b.deviceID
	b.mu.Unlock()
	config["id"], config["label"], config["path"] = id, offer.Label, incoming
	config["type"], config["paused"], config["rescanIntervalS"] = "receiveonly", false, 300
	config["devices"] = []map[string]string{{"deviceID": self}, {"deviceID": device}}
	if err := b.call(r.Context(), "POST", "/rest/config/folders", config, nil); err != nil {
		return err
	}
	phones := append([]backupPhone(nil), previous.Phones...)
	phones[index].Folders = append(append([]backupFolder(nil), phones[index].Folders...), folder)
	b.mu.Lock()
	b.settings.Phones = phones
	err := b.saveLocked(previous)
	b.mu.Unlock()
	if err != nil {
		_ = b.call(r.Context(), "DELETE", "/rest/config/folders/"+url.PathEscape(id), nil, nil)
		return err
	}
	return writeJSON(w, 200, folder)
}

func (b *phoneBackup) removePhone(w http.ResponseWriter, r *http.Request, id string) error {
	b.mu.Lock()
	previous := b.settings
	b.mu.Unlock()
	phones := []backupPhone{}
	found := false
	for _, p := range previous.Phones {
		if p.ID != id {
			phones = append(phones, p)
			continue
		}
		found = true
		for _, f := range p.Folders {
			if err := b.call(r.Context(), "DELETE", "/rest/config/folders/"+url.PathEscape(f.ID), nil, nil); err != nil {
				return err
			}
		}
		if err := b.call(r.Context(), "DELETE", "/rest/config/devices/"+url.PathEscape(id), nil, nil); err != nil {
			return err
		}
	}
	if !found {
		return bad(404, "Phone not found")
	}
	b.mu.Lock()
	b.settings.Phones = phones
	err := b.saveLocked(previous)
	b.mu.Unlock()
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]string{"detail": "Phone removed. Backed-up photos are preserved."})
}
