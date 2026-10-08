//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package dscpkg

import (
	"os"
	"path/filepath"
	"syscall"
)

func lockPackagesDirectory(packagesDir string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(packagesDir, ".dscpkg.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
