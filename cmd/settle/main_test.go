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
	"gochain/node"
	"gochain/rpc"
)

func TestCLIInitTransferWaitAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "devnet")
	ctx := context.Background()
	var output bytes.Buffer
	if err := run(ctx, []string{"init", "--dir", dir}, &output); err != nil {
		t.Fatal(err)
	}
	var accounts struct {
		Alice string `json:"alice"`
		Bob   string `json:"bob"`
	}
	if err := json.Unmarshal(output.Bytes(), &accounts); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"init", "--dir", dir}, &output); err == nil {
		t.Fatal("init overwrote devnet")
	}
	output.Reset()
	if err := run(ctx, []string{"address", "--key", filepath.Join(dir, "bob.key")}, &output); err != nil || strings.TrimSpace(output.String()) != accounts.Bob {
		t.Fatal(output.String(), err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "genesis.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g blockchain.Genesis
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	n, err := node.Open(data, g)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(rpc.New(n))
	t.Cleanup(func() { server.Close(); _ = n.Close() })
	output.Reset()
	if err := run(ctx, []string{"transfer", "--rpc", server.URL, "--key", filepath.Join(dir, "alice.key"), "--to", accounts.Bob, "--amount", "20"}, &output); err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(output.String())
	if _, err := n.Produce(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(ctx, []string{"wait", "--rpc", server.URL, "--hash", hash, "--timeout", "1s"}, &output); err != nil || !strings.Contains(output.String(), "committed") {
		t.Fatal(output.String(), err)
	}
	server.Close()
	_ = n.Close()
	n, err = node.Open(data, g)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(rpc.New(n))
	output.Reset()
	params, _ := json.Marshal([]string{accounts.Bob})
	if err := run(ctx, []string{"rpc", "--rpc", server.URL, "--method", "chain_getBalance", "--params", string(params)}, &output); err != nil || strings.TrimSpace(output.String()) != `"20"` {
		t.Fatal(output.String(), err)
	}
}

func TestCLIIdentityRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.p2p.key")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"identity", "--out", path}, &output); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(output.String())
	output.Reset()
	if err := run(context.Background(), []string{"peer-id", "--key", path}, &output); err != nil || strings.TrimSpace(output.String()) != id {
		t.Fatal(output.String(), err)
	}
}
