// Package filelock provides a small context-aware, cross-process file lock.
package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const retryInterval = 25 * time.Millisecond

// Lock owns one operating-system advisory lock until Close is called.
type Lock struct {
	file *os.File
}

// Acquire waits for an exclusive lock while honoring context cancellation.
// The lock file contains no data and may remain after the process exits.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("lock path must be absolute")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- a private lock directory needs owner execute.
		return nil, fmt.Errorf("protect lock directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("lock path exists and is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect lock path: %w", err)
	}

	// #nosec G304 -- path is an absolute application-owned lock path.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect lock file: %w", err)
	}
	for {
		locked, lockErr := tryLock(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("lock file: %w", lockErr)
		}
		if locked {
			return &Lock{file: file}, nil
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, fmt.Errorf("wait for file lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// Close releases the lock. It is safe to call more than once.
func (lock *Lock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	file := lock.file
	lock.file = nil
	return errors.Join(unlock(file), file.Close())
}
