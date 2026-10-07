package cerr

import (
	"errors"
	"fmt"
)

type Kind string

const (
	KindConfig                Kind = "ConfigError"
	KindSwitch                Kind = "SwitchError"
	KindSession               Kind = "SessionError"
	KindValidation            Kind = "ValidationError"
	KindAccountNotFound       Kind = "AccountNotFoundError"
	KindCredential            Kind = "CredentialError"
	KindCredentialRead        Kind = "CredentialReadError"
	KindCredentialWrite       Kind = "CredentialWriteError"
	KindLock                  Kind = "LockError"
	KindClaudeCodeLockTimeout Kind = "ClaudeCodeLockTimeout"
	KindTransfer              Kind = "TransferError"
	KindMigration             Kind = "MigrationError"
	KindMigrationIncomplete   Kind = "MigrationIncomplete"
)

// Error is a domain error carrying a Kind and message.
type Error struct {
	Kind    Kind
	Msg     string
	wrapped error
}

// Error returns the message alone, matching Python's str(exc).
func (e *Error) Error() string { return e.Msg }

// Unwrap returns the wrapped cause, if any.
func (e *Error) Unwrap() error { return e.wrapped }

func (e *Error) Wrap(cause error) *Error {
	e.wrapped = cause
	return e
}

func newError(kind Kind, format string, a ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, a...)}
}

// Config builds a ConfigError.
func Config(format string, a ...any) *Error { return newError(KindConfig, format, a...) }

// Switch builds a SwitchError.
func Switch(format string, a ...any) *Error { return newError(KindSwitch, format, a...) }

// Session builds a SessionError.
func Session(format string, a ...any) *Error { return newError(KindSession, format, a...) }

// Validation builds a ValidationError.
func Validation(format string, a ...any) *Error { return newError(KindValidation, format, a...) }

// AccountNotFound builds an AccountNotFoundError.
func AccountNotFound(format string, a ...any) *Error {
	return newError(KindAccountNotFound, format, a...)
}

// Credential builds a CredentialError.
func Credential(format string, a ...any) *Error { return newError(KindCredential, format, a...) }

// CredentialRead builds a CredentialReadError.
func CredentialRead(format string, a ...any) *Error {
	return newError(KindCredentialRead, format, a...)
}

// CredentialWrite builds a CredentialWriteError.
func CredentialWrite(format string, a ...any) *Error {
	return newError(KindCredentialWrite, format, a...)
}

// Lock builds a LockError.
func Lock(format string, a ...any) *Error { return newError(KindLock, format, a...) }

// ClaudeCodeLockTimeout builds a ClaudeCodeLockTimeout (a LockError subtype in
// Python; a distinct Kind here). Nothing has been mutated when it is raised.
func ClaudeCodeLockTimeout(format string, a ...any) *Error {
	return newError(KindClaudeCodeLockTimeout, format, a...)
}

// Transfer builds a TransferError.
func Transfer(format string, a ...any) *Error { return newError(KindTransfer, format, a...) }

// Migration builds a MigrationError.
func Migration(format string, a ...any) *Error { return newError(KindMigration, format, a...) }

// MigrationIncomplete builds a MigrationIncomplete.
func MigrationIncomplete(format string, a ...any) *Error {
	return newError(KindMigrationIncomplete, format, a...)
}

func IsClaudeSwitchError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

func TypeName(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return string(e.Kind)
	}
	return ""
}
