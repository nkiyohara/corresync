package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/savedquerystore"
)

func TestSavedQueryCLIReviewsBeforePrivateStateChanges(t *testing.T) {
	app, path, stdout := newAccountCommandRuntime(t, &accountDiscovererStub{})
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configuration.Accounts[configuration.DefaultAccount].ID
	service, err := application.NewSavedQueryService(savedquerystore.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	command := savedMailQueryCommand{
		Name: "priority", Query: "subject:\x1b[31msynthetic",
		Folder: "inbox", Limit: 25, TimeZone: "UTC",
	}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	catalog, err := service.List(t.Context(), account)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Queries) != 0 {
		t.Fatalf("preview wrote saved query state: %+v", catalog.Queries)
	}
	if strings.Contains(stdout.String(), "\x1b[31m") ||
		!strings.Contains(stdout.String(), `\x1b[31m`) ||
		!strings.Contains(stdout.String(), "No changes made") {
		t.Fatalf("unsafe or incomplete saved query preview: %q", stdout.String())
	}

	stdout.Reset()
	command.Approve = true
	command.JSON = true
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	catalog, err = service.List(t.Context(), account)
	if err != nil || len(catalog.Queries) != 1 ||
		catalog.Queries[0].Mail.Query != command.Query {
		t.Fatalf("committed saved query catalog = %+v error = %v", catalog, err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status": "completed"`)) {
		t.Fatalf("saved query JSON commit = %s", stdout.String())
	}

	stdout.Reset()
	deletion := savedQueryDeleteCommand{Name: "priority"}
	if err := deletion.Run(app); err != nil {
		t.Fatal(err)
	}
	catalog, err = service.List(t.Context(), account)
	if err != nil || len(catalog.Queries) != 1 {
		t.Fatalf("delete preview changed catalog = %+v error = %v", catalog, err)
	}
	deletion.Approve = true
	if err := deletion.Run(app); err != nil {
		t.Fatal(err)
	}
	catalog, err = service.List(t.Context(), account)
	if err != nil || len(catalog.Queries) != 0 {
		t.Fatalf("delete commit catalog = %+v error = %v", catalog, err)
	}
}

func TestSavedCalendarQueryCLIRequiresWholeMinuteWindows(t *testing.T) {
	app, _, _ := newAccountCommandRuntime(t, &accountDiscovererStub{})
	command := savedCalendarQueryCommand{
		Name: "week", StartOffset: 30 * time.Second, Window: 7 * 24 * time.Hour,
		TimeZone: "UTC",
	}
	if err := command.Run(app); err == nil || !strings.Contains(err.Error(), "whole minutes") {
		t.Fatalf("sub-minute start offset error = %v", err)
	}
}
