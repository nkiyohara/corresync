package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

func TestClearAccountProfilesIsolatesStorageAndHonorsOwnership(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	const account domain.AccountID = "acc_00000000000000000000000000000001"
	const other domain.AccountID = "acc_00000000000000000000000000000002"
	profile, err := paths.ProfileDir(account)
	if err != nil {
		t.Fatal(err)
	}
	otherProfile, err := paths.ProfileDir(other)
	if err != nil {
		t.Fatal(err)
	}
	state, err := paths.AccountStateDir(account)
	if err != nil {
		t.Fatal(err)
	}
	teams, err := paths.ProviderProfileDir(account, domain.ProviderID(domain.MessagingProviderMicrosoftTeams))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{profile, otherProfile, state, teams} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "synthetic"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := LockProfile(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	err = ClearAccountProfiles(ctx, account)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active profile was cleared: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "SingletonLock"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ClearAccountProfiles(t.Context(), account); err == nil {
		t.Fatal("older browser marker ignored")
	}
	if err := os.Remove(filepath.Join(profile, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	if err := ClearAccountProfiles(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{profile, teams} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("profile remained: %v", err)
		}
	}
	for _, dir := range []string{otherProfile, state} {
		if _, err := os.Stat(filepath.Join(dir, "synthetic")); err != nil {
			t.Fatalf("unrelated storage removed: %v", err)
		}
	}
	if err := ClearAccountProfiles(t.Context(), account); err != nil {
		t.Fatal(err)
	}
}

func TestClearAccountProfilesRejectsLinkedProfile(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	const account domain.AccountID = "acc_00000000000000000000000000000001"
	profile, err := paths.ProfileDir(account)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(profile), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "synthetic")
	if err := os.WriteFile(marker, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, profile); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ClearAccountProfiles(t.Context(), account); err == nil {
		t.Fatal("linked profile accepted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("symlink destination changed")
	}
}

func TestPreparingProfileDoesNotRecreateStorageWhileClearOwnsLock(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile")
	lock, err := LockProfile(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := prepareProfile(ctx, profile); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy profile preparation = %v", err)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile recreated before taking ownership: %v", err)
	}
}
