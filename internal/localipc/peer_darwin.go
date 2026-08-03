//go:build darwin

package localipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			controlErr = err
			return
		}
		uid = credentials.Uid
	}); err != nil {
		return 0, err
	}
	if controlErr != nil {
		return 0, fmt.Errorf("read local IPC peer credentials: %w", controlErr)
	}
	return uid, nil
}

func peerProcessID(connection *net.UnixConn) (int, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var processID int
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		processID, controlErr = unix.GetsockoptInt(
			int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID,
		)
	}); err != nil {
		return 0, err
	}
	if controlErr != nil {
		return 0, fmt.Errorf("read local IPC peer process: %w", controlErr)
	}
	return processID, nil
}
