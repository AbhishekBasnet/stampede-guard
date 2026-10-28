package ids

import (
	"crypto/rand"
	"encoding/hex"
)

func New(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b) // never returns an error since Go 1.24
	return prefix + "_" + hex.EncodeToString(b)
}
