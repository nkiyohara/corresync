package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestClassifyProbeErrorKeepsRawBrowserFailurePrivate(t *testing.T) {
	t.Parallel()
	private := "/home/person/.cache/chrome/chrome"
	for _, test := range []struct {
		name string
		goos string
		err  error
		want error
	}{
		{
			name: "AppArmor user namespace", goos: "linux",
			err:  errors.New("Failed to move to new namespace: Operation not permitted " + private),
			want: ErrLinuxSandboxUnavailable,
		},
		{
			name: "setuid helper", goos: "linux",
			err:  errors.New("No usable sandbox! SUID sandbox helper " + private),
			want: ErrLinuxSandboxUnavailable,
		},
		{
			name: "generic Linux", goos: "linux",
			err: errors.New("missing shared object " + private), want: errBrowserProbeFailed,
		},
		{
			name: "unrelated permission failure", goos: "linux",
			err:  errors.New("fork/exec " + private + ": operation not permitted"),
			want: errBrowserProbeFailed,
		},
		{
			name: "other platform", goos: "darwin",
			err: errors.New("launch failed " + private), want: errBrowserProbeFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := classifyProbeError(context.Background(), test.err, test.goos)
			if !errors.Is(got, test.want) {
				t.Fatalf("classifyProbeError() = %v, want %v", got, test.want)
			}
			if strings.Contains(got.Error(), private) {
				t.Fatalf("classified error exposed private path: %v", got)
			}
		})
	}
}

func TestClassifyProbeErrorReportsTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(context.DeadlineExceeded)
	if got := classifyProbeError(ctx, errors.New("private stderr"), "linux"); !errors.Is(got, errBrowserProbeTimeout) {
		t.Fatalf("classifyProbeError() = %v", got)
	}
}

func TestClassifyProbeErrorReportsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := classifyProbeError(ctx, errors.New("private stderr"), "linux"); !errors.Is(got, errBrowserProbeCanceled) {
		t.Fatalf("classifyProbeError() = %v", got)
	}
}
