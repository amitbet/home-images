//go:build windows && (amd64 || arm64)

package cliptext

import "golang.org/x/sys/windows"

func LibraryName() string { return "onnxruntime.dll" }
func openLibrary(path string) (uintptr, error) {
	h, e := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	return uintptr(h), e
}
func lookupLibrary(h uintptr, name string) (uintptr, error) {
	return windows.GetProcAddress(windows.Handle(h), name)
}
func closeLibrary(h uintptr)     { _ = windows.FreeLibrary(windows.Handle(h)) }
func nativePath(path string) any { p, _ := windows.UTF16FromString(path); return p }
