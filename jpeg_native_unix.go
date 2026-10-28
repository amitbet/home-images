//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/ebitengine/purego"
)

type turboSize = uint64
type turboJPEG struct {
	handle uintptr
	proc   map[string]uintptr
}

func turboLibraryName() string {
	if runtime.GOOS == "darwin" {
		return "libturbojpeg.dylib"
	}
	return "libturbojpeg.so"
}
func loadTurboJPEG(path string) (*turboJPEG, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("TurboJPEG path must be absolute")
	}
	handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	lib := &turboJPEG{handle: handle, proc: map[string]uintptr{}}
	for _, name := range turboSymbols {
		pointer, err := purego.Dlsym(handle, name)
		if err != nil {
			lib.close()
			return nil, err
		}
		lib.proc[name] = pointer
	}
	return lib, nil
}
func (lib *turboJPEG) close() error { return purego.Dlclose(lib.handle) }

//go:uintptrescapes
func (lib *turboJPEG) call(name string, args ...uintptr) uintptr {
	result, _, _ := purego.SyscallN(lib.proc[name], args...)
	return result
}
