//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	winregistry "golang.org/x/sys/windows/registry"
)

type platformSessionState struct {
	PlinkPath string `json:"plink_path,omitempty"`
	PlinkPID  int    `json:"plink_pid,omitempty"`
}

func newPlatformSessionState(_, _ string) platformSessionState {
	return platformSessionState{}
}

func removePlatformSessionFiles(_, id string) {
	removePlinkProfiles(id)
}

func runtimeDirectory() string {
	base, err := os.UserCacheDir()
	if err == nil && base != "" {
		return filepath.Join(base, "ssh-handoff", "runtime")
	}
	if profile, profileErr := windows.GetCurrentProcessToken().GetUserProfileDirectory(); profileErr == nil &&
		profile != "" {
		return filepath.Join(profile, ".ssh-handoff", "runtime")
	}
	// A machine-wide temporary directory cannot preserve the session's
	// current-user boundary, so directory setup fails closed instead.
	return ""
}

func ensurePrivateDirectory(path string) error {
	if path == "" {
		return errors.New("cannot determine a private runtime directory for the current Windows user")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := validateWindowsDirectory(path); err != nil {
		return err
	}
	user, err := currentWindowsUser()
	if err != nil {
		return fmt.Errorf("query current Windows user: %w", err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read runtime directory owner: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read runtime directory owner: %w", err)
	}
	if !windows.EqualSid(owner, user.User.Sid) {
		return fmt.Errorf("runtime directory is not owned by the current user: %s", path)
	}
	acl, err := privateWindowsACL(user.User.Sid)
	if err != nil {
		return fmt.Errorf("build private runtime ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("protect runtime directory ACL: %w", err)
	}
	return validatePrivateDirectory(path)
}

func validatePrivateDirectory(path string) error {
	if path == "" {
		return errors.New("cannot determine a private runtime directory for the current Windows user")
	}
	if err := validateWindowsDirectory(path); err != nil {
		return err
	}
	user, err := currentWindowsUser()
	if err != nil {
		return fmt.Errorf("query current Windows user: %w", err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read runtime directory security: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read runtime directory owner: %w", err)
	}
	if !windows.EqualSid(owner, user.User.Sid) {
		return fmt.Errorf("runtime directory is not owned by the current user: %s", path)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read runtime directory ACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("runtime directory inherits permissions from its parent: %s", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read runtime directory ACL: %w", err)
	}
	if dacl == nil || dacl.AceCount != 2 {
		return fmt.Errorf("runtime directory ACL is not private to the current user: %s", path)
	}
	// The file-system security provider expands GENERIC_ALL on the directory
	// itself while preserving it on the inherit-only ACE for children.
	const fileAllAccess windows.ACCESS_MASK = 0x1f01ff
	wantInheritedFlags := uint8(
		windows.OBJECT_INHERIT_ACE |
			windows.CONTAINER_INHERIT_ACE |
			windows.INHERIT_ONLY_ACE,
	)
	direct, inherited := false, false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read runtime directory ACL entry: %w", err)
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			!windows.EqualSid(aceSID, user.User.Sid) {
			return fmt.Errorf("runtime directory ACL is not private to the current user: %s", path)
		}
		switch {
		case ace.Header.AceFlags == 0 && ace.Mask == fileAllAccess:
			direct = true
		case ace.Header.AceFlags == wantInheritedFlags && ace.Mask == windows.GENERIC_ALL:
			inherited = true
		default:
			return fmt.Errorf("runtime directory ACL is not private to the current user: %s", path)
		}
	}
	if !direct || !inherited {
		return fmt.Errorf("runtime directory ACL is not private to the current user: %s", path)
	}
	return nil
}

func validateWindowsDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("runtime path is not a directory: %s", path)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("runtime path must not be a reparse point: %s", path)
	}
	return nil
}

func currentWindowsUser() (*windows.Tokenuser, error) {
	return windows.GetCurrentProcessToken().GetTokenUser()
}

func privateWindowsACL(user *windows.SID) (*windows.ACL, error) {
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:P(A;OICI;GA;;;" + user.String() + ")",
	)
	if err != nil {
		return nil, err
	}
	acl, _, err := descriptor.DACL()
	return acl, err
}

func withFileLock(path string, action func() error) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	var overlapped windows.Overlapped
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		&overlapped,
	); err != nil {
		return err
	}
	defer func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
	}()
	return action()
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // read-only liveness probe
	result, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && result == uint32(windows.WAIT_TIMEOUT)
}

func terminatePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(pid),
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // best effort during teardown

	result, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return err
	}
	if result == windows.WAIT_OBJECT_0 {
		return nil
	}
	return windows.TerminateProcess(handle, 1)
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // read-only liveness probe

	milliseconds := timeout.Milliseconds()
	if milliseconds < 0 {
		milliseconds = 0
	}
	if milliseconds > int64(^uint32(0)-1) {
		milliseconds = int64(^uint32(0) - 1)
	}
	result, err := windows.WaitForSingleObject(handle, uint32(milliseconds))
	return err == nil && result == windows.WAIT_OBJECT_0
}

const (
	puttySessionsRegistryPath = `Software\SimonTatham\PuTTY\Sessions`
	plinkProfileOwnerValue    = "SSH-HandoffManaged"
)

func plinkProfileName(id, role string) string {
	return "ssh-handoff-" + id + "-" + role
}

func createPlinkProfiles(id string) error {
	upstream := plinkProfileName(id, "upstream")
	downstream := plinkProfileName(id, "downstream")

	if err := writePlinkProfile(upstream, true, false, false); err != nil {
		return err
	}
	if err := writePlinkProfile(downstream, false, true, true); err != nil {
		removePlinkProfile(upstream)
		return err
	}
	return nil
}

func writePlinkProfile(name string, upstream, downstream, disableAuth bool) error {
	removePlinkProfile(name)
	path := puttySessionsRegistryPath + `\` + name
	key, openedExisting, err := winregistry.CreateKey(
		winregistry.CURRENT_USER,
		path,
		winregistry.SET_VALUE,
	)
	if err != nil {
		return fmt.Errorf("create temporary PuTTY session %s: %w", name, err)
	}
	keyOpen := true
	defer func() {
		if keyOpen {
			_ = key.Close()
		}
	}()
	if openedExisting {
		return fmt.Errorf(
			"temporary PuTTY session name conflicts with an unmanaged saved session: %s",
			name,
		)
	}
	closeAndDelete := func() {
		_ = key.Close()
		keyOpen = false
		deletePlinkProfile(name)
	}
	if err := key.SetDWordValue(plinkProfileOwnerValue, 1); err != nil {
		closeAndDelete()
		return fmt.Errorf("mark temporary PuTTY session %s: %w", name, err)
	}

	values := map[string]uint32{
		"ConnectionSharing":           1,
		"ConnectionSharingUpstream":   boolDWORD(upstream),
		"ConnectionSharingDownstream": boolDWORD(downstream),
	}
	stringValues := map[string]string{
		"Protocol": "ssh",
	}
	if disableAuth {
		values["TryAgent"] = 0
		values["AgentFwd"] = 0
		values["AuthGSSAPI"] = 0
		values["AuthGSSAPIKEX"] = 0
		values["AuthTIS"] = 0
		values["AuthKI"] = 0
		values["SshNoTrivialAuth"] = 1
		stringValues["PublicKeyFile"] = ""
		stringValues["DetachedCertificate"] = ""
		stringValues["AuthPlugin"] = ""
	}
	for value, data := range values {
		if err := key.SetDWordValue(value, data); err != nil {
			closeAndDelete()
			return fmt.Errorf("configure temporary PuTTY session %s: %w", name, err)
		}
	}
	for value, data := range stringValues {
		if err := key.SetStringValue(value, data); err != nil {
			closeAndDelete()
			return fmt.Errorf("configure temporary PuTTY session %s: %w", name, err)
		}
	}
	return nil
}

func boolDWORD(value bool) uint32 {
	if value {
		return 1
	}
	return 0
}

func removePlinkProfiles(id string) {
	removePlinkProfile(plinkProfileName(id, "upstream"))
	removePlinkProfile(plinkProfileName(id, "downstream"))
}

func removePlinkProfile(name string) {
	path := puttySessionsRegistryPath + `\` + name
	key, err := winregistry.OpenKey(
		winregistry.CURRENT_USER,
		path,
		winregistry.QUERY_VALUE,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return
	}
	if err != nil {
		return
	}
	managed, _, markerErr := key.GetIntegerValue(plinkProfileOwnerValue)
	_ = key.Close()
	if markerErr != nil || managed != 1 {
		return
	}
	deletePlinkProfile(name)
}

func deletePlinkProfile(name string) {
	err := winregistry.DeleteKey(
		winregistry.CURRENT_USER,
		puttySessionsRegistryPath+`\`+name,
	)
	if err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return
	}
}
