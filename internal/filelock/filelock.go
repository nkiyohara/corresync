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
	root *os.Root
}

// Acquire waits for an exclusive lock while honoring context cancellation.
// The lock file contains no data and may remain after the process exits.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	return acquire(ctx, path, true)
}

// AcquireSidecar waits for an exclusive sidecar lock without changing the
// permissions of an existing host-owned directory. Newly created directories
// remain private.
func AcquireSidecar(ctx context.Context, path string) (*Lock, error) {
	return acquire(ctx, path, false)
}

func acquire(ctx context.Context, path string, protectDirectory bool) (*Lock, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("lock path must be clean and absolute")
	}
	directory := filepath.Dir(path)
	directoryInfo, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create lock directory: %w", err)
		}
		directoryInfo, err = os.Lstat(directory)
	}
	if err != nil || !directoryInfo.IsDir() || isLinkLike(directoryInfo) {
		if err != nil {
			return nil, fmt.Errorf("inspect lock directory: %w", err)
		}
		return nil, errors.New("lock directory is not a regular directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open lock directory: %w", err)
	}
	rootedDirectory, err := root.Stat(".")
	if err != nil || !os.SameFile(directoryInfo, rootedDirectory) {
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect opened lock directory: %w", err)
		}
		return nil, errors.New("lock directory changed while opening")
	}
	if protectDirectory {
		if err := root.Chmod(".", 0o700); err != nil { // #nosec G302 -- a private lock directory needs owner execute.
			_ = root.Close()
			return nil, fmt.Errorf("protect lock directory: %w", err)
		}
	}
	name := filepath.Base(path)
	var expected os.FileInfo
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || isLinkLike(info) {
			_ = root.Close()
			return nil, errors.New("lock path exists and is not a regular file")
		}
		expected = info
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = root.Close()
		return nil, fmt.Errorf("inspect lock path: %w", err)
	}

	file, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	opened, err := file.Stat()
	current, currentErr := root.Lstat(name)
	if err != nil || currentErr != nil || !opened.Mode().IsRegular() || isLinkLike(opened) ||
		!current.Mode().IsRegular() || isLinkLike(current) ||
		!os.SameFile(current, opened) || expected != nil && !os.SameFile(expected, opened) {
		_ = errors.Join(file.Close(), root.Close())
		if err != nil {
			return nil, fmt.Errorf("inspect opened lock file: %w", err)
		}
		if currentErr != nil {
			return nil, fmt.Errorf("reinspect lock path: %w", currentErr)
		}
		return nil, errors.New("lock path changed while opening")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = errors.Join(file.Close(), root.Close())
		return nil, fmt.Errorf("protect lock file: %w", err)
	}
	for {
		locked, lockErr := tryLock(file)
		if lockErr != nil {
			_ = errors.Join(file.Close(), root.Close())
			return nil, fmt.Errorf("lock file: %w", lockErr)
		}
		if locked {
			return &Lock{file: file, root: root}, nil
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = errors.Join(file.Close(), root.Close())
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
	root := lock.root
	lock.file = nil
	lock.root = nil
	return errors.Join(unlock(file), file.Close(), root.Close())
}
