package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gochain/blockchain"
	"gochain/crypto"
	"gochain/node"
	"gochain/rpc"
	"gochain/wallet"
)

func TestExportAndVerifyAfterServerShutdown(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := crypto.Address(key.PubKey().SerializeCompressed())
	bob, _ := crypto.Address(other.PubKey().SerializeCompressed())
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "wallet.key")
	if err := wallet.Save(keyPath, key); err != nil {
		t.Fatal(err)
	}
	n, err := node.Open(filepath.Join(dir, "data"), blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{alice: 100}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	server := httptest.NewServer(rpc.New(n))
	defer server.Close()
	var output bytes.Buffer
	ctx := context.Background()
	// Offline signing works with a nonexistent RPC endpoint.
	if err := run(ctx, []string{"sign", "--key", keyPath, "--to", bob, "--amount", "20", "--nonce", "0", "--chain-id", "9001", "--rpc", "http://127.0.0.1:1"}, &output); err != nil {
		t.Fatal(err)
	}
	tx, err := rpc.DecodeRawTransaction(strings.TrimSpace(output.String()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Submit(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.Produce(time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bundle.json")
	output.Reset()
	if err := run(ctx, []string{"export", "--rpc", server.URL, "--hash", tx.Hash().String(), "--out", path}, &output); err != nil {
		t.Fatal(err)
	}
	server.Close()
	_ = n.Close()
	output.Reset()
	if err := run(ctx, []string{"verify", "--file", path, "--block-hash", block.Hash().String()}, &output); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result["valid"] != true {
		t.Fatal(output.String(), err)
	}
	if err := run(ctx, []string{"verify", "--file", path}, &output); err == nil {
		t.Fatal("accepted missing external anchor")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"amount": "20"`), []byte(`"amount": "21"`), 1)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"verify", "--file", path, "--block-hash", block.Hash().String()}, &output); err == nil {
		t.Fatal("altered payment accepted")
	}
}
