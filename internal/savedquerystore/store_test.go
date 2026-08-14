package savedquerystore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	storeTestAccount domain.AccountID = "acc_00000000000000000000000000000061"
	otherTestAccount domain.AccountID = "acc_00000000000000000000000000000062"
)

func TestStoreRoundTripsAndIsolatesPrivateDefinitions(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	query := storeTestQuery()
	if err := store.PutSavedQuery(t.Context(), query, ""); err != nil {
		t.Fatal(err)
	}
	queries, err := store.ListSavedQueries(t.Context(), storeTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || queries[0].Name != "priority" ||
		queries[0].Mail.Query != "is:unread" {
		t.Fatalf("saved query round trip = %+v", queries)
	}
	queries[0].Mail.Query = "mutated"
	again, err := store.ListSavedQueries(t.Context(), storeTestAccount)
	if err != nil || again[0].Mail.Query != "is:unread" {
		t.Fatalf("saved query store leaked mutable state: %+v, %v", again, err)
	}
	other, err := store.ListSavedQueries(t.Context(), otherTestAccount)
	if err != nil || len(other) != 0 {
		t.Fatalf("other account catalog = %+v, %v", other, err)
	}
	info, err := os.Stat(filepath.Join(root, string(storeTestAccount), "saved-queries.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved query mode = %o", info.Mode().Perm())
	}
}

func TestStoreRejectsStaleReplacementAndDeletion(t *testing.T) {
	store := NewAt(t.TempDir())
	query := storeTestQuery()
	if err := store.PutSavedQuery(t.Context(), query, ""); err != nil {
		t.Fatal(err)
	}
	replacement := storeTestQuery()
	replacement.Mail.Limit = 10
	replacement.Revision = testRevision(replacement)
	if err := store.PutSavedQuery(t.Context(), replacement, strings.Repeat("0", 64)); err == nil {
		t.Fatal("store accepted a stale replacement")
	}
	if err := store.DeleteSavedQuery(
		t.Context(), storeTestAccount, "priority", strings.Repeat("0", 64),
	); err == nil {
		t.Fatal("store accepted a stale deletion")
	}
	if err := store.DeleteSavedQuery(
		t.Context(), storeTestAccount, "priority", query.Revision,
	); err != nil {
		t.Fatal(err)
	}
}

func TestStoreFailsClosedForMalformedOrLinkedCatalog(t *testing.T) {
	root := t.TempDir()
	accountDirectory := filepath.Join(root, string(storeTestAccount))
	if err := os.MkdirAll(accountDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(accountDirectory, "saved-queries.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"queries":[],"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewAt(root)
	if _, err := store.ListSavedQueries(t.Context(), storeTestAccount); err == nil {
		t.Fatal("store accepted unknown persisted fields")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte(`{"schemaVersion":1,"queries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.ListSavedQueries(t.Context(), storeTestAccount); err == nil {
		t.Fatal("store followed a linked saved query catalog")
	}
}

func TestStorePurgesExactReviewedCatalogIncludingCorruption(t *testing.T) {
	root := t.TempDir()
	store := NewAt(root)
	query := storeTestQuery()
	if err := store.PutSavedQuery(t.Context(), query, ""); err != nil {
		t.Fatal(err)
	}
	state, err := store.InspectSavedQueryCatalog(t.Context(), storeTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision == "" || state.Definitions != 1 || state.Corrupt {
		t.Fatalf("catalog state = %+v", state)
	}
	path := filepath.Join(root, string(storeTestAccount), "saved-queries.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"queries":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.PurgeSavedQueryCatalog(
		t.Context(), storeTestAccount, state.Revision,
	); err == nil {
		t.Fatal("store purged a catalog changed after review")
	}
	corrupt, err := store.InspectSavedQueryCatalog(t.Context(), storeTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	if !corrupt.Corrupt || corrupt.Revision == "" || corrupt.Definitions != 0 {
		t.Fatalf("corrupt catalog state = %+v", corrupt)
	}
	if err := store.PurgeSavedQueryCatalog(
		t.Context(), storeTestAccount, corrupt.Revision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("purged catalog stat error = %v", err)
	}
}

func storeTestQuery() application.SavedQueryDefinition {
	query := application.SavedQueryDefinition{
		Version: application.SavedQueryDefinitionVersion,
		Account: storeTestAccount, Name: "priority", Kind: application.SavedQueryMail,
		Mail: &application.SavedMailQuery{
			Folder: application.MailFolder{
				Kind: application.MailFolderDistinguished, ID: "inbox",
			},
			Query: "is:unread", Limit: 25, TimeZone: "UTC",
		},
	}
	query.Revision = testRevision(query)
	return query
}

func testRevision(query application.SavedQueryDefinition) string {
	service, err := application.NewSavedQueryService(&revisionRepository{}, nil)
	if err != nil {
		panic(err)
	}
	review, err := service.ReviewSave(context.Background(), application.SavedQuerySaveInput{
		Account: query.Account, Name: query.Name, Kind: query.Kind,
		Mail: query.Mail, Calendar: query.Calendar,
	})
	if err != nil {
		panic(err)
	}
	return review.Definition.Revision
}

type revisionRepository struct{}

func (*revisionRepository) ListSavedQueries(
	context.Context,
	domain.AccountID,
) ([]application.SavedQueryDefinition, error) {
	return nil, nil
}

func (*revisionRepository) PutSavedQuery(
	context.Context,
	application.SavedQueryDefinition,
	string,
) error {
	return nil
}

func (*revisionRepository) DeleteSavedQuery(
	context.Context,
	domain.AccountID,
	string,
	string,
) error {
	return nil
}

func (*revisionRepository) InspectSavedQueryCatalog(
	context.Context,
	domain.AccountID,
) (application.SavedQueryCatalogState, error) {
	return application.SavedQueryCatalogState{}, nil
}

func (*revisionRepository) PurgeSavedQueryCatalog(
	context.Context,
	domain.AccountID,
	string,
) error {
	return nil
}
