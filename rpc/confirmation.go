package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gochain/crypto"
)

const MaxWaiters = 128
const MaxReceiptWait = 5 * time.Second

func (s *Server) waitForReceipt(ctx context.Context, params json.RawMessage) (any, *Error) {
	invalid := &Error{Code: -32602, Message: "expected {hash: string, timeoutMs?: decimal string in 1..5000}"}
	var fields map[string]json.RawMessage
	if json.Unmarshal(params, &fields) != nil || fields == nil || len(fields) < 1 || len(fields) > 2 {
		return nil, invalid
	}
	var hash crypto.Hash
	var hashText string
	if json.Unmarshal(fields["hash"], &hashText) != nil || hash.UnmarshalText([]byte(hashText)) != nil {
		return nil, invalid
	}
	duration := MaxReceiptWait
	for key, raw := range fields {
		switch key {
		case "hash":
		case "timeoutMs":
			var ms Quantity
			if json.Unmarshal(raw, &ms) != nil || ms == 0 || ms > 5000 {
				return nil, invalid
			}
			duration = time.Duration(ms) * time.Millisecond
		default:
			return nil, invalid
		}
	}
	// Immediate queries do not consume a waiting slot.
	receipt, err := s.node.Receipt(hash)
	if err != nil {
		return nil, &Error{Code: -32000, Message: err.Error()}
	}
	if receipt != nil {
		return receiptView(receipt), nil
	}
	select {
	case s.waiters <- struct{}{}:
		defer func() { <-s.waiters }()
	default:
		return nil, &Error{Code: -32005, Message: "too many receipt waiters; retry later"}
	}
	waitCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	receipt, err = s.node.WaitReceipt(waitCtx, hash)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, nil
	}
	if err != nil {
		return nil, &Error{Code: -32000, Message: err.Error()}
	}
	return receiptView(receipt), nil
}
