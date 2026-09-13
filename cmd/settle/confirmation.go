package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gochain/crypto"
	"gochain/rpc"
)

func waitReceipt(ctx context.Context, client rpc.Client, hash string, timeout time.Duration) (json.RawMessage, error) {
	var parsed crypto.Hash
	if err := parsed.UnmarshalText([]byte(hash)); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		var receipt json.RawMessage
		err := client.Call(waitCtx, "chain_waitForTransactionReceipt", map[string]string{"hash": hash, "timeoutMs": "5000"}, &receipt)
		if err != nil {
			return nil, fmt.Errorf("transaction %s confirmation unavailable: %w; it may still commit, query this hash before resubmitting", hash, err)
		}
		if string(receipt) != "null" {
			return receipt, nil
		}
	}
}
