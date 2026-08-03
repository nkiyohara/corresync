//go:build linux

package localipc

import "golang.org/x/sys/unix"

func signalPeerProcess(processID int) error {
	process, err := unix.PidfdOpen(processID, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(process) }()
	return unix.PidfdSendSignal(process, unix.SIGTERM, nil, 0)
}
