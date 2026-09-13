package blockchain

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"gochain/crypto"
)

const TransferGas uint64 = 21_000

type Transaction struct {
	ChainID   uint64 `json:"chainId"`
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    uint64 `json:"amount"`
	Nonce     uint64 `json:"nonce"`
	GasLimit  uint64 `json:"gasLimit"`
	GasPrice  uint64 `json:"gasPrice"`
	PublicKey []byte `json:"publicKey"`
	Signature []byte `json:"signature"`
}

// Hash uses a versioned domain and fixed-width binary fields, never JSON.
// Validation must precede using a hash as the identity of untrusted input.
func (tx Transaction) Hash() crypto.Hash {
	var b bytes.Buffer
	b.WriteString("gochain/tx/v1")
	for _, value := range []uint64{tx.ChainID, tx.Amount, tx.Nonce, tx.GasLimit, tx.GasPrice} {
		_ = binary.Write(&b, binary.BigEndian, value)
	}
	for _, address := range []string{tx.From, tx.To} {
		if crypto.ValidAddress(address) {
			raw, _ := hex.DecodeString(address[2:])
			b.Write(raw)
		}
	}
	return crypto.Sum(b.Bytes())
}

func (tx Transaction) Clone() Transaction {
	tx.PublicKey = append([]byte(nil), tx.PublicKey...)
	tx.Signature = append([]byte(nil), tx.Signature...)
	return tx
}

func (tx Transaction) Sign(key *crypto.PrivateKey) (Transaction, error) {
	if key == nil {
		return Transaction{}, fmt.Errorf("missing private key")
	}
	tx.PublicKey = key.PubKey().SerializeCompressed()
	tx.From, _ = crypto.Address(tx.PublicKey)
	tx.Signature = crypto.Sign(key, tx.Hash())
	return tx, tx.Validate(tx.ChainID)
}

func (tx Transaction) Fee() (uint64, error) {
	if tx.GasLimit < TransferGas {
		return 0, fmt.Errorf("gas limit below %d", TransferGas)
	}
	if tx.GasPrice > math.MaxUint64/TransferGas {
		return 0, fmt.Errorf("fee overflow")
	}
	return TransferGas * tx.GasPrice, nil
}

func (tx Transaction) Cost() (uint64, error) {
	fee, err := tx.Fee()
	if err != nil {
		return 0, err
	}
	if tx.Amount > math.MaxUint64-fee {
		return 0, fmt.Errorf("transaction cost overflow")
	}
	return tx.Amount + fee, nil
}

func (tx Transaction) Validate(chainID uint64) error {
	if chainID == 0 || tx.ChainID != chainID {
		return fmt.Errorf("chain ID mismatch")
	}
	if !crypto.ValidAddress(tx.From) || !crypto.ValidAddress(tx.To) {
		return fmt.Errorf("invalid address")
	}
	if tx.Amount == 0 {
		return fmt.Errorf("amount must be positive")
	}
	if tx.Nonce == math.MaxUint64 {
		return fmt.Errorf("nonce exhausted")
	}
	if _, err := tx.Cost(); err != nil {
		return err
	}
	address, err := crypto.Address(tx.PublicKey)
	if err != nil || address != tx.From {
		return fmt.Errorf("sender does not match public key")
	}
	return crypto.Verify(tx.PublicKey, tx.Signature, tx.Hash())
}
