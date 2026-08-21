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

// CrashStore atomically replaces one bounded content-free crash record.
type CrashStore struct {
	Path string
}

// Save persists record without appending or retaining historical failures.
func (store Store) Save(record ErrorRecord) error {
	if err := record.validate(); err != nil {
		return err
	}
	return saveDiagnosticRecord(store.Path, ".last-error-*.tmp", record)
}

// Save persists one crash without appending or retaining historical failures.
func (store CrashStore) Save(record CrashRecord) error {
	if err := record.validate(); err != nil {
		return err
	}
	return saveDiagnosticRecord(store.Path, ".last-crash-*.tmp", record)
}

func saveDiagnosticRecord(path, temporaryPattern string, record any) error {
	if !filepath.IsAbs(path) {
		return errors.New("diagnostic record path must be absolute")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode diagnostic record: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximumRecordBytes {
		return ErrInvalidRecord
	}
	directory := filepath.Dir(path)
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
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("diagnostic record is not an owner-only regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect diagnostic record: %w", err)
	}

	temporary, err := os.CreateTemp(directory, temporaryPattern)
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace diagnostic record: %w", err)
	}
	return nil
}

// Load reads one strictly bounded record and rejects trailing data.
func (store Store) Load() (ErrorRecord, error) {
	var record ErrorRecord
	if err := loadDiagnosticRecord(store.Path, &record); err != nil {
		return ErrorRecord{}, err
	}
	if err := record.validate(); err != nil {
		return ErrorRecord{}, ErrInvalidRecord
	}
	return record, nil
}

// Load reads one strictly bounded crash record and rejects trailing data.
func (store CrashStore) Load() (CrashRecord, error) {
	var record CrashRecord
	if err := loadDiagnosticRecord(store.Path, &record); err != nil {
		return CrashRecord{}, err
	}
	if err := record.validate(); err != nil {
		return CrashRecord{}, ErrInvalidRecord
	}
	return record, nil
}

func loadDiagnosticRecord(path string, record any) error {
	if !filepath.IsAbs(path) {
		return ErrInvalidRecord
	}
	file, err := openDiagnosticRecord(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	encoded, err := io.ReadAll(io.LimitReader(file, maximumRecordBytes+1))
	if err != nil || len(encoded) > maximumRecordBytes {
		return ErrInvalidRecord
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(record); err != nil {
		return ErrInvalidRecord
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidRecord
	}
	return nil
}
