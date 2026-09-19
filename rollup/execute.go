package rollup

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"gochain/crypto"
)

var halfOrder, _ = hex.DecodeString("7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0")

type Deposit struct {
	Account uint8    `json:"account"`
	Asset   uint8    `json:"asset"`
	Amount  Quantity `json:"amount"`
}
type Batch struct {
	Config       Config      `json:"config"`
	Previous     State       `json:"previous"`
	Sequence     Quantity    `json:"sequence"`
	DepositStart Quantity    `json:"depositStart"`
	Deposits     []Deposit   `json:"deposits"`
	Operations   []Operation `json:"operations"`
}
type Result struct {
	PreRoot    crypto.Hash `json:"preRoot"`
	PostRoot   crypto.Hash `json:"postRoot"`
	State      State       `json:"state"`
	Data       string      `json:"data"`
	DepositEnd Quantity    `json:"depositEnd"`
	Sequence   Quantity    `json:"sequence"`
}

func verify(c Config, o Operation, counter bool) error {
	h, err := o.Digest(c)
	if err != nil {
		return err
	}
	text, index := o.Signature, o.From
	if counter {
		text, index = o.CounterSignature, o.To
	}
	sig, err := decodeSig(text, false)
	if err != nil {
		return err
	}
	if (sig[64] != 27 && sig[64] != 28) || bytes.Equal(sig[32:64], make([]byte, 32)) || bytes.Compare(sig[32:64], halfOrder) > 0 {
		return fmt.Errorf("noncanonical signature")
	}
	compact := append([]byte{sig[64]}, sig[:64]...)
	pub, _, err := ecdsa.RecoverCompact(compact, h[:])
	if err != nil {
		return err
	}
	address := Keccak(pub.SerializeUncompressed()[1:])
	if "0x"+hex.EncodeToString(address[12:]) != c.Accounts[index] {
		return fmt.Errorf("invalid signer")
	}
	return nil
}
func add(target *Quantity, amount Quantity) error {
	if uint64(*target) > math.MaxUint64-uint64(amount) {
		return fmt.Errorf("balance overflow")
	}
	*target += amount
	return nil
}
func sub(target *Quantity, amount Quantity) error {
	if *target < amount {
		return fmt.Errorf("insufficient balance")
	}
	*target -= amount
	return nil
}
func apply(c Config, s *State, o Operation, sequence Quantity) error {
	if o.Kind > WithdrawRWA || int(o.From) >= len(c.Accounts) || int(o.To) >= len(c.Accounts) || o.Amount == 0 {
		return fmt.Errorf("invalid operation")
	}
	if sequence == 0 || o.Sequence != sequence || o.Nonce != s.Nonce[o.From] {
		return fmt.Errorf("sequence/nonce mismatch")
	}
	if err := verify(c, o, false); err != nil {
		return err
	}
	if err := add(&s.Nonce[o.From], 1); err != nil {
		return err
	}
	if o.Kind == DvP {
		if o.From == o.To || o.Price == 0 || o.CounterNonce != s.Nonce[o.To] {
			return fmt.Errorf("invalid DvP")
		}
		if err := verify(c, o, true); err != nil {
			return err
		}
		if err := add(&s.Nonce[o.To], 1); err != nil {
			return err
		}
		if err := sub(&s.RWA[o.From], o.Amount); err != nil {
			return err
		}
		if err := add(&s.RWA[o.To], o.Amount); err != nil {
			return err
		}
		if err := sub(&s.Cash[o.To], o.Price); err != nil {
			return err
		}
		return add(&s.Cash[o.From], o.Price)
	}
	blank, err := decodeSig(o.CounterSignature, true)
	if err != nil || !bytes.Equal(blank, make([]byte, 65)) || o.Price != 0 || o.CounterNonce != 0 {
		return fmt.Errorf("nonzero unused fields")
	}
	if o.Kind >= WithdrawCash && o.To != o.From {
		return fmt.Errorf("withdrawal recipient mismatch")
	}
	switch o.Kind {
	case Pay:
		if err := sub(&s.Cash[o.From], o.Amount); err != nil {
			return err
		}
		return add(&s.Cash[o.To], o.Amount)
	case TransferRWA:
		if err := sub(&s.RWA[o.From], o.Amount); err != nil {
			return err
		}
		return add(&s.RWA[o.To], o.Amount)
	case WithdrawCash:
		return sub(&s.Cash[o.From], o.Amount)
	case WithdrawRWA:
		return sub(&s.RWA[o.From], o.Amount)
	}
	return fmt.Errorf("unknown operation")
}

// Execute is all-or-nothing: no externally visible state is mutated on failure.
// Deposits must come from canonical L1 events, never from an untrusted sequencer.
func Execute(b Batch) (Result, error) {
	if err := b.Config.Validate(); err != nil {
		return Result{}, err
	}
	if b.Sequence == 0 || len(b.Deposits) > MaxOperations || len(b.Operations) > MaxOperations || len(b.Deposits)+len(b.Operations) == 0 {
		return Result{}, fmt.Errorf("invalid batch bounds")
	}
	if uint64(b.DepositStart) > math.MaxUint64-uint64(len(b.Deposits)) {
		return Result{}, fmt.Errorf("deposit cursor overflow")
	}
	s := b.Previous
	for i, d := range b.Deposits {
		if int(d.Account) >= len(b.Config.Accounts) || d.Asset > 1 || d.Amount == 0 {
			return Result{}, fmt.Errorf("invalid deposit %d", i)
		}
		target := &s.Cash[d.Account]
		if d.Asset == 1 {
			target = &s.RWA[d.Account]
		}
		if err := add(target, d.Amount); err != nil {
			return Result{}, err
		}
	}
	var raw []byte
	for i, o := range b.Operations {
		if err := apply(b.Config, &s, o, b.Sequence); err != nil {
			return Result{}, fmt.Errorf("operation %d: %w", i, err)
		}
		encoded, err := o.Encode()
		if err != nil {
			return Result{}, err
		}
		raw = append(raw, encoded...)
	}
	return Result{PreRoot: b.Previous.Root(), PostRoot: s.Root(), State: s, Data: "0x" + hex.EncodeToString(raw), DepositEnd: b.DepositStart + Quantity(len(b.Deposits)), Sequence: b.Sequence}, nil
}
