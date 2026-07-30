//go:build darwin

package localipc

import "golang.org/x/sys/unix"

func unixStatDevice(stat unix.Stat_t) uint64 {
	// #nosec G115 -- dev_t is a signed 32-bit C type; preserve its raw identifier bits.
	return uint64(uint32(stat.Dev))
}

func unixStatMode(stat unix.Stat_t) uint32 {
	return uint32(stat.Mode)
}
