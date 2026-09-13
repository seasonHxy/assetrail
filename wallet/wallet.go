// Package wallet stores local development keys; keys never enter an RPC request.
package wallet

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"gochain/crypto"
)

func Save(path string, key *crypto.PrivateKey) error {
	if key == nil {
		return fmt.Errorf("missing private key")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, hex.EncodeToString(key.Serialize())); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func Load(path string) (*crypto.PrivateKey, error) {
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
		return nil, fmt.Errorf("key must be a regular file readable only by its owner; use chmod 600")
	}
	b, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range b {
			b[i] = 0
		}
	}()
	raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("invalid key encoding")
	}
	defer func() {
		for i := range raw {
			raw[i] = 0
		}
	}()
	return crypto.ParsePrivateKey(raw)
}
