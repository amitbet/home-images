//go:build (linux || darwin) && (amd64 || arm64)

package cliptext

import (
	"github.com/ebitengine/purego"
	"runtime"
)

func LibraryName() string {
	if runtime.GOOS == "darwin" {
		return "libonnxruntime.dylib"
	}
	return "libonnxruntime.so"
}
func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}
func lookupLibrary(h uintptr, name string) (uintptr, error) { return purego.Dlsym(h, name) }
func closeLibrary(h uintptr)                                { _ = purego.Dlclose(h) }
func nativePath(path string) any                            { return append([]byte(path), 0) }
