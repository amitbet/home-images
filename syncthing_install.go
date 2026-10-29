package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const syncthingVersion = "v2.1.0"

// Pinned to the upstream release's signed sha256sum.txt.asc. Never execute a
// download based solely on a checksum fetched alongside that download.
var syncthingDownloads = map[string]struct{ archive, sha256 string }{
	"linux/amd64":   {"syncthing-linux-amd64-v2.1.0.tar.gz", "624c2f3303c9ed7d6f27a98ad9767ab95a7b5e453c3c5e17ce8a60f166e47011"},
	"linux/arm64":   {"syncthing-linux-arm64-v2.1.0.tar.gz", "6740739c46342a7b25104772822ba958b8933ed625b40e859a51dedfb9dfdad2"},
	"darwin/amd64":  {"syncthing-macos-amd64-v2.1.0.zip", "e0479500135dbb25b1705c6884afcc699b7595fec7b6606f92aa59e0f29b91f3"},
	"darwin/arm64":  {"syncthing-macos-arm64-v2.1.0.zip", "813d3196226ee8b3e345fba22885d12b8887e22917f89ddfa744d775a9b96a9d"},
	"windows/amd64": {"syncthing-windows-amd64-v2.1.0.zip", "33da7c8371f4a70dcf7e5f9136d71dbf5ea280d06bb99db0d1e979b14c324deb"},
	"windows/arm64": {"syncthing-windows-arm64-v2.1.0.zip", "34b8182d46674f39fbb86b10906c2b524207af8af76b044e4d603f04a24dfd50"},
}

func syncthingExe() string {
	if runtime.GOOS == "windows" {
		return "syncthing.exe"
	}
	return "syncthing"
}

func installSyncthing(ctx context.Context, base string) (string, error) {
	download, ok := syncthingDownloads[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("phone backup is not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	dir := filepath.Join(base, "bin", syncthingVersion)
	binary := filepath.Join(dir, syncthingExe())
	if info, err := os.Lstat(binary); err == nil && info.Mode().IsRegular() {
		return binary, nil
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	url := "https://github.com/syncthing/syncthing/releases/download/" + syncthingVersion + "/" + download.archive
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download Syncthing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download Syncthing: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64<<20 {
		return "", fmt.Errorf("Syncthing archive exceeds size limit")
	}
	if err = verifySyncthingArchive(data, download.sha256); err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dir), ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err = extractSyncthing(data, strings.HasSuffix(download.archive, ".zip"), stage); err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(stage, syncthingExe())); err != nil {
		return "", fmt.Errorf("Syncthing archive has no executable: %w", err)
	}
	if _, err = os.Stat(filepath.Join(stage, "LICENSE.txt")); err != nil {
		return "", fmt.Errorf("Syncthing archive has no license: %w", err)
	}
	if err = os.Rename(stage, dir); err != nil {
		return "", err
	}
	return binary, nil
}

func verifySyncthingArchive(data []byte, expected string) error {
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != expected {
		return fmt.Errorf("Syncthing download checksum mismatch; nothing was installed")
	}
	return nil
}

// Extract only explicitly named regular files, never archive paths or links.
func extractSyncthing(data []byte, isZip bool, dir string) error {
	write := func(name string, reader io.Reader) error {
		// Upstream also ships service scripts named "syncthing" under etc/.
		// Only direct children of the release directory are runtime assets.
		parts := strings.Split(name, "/")
		if len(parts) != 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
			return nil
		}
		name = parts[1]
		if name != syncthingExe() && name != "LICENSE.txt" && name != "AUTHORS.txt" && name != "README.txt" {
			return nil
		}
		mode := os.FileMode(0600)
		if name == syncthingExe() {
			mode = 0700
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(reader, 128<<20+1))
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if n > 128<<20 {
			return fmt.Errorf("Syncthing archive member exceeds size limit")
		}
		return nil
	}
	if isZip {
		z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		for _, entry := range z.File {
			if !entry.Mode().IsRegular() {
				continue
			}
			r, err := entry.Open()
			if err != nil {
				return err
			}
			err = write(entry.Name, r)
			r.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	t := tar.NewReader(gz)
	for {
		h, err := t.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg {
			if err = write(h.Name, t); err != nil {
				return err
			}
		}
	}
}
