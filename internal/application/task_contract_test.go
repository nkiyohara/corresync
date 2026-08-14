package application

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalTaskJSONFixtures(t *testing.T) {
	t.Parallel()

	var create TaskCreateInput
	decodeTaskContractFixture(t, "task-create-v1.json", &create)
	if create.Account != "" {
		t.Fatal("create fixture must leave account selection to the calling surface")
	}
	create.Account = testTaskAccount
	if err := create.Validate(); err != nil {
		t.Fatalf("create fixture validation error = %v", err)
	}
	if len(create.Checklist) != 1 || create.Checklist[0].ID != "" {
		t.Fatalf("create fixture must permit a provider-assigned checklist ID: %+v", create.Checklist)
	}

	var task Task
	decodeTaskContractFixture(t, "task-v1.json", &task)
	if err := task.Validate(); err != nil {
		t.Fatalf("task fixture validation error = %v", err)
	}
	if task.ID != "task-synthetic-001" || task.Provenance.AccountID != testTaskAccount ||
		len(task.Sources) != 1 || task.Sources[0].Kind != TaskSourceMail ||
		len(task.Degradations) != 1 || !task.Degradations[0].Lossy {
		t.Fatalf("task fixture lost canonical fields: %+v", task)
	}
}

func decodeTaskContractFixture(t *testing.T, name string, destination any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", name)) // #nosec G304 -- fixed synthetic fixture name.
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("%s contains trailing JSON: %v", name, err)
	}
}
