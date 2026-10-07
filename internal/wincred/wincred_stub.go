//go:build !windows

package wincred

type Real struct{}

func New() Real { return Real{} }

func (Real) Get(service, account string) (string, bool, error) { return "", false, nil }

func (Real) Delete(service, account string) error { return nil }

var _ Client = Real{}
