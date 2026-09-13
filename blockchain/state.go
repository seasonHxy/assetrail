package blockchain

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sort"

	"gochain/crypto"
)

type Account struct {
	Balance uint64 `json:"balance"`
	Nonce   uint64 `json:"nonce"` // Next transaction nonce, starting at zero.
}

// State is a mutable execution snapshot; callers must not share it across goroutines.
type State struct{ accounts map[string]Account }

func NewState(alloc map[string]uint64) (*State, error) {
	s := &State{accounts: make(map[string]Account, len(alloc))}
	for address, balance := range alloc {
		if !crypto.ValidAddress(address) {
			return nil, fmt.Errorf("invalid genesis address: %s", address)
		}
		// Empty accounts have one canonical representation: absence.
		if balance > 0 {
			s.accounts[address] = Account{Balance: balance}
		}
	}
	return s, nil
}
func (s *State) Account(address string) Account { return s.accounts[address] }
func (s *State) Clone() *State {
	copy := &State{accounts: make(map[string]Account, len(s.accounts))}
	for address, account := range s.accounts {
		copy.accounts[address] = account
	}
	return copy
}

// Apply validates everything before mutating either account. Fees are burned.
func (s *State) Apply(tx Transaction, chainID uint64) error {
	if err := tx.Validate(chainID); err != nil {
		return err
	}
	sender := s.Account(tx.From)
	if tx.Nonce != sender.Nonce {
		return fmt.Errorf("nonce: got %d, want %d", tx.Nonce, sender.Nonce)
	}
	cost, _ := tx.Cost()
	if sender.Balance < cost {
		return fmt.Errorf("insufficient balance")
	}
	sender.Balance -= cost
	sender.Nonce++
	if tx.From == tx.To {
		sender.Balance += tx.Amount
		s.accounts[tx.From] = sender
		return nil
	}
	receiver := s.Account(tx.To)
	if receiver.Balance > math.MaxUint64-tx.Amount {
		return fmt.Errorf("recipient balance overflow")
	}
	receiver.Balance += tx.Amount
	s.accounts[tx.From], s.accounts[tx.To] = sender, receiver
	return nil
}

func (s *State) Root() crypto.Hash {
	addresses := make([]string, 0, len(s.accounts))
	for address := range s.accounts {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	leaves := make([][]byte, 0, len(addresses))
	for _, address := range addresses {
		var b bytes.Buffer
		b.WriteString("gochain/account/v1")
		raw, _ := hex.DecodeString(address[2:])
		b.Write(raw)
		account := s.accounts[address]
		_ = binary.Write(&b, binary.BigEndian, account.Balance)
		_ = binary.Write(&b, binary.BigEndian, account.Nonce)
		leaves = append(leaves, b.Bytes())
	}
	return crypto.MerkleRoot(leaves)
}
