//go:build !windows

// Stub per spec 07§5.3 / DESIGN A9: migrations gate on platform.Windows before calling Real, so this only lets the package link.

package wincred

// Real behaves like a Credential Manager with no legacy entries: Get is not-found, Delete succeeds.
type Real struct{}

func New() Real { return Real{} }

func (Real) Get(service, account string) (string, bool, error) { return "", false, nil }

func (Real) Delete(service, account string) error { return nil }

var _ Client = Real{}
