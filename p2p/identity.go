package p2p

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// CreateIdentity creates an Ed25519 network identity separate from wallet keys.
func CreateIdentity(path string) (peer.ID, error) {
	key, _, err := libcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return "", err
	}
	raw, err := libcrypto.MarshalPrivateKey(key)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return peer.IDFromPrivateKey(key)
}

func LoadIdentity(path string) (libcrypto.PrivKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("P2P key must be a regular 0600 file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1024 {
		return nil, fmt.Errorf("P2P identity file too large")
	}
	return libcrypto.UnmarshalPrivateKey(raw)
}
