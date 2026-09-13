package wallet

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gochain/crypto"
)

func TestWalletRoundTripAndNoOverwrite(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "alice.key")
	if err := Save(path, key); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Serialize(), key.Serialize()) {
		t.Fatal("different key")
	}
	if err := Save(path, key); err == nil {
		t.Fatal("overwrote wallet")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted publicly readable key")
	}
}

func TestInvalidPrivateKeys(t *testing.T) {
	for _, raw := range [][]byte{nil, make([]byte, 32), bytes.Repeat([]byte{255}, 32), make([]byte, 33)} {
		if _, err := crypto.ParsePrivateKey(raw); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(path, []byte("not-a-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("invalid file accepted")
	}
}
