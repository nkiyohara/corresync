package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/muesli/cancelreader"
)

type terminalRune struct {
	value rune
	err   error
}

// One reader owns stdin for the entire interaction. In particular, no
// canonical-mode read can echo queued password characters between screens.
type terminalInput struct {
	events <-chan terminalRune
}

func startTerminalInput(input cancelreader.CancelReader, cancel context.CancelFunc) (*terminalInput, func() error) {
	// Bound pending keystrokes without blocking interrupt detection behind a
	// slow daemon call. Overflow cancels the interaction rather than dropping
	// or accumulating credential input indefinitely.
	events := make(chan terminalRune, 64)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		reader := bufio.NewReader(input)
		for {
			value, _, err := reader.ReadRune()
			if value == 3 {
				cancel()
				return
			}
			select {
			case events <- terminalRune{value: value, err: err}:
			case <-stop:
				return
			default:
				cancel()
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return &terminalInput{events: events}, func() error {
		close(stop)
		if !input.Cancel() {
			// Windows overlapped reads may not support Cancel. Close the
			// reader's owned console handle before waiting, and never hold
			// the caller's terminal restoration hostage to a stuck read.
			closeErr := input.Close()
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-done:
				return closeErr
			case <-timer.C:
				return errors.Join(closeErr, errors.New("terminal input reader did not stop"))
			}
		}
		<-done
		return input.Close()
	}
}

func (input *terminalInput) readRune(ctx context.Context) (rune, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case event, ok := <-input.events:
		if !ok {
			return 0, io.EOF
		}
		return event.value, event.err
	}
}

// Escape alone returns to the menu. Consume a complete CSI/SS3 sequence so
// arrows and function keys cannot exit a password field or become form text.
// Unsupported or truncated sequences fail closed, with a fixed time/size bound.
func (input *terminalInput) readKey(ctx context.Context) (rune, error) {
	value, err := input.readRune(ctx)
	if err != nil || value != 27 {
		return value, err
	}
	sequenceContext, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	value, err = input.readRune(sequenceContext)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil || errors.Is(err, io.EOF) {
		return 27, nil
	}
	if err != nil {
		return 0, err
	}
	if value != '[' && value != 'O' {
		return 0, nil // Alt-modified key: never relay its printable suffix.
	}
	introducer := value
	for index := range 32 {
		value, err = input.readRune(sequenceContext)
		if err != nil {
			return 0, errors.New("incomplete terminal key sequence; login cancelled")
		}
		// Linux console F1-F5 use ESC [[ A through ESC [[ E.
		if index == 0 && introducer == '[' && value == '[' {
			continue
		}
		if value >= 0x40 && value <= 0x7e {
			return 0, nil
		}
		if value < 0x20 || value > 0x3f {
			break
		}
	}
	return 0, errors.New("unsupported terminal key sequence; login cancelled")
}

// Raw mode disables the terminal's normal LF-to-CRLF output processing.
type terminalOutput struct{ io.Writer }

func (output terminalOutput) Write(value []byte) (int, error) {
	_, err := io.WriteString(output.Writer, strings.ReplaceAll(string(value), "\n", "\r\n"))
	if err != nil {
		return 0, err
	}
	return len(value), nil
}
