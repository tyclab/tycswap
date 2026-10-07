//go:build windows

package wincred

import (
	"errors"
	"fmt"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errInTests is credRead's and credDelete's answer inside a test binary,
// before any call: a test never reads or deletes a user's credentials.
var errInTests = errors.New("wincred: the real Windows Credential Manager is not reachable from tests; give the test a fake (wincred.NewFake)")

const credTypeGeneric = 1 // CRED_TYPE_GENERIC

// credentialW mirrors the Win32 CREDENTIALW struct layout exactly (field order
// and types matter — this is read via unsafe.Pointer from what CredReadW
// allocates).
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	modAdvapi32    = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW  = modAdvapi32.NewProc("CredReadW")
	procCredDelete = modAdvapi32.NewProc("CredDeleteW")
	procCredFree   = modAdvapi32.NewProc("CredFree")
)

// Real is the concrete Windows Credential Manager Client.
type Real struct{}

// New returns the real Windows Credential Manager Client.
func New() Real { return Real{} }

// credRead reads one CRED_TYPE_GENERIC entry by exact target name. found is
// false (nil error) only for ERROR_NOT_FOUND; any other failure is a real
// error. A variable so a test can stand in for the Credential Manager.
var credRead = func(target string) (value string, username string, found bool, err error) {
	if testing.Testing() {
		return "", "", false, errInTests
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", "", false, err
	}
	var cred *credentialW
	r1, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	if r1 == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("CredReadW(%s): %w", target, callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))

	if cred.UserName != nil {
		username = windows.UTF16PtrToString(cred.UserName)
	}
	if cred.CredentialBlobSize > 0 && cred.CredentialBlob != nil {
		blob := unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)
		// keyring writes the UTF-16LE-encoded secret as the blob (matching
		// Windows Credential Manager's own Credential Manager UI convention).
		value = utf16BytesToString(blob)
	}
	return value, username, true, nil
}

// credDelete deletes one CRED_TYPE_GENERIC entry by exact target name. A
// missing entry is treated as a successful no-op (rc-44 parity). A variable
// so a test can stand in for the Credential Manager.
var credDelete = func(target string) error {
	if testing.Testing() {
		return errInTests
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r1, _, callErr := procCredDelete.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
	)
	if r1 == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return nil
		}
		return fmt.Errorf("CredDeleteW(%s): %w", target, callErr)
	}
	return nil
}

func utf16BytesToString(b []byte) string {
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return windows.UTF16ToString(u16)
}

func compoundName(service, account string) string { return account + "@" + service }

func (Real) Get(service, account string) (string, bool, error) {
	value, username, found, err := credRead(service)
	if err != nil {
		return "", false, err
	}
	if found && username == account {
		return value, true, nil
	}
	value, _, found, err = credRead(compoundName(service, account))
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	return value, true, nil
}

func (Real) Delete(service, account string) error {
	_, username, found, err := credRead(service)
	if err != nil {
		return err
	}
	if found && username == account {
		return credDelete(service)
	}
	return credDelete(compoundName(service, account))
}

var _ Client = Real{}
