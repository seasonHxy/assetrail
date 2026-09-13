// Package rpc exposes a bounded JSON-RPC 2.0 HTTP API for the local node.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"mime"
	"net/http"

	"gochain/crypto"
	"gochain/node"
)

const MaxRequestBytes = 1 << 20
const MaxBatchSize = 32

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

type Server struct {
	node        *node.Node
	networkInfo func() any
	waiters     chan struct{}
}

// Optional network info is read-only and must be safe for concurrent calls.
func New(n *node.Node, networkInfo ...func() any) *Server {
	s := &Server{node: n, waiters: make(chan struct{}, MaxWaiters)}
	if len(networkInfo) > 0 {
		s.networkInfo = networkInfo[0]
	}
	return s
}

func failure(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": &Error{Code: code, Message: message}}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		if _, err := s.node.Status(); err != nil {
			http.Error(w, "node unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	// No browser client is shipped in this release. Prevent browser-originated
	// submissions to the local signing-independent node, including DNS rebinding.
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins are not enabled", http.StatusForbidden)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "use application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	body = bytes.TrimSpace(body)
	w.Header().Set("Content-Type", "application/json")
	var result any
	if !json.Valid(body) {
		result = failure(nil, -32700, "Parse error")
	} else if len(body) > 0 && body[0] == '[' {
		var batch []json.RawMessage
		_ = json.Unmarshal(body, &batch)
		if len(batch) == 0 || len(batch) > MaxBatchSize {
			result = failure(nil, -32600, "Invalid batch size")
		} else {
			responses := make([]any, 0, len(batch))
			for _, item := range batch {
				if response := s.handle(r.Context(), item, false); response != nil {
					responses = append(responses, response)
				}
			}
			if len(responses) > 0 {
				result = responses
			}
		}
	} else {
		if response := s.handle(r.Context(), body, true); response != nil {
			result = response
		}
	}
	if result == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) handle(ctx context.Context, data json.RawMessage, allowWait bool) map[string]any {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return failure(nil, -32600, "Invalid Request")
	}
	var version, method string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" || json.Unmarshal(fields["method"], &method) != nil || bytes.Equal(fields["method"], []byte("null")) {
		return failure(nil, -32600, "Invalid Request")
	}
	id, hasID := fields["id"]
	if hasID {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(id))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return failure(nil, -32600, "Invalid Request")
		}
		switch value.(type) {
		case nil, string, json.Number:
		default:
			return failure(nil, -32600, "Invalid Request")
		}
	}
	params := bytes.TrimSpace(fields["params"])
	if len(params) > 0 && params[0] != '[' && params[0] != '{' {
		return failure(nil, -32600, "Invalid Request")
	}
	if method == "chain_waitForTransactionReceipt" {
		if !hasID {
			return nil
		}
		if !allowWait {
			return failure(id, -32600, "receipt wait is not allowed in a batch")
		}
	}
	result, rpcErr := s.call(ctx, method, params)
	if !hasID {
		return nil
	}
	if rpcErr != nil {
		return failure(id, rpcErr.Code, rpcErr.Message)
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

// Each current method takes either no arguments or one named string argument.
func argument(params json.RawMessage, name string) (string, bool) {
	if len(params) == 0 {
		return "", name == ""
	}
	if params[0] == '[' {
		var args []json.RawMessage
		if json.Unmarshal(params, &args) != nil {
			return "", false
		}
		if name == "" {
			return "", len(args) == 0
		}
		if len(args) != 1 {
			return "", false
		}
		var value string
		if len(args[0]) == 0 || args[0][0] != '"' {
			return "", false
		}
		if json.Unmarshal(args[0], &value) != nil {
			return "", false
		}
		return value, true
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(params, &args) != nil {
		return "", false
	}
	if name == "" {
		return "", len(args) == 0
	}
	if len(args) != 1 {
		return "", false
	}
	var value string
	raw, ok := args[name]
	if !ok || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func (s *Server) call(ctx context.Context, method string, params json.RawMessage) (any, *Error) {
	invalid := &Error{Code: -32602, Message: "Invalid params"}
	backend := func(err error) *Error {
		if err == nil {
			return nil
		}
		return &Error{Code: -32000, Message: err.Error()}
	}
	switch method {
	case "chain_waitForTransactionReceipt":
		return s.waitForReceipt(ctx, params)
	case "chain_getPeers":
		if _, ok := argument(params, ""); !ok {
			return nil, invalid
		}
		if _, err := s.node.Status(); err != nil {
			return nil, backend(err)
		}
		if s.networkInfo == nil {
			return map[string]any{"enabled": false, "peers": []any{}, "connectedCount": "0"}, nil
		}
		return s.networkInfo(), nil
	case "chain_getChainId", "chain_getBlockNumber", "chain_getStatus", "chain_getMempool":
		if _, ok := argument(params, ""); !ok {
			return nil, invalid
		}
		status, err := s.node.Status()
		if err != nil {
			return nil, backend(err)
		}
		switch method {
		case "chain_getChainId":
			return Quantity(status.ChainID), nil
		case "chain_getBlockNumber":
			return Quantity(status.Height), nil
		case "chain_getMempool":
			return map[string]any{"pending": Quantity(status.Pending), "queued": Quantity(status.Queued)}, nil
		default:
			return map[string]any{"name": "SettleLab", "version": "0.2.0-dev", "mode": status.Mode, "peerId": status.PeerID, "authority": status.Authority, "chainId": Quantity(status.ChainID), "height": Quantity(status.Height), "blockHash": status.BlockHash, "stateRoot": status.StateRoot, "pending": Quantity(status.Pending), "queued": Quantity(status.Queued)}, nil
		}
	case "chain_getBalance", "chain_getNonce", "chain_getPendingNonce", "chain_getAccount":
		address, ok := argument(params, "address")
		if !ok || !crypto.ValidAddress(address) {
			return nil, invalid
		}
		if method == "chain_getPendingNonce" {
			nonce, err := s.node.PendingNonce(address)
			return Quantity(nonce), backend(err)
		}
		account, err := s.node.Account(address)
		if err != nil {
			return nil, backend(err)
		}
		if method == "chain_getBalance" {
			return Quantity(account.Balance), nil
		}
		if method == "chain_getNonce" {
			return Quantity(account.Nonce), nil
		}
		return map[string]any{"address": address, "balance": Quantity(account.Balance), "nonce": Quantity(account.Nonce)}, nil
	case "chain_getBlock", "chain_getBlockHeader", "chain_getCommitCertificate":
		height, ok := argument(params, "height")
		if !ok {
			return nil, invalid
		}
		n := uint64(math.MaxUint64)
		if height != "latest" {
			value, err := ParseQuantity(height)
			if err != nil || value == math.MaxUint64 {
				return nil, invalid
			}
			n = value
		}
		if method == "chain_getCommitCertificate" {
			certificate, err := s.node.CommitCertificate(n)
			return certificate, backend(err)
		}
		if method == "chain_getBlockHeader" {
			block, err := s.node.Header(n)
			return headerView(block), backend(err)
		}
		block, err := s.node.Block(n)
		return blockView(block), backend(err)
	case "chain_sendRawTransaction":
		raw, ok := argument(params, "raw")
		if !ok {
			return nil, invalid
		}
		tx, err := DecodeRawTransaction(raw)
		if err != nil {
			return nil, &Error{Code: -32602, Message: err.Error()}
		}
		hash, err := s.node.Submit(tx)
		if err != nil {
			return nil, backend(err)
		}
		return hash, nil
	case "chain_getTransaction", "chain_getTransactionReceipt", "chain_getTransactionProof":
		text, ok := argument(params, "hash")
		var hash crypto.Hash
		if !ok || hash.UnmarshalText([]byte(text)) != nil {
			return nil, invalid
		}
		if method == "chain_getTransactionProof" {
			proof, err := s.node.Proof(hash)
			if err != nil {
				return nil, backend(err)
			}
			if proof == nil {
				return nil, nil
			}
			return map[string]any{"transactionHash": hash, "blockHash": proof.Receipt.BlockHash, "blockHeight": Quantity(proof.Receipt.BlockHeight), "root": proof.Root, "proof": map[string]any{"index": Quantity(proof.Proof.Index), "total": Quantity(proof.Proof.Total), "siblings": proof.Proof.Siblings}}, nil
		}
		if method == "chain_getTransactionReceipt" {
			receipt, err := s.node.Receipt(hash)
			return receiptView(receipt), backend(err)
		}
		info, err := s.node.Transaction(hash)
		if err != nil {
			return nil, backend(err)
		}
		if info == nil {
			return nil, nil
		}
		status := "pending"
		if info.Receipt != nil {
			status = "committed"
		}
		return map[string]any{"hash": hash, "transaction": wireTransaction(info.Transaction), "status": status, "receipt": receiptView(info.Receipt)}, nil
	default:
		return nil, &Error{Code: -32601, Message: "Method not found"}
	}
}
