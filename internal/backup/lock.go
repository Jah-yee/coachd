package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockData takes the data-directory lock (<dataDir>/coachd.lock) that serve
// holds while it runs. It returns ErrLocked when another process holds it.
func LockData(dataDir string) (release func(), err error) {
	return flock(filepath.Join(dataDir, "coachd.lock"), ErrLocked)
}

// lockBackups takes the lock in the backups directory dir that Run and
// Restore hold. It returns ErrBusy when another process holds it.
func lockBackups(dir string) (release func(), err error) {
	return flock(filepath.Join(dir, ".lock"), ErrBusy)
}

// flock takes an exclusive, non-blocking flock on path (created with mode
// 0600, never deleted) and returns held if another open file holds it. The
// lock lasts until release closes the file. flock locks belong to the open
// file, so two opens in one process exclude each other too.
func flock(path string, held error) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("backup: lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, held
		}
		return nil, fmt.Errorf("backup: lock: %w", err)
	}
	return func() { f.Close() }, nil
}
