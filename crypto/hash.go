// Package crypto provides GoChain's SHA-256 commitments and secp256k1 keys.
package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type Hash [32]byte

func Sum(data []byte) Hash                  { return sha256.Sum256(data) }
func (h Hash) String() string               { return "0x" + hex.EncodeToString(h[:]) }
func (h Hash) MarshalText() ([]byte, error) { return []byte(h.String()), nil }
func (h *Hash) UnmarshalText(text []byte) error {
	if len(text) != 66 || string(text[:2]) != "0x" {
		return fmt.Errorf("invalid hash")
	}
	b, err := hex.DecodeString(string(text[2:]))
	if err != nil {
		return err
	}
	copy(h[:], b)
	return nil
}
