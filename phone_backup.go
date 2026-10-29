package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type backupFolder struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Directory string `json:"directory"`
}
type backupPhone struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Directory string         `json:"directory"`
	Folders   []backupFolder `json:"folders"`
}
type backupSettings struct {
	Enabled bool          `json:"enabled"`
	Port    int           `json:"port"`
	Phones  []backupPhone `json:"phones"`
}
type syncthingConnection struct {
	url, key string
	client   *http.Client
}
type phoneBackup struct {
	lib                         *Library
	base                        string
	opMu                        sync.Mutex // serialize configuration changes and lifecycle operations
	mu                          sync.Mutex
	settings                    backupSettings
	connection                  *syncthingConnection
	process                     *os.Process
	deviceID, status, lastError string
	cancel                      context.CancelFunc
	done                        chan struct{}
	closed                      bool
	importedSequences           map[string]int64 // owned by the single receiver/import worker
}

func newPhoneBackup(l *Library) (*phoneBackup, error) {
	b := &phoneBackup{lib: l, base: filepath.Join(l.config.DataDir, "syncthing"), status: "disabled"}
	data, err := os.ReadFile(filepath.Join(b.base, "home-images.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(data, &b.settings); err != nil {
			return nil, fmt.Errorf("phone backup settings: %w", err)
		}
	}
	_, err = l.db.Exec(`CREATE TABLE IF NOT EXISTS phone_backup_files(source TEXT PRIMARY KEY,signature TEXT NOT NULL,archive TEXT NOT NULL,imported_at TEXT NOT NULL); CREATE INDEX IF NOT EXISTS phone_backup_imported ON phone_backup_files(source,imported_at)`)
	return b, err
}

func privateWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Caller holds mu. Restore the previous settings if persisting a change fails.
func (b *phoneBackup) saveLocked(previous backupSettings) error {
	data, err := json.MarshalIndent(b.settings, "", "  ")
	if err == nil {
		err = os.MkdirAll(b.base, 0700)
	}
	if err == nil {
		err = privateWrite(filepath.Join(b.base, "home-images.json"), data)
	}
	if err != nil {
		b.settings = previous
	}
	return err
}

func (b *phoneBackup) Start() {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !b.settings.Enabled || b.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel, b.done = cancel, make(chan struct{})
	done := b.done
	go func() { defer close(done); b.run(ctx) }()
}

func (b *phoneBackup) setEnabled(enabled bool) error {
	b.opMu.Lock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.opMu.Unlock()
		return fmt.Errorf("phone backup is shutting down")
	}
	previous := b.settings
	b.settings.Enabled = enabled
	err := b.saveLocked(previous)
	cancel, done := b.cancel, b.done
	b.mu.Unlock()
	if err == nil && !enabled && cancel != nil {
		cancel()
		<-done
		b.mu.Lock()
		b.cancel = nil
		b.status = "disabled"
		b.lastError = ""
		b.mu.Unlock()
	}
	b.opMu.Unlock()
	if err == nil && enabled {
		b.Start()
	}
	return err
}

func (b *phoneBackup) Close() {
	b.opMu.Lock()
	defer b.opMu.Unlock()
	b.mu.Lock()
	b.closed = true
	cancel, done := b.cancel, b.done
	b.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (b *phoneBackup) setStatus(status string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = status
	b.lastError = ""
	if err != nil {
		b.lastError = err.Error()
		log.Printf("Phone backup: %v", err)
	}
}

func (b *phoneBackup) run(ctx context.Context) {
	defer func() { b.mu.Lock(); b.connection = nil; b.mu.Unlock() }()
	for ctx.Err() == nil {
		b.setStatus("starting", nil)
		err := b.serve(ctx)
		b.mu.Lock()
		b.connection = nil
		b.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		b.setStatus("retrying", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func managedSyncthingCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	// Never inherit another installation's paths, credentials or debug settings.
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "ST") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "STMONITORED=yes", "STNOUPGRADE=1")
	cmd.Stdout, cmd.Stderr = log.Writer(), log.Writer()
	configureSyncthingProcess(cmd)
	return cmd
}

// Keep all of Syncthing's generated defaults except the options we own.
type syncthingXML struct {
	XMLName  xml.Name
	Attrs    []xml.Attr     `xml:",any,attr"`
	Text     string         `xml:",chardata"`
	Children []syncthingXML `xml:",any"`
}

func (n *syncthingXML) child(name string) *syncthingXML {
	for i := range n.Children {
		if n.Children[i].XMLName.Local == name {
			return &n.Children[i]
		}
	}
	n.Children = append(n.Children, syncthingXML{XMLName: xml.Name{Local: name}})
	return &n.Children[len(n.Children)-1]
}
func (n *syncthingXML) set(name, value string) { c := n.child(name); c.Text = value; c.Children = nil }
func (n *syncthingXML) attribute(name, value string) {
	for i := range n.Attrs {
		if n.Attrs[i].Name.Local == name {
			n.Attrs[i].Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}

func configureSyncthingXML(data []byte, address, key string, port int) ([]byte, error) {
	var root syncthingXML
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root.XMLName.Local != "configuration" {
		return nil, fmt.Errorf("invalid managed Syncthing configuration")
	}
	gui := root.child("gui")
	gui.attribute("enabled", "true")
	gui.attribute("tls", "false")
	gui.set("address", address)
	gui.set("apikey", key)
	opts := root.child("options")
	// Replace every listen address, including a generated relay/QUIC default.
	children := opts.Children[:0]
	for _, c := range opts.Children {
		if c.XMLName.Local != "listenAddress" {
			children = append(children, c)
		}
	}
	opts.Children = children
	opts.set("listenAddress", fmt.Sprintf("tcp://0.0.0.0:%d", port))
	opts.Children = append(opts.Children, syncthingXML{XMLName: xml.Name{Local: "listenAddress"}, Text: "dynamic+https://relays.syncthing.net/endpoint"})
	opts.set("startBrowser", "false")
	opts.set("autoUpgradeIntervalH", "0")
	opts.set("crashReportingEnabled", "false")
	opts.set("urAccepted", "-1")
	return xml.MarshalIndent(root, "", "  ")
}

func availableSyncPort(preferred int) (int, error) {
	if preferred < 1024 || preferred > 65535 {
		preferred = 22001
	}
	l, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", preferred))
	if err != nil {
		l, err = net.Listen("tcp4", "0.0.0.0:0")
	}
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func (b *phoneBackup) serve(ctx context.Context) error {
	if err := b.prepareArchive(); err != nil {
		return err
	}
	binary, err := installSyncthing(ctx, b.base)
	if err != nil {
		return err
	}
	home := filepath.Join(b.base, "instance")
	if err = os.MkdirAll(home, 0700); err != nil {
		return err
	}
	config := filepath.Join(home, "config.xml")
	if _, err = os.Stat(config); errors.Is(err, os.ErrNotExist) {
		generateCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = managedSyncthingCommand(generateCtx, binary, "generate", "--home="+home, "--no-port-probing").Run()
		cancel()
		if err != nil {
			return fmt.Errorf("initialize managed Syncthing: %w", err)
		}
	}
	b.mu.Lock()
	preferred := b.settings.Port
	b.mu.Unlock()
	port, err := availableSyncPort(preferred)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := l.Addr().String()
	l.Close()
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	key := hex.EncodeToString(random[:])
	data, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	data, err = configureSyncthingXML(data, address, key, port)
	if err != nil {
		return err
	}
	if err = privateWrite(config, data); err != nil {
		return err
	}
	b.mu.Lock()
	previous := b.settings
	b.settings.Port = port
	err = b.saveLocked(previous)
	b.mu.Unlock()
	if err != nil {
		return err
	}
	connection := &syncthingConnection{url: "http://" + address, key: key, client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	defer connection.client.CloseIdleConnections()
	// The supervisor owns restart and shutdown; STMONITORED avoids Syncthing's
	// additional monitor process. No system-wide service or executable is changed.
	childCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := managedSyncthingCommand(childCtx, binary, "serve", "--home="+home, "--no-browser", "--no-restart", "--no-upgrade", "--no-port-probing")
	if err = cmd.Start(); err != nil {
		return err
	}
	b.mu.Lock()
	b.process = cmd.Process
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.process = nil; b.mu.Unlock() }()
	cleanup, err := attachSyncthingProcess(cmd)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return err
	}
	defer cleanup()
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(done) }()
	defer func() {
		select {
		case <-done:
			return
		default:
		}
		shutdownCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		_ = connection.call(shutdownCtx, "POST", "/rest/system/shutdown", nil, nil)
		stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cancel()
			<-done
		}
	}()
	startup := time.NewTimer(30 * time.Second)
	defer startup.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		var status struct {
			MyID string `json:"myID"`
		}
		if err = connection.call(ctx, "GET", "/rest/system/status", nil, &status); err == nil && status.MyID != "" {
			b.mu.Lock()
			b.connection = connection
			b.deviceID = status.MyID
			b.mu.Unlock()
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return fmt.Errorf("Syncthing exited during startup: %v", exitErr)
		case <-startup.C:
			return fmt.Errorf("Syncthing API did not become ready: %w", err)
		case <-tick.C:
		}
	}
	b.setStatus("running", nil)
	b.importedSequences = make(map[string]int64)
	imports := time.NewTicker(5 * time.Second)
	defer imports.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return fmt.Errorf("managed Syncthing exited: %v", exitErr)
		case <-imports.C:
			if err = b.importFolders(ctx); ctx.Err() == nil {
				b.setStatus("running", err)
			}
		}
	}
}

func (c *syncthingConnection) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if method == "DELETE" && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Syncthing %s %s: HTTP %d", method, strings.Split(path, "?")[0], resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<20))
	return err
}
func (b *phoneBackup) call(ctx context.Context, method, path string, body, out any) error {
	b.mu.Lock()
	c := b.connection
	b.mu.Unlock()
	if c == nil {
		return bad(503, "Phone backup receiver is not ready")
	}
	return c.call(ctx, method, path, body, out)
}

func folderKey(id string) string { sum := sha256String(id); return sum[:24] }
func sha256String(value string) string { // also used for safe, stable per-device directories
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
