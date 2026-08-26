package localipc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	credentialBytes   = 32
	credentialPrefix  = "ipc1_"
	maxCredentialFile = 128
)

// CredentialLoadReason is a stable, non-secret explanation for why a daemon
// client could not load the rotating local credential.
type CredentialLoadReason string

const (
	CredentialMissing          CredentialLoadReason = "missing_credential" // #nosec G101 -- status category, not a credential.
	CredentialMalformed        CredentialLoadReason = "invalid_credential" // #nosec G101 -- status category, not a credential.
	CredentialPermissionDenied CredentialLoadReason = "permission_denied"
	CredentialUnavailable      CredentialLoadReason = "credential_unavailable" // #nosec G101 -- status category, not a credential.
)

var (
	ErrCredentialMissing          = errors.New("daemon IPC credential is missing")
	ErrCredentialMalformed        = errors.New("daemon IPC credential is invalid")
	ErrCredentialPermissionDenied = errors.New("daemon IPC credential is not readable")
	ErrCredentialUnavailable      = errors.New("daemon IPC credential is unavailable")
)

// CredentialLoadError deliberately omits the credential path and content.
// Its wrapped cause remains available to errors.Is without entering normal
// command output.
type CredentialLoadError struct {
	Reason CredentialLoadReason
	cause  error
}

func (loadErr *CredentialLoadError) Error() string {
	switch loadErr.Reason {
	case CredentialMissing:
		return ErrCredentialMissing.Error()
	case CredentialMalformed:
		return ErrCredentialMalformed.Error()
	case CredentialPermissionDenied:
		return ErrCredentialPermissionDenied.Error()
	case CredentialUnavailable:
		return ErrCredentialUnavailable.Error()
	default:
		return "unknown daemon IPC credential failure"
	}
}

func (loadErr *CredentialLoadError) Unwrap() error { return loadErr.cause }

func (loadErr *CredentialLoadError) Is(target error) bool {
	switch loadErr.Reason {
	case CredentialMissing:
		return target == ErrCredentialMissing
	case CredentialMalformed:
		return target == ErrCredentialMalformed
	case CredentialPermissionDenied:
		return target == ErrCredentialPermissionDenied
	case CredentialUnavailable:
		return target == ErrCredentialUnavailable
	default:
		return false
	}
}

// CredentialLoadFailure returns the bounded machine reason for one load
// failure without exposing the state path or credential bytes.
func CredentialLoadFailure(err error) (CredentialLoadReason, bool) {
	var loadErr *CredentialLoadError
	if !errors.As(err, &loadErr) {
		return "", false
	}
	return loadErr.Reason, true
}

func credentialLoadFailure(reason CredentialLoadReason, cause error) error {
	if errors.Is(cause, os.ErrNotExist) {
		reason = CredentialMissing
	} else if errors.Is(cause, os.ErrPermission) {
		reason = CredentialPermissionDenied
	}
	return &CredentialLoadError{Reason: reason, cause: cause}
}

// Credential is the short-lived bearer used in addition to OS peer controls.
// Its value must never be logged or persisted anywhere except its private file.
type Credential struct {
	value string
	path  string
	once  sync.Once
	err   error
}

// IssueCredential rotates the endpoint credential after its listener is held.
func IssueCredential(endpoint Endpoint) (*Credential, error) {
	if err := ensurePrivateDirectory(filepath.Dir(endpoint.CredentialPath)); err != nil {
		return nil, fmt.Errorf("protect IPC credential directory: %w", err)
	}
	random := make([]byte, credentialBytes)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return nil, fmt.Errorf("generate IPC credential: %w", err)
	}
	value := credentialPrefix + base64.RawURLEncoding.EncodeToString(random)
	if err := replaceCredentialFile(endpoint.CredentialPath, value); err != nil {
		return nil, err
	}
	return &Credential{value: value, path: endpoint.CredentialPath}, nil
}

// Value returns the in-memory credential for HTTP authentication.
func (credential *Credential) Value() string { return credential.value }

// Close removes this credential only if the path still contains the same
// value, so a late shutdown cannot delete a successor daemon's credential.
func (credential *Credential) Close() error {
	credential.once.Do(func() {
		current, err := loadCredentialPath(credential.path)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			credential.err = err
			return
		}
		if !MatchesCredential(current, credential.value) {
			return
		}
		credential.err = os.Remove(credential.path)
	})
	return credential.err
}

// LoadCredential reads and validates the active local credential.
func LoadCredential(endpoint Endpoint) (string, error) {
	return loadCredentialPath(endpoint.CredentialPath)
}

func loadCredentialPath(path string) (string, error) {
	if err := validateCredentialFile(path); err != nil {
		return "", credentialLoadFailure(CredentialMalformed, err)
	}
	file, err := os.Open(path) // #nosec G304 -- derived private state path.
	if err != nil {
		return "", credentialLoadFailure(CredentialUnavailable, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxCredentialFile+1))
	if err != nil {
		return "", credentialLoadFailure(CredentialUnavailable, err)
	}
	if len(data) > maxCredentialFile {
		return "", credentialLoadFailure(
			CredentialMalformed,
			errors.New("IPC credential file is too large"),
		)
	}
	value := strings.TrimSuffix(string(data), "\n")
	if err := ValidateCredential(value); err != nil {
		return "", credentialLoadFailure(CredentialMalformed, err)
	}
	return value, nil
}

func replaceCredentialFile(path, value string) error {
	if err := rejectCredentialTarget(path); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".ipc-token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary IPC credential: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary IPC credential: %w", err)
	}
	if _, err := io.WriteString(temporary, value+"\n"); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write IPC credential: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync IPC credential: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close IPC credential: %w", err)
	}
	if err := removeRegularCredential(path); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace IPC credential: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // #nosec G302 -- owner-only by design.
		return fmt.Errorf("protect IPC credential: %w", err)
	}
	if err := protectCredentialPath(path); err != nil {
		return fmt.Errorf("apply IPC credential access control: %w", err)
	}
	return nil
}

func rejectCredentialTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect IPC credential: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("IPC credential path exists and is not a regular file")
	}
	return nil
}

func removeRegularCredential(path string) error {
	if err := rejectCredentialTarget(path); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ValidateCredential checks format without disclosing why a secret failed.
func ValidateCredential(value string) error {
	if len(value) != len(credentialPrefix)+base64.RawURLEncoding.EncodedLen(credentialBytes) ||
		!strings.HasPrefix(value, credentialPrefix) {
		return errors.New("invalid IPC credential")
	}
	if _, err := base64.RawURLEncoding.DecodeString(value[len(credentialPrefix):]); err != nil {
		return errors.New("invalid IPC credential")
	}
	return nil
}

// MatchesCredential compares two credentials without content-dependent timing.
func MatchesCredential(actual, expected string) bool {
	if len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}
