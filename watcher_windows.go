//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"log"
	"path/filepath"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (l *Library) watchChanges(ctx context.Context) {
	changes := make(chan string, 8192)
	rescan := make(chan struct{}, 1)
	scanRunning := make(chan struct{}, 1)
	signalScan := func() {
		select {
		case rescan <- struct{}{}:
		default:
		}
	}
	readerDone := make(chan struct{})
	go func() { defer close(readerDone); l.readDirectoryChanges(ctx, changes, signalScan) }()
	defer func() { <-readerDone }()
	pending := map[string]time.Time{}
	retries := map[string]int{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case path := <-changes:
			if validWatchPath(path) {
				pending[path] = time.Now().Add(3 * time.Second)
				retries[path] = 0
			}
		case <-rescan:
			log.Print("Filesystem watcher lost events; scheduling reconciliation")
			select {
			case scanRunning <- struct{}{}:
				go func() {
					defer func() { <-scanRunning }()
					l.scanRunMu.Lock()
					defer l.scanRunMu.Unlock()
					if ctx.Err() == nil {
						l.scanLocked(ctx)
					}
				}()
			default:
			}

		case now := <-ticker.C:
			for path, due := range pending {
				if now.Before(due) {
					continue
				}
				delete(pending, path)
				if err := l.reconcileChangedPath(ctx, path); err != nil && ctx.Err() == nil {
					retries[path]++
					if retries[path] <= 3 {
						log.Printf("Filesystem watcher %s: %v; retrying", path, err)
						pending[path] = now.Add(30 * time.Second)
					} else {
						log.Printf("Filesystem watcher %s: %v; will retry on next scan", path, err)
						delete(retries, path)
					}
				}
			}
		}
	}
}

func (l *Library) readDirectoryChanges(ctx context.Context, changes chan<- string, rescan func()) {
	for ctx.Err() == nil {
		if err := l.readDirectoryChangesOnce(ctx, changes, rescan); err != nil && ctx.Err() == nil {
			log.Printf("Filesystem watcher stopped: %v; retrying", err)
			rescan()
			if !waitContext(ctx, 10*time.Second) {
				return
			}
		}
	}
}
func (l *Library) readDirectoryChangesOnce(ctx context.Context, changes chan<- string, rescan func()) error {
	root, err := windows.UTF16PtrFromString(l.rootPath)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(root, windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	buf := make([]byte, 64*1024)
	log.Print("Filesystem watcher started for photo library")
	for ctx.Err() == nil {
		var ov windows.Overlapped
		ov.HEvent = event
		windows.ResetEvent(event)
		err = windows.ReadDirectoryChanges(handle, &buf[0], uint32(len(buf)), true, windows.FILE_NOTIFY_CHANGE_FILE_NAME|windows.FILE_NOTIFY_CHANGE_DIR_NAME|windows.FILE_NOTIFY_CHANGE_LAST_WRITE|windows.FILE_NOTIFY_CHANGE_SIZE, nil, &ov, 0)
		if err != nil {
			return err
		}
		for {
			result, e := windows.WaitForSingleObject(event, 1000)
			if e != nil {
				windows.CancelIoEx(handle, &ov)
				return e
			}
			if result == windows.WAIT_OBJECT_0 {
				break
			}
			if ctx.Err() != nil {
				windows.CancelIoEx(handle, &ov)
				windows.WaitForSingleObject(event, windows.INFINITE)
				return nil
			}
		}
		var n uint32
		if err = windows.GetOverlappedResult(handle, &ov, &n, false); err != nil {
			return err
		}
		if n == 0 {
			rescan()
			continue
		}
		for offset := uint32(0); offset+12 <= n; {
			next := binary.LittleEndian.Uint32(buf[offset:])
			size := binary.LittleEndian.Uint32(buf[offset+8:])
			if size%2 != 0 || offset+12+size > n {
				rescan()
				break
			}
			raw := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[offset+12])), int(size/2))
			path := filepath.ToSlash(string(utf16.Decode(raw)))
			select {
			case changes <- path:
			default:
				rescan()
			}
			if next == 0 {
				break
			}
			if next < 12 || offset+next >= n {
				rescan()
				break
			}
			offset += next
		}
	}
	return nil
}
