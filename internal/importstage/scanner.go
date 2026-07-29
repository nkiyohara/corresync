// Package importstage implements bounded, read-only archive scanning and
// Corresync-owned local staging. It never authenticates or contacts a provider.
package importstage

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
	"runtime"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/filelock"
	"github.com/nkiyohara/corresync/internal/paths"
)

const (
	maximumIndexEntries = 100_000
	maximumIndexBytes   = 32 << 20
	maximumPlanBytes    = 32 << 20
)

// Scanner is stateless; account-scoped advisory locks serialize staging.
type Scanner struct{}

func New() *Scanner {
	return &Scanner{}
}

type candidate struct {
	item     application.ImportItem
	raw      []byte
	identity string
}

type scanResult struct {
	format       application.ImportFormat
	candidates   []candidate
	hints        []application.ImportDesktopHint
	gates        []application.ImportDecisionGate
	degradations []domain.Degradation
	bytesRead    int64
}

type stagingIndex struct {
	Version    int                 `json:"version"`
	Exact      map[string]string   `json:"exact"`
	Identities map[string][]string `json:"identities"`
}

func (scanner *Scanner) Scan(
	ctx context.Context,
	input application.ImportScanInput,
) (application.ImportPlan, error) {
	if err := input.Validate(); err != nil {
		return application.ImportPlan{}, err
	}
	result, err := scanner.scanSource(ctx, input.Source, input.Format)
	if err != nil {
		return application.ImportPlan{}, err
	}
	if len(result.candidates) > application.MaxImportPlanItems ||
		len(result.hints) > application.MaxImportDesktopHints ||
		result.bytesRead > application.MaxImportSourceBytes {
		return application.ImportPlan{}, errors.New(
			"import source exceeds the configured scan bounds",
		)
	}
	planID, err := importPlanID(input, result)
	if err != nil {
		return application.ImportPlan{}, err
	}
	root, err := importRoot(input.Account)
	if err != nil {
		return application.ImportPlan{}, err
	}
	if err := ensureImportHierarchy(root); err != nil {
		return application.ImportPlan{}, err
	}
	lock, err := filelock.Acquire(ctx, root+".lock")
	if err != nil {
		return application.ImportPlan{}, fmt.Errorf("lock import staging: %w", err)
	}
	defer func() { _ = lock.Close() }()

	indexPath := filepath.Join(root, "index.json")
	index, err := loadIndex(indexPath)
	if err != nil {
		return application.ImportPlan{}, err
	}
	plan := application.ImportPlan{
		Version: 1, ID: planID, Account: input.Account,
		Source: input.Source, Format: result.format,
		ContentTrust: "untrusted_data",
		Items:        make([]application.ImportItem, 0, len(result.candidates)),
		DesktopHints: append([]application.ImportDesktopHint(nil), result.hints...),
		DecisionGates: append(
			[]application.ImportDecisionGate(nil),
			result.gates...,
		),
		Degradations: append(
			[]domain.Degradation(nil),
			result.degradations...,
		),
		BytesRead: result.bytesRead,
	}
	for _, source := range result.candidates {
		if err := ctx.Err(); err != nil {
			return application.ImportPlan{}, err
		}
		current := source.item
		current.Status = "staged"
		if existing, duplicate := index.Exact[current.DedupeKey]; duplicate {
			if existing != current.ObjectSHA256 {
				return application.ImportPlan{}, errors.New(
					"import staging index contains an inconsistent exact match",
				)
			}
			current.Status = "duplicate"
			plan.DuplicateItems++
		} else {
			if source.identity != "" &&
				identityHasDifferentObject(
					index.Identities[source.identity],
					current.ObjectSHA256,
				) {
				current.Status = "conflict"
				current.Degradations = append(
					current.Degradations,
					domain.Degradation{
						Feature: "import.deduplication",
						Reason:  "the same source identity has different content or metadata; both copies were retained",
					},
				)
				plan.Conflicts++
			}
			index.Exact[current.DedupeKey] = current.ObjectSHA256
			if source.identity != "" {
				index.Identities[source.identity] = append(
					index.Identities[source.identity],
					current.ObjectSHA256,
				)
				slices.Sort(index.Identities[source.identity])
				index.Identities[source.identity] = slices.Compact(
					index.Identities[source.identity],
				)
			}
			plan.StagedItems++
		}
		plan.Items = append(plan.Items, current)
	}
	if len(index.Exact) > maximumIndexEntries ||
		len(index.Identities) > maximumIndexEntries {
		return application.ImportPlan{}, errors.New(
			"import staging index reached its configured limit; purge staging before scanning more data",
		)
	}
	for _, objects := range index.Identities {
		if len(objects) > maximumIndexEntries {
			return application.ImportPlan{}, errors.New(
				"import staging identity reached its configured limit; purge staging before scanning more data",
			)
		}
	}
	if err := plan.Validate(); err != nil {
		return application.ImportPlan{}, fmt.Errorf(
			"validate generated import plan: %w",
			err,
		)
	}
	planPath := filepath.Join(root, "plans", plan.ID+".json")
	if existing, err := loadExistingPlan(planPath, plan); err != nil {
		return application.ImportPlan{}, err
	} else if existing {
		plan.ExistingPlan = true
	}
	if _, err := encodePrivateJSON(plan, maximumPlanBytes); err != nil {
		return application.ImportPlan{}, err
	}
	if _, err := encodePrivateJSON(index, maximumIndexBytes); err != nil {
		return application.ImportPlan{}, err
	}
	for _, source := range result.candidates {
		if err := ctx.Err(); err != nil {
			return application.ImportPlan{}, err
		}
		if err := writeObject(
			root,
			source.item.ObjectSHA256,
			source.raw,
		); err != nil {
			return application.ImportPlan{}, err
		}
	}
	if !plan.ExistingPlan {
		if err := writePrivateJSON(planPath, plan, maximumPlanBytes); err != nil {
			return application.ImportPlan{}, err
		}
	}
	if err := writePrivateJSON(indexPath, index, maximumIndexBytes); err != nil {
		return application.ImportPlan{}, err
	}
	return plan, nil
}

func importPlanID(
	input application.ImportScanInput,
	result scanResult,
) (string, error) {
	type identity struct {
		Account domain.AccountID                 `json:"account"`
		Source  string                           `json:"source"`
		Format  application.ImportFormat         `json:"format"`
		Items   []application.ImportItem         `json:"items"`
		Hints   []application.ImportDesktopHint  `json:"hints,omitempty"`
		Gates   []application.ImportDecisionGate `json:"gates,omitempty"`
	}
	value := identity{
		Account: input.Account, Source: input.Source, Format: result.format,
		Items: make([]application.ImportItem, 0, len(result.candidates)),
		Hints: result.hints, Gates: result.gates,
	}
	for _, item := range result.candidates {
		copy := item.item
		copy.Status = ""
		value.Items = append(value.Items, copy)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "imp_" + hex.EncodeToString(digest[:]), nil
}

func loadIndex(path string) (stagingIndex, error) {
	index := stagingIndex{
		Version:    1,
		Exact:      make(map[string]string),
		Identities: make(map[string][]string),
	}
	data, exists, err := readPrivateFile(path, maximumIndexBytes)
	if err != nil || !exists {
		return index, err
	}
	if err := decodeStrictJSON(data, &index); err != nil {
		return stagingIndex{}, fmt.Errorf("decode import staging index: %w", err)
	}
	if index.Version != 1 || index.Exact == nil || index.Identities == nil ||
		len(index.Exact) > maximumIndexEntries ||
		len(index.Identities) > maximumIndexEntries {
		return stagingIndex{}, errors.New("import staging index is invalid")
	}
	for key, object := range index.Exact {
		if !validDigest(key) || !validDigest(object) {
			return stagingIndex{}, errors.New("import staging index digest is invalid")
		}
	}
	for identity, objects := range index.Identities {
		if identity == "" || len(identity) > 2048 ||
			strings.ContainsAny(identity, "\r\n\x00") ||
			len(objects) > maximumIndexEntries {
			return stagingIndex{}, errors.New("import staging identity is invalid")
		}
		for _, object := range objects {
			if !validDigest(object) {
				return stagingIndex{}, errors.New(
					"import staging identity digest is invalid",
				)
			}
		}
	}
	return index, nil
}

func loadExistingPlan(
	path string,
	expected application.ImportPlan,
) (bool, error) {
	data, exists, err := readPrivateFile(path, maximumPlanBytes)
	if err != nil || !exists {
		return exists, err
	}
	var plan application.ImportPlan
	if err := decodeStrictJSON(data, &plan); err != nil {
		return false, fmt.Errorf("decode existing import plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return false, fmt.Errorf("validate existing import plan: %w", err)
	}
	if plan.ID != expected.ID || plan.Account != expected.Account ||
		plan.Source != expected.Source || plan.Format != expected.Format ||
		plan.ContentTrust != expected.ContentTrust {
		return false, errors.New(
			"existing import plan does not match its deterministic identity",
		)
	}
	return true, nil
}

func writeObject(root, digest string, content []byte) error {
	if len(content) > application.MaxImportItemBytes {
		return errors.New("import object exceeds the configured item limit")
	}
	if !validDigest(digest) || contentDigest(content) != digest {
		return errors.New("import object digest is invalid")
	}
	directory := filepath.Join(root, "objects", digest[:2])
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	path := filepath.Join(directory, digest)
	if data, exists, err := readPrivateFile(
		path,
		application.MaxImportItemBytes,
	); err != nil {
		return err
	} else if exists {
		existing := sha256.Sum256(data)
		if hex.EncodeToString(existing[:]) != digest {
			return errors.New("existing import object failed its content digest")
		}
		return nil
	}
	// #nosec G304 -- path contains only an application-owned root and SHA-256.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create import object: %w", err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write import object: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("sync import object: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close import object: %w", err)
	}
	return nil
}

func writePrivateJSON(path string, value any, maximum int) error {
	encoded, err := encodePrivateJSON(value, maximum)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("import staging target is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect import staging target: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".import-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary import file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
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
		return fmt.Errorf("replace import staging file: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func encodePrivateJSON(value any, maximum int) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximum {
		return nil, errors.New("import staging JSON exceeds its configured limit")
	}
	return encoded, nil
}

func readPrivateFile(path string, maximum int) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect import staging file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("import staging file is not regular")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, false, errors.New("import staging file permissions are too broad")
	}
	// #nosec G304 -- caller supplies an application-owned staging path.
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, false, errors.New(
			"import staging file changed while it was opened",
		)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maximum {
		return nil, false, errors.New("import staging file exceeds its configured limit")
	}
	return data, true, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing content")
	}
	return nil
}

func identityHasDifferentObject(objects []string, current string) bool {
	for _, object := range objects {
		if object != current {
			return true
		}
	}
	return false
}

func importRoot(account domain.AccountID) (string, error) {
	state, err := paths.AccountStateDir(account)
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "imports"), nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create import staging directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect import staging directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("import staging path is not an owned directory")
	}
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- private directories require owner execute.
		return fmt.Errorf("protect import staging directory: %w", err)
	}
	return nil
}

func ensureImportHierarchy(root string) error {
	accountState := filepath.Dir(root)
	accountsRoot := filepath.Dir(accountState)
	for _, directory := range []string{accountsRoot, accountState, root} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

// Purge removes only the account-derived import root and refuses symlinks.
func (*Scanner) Purge(
	ctx context.Context,
	account domain.AccountID,
) (returnErr error) {
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	root, err := importRoot(account)
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect import staging: %w", err)
	}
	for _, parent := range []string{
		filepath.Dir(filepath.Dir(root)),
		filepath.Dir(root),
	} {
		info, inspectErr := os.Lstat(parent)
		if inspectErr != nil {
			return fmt.Errorf("inspect import staging parent: %w", inspectErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("import staging parent is not an owned directory")
		}
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("import staging root is not an owned directory")
	}
	lock, err := filelock.Acquire(ctx, root+".lock")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove import staging: %w", err)
	}
	return nil
}

func contentDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func dedupeDigest(values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func validDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cleanSourcePath(path string) string {
	return filepath.Clean(path)
}

func safeRelative(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.Base(path)
	}
	return filepath.ToSlash(relative)
}
