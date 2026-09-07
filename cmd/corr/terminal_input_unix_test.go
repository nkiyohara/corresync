//go:build !windows

package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

type terminalReadyOutput struct {
	ready chan struct{}
	once  sync.Once
}

func (output *terminalReadyOutput) Write(value []byte) (int, error) {
	output.once.Do(func() { close(output.ready) })
	return len(value), nil
}

func TestTerminalLoginRestoresTTYOnInterrupt(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output := &terminalReadyOutput{ready: make(chan struct{})}
	app := &runtime{context: ctx, stdin: slave, stdout: output}
	client := &terminalLoginClientStub{}
	done := make(chan error, 1)
	go func() { done <- runTerminalLogin(app, client, "test-account") }()
	select {
	case <-output.ready:
	case <-ctx.Done():
		t.Fatal("terminal login did not start")
	}
	// Cancel while waiting for a selection; this must interrupt the read and
	// join its goroutine before restoring the terminal and returning.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupt error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal read remained blocked after cancellation")
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("TTY settings were not restored: %v", err)
	}
}

func TestTerminalInputInterruptsBlockedRead(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	reader, err := cancelreader.NewReader(slave)
	if err != nil {
		t.Fatal(err)
	}
	_, closeReader := startTerminalInput(reader, func() {})
	done := make(chan error, 1)
	go func() { done <- closeReader() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader cleanup did not unblock stdin")
	}
}
