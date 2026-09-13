package blockchain

import (
	"encoding/json"
	"math"
	"sync"
	"testing"

	"gochain/crypto"
)

func testKey(t *testing.T) (*crypto.PrivateKey, string) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	address, err := crypto.Address(key.PubKey().SerializeCompressed())
	if err != nil {
		t.Fatal(err)
	}
	return key, address
}
func signed(t *testing.T, key *crypto.PrivateKey, to string, amount, nonce, price uint64) Transaction {
	t.Helper()
	tx, err := (Transaction{ChainID: 9001, To: to, Amount: amount, Nonce: nonce, GasLimit: TransferGas, GasPrice: price}).Sign(key)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func newChain(t *testing.T, alloc map[string]uint64) *Chain {
	t.Helper()
	c, err := NewChain(9001, alloc)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTransactionTampering(t *testing.T) {
	key, _ := testKey(t)
	_, to := testKey(t)
	other, otherAddress := testKey(t)
	tx := signed(t, key, to, 20, 0, 0)
	if err := tx.Validate(9001); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Transaction){
		"amount": func(tx *Transaction) { tx.Amount++ }, "nonce": func(tx *Transaction) { tx.Nonce++ },
		"recipient": func(tx *Transaction) { tx.To = otherAddress }, "sender": func(tx *Transaction) { tx.From = otherAddress },
		"chain": func(tx *Transaction) { tx.ChainID++ }, "gasLimit": func(tx *Transaction) { tx.GasLimit++ },
		"gasPrice": func(tx *Transaction) { tx.GasPrice++ }, "signature": func(tx *Transaction) { tx.Signature[0] ^= 1 },
		"key": func(tx *Transaction) { tx.PublicKey = other.PubKey().SerializeCompressed() },
	} {
		t.Run(name, func(t *testing.T) {
			bad := tx.Clone()
			mutate(&bad)
			if bad.Validate(9001) == nil {
				t.Fatal("accepted tampering")
			}
		})
	}
	if tx.Validate(9002) == nil {
		t.Fatal("cross-chain replay accepted")
	}
	data, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Transaction
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Hash() != tx.Hash() || decoded.Validate(9001) != nil {
		t.Fatal("JSON roundtrip changed transaction")
	}
}

func TestStateExecutionAndFees(t *testing.T) {
	key, alice := testKey(t)
	_, bob := testKey(t)
	s, _ := NewState(map[string]uint64{alice: 100000})
	if err := s.Apply(signed(t, key, bob, 20, 0, 1), 9001); err != nil {
		t.Fatal(err)
	}
	if s.Account(alice) != (Account{Balance: 78980, Nonce: 1}) || s.Account(bob).Balance != 20 {
		t.Fatal("incorrect transfer or fee")
	}
	root := s.Root()
	if s.Apply(signed(t, key, bob, 20, 0, 1), 9001) == nil || root != s.Root() {
		t.Fatal("replay changed state")
	}
	if err := s.Apply(signed(t, key, alice, 10, 1, 1), 9001); err != nil {
		t.Fatal(err)
	}
	if s.Account(alice) != (Account{Balance: 57980, Nonce: 2}) {
		t.Fatal("self transfer should only burn fee")
	}
}

func TestInvalidExecutionIsAtomic(t *testing.T) {
	key, alice := testKey(t)
	_, bob := testKey(t)
	for _, tc := range []struct {
		name                              string
		balance, recipient, amount, nonce uint64
	}{
		{"insufficient", 5, 0, 6, 0}, {"nonce gap", 100, 0, 1, 2}, {"recipient overflow", 100, math.MaxUint64, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := NewState(map[string]uint64{alice: tc.balance, bob: tc.recipient})
			root := s.Root()
			if s.Apply(signed(t, key, bob, tc.amount, tc.nonce, 0), 9001) == nil {
				t.Fatal("expected rejection")
			}
			if s.Root() != root {
				t.Fatal("failed execution mutated state")
			}
		})
	}
	tx := signed(t, key, bob, 1, 0, 0)
	for _, mutate := range []func(*Transaction){
		func(tx *Transaction) { tx.GasPrice = math.MaxUint64 },
		func(tx *Transaction) { tx.Amount = math.MaxUint64; tx.GasPrice = 1 },
		func(tx *Transaction) { tx.Nonce = math.MaxUint64 },
		func(tx *Transaction) { tx.GasLimit = TransferGas - 1 },
		func(tx *Transaction) { tx.Amount = 0 },
	} {
		bad := tx.Clone()
		mutate(&bad)
		if _, err := bad.Sign(key); err == nil {
			t.Fatal("accepted invalid numeric field")
		}
	}
}

func TestRootDeterminismAndIsolation(t *testing.T) {
	key, alice := testKey(t)
	_, bob := testKey(t)
	a, _ := NewState(map[string]uint64{alice: 100, bob: 20})
	b, _ := NewState(map[string]uint64{bob: 20, alice: 100})
	if a.Root() != b.Root() {
		t.Fatal("root depends on insertion order")
	}
	empty, _ := NewState(nil)
	zero, _ := NewState(map[string]uint64{alice: 0})
	if empty.Root() != zero.Root() {
		t.Fatal("zero genesis account has distinct root")
	}
	clone := a.Clone()
	if err := clone.Apply(signed(t, key, bob, 1, 0, 0), 9001); err != nil {
		t.Fatal(err)
	}
	if a.Root() != b.Root() {
		t.Fatal("snapshot aliases source")
	}
}

func TestBlockLifecycle(t *testing.T) {
	key, alice := testKey(t)
	_, bob := testKey(t)
	chain := newChain(t, map[string]uint64{alice: 100})
	initial := chain.Snapshot().Root()
	tx := signed(t, key, bob, 20, 0, 0)
	block, err := chain.BuildBlock([]Transaction{tx}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if chain.Snapshot().Root() != initial || chain.Tip().Header.Height != 0 {
		t.Fatal("building changed chain")
	}
	for name, mutate := range map[string]func(*Block){
		"state root":  func(b *Block) { b.Header.StateRoot[0] ^= 1 },
		"tx root":     func(b *Block) { b.Header.TxRoot[0] ^= 1 },
		"parent":      func(b *Block) { b.Header.ParentHash[0] ^= 1 },
		"height":      func(b *Block) { b.Header.Height++ },
		"chain":       func(b *Block) { b.Header.ChainID++ },
		"time":        func(b *Block) { b.Header.Timestamp = 0 },
		"signature":   func(b *Block) { b.Transactions[0].Signature[0] ^= 1 },
		"transaction": func(b *Block) { b.Transactions[0].Amount++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := block.Clone()
			mutate(&bad)
			if chain.Append(bad) == nil {
				t.Fatal("accepted invalid block")
			}
			if chain.Snapshot().Root() != initial || chain.Tip().Header.Height != 0 {
				t.Fatal("invalid block changed chain")
			}
		})
	}
	if err := chain.Append(block); err != nil {
		t.Fatal(err)
	}
	if chain.Snapshot().Account(alice).Balance != 80 || chain.Snapshot().Account(bob).Balance != 20 {
		t.Fatal("wrong balances")
	}
	if chain.Append(block) == nil {
		t.Fatal("accepted duplicate block")
	}
	block.Transactions[0].Signature[0] ^= 1
	if chain.Tip().Transactions[0].Validate(9001) != nil {
		t.Fatal("appended block aliases caller")
	}
	tip := chain.Tip()
	tip.Transactions[0].Signature[0] ^= 1
	if chain.Tip().Transactions[0].Validate(9001) != nil {
		t.Fatal("tip aliases internal block")
	}
}

func TestWholeBlockRollbackAndIndependentReplay(t *testing.T) {
	key, alice := testKey(t)
	_, bob := testKey(t)
	chain := newChain(t, map[string]uint64{alice: 100})
	root := chain.Snapshot().Root()
	txs := []Transaction{signed(t, key, bob, 20, 0, 0), signed(t, key, bob, 90, 1, 0)}
	if _, err := chain.BuildBlock(txs, 1); err == nil {
		t.Fatal("overspend in candidate accepted")
	}
	bad := Block{Header: BlockHeader{ChainID: 9001, Height: 1, ParentHash: chain.Tip().Hash(), Timestamp: 1, TxRoot: crypto.MerkleRoot(TransactionLeaves(txs))}, Transactions: txs}
	if chain.Append(bad) == nil || chain.Snapshot().Root() != root {
		t.Fatal("partial block execution escaped")
	}
	other := newChain(t, map[string]uint64{alice: 100})
	for i := uint64(0); i < 5; i++ {
		block, err := chain.BuildBlock([]Transaction{signed(t, key, bob, 10, i, 0)}, int64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Block
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if err := chain.Append(block); err != nil {
			t.Fatal(err)
		}
		if err := other.Append(decoded); err != nil {
			t.Fatal(err)
		}
		if chain.Tip().Hash() != other.Tip().Hash() || chain.Snapshot().Root() != other.Snapshot().Root() {
			t.Fatal("replay diverged")
		}
	}
}

func TestConcurrentChainAccess(t *testing.T) {
	key, alice := testKey(t)
	_, bob := testKey(t)
	chain := newChain(t, map[string]uint64{alice: 100})
	block, err := chain.BuildBlock([]Transaction{signed(t, key, bob, 20, 0, 0)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				chain.Tip()
				chain.Snapshot()
			}
		}()
	}
	if err := chain.Append(block); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}
