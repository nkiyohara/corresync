package integrationlifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/filelock"
)

const maximumHostConfigBytes = 1 << 20

type jsonSnapshot struct {
	document    map[string]any
	data        []byte
	fingerprint string
	exists      bool
	mode        os.FileMode
}

type JSONStore struct{}

func (JSONStore) Inspect(path string, adapter jsonAdapter, request Request, environment Environment) (Inspection, error) {
	if err := validateTargetParents(path, request, environment); err != nil {
		return inspectionForFileError(path, request.Scope, err), nil
	}
	snapshot, err := loadJSONSnapshot(path)
	if err != nil {
		return inspectionForFileError(path, request.Scope, err), nil
	}
	inspection := Inspection{
		Scope: request.Scope, Path: path, Fingerprint: snapshot.fingerprint,
	}
	if !snapshot.exists {
		inspection.State = StateAbsent
		inspection.Detail = "The named Corresync integration is not present in the documented host configuration."
		return inspection, nil
	}
	servers, err := adapter.serverMap(snapshot.document, false)
	if err != nil {
		inspection.State = StateMalformed
		inspection.Detail = "The documented host configuration has an incompatible MCP object shape."
		return inspection, nil //nolint:nilerr // Parse failures are represented by the typed inspection state.
	}
	if servers == nil {
		inspection.State = StateAbsent
		inspection.Detail = "The named Corresync integration is not registered."
		return inspection, nil
	}
	entry, exists := servers[request.ServerName]
	if !exists {
		inspection.State = StateAbsent
		inspection.Detail = "The named Corresync integration is not registered."
		return inspection, nil
	}
	inspection.State = adapter.classifyEntry(request, entry)
	switch inspection.State {
	case StateHealthy:
		inspection.Detail = "The documented host configuration has the expected absolute Corresync launch contract."
	case StateDisabled:
		inspection.Detail = "The named Corresync integration is present but disabled."
	case StateStalePath:
		inspection.Detail = "The named Corresync integration has stale Corresync-owned launch fields."
	case StateNameConflict, StateAbsent, StateVersionDrift, StateMalformed, StateUnreadable, StateUnavailable:
		inspection.Detail = "The requested name belongs to a different host integration."
	}
	return inspection, nil
}

func inspectionForFileError(path string, scope agenthost.Scope, err error) Inspection {
	inspection := Inspection{Scope: scope, Path: path}
	var malformed *malformedJSONError
	switch {
	case errors.As(err, &malformed):
		inspection.State = StateMalformed
		inspection.Detail = "The documented host configuration is malformed or uses an unsupported schema version."
	case errors.Is(err, os.ErrPermission):
		inspection.State = StateUnreadable
		inspection.Detail = "The documented host configuration is not readable by the current user."
	default:
		inspection.State = StateUnreadable
		inspection.Detail = "The documented host configuration has an unsafe path, owner, mode, or file type."
	}
	return inspection
}

type malformedJSONError struct{ err error }

func (err *malformedJSONError) Error() string { return err.err.Error() }
func (err *malformedJSONError) Unwrap() error { return err.err }

func loadJSONSnapshot(path string) (jsonSnapshot, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return jsonSnapshot{}, errors.New("host configuration path must be clean and absolute")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return jsonSnapshot{
			document: make(map[string]any), fingerprint: absentFileFingerprint(path), mode: 0o600,
		}, nil
	}
	if err != nil {
		return jsonSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return jsonSnapshot{}, errors.New("host configuration path is not a regular file")
	}
	if WritableByOtherUsers(info) {
		return jsonSnapshot{}, errors.New("host configuration is writable by another user")
	}
	if !ownedByCurrentUser(info) {
		return jsonSnapshot{}, errors.New("host configuration is not owned by the current user")
	}
	if info.Size() > maximumHostConfigBytes {
		return jsonSnapshot{}, fmt.Errorf("host configuration exceeds %d bytes", maximumHostConfigBytes)
	}
	// #nosec G304 -- path is selected by one reviewed adapter from explicit roots.
	file, err := os.Open(path)
	if err != nil {
		return jsonSnapshot{}, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return jsonSnapshot{}, err
	}
	if !os.SameFile(info, opened) {
		return jsonSnapshot{}, errors.New("host configuration changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumHostConfigBytes+1))
	if err != nil {
		return jsonSnapshot{}, err
	}
	if len(data) > maximumHostConfigBytes {
		return jsonSnapshot{}, fmt.Errorf("host configuration exceeds %d bytes", maximumHostConfigBytes)
	}
	document, err := decodeJSONDocument(data)
	if err != nil {
		return jsonSnapshot{}, &malformedJSONError{err: err}
	}
	return jsonSnapshot{
		document: document, data: data, fingerprint: fileFingerprint(data),
		exists: true, mode: info.Mode().Perm(),
	}, nil
}

func decodeJSONDocument(data []byte) (map[string]any, error) {
	value, err := hujson.Parse(slices.Clone(data))
	if err != nil {
		return nil, fmt.Errorf("parse JSON/JSONC: %w", err)
	}
	value.Standardize()
	decoder := json.NewDecoder(bytes.NewReader(value.Pack()))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode JSON object: %w", err)
	}
	if document == nil {
		return nil, errors.New("host configuration root must be an object")
	}
	if version, exists := document["schemaVersion"]; exists {
		number, ok := version.(json.Number)
		if !ok || number.String() != "1" {
			return nil, fmt.Errorf("unsupported host configuration schemaVersion %v", version)
		}
	}
	return document, nil
}

func absentFileFingerprint(path string) string {
	sum := sha256.Sum256([]byte("absent\x00" + path))
	return hex.EncodeToString(sum[:])
}

func fileFingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (JSONStore) Apply(
	ctx context.Context,
	path string,
	adapter jsonAdapter,
	request Request,
	environment Environment,
	expectedFingerprint string,
	remove bool,
) (returnErr error) {
	if err := prepareTargetParent(path, request, environment); err != nil {
		return err
	}
	lock, err := filelock.AcquireSidecar(ctx, path+".corresync.lock")
	if err != nil {
		return fmt.Errorf("acquire host configuration lock: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if err := validateTargetParents(path, request, environment); err != nil {
		return err
	}

	snapshot, err := loadJSONSnapshot(path)
	if err != nil {
		return err
	}
	if snapshot.fingerprint != expectedFingerprint {
		return errors.New("host configuration changed after preview")
	}
	servers, err := adapter.serverMap(snapshot.document, !remove)
	if err != nil {
		return err
	}
	if remove {
		if servers != nil {
			delete(servers, request.ServerName)
		}
	} else {
		entry, _ := servers[request.ServerName].(map[string]any)
		if entry == nil {
			entry = make(map[string]any)
		}
		for name, value := range adapter.expectedEntry(request) {
			entry[name] = value
		}
		for _, name := range hostAutoApprovalFields {
			delete(entry, name)
		}
		delete(entry, "disabled")
		if adapter.shape != shapeZed {
			delete(entry, "enabled")
		}
		servers[request.ServerName] = entry
	}
	encoded, err := json.MarshalIndent(snapshot.document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode host configuration: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximumHostConfigBytes {
		return fmt.Errorf("updated host configuration exceeds %d bytes", maximumHostConfigBytes)
	}
	if err := validateTargetParents(path, request, environment); err != nil {
		return err
	}
	if snapshot.exists {
		if err := writeAtomicPrivate(path+".corresync.bak", snapshot.data, snapshot.mode); err != nil {
			return fmt.Errorf("write host configuration recovery copy: %w", err)
		}
	}
	mode := os.FileMode(0o600)
	if request.Scope != agenthost.ScopeUser {
		mode = 0o644
	}
	if snapshot.exists {
		mode = snapshot.mode
	}
	if err := writeAtomicPrivate(path, encoded, mode); err != nil {
		return fmt.Errorf("replace host configuration: %w", err)
	}
	return nil
}

func prepareTargetParent(path string, request Request, environment Environment) error {
	if err := validateTargetParents(path, request, environment); err != nil {
		return err
	}
	mode := os.FileMode(0o700)
	if request.Scope != agenthost.ScopeUser {
		mode = 0o755
	}
	if err := os.MkdirAll(filepath.Dir(path), mode); err != nil {
		return fmt.Errorf("create host configuration directory: %w", err)
	}
	return validateTargetParents(path, request, environment)
}

func validateTargetParents(path string, request Request, environment Environment) error {
	root := request.ProjectDirectory
	if request.Scope == agenthost.ScopeUser {
		root = environment.HomeDirectory
		if relative, err := filepath.Rel(root, path); err != nil || relative == ".." ||
			filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			root = environment.ConfigDirectory
		}
	}
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("integration configuration root must be absolute")
	}
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) ||
			WritableByOtherUsers(info) {
			return fmt.Errorf("integration configuration root has an unsafe type, owner, or mode: %s", root)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("host configuration target escapes its reviewed root")
	}
	for directory := filepath.Dir(path); directory != root; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !ownedByCurrentUser(info) ||
				WritableByOtherUsers(info) {
				return fmt.Errorf("host configuration parent has an unsafe type, owner, or mode: %s", directory)
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func writeAtomicPrivate(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || WritableByOtherUsers(info) {
			return errors.New("target exists with an unsafe file type, owner, or mode")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".corresync-integration-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	mode &= 0o666
	if mode == 0 {
		mode = 0o600
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}
