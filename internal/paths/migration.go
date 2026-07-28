package paths

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nkiyohara/owa-bridge/internal/domain"
)

const legacyMigrationMarker = ".migrated-from-owa-bridge-v1"

// MigrateLegacyState copies rollback-safe, non-secret v0.6.x state into the
// Corresync namespace. IPC credentials are deliberately not copied; the new
// daemon rotates its own credential. Browser profiles are moved and re-keyed
// from aliases to stable account IDs so two readable copies of authenticated
// session material never exist.
func MigrateLegacyState(accounts map[string]domain.AccountID) (bool, error) {
	if os.Getenv("CORRESYNC_STATE_DIR") != "" || os.Getenv("OWA_STATE_DIR") != "" {
		return false, nil
	}
	source, err := LegacyStateDir()
	if err != nil {
		return false, err
	}
	sourceInfo, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect legacy state: %w", err)
	}
	if !sourceInfo.IsDir() {
		return false, errors.New("legacy state path is not a directory")
	}

	target, err := StateDir()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return false, fmt.Errorf("create Corresync state directory: %w", err)
	}
	if err := os.Chmod(target, 0o700); err != nil { // #nosec G302 -- private directories require owner execute.
		return false, fmt.Errorf("protect Corresync state directory: %w", err)
	}
	marker := filepath.Join(target, legacyMigrationMarker)
	if info, markerErr := os.Lstat(marker); markerErr == nil {
		if !info.Mode().IsRegular() {
			return false, errors.New("state migration marker is not a regular file")
		}
		return false, nil
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect state migration marker: %w", markerErr)
	}

	unlock, err := acquireStateMigrationLock(target)
	if err != nil {
		return false, err
	}
	defer unlock()
	if _, markerErr := os.Lstat(marker); markerErr == nil {
		return false, nil
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect state migration marker: %w", markerErr)
	}

	if err := copyLegacyChildren(source, target); err != nil {
		return false, err
	}
	for alias, accountID := range accounts {
		if err := domain.AccountAlias(alias).Validate(); err != nil {
			return false, fmt.Errorf("validate legacy profile alias %q: %w", alias, err)
		}
		if err := accountID.ValidateOpaque(); err != nil {
			return false, fmt.Errorf("validate migrated profile account %q: %w", alias, err)
		}
		legacyProfile := filepath.Join(source, "profiles", profileKey(alias))
		newProfile := filepath.Join(target, "profiles", profileKey(string(accountID)))
		if err := moveLegacyProfile(legacyProfile, newProfile); err != nil {
			return false, fmt.Errorf("migrate browser profile %q: %w", alias, err)
		}
	}
	if err := writePrivateFileIfAbsent(marker, []byte("corresync-state-migration-v1\n"), 0o600); err != nil {
		return false, fmt.Errorf("write state migration marker: %w", err)
	}
	return true, nil
}

func moveLegacyProfile(source, target string) error {
	sourceInfo, sourceErr := os.Lstat(source)
	if errors.Is(sourceErr, os.ErrNotExist) {
		return nil
	}
	if sourceErr != nil {
		return sourceErr
	}
	if !sourceInfo.IsDir() {
		return errors.New("legacy browser profile is not a directory")
	}
	if _, targetErr := os.Lstat(target); targetErr == nil {
		return errors.New("both legacy and Corresync browser profiles exist")
	} else if !errors.Is(targetErr, os.ErrNotExist) {
		return targetErr
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf(
			"move browser profile atomically (source and destination must share a filesystem): %w",
			err,
		)
	}
	return nil
}

func acquireStateMigrationLock(target string) (func(), error) {
	lockPath := filepath.Join(target, ".state-migration.lock")
	// #nosec G304 -- lockPath is derived from the platform state directory.
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, errors.New("another Corresync state migration is in progress")
	}
	if err != nil {
		return nil, fmt.Errorf("create state migration lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("close state migration lock: %w", err)
	}
	return func() { _ = os.Remove(lockPath) }, nil
}

func copyLegacyChildren(source, target string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read legacy state: %w", err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "ipc", "profiles", legacyMigrationMarker, ".state-migration.lock":
			continue
		}
		sourcePath := filepath.Join(source, entry.Name())
		targetPath := filepath.Join(target, entry.Name())
		if err := copyTreeIfPresent(sourcePath, targetPath, source); err != nil {
			return fmt.Errorf("migrate legacy state entry %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func copyTreeIfPresent(source, target, sourceRoot string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(target, privateDirectoryMode(info.Mode())); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "Singleton") || entry.Name() == "DevToolsActivePort" {
				continue
			}
			if err := copyTreeIfPresent(
				filepath.Join(source, entry.Name()),
				filepath.Join(target, entry.Name()),
				sourceRoot,
			); err != nil {
				return err
			}
		}
		return nil
	}
	if info.Mode().IsRegular() {
		return copyRegularFile(source, target, privateFileMode(info.Mode()))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return copyRelativeSymlink(source, target, sourceRoot)
	}
	return fmt.Errorf("unsupported state file type %s", info.Mode().Type())
}

func copyRegularFile(source, target string, mode fs.FileMode) error {
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("state migration destination is not a regular file")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	// #nosec G304 -- source is confined to the platform legacy state root.
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	temporary, err := os.CreateTemp(filepath.Dir(target), ".state-copy-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil { // #nosec G302 -- mode is reduced to owner-only bits.
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return err
	}
	return nil
}

func copyRelativeSymlink(source, target, sourceRoot string) error {
	link, err := os.Readlink(source)
	if err != nil {
		return err
	}
	if filepath.IsAbs(link) {
		return errors.New("absolute state symlink is not migrated")
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(source), link))
	relative, err := filepath.Rel(sourceRoot, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("state symlink escapes its source tree")
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return errors.New("state migration destination is not a symlink")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return os.Symlink(link, target)
}

func writePrivateFileIfAbsent(path string, contents []byte, mode fs.FileMode) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, contents, mode) // #nosec G306 -- caller supplies owner-only mode.
}

func privateDirectoryMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm()&0o700 | 0o700
}

func privateFileMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm()&0o700 | 0o600
}
