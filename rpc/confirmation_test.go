package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gochain/blockchain"
	"gochain/crypto"
)

func TestReceiptLongPollAndBounds(t *testing.T) {
	n, key, alice, bob := fixture(t)
	s := New(n)
	tx, err := (blockchain.Transaction{ChainID: 9001, To: bob, Amount: 1, GasLimit: blockchain.TransferGas}).Sign(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Submit(tx); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	client := Client{URL: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var nonce Quantity
	if err := client.Call(ctx, "chain_getPendingNonce", []string{alice}, &nonce); err != nil || nonce != 1 {
		t.Fatal(nonce, err)
	}
	var confirmed Quantity
	if err := client.Call(ctx, "chain_getNonce", []string{alice}, &confirmed); err != nil || confirmed != 0 {
		t.Fatal(confirmed, err)
	}
	done := make(chan error, 1)
	go func() {
		var receipt struct {
			TransactionHash crypto.Hash `json:"transactionHash"`
			Scope           string      `json:"confirmationScope"`
		}
		err := client.Call(ctx, "chain_waitForTransactionReceipt", map[string]string{"hash": tx.Hash().String()}, &receipt)
		if err == nil && (receipt.TransactionHash != tx.Hash() || receipt.Scope != "local-durable") {
			err = fmt.Errorf("wrong confirmation: %+v", receipt)
		}
		done <- err
	}()
	for len(s.waiters) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := n.Produce(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(s.waiters) != 0 {
		t.Fatal("waiter leaked")
	}
	params := json.RawMessage(fmt.Sprintf(`{"hash":"%s","timeoutMs":"1"}`, crypto.Hash{}))
	if result, err := s.waitForReceipt(ctx, params); err != nil || result != nil {
		t.Fatal("timeout must return null", result, err)
	}
	for i := 0; i < MaxWaiters; i++ {
		s.waiters <- struct{}{}
	}
	if _, err := s.waitForReceipt(ctx, params); err == nil || err.Code != -32005 {
		t.Fatal("limit ignored", err)
	}
	// Existing receipts remain readable even when all waiting slots are occupied.
	ready := json.RawMessage(fmt.Sprintf(`{"hash":"%s"}`, tx.Hash()))
	if result, err := s.waitForReceipt(ctx, ready); err != nil || result == nil {
		t.Fatal(result, err)
	}
	for i := 0; i < MaxWaiters; i++ {
		<-s.waiters
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := s.waitForReceipt(cancelled, params); err == nil {
		t.Fatal("cancellation ignored")
	}
	if len(s.waiters) != 0 {
		t.Fatal("cancelled waiter leaked")
	}
	for _, bad := range []string{`null`, `[]`, `{}`, `{"hash":null}`, `{"hash":42}`, `{"hash":"x"}`, fmt.Sprintf(`{"hash":"%s","timeoutMs":10}`, crypto.Hash{}), fmt.Sprintf(`{"hash":"%s","timeoutMs":"0"}`, crypto.Hash{}), fmt.Sprintf(`{"hash":"%s","timeoutMs":"5001"}`, crypto.Hash{}), fmt.Sprintf(`{"hash":"%s","other":"1"}`, crypto.Hash{})} {
		if _, err := s.waitForReceipt(ctx, json.RawMessage(bad)); err == nil || err.Code != -32602 {
			t.Fatal("invalid params accepted", bad, err)
		}
	}
	wait := fmt.Sprintf(`{"jsonrpc":"2.0","method":"chain_waitForTransactionReceipt","params":{"hash":"%s"},"id":1}`, crypto.Hash{})
	if w := request(s, "["+wait+"]"); !strings.Contains(w.Body.String(), "-32600") {
		t.Fatal("batch wait accepted", w.Body.String())
	}
	wait = strings.Replace(wait, `,"id":1`, "", 1)
	if w := request(s, wait); w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("wait notification mishandled")
	}
}
