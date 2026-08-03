//go:build windows

package localipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func platformEndpoint(
	id,
	runtimeName string,
) (address, runtimeDirectory, lockPath string, err error) {
	return `\\.\pipe\` + runtimeName + `-` + id, "", "", nil
}

func legacyPlatformEndpoint(
	id,
	runtimeName string,
) (address, runtimeDirectory, lockPath string, err error) {
	return platformEndpoint(id, runtimeName)
}

func previousPlatformEndpoints(string, string) ([]platformPaths, error) {
	return nil, nil
}

func platformEndpointActive(Endpoint) (bool, error) {
	return false, nil
}

// Listen creates a byte-mode named pipe restricted to SYSTEM and the current
// user. go-winio creates pipes with FILE_PIPE_REJECT_REMOTE_CLIENTS.
func Listen(endpoint Endpoint) (*Listener, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve current Windows user: %w", err)
	}
	sid := user.User.Sid.String()
	descriptor := "O:" + sid + "D:P(A;;GA;;;SY)(A;;GA;;;" + sid + ")"
	base, err := winio.ListenPipe(endpoint.Address, &winio.PipeConfig{
		SecurityDescriptor: descriptor,
		MessageMode:        false,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on local IPC named pipe: %w", err)
	}
	return newListener(base, func() error { return nil }), nil
}

// DialContext connects only to the derived local named pipe.
func DialContext(ctx context.Context, endpoint Endpoint) (net.Conn, error) {
	connection, err := winio.DialPipeContext(ctx, endpoint.Address)
	if err != nil {
		return nil, err
	}
	if err := validateNamedPipeServer(connection); err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	return connection, nil
}

func ensurePrivateDirectory(path string) error {
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
	return nil
}

func validateCredentialFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("IPC credential path is not a regular file")
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect IPC credential access control: %w", err)
	}
	return validateWindowsSecurityDescriptor(descriptor, "IPC credential")
}

func validateNamedPipeServer(connection net.Conn) error {
	fileDescriptor, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("local IPC pipe does not expose an authenticated handle")
	}
	handle := windows.Handle(fileDescriptor.Fd())
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect local IPC pipe access control: %w", err)
	}
	if err := validateWindowsSecurityDescriptor(descriptor, "local IPC pipe"); err != nil {
		return err
	}

	var processID uint32
	if err := windows.GetNamedPipeServerProcessId(handle, &processID); err != nil {
		return fmt.Errorf("resolve local IPC pipe owner process: %w", err)
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		processID,
	)
	if err != nil {
		return fmt.Errorf("open local IPC pipe owner process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var processToken windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &processToken); err != nil {
		return fmt.Errorf("open local IPC pipe owner token: %w", err)
	}
	defer func() { _ = processToken.Close() }()
	owner, err := processToken.GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve local IPC pipe owner: %w", err)
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve current Windows user: %w", err)
	}
	if !owner.User.Sid.Equals(current.User.Sid) {
		return errors.New("local IPC pipe is owned by another Windows user")
	}
	return nil
}

func validateWindowsSecurityDescriptor(
	descriptor *windows.SECURITY_DESCRIPTOR,
	description string,
) error {
	if descriptor == nil || !descriptor.IsValid() {
		return fmt.Errorf("%s has an invalid security descriptor", description)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("resolve %s owner: %w", description, err)
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve current Windows user: %w", err)
	}
	if owner == nil || !owner.Equals(current.User.Sid) {
		return fmt.Errorf("%s is owned by another Windows user", description)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("inspect %s DACL protection: %w", description, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("%s DACL is not protected", description)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("inspect %s DACL: %w", description, err)
	}
	if dacl == nil {
		return fmt.Errorf("%s has no access-control entries", description)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve Windows SYSTEM identity: %w", err)
	}
	if dacl.AceCount != 2 {
		return fmt.Errorf(
			"%s access is not limited to the current user and SYSTEM",
			description,
		)
	}
	currentAllowed := false
	systemAllowed := false
	for index := uint16(0); index < dacl.AceCount; index++ {
		var entry *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &entry); err != nil {
			return fmt.Errorf("inspect %s access entry: %w", description, err)
		}
		if entry == nil ||
			entry.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			entry.Header.AceFlags != 0 {
			return fmt.Errorf("%s has an unsupported access entry", description)
		}
		// ACCESS_ALLOWED_ACE stores its variable-length SID at SidStart.
		// Windows validates the ACL before GetAce exposes this pointer.
		sid := (*windows.SID)(unsafe.Pointer(&entry.SidStart)) // #nosec G103 -- documented Windows ACE layout.
		switch {
		case sid.Equals(current.User.Sid):
			currentAllowed = true
		case sid.Equals(system):
			systemAllowed = true
		default:
			return fmt.Errorf("%s grants access to another identity", description)
		}
	}
	if !currentAllowed || !systemAllowed {
		return fmt.Errorf(
			"%s access is not limited to the current user and SYSTEM",
			description,
		)
	}
	return nil
}

func protectCredentialPath(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(user.User.Sid)
	pinner.Pin(system)
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(system),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, acl, nil,
	)
}
