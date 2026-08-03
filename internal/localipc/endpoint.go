// Package localipc provides authenticated, same-user local transports for the
// Corresync session owner. It never opens a TCP listener.
package localipc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/nkiyohara/corresync/internal/paths"
)

const (
	namespaceVersion       = "corresync-ipc-v1"
	legacyNamespaceVersion = "owa-bridge-ipc-v1"
)

var errNoActiveOwner = errors.New("IPC singleton lock is not held by an active owner")

// Endpoint identifies one daemon namespace without exposing configuration
// content. Different config paths and state directories cannot collide.
type Endpoint struct {
	ID             string
	Address        string
	CredentialPath string
	lockPath       string
	runtimeDir     string
}

type platformPaths struct {
	address    string
	runtimeDir string
	lockPath   string
}

// Resolve derives the current platform endpoint for an absolute config path.
func Resolve(configPath string) (Endpoint, error) {
	if !filepath.IsAbs(configPath) {
		return Endpoint{}, errors.New("IPC config path must be absolute")
	}
	stateDirectory, err := paths.StateDir()
	if err != nil {
		return Endpoint{}, err
	}
	return deriveEndpoint(
		filepath.Clean(configPath),
		stateDirectory,
		namespaceVersion,
		"corresync",
	)
}

// ResolveInState derives an endpoint with an explicit absolute state directory.
// It is useful to isolate embedded runtimes and contract tests.
func ResolveInState(configPath, stateDirectory string) (Endpoint, error) {
	return deriveEndpoint(
		filepath.Clean(configPath),
		filepath.Clean(stateDirectory),
		namespaceVersion,
		"corresync",
	)
}

// ResolvePrevious returns the distinct Unix runtime locations selected by
// Corresync through v0.8.5 for the same config/state namespace. Windows named
// pipes were already independent of process environment and return no prior
// locations. Callers use this only to migrate or diagnose an authenticated
// owner before opening the canonical endpoint.
func ResolvePrevious(configPath string) ([]Endpoint, error) {
	if !filepath.IsAbs(configPath) {
		return nil, errors.New("IPC config path must be absolute")
	}
	stateDirectory, err := paths.StateDir()
	if err != nil {
		return nil, err
	}
	return resolvePreviousInState(configPath, stateDirectory)
}

// ResolvePreviousInState is the isolated-state form used by lifecycle tests.
func ResolvePreviousInState(configPath, stateDirectory string) ([]Endpoint, error) {
	return resolvePreviousInState(configPath, stateDirectory)
}

// ResolveLegacyInState derives the exact endpoint namespace used by v0.6.x.
// It exists only so coordinated migration can stop the authenticated owner
// before moving browser profiles.
func ResolveLegacyInState(configPath, stateDirectory string) (Endpoint, error) {
	return deriveEndpoint(
		filepath.Clean(configPath),
		filepath.Clean(stateDirectory),
		legacyNamespaceVersion,
		"owa-bridge",
	)
}

func deriveEndpoint(
	configPath,
	stateDirectory,
	namespace,
	runtimeName string,
) (Endpoint, error) {
	if !filepath.IsAbs(configPath) || !filepath.IsAbs(stateDirectory) {
		return Endpoint{}, errors.New("IPC inputs must be absolute")
	}
	digest := sha256.Sum256([]byte(
		namespace + "\x00" + filepath.Clean(configPath) + "\x00" + filepath.Clean(stateDirectory),
	))
	id := hex.EncodeToString(digest[:16])
	resolver := platformEndpoint
	if namespace == legacyNamespaceVersion {
		resolver = legacyPlatformEndpoint
	}
	address, runtimeDirectory, lockPath, err := resolver(id, runtimeName)
	if err != nil {
		return Endpoint{}, fmt.Errorf("resolve local IPC endpoint: %w", err)
	}
	return Endpoint{
		ID:             id,
		Address:        address,
		CredentialPath: filepath.Join(stateDirectory, "ipc", id+".token"),
		lockPath:       lockPath,
		runtimeDir:     runtimeDirectory,
	}, nil
}

func resolvePreviousInState(configPath, stateDirectory string) ([]Endpoint, error) {
	configPath = filepath.Clean(configPath)
	stateDirectory = filepath.Clean(stateDirectory)
	if !filepath.IsAbs(configPath) || !filepath.IsAbs(stateDirectory) {
		return nil, errors.New("IPC inputs must be absolute")
	}
	digest := sha256.Sum256([]byte(
		namespaceVersion + "\x00" + configPath + "\x00" + stateDirectory,
	))
	id := hex.EncodeToString(digest[:16])
	current, err := deriveEndpoint(
		configPath,
		stateDirectory,
		namespaceVersion,
		"corresync",
	)
	if err != nil {
		return nil, err
	}
	previous, err := previousPlatformEndpoints(id, "corresync")
	if err != nil {
		return nil, fmt.Errorf("resolve previous local IPC endpoints: %w", err)
	}
	credentialPath := filepath.Join(stateDirectory, "ipc", id+".token")
	result := make([]Endpoint, 0, len(previous))
	seen := map[string]struct{}{current.Address: {}}
	for _, candidate := range previous {
		if _, duplicate := seen[candidate.address]; duplicate {
			continue
		}
		seen[candidate.address] = struct{}{}
		result = append(result, Endpoint{
			ID:             id,
			Address:        candidate.address,
			CredentialPath: credentialPath,
			lockPath:       candidate.lockPath,
			runtimeDir:     candidate.runtimeDir,
		})
	}
	return result, nil
}

// EndpointActive checks only the authenticated transport ownership boundary;
// it does not read the bearer credential or send a daemon request.
func EndpointActive(endpoint Endpoint) (bool, error) {
	return platformEndpointActive(endpoint)
}
