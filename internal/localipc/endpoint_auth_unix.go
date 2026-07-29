//go:build linux || darwin

package localipc

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type unixNodeIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
	uid    uint32
}

func identityOf(stat unix.Stat_t) unixNodeIdentity {
	return unixNodeIdentity{
		device: unixStatDevice(stat),
		inode:  stat.Ino,
		mode:   unixStatMode(stat),
		uid:    stat.Uid,
	}
}

type endpointGuard struct {
	endpoint Endpoint
	uid      uint32
	dirFD    int
	lockFD   int
	dir      unixNodeIdentity
	socket   unixNodeIdentity
}

func validateAndPinEndpoint(endpoint Endpoint) (*endpointGuard, error) {
	if endpoint.runtimeDir == "" || endpoint.lockPath == "" ||
		filepath.Dir(endpoint.Address) != endpoint.runtimeDir ||
		filepath.Dir(endpoint.lockPath) != endpoint.runtimeDir {
		return nil, errors.New("unix endpoint paths do not share one runtime directory")
	}
	uid, err := currentEffectiveUID()
	if err != nil {
		return nil, err
	}
	dirFD, err := unix.Open(
		endpoint.runtimeDir,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open runtime directory without following links: %w", err)
	}
	guard := &endpointGuard{
		endpoint: endpoint, uid: uid, dirFD: dirFD, lockFD: -1,
	}
	closeOnError := func(failure error) (*endpointGuard, error) {
		return nil, errors.Join(failure, guard.Close())
	}
	var directory unix.Stat_t
	if err := unix.Fstat(dirFD, &directory); err != nil {
		return closeOnError(err)
	}
	if err := validateUnixNode(directory, uid, unix.S_IFDIR, "runtime directory"); err != nil {
		return closeOnError(err)
	}
	guard.dir = identityOf(directory)

	lockFD, err := unix.Openat(
		dirFD,
		filepath.Base(endpoint.lockPath),
		unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return closeOnError(fmt.Errorf("open singleton lock: %w", err))
	}
	guard.lockFD = lockFD
	var lock unix.Stat_t
	if err := unix.Fstat(lockFD, &lock); err != nil {
		return closeOnError(err)
	}
	if err := validateUnixNode(lock, uid, unix.S_IFREG, "singleton lock"); err != nil {
		return closeOnError(err)
	}
	if err := requireHeldLock(lockFD); err != nil {
		return closeOnError(err)
	}

	socket, err := statEndpointSocket(dirFD, filepath.Base(endpoint.Address))
	if err != nil {
		return closeOnError(err)
	}
	if err := validateUnixNode(socket, uid, unix.S_IFSOCK, "endpoint socket"); err != nil {
		return closeOnError(err)
	}
	guard.socket = identityOf(socket)
	return guard, nil
}

func (guard *endpointGuard) ValidateConnected(connection *net.UnixConn) error {
	if connection == nil {
		return errors.New("connected Unix socket is required")
	}
	var currentDirectory unix.Stat_t
	if err := unix.Lstat(guard.endpoint.runtimeDir, &currentDirectory); err != nil {
		return fmt.Errorf("reinspect runtime directory: %w", err)
	}
	if identityOf(currentDirectory) != guard.dir {
		return errors.New("IPC runtime directory changed while connecting")
	}
	currentSocket, err := statEndpointSocket(
		guard.dirFD,
		filepath.Base(guard.endpoint.Address),
	)
	if err != nil {
		return err
	}
	if identityOf(currentSocket) != guard.socket {
		return errors.New("IPC socket changed while connecting")
	}
	peer, err := peerUID(connection)
	if err != nil {
		return err
	}
	if peer != guard.uid {
		return errors.New("connected IPC peer is not the current user")
	}
	if err := requireHeldLock(guard.lockFD); err != nil {
		return errors.New("IPC singleton ownership changed while connecting")
	}
	return nil
}

func (guard *endpointGuard) Close() error {
	var failures []error
	if guard.lockFD >= 0 {
		failures = append(failures, unix.Close(guard.lockFD))
		guard.lockFD = -1
	}
	if guard.dirFD >= 0 {
		failures = append(failures, unix.Close(guard.dirFD))
		guard.dirFD = -1
	}
	return errors.Join(failures...)
}

func statEndpointSocket(directoryFD int, name string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(
		directoryFD,
		name,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return unix.Stat_t{}, fmt.Errorf("inspect endpoint socket: %w", err)
	}
	return stat, nil
}

func validateUnixNode(
	stat unix.Stat_t,
	effectiveUID uint32,
	expectedType uint32,
	name string,
) error {
	if unixStatMode(stat)&unix.S_IFMT != expectedType {
		return fmt.Errorf("%s has an unexpected file type", name)
	}
	if stat.Uid != effectiveUID {
		return fmt.Errorf("%s is not owned by the current user", name)
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("%s grants group or other access", name)
	}
	return nil
}

func requireHeldLock(fd int) error {
	err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		unlockErr := unix.Flock(fd, unix.LOCK_UN)
		return errors.Join(
			errors.New("IPC singleton lock is not held by an active owner"),
			unlockErr,
		)
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return nil
	}
	return fmt.Errorf("inspect IPC singleton lock: %w", err)
}
