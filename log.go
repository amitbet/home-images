package main

import (
	"os"
	"sync"
)

type rotatingLog struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	max, size int64
}

func newRotatingLog(path string, max int64) (*rotatingLog, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if e != nil {
		return nil, e
	}
	s, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	return &rotatingLog{file: f, path: path, max: max, size: s.Size()}, nil
}
func (l *rotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(b)) > l.max {
		if e := l.file.Close(); e != nil {
			return 0, e
		}
		_ = os.Remove(l.path + ".1")
		if e := os.Rename(l.path, l.path+".1"); e != nil {
			return 0, e
		}
		f, e := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
		if e != nil {
			return 0, e
		}
		l.file = f
		l.size = 0
	}
	n, e := l.file.Write(b)
	l.size += int64(n)
	return n, e
}
func (l *rotatingLog) Close() error { l.mu.Lock(); defer l.mu.Unlock(); return l.file.Close() }
