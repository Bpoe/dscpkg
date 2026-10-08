package dscpkg

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	lockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	unlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

func lockPackagesDirectory(packagesDir string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(packagesDir, ".dscpkg.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileEx.Call(uintptr(syscall.Handle(file.Fd())), 2, 0, ^uintptr(0), ^uintptr(0), uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		_ = file.Close()
		return nil, fmt.Errorf("lock packages directory: %w", callErr)
	}
	return func() {
		_, _, _ = unlockFileEx.Call(uintptr(syscall.Handle(file.Fd())), 0, ^uintptr(0), ^uintptr(0), uintptr(unsafe.Pointer(&overlapped)))
		_ = file.Close()
	}, nil
}
