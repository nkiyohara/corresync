//go:build darwin

package localipc

import "golang.org/x/sys/unix"

func unixStatDevice(stat unix.Stat_t) uint64 {
	// #nosec G115 -- Darwin exposes dev_t as int32; widening preserves the
	// kernel identifier for equality checks and does not size memory or files.
	return uint64(stat.Dev)
}

func unixStatMode(stat unix.Stat_t) uint32 {
	return uint32(stat.Mode)
}
