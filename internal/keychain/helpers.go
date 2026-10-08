package keychain

import (
	"encoding/hex"
	"os"
)

func getenv(key string) string { return os.Getenv(key) }

// toHex encodes s as lowercase hex (Python bytes.hex() of the UTF-8 bytes).
func toHex(s string) string { return hex.EncodeToString([]byte(s)) }
