package panicguard

import (
	"context"
	"testing"
)

func TestRecordExcludesRecoveredValue(t *testing.T) {
	t.Parallel()

	var gotBoundary Boundary
	var gotCallers []uintptr
	ctx := WithRecorder(context.Background(), func(boundary Boundary, callers []uintptr) {
		gotBoundary = boundary
		gotCallers = append([]uintptr(nil), callers...)
	})
	Record(ctx, BoundaryDaemonRequest)
	if gotBoundary != BoundaryDaemonRequest || len(gotCallers) == 0 || len(gotCallers) > maximumCallers {
		t.Fatalf("recorded panic metadata = %q, %d callers", gotBoundary, len(gotCallers))
	}
}

func TestRecoverRecordsAndPreservesPanic(t *testing.T) {
	secret := "synthetic-private-panic"
	recorded := 0
	ctx := WithRecorder(context.Background(), func(Boundary, []uintptr) { recorded++ })
	func() {
		defer func() {
			if recovered := recover(); recovered != secret {
				t.Fatalf("recovered value = %v", recovered)
			}
		}()
		defer Recover(ctx, BoundaryBackgroundWork)
		panic(secret)
	}()
	if recorded != 1 {
		t.Fatalf("recorder calls = %d, want 1", recorded)
	}
}
