package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

type SFTPConfig struct {
	Enabled      bool   `json:"enabled"`
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	PublicURL    string `json:"public_url,omitempty"`
	MaxFileBytes int64  `json:"max_file_bytes,omitempty"`
}

func (c *SFTPConfig) validate() error {
	if c.Port == 0 {
		c.Port = 2222
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.MaxFileBytes == 0 {
		c.MaxFileBytes = 20 << 30
	}
	if !c.Enabled {
		return nil
	}
	if c.Port < 1 || c.Port > 65535 || net.ParseIP(c.Host) == nil || c.MaxFileBytes < 1 {
		return fmt.Errorf("sftp requires an IP host, valid port, positive max_file_bytes")
	}
	return validateBackupURL(c.PublicURL)
}

type phoneAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Hash string `json:"hash"`
}
type phonePair struct {
	Account  phoneAccount
	Password string
	Expires  time.Time
}
type phoneBackup struct {
	lib         *Library
	listener    net.Listener
	signer      ssh.Signer
	mu          sync.Mutex
	accounts    map[string]phoneAccount
	pairs       map[string]phonePair
	connections map[net.Conn]string
	wg          sync.WaitGroup
	closed      bool
	scan        chan struct{}
	done        chan struct{}
}

func backupSecret() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func startPhoneBackup(l *Library) (*phoneBackup, error) {
	b := &phoneBackup{lib: l, accounts: map[string]phoneAccount{}, pairs: map[string]phonePair{}, connections: map[net.Conn]string{}, scan: make(chan struct{}, 1), done: make(chan struct{})}
	keyPath := filepath.Join(l.config.DataDir, "sftp-host-key.pem")
	data, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		block, e := ssh.MarshalPrivateKey(key, "Home Images")
		if e != nil {
			return nil, e
		}
		data = pem.EncodeToMemory(block)
		f, e := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(data)
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
	} else if err != nil {
		return nil, err
	}
	b.signer, err = ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, err
	}
	data, err = os.ReadFile(filepath.Join(l.config.DataDir, "phones.json"))
	if err == nil {
		if err = json.Unmarshal(data, &b.accounts); err != nil {
			return nil, err
		}
		if b.accounts == nil {
			return nil, fmt.Errorf("invalid phones.json")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = l.root.MkdirAll("Phone uploads", 0750); err != nil {
		return nil, err
	}
	b.listener, err = net.Listen("tcp", net.JoinHostPort(l.config.SFTP.Host, fmt.Sprint(l.config.SFTP.Port)))
	if err != nil {
		return nil, err
	}
	b.wg.Add(2)
	go b.accept()
	go func() {
		defer b.wg.Done()
		ctx := backupContext(b.done)
		for {
			select {
			case <-b.done:
				return
			case <-b.scan:
				select {
				case <-b.done:
					return
				case <-time.After(2 * time.Second):
				}
				l.Scan(ctx)
			}
		}
	}()
	return b, nil
}
func (b *phoneBackup) saveLocked() error {
	data, err := json.MarshalIndent(b.accounts, "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(b.lib.config.DataDir, "phones.json")
	tmp := file + ".tmp"
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
func (b *phoneBackup) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.done)
	b.listener.Close()
	for c := range b.connections {
		c.Close()
	}
	b.mu.Unlock()
	b.wg.Wait()
}
func (b *phoneBackup) accept() {
	defer b.wg.Done()
	for {
		c, err := b.listener.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed || len(b.connections) >= 16 {
			b.mu.Unlock()
			c.Close()
			continue
		}
		b.connections[c] = ""
		b.wg.Add(1)
		b.mu.Unlock()
		go func() {
			defer b.wg.Done()
			defer c.Close()
			defer func() { b.mu.Lock(); delete(b.connections, c); b.mu.Unlock() }()
			b.serve(c)
		}()
	}
}
func (b *phoneBackup) serve(c net.Conn) {
	c.SetDeadline(time.Now().Add(20 * time.Second))
	cfg := &ssh.ServerConfig{MaxAuthTries: 3, PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		b.mu.Lock()
		a, ok := b.accounts[meta.User()]
		b.mu.Unlock()
		if !ok || bcrypt.CompareHashAndPassword([]byte(a.Hash), password) != nil {
			return nil, fmt.Errorf("invalid credentials")
		}
		return &ssh.Permissions{Extensions: map[string]string{"phone": a.ID, "hash": a.Hash}}, nil
	}}
	cfg.AddHostKey(b.signer)
	conn, channels, requests, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	b.mu.Lock()
	a, ok := b.accounts[conn.User()]
	ok = ok && conn.Permissions.Extensions["hash"] == a.Hash
	if ok {
		b.connections[c] = conn.User()
	}
	b.mu.Unlock()
	if !ok {
		return
	}
	c.SetDeadline(time.Now().Add(24 * time.Hour))
	go ssh.DiscardRequests(requests)
	// Exactly one SFTP subsystem; never allow shells, forwarding, exec, or other channels.
	for ch := range channels {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "SFTP only")
			continue
		}
		channel, reqs, e := ch.Accept()
		if e != nil {
			return
		}
		for req := range reqs {
			var payload struct{ Name string }
			valid := req.Type == "subsystem" && ssh.Unmarshal(req.Payload, &payload) == nil && payload.Name == "sftp"
			req.Reply(valid, nil)
			if !valid {
				continue
			}
			go ssh.DiscardRequests(reqs)
			go func() {
				for extra := range channels {
					extra.Reject(ssh.Prohibited, "One SFTP session per connection")
				}
			}()
			root, e := b.lib.root.OpenRoot(filepath.Join("Phone uploads", conn.User()))
			if e != nil {
				channel.Close()
				return
			}
			h := &phoneFS{root: root, backup: b}
			srv := sftp.NewRequestServer(channel, sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h})
			srv.Serve()
			srv.Close()
			root.Close()
			channel.Close()
			return
		}
		channel.Close()
	}
}

type phoneFS struct {
	root   *os.Root
	backup *phoneBackup
}

func phonePath(p string) (string, error) {
	if strings.ContainsAny(p, "\\:\x00") {
		return "", os.ErrPermission
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ".", nil
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.HasPrefix(part, ".") || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return "", os.ErrPermission
		}
	}
	return filepath.FromSlash(p), nil
}
func (h *phoneFS) Fileread(*sftp.Request) (io.ReaderAt, error) { return nil, os.ErrPermission }
func (h *phoneFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	p, e := phonePath(r.Filepath)
	if e != nil || p == "." {
		return nil, os.ErrPermission
	}
	flags := r.Pflags()
	if !flags.Creat || !flags.Write || flags.Append {
		return nil, os.ErrPermission
	}
	if _, e = h.root.Lstat(p); !errors.Is(e, os.ErrNotExist) {
		if e == nil {
			e = os.ErrExist
		}
		return nil, e
	}
	tmp := filepath.Join(filepath.Dir(p), ".upload-"+backupSecret()+".part")
	f, e := h.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	return &phoneUpload{file: f, fs: h, tmp: tmp, target: p}, nil
}

type phoneUpload struct {
	mu          sync.Mutex
	file        *os.File
	fs          *phoneFS
	tmp, target string
	failed      bool
}

func (h *phoneFS) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	w, err := h.Filewrite(r)
	if err != nil {
		return nil, err
	}
	return w.(*phoneUpload), nil
}
func (u *phoneUpload) ReadAt([]byte, int64) (int, error) { return 0, os.ErrPermission }

func (u *phoneUpload) WriteAt(p []byte, off int64) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if off < 0 || off > u.fs.backup.lib.config.SFTP.MaxFileBytes-int64(len(p)) {
		u.failed = true
		return 0, fmt.Errorf("file exceeds upload limit")
	}
	n, e := u.file.WriteAt(p, off)
	if e != nil {
		u.failed = true
	}
	return n, e
}
func (u *phoneUpload) TransferError(error) { u.mu.Lock(); u.failed = true; u.mu.Unlock() }
func (u *phoneUpload) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	defer u.fs.root.Remove(u.tmp)
	e := u.file.Sync()
	ce := u.file.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if u.failed {
		return fmt.Errorf("incomplete upload")
	}
	// Hard-link publication is atomic and refuses to replace an existing original.
	if e = u.fs.root.Link(u.tmp, u.target); e != nil {
		return e
	}
	select {
	case u.fs.backup.scan <- struct{}{}:
	default:
	}
	return nil
}
func (h *phoneFS) Filecmd(r *sftp.Request) error {
	p, e := phonePath(r.Filepath)
	if e != nil {
		return e
	}
	if r.Method == "Mkdir" {
		return h.root.Mkdir(p, 0750)
	}
	// PhotoSync may preserve timestamps after upload. Ignore this harmless request;
	// original capture dates remain in EXIF. No deletion, rename, links or truncation.
	if r.Method == "Setstat" && r.AttrFlags().Acmodtime && !r.AttrFlags().Size && !r.AttrFlags().Permissions && !r.AttrFlags().UidGid {
		return nil
	}
	return os.ErrPermission
}

type phoneList []os.FileInfo

func (l phoneList) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off < 0 {
		return 0, os.ErrInvalid
	}
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if n < len(out) {
		return n, io.EOF
	}
	return n, nil
}
func (h *phoneFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	p, e := phonePath(r.Filepath)
	if e != nil {
		return nil, e
	}
	switch r.Method {
	case "Stat", "Lstat":
		fi, e := h.root.Lstat(p)
		if e != nil {
			return nil, e
		}
		return phoneList{fi}, nil
	case "List":
		f, e := h.root.Open(p)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		entries, e := f.Readdir(-1)
		if e != nil {
			return nil, e
		}
		out := phoneList{}
		for _, fi := range entries {
			if !strings.HasPrefix(fi.Name(), ".") && fi.Mode()&os.ModeSymlink == 0 {
				out = append(out, fi)
			}
		}
		return out, nil
	}
	return nil, os.ErrPermission
}

// Force Realpath to remain in the phone's virtual root.
func (h *phoneFS) RealPath(p string) (string, error) {
	n, e := phonePath(p)
	if e != nil {
		return "", e
	}
	return path.Join("/", filepath.ToSlash(n)), nil
}
