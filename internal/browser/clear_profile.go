package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

// ClearAccountProfiles removes only the two application-owned browser profiles
// for an exact opaque account. It never removes account caches or keyring data.
func ClearAccountProfiles(ctx context.Context, account domain.AccountID) error {
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	state, err := paths.StateDir()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(state)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	outlook, err := paths.ProfileDir(account)
	if err != nil {
		return err
	}
	teams, err := paths.ProviderProfileDir(account, domain.ProviderID(domain.MessagingProviderMicrosoftTeams))
	if err != nil {
		return err
	}
	for _, profile := range []string{outlook, teams} {
		relative, err := filepath.Rel(state, profile)
		if err != nil {
			return err
		}
		parent, err := openProfileParent(root, relative)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		lock, err := LockProfile(ctx, profile)
		if err != nil {
			_ = parent.Close()
			return err
		}
		clearErr := clearLockedBrowserProfile(ctx, parent, filepath.Base(relative))
		closeErr := errors.Join(lock.Close(), parent.Close())
		if err := errors.Join(clearErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

// Pin every directory before traversing onward; replacing an ancestor with a
// symlink cannot redirect deletion into another account or outside state.
func openProfileParent(root *os.Root, relative string) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(filepath.Dir(relative), string(filepath.Separator))
	for _, part := range parts {
		info, err := current.Lstat(part)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = current.Close()
			return nil, errors.New("refusing linked browser profile ancestors")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		opened, err := next.Stat(".")
		_ = current.Close()
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			return nil, errors.New("browser profile ancestor changed while opening")
		}
		current = next
	}
	return current, nil
}

func clearLockedBrowserProfile(ctx context.Context, root *os.Root, relative string) error {
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to clear a non-directory or linked browser profile")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Older Chromium owners do not hold our sidecar; never remove their profile
	// while Chromium's singleton marker remains, including a stale marker.
	if _, err := root.Lstat(filepath.Join(relative, "SingletonLock")); err == nil {
		return errors.New("browser singleton marker remains; close its browser before clearing saved sign-in")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.RemoveAll(relative); err != nil {
		return fmt.Errorf("clear account browser storage: %w", err)
	}
	return nil
}
