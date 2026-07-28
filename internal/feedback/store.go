package feedback

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maximumRecordBytes = 8 << 10

var (
	// ErrNoRecord means no command failure has been recorded yet.
	ErrNoRecord = errors.New("no local error record")
	// ErrInvalidRecord means persisted diagnostics are unsafe or malformed.
	ErrInvalidRecord = errors.New("invalid local error record")
)

// Store atomically replaces one bounded content-free error record.
type Store struct {
	Path string
}

// Save persists record without appending or retaining historical failures.
func (store Store) Save(record ErrorRecord) error {
	if !filepath.IsAbs(store.Path) {
		return errors.New("diagnostic record path must be absolute")
	}
	if err := record.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode diagnostic record: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximumRecordBytes {
		return ErrInvalidRecord
	}
	directory := filepath.Dir(store.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create diagnostic directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect diagnostic directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("diagnostic directory is not a directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- owner-only diagnostic state.
		return fmt.Errorf("protect diagnostic directory: %w", err)
	}
	if info, err := os.Lstat(store.Path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("diagnostic record is not an owner-only regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect diagnostic record: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".last-error-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary diagnostic record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary diagnostic record: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary diagnostic record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary diagnostic record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary diagnostic record: %w", err)
	}
	if err := os.Rename(temporaryPath, store.Path); err != nil {
		return fmt.Errorf("replace diagnostic record: %w", err)
	}
	return nil
}

// Load reads one strictly bounded record and rejects trailing data.
func (store Store) Load() (ErrorRecord, error) {
	if !filepath.IsAbs(store.Path) {
		return ErrorRecord{}, ErrInvalidRecord
	}
	file, err := openDiagnosticRecord(store.Path)
	if err != nil {
		return ErrorRecord{}, err
	}
	defer func() { _ = file.Close() }()
	encoded, err := io.ReadAll(io.LimitReader(file, maximumRecordBytes+1))
	if err != nil || len(encoded) > maximumRecordBytes {
		return ErrorRecord{}, ErrInvalidRecord
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var record ErrorRecord
	if err := decoder.Decode(&record); err != nil {
		return ErrorRecord{}, ErrInvalidRecord
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrorRecord{}, ErrInvalidRecord
	}
	if err := record.validate(); err != nil {
		return ErrorRecord{}, ErrInvalidRecord
	}
	return record, nil
}
