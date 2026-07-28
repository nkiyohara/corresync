// Package paths resolves platform-native, account-safe local state paths.
package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nkiyohara/corresync/internal/domain"
)

// StateDir returns the private application state directory for this platform.
func StateDir() (string, error) {
	if override := os.Getenv("CORRESYNC_STATE_DIR"); override != "" {
		if !filepath.IsAbs(override) {
			return "", errors.New("CORRESYNC_STATE_DIR must be absolute")
		}
		return filepath.Clean(override), nil
	}
	// OWA_STATE_DIR remains an explicit compatibility override. It is never
	// silently copied or renamed because callers may intentionally share it.
	if override := os.Getenv("OWA_STATE_DIR"); override != "" {
		if !filepath.IsAbs(override) {
			return "", errors.New("OWA_STATE_DIR must be absolute")
		}
		return filepath.Clean(override), nil
	}
	return defaultStateDir("corresync")
}

// LegacyStateDir returns the default owa-bridge v0.6.x state path. Explicit
// state overrides are intentionally excluded from automatic migration.
func LegacyStateDir() (string, error) {
	return defaultStateDir("owa-bridge")
}

func defaultStateDir(applicationName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	configDirectory, configErr := os.UserConfigDir()
	cacheDirectory, cacheErr := os.UserCacheDir()
	return stateDir(
		runtime.GOOS,
		home,
		configDirectory,
		cacheDirectory,
		configErr,
		cacheErr,
		os.Getenv("XDG_STATE_HOME"),
		applicationName,
	)
}

// ProfileDir uses a digest so an account alias can never become a path.
func ProfileDir(account domain.AccountID) (string, error) {
	if err := account.Validate(); err != nil {
		return "", err
	}
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "profiles", profileKey(string(account))), nil
}

// AccountStateDir contains provider cursors, caches, import plans, and monitor
// state owned by one stable account. It never uses an alias or address as a
// path component.
func AccountStateDir(account domain.AccountID) (string, error) {
	if err := account.ValidateOpaque(); err != nil {
		return "", err
	}
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "accounts", profileKey(string(account))), nil
}

func profileKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}

// AuditPath returns the content-free JSONL audit path.
func AuditPath() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "audit", "events.jsonl"), nil
}

// UpdateCachePath returns the private, content-free release check cache.
func UpdateCachePath() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "updates", "latest.json"), nil
}

// UpdateTrustCachePath returns the private cache for Sigstore TUF trust
// metadata used only by an explicit direct self-update.
func UpdateTrustCachePath() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "updates", "sigstore"), nil
}

// FeedbackErrorPath returns the single replace-in-place sanitized error record.
func FeedbackErrorPath() (string, error) {
	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "diagnostics", "last-error.json"), nil
}

func stateDir(
	goos, home, configDirectory, cacheDirectory string,
	configErr, cacheErr error,
	xdgStateHome string,
	applicationName string,
) (string, error) {
	switch goos {
	case "linux":
		if xdgStateHome != "" {
			if !filepath.IsAbs(xdgStateHome) {
				return "", errors.New("XDG_STATE_HOME must be absolute")
			}
			return filepath.Join(xdgStateHome, applicationName), nil
		}
		if home == "" {
			return "", errors.New("user home is empty")
		}
		return filepath.Join(home, ".local", "state", applicationName), nil
	case "darwin":
		if configErr != nil {
			return "", fmt.Errorf("resolve application support directory: %w", configErr)
		}
		return filepath.Join(configDirectory, applicationName), nil
	case "windows":
		if cacheErr != nil {
			return "", fmt.Errorf("resolve local application data: %w", cacheErr)
		}
		return filepath.Join(cacheDirectory, applicationName), nil
	default:
		if cacheErr != nil {
			return "", fmt.Errorf("resolve state directory: %w", cacheErr)
		}
		return filepath.Join(cacheDirectory, applicationName), nil
	}
}
