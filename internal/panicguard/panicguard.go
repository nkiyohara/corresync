// Package panicguard records content-free evidence at owned goroutine
// boundaries before preserving Go's fail-closed panic behavior.
package panicguard

import (
	"context"
	"runtime"
)

const maximumCallers = 64

// Boundary is a closed, content-free crash location.
type Boundary string

const (
	BoundaryProcess        Boundary = "process"
	BoundaryDaemonRequest  Boundary = "daemon_request"
	BoundaryDaemonServer   Boundary = "daemon_server"
	BoundaryMonitor        Boundary = "monitor"
	BoundaryBackgroundWork Boundary = "background_work"
)

// Recorder receives only a fixed boundary and program counters. The recovered
// panic value is deliberately absent from this contract.
type Recorder func(Boundary, []uintptr)

type recorderKey struct{}

// WithRecorder attaches one process-owned recorder without changing
// cancellation or request values.
func WithRecorder(ctx context.Context, recorder Recorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderKey{}, recorder)
}

// Record captures a bounded stack for an already recovered panic.
func Record(ctx context.Context, boundary Boundary) {
	recorder, _ := ctx.Value(recorderKey{}).(Recorder)
	if recorder == nil {
		return
	}
	callers := make([]uintptr, maximumCallers)
	count := runtime.Callers(2, callers)
	recorder(boundary, callers[:count])
}

// Recover records a panic at a directly deferred boundary and re-raises it.
// Callers must use this function only as a defer target.
func Recover(ctx context.Context, boundary Boundary) {
	if recovered := recover(); recovered != nil {
		Record(ctx, boundary)
		panic(recovered)
	}
}

// Go starts owned background work. A panic is recorded without its value and
// then re-raised so the process never continues in an uncertain state.
func Go(ctx context.Context, boundary Boundary, work func()) {
	go func() {
		defer Recover(ctx, boundary)
		work()
	}()
}
