package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireSerializesAndHonorsContext(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.lock")
	first, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRejectsRelativePath(t *testing.T) {
	t.Parallel()
	if _, err := Acquire(context.Background(), "relative.lock"); err == nil {
		t.Fatal("Acquire() accepted a relative path")
	}
}

func TestAcquireSidecarPreservesExistingDirectoryMode(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil { // #nosec G302 -- host-owned public directory fixture.
		t.Fatal(err)
	}
	lock, err := AcquireSidecar(context.Background(), filepath.Join(directory, "config.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	assertDirectoryMode(t, info, 0o755)
}

func TestAcquireRejectsSymlinkLockPath(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.lock")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := AcquireSidecar(context.Background(), path); err == nil {
		t.Fatal("AcquireSidecar() accepted a symlink lock path")
	}
}

func TestAcquireRejectsSymlinkDirectoryWithoutChangingTargetMode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Chmod(target, 0o755); err != nil { // #nosec G302 -- intentionally visible fixture.
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Acquire(context.Background(), filepath.Join(linked, "state.lock")); err == nil {
		t.Fatal("Acquire() accepted a symlink directory")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	assertDirectoryMode(t, info, 0o755)
}
