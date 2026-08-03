//go:build darwin

package localipc

import "golang.org/x/sys/unix"

func signalPeerProcess(processID int) error {
	return unix.Kill(processID, unix.SIGTERM)
}
