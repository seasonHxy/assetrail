package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
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

func TestConsecutiveTransfersAndWaitTimeout(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	address, _ := crypto.Address(key.PubKey().SerializeCompressed())
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "alice.key")
	if err := wallet.Save(keyPath, key); err != nil {
		t.Fatal(err)
	}
	n, err := node.Open(filepath.Join(dir, "data"), blockchain.Genesis{ChainID: 9001, Alloc: map[string]uint64{address: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	s := httptest.NewServer(rpc.New(n))
	defer s.Close()
	ctx := context.Background()
	args := []string{"transfer", "--rpc", s.URL, "--key", keyPath, "--to", address, "--amount", "1"}
	var out bytes.Buffer
	for i := 0; i < 3; i++ {
		out.Reset()
		if err := run(ctx, args, &out); err != nil {
			t.Fatal(err)
		}
		var hash crypto.Hash
		if err := hash.UnmarshalText([]byte(strings.TrimSpace(out.String()))); err != nil {
			t.Fatal(err)
		}
		info, err := n.Transaction(hash)
		if err != nil || info == nil || info.Transaction.Nonce != uint64(i) {
			t.Fatal("pending nonce not used", err)
		}
	}
	out.Reset()
	err = run(ctx, append(append([]string{}, args...), "--wait", "--timeout", "15ms"), &out)
	if err == nil || !strings.Contains(err.Error(), "may still commit") {
		t.Fatal("ambiguous submission not reported", err)
	}
	page, _ := n.PendingPage(0, 10)
	if len(page) != 4 || !strings.Contains(err.Error(), page[3].Hash().String()) {
		t.Fatal("timeout lost transaction hash", err)
	}
	if _, err := n.Produce(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if r, err := n.Receipt(page[3].Hash()); err != nil || r == nil {
		t.Fatal("timeout cancelled admitted transaction", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- n.Run(runCtx, 100*time.Millisecond) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	out.Reset()
	if err := run(ctx, append(append([]string{}, args...), "--wait", "--timeout", "5s"), &out); err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if json.Unmarshal(out.Bytes(), &receipt) != nil || receipt["confirmationScope"] != "local-durable" {
		t.Fatal(out.String())
	}
}
