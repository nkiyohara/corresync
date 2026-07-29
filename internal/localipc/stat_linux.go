//go:build linux

package localipc

import "golang.org/x/sys/unix"

func unixStatDevice(stat unix.Stat_t) uint64 {
	return stat.Dev
}

func unixStatMode(stat unix.Stat_t) uint32 {
	return stat.Mode
}
