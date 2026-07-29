package config

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/nkiyohara/corresync/internal/filelock"
)

// Update serializes a read-modify-write transaction against the latest
// complete configuration. The mutator receives a validated copy and must not
// retain it after returning.
func Update(ctx context.Context, path string, mutate func(*Config) error) (returnErr error) {
	if mutate == nil {
		return errors.New("config mutator is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve config update path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	lock, err := filelock.Acquire(ctx, absolute+".lock")
	if err != nil {
		return fmt.Errorf("acquire config update lock: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()

	configuration, err := Load(absolute)
	if err != nil {
		return err
	}
	if err := mutate(&configuration); err != nil {
		return err
	}
	return Save(absolute, configuration)
}
