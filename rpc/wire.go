package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gochain/blockchain"
	"gochain/node"
)

// Quantity is a decimal JSON string, preserving all uint64 values in JS clients.
type Quantity uint64

func (q Quantity) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(q), 10))
}
func (q *Quantity) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("quantity must be a decimal string")
	}
	n, err := ParseQuantity(value)
	if err != nil {
		return err
	}
	*q = Quantity(n)
	return nil
}
func ParseQuantity(value string) (uint64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, fmt.Errorf("invalid decimal quantity")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid decimal quantity")
		}
	}
	return strconv.ParseUint(value, 10, 64)
}

type WireTransaction struct {
	ChainID   Quantity `json:"chainId"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Amount    Quantity `json:"amount"`
	Nonce     Quantity `json:"nonce"`
	GasLimit  Quantity `json:"gasLimit"`
	GasPrice  Quantity `json:"gasPrice"`
	PublicKey []byte   `json:"publicKey"`
	Signature []byte   `json:"signature"`
}

func wireTransaction(tx blockchain.Transaction) WireTransaction {
	return WireTransaction{ChainID: Quantity(tx.ChainID), From: tx.From, To: tx.To, Amount: Quantity(tx.Amount), Nonce: Quantity(tx.Nonce), GasLimit: Quantity(tx.GasLimit), GasPrice: Quantity(tx.GasPrice), PublicKey: tx.PublicKey, Signature: tx.Signature}
}

// Raw transactions are hex-encoded UTF-8 JSON of WireTransaction, not Ethereum RLP.
func EncodeRawTransaction(tx blockchain.Transaction) (string, error) {
	if err := tx.Validate(tx.ChainID); err != nil {
		return "", err
	}
	b, err := json.Marshal(wireTransaction(tx))
	return "0x" + hex.EncodeToString(b), err
}
func DecodeRawTransaction(raw string) (blockchain.Transaction, error) {
	if !strings.HasPrefix(raw, "0x") || len(raw) > 16*1024 {
		return blockchain.Transaction{}, fmt.Errorf("invalid raw transaction encoding or size")
	}
	b, err := hex.DecodeString(raw[2:])
	if err != nil {
		return blockchain.Transaction{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var w WireTransaction
	if err := dec.Decode(&w); err != nil {
		return blockchain.Transaction{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return blockchain.Transaction{}, fmt.Errorf("trailing transaction data")
	}
	if len(w.PublicKey) != 33 || len(w.Signature) > 72 {
		return blockchain.Transaction{}, fmt.Errorf("invalid key or signature size")
	}
	tx := blockchain.Transaction{ChainID: uint64(w.ChainID), From: w.From, To: w.To, Amount: uint64(w.Amount), Nonce: uint64(w.Nonce), GasLimit: uint64(w.GasLimit), GasPrice: uint64(w.GasPrice), PublicKey: w.PublicKey, Signature: w.Signature}
	return tx, tx.Validate(tx.ChainID)
}

func blockView(b *blockchain.Block) any {
	if b == nil {
		return nil
	}
	txs := make([]any, len(b.Transactions))
	for i, tx := range b.Transactions {
		txs[i] = map[string]any{"hash": tx.Hash(), "transaction": wireTransaction(tx)}
	}
	return map[string]any{"hash": b.Hash(), "header": map[string]any{
		"chainId": Quantity(b.Header.ChainID), "height": Quantity(b.Header.Height), "parentHash": b.Header.ParentHash,
		"txRoot": b.Header.TxRoot, "stateRoot": b.Header.StateRoot, "timestamp": strconv.FormatInt(b.Header.Timestamp, 10),
	}, "transactions": txs}
}

func headerView(b *blockchain.Block) any {
	if b == nil {
		return nil
	}
	return map[string]any{"hash": b.Hash(), "header": map[string]any{
		"chainId": Quantity(b.Header.ChainID), "height": Quantity(b.Header.Height), "parentHash": b.Header.ParentHash,
		"txRoot": b.Header.TxRoot, "stateRoot": b.Header.StateRoot, "timestamp": strconv.FormatInt(b.Header.Timestamp, 10),
	}}
}
func receiptView(r *node.Receipt) any {
	if r == nil {
		return nil
	}
	scope := r.ConfirmationScope
	if scope == "" {
		scope = "local-durable"
	}
	return map[string]any{"transactionHash": r.TransactionHash, "blockHash": r.BlockHash, "blockHeight": Quantity(r.BlockHeight), "transactionIndex": Quantity(r.TransactionIndex), "gasUsed": Quantity(r.GasUsed), "fee": Quantity(r.Fee), "status": "committed", "confirmationScope": scope}
}
