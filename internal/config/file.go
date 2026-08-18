package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/nkiyohara/corresync/internal/filelock"
)

const maximumConfigBytes = 1 << 20

// DefaultPath returns the platform-native user configuration path.
func DefaultPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(directory, "corresync", "config.toml"), nil
}

// LegacyDefaultPath returns the read-only rollback path used by owa-bridge
// releases through v0.6.x. Migration never edits or removes this file.
func LegacyDefaultPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(directory, "owa-bridge", "config.toml"), nil
}

// Load decodes a strict TOML file and rejects unknown fields.
func Load(path string) (Config, error) {
	data, err := readConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	version, err := encodedVersion(data)
	if err != nil {
		return Config{}, err
	}
	if version == 2 {
		return MigrateV2(data)
	}
	if version == 3 {
		return MigrateV3(data)
	}
	if version == 4 {
		return MigrateV4(data)
	}
	if version == 5 {
		return MigrateV5(data)
	}
	if version == 6 {
		return MigrateV6(data)
	}
	if version == 7 {
		return MigrateV7(data)
	}
	if version == 8 {
		return MigrateV8(data)
	}
	if version == 9 {
		return MigrateV9(data)
	}
	if version == 10 {
		return MigrateV10(data)
	}
	return Parse(data)
}

// Parse decodes bounded strict TOML and validates the complete configuration.
func Parse(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var configuration Config
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, err
	}
	return configuration, nil
}

func encodedVersion(data []byte) (int, error) {
	if len(data) > maximumConfigBytes {
		return 0, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var header struct {
		Version int `toml:"version"`
	}
	if err := toml.Unmarshal(data, &header); err != nil {
		return 0, fmt.Errorf("decode config version: %w", err)
	}
	if header.Version == 0 {
		return 0, errors.New("config version is required")
	}
	return header.Version, nil
}

// Fingerprint hashes the exact bounded config file so a daemon cannot silently
// continue with stale policy after an edit. Configuration is intentionally
// secret-free; only the digest crosses IPC.
func Fingerprint(path string) (string, error) {
	data, err := readConfigFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func readConfigFile(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- path is the explicit local config API input.
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = file.Close() }()

	limited := io.LimitReader(file, maximumConfigBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(data) > maximumConfigBytes {
		return nil, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	return data, nil
}

// Save atomically replaces a configuration with user-only permissions.
func Save(path string, configuration Config) error {
	if err := configuration.Validate(); err != nil {
		return err
	}
	encoded, err := toml.Marshal(configuration)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if len(encoded) > maximumConfigBytes {
		return fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	return writeConfigFile(path, encoded)
}

// Create atomically coordinates first-time configuration creation with every
// read-modify-write transaction. It never replaces an existing path.
func Create(ctx context.Context, path string, configuration Config) (created bool, returnErr error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolve config create path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	lock, err := filelock.Acquire(ctx, absolute+".lock")
	if err != nil {
		return false, fmt.Errorf("acquire config create lock: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()

	info, err := os.Lstat(absolute)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, errors.New("config path exists and is not a regular file")
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect config path: %w", err)
	}
	if err := Save(absolute, configuration); err != nil {
		return false, err
	}
	return true, nil
}

// SaveTOML validates and atomically preserves caller-provided TOML, including
// comments and ordering.
func SaveTOML(path string, encoded []byte) error {
	if _, err := Parse(encoded); err != nil {
		return err
	}
	return writeConfigFile(path, encoded)
}

func writeConfigFile(path string, encoded []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- private directories require owner execute.
		return fmt.Errorf("protect config directory: %w", err)
	}
	if err := rejectNonRegular(path); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(directory, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary config: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect config: %w", err)
	}
	return nil
}

func rejectNonRegular(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect config path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("config path exists and is not a regular file")
	}
	return nil
}
