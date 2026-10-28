//go:build windows && (amd64 || arm64)

package main

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type turboSize = uint32
type turboJPEG struct {
	dll  *windows.DLL
	proc map[string]*windows.Proc
}

func turboLibraryName() string { return "turbojpeg.dll" }
func loadTurboJPEG(path string) (*turboJPEG, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("TurboJPEG path must be absolute")
	}
	handle, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return nil, err
	}
	lib := &turboJPEG{dll: &windows.DLL{Name: path, Handle: handle}, proc: map[string]*windows.Proc{}}
	for _, name := range turboSymbols {
		p, err := lib.dll.FindProc(name)
		if err != nil {
			lib.close()
			return nil, err
		}
		lib.proc[name] = p
	}
	return lib, nil
}
func (lib *turboJPEG) close() error { return lib.dll.Release() }

//go:uintptrescapes
func (lib *turboJPEG) call(name string, args ...uintptr) uintptr {
	result, _, _ := lib.proc[name].Call(args...)
	return result
}
