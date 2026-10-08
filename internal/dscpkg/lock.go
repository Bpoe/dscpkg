package dscpkg

import "os"

func withPackagesLock(packagesDir string, operation func() error) error {
	if err := os.MkdirAll(packagesDir, 0o755); err != nil {
		return err
	}
	unlock, err := lockPackagesDirectory(packagesDir)
	if err != nil {
		return err
	}
	defer unlock()
	return operation()
}
