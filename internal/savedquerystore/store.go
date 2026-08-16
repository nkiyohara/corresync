// Package savedquerystore persists private query definitions without caching
// provider content. Every catalog lives under one stable account state root.
package savedquerystore

import (
	"bytes"
	"context"
	"crypto/rand"
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

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/filelock"
	"github.com/nkiyohara/corresync/internal/paths"
)

const (
	schemaVersion     = 1
	maximumStateBytes = 256 << 10
)

type persistedCatalog struct {
	SchemaVersion int                                `json:"schemaVersion"`
	Queries       []application.SavedQueryDefinition `json:"queries"`
}

type Store struct {
	resolve func(domain.AccountID) (string, error)
}

func New() Store {
	return Store{resolve: func(account domain.AccountID) (string, error) {
		directory, err := paths.AccountStateDir(account)
		if err != nil {
			return "", err
		}
		return filepath.Join(directory, "queries", "saved-queries.json"), nil
	}}
}

func NewAt(directory string) Store {
	return Store{resolve: func(account domain.AccountID) (string, error) {
		if err := account.ValidateOpaque(); err != nil {
			return "", err
		}
		return filepath.Join(directory, string(account), "saved-queries.json"), nil
	}}
}

func (store Store) ListSavedQueries(
	ctx context.Context,
	account domain.AccountID,
) ([]application.SavedQueryDefinition, error) {
	path, err := store.path(account)
	if err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return nil, fmt.Errorf("lock saved query catalog: %w", err)
	}
	defer func() { _ = lock.Close() }()
	catalog, err := load(path, account)
	if err != nil {
		return nil, err
	}
	return cloneQueries(catalog.Queries), nil
}

func (store Store) PutSavedQuery(
	ctx context.Context,
	query application.SavedQueryDefinition,
	expectedRevision string,
) error {
	if err := query.Validate(); err != nil {
		return err
	}
	path, err := store.path(query.Account)
	if err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return fmt.Errorf("lock saved query catalog: %w", err)
	}
	defer func() { _ = lock.Close() }()
	catalog, err := load(path, query.Account)
	if err != nil {
		return err
	}
	index := -1
	for candidate := range catalog.Queries {
		if catalog.Queries[candidate].Name == query.Name {
			index = candidate
			break
		}
	}
	if index < 0 {
		if expectedRevision != "" {
			return errors.New("saved query changed after review; review it again")
		}
		if len(catalog.Queries) >= application.MaxSavedQueries {
			return fmt.Errorf(
				"at most %d saved queries are supported per account",
				application.MaxSavedQueries,
			)
		}
		catalog.Queries = append(catalog.Queries, query)
	} else {
		if catalog.Queries[index].Revision != expectedRevision {
			return errors.New("saved query changed after review; review it again")
		}
		catalog.Queries[index] = query
	}
	sortQueries(catalog.Queries)
	return save(path, catalog)
}

func (store Store) DeleteSavedQuery(
	ctx context.Context,
	account domain.AccountID,
	name string,
	expectedRevision string,
) error {
	path, err := store.path(account)
	if err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return fmt.Errorf("lock saved query catalog: %w", err)
	}
	defer func() { _ = lock.Close() }()
	catalog, err := load(path, account)
	if err != nil {
		return err
	}
	for index := range catalog.Queries {
		query := catalog.Queries[index]
		if query.Name != name {
			continue
		}
		if expectedRevision == "" || query.Revision != expectedRevision {
			return errors.New("saved query changed after review; review it again")
		}
		catalog.Queries = slices.Delete(catalog.Queries, index, index+1)
		return save(path, catalog)
	}
	return errors.New("saved query was not found in this account")
}

func (store Store) InspectSavedQueryCatalog(
	ctx context.Context,
	account domain.AccountID,
) (application.SavedQueryCatalogState, error) {
	path, err := store.path(account)
	if err != nil {
		return application.SavedQueryCatalogState{}, err
	}
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return application.SavedQueryCatalogState{}, fmt.Errorf("lock saved query catalog: %w", err)
	}
	defer func() { _ = lock.Close() }()
	return inspect(path, account)
}

func (store Store) PurgeSavedQueryCatalog(
	ctx context.Context,
	account domain.AccountID,
	expectedRevision string,
) error {
	if expectedRevision == "" {
		return errors.New("saved query catalog revision is required")
	}
	path, err := store.path(account)
	if err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return fmt.Errorf("lock saved query catalog: %w", err)
	}
	defer func() { _ = lock.Close() }()
	state, err := inspect(path, account)
	if err != nil {
		return err
	}
	if state.Revision == "" || state.Revision != expectedRevision {
		return errors.New("saved query catalog changed after review; review it again")
	}
	root, err := openCatalogRoot(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open saved query directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("saved query catalog changed before purge")
	}
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("purge saved query catalog: %w", err)
	}
	return syncDirectory(root)
}

func (store Store) path(account domain.AccountID) (string, error) {
	if err := account.ValidateOpaque(); err != nil {
		return "", err
	}
	if store.resolve == nil {
		return "", errors.New("saved query path resolver is unavailable")
	}
	path, err := store.resolve(account)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("saved query path must be clean and absolute")
	}
	return path, nil
}

func load(path string, account domain.AccountID) (persistedCatalog, error) {
	directory := filepath.Dir(path)
	root, err := openCatalogRoot(directory)
	if errors.Is(err, os.ErrNotExist) {
		return persistedCatalog{SchemaVersion: schemaVersion}, nil
	}
	if err != nil {
		return persistedCatalog{}, fmt.Errorf("open saved query directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return persistedCatalog{SchemaVersion: schemaVersion}, nil
	}
	if err != nil {
		return persistedCatalog{}, fmt.Errorf("inspect saved query catalog: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maximumStateBytes {
		return persistedCatalog{}, errors.New("saved query catalog is not a bounded regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return persistedCatalog{}, fmt.Errorf("open saved query catalog: %w", err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return persistedCatalog{}, errors.New("saved query catalog changed while opening")
	}
	var catalog persistedCatalog
	decoder := json.NewDecoder(io.LimitReader(file, maximumStateBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return persistedCatalog{}, fmt.Errorf("decode saved query catalog: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return persistedCatalog{}, errors.New("saved query catalog contains trailing data")
	}
	if err := validateCatalog(catalog, account); err != nil {
		return persistedCatalog{}, err
	}
	sortQueries(catalog.Queries)
	return catalog, nil
}

func inspect(
	path string,
	account domain.AccountID,
) (application.SavedQueryCatalogState, error) {
	directory := filepath.Dir(path)
	root, err := openCatalogRoot(directory)
	if errors.Is(err, os.ErrNotExist) {
		return application.SavedQueryCatalogState{}, nil
	}
	if err != nil {
		return application.SavedQueryCatalogState{}, fmt.Errorf("open saved query directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return application.SavedQueryCatalogState{}, nil
	}
	if err != nil {
		return application.SavedQueryCatalogState{}, fmt.Errorf("inspect saved query catalog: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maximumStateBytes {
		return application.SavedQueryCatalogState{}, errors.New("saved query catalog is not a bounded regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return application.SavedQueryCatalogState{}, fmt.Errorf("open saved query catalog: %w", err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return application.SavedQueryCatalogState{}, errors.New("saved query catalog changed while opening")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, maximumStateBytes+1))
	if err != nil {
		return application.SavedQueryCatalogState{}, fmt.Errorf("read saved query catalog: %w", err)
	}
	if len(encoded) > maximumStateBytes {
		return application.SavedQueryCatalogState{}, errors.New("saved query catalog exceeds its size bound")
	}
	digest := sha256.Sum256(encoded)
	state := application.SavedQueryCatalogState{Revision: hex.EncodeToString(digest[:])}
	var catalog persistedCatalog
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&catalog); decodeErr != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		validateCatalog(catalog, account) != nil {
		state.Corrupt = true
		return state, nil
	}
	state.Definitions = len(catalog.Queries)
	return state, nil
}

func validateCatalog(catalog persistedCatalog, account domain.AccountID) error {
	if catalog.SchemaVersion != schemaVersion ||
		len(catalog.Queries) > application.MaxSavedQueries {
		return errors.New("saved query catalog failed structural validation")
	}
	seen := make(map[string]struct{}, len(catalog.Queries))
	for _, query := range catalog.Queries {
		if query.Account != account || query.Revision == "" {
			return errors.New("saved query catalog crossed its account boundary")
		}
		if err := query.Validate(); err != nil {
			return fmt.Errorf("validate saved query catalog: %w", err)
		}
		if _, duplicate := seen[query.Name]; duplicate {
			return errors.New("saved query catalog contains a duplicate name")
		}
		seen[query.Name] = struct{}{}
	}
	return nil
}

func save(path string, catalog persistedCatalog) error {
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("encode saved query catalog: %w", err)
	}
	if len(encoded) > maximumStateBytes {
		return errors.New("saved query catalog exceeds its size bound")
	}
	directory := filepath.Dir(path)
	if _, err := os.Lstat(directory); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create saved query directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect saved query directory: %w", err)
	}
	root, err := openCatalogRoot(directory)
	if err != nil {
		return fmt.Errorf("open saved query directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.Chmod(".", 0o700); err != nil { // #nosec G302 -- private query definitions.
		return fmt.Errorf("protect saved query directory: %w", err)
	}
	name := filepath.Base(path)
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return errors.New("saved query catalog path is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect saved query catalog path: %w", err)
	}
	temporaryName, err := privateTemporaryName()
	if err != nil {
		return err
	}
	temporary, err := root.OpenFile(
		temporaryName,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create temporary saved query catalog: %w", err)
	}
	defer func() { _ = root.Remove(temporaryName) }()
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write saved query catalog: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync saved query catalog: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close saved query catalog: %w", err)
	}
	if err := root.Rename(temporaryName, name); err != nil {
		return fmt.Errorf("replace saved query catalog: %w", err)
	}
	if err := root.Chmod(name, 0o600); err != nil { // #nosec G302 -- owner-only private state.
		return fmt.Errorf("protect saved query catalog: %w", err)
	}
	return syncDirectory(root)
}

func openCatalogRoot(directory string) (*os.Root, error) {
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("saved query directory is not a regular directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	openedDirectory, err := root.Stat(".")
	if err != nil || !os.SameFile(directoryInfo, openedDirectory) {
		_ = root.Close()
		return nil, errors.New("saved query directory changed while opening")
	}
	return root, nil
}

func syncDirectory(root *os.Root) error {
	directoryHandle, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open saved query directory for sync: %w", err)
	}
	defer func() { _ = directoryHandle.Close() }()
	if err := directoryHandle.Sync(); err != nil && !isUnsupportedDirectorySync(err) {
		return fmt.Errorf("sync saved query directory: %w", err)
	}
	return nil
}

func privateTemporaryName() (string, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("create saved query temporary name: %w", err)
	}
	return ".saved-queries-" + hex.EncodeToString(suffix[:]) + ".tmp", nil
}

func isUnsupportedDirectorySync(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "invalid argument") ||
		strings.Contains(message, "not supported") ||
		strings.Contains(message, "incorrect function")
}

func sortQueries(queries []application.SavedQueryDefinition) {
	slices.SortFunc(queries, func(left, right application.SavedQueryDefinition) int {
		return strings.Compare(left.Name, right.Name)
	})
}

func cloneQueries(
	queries []application.SavedQueryDefinition,
) []application.SavedQueryDefinition {
	cloned := make([]application.SavedQueryDefinition, len(queries))
	for index, query := range queries {
		cloned[index] = query
		if query.Mail != nil {
			mail := *query.Mail
			cloned[index].Mail = &mail
		}
		if query.Calendar != nil {
			calendar := *query.Calendar
			cloned[index].Calendar = &calendar
		}
	}
	return cloned
}
