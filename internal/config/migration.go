package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/nkiyohara/corresync/internal/domain"
)

const legacyVersion = 1

type legacyConfig struct {
	Version        int                      `toml:"version"`
	DefaultAccount string                   `toml:"default_account"`
	Accounts       map[string]legacyAccount `toml:"accounts"`
	Policy         Policy                   `toml:"policy"`
	Browser        Browser                  `toml:"browser"`
	Updates        Updates                  `toml:"updates"`
}

type legacyAccount struct {
	Origin  string `toml:"origin"`
	Mailbox string `toml:"mailbox,omitempty"`
}

// MigrateV1 converts an exact legacy config snapshot to v2. Account IDs are
// generated once and must be persisted by the caller before use.
func MigrateV1(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy legacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode legacy config: %w", err)
	}
	if legacy.Version != legacyVersion {
		return Config{}, fmt.Errorf("legacy config version must be %d", legacyVersion)
	}
	if len(legacy.Accounts) == 0 {
		return Config{}, errors.New("at least one account is required")
	}

	aliases := make([]string, 0, len(legacy.Accounts))
	for alias := range legacy.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	configuration := Config{
		Version:        CurrentVersion,
		DefaultAccount: legacy.DefaultAccount,
		Accounts:       make(map[string]Account, len(legacy.Accounts)),
		Policy:         legacy.Policy,
		Browser:        legacy.Browser,
		Updates:        legacy.Updates,
	}
	for _, alias := range aliases {
		accountID, err := domain.NewAccountID()
		if err != nil {
			return Config{}, err
		}
		account := legacy.Accounts[alias]
		configuration.Accounts[alias] = Account{
			ID:       accountID,
			Provider: domain.ProviderMicrosoftOWA,
			Origin:   account.Origin,
			Mailbox:  account.Mailbox,
		}
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated config: %w", err)
	}
	return configuration, nil
}

// EnsureDefaultPath performs the one-way, rollback-safe default migration.
// The legacy file remains byte-for-byte unchanged; the returned file is v2.
func EnsureDefaultPath() (path string, migrated bool, err error) {
	path, err = DefaultPath()
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		if _, loadErr := Load(path); loadErr != nil {
			return "", false, loadErr
		}
		return path, false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect config path: %w", statErr)
	}

	legacyPath, err := LegacyDefaultPath()
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Lstat(legacyPath); errors.Is(statErr, os.ErrNotExist) {
		return path, false, nil
	} else if statErr != nil {
		return "", false, fmt.Errorf("inspect legacy config path: %w", statErr)
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", false, fmt.Errorf("create config directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- private directories require owner execute.
		return "", false, fmt.Errorf("protect config directory: %w", err)
	}
	unlock, acquired, err := acquireMigrationLock(directory, path)
	if err != nil {
		return "", false, err
	}
	if !acquired {
		return path, false, nil
	}
	defer unlock()

	if _, statErr := os.Lstat(path); statErr == nil {
		if _, loadErr := Load(path); loadErr != nil {
			return "", false, loadErr
		}
		return path, false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect config path: %w", statErr)
	}

	data, err := readConfigFile(legacyPath)
	if err != nil {
		return "", false, err
	}
	configuration, err := MigrateV1(data)
	if err != nil {
		return "", false, err
	}
	if err := Save(path, configuration); err != nil {
		return "", false, fmt.Errorf("save migrated config: %w", err)
	}
	return path, true, nil
}

func acquireMigrationLock(
	directory string,
	target string,
) (unlock func(), acquired bool, err error) {
	lockPath := filepath.Join(directory, ".migration.lock")
	for attempt := 0; attempt < 100; attempt++ {
		// #nosec G304 -- lockPath is derived from the platform config directory.
		file, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if openErr == nil {
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, false, fmt.Errorf("close migration lock: %w", closeErr)
			}
			return func() { _ = os.Remove(lockPath) }, true, nil
		}
		if !errors.Is(openErr, os.ErrExist) {
			return nil, false, fmt.Errorf("create migration lock: %w", openErr)
		}
		if _, statErr := os.Lstat(target); statErr == nil {
			if _, loadErr := Load(target); loadErr != nil {
				return nil, false, loadErr
			}
			return func() {}, false, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, false, errors.New("timed out waiting for config migration lock")
}
