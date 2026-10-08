//go:build windows

package paths

// No-op: POSIX owner and mode bits do not describe Windows write access; an owner-only DACL check is not implemented yet.
func CheckPrivateRoot(root string) (warning string, err error) { return "", nil }
