package responses

import (
	"crypto/rand"
	"encoding/hex"
)

// GenerateResponseID generates a "resp_" prefixed unique ID.
func GenerateResponseID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "resp_" + hex.EncodeToString(b)
}

// GenerateItemID generates a short unique ID with the given prefix (e.g. "msg_", "fc_").
func GenerateItemID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
