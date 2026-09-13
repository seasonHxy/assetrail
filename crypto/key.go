package crypto

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

type PrivateKey = secp256k1.PrivateKey

func GenerateKey() (*PrivateKey, error) { return secp256k1.GeneratePrivateKey() }

// ParsePrivateKey rejects zero and out-of-range scalars rather than reducing them.
func ParsePrivateKey(raw []byte) (*PrivateKey, error) {
	if len(raw) != 32 {
		return nil, fmt.Errorf("private key must contain exactly 32 bytes")
	}
	var scalar secp256k1.ModNScalar
	defer scalar.Zero()
	if scalar.SetByteSlice(raw) || scalar.IsZero() {
		return nil, fmt.Errorf("invalid private key scalar")
	}
	return secp256k1.PrivKeyFromBytes(raw), nil
}

// Address is the last 20 bytes of SHA-256(compressed public key).
// It is deliberately not an Ethereum address.
func Address(publicKey []byte) (string, error) {
	key, err := secp256k1.ParsePubKey(publicKey)
	if err != nil {
		return "", err
	}
	h := Sum(key.SerializeCompressed())
	return "0x" + hex.EncodeToString(h[12:]), nil
}

func ValidAddress(address string) bool {
	if len(address) != 42 || !strings.HasPrefix(address, "0x") || address != strings.ToLower(address) {
		return false
	}
	_, err := hex.DecodeString(address[2:])
	return err == nil
}

func Sign(key *PrivateKey, digest Hash) []byte { return ecdsa.Sign(key, digest[:]).Serialize() }

func Verify(publicKey, signature []byte, digest Hash) error {
	if len(publicKey) != 33 {
		return fmt.Errorf("expected compressed public key")
	}
	key, err := secp256k1.ParsePubKey(publicKey)
	if err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	// Serialize normalizes to low-S; reject alternative encodings.
	if !bytes.Equal(sig.Serialize(), signature) {
		return fmt.Errorf("non-canonical signature")
	}
	if !sig.Verify(digest[:], key) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}
