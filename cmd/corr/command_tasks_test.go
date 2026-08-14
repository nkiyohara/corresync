package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestTaskCreateContractFixtureUsesStrictCanonicalJSON(t *testing.T) {
	t.Parallel()
	var input application.TaskCreateInput
	if err := readTaskJSON(
		&runtime{},
		filepath.Join("..", "..", "testdata", "contracts", "task-create-v1.json"),
		&input,
	); err != nil {
		t.Fatal(err)
	}
	if input.Account != "" {
		t.Fatal("task fixture selected an account instead of leaving CLI routing explicit")
	}
	input.Account = "acc_00000000000000000000000000000001"
	if err := input.Validate(); err != nil {
		t.Fatalf("fixture validation error = %v", err)
	}

	unknown := bytes.NewBufferString(`{"listId":"list-1","title":"Synthetic","priority":"none","providerAction":{}}`)
	if err := readTaskJSON(&runtime{stdin: unknown}, "-", &application.TaskCreateInput{}); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict task JSON error = %v", err)
	}
}

func TestWriteTaskReviewIncludesExactRouteAndLoss(t *testing.T) {
	t.Parallel()
	review := application.TaskWriteReview{
		Account:  "acc_00000000000000000000000000000001",
		Provider: domain.ProviderTodoist,
		Action:   "update", ListID: "list-1", TaskID: "task-1", Version: "version-1",
		Degradations: []domain.Degradation{{
			Feature: "priority_mapping", Reason: "Synthetic priority mapping is lossy.", Lossy: true,
		}},
	}
	var output bytes.Buffer
	if err := writeTaskReview(&output, review, false); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Preview task update", `"account": "acc_00000000000000000000000000000001"`,
		`"provider": "todoist"`, `"version": "version-1"`, `"lossy": true`,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("task review omitted %q: %s", expected, output.String())
		}
	}
}
