package config

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestUpdatePersistsValidatedMutation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := Default()
	if err := Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	if err := Update(context.Background(), path, func(current *Config) error {
		current.Policy.MaxRecipients = 7
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Policy.MaxRecipients != 7 {
		t.Fatalf("MaxRecipients = %d", updated.Policy.MaxRecipients)
	}
}

func TestUpdateDoesNotPersistRejectedMutation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := Default()
	if err := Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("stop")
	if err := Update(context.Background(), path, func(current *Config) error {
		current.DefaultAccount = "missing"
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Update() error = %v", err)
	}
	updated, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DefaultAccount != configuration.DefaultAccount {
		t.Fatalf("DefaultAccount = %q", updated.DefaultAccount)
	}
}
