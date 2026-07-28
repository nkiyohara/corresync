//go:build linux || darwin

package localipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const maximumUnixSocketPath = 100

const fallbackUnixTempDir = "/tmp"

func platformEndpoint(
	id,
	runtimeName string,
) (address, runtimeDirectory, lockPath string, err error) {
	effectiveUID, err := currentEffectiveUID()
	if err != nil {
		return "", "", "", err
	}
	if runtimeBase := os.Getenv("XDG_RUNTIME_DIR"); runtimeBase != "" {
		if suitable, _ := suitableRuntimeBase(runtimeBase, effectiveUID); suitable {
			runtimeDirectory = filepath.Join(filepath.Clean(runtimeBase), runtimeName)
			address = filepath.Join(runtimeDirectory, id+".sock")
			if len(address) <= maximumUnixSocketPath {
				return address, runtimeDirectory, filepath.Join(runtimeDirectory, id+".lock"), nil
			}
		}
	}
	temporary, err := currentTempDir()
	if err != nil {
		return "", "", "", err
	}
	return platformEndpointInTemp(temporary, id, int(effectiveUID), runtimeName)
}

func platformEndpointInTemp(
	temporary, id string,
	effectiveUID int,
	runtimeName string,
) (address, runtimeDirectory, lockPath string, err error) {
	runtimeDirectory = filepath.Join(temporary, runtimeName+"-"+strconv.Itoa(effectiveUID))
	address = filepath.Join(runtimeDirectory, id+".sock")
	if len(address) > maximumUnixSocketPath && temporary != fallbackUnixTempDir {
		runtimeDirectory = filepath.Join(
			fallbackUnixTempDir, runtimeName+"-"+strconv.Itoa(effectiveUID),
		)
		address = filepath.Join(runtimeDirectory, id+".sock")
	}
	if len(address) > maximumUnixSocketPath {
		return "", "", "", fmt.Errorf("unix socket path exceeds %d bytes", maximumUnixSocketPath)
	}
	return address, runtimeDirectory, filepath.Join(runtimeDirectory, id+".lock"), nil
}

// Listen creates a singleton, same-effective-user Unix-domain listener.
func Listen(endpoint Endpoint) (*Listener, error) {
	effectiveUID, err := currentEffectiveUID()
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDirectory(endpoint.runtimeDir); err != nil {
		return nil, fmt.Errorf("protect IPC runtime directory: %w", err)
	}
	lock, err := openSingletonLock(endpoint.lockPath, effectiveUID)
	if err != nil {
		return nil, fmt.Errorf("open IPC singleton lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("corresync daemon is already running for this config")
	}
	unlock := func() error {
		return errors.Join(unix.Flock(int(lock.Fd()), unix.LOCK_UN), lock.Close())
	}
	if err := removeStaleSocket(endpoint.Address); err != nil {
		return nil, errors.Join(err, unlock())
	}
	base, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint.Address, Net: "unix"})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("listen on local IPC socket: %w", err), unlock())
	}
	if err := os.Chmod(endpoint.Address, 0o600); err != nil { // #nosec G302 -- owner-only socket.
		return nil, errors.Join(err, base.Close(), os.Remove(endpoint.Address), unlock())
	}
	verified := &sameUserListener{UnixListener: base, uid: effectiveUID}
	cleanup := func() error {
		removeErr := os.Remove(endpoint.Address)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(removeErr, unlock())
	}
	return newListener(verified, cleanup), nil
}

// DialContext connects only to the derived Unix-domain endpoint.
func DialContext(ctx context.Context, endpoint Endpoint) (net.Conn, error) {
	dialer := net.Dialer{}
	return dialAuthenticatedUnix(
		ctx,
		endpoint,
		func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", address)
		},
	)
}

func dialAuthenticatedUnix(
	ctx context.Context,
	endpoint Endpoint,
	dial func(context.Context, string) (net.Conn, error),
) (net.Conn, error) {
	guard, err := validateAndPinEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("authenticate local IPC endpoint: %w", err)
	}
	defer func() { _ = guard.Close() }()
	connection, err := dial(ctx, endpoint.Address)
	if err != nil {
		return nil, err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("local IPC connection is not a Unix socket")
	}
	if err := guard.ValidateConnected(unixConnection); err != nil {
		return nil, errors.Join(
			fmt.Errorf("authenticate connected local IPC endpoint: %w", err),
			connection.Close(),
		)
	}
	return connection, nil
}

type sameUserListener struct {
	*net.UnixListener
	uid uint32
}

func (listener *sameUserListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := peerUID(connection)
		if err == nil && uid == listener.uid {
			return connection, nil
		}
		_ = connection.Close()
	}
}

func removeStaleSocket(path string) error {
	effectiveUID, err := currentEffectiveUID()
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local IPC socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("local IPC path exists and is not a socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != effectiveUID {
		return errors.New("local IPC socket is not owned by the current user")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale local IPC socket: %w", err)
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	effectiveUID, err := currentEffectiveUID()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("private IPC path is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != effectiveUID {
		return errors.New("private IPC directory is not owned by the current user")
	}
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- owner-only directory.
		return err
	}
	return nil
}

func suitableRuntimeBase(path string, effectiveUID uint32) (bool, error) {
	if !filepath.IsAbs(path) {
		return false, errors.New("runtime base must be absolute")
	}
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return false, errors.New("runtime base is not a private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != effectiveUID {
		return false, errors.New("runtime base is not owned by the current user")
	}
	return true, nil
}

func openSingletonLock(path string, effectiveUID uint32) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := validateUnixNode(stat, effectiveUID, unix.S_IFREG, "singleton lock"); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func validateCredentialFile(path string) error {
	effectiveUID, err := currentEffectiveUID()
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("IPC credential file is not owner-only and regular")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != effectiveUID {
		return errors.New("IPC credential file is not owned by the current user")
	}
	return nil
}

func protectCredentialPath(string) error { return nil }

func currentEffectiveUID() (uint32, error) {
	uid := os.Geteuid()
	if uid < 0 {
		return 0, errors.New("current effective user ID is invalid")
	}
	return uint32(uid), nil // #nosec G115 -- non-negative OS UID is defined within uint32.
}
