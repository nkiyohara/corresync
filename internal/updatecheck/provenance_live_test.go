package updatecheck

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestPublishedProvenanceOptIn(t *testing.T) {
	manifestPath := os.Getenv("CORRESYNC_TEST_UPDATE_MANIFEST")
	bundlePath := os.Getenv("CORRESYNC_TEST_UPDATE_BUNDLE")
	workflowIdentity := os.Getenv("CORRESYNC_TEST_UPDATE_IDENTITY")
	if manifestPath == "" || bundlePath == "" || workflowIdentity == "" {
		t.Skip("set CORRESYNC_TEST_UPDATE_MANIFEST, CORRESYNC_TEST_UPDATE_BUNDLE, and CORRESYNC_TEST_UPDATE_IDENTITY")
	}
	if err := VerifyProvenance(
		t.Context(),
		manifestPath,
		bundlePath,
		workflowIdentity,
		t.TempDir(),
	); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedSelfUpdateOptIn(t *testing.T) {
	target := os.Getenv("CORRESYNC_TEST_SELF_UPDATE_TARGET")
	currentVersion := os.Getenv("CORRESYNC_TEST_SELF_UPDATE_CURRENT")
	if target == "" || currentVersion == "" {
		t.Skip("set CORRESYNC_TEST_SELF_UPDATE_TARGET and CORRESYNC_TEST_SELF_UPDATE_CURRENT")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	result, err := (Installer{
		CurrentVersion: currentVersion,
		Executable:     target,
		TrustCachePath: t.TempDir(),
		Client:         &http.Client{Timeout: 2 * time.Minute},
	}).Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != InstallStatusUpdated || result.BackupPath == "" {
		t.Fatalf("Install() = %+v", result)
	}
}
