package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nkiyohara/corresync/internal/filelock"
)

// LockProfile serializes browser ownership and explicit profile clearing.
// The sidecar remains outside the profile so deleting storage never drops it.
func LockProfile(ctx context.Context, profile string) (*filelock.Lock, error) {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	lock, err := filelock.AcquireSidecar(bounded, profile+".owner.lock")
	if err != nil {
		return nil, fmt.Errorf("browser profile is busy or unavailable; close its other Corresync owner first: %w", err)
	}
	return lock, nil
}

func prepareProfile(ctx context.Context, profile string) (*filelock.Lock, error) {
	lock, err := LockProfile(ctx, profile)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*filelock.Lock, error) { return nil, errors.Join(err, lock.Close()) }
	if err := os.MkdirAll(profile, 0700); err != nil {
		return fail(fmt.Errorf("create browser profile: %w", err))
	}
	info, err := os.Lstat(profile)
	if err != nil {
		return fail(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail(errors.New("browser profile must be an unlinked directory"))
	}
	if err := os.Chmod(profile, 0700); err != nil {
		return fail(fmt.Errorf("protect browser profile: %w", err))
	} // #nosec G302 -- private browser directory requires owner execute.
	return lock, nil
}
