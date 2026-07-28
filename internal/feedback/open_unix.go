//go:build linux || darwin

package feedback

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openDiagnosticRecord(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoRecord
	}
	if err != nil {
		return nil, fmt.Errorf("%w: open record without following links", ErrInvalidRecord)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(ErrInvalidRecord, err, file.Close())
	}
	effectiveUID := os.Geteuid()
	if effectiveUID < 0 {
		return nil, errors.Join(ErrInvalidRecord, file.Close())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 ||
		stat.Uid != uint32(effectiveUID) { // #nosec G115 -- checked non-negative OS UID.
		return nil, errors.Join(ErrInvalidRecord, file.Close())
	}
	return file, nil
}
