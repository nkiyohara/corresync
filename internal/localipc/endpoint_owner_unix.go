//go:build linux || darwin

package localipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
)

// StopEndpointOwner sends a normal termination signal to the exact same-user
// process pinned by a protected Corresync endpoint. It is a narrowly scoped
// recovery path for old split owners whose shared bearer credential was
// rotated by another runtime location. Callers must derive endpoint from the
// selected config and invoke this only for an explicit daemon-stop command.
func StopEndpointOwner(ctx context.Context, endpoint Endpoint) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	connection, err := DialContext(ctx, endpoint)
	if err != nil {
		return 0, fmt.Errorf("connect to protected session-owner endpoint: %w", err)
	}
	defer func() { _ = connection.Close() }()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, errors.New("session-owner endpoint is not a Unix socket")
	}
	processID, err := peerProcessID(unixConnection)
	if err != nil {
		return 0, err
	}
	if processID < 2 || processID == os.Getpid() {
		return 0, errors.New("session-owner endpoint returned an unsafe process ID")
	}
	if err := signalPeerProcess(processID); err != nil {
		return 0, fmt.Errorf("signal pinned session owner PID %d: %w", processID, err)
	}
	return processID, nil
}
