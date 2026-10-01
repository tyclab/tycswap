//go:build windows

package paths

// CheckPrivateRoot is a no-op on Windows: POSIX owner and mode bits do not
// describe who can write there. An owner-only DACL check is a follow-up.
func CheckPrivateRoot(root string) (warning string, err error) { return "", nil }
