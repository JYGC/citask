package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ExclusivePollFileLock uses flock because the kernel releases it when the holder dies, so a crash can
// never leave CI stuck behind a stale lock. Go opens files close-on-exec, so a step that outlives
// citask does not keep the lock either.
type ExclusivePollFileLock struct {
	LockFile string
}

func (lock ExclusivePollFileLock) TryAcquireExclusivePollLock() (releaseLock func() error, acquired bool, err error) {
	if err := os.MkdirAll(filepath.Dir(lock.LockFile), 0o755); err != nil {
		return nil, false, fmt.Errorf("create folder for %s: %w", lock.LockFile, err)
	}
	lockFile, err := os.OpenFile(lock.LockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", lock.LockFile, err)
	}

	err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		lockFile.Close()
		return nil, false, nil
	}
	if err != nil {
		lockFile.Close()
		return nil, false, fmt.Errorf("lock %s: %w", lock.LockFile, err)
	}
	return lockFile.Close, true, nil
}
