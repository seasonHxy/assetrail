package blockchain

import (
	"bytes"
	"encoding/binary"

	"gochain/crypto"
)

type BlockHeader struct {
	ChainID    uint64      `json:"chainId"`
	Height     uint64      `json:"height"`
	ParentHash crypto.Hash `json:"parentHash"`
	TxRoot     crypto.Hash `json:"txRoot"`
	StateRoot  crypto.Hash `json:"stateRoot"`
	Timestamp  int64       `json:"timestamp"` // Unix seconds, strictly increasing after genesis.
}

type Block struct {
	Header       BlockHeader   `json:"header"`
	Transactions []Transaction `json:"transactions"`
}

func (b Block) Hash() crypto.Hash {
	var buf bytes.Buffer
	buf.WriteString("gochain/block/v1")
	_ = binary.Write(&buf, binary.BigEndian, b.Header.ChainID)
	_ = binary.Write(&buf, binary.BigEndian, b.Header.Height)
	_ = binary.Write(&buf, binary.BigEndian, b.Header.Timestamp)
	buf.Write(b.Header.ParentHash[:])
	buf.Write(b.Header.TxRoot[:])
	buf.Write(b.Header.StateRoot[:])
	return crypto.Sum(buf.Bytes())
}

func (b Block) Clone() Block {
	txs := make([]Transaction, len(b.Transactions))
	for i, tx := range b.Transactions {
		txs[i] = tx.Clone()
	}
	b.Transactions = txs
	return b
}

func TransactionLeaves(txs []Transaction) [][]byte {
	leaves := make([][]byte, len(txs))
	for i, tx := range txs {
		hash := tx.Hash()
		leaves[i] = append([]byte(nil), hash[:]...)
	}
	return leaves
}
