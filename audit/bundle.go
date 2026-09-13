// Package audit verifies signed transaction inclusion against an independently
// trusted block hash. It has no storage, RPC or network dependency.
package audit

import (
	"encoding/json"
	"fmt"
	"io"

	"gochain/blockchain"
	"gochain/crypto"
)

const Version = "settlelab.audit.v1"
const MaxBundleBytes = 16 << 10

// All integer fields use decimal strings so evidence can travel through JS
// tooling without silently rounding monetary values or nonces.
type Transfer struct {
	ChainID   uint64 `json:"chainId,string"`
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    uint64 `json:"amount,string"`
	Nonce     uint64 `json:"nonce,string"`
	GasLimit  uint64 `json:"gasLimit,string"`
	GasPrice  uint64 `json:"gasPrice,string"`
	PublicKey []byte `json:"publicKey"`
	Signature []byte `json:"signature"`
}
type Header struct {
	ChainID    uint64      `json:"chainId,string"`
	Height     uint64      `json:"height,string"`
	ParentHash crypto.Hash `json:"parentHash"`
	TxRoot     crypto.Hash `json:"txRoot"`
	StateRoot  crypto.Hash `json:"stateRoot"`
	Timestamp  int64       `json:"timestamp,string"`
}
type Proof struct {
	Index    uint64        `json:"index,string"`
	Total    uint64        `json:"total,string"`
	Siblings []crypto.Hash `json:"siblings"`
}
type Bundle struct {
	Version     string   `json:"version"`
	Transaction Transfer `json:"transaction"`
	Header      Header   `json:"header"`
	Proof       Proof    `json:"proof"`
}
type Result struct {
	Valid           bool        `json:"valid"`
	Scope           string      `json:"scope"`
	BlockHash       crypto.Hash `json:"blockHash"`
	TransactionHash crypto.Hash `json:"transactionHash"`
	BlockHeight     uint64      `json:"blockHeight,string"`
	ChainID         uint64      `json:"chainId,string"`
	From            string      `json:"from"`
	To              string      `json:"to"`
	Amount          uint64      `json:"amount,string"`
	Nonce           uint64      `json:"nonce,string"`
}

func (t Transfer) Core() blockchain.Transaction {
	return blockchain.Transaction{ChainID: t.ChainID, From: t.From, To: t.To, Amount: t.Amount, Nonce: t.Nonce, GasLimit: t.GasLimit, GasPrice: t.GasPrice, PublicKey: append([]byte(nil), t.PublicKey...), Signature: append([]byte(nil), t.Signature...)}
}
func (h Header) Core() blockchain.BlockHeader {
	return blockchain.BlockHeader{ChainID: h.ChainID, Height: h.Height, ParentHash: h.ParentHash, TxRoot: h.TxRoot, StateRoot: h.StateRoot, Timestamp: h.Timestamp}
}
func New(tx blockchain.Transaction, h blockchain.BlockHeader, p crypto.MerkleProof) Bundle {
	return Bundle{Version: Version,
		Transaction: Transfer{ChainID: tx.ChainID, From: tx.From, To: tx.To, Amount: tx.Amount, Nonce: tx.Nonce, GasLimit: tx.GasLimit, GasPrice: tx.GasPrice, PublicKey: append([]byte(nil), tx.PublicKey...), Signature: append([]byte(nil), tx.Signature...)},
		Header:      Header{ChainID: h.ChainID, Height: h.Height, ParentHash: h.ParentHash, TxRoot: h.TxRoot, StateRoot: h.StateRoot, Timestamp: h.Timestamp},
		Proof:       Proof{Index: p.Index, Total: p.Total, Siblings: append([]crypto.Hash(nil), p.Siblings...)},
	}
}

func Decode(r io.Reader) (Bundle, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBundleBytes+1))
	if err != nil {
		return Bundle{}, err
	}
	if len(data) > MaxBundleBytes {
		return Bundle{}, fmt.Errorf("audit bundle too large")
	}
	var b Bundle
	// A strict decoder rejects unknown fields and extra JSON documents.
	if err := decodeStrict(data, &b); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// Verify proves authorization and inclusion only. It neither establishes that
// trustedBlockHash is authentic nor proves consensus, balances or execution.
func Verify(b Bundle, trustedBlockHash crypto.Hash) (Result, error) {
	if trustedBlockHash == (crypto.Hash{}) {
		return Result{}, fmt.Errorf("an independently trusted block hash is required")
	}
	if b.Version != Version {
		return Result{}, fmt.Errorf("unsupported audit bundle version")
	}
	if b.Header.Height == 0 || b.Header.Timestamp <= 0 {
		return Result{}, fmt.Errorf("invalid transaction block header")
	}
	blockHash := (blockchain.Block{Header: b.Header.Core()}).Hash()
	if blockHash != trustedBlockHash {
		return Result{}, fmt.Errorf("bundle header does not match trusted block hash")
	}
	tx := b.Transaction.Core()
	if err := tx.Validate(b.Header.ChainID); err != nil {
		return Result{}, fmt.Errorf("transaction authorization: %w", err)
	}
	if b.Proof.Total == 0 || b.Proof.Total > blockchain.MaxBlockTransactions {
		return Result{}, fmt.Errorf("invalid transaction count")
	}
	hash := tx.Hash()
	p := crypto.MerkleProof{Index: b.Proof.Index, Total: b.Proof.Total, Siblings: b.Proof.Siblings}
	if !crypto.VerifyProof(hash[:], p, b.Header.TxRoot) {
		return Result{}, fmt.Errorf("invalid transaction inclusion proof")
	}
	return Result{Valid: true, Scope: "signed-transaction-inclusion", BlockHash: blockHash, TransactionHash: hash, BlockHeight: b.Header.Height, ChainID: tx.ChainID, From: tx.From, To: tx.To, Amount: tx.Amount, Nonce: tx.Nonce}, nil
}

func Encode(w io.Writer, b Bundle) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(b)
}
