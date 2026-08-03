//go:build linux

package localipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(connection *net.UnixConn) (uint32, error) {
	credentials, err := peerCredentials(connection)
	if err != nil {
		return 0, err
	}
	return credentials.Uid, nil
}

func peerProcessID(connection *net.UnixConn) (int, error) {
	credentials, err := peerCredentials(connection)
	if err != nil {
		return 0, err
	}
	return int(credentials.Pid), nil
}

func peerCredentials(connection *net.UnixConn) (*unix.Ucred, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credentials *unix.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			controlErr = err
		}
	}); err != nil {
		return nil, err
	}
	if controlErr != nil {
		return nil, fmt.Errorf("read local IPC peer credentials: %w", controlErr)
	}
	return credentials, nil
}
