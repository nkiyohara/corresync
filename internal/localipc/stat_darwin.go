//go:build darwin

package localipc

import "golang.org/x/sys/unix"

func unixStatDevice(stat unix.Stat_t) uint64 {
	return uint64(stat.Dev)
}

func unixStatMode(stat unix.Stat_t) uint32 {
	return uint32(stat.Mode)
}
