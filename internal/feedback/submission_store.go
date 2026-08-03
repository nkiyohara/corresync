package feedback

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SubmissionStore persists content-free at-most-once attempt markers. A
// marker is claimed before invoking GitHub so a lost response cannot create a
// duplicate public issue on the next identical failure.
type SubmissionStore struct {
	Directory string
}

// Claim returns true only for the first attempt for one sanitized build and
// error fingerprint. It stores no time, issue content, URL, or GitHub identity.
func (store SubmissionStore) Claim(build Build, record ErrorRecord) (bool, error) {
	if !filepath.IsAbs(store.Directory) {
		return false, errors.New("automatic feedback marker directory must be absolute")
	}
	if err := record.validate(); err != nil {
		return false, errors.New("automatic feedback record is invalid")
	}
	build = sanitizeBuild(build)
	digest := sha256.Sum256([]byte(
		build.Version + "\x00" + build.Commit + "\x00" + record.ID,
	))
	name := hex.EncodeToString(digest[:]) + ".attempted"
	if err := os.MkdirAll(store.Directory, 0o700); err != nil {
		return false, fmt.Errorf("create automatic feedback marker directory: %w", err)
	}
	info, err := os.Lstat(store.Directory)
	if err != nil {
		return false, fmt.Errorf("inspect automatic feedback marker directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("automatic feedback marker path is not a directory")
	}
	if err := os.Chmod(store.Directory, 0o700); err != nil { // #nosec G302 -- owner-only diagnostic state.
		return false, fmt.Errorf("protect automatic feedback marker directory: %w", err)
	}
	path := filepath.Join(store.Directory, name)
	file, err := os.OpenFile( // #nosec G304 -- absolute private directory plus a SHA-256 filename.
		path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
	)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim automatic feedback attempt: %w", err)
	}
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.WriteString("attempted\n"); err != nil {
		return false, fmt.Errorf("write automatic feedback marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("sync automatic feedback marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close automatic feedback marker: %w", err)
	}
	removeOnFailure = false
	return true, nil
}
