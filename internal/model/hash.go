package model

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContentHash returns the lowercase hex-encoded SHA-256 of content. It is the
// single definition of how OpenWhisker hashes vault content, so the
// before_hash / after_hash guards that anchor the conflict-control model agree
// across every layer that computes them.
func ContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
